package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sbx"
)

const alicePKey = "u-0123456789abcdef0123456789abcdef"

// covers PD-01 PD-14 S3 — identify's partition headers (plans/partitions/02
// §6): X-XBin-Partition names the partition the caller acts in and
// X-XBin-Partition-Id its id for user partitions only; a call between
// unpartitioned ends carries neither; an F5 attribution reaches global as
// the person, with their level and a clamped role; and inbound values of
// every one of them never survive, whatever the decision.
func TestIdentifyPartition(t *testing.T) {
	px := &Proxy{UserLevel: func(uid, tile string) string { return "write" }}
	instance := auth.Principal{Component: "apps/agent", Via: "instance", Partition: "user:alice"}
	for _, c := range []struct {
		name                     string
		p                        auth.Principal
		d                        Decision
		part, id, user, level, r string
	}{
		{"unpartitioned", auth.Principal{Component: "apps/x", Via: "instance"}, Decision{Role: "reader"}, "", "", "", "", "reader"},
		{"a person on a partitioned tile", auth.Principal{UserID: "alice", Via: "session"},
			Decision{Role: "admin", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey}, "user:alice", alicePKey, "alice", "write", "admin"},
		{"alice's partition to an unpartitioned provider", instance,
			Decision{Role: "reader", CallerPartition: "user:alice", CallerPartitionID: alicePKey}, "user:alice", alicePKey, "", "", "reader"},
		{"the global instance", auth.Principal{Component: "apps/agent", Via: "instance"},
			Decision{Role: "reader", CallerPartition: "global", CallerPartitionID: "stray"}, "global", "", "", "", "reader"},
		{"F5 from alice's partition", instance,
			Decision{Role: "admin", Partition: "global", CallerPartition: "user:alice", CallerPartitionID: alicePKey,
				Attribute: &auth.Attribution{UserID: "alice", Level: "read", Role: "reader"}}, "user:alice", alicePKey, "alice", "read", "reader"},
	} {
		r := httptest.NewRequest("GET", "/api/apps/agent/runs", nil)
		for _, h := range []string{HeaderPartition, HeaderPartitionID, HeaderUser, HeaderUserLevel, HeaderRole} {
			r.Header.Set(h, "spoofed")
		}
		px.identify(r, c.p, c.d.Role, "apps/agent")
		px.identifyPartition(r, c.d)
		for h, want := range map[string]string{HeaderPartition: c.part, HeaderPartitionID: c.id, HeaderUser: c.user, HeaderUserLevel: c.level, HeaderRole: c.r} {
			if got := r.Header.Values(h); want == "" && len(got) != 0 || want != "" && (len(got) != 1 || got[0] != want) {
				t.Errorf("%s: %s = %q, want %q", c.name, h, got, want)
			}
		}
	}
}

// fakeParts is a PartitionRunner over one socket: it records what was
// asked and which holds are live.
type fakeParts struct {
	mu     sync.Mutex
	sock   string
	err    error
	asked  []string
	holds  map[string]int // "active"/"passive" → live holds
	events []string
	gens   []fakeGen // when set, what each EnsurePartition answers in turn (then sock)
}

var startNames = map[PartitionStart]string{StartInteractive: "interactive", StartBackground: "background", StartMail: "mail"}

func (f *fakeParts) EnsurePartition(_ context.Context, c *registry.Component, dep, part string, class PartitionStart) (PartitionGen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, fmt.Sprintf("%s %s %s %s", c.Path, dep, part, startNames[class]))
	if f.err != nil {
		return nil, f.err
	}
	if len(f.gens) > 0 { // what each call answers, in order (a swap)
		g := f.gens[0]
		f.gens = f.gens[1:]
		return g, nil
	}
	return fakeGen{sock: f.sock}, nil
}

func (f *fakeParts) TrackPartition(tile, dep, part string, passive bool) func() {
	kind := "active"
	if passive {
		kind = "passive"
	}
	f.mu.Lock()
	f.holds[kind]++
	f.events = append(f.events, "+"+kind)
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.holds[kind]--
			f.events = append(f.events, "-"+kind)
			f.mu.Unlock()
		})
	}
}

// partProxyWorld: apps/pg (user + global) and apps/pu (user), recorded
// partitioned by a stub mode store, a proxy whose Route answers what the
// test sets, and a backend on a unix socket echoing the partition headers
// (or streaming, at /stream).
func partProxyWorld(t *testing.T) (*Proxy, *fakeParts, *Decision) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"apps/pg/xbin.json": `{"runtime":"go","partition":["user","global"]}`,
		"apps/pu/xbin.json": `{"runtime":"go","partition":["user"]}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := &registry.Registry{Root: root, PartitionModes: func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Requested == nil {
			return registry.PartitionMode{}
		}
		return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: *a.Requested}
	}}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprint(w, "data: hi\n\n")
			return
		}
		fmt.Fprintf(w, "%s|%s|%s", r.Header.Get(HeaderPartition), r.Header.Get(HeaderPartitionID), r.Header.Get(HeaderUser))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	d := &Decision{}
	fp := &fakeParts{sock: sock, holds: map[string]int{}}
	px := &Proxy{Reg: reg, Partitions: fp, Hub: events.NewHub()}
	px.Route = func(auth.Principal, *registry.Component, string) Decision { return *d }
	return px, fp, d
}

// covers PD-18 PD-19 — ServeHTTP on a user partition (02 §7): the reached
// partition starts through the runner's EnsurePartition — interactive,
// background for a cron or bus delivery, mail for a mail doorbell — never
// EnsureDeployment (nil here: reaching it
// would panic); the backend sees the caller's partition; a text/event-
// stream answer turns the hold passive without a gap; an admission refusal
// is 503; and an xbind without a partition runner answers 503.
func TestPartitionProxy(t *testing.T) {
	px, fp, d := partProxyWorld(t)
	call := func(p auth.Principal, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(HeaderPartition, "user:mallory")
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, r)
		return rec
	}
	alice := auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame"}
	*d = Decision{Deployment: "main", Role: "admin", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey}
	if rec := call(alice, "/api/apps/pg/x"); rec.Code != 200 || rec.Body.String() != "user:alice|"+alicePKey+"|alice" {
		t.Errorf("alice's frame: %d %q", rec.Code, rec.Body.String())
	}
	d.Delivery = "cron"
	if rec := call(auth.Principal{Component: "xbin/cron", Via: "cron", Partition: "user:alice"}, "/api/apps/pg/tick"); rec.Code != 200 {
		t.Errorf("alice's cron: %d %q", rec.Code, rec.Body.String())
	}
	d.Delivery = "mail"
	if rec := call(auth.Principal{Component: "xbin/mail", Via: "mail", Partition: "user:alice"}, "/api/apps/pg/mail"); rec.Code != 200 {
		t.Errorf("alice's mail doorbell: %d %q", rec.Code, rec.Body.String())
	}
	d.Delivery = ""
	if rec := call(alice, "/api/apps/pg/stream"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "data: hi") {
		t.Errorf("the stream: %d %q", rec.Code, rec.Body.String())
	}
	fp.mu.Lock()
	asked, evs, holds := strings.Join(fp.asked, ","), strings.Join(fp.events, " "), fmt.Sprint(fp.holds)
	fp.mu.Unlock()
	if asked != "apps/pg main user:alice interactive,apps/pg main user:alice background,apps/pg main user:alice mail,apps/pg main user:alice interactive" {
		t.Errorf("EnsurePartition asked %s", asked)
	}
	if evs != "+active -active +active -active +active -active +active +passive -active -passive" || holds != "map[active:0 passive:0]" {
		t.Errorf("holds: %s (%s)", evs, holds)
	}

	fp.err = sbx.Refuse(errors.New("too many people's instances of apps/pg are running; try again shortly"))
	if rec := call(alice, "/api/apps/pg/x"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "too many people") {
		t.Errorf("an admission refusal: %d %q", rec.Code, rec.Body.String())
	}
	px.Partitions = nil
	if rec := call(alice, "/api/apps/pg/x"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "no partition runner") {
		t.Errorf("no partition runner: %d %q", rec.Code, rec.Body.String())
	}
}

// covers PD-21 — public ingress reaches a partitioned tile's global
// instance only (02 §7): a tile without one answers today's 503 before the
// runner is asked; one with it reaches the runner (a build failure's 502
// here); and the forwarded request never names a partition, a forged one
// included.
func TestIngressPartition(t *testing.T) {
	px, _, _ := partProxyWorld(t)
	px.Runner = runner.New(px.Reg.Root, nil, px.Hub, px.Reg)
	for _, c := range []struct {
		tile string
		code int
	}{{"apps/pu", http.StatusServiceUnavailable}, {"apps/pg", http.StatusBadGateway}} {
		r := httptest.NewRequest("GET", "http://shop.example.com/", nil)
		r.Header.Set(HeaderPartition, "user:alice")
		r.Header.Set(HeaderPartitionID, alicePKey)
		rec := httptest.NewRecorder()
		px.ForwardIngress(rec, r, ingress.Route{Component: c.tile, Slot: "web", Paths: []string{"/*"}, Source: "runtime", Host: "shop.example.com"}, false)
		if rec.Code != c.code {
			t.Errorf("%s: %d %q, want %d", c.tile, rec.Code, rec.Body.String(), c.code)
		}
		if r.Header.Get(HeaderPartition) != "" && c.code != http.StatusServiceUnavailable || r.Header.Get(HeaderPartitionID) != "" && c.code != http.StatusServiceUnavailable {
			t.Errorf("%s: the forwarded request names a partition: %q %q", c.tile, r.Header.Get(HeaderPartition), r.Header.Get(HeaderPartitionID))
		}
	}
}
