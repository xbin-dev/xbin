package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/proxy"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-18 — the merge guard of the proxy's partition runner: once the
// runner can start people's partitions (it has EnsurePartition),
// partitionRunnerOf must build the adapter, or every call reaching a user
// partition answers 503 "no partition runner".
func TestPartitionRunnerWired(t *testing.T) {
	if _, ok := reflect.TypeOf(&runner.Runner{}).MethodByName("EnsurePartition"); ok && partitionRunnerOf == nil {
		t.Error("runner.Runner has EnsurePartition but partitionRunnerOf is nil: set it (internal/boot/partitionroute.go's doc, plans/partitions/records/F2.md)")
	}
}

// covers PD-18 S13 — partitionStarts, the adapter: the proxy's start
// classes map onto the runner's; the runner's listed refusals become
// sbx.ErrRefused (503) keeping their text, any other error passes as it
// is; holds pass through; and registerPartitionInstance registers a
// person's partition token with their uid, nothing without one.
func TestPartitionStartsAdapter(t *testing.T) {
	errBusy, errOther := errors.New("caps reached"), errors.New("boom")
	var asked []string
	a := partitionStarts[string, testGen]{
		ensure: func(_ context.Context, c *registry.Component, dep, part, class string) (testGen, error) {
			asked = append(asked, c.Path+" "+dep+" "+part+" "+class)
			switch part {
			case "user:busy":
				return testGen{}, fmt.Errorf("apps/t: %w", errBusy)
			case "user:broken":
				return testGen{}, errOther
			}
			return testGen{"/sock"}, nil
		},
		track: func(tile, dep, part string, passive bool) func() {
			asked = append(asked, fmt.Sprintf("hold %s %s %s %v", tile, dep, part, passive))
			return func() {}
		},
		classes:     [3]string{proxy.StartInteractive: "i", proxy.StartBackground: "b", proxy.StartMail: "m"},
		unavailable: []error{errBusy},
	}
	c := &registry.Component{Path: "apps/t"}
	for class, want := range map[proxy.PartitionStart]string{proxy.StartInteractive: "i", proxy.StartBackground: "b", proxy.StartMail: "m"} {
		asked = nil
		if gen, err := a.EnsurePartition(context.Background(), c, "main", "user:alice", class); err != nil || gen.Sock() != "/sock" ||
			len(asked) != 1 || asked[0] != "apps/t main user:alice "+want {
			t.Errorf("class %d: %v, %v, asked %v", class, gen, err, asked)
		}
	}
	if gen, err := a.EnsurePartition(context.Background(), c, "main", "user:busy", proxy.StartInteractive); gen != nil || !errors.Is(err, sbx.ErrRefused) ||
		!errors.Is(err, errBusy) || err.Error() != "apps/t: caps reached" {
		t.Errorf("a listed refusal: %v", err)
	}
	if _, err := a.EnsurePartition(context.Background(), c, "main", "user:broken", proxy.StartInteractive); errors.Is(err, sbx.ErrRefused) || err != errOther {
		t.Errorf("another error: %v", err)
	}
	if _, err := a.EnsurePartition(context.Background(), c, "main", "user:alice", proxy.PartitionStart(9)); !errors.Is(err, sbx.ErrRefused) {
		t.Errorf("an unknown class: %v", err)
	}
	asked = nil
	a.TrackPartition("apps/t", "main", "user:alice", true)()
	if len(asked) != 1 || asked[0] != "hold apps/t main user:alice true" {
		t.Errorf("the hold: %v", asked)
	}

	root := t.TempDir()
	au, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	au.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })
	st, err := users.Open(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", "bob"} {
		if _, err := st.Upsert(users.User{ID: id, Role: users.RoleUser}, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	au.SetUsers(st)
	uid, err := st.EnsureUID("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &State{Auth: au, Users: st}
	s.registerPartitionInstance("tok-alice", "apps/t", "main", "user:alice", uid)
	s.registerPartitionInstance("tok-bob", "apps/t", "main", "user:bob", "") // no uid: nothing
	lookup := func(tok string) (auth.Principal, bool) {
		r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return au.FromRequest(r)
	}
	if p, ok := lookup("tok-alice"); !ok || p.Component != "apps/t" || p.Partition != "user:alice" || uid == "" {
		t.Errorf("alice's partition token: %+v %v", p, ok)
	}
	if p, ok := lookup("tok-bob"); ok {
		t.Errorf("bob's partition token registered without a uid: %+v", p)
	}
}

// covers PD-10 PD-29 S13 — identity and routing wired in a booted xbind
// (plans/partitions/02): on a partitioned tile (user + global), the owner
// token reaches the global instance, whose backend hears X-XBin-Partition:
// global; alice's document names her partition, her frame is refused the
// routes whose handlers don't act per partition yet and reaches the neutral
// ones, and her call to the tile's API reaches her partition, which this
// xbind's runner can't start yet (503); an instance token of her partition
// authenticates only while she lives with the same uid (401 once disabled).
func TestPartitionWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pa/xbin.json":         `{"runtime":"node","partition":["user","global"]}`,
		"apps/pa/backend/server.js": zsNodeServer,
		"apps/pa/index.html":        "<!doctype html><html><head></head><body>pa</body></html>\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	if _, err := d.st.Users.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/pa": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, hdr map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, d.url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	owner := map[string]string{"Authorization": "Bearer " + d.owner}
	code, body := do("GET", "/api/apps/pa/hello", owner)
	var echo struct{ Xbin map[string]string }
	_ = json.Unmarshal([]byte(body), &echo)
	if code != 200 || echo.Xbin["x-xbin-partition"] != "global" || echo.Xbin["x-xbin-partition-id"] != "" {
		t.Errorf("the owner token on apps/pa: %d %s", code, body)
	}
	aliceCookie := map[string]string{"Cookie": auth.CookieName + "=" + d.st.Auth.NewSession("alice", "127.0.0.1")}
	if code, body := do("GET", "/c/apps/pa/", aliceCookie); code != 200 || !strings.Contains(body, `<meta name="xbin-partition" content="user:alice">`) {
		t.Errorf("alice's document: %d %s", code, body)
	}
	frame := map[string]string{auth.FrameTokenHeader: d.st.Auth.MintFrameToken("apps/pa", "alice", time.Hour)}
	// a route still unconverted, whichever that is now (the packs that
	// convert them remove their rows): refused to her partition
	var unconverted []string
	for pat := range server.PartitionUnconverted() {
		if m, path, ok := strings.Cut(pat, " "); ok && m == "GET" && !strings.Contains(path, "{") {
			unconverted = append(unconverted, path)
		}
	}
	slices.Sort(unconverted)
	if len(unconverted) > 0 {
		if code, body := do("GET", "/api/xbin"+unconverted[0], frame); code != 403 || !strings.Contains(body, "isn't available to a partition's credentials yet") {
			t.Errorf("alice's frame on %s (not per partition yet): %d %s", unconverted[0], code, body)
		}
	} else {
		t.Log("every route is converted: the refusal of an unconverted one is internal/server's synthetic probe (TestPartitionGate)")
	}
	// tile-status from her partition's frame is her partition's (F7b)
	if code, body := do("GET", "/api/xbin/tile-status?component=apps/pa", frame); code != 200 || !strings.Contains(body, `"partition":"user:alice"`) {
		t.Errorf("alice's frame on tile-status: %d %s", code, body)
	}
	// her partition's own registrations (F5): none yet, not global's
	if code, body := do("GET", "/api/xbin/bus/subscriptions", frame); code != 200 || strings.TrimSpace(body) != `{"subscriptions":[]}` {
		t.Errorf("alice's frame on bus subscriptions: %d %s", code, body)
	}
	// kv acts on her partition (the data plane): past the gate, to the handler
	if code, body := do("GET", "/api/xbin/kv/res:apps/pa/db/k", frame); code != 404 || !strings.Contains(body, "no such kv resource") {
		t.Errorf("alice's frame on kv: %d %s", code, body)
	}
	if code, body := do("GET", "/api/xbin/whoami", frame); code != 200 {
		t.Errorf("alice's frame on whoami: %d %s", code, body)
	}
	// without the runner's side this xbind has no partition runner; with it
	// (not --isolate here) the runner refuses the start: 503 either way,
	// never a silent "no partition runner" once the runner can start one
	code, body = do("GET", "/api/apps/pa/hello", frame)
	if noRunner := strings.Contains(body, "no partition runner"); code != 503 || noRunner != (partitionRunnerOf == nil) {
		t.Errorf("alice's frame on the tile's API: %d %s", code, body)
	}
	// the runner's people's-partitions hooks this plane fills are installed
	// once the runner has them (records/F2.md)
	rv := reflect.ValueOf(d.st.Run).Elem()
	for _, hook := range []string{"PartitionIdent", "ShouldRunPartition", "RegisterPartitionInstance", "PartitionEvent", "PartitionExit"} {
		if f := rv.FieldByName(hook); f.IsValid() && f.Kind() == reflect.Func && f.IsNil() {
			t.Errorf("the runner's %s hook isn't installed (plans/partitions/records/F2.md, Seams)", hook)
		}
	}
	if u, _ := d.st.Users.Get("alice"); !users.UIDOK(u.UID) {
		t.Errorf("alice's first partition minted no uid: %q", u.UID)
	}

	// F7b's side is wired: the runner's rows and stops, the credential gate —
	// a link minted held (no hold names it here) waits through the booted
	// redemption, never redeems
	if !d.st.Broker.PartitionOpsWired() || !d.st.Broker.CredentialGateInstalled() {
		t.Errorf("the partition operations aren't wired: ops %v, credential gate %v", d.st.Broker.PartitionOpsWired(), d.st.Broker.CredentialGateInstalled())
	}
	tok, err := d.st.Users.CreateHeldInvite("alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(d.url+"/api/xbin/invite/redeem", "application/json", strings.NewReader(`{"invite":"`+tok+`","password":"newpassword1"}`))
	if err != nil {
		t.Fatal(err)
	}
	held, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 409 || !strings.Contains(string(held), "waiting for alice") {
		t.Errorf("a held link's redemption: %d %s", resp.StatusCode, held)
	}

	u, _ := d.st.Users.Get("alice")
	d.st.Auth.RegisterInstancePartition("tok-alice", "apps/pa", "", util.UserPartition("alice"), u.UID)
	inst := map[string]string{"Authorization": "Bearer tok-alice"}
	if code, body := do("GET", "/api/xbin/whoami", inst); code != 200 {
		t.Errorf("alice's partition's token: %d %s", code, body)
	}
	u.Disabled = true
	if _, err := d.st.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if code, body := do("GET", "/api/xbin/whoami", inst); code != 401 {
		t.Errorf("alice disabled, her partition's token: %d %s", code, body)
	}
}

// testGen is a generation of a person's partition for the adapter's test.
type testGen struct{ sock string }

func (g testGen) Sock() string  { return g.sock }
func (g testGen) Retired() bool { return false }
