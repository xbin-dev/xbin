package main

// manager_test.go — what the contract leaves to this manager: images built
// once and cloned, what hello offers (egress by the bound classes, images by
// the substrate's clones), quotas, runtime names kept inside, clientIds per
// consumer, surviving a restart, and the operators' routes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

// setConfig replaces the manager's config (as PUT /ops/config would).
func (tm *testManager) setConfig(t *testing.T, f func(*Config)) {
	t.Helper()
	cfg := tm.m.config()
	f(&cfg)
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if err := tm.st.putConfig(cfg); err != nil {
		t.Fatal(err)
	}
	tm.m.mu.Lock()
	tm.m.cfg = cfg
	tm.m.mu.Unlock()
	tm.m.forgetRuntime()
}

func hello(t *testing.T, c sandboxcontract.Caller) map[string]any {
	t.Helper()
	var h map[string]any
	c.Call("GET", "/hello", nil, 200, &h)
	return h
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", d, what)
		}
	}
}

func TestImages(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	tm.setConfig(t, func(c *Config) {
		c.Images = append(c.Images,
			Image{ID: "tools", Title: "With tools", Setup: `echo "built as $IMAGE_ID" > marker; echo setup-ran`},
			Image{ID: "broken", Setup: "echo nope; exit 3"},
			Image{ID: "slow", Setup: "sleep 1; echo slow > marker"})
	})
	a := tm.tg.As(t, "apps/img-a")
	if h := hello(t, a); len(h["images"].([]any)) != 4 {
		t.Fatalf("hello's images: %v", h["images"])
	}
	// the first sandbox of an image builds it (?wait lets the build finish)
	var sb sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "one", "image": "tools"}, 201, &sb)
	t.Cleanup(func() { _, _, _ = a.Do(context.Background(), "DELETE", "/sandboxes/"+sb.ID, nil) })
	if sb.State != "running" || sb.Image.ID != "tools" || sb.Image.Title != "With tools" {
		t.Fatalf("a sandbox of a built image: %+v", sb)
	}
	if got := a.Read(sb.ID, sb.Workdir+"/marker"); got != "built as tools\n" {
		t.Fatalf("the setup's work isn't in the clone: %q", got)
	}
	tm.m.mu.Lock()
	built := *tm.m.imgs["tools"]
	tm.m.mu.Unlock()
	if built.State != "ready" || built.Snapshot == "" || !strings.Contains(built.Log, "setup-ran") {
		t.Fatalf("the built image: %+v", built)
	}
	// the next one clones the snapshot: no build
	creates := tm.fb.Calls("create")
	two := a.Create(map[string]any{"name": "two", "image": "tools"})
	if tm.fb.Calls("create") != creates+1 || two.State != "running" || a.Read(two.ID, two.Workdir+"/marker") != "built as tools\n" {
		t.Fatalf("the second sandbox of an image: %+v (%d creates)", two, tm.fb.Calls("create")-creates)
	}
	// the template sandbox is nobody's
	if l := a.List(); len(l) != 2 {
		t.Fatalf("a lists %d sandboxes", len(l))
	}
	// a changed script rebuilds, and the old template goes
	tm.setConfig(t, func(c *Config) { c.Images[1].Setup = `echo v2 > marker` })
	var three sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "three", "image": "tools"}, 201, &three)
	if got := a.Read(three.ID, three.Workdir+"/marker"); got != "v2\n" {
		t.Fatalf("after a changed setup: %q", got)
	}
	eventually(t, 5*time.Second, "the old template sandbox is deleted", func() bool {
		_, err := tm.fb.Get(context.Background(), built.Runtime)
		return errors.Is(err, xbin.ErrSandboxNotFound)
	})
	// a failing setup: the sandbox is in error, saying why; it deletes fine
	var bad sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "bad", "image": "broken"}, 201, &bad)
	if bad.State != "error" || !strings.Contains(bad.StateDetail, "exited 3") || !strings.Contains(bad.StateDetail, "nope") {
		t.Fatalf("a sandbox of a broken image: %+v", bad)
	}
	a.Refused("POST", "/sandboxes/"+bad.ID+"/run", map[string]any{"cmd": "true"}, 409, "state")
	a.Call("DELETE", "/sandboxes/"+bad.ID, nil, 204, nil)
	// without ?wait the create answers "creating" while the image builds
	var slow sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes", map[string]any{"name": "slow", "image": "slow"}, 201, &slow)
	t.Cleanup(func() { _, _, _ = a.Do(context.Background(), "DELETE", "/sandboxes/"+slow.ID, nil) })
	if slow.State != "creating" {
		t.Fatalf("a create waiting on a build: %+v", slow)
	}
	a.Refused("POST", "/sandboxes/"+slow.ID+"/run", map[string]any{"cmd": "true"}, 409, "state")
	eventually(t, 20*time.Second, "the build finishes", func() bool { return a.Get(slow.ID).State == "running" })
	slow = a.Get(slow.ID) // (the fake puts the workdir where it likes once it has the sandbox)
	if got := a.Read(slow.ID, slow.Workdir+"/marker"); got != "slow\n" {
		t.Fatalf("the slow image: %q", got)
	}
	// a sandbox deleted while its image builds is gone for good
	tm.setConfig(t, func(c *Config) { c.Images[3].Setup = "sleep 1; echo slower > marker" })
	var gone sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes", map[string]any{"name": "gone", "image": "slow"}, 201, &gone)
	a.Call("DELETE", "/sandboxes/"+gone.ID, nil, 204, nil)
	a.Refused("GET", "/sandboxes/"+gone.ID, nil, 404, "not-found")
}

func TestOffer(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	tm.setConfig(t, func(c *Config) { c.Images = append(c.Images, Image{ID: "tools", Setup: "true"}) })
	a := tm.tg.As(t, "apps/offer-a")
	h := hello(t, a)
	if e := strs(h["egress"]); strings.Join(e, ",") != "none,internet,open" {
		t.Fatalf("both classes bound: %v", e)
	}
	// unbound classes: none only; a sandbox can't ask for more
	tm.fb.mu.Lock()
	tm.fb.Classes = map[string]string{"internet": "", "open": ""}
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	if e := strs(hello(t, a)["egress"]); strings.Join(e, ",") != "none" {
		t.Fatalf("no class bound: %v", e)
	}
	a.Refused("POST", "/sandboxes", map[string]any{"name": "x", "egress": "internet"}, 400, "invalid")
	// the internet class bound to more than the internet isn't "internet"
	tm.fb.mu.Lock()
	tm.fb.Classes = map[string]string{"internet": "open", "open": "internet"}
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	if e := strs(hello(t, a)["egress"]); strings.Join(e, ",") != "none,open" {
		t.Fatalf("internet bound wide, open bound narrow: %v", e)
	}
	// a sandbox claims what it can reach, never less
	sb := a.Create(map[string]any{"name": "wide", "egress": "open"})
	if sb.Egress != "open" {
		t.Fatalf("egress: %+v", sb)
	}
	// without clones, images with a setup script are hidden, and hello says so
	tm.fb.mu.Lock()
	tm.fb.Caps = []string{"exec", "files", "tar", "snapshots"}
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	h = hello(t, a)
	if ims := h["images"].([]any); len(ims) != 1 || ims[0].(map[string]any)["id"] != "base" || h["notes"] == nil {
		t.Fatalf("no clones: images %v, notes %v", h["images"], h["notes"])
	}
	a.Refused("POST", "/sandboxes", map[string]any{"name": "x", "image": "tools"}, 400, "invalid")
	// sizes over the substrate's per-sandbox caps aren't offered
	tm.fb.mu.Lock()
	tm.fb.Limits.PerSandbox = xbin.SandboxSizeLimits{MaxMemMiB: 4096, MaxVCPUs: 4, MaxDiskGiB: 100}
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	if sz := hello(t, a)["sizes"].([]any); len(sz) != 2 {
		t.Fatalf("sizes within the caps: %v", sz)
	}
	a.Refused("POST", "/sandboxes", map[string]any{"name": "x", "size": "large"}, 400, "invalid")
	// a substrate that runs everything as root: the user is root
	tm.fb.mu.Lock()
	tm.fb.Users = "root"
	tm.fb.mu.Unlock()
	tm.m.forgetRuntime()
	if r := a.Create(map[string]any{"name": "rooted"}); r.User != "root" {
		t.Fatalf("a users:root substrate: %+v", r)
	}
}

func TestQuotas(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	tm.setConfig(t, func(c *Config) {
		c.Quotas = Quotas{Consumer: Quota{Sandboxes: 3}, Person: Quota{Running: 1},
			Consumers: map[string]Quota{"apps/q-big": {}}, People: map[string]Quota{"boss": {Running: 5}}}
	})
	a := tm.tg.As(t, "apps/q-a")
	h := hello(t, a.Asserting("alice"))
	if l := h["limits"].(map[string]any); l["sandboxes"] != float64(3) || l["running"] != float64(1) {
		t.Fatalf("hello's effective limits: %v", l)
	}
	if l := hello(t, tm.tg.As(t, "apps/q-big"))["limits"].(map[string]any); l["sandboxes"] != float64(0) {
		t.Fatalf("an override without limits: %v", l)
	}
	alice := a.Asserting("alice")
	one := alice.Create(map[string]any{"name": "one"})
	// alice runs one already: another running one is refused, a stopped one isn't
	alice.Refused("POST", "/sandboxes", map[string]any{"name": "two"}, 429, "limit")
	two := alice.Create(map[string]any{"name": "two", "start": false})
	alice.Refused("POST", "/sandboxes/"+two.ID+"/start?wait=5", nil, 429, "limit")
	alice.Refused("POST", "/sandboxes/"+two.ID+"/run", map[string]any{"cmd": "true"}, 429, "limit") // an auto-start too
	a.Call("POST", "/sandboxes/"+one.ID+"/stop?wait=5", nil, 200, nil)
	alice.Sh(two.ID, "true")
	// the boss may run five
	boss := a.Asserting("boss")
	boss.Create(map[string]any{"name": "b1"})
	// the consumer holds three: the fourth is refused
	a.Refused("POST", "/sandboxes", map[string]any{"name": "four", "start": false}, 429, "limit")
	// a person's quotas count across consumers; the other consumer's own count is its own
	b := tm.tg.As(t, "apps/q-b").Asserting("alice")
	b.Refused("POST", "/sandboxes", map[string]any{"name": "elsewhere"}, 429, "limit")
	b.Create(map[string]any{"name": "elsewhere", "start": false})
}

func TestRuntimeNamesStayInside(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	a := tm.tg.As(t, "apps/rn-a")
	sb := a.Create(map[string]any{"name": "named"})
	tm.m.mu.Lock()
	rt := tm.m.recs[sb.ID].Runtime
	tm.m.mu.Unlock()
	if rt == "" || rt == sb.ID {
		t.Fatalf("runtime name %q for %s", rt, sb.ID)
	}
	var view map[string]any
	a.Call("GET", "/sandboxes/"+sb.ID, nil, 200, &view)
	delete(view, "workdir") // (the fake's layout is its host directory, named after the runtime's sandbox)
	delete(view, "home")
	if raw, _ := json.Marshal(view); bytes.Contains(raw, []byte(rt)) {
		t.Fatalf("the runtime name shows: %s", raw)
	}
	tm.fb.FailNext("run", &xbin.SandboxError{Status: 409, Refusal: "state", State: "stopping", Message: "sandbox " + rt + " is stopping"})
	r := a.Refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 409, "state")
	if strings.Contains(r.Error, rt) || !strings.Contains(r.Error, sb.ID) || r.State != "stopping" {
		t.Fatalf("a refusal naming the runtime sandbox: %+v", r)
	}
	// a substrate that isn't there is unavailable, not a 500
	tm.fb.FailNext("run", errors.New("dial unix: connection refused"))
	a.Refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 503, "unavailable")
}

// An image's build sandbox has a runtime name too: a failed build's detail
// (the substrate's refusal, the script's last lines) never shows it.
func TestImageBuildNamesStayInside(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	tm.setConfig(t, func(c *Config) {
		c.Images = append(c.Images, Image{ID: "leaky", Setup: `pwd; echo "on $SANDBOX_ID"; exit 3`})
	})
	a := tm.tg.As(t, "apps/in-a")
	var bad sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "bad", "image": "leaky"}, 201, &bad)
	tm.m.mu.Lock()
	built := *tm.m.imgs["leaky"]
	tm.m.mu.Unlock()
	if bad.State != "error" || !strings.Contains(bad.StateDetail, "exited 3") || !strings.Contains(bad.StateDetail, "on image:leaky") {
		t.Fatalf("a sandbox of a broken image: %+v", bad)
	}
	if built.Runtime == "" || strings.Contains(bad.StateDetail, built.Runtime) || strings.Contains(built.Detail, built.Runtime) {
		t.Fatalf("the build sandbox's name %s shows: %q (image: %q)", built.Runtime, bad.StateDetail, built.Detail)
	}
	if !strings.Contains(built.Log, built.Runtime) { // the operators' log is the script's own output
		t.Fatalf("the build log: %q", built.Log)
	}
}

func TestExecClientIDsPerConsumer(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	a, b := tm.tg.As(t, "apps/ec-a"), tm.tg.As(t, "apps/ec-b")
	sb := a.Create(map[string]any{"name": "shared"})
	a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": b.Consumer(), "users": "*"}}}, 200, nil)
	x := a.Exec(sb.ID, map[string]any{"cmd": "sleep 5", "clientId": "k"})
	y := b.Exec(sb.ID, map[string]any{"cmd": "sleep 5", "clientId": "k"})
	if x.ID == y.ID || x.ClientID != "k" || y.ClientID != "k" {
		t.Fatalf("one clientId, two consumers: %+v %+v", x, y)
	}
	var l struct{ Execs []sandboxcontract.Exec }
	b.Call("GET", "/sandboxes/"+sb.ID+"/execs", nil, 200, &l)
	for _, e := range l.Execs {
		if e.ID == x.ID && e.ClientID != "" {
			t.Fatalf("b sees a's clientId: %+v", e)
		}
	}
	a.Call("DELETE", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 204, nil)
	b.Call("DELETE", "/sandboxes/"+sb.ID+"/execs/"+y.ID, nil, 204, nil)
}

func TestRestart(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "db.sqlite")
	tm := newTestManager(t, db)
	a := tm.tg.As(t, "apps/rs-a")
	sb := a.Asserting("alice").Create(map[string]any{"name": "kept", "labels": map[string]string{"k": "v"}, "visibility": "team"})
	a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/rs-b", "users": []string{"carol"}}}}, 200, nil)
	var s1 sandboxcontract.Snapshot
	a.Call("POST", "/sandboxes/"+sb.ID+"/snapshots", map[string]any{"name": "one", "clientId": "c"}, 201, &s1)
	var again sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes", map[string]any{"name": "idem", "clientId": "i", "start": false}, 201, &again)
	// a creation cut short: its record says "creating"
	tm.m.mu.Lock()
	half := &record{ID: "sb-half", Runtime: "shalf", Name: "half", Image: "base", Size: "small", Egress: "none",
		Owner: owner{Via: "apps/rs-a"}, Visibility: "private", Members: []string{}, Workdir: "/work", Home: "/home/dev",
		User: "dev", UID: 1000, GID: 1000, Shell: "/bin/bash", Created: now(), Version: 1, Overlay: "creating",
		Plan: &createPlan{Start: true, Size: sizeSpec{2048, 2, 20}}}
	if err := tm.st.putRecord(half); err != nil {
		t.Fatal(err)
	}
	tm.m.mu.Unlock()
	// a new manager on the same table and substrate
	tm.srv.Close()
	tm.m.Close()
	tm.fb.mu.Lock() // (the old manager's cleanup closes its backend: this one is the substrate's continuation)
	fb := &fakeBackend{Root: tm.fb.Root, Grace: tm.fb.Grace, boxes: tm.fb.boxes}
	tm.fb.mu.Unlock()
	tm2 := serveManager(t, db, fb)
	a2 := tm2.tg.As(t, "apps/rs-a")
	got := a2.Get(sb.ID)
	if got.Owner.User != "alice" || !got.Owner.Asserted || got.Labels["k"] != "v" || got.Visibility != "team" || len(got.Shares) != 1 || got.Version != sb.Version+1 {
		t.Fatalf("after a restart: %+v", got)
	}
	tm2.tg.As(t, "apps/rs-b").Verified("carol").Get(sb.ID)
	var s2 sandboxcontract.Snapshot
	a2.Call("POST", "/sandboxes/"+sb.ID+"/snapshots", map[string]any{"name": "one", "clientId": "c"}, 200, &s2)
	if s2.ID != s1.ID {
		t.Fatalf("a snapshot clientId across a restart: %+v", s2)
	}
	var idem sandboxcontract.Sandbox
	a2.Call("POST", "/sandboxes", map[string]any{"name": "idem", "clientId": "i", "start": false}, 200, &idem)
	if idem.ID != again.ID {
		t.Fatalf("a create clientId across a restart: %s, want %s", idem.ID, again.ID)
	}
	eventually(t, 10*time.Second, "the cut-short creation finishes", func() bool {
		var s sandboxcontract.Sandbox
		resp, b, err := a2.Do(context.Background(), "GET", "/sandboxes/sb-half", nil)
		return err == nil && resp.StatusCode == 200 && json.Unmarshal(b, &s) == nil && s.State == "running"
	})
	if out := a2.Sh("sb-half", "pwd"); !strings.HasSuffix(strings.TrimSpace(out), "/work") {
		t.Fatalf("the resumed sandbox runs in %q", out)
	}
}

func TestBackendDown(t *testing.T) {
	t.Parallel()
	st, err := openStore(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	cfg := defaultConfig()
	cfg.Backend = "no-such"
	if err := st.putConfig(cfg); err != nil {
		t.Fatal(err)
	}
	m, err := newManager(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.Logf = func(string, ...any) {}
	defer m.Close()
	srv := httptest.NewServer(m.contractHandler())
	defer srv.Close()
	a := sandboxcontract.Target{URL: srv.URL}.As(t, "apps/down")
	if r := a.Refused("GET", "/hello", nil, 503, "unavailable"); !strings.Contains(r.Error, "no backend") {
		t.Fatalf("a manager without its backend: %+v", r)
	}
	a.Refused("POST", "/sandboxes", map[string]any{"name": "x"}, 503, "unavailable")
}

// --- the operators and the tile's own routes ---------------------------------------------

// tileServer serves the whole tile as main does, as apps/cs (its own page's
// calls come from there).
func tileServer(t *testing.T, tm *testManager) *httptest.Server {
	tm.m.self = "apps/cs"
	mux := http.NewServeMux()
	mux.Handle("/sbx/", consumerGuard(tm.m.contractHandler()))
	tm.m.operatorRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type as struct{ from, role, user, level string }

func call(t *testing.T, srv *httptest.Server, who as, method, path string, body any, want int, out any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("X-XBin-From", who.from)
	req.Header.Set("X-XBin-Role", who.role)
	if who.user != "" {
		req.Header.Set("X-XBin-User", who.user)
		req.Header.Set("X-XBin-User-Level", who.level)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s as %+v: %d %s, want %d", method, path, who, resp.StatusCode, b, want)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
}

// upgrade sends a WebSocket handshake for path as who: its status, and the
// body of a refusal (a socket that opened is closed at once).
func upgrade(t *testing.T, srv *httptest.Server, who as, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("X-XBin-From", who.from)
	req.Header.Set("X-XBin-Role", who.role)
	if who.user != "" {
		req.Header.Set("X-XBin-User", who.user)
		req.Header.Set("X-XBin-User-Level", who.level)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return resp.StatusCode, ""
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestOperators(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	srv := tileServer(t, tm)
	owner := as{from: "owner", role: "admin"}
	writer := as{from: "apps/cs", role: "admin", user: "olga", level: "write"}
	reader := as{from: "apps/cs", role: "admin", user: "rita", level: "read"}
	agentTile := as{from: "apps/agent", role: "consumer"}
	// the contract's routes: the consumer role (or the tile itself), nothing else
	call(t, srv, as{from: "apps/stranger", role: "reader"}, "GET", "/sbx/hello", nil, 403, nil)
	var sb sandboxcontract.Sandbox
	call(t, srv, agentTile, "POST", "/sbx/sandboxes", map[string]any{"name": "ops"}, 201, &sb)
	call(t, srv, writer, "GET", "/sbx/sandboxes/"+sb.ID, nil, 404, nil) // the tile itself is a consumer of its own
	// the operators' routes: write access to the tile
	call(t, srv, agentTile, "GET", "/ops/state", nil, 403, nil)
	call(t, srv, reader, "GET", "/ops/state", nil, 403, nil)
	var me map[string]any
	call(t, srv, reader, "GET", "/me", nil, 200, &me)
	if me["operator"] != false || me["user"] != "rita" {
		t.Fatalf("/me for a reader: %v", me)
	}
	var st struct {
		Backend   map[string]any
		Sandboxes []opView
		Usage     struct{ Consumers map[string]usage }
		Orphans   []orphan
		Offer     map[string]any
	}
	call(t, srv, writer, "GET", "/ops/state", nil, 200, &st)
	if len(st.Sandboxes) != 1 || st.Sandboxes[0].ID != sb.ID || st.Sandboxes[0].Consumer != "apps/agent" || st.Sandboxes[0].Runtime == "" ||
		st.Usage.Consumers["apps/agent"].Sandboxes != 1 || st.Usage.Consumers["apps/agent"].Running != 1 || st.Offer == nil {
		t.Fatalf("the operators' state: %+v", st)
	}
	// operators change a sandbox's definition, never who may use it: that is
	// its home consumer's (or its owner's there), whoever the operator is
	var pv opView
	call(t, srv, owner, "PATCH", "/ops/sandboxes/"+sb.ID, map[string]any{"name": "renamed", "labels": map[string]string{"k": "v"}, "autoStopMin": 45}, 200, &pv)
	if pv.Name != "renamed" || pv.Labels["k"] != "v" || pv.AutoStopMin != 45 {
		t.Fatalf("an operator's PATCH: %+v", pv)
	}
	for _, body := range []map[string]any{{"shares": []map[string]any{{"consumer": "apps/term", "users": "*"}}}, {"visibility": "team"},
		{"members": []string{"mallory"}}, {"name": "again", "shares": []map[string]any{}}} {
		call(t, srv, owner, "PATCH", "/ops/sandboxes/"+sb.ID, body, 403, nil)
		call(t, srv, writer, "PATCH", "/ops/sandboxes/"+sb.ID, body, 403, nil)
	}
	call(t, srv, as{from: "apps/term", role: "consumer"}, "GET", "/sbx/sandboxes/"+sb.ID, nil, 404, nil)
	if v := tm.m.recCopy(sb.ID); v.Name != "renamed" || v.Visibility != "private" || len(v.Members) != 0 || len(v.Shares) != 0 {
		t.Fatalf("a refused PATCH changed the sandbox: %+v", v)
	}
	call(t, srv, agentTile, "PATCH", "/sbx/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/term", "users": "*"}}}, 200, nil)
	call(t, srv, as{from: "apps/term", role: "consumer"}, "GET", "/sbx/sandboxes/"+sb.ID, nil, 200, nil)
	// operators set quotas
	call(t, srv, owner, "PUT", "/ops/config", map[string]any{"quotas": map[string]any{"consumer": map[string]any{"sandboxes": 1}}}, 200, nil)
	call(t, srv, agentTile, "POST", "/sbx/sandboxes", map[string]any{"name": "over"}, 429, nil)
	call(t, srv, owner, "PUT", "/ops/config", map[string]any{"images": []map[string]any{}}, 400, nil)
	call(t, srv, owner, "PUT", "/ops/config", map[string]any{"backend": "other"}, 409, nil) // its sandboxes live on this one
	// snapshots: an operator's backups of anyone's sandbox (writers only)
	var snap xbin.Snapshot
	call(t, srv, reader, "POST", "/ops/sandboxes/"+sb.ID+"/snapshots", map[string]any{"name": "nightly"}, 403, nil)
	call(t, srv, writer, "POST", "/ops/sandboxes/"+sb.ID+"/snapshots", map[string]any{"name": "nightly"}, 201, &snap)
	var snaps struct{ Snapshots []xbin.Snapshot }
	call(t, srv, owner, "GET", "/ops/sandboxes/"+sb.ID+"/snapshots", nil, 200, &snaps)
	if len(snaps.Snapshots) != 1 || snaps.Snapshots[0].Name != "nightly" || snap.ID == "" {
		t.Fatalf("the operators' snapshots: %+v", snaps)
	}
	var restored opView
	call(t, srv, owner, "POST", "/ops/sandboxes/"+sb.ID+"/snapshots/"+snap.ID+"/restore", nil, 200, &restored)
	if restored.ID != sb.ID || restored.Consumer != "apps/agent" {
		t.Fatalf("a restore answers the sandbox: %+v", restored)
	}
	call(t, srv, owner, "DELETE", "/ops/sandboxes/"+sb.ID+"/snapshots/"+snap.ID, nil, 204, nil)
	call(t, srv, owner, "GET", "/ops/sandboxes/sb-nope/snapshots", nil, 404, nil)
	// the mode and the mounts are the operators' too
	call(t, srv, owner, "PUT", "/ops/config", map[string]any{"mode": "auto", "mounts": []map[string]any{{"res": "res:apps/cs/cache", "at": "/cache"}}}, 200, &st)
	call(t, srv, owner, "PUT", "/ops/config", map[string]any{"mounts": []map[string]any{{"res": "res:apps/cs/cache", "at": "/proc/x"}}}, 400, nil)
	// lifecycle and deletion
	var v opView
	call(t, srv, owner, "POST", "/ops/sandboxes/"+sb.ID+"/stop?wait=5", nil, 200, &v)
	if v.State != "stopped" {
		t.Fatalf("an operator's stop: %+v", v)
	}
	// an orphan: the substrate's, not the manager's
	if _, err := tm.fb.Create(context.Background(), xbin.SandboxSpec{Name: "stray", Mode: "vm"}); err != nil {
		t.Fatal(err)
	}
	call(t, srv, owner, "GET", "/ops/state", nil, 200, &st)
	if len(st.Orphans) != 1 || st.Orphans[0].Name != "stray" {
		t.Fatalf("orphans: %+v", st.Orphans)
	}
	call(t, srv, owner, "DELETE", "/ops/orphans/"+v.Runtime, nil, 409, nil)
	call(t, srv, owner, "DELETE", "/ops/orphans/stray", nil, 204, nil)
	call(t, srv, owner, "DELETE", "/ops/sandboxes/"+sb.ID, nil, 204, nil)
	call(t, srv, agentTile, "GET", "/sbx/sandboxes/"+sb.ID, nil, 404, nil)
	// with no sandboxes left, the backend may change (to one this build lacks: down, and saying so)
	call(t, srv, owner, "PUT", "/ops/config", map[string]any{"backend": "other"}, 200, &st)
	if !strings.Contains(st.Backend["error"].(string), "no backend") {
		t.Fatalf("an unknown backend: %v", st.Backend)
	}
	call(t, srv, agentTile, "GET", "/sbx/hello", nil, 503, nil)
}

// A person on the tile's own page with read access to it looks and never
// changes (D29's rule for mutating endpoints): every change is refused
// before it is routed, and a read never starts a stopped sandbox. A person
// with write access, and every other consumer's calls, are as before.
func TestPageReaders(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	srv := tileServer(t, tm)
	olga := as{from: "apps/cs", role: "admin", user: "olga", level: "write"}
	tess := as{from: "apps/cs", role: "admin", user: "tess", level: "terminal"}
	rita := as{from: "apps/cs", role: "admin", user: "rita", level: "read"}
	var me map[string]any
	call(t, srv, rita, "GET", "/me", nil, 200, &me)
	if me["write"] != false || me["level"] != "read" || me["operator"] != false || me["self"] != "apps/cs" {
		t.Fatalf("/me for a reader: %v", me)
	}
	call(t, srv, olga, "GET", "/me", nil, 200, &me)
	if me["write"] != true || me["level"] != "write" {
		t.Fatalf("/me for a writer: %v", me)
	}
	// a writer makes sandboxes on the page, the team's to use
	var team, off sandboxcontract.Sandbox
	call(t, srv, olga, "POST", "/sbx/sandboxes", map[string]any{"name": "team", "visibility": "team"}, 201, &team)
	call(t, srv, olga, "POST", "/sbx/sandboxes", map[string]any{"name": "off", "visibility": "team", "start": false}, 201, &off)
	call(t, srv, tess, "PUT", "/sbx/sandboxes/"+team.ID+"/files/content?path="+team.Workdir+"/note.txt", nil, 200, nil) // terminal ⊇ write
	// the reader sees them, reads a running one's files and output…
	var l struct{ Sandboxes []sandboxcontract.Sandbox }
	call(t, srv, rita, "GET", "/sbx/sandboxes", nil, 200, &l)
	if len(l.Sandboxes) != 2 {
		t.Fatalf("a reader lists %d sandboxes", len(l.Sandboxes))
	}
	call(t, srv, rita, "GET", "/sbx/hello", nil, 200, nil)
	call(t, srv, rita, "GET", "/sbx/sandboxes/"+team.ID, nil, 200, nil)
	call(t, srv, rita, "GET", "/sbx/sandboxes/"+team.ID+"/files/list?path="+team.Workdir, nil, 200, nil)
	call(t, srv, rita, "GET", "/sbx/sandboxes/"+team.ID+"/files/stat?path="+team.Workdir+"/note.txt", nil, 200, nil)
	call(t, srv, rita, "GET", "/sbx/sandboxes/"+team.ID+"/execs", nil, 200, nil)
	call(t, srv, rita, "GET", "/sbx/sandboxes/"+team.ID+"/snapshots", nil, 200, nil)
	// …and changes nothing: every change is refused, whatever it names
	sb := "/sbx/sandboxes/" + team.ID
	for _, c := range []struct{ method, path string }{
		{"POST", "/sbx/sandboxes"}, {"PATCH", sb}, {"DELETE", sb}, {"POST", sb + "/start"}, {"POST", sb + "/stop"},
		{"POST", sb + "/run"}, {"POST", sb + "/execs"}, {"DELETE", sb + "/execs/e1-1"}, {"POST", sb + "/execs/e1-1/stdin"},
		{"POST", sb + "/execs/e1-1/signal"}, {"POST", sb + "/execs/e1-1/resize"}, {"GET", sb + "/execs/e1-1/tty"}, {"GET", sb + "/tty"},
		{"GET", sb + "/execs/e1-1/stdio"}, {"PUT", sb + "/files/content?path=x"}, {"POST", sb + "/files/mkdir"}, {"POST", sb + "/files/remove"}, {"POST", sb + "/files/move"},
		{"PUT", sb + "/tar?path=/"}, {"POST", sb + "/snapshots"}, {"POST", sb + "/snapshots/s-1/restore"}, {"DELETE", sb + "/snapshots/s-1"},
		{"POST", "/sbx/sandboxes/sb-nope/start"},
	} {
		var r sandboxcontract.Refusal
		call(t, srv, rita, c.method, c.path, map[string]any{"name": "x", "cmd": "true", "path": "x", "signal": "TERM", "rows": 1, "cols": 1}, 403, &r)
		if r.Refusal != "not-allowed" || !strings.Contains(r.Error, "write access") {
			t.Fatalf("%s %s as a reader: %+v", c.method, c.path, r)
		}
	}
	if n := len(tm.m.recs); n != 2 {
		t.Fatalf("a reader's create made one: %d sandboxes", n)
	}
	// a stdio socket is a change: it writes the exec's stdin (and takes it
	// from the socket attached before) — a reader's upgrade is refused
	// before it is routed, as is every other upgrade; a writer's attaches
	var x sandboxcontract.Exec
	call(t, srv, olga, "POST", sb+"/execs", map[string]any{"cmd": "cat > pwned.txt", "stdin": true, "split": true}, 201, &x)
	for _, p := range []string{"/execs/" + x.ID + "/stdio", "/execs/" + x.ID + "/stdio?since=0", "/execs/nope/stdio", "/ports/8080/"} {
		code, body := upgrade(t, srv, rita, sb+p)
		if code != http.StatusForbidden || !strings.Contains(body, `"not-allowed"`) || !strings.Contains(body, "write access") {
			t.Fatalf("a reader's upgrade to %s: %d %s", p, code, body)
		}
	}
	if code, body := upgrade(t, srv, olga, sb+"/execs/"+x.ID+"/stdio"); code != http.StatusSwitchingProtocols {
		t.Fatalf("a writer's stdio socket: %d %s", code, body)
	}
	// a read of a stopped sandbox doesn't start it for a reader
	call(t, srv, rita, "GET", "/sbx/sandboxes/"+off.ID+"/files/list?path="+off.Workdir, nil, 403, nil)
	if in, err := tm.fb.Get(context.Background(), tm.m.recCopy(off.ID).Runtime); err != nil || in.State != "stopped" {
		t.Fatalf("a reader's read started the sandbox: %+v %v", in, err)
	}
	call(t, srv, olga, "GET", "/sbx/sandboxes/"+off.ID+"/files/list?path="+off.Workdir, nil, 200, nil) // a writer's does
	// every other consumer is trusted as the contract says, whatever the
	// person's level on this tile
	agentReader := as{from: "apps/agent", role: "consumer", user: "rita", level: "read"}
	var mine sandboxcontract.Sandbox
	call(t, srv, agentReader, "POST", "/sbx/sandboxes", map[string]any{"name": "via the agent", "start": false}, 201, &mine)
	call(t, srv, agentReader, "POST", "/sbx/sandboxes/"+mine.ID+"/run", map[string]any{"cmd": "true"}, 200, nil)
	call(t, srv, agentReader, "DELETE", "/sbx/sandboxes/"+mine.ID, nil, 204, nil)
	// the owner's token (no person) is the workspace itself
	call(t, srv, as{from: "apps/cs", role: "admin"}, "POST", "/sbx/sandboxes/"+team.ID+"/stop", nil, 200, nil)
}

// A failed rebuild keeps the previous good build: its template sandbox stays
// until a build succeeds, and while it is current for the script (an
// operator's rebuild that failed, a script changed back) sandboxes of the
// image clone it.
func TestImageRebuildKeepsGoodBuild(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	srv := tileServer(t, tm)
	good := `echo "v1" > marker`
	tm.setConfig(t, func(c *Config) { c.Images = append(c.Images, Image{ID: "tools", Setup: good}) })
	a := tm.tg.As(t, "apps/kg-a")
	var one sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "one", "image": "tools"}, 201, &one)
	img := func() builtImage {
		tm.m.mu.Lock()
		defer tm.m.mu.Unlock()
		return *tm.m.imgs["tools"]
	}
	v1 := img()
	exists := func(name string) bool { _, err := tm.fb.Get(context.Background(), name); return err == nil }
	if v1.State != "ready" || !exists(v1.Runtime) {
		t.Fatalf("the first build: %+v", v1)
	}
	// an operator's rebuild that fails (the substrate refuses its sandbox):
	// the build says so, the good one is kept and serves
	tm.fb.FailNext("create", &xbin.SandboxError{Status: 503, Refusal: "unavailable", Message: "no room"})
	call(t, srv, as{from: "owner", role: "admin"}, "POST", "/ops/images/tools/build", nil, 202, nil)
	// over means its job is gone too: the build marks the image "error"
	// before its job leaves m.builds, and a create in between takes the
	// build path ("its first use", not running yet) — once on a loaded CI
	// runner (v0.3.68's tag run)
	eventually(t, 10*time.Second, "the rebuild fails", func() bool {
		tm.m.mu.Lock()
		defer tm.m.mu.Unlock()
		return tm.m.imgs["tools"].State == "error" && tm.m.builds["tools"] == nil
	})
	failed := img()
	if failed.Previous == nil || failed.Previous.Runtime != v1.Runtime || failed.Previous.Snapshot != v1.Snapshot || !exists(v1.Runtime) {
		t.Fatalf("a failed rebuild dropped the good build: %+v (previous %+v)", failed, failed.Previous)
	}
	creates := tm.fb.Calls("create")
	two := a.Create(map[string]any{"name": "two", "image": "tools"})
	if tm.fb.Calls("create") != creates+1 || two.State != "running" || a.Read(two.ID, two.Workdir+"/marker") != "v1\n" {
		t.Fatalf("a sandbox of an image whose rebuild failed: %+v (%d creates)", two, tm.fb.Calls("create")-creates)
	}
	// the kept build is the manager's, not an orphan
	var st struct{ Orphans []orphan }
	call(t, srv, as{from: "owner", role: "admin"}, "GET", "/ops/state", nil, 200, &st)
	if len(st.Orphans) != 0 {
		t.Fatalf("the kept build is an orphan: %+v", st.Orphans)
	}
	// a changed script that fails to build: that sandbox is in error, the
	// good build stays (its script isn't current, so it serves no one)…
	tm.setConfig(t, func(c *Config) { c.Images[1].Setup = "echo broken; exit 3" })
	var bad sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "bad", "image": "tools"}, 201, &bad)
	if bad.State != "error" || !strings.Contains(bad.StateDetail, "exited 3") {
		t.Fatalf("a sandbox of a broken script: %+v", bad)
	}
	if b := img(); b.State != "error" || b.Previous == nil || b.Previous.Runtime != v1.Runtime || !exists(v1.Runtime) {
		t.Fatalf("a failed build of a changed script dropped the good build: %+v", b)
	}
	// …and a script changed back clones it at once
	tm.setConfig(t, func(c *Config) { c.Images[1].Setup = good })
	creates = tm.fb.Calls("create")
	back := a.Create(map[string]any{"name": "back", "image": "tools"})
	if tm.fb.Calls("create") != creates+1 || a.Read(back.ID, back.Workdir+"/marker") != "v1\n" {
		t.Fatalf("the script changed back: %+v (%d creates)", back, tm.fb.Calls("create")-creates)
	}
	// a build that succeeds replaces it: the old template goes only now
	tm.setConfig(t, func(c *Config) { c.Images[1].Setup = `echo "v2" > marker` })
	var three sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "three", "image": "tools"}, 201, &three)
	if got := a.Read(three.ID, three.Workdir+"/marker"); got != "v2\n" {
		t.Fatalf("after a good build: %q", got)
	}
	if b := img(); b.State != "ready" || b.Previous != nil {
		t.Fatalf("a good build: %+v", b)
	}
	eventually(t, 5*time.Second, "the old template sandbox is deleted", func() bool { return !exists(v1.Runtime) })
}

// A clone's creation error never names its source's runtime sandbox: the
// source's contract id, or image:<id> for an image's template, instead.
func TestCloneNamesStayInside(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	tm.setConfig(t, func(c *Config) { c.Images = append(c.Images, Image{ID: "tools", Setup: "true"}) })
	a := tm.tg.As(t, "apps/cn-a")
	src := a.Create(map[string]any{"name": "src"})
	srcRT := tm.m.recCopy(src.ID).Runtime
	clean := func(r sandboxcontract.Refusal, name, want string) {
		t.Helper()
		if strings.Contains(r.Error, name) || !strings.Contains(r.Error, want) {
			t.Fatalf("a clone's error names %s: %+v (want %s)", name, r, want)
		}
	}
	// the substrate refuses, naming the source
	tm.fb.FailNext("create", &xbin.SandboxError{Status: 409, Refusal: "exists", Message: "sandbox " + srcRT + " has a clone of that name"})
	clean(a.Refused("POST", "/sandboxes", map[string]any{"name": "c1", "from": map[string]any{"sandbox": src.ID}}, 409, "exists"), srcRT, src.ID)
	tm.fb.FailNext("snapshot", &xbin.SandboxError{Status: 409, Refusal: "state", State: "stopping", Message: "sandbox " + srcRT + " is stopping"})
	clean(a.Refused("POST", "/sandboxes", map[string]any{"name": "c1", "from": map[string]any{"sandbox": src.ID}}, 409, "state"), srcRT, src.ID) // running: snapshotted for the clone
	// the source is gone at the substrate (the manager still has it)
	if err := tm.fb.Delete(context.Background(), srcRT); err != nil {
		t.Fatal(err)
	}
	clean(a.Refused("POST", "/sandboxes", map[string]any{"name": "c2", "from": map[string]any{"sandbox": src.ID}}, 404, "not-found"), srcRT, src.ID)
	// an image's clone: its template sandbox is image:<id>
	var one sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "one", "image": "tools"}, 201, &one)
	tm.m.mu.Lock()
	imgRT := tm.m.imgs["tools"].Runtime
	tm.m.mu.Unlock()
	tm.fb.FailNext("create", fmt.Errorf("clone of %s: disk full", imgRT))
	clean(a.Refused("POST", "/sandboxes", map[string]any{"name": "c3", "image": "tools"}, 503, "unavailable"), imgRT, "image:tools")
	if l := a.List(); len(l) != 2 {
		t.Fatalf("failed clones stay: %d sandboxes", len(l))
	}
}

func TestConfigMerge(t *testing.T) {
	t.Parallel()
	c := defaultConfig()
	c.Quotas.Consumers = map[string]Quota{"apps/a": {Sandboxes: 2}}
	next, err := c.merge([]byte(`{"quotas": {"person": {"running": 2}}, "unknown": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if next.Quotas.Person.Running != 2 || next.Quotas.Consumers != nil || len(next.Images) != 1 || len(next.Sizes) != 3 {
		t.Fatalf("a field replaced whole, the rest kept: %+v", next)
	}
	for _, bad := range []string{`{"images": []}`, `{"sizes": [{"id": "x", "memMiB": 1, "vcpus": 1, "diskGiB": 1}]}`,
		`{"layout": {"workdir": "rel", "home": "/h", "user": "u", "shell": "/bin/sh"}}`, `{"mode": "cloud"}`,
		`{"images": [{"id": "a", "default": true}, {"id": "b", "default": true}]}`, `{"quotas": {"consumer": {"sandboxes": -1}}}`,
		`{"mounts": [{"res": "apps/x/y", "at": "/m"}]}`, `{"mounts": [{"res": "res:apps/x/y", "at": "rel"}]}`,
		`{"mounts": [{"res": "res:apps/x/y", "path": "../up", "at": "/m"}]}`, `{"mounts": [{"res": "res:apps/x/y", "at": "/"}]}`} {
		if _, err := c.merge([]byte(bad)); err == nil {
			t.Errorf("merged %s", bad)
		}
	}
}

// A new manager makes VM sandboxes only; one made before that default keeps
// the automatic mode it ran with — a config saved without a mode, or none
// saved at all by a manager that already has sandboxes.
func TestModeDefault(t *testing.T) {
	t.Parallel()
	open := func(t *testing.T) *store {
		st, err := openStore(filepath.Join(t.TempDir(), "db.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.close() })
		return st
	}
	modeOf := func(t *testing.T, st *store) string {
		m, err := newManager(st, &fakeBackend{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m.config().Mode
	}
	if got := modeOf(t, open(t)); got != "vm" {
		t.Errorf("a new manager's mode %q, want vm", got)
	}
	st := open(t)
	if err := st.putSetting("config", `{"images": [{"id": "base", "title": "B", "default": true}], "sizes": [{"id": "s", "title": "S", "memMiB": 1024, "vcpus": 1, "diskGiB": 10, "default": true}], "layout": {"workdir": "/work", "home": "/home/dev", "user": "dev", "uid": 1000, "gid": 1000, "shell": "/bin/bash"}}`); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, st); got != "auto" {
		t.Errorf("a config saved without a mode: %q, want auto", got)
	}
	st = open(t)
	if err := st.putRecord(&record{ID: "sb-old", Runtime: "old", Name: "old"}); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, st); got != "auto" {
		t.Errorf("sandboxes and no config: %q, want auto", got)
	}
	if saved, _ := st.setting("config"); !strings.Contains(saved, `"mode":"auto"`) {
		t.Errorf("the kept mode isn't saved: %s", saved)
	}
}
