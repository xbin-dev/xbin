package runner

// covers P7 P23 T4 SC-INBOUND — the network half of the runner's deployment
// edges (07-runtime §10.4; 09-fabric §1, §5.7, §5.8):
// TestNonPrimaryNeverJoinsProviderRoster, TestNonPrimaryNetVerdictAtSpawn,
// and 09-fabric §10's TestTerminatorDoorPrimaryOnly, TestEdgeStreamDial and
// TestDialIntoPrimary. The isolated end-to-end versions are integration
// steps (TestInboundEdgesReachOnlyPrimary); these pin the runner's
// decisions and dials, with backends listening on the host (NetHost) where
// a netns would be.

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// wiredRunner is a runner whose spawn-time hooks answer every view with the
// primary's wiring: a provider roster, a splice, a lan-ingress leg, a
// terminator's door and a stream slot, and a relay egress policy.
func wiredRunner(t *testing.T, door string) (*Runner, *registry.Component) {
	t.Helper()
	root := t.TempDir()
	c := &registry.Component{Path: "apps/x", Dir: filepath.Join(root, "apps", "x"), Manifest: registry.Manifest{Runtime: "go"}}
	r := &Runner{Root: root, RunDir: filepath.Join(t.TempDir(), "run"), Isolate: true, Rootfs: "/nonexistent-rootfs",
		states: map[string]*state{}, netmux: newNetMux()}
	r.NetRoster = func(*registry.Component) []sandbox.NetClient {
		return []sandbox.NetClient{{Name: "apps/client", Addr: "10.42.0.1/30"}}
	}
	r.NetTarget = func(*registry.Component) (string, string, string, bool) {
		return "apps/prov", "10.9.0.2/30", "10.9.0.1", true
	}
	r.NetLinks = func(*registry.Component) []sandbox.NetLink {
		return []sandbox.NetLink{{Provider: "apps/lan", Slot: "lan", Addr: "10.43.0.2/30"}}
	}
	r.IngressNet = func(*registry.Component) bool { return true }
	r.IngressFwd = func(*registry.Component) map[int]string {
		return map[int]string{8642: "unix:" + door, 20000: "stream:apps/db:5432"}
	}
	rule, err := sandbox.ParseRule("net:internet:443")
	if err != nil {
		t.Fatal(err)
	}
	r.Egress = func(*registry.Component) sandbox.EgressPolicy {
		return sandbox.EgressPolicy{Rules: []sandbox.Rule{rule}}
	}
	return r, c
}

// covers P23 T4 — TestNonPrimaryNeverJoinsProviderRoster (06-security T4;
// 09-fabric §5.7–§5.8): whatever the hooks answer, a non-primary generation
// registers no net-provider roster, splices to no provider and takes no
// lan-ingress leg, and its launch spec asks for none of them, no host
// network and no splice; the primary's wiring is the hooks' answer, today's.
func TestNonPrimaryNeverJoinsProviderRoster(t *testing.T) {
	r, c := wiredRunner(t, "/door.sock")
	np := r.netPlanFor(c, "main", io.Discard)
	if len(np.clients) != 1 || !np.spliced || np.provider != "apps/prov" || len(np.links) != 1 || len(np.fwd) != 2 {
		t.Errorf("the primary's wiring: %+v, want the hooks' roster, splice, leg and forwards", np)
	}
	dev := nonPrimary(c, "dev")
	nd := r.netPlanFor(dev, "dev", io.Discard)
	if nd.clients != nil || nd.spliced || nd.provider != "" || nd.links != nil {
		t.Errorf("a non-primary generation's wiring: %+v, want no roster, splice or lan-ingress leg", nd)
	}
	spec := r.launchSpecWith(dev, "/bin/backend", filepath.Join(r.RunDir, "d"), nil, sandbox.EgressPolicy{}, "", nil, nil)
	if spec.NetClients != nil || spec.NetLinks != nil || spec.HostNet || spec.Net == "splice" {
		t.Errorf("a non-primary launch spec: clients %v, links %v, hostnet %v, net %q", spec.NetClients, spec.NetLinks, spec.HostNet, spec.Net)
	}
	if spec.Net != "" {
		t.Errorf("a non-primary spec without egress builds ingress plumbing: net %q", spec.Net)
	}
}

// covers P23 T4 — the net verdict applied at spawn (09-fabric §5.8): a
// non-primary generation of a tile whose net shares the host, or is spliced
// through a provider tile, gets no egress at all, even when the Egress hook
// answers a policy, and its log says why; otherwise it inherits the hook's
// relay policy; the primary's egress is the hook's, whatever its net.
func TestNonPrimaryNetVerdictAtSpawn(t *testing.T) {
	r, c := wiredRunner(t, "/door.sock")
	dev := nonPrimary(c, "dev")
	hostShare := false
	r.NetHost = func(*registry.Component) bool { return hostShare }

	pol, why := r.spawnEgress(dev) // spliced through apps/prov
	if !pol.Empty() || why != whySplice {
		t.Errorf("a provider-bound tile's non-primary: %v %q, want no egress, %q", pol.Strings(), why, whySplice)
	}
	hostShare = true
	if pol, why = r.spawnEgress(dev); !pol.Empty() || why != whyHostShare {
		t.Errorf("a host-sharing tile's non-primary: %v %q, want no egress, %q", pol.Strings(), why, whyHostShare)
	}
	if pol, why = r.spawnEgress(c); pol.Empty() || why != "" {
		t.Errorf("the host-sharing tile's primary: %v %q, want the hook's policy", pol.Strings(), why)
	}
	hostShare = false
	r.NetTarget = func(*registry.Component) (string, string, string, bool) { return "", "", "", false }
	if pol, why = r.spawnEgress(dev); !reflect.DeepEqual(pol.Strings(), []string{"net:internet:443"}) || why != "" {
		t.Errorf("a relay tile's non-primary: %v %q, want the tile's relay policy (inherit)", pol.Strings(), why)
	}
	var log bytes.Buffer
	logVerdict(&log, whyHostShare)
	logVerdict(&log, "")
	if log.String() != whyHostShare+"\n" {
		t.Errorf("the verdict's log line: %q", log.String())
	}
}

// covers P7 — 09-fabric §10's TestTerminatorDoorPrimaryOnly (§1): only the
// terminator's primary gets its forward door in its relay; a non-primary
// generation's relay keeps only its stream-slot forwards, a door dial from
// it is refused with one line in its log, and a generation that stopped
// being the primary loses the door at its next dial.
func TestTerminatorDoorPrimaryOnly(t *testing.T) {
	door := filepath.Join(t.TempDir(), "door.sock")
	ln, err := net.Listen("unix", door)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go acceptAll(ln)
	r, c := wiredRunner(t, door)
	primary := "main"
	var mu sync.Mutex
	r.Primary = func(string) string { mu.Lock(); defer mu.Unlock(); return primary }

	if got := r.ingressFwd(c); len(got) != 2 {
		t.Errorf("the primary's forwards: %v, want the door and the stream slot", got)
	}
	if got, want := r.ingressFwd(nonPrimary(c, "dev")), map[int]string{20000: "stream:apps/db:5432"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a non-primary's forwards: %v, want %v", got, want)
	}

	conn, err := r.hostDialFor("apps/x", "main", io.Discard)("unix:" + door)
	if err != nil {
		t.Fatalf("the primary's door: %v", err)
	}
	conn.Close()
	var devLog bytes.Buffer
	if _, err := r.hostDialFor("apps/x", "dev", &devLog)("unix:" + door); err == nil {
		t.Error("a non-primary generation dialled the door")
	}
	if got := devLog.String(); got != "the ingress forward door serves the tile's primary only\n" {
		t.Errorf("dev's log: %q", got)
	}

	mu.Lock()
	primary = "dev" // reassigned: main's generation drains until it restarts
	mu.Unlock()
	var mainLog bytes.Buffer
	if _, err := r.hostDialFor("apps/x", "main", &mainLog)("unix:" + door); err == nil || strings.Count(mainLog.String(), "\n") != 1 {
		t.Errorf("the former primary's door dial: %v, log %q; want refused, one line", err, mainLog.String())
	}
}

func acceptAll(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
	}
}

// hostListener is a TCP listener on the host's loopback, where a backend
// with host networking listens.
func hostListener(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go acceptAll(ln)
	return ln, ln.Addr().(*net.TCPAddr).Port
}

func activeOf(r *Runner, tile, dep string) int {
	s := r.existingStateOf(tile, dep)
	if s == nil {
		return -1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// covers P23 T4 — 09-fabric §10's TestEdgeStreamDial (§5.7): a stream slot's
// dial from the primary's relay reaches the provider's primary, through
// DialInto; the same dial from a non-primary generation's relay closes at
// once, with one line in that deployment's log naming the slot, and never
// wakes the provider.
func TestEdgeStreamDial(t *testing.T) {
	f, _ := newDepFake(t, "apps/x", "apps/db")
	f.set("apps/x", "dev", "worktree")
	f.r.NetHost = func(c *registry.Component) bool { return c.Path == "apps/db" }
	_, port := hostListener(t)
	slot := "stream:apps/db:" + strconv.Itoa(port)

	var devLog bytes.Buffer
	if _, err := f.r.hostDialFor("apps/x", "dev", &devLog)(slot); err == nil {
		t.Fatal("a non-primary generation dialled a stream slot")
	}
	want := "stream slot to apps/db:" + strconv.Itoa(port) + " blocked by edge policy (a non-primary deployment reaches no stream slot)\n"
	if got := devLog.String(); got != want {
		t.Errorf("dev's log: %q, want %q", got, want)
	}
	if got := f.takeLog(); len(got) != 0 || f.r.existingStateOf("apps/db", "main") != nil {
		t.Errorf("a refused dial woke the provider: %q", got)
	}

	conn, err := f.r.hostDialFor("apps/x", "main", io.Discard)(slot)
	if err != nil {
		t.Fatalf("the primary's stream slot: %v", err)
	}
	if got, want := f.takeLog(), []string{"build apps/db main@worktree", "start apps/db main g1 @worktree"}; !equalStrings(got, want) {
		t.Errorf("the primary's dial: %q, want the provider's primary started: %q", got, want)
	}
	if n := activeOf(f.r, "apps/db", "main"); n != 1 {
		t.Errorf("the provider's primary holds %d connections, want 1", n)
	}
	conn.Close()
	if n := activeOf(f.r, "apps/db", "main"); n != 0 {
		t.Errorf("after the close: %d connections", n)
	}
}

// covers P7 SC-INBOUND — 09-fabric §10's TestDialIntoPrimary (§1, F3):
// DialInto resolves the tile's primary once, ensures, tracks and dials that
// deployment's generation, and never touches another deployment's; after a
// reassignment it reaches the new primary.
func TestDialIntoPrimary(t *testing.T) {
	f, _ := newDepFake(t, "apps/x")
	f.set("apps/x", "dev", "worktree")
	f.r.NetHost = func(*registry.Component) bool { return true }
	_, port := hostListener(t)
	dial := func() net.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := f.r.DialInto(ctx, "apps/x", "tcp", port)
		if err != nil {
			t.Fatalf("DialInto: %v", err)
		}
		return conn
	}

	conn := dial()
	if got, want := f.takeLog(), []string{"build apps/x main@worktree", "start apps/x main g1 @worktree"}; !equalStrings(got, want) {
		t.Errorf("DialInto with main primary: %q, want %q", got, want)
	}
	if m, d := activeOf(f.r, "apps/x", "main"), activeOf(f.r, "apps/x", "dev"); m != 1 || d != -1 {
		t.Errorf("connections: main %d, dev %d; want 1 and no state", m, d)
	}
	conn.Close()

	f.setPrimary("apps/x", "dev")
	conn = dial()
	defer conn.Close()
	if got, want := f.takeLog(), []string{"build apps/x dev@worktree", "start apps/x dev g1 @worktree"}; !equalStrings(got, want) {
		t.Errorf("DialInto with dev primary: %q, want %q", got, want)
	}
	if m, d := activeOf(f.r, "apps/x", "main"), activeOf(f.r, "apps/x", "dev"); m != 0 || d != 1 {
		t.Errorf("connections: main %d, dev %d; want 0 and 1", m, d)
	}
}
