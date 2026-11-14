package boot

// sealed_test.go — spike S1 of the workspace agent (issue #51): a sealed
// component, declared by a table and by nothing in the workspace, is
// served and run from the code the binary carries, at its reserved path,
// with no code or terminal surface, and a same-named directory in the
// workspace changes nothing. In-process, no isolation (the sandboxed path
// is the runner's own; see plans/wsagent/s1-sealed.md for what that
// leaves unproven).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/util"
)

// sealedCounter is a sealed component's source as a release would embed
// it: a page, a manifest, a scope with a kv resource, a Go backend on the
// SDK alone (no module download), and a go.mod.tile as builtins ship one.
func sealedCounter(version string) fs.FS {
	return fstest.MapFS{
		"xbin.json": {Data: []byte(`{"runtime":"go","title":"Sealed counter","expose":{"roles":{"reader":"read","writer":"write"}},
  "interfaces":{"llm":{"kind":"http","service":"openai"},"net":{"kind":"net"}},
  "uses":[{"target":"res:apps/counter/state","role":"writer"},{"target":"res:apps/counter/events","role":"writer"}]}`)},
		"scope.json":  {Data: []byte(`{"resources":{"state":{"type":"kv"},"events":{"type":"bus"}}}`)},
		"index.html":  {Data: []byte("<!doctype html><html><head><title>sealed</title></head><body>" + version + "</body></html>")},
		"app.js":      {Data: []byte("export const version = '" + version + "';\n")},
		"go.mod.tile": {Data: []byte("module counter\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")},
		"backend/main.go": {Data: []byte(`package main

import (
	"fmt"
	"net/http"
	"os"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		wd, _ := os.Getwd()
		fmt.Fprintf(w, "` + version + ` self=%s from=%s cwd=%s", os.Getenv("XBIN_COMPONENT"), xbin.Caller(r).From, wd)
	})
	xbin.Serve(mux)
}
`)},
	}
}

// sealedDaemon is an in-process xbind serving ws, with its built state.
type sealedDaemon struct {
	t     *testing.T
	st    *State
	base  string // http://127.0.0.1:<port>
	token string // the owner token ("" under --no-auth)
	stop  func()
}

// startSealed boots ws in-process with the given sealed table (tune adjusts
// the config: by default --no-auth, a development xbind) and serves it
// until the test ends or stop is called.
func startSealed(t *testing.T, ws string, table []sealed.Spec, tune ...func(*Config)) *sealedDaemon {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(ws, ln)
	cfg.Sealed = table
	if cfg.Sealed == nil {
		cfg.Sealed = []sealed.Spec{}
	}
	cfg.SealedBin = filepath.Join(t.TempDir(), "no-sealed-bin")
	for _, f := range tune {
		f(cfg)
	}
	ready := make(chan string, 1)
	cfg.Ready = func(a string) { ready <- a }
	st, err := prepare(cfg)
	if err != nil {
		ln.Close()
		t.Fatalf("boot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- st.serve(ctx) }()
	d := &sealedDaemon{t: t, st: st}
	select {
	case addr := <-ready:
		d.base = "http://" + addr
	case err := <-done:
		cancel()
		t.Fatalf("boot ended before serving: %v", err)
	case <-time.After(60 * time.Second):
		cancel()
		t.Fatal("boot never served")
	}
	stopped := false
	d.stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Error("boot did not stop after cancel")
		}
	}
	t.Cleanup(d.stop)
	if !cfg.NoAuth {
		b, err := os.ReadFile(filepath.Join(ws, ".xbin", "token"))
		if err != nil {
			t.Fatalf("the owner token: %v", err)
		}
		d.token = strings.TrimSpace(string(b))
	}
	return d
}

// do sends a request as the owner, or with the bearer token as[0].
func (d *sealedDaemon) do(method, path string, body any, as ...string) (int, string) {
	d.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.base+path, rd)
	if err != nil {
		d.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	tok := d.token
	if len(as) > 0 {
		tok = as[0]
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// addUser makes an account with tile access entries and signs it in.
func (d *sealedDaemon) addUser(id string, tiles map[string]string) (token string) {
	d.t.Helper()
	if code, body := d.do("POST", "/api/xbin/users", map[string]any{"id": id, "name": id, "role": "user", "password": "pw-" + id + "-pw", "tiles": tiles}); code != 200 {
		d.t.Fatalf("adding %s: %d %s", id, code, body)
	}
	code, body := d.do("POST", "/api/xbin/login", map[string]string{"username": id, "password": "pw-" + id + "-pw"}, "")
	var out struct{ Token string }
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.Token == "" {
		d.t.Fatalf("signing %s in: %d %s", id, code, body)
	}
	return out.Token
}

// serving is startSealed for the tests that need only the address.
func serving(t *testing.T, ws string, table []sealed.Spec, tune ...func(*Config)) (addr string, stop func()) {
	t.Helper()
	d := startSealed(t, ws, table, tune...)
	return strings.TrimPrefix(d.base, "http://"), d.stop
}

// prebuilt builds the Go backends of table as a release does (sealed.Build)
// and answers the directory: a production xbind's bin/sealed.
func prebuilt(t *testing.T, table []sealed.Spec) string {
	t.Helper()
	set, err := sealed.Load(table)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := set.Build(sealed.BuildOptions{Out: dir, SDK: "../../sdk"}); err != nil {
		t.Fatalf("building the fixture's backends: %v", err)
	}
	return dir
}

// production makes a config a production xbind's as far as sealed
// components go: auth on, not --dev, its prebuilt backends beside it.
func production(bin string) func(*Config) {
	return func(c *Config) { c.NoAuth, c.Dev, c.SealedBin = false, false, bin }
}

func httpDo(t *testing.T, method, url string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSealedComponentServedAndRunFromEmbeddedSource(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace and compiles a backend")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	// A workspace that already holds a directory at the reserved path, and
	// one at the path whose data key is the sealed scope's (xbin~counter):
	// a hostile or accidental squatter, there before the upgrade.
	squat := map[string]string{
		"xbin/counter/xbin.json":         `{"runtime":"node","chrome":true}`,
		"xbin/counter/index.html":        "<html><body>WORKSPACE COPY</body></html>",
		"xbin/counter/app.js":            "export const version = 'WORKSPACE COPY';\n",
		"xbin/counter/backend/server.js": "process.exit(1)\n",
		"xbin~counter/xbin.json":         `{}`,
		"xbin~counter/scope.json":        `{"resources":{"state":{"type":"kv"}}}`,
	}
	for rel, body := range squat {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rootManifest, _ := os.ReadFile(filepath.Join(ws, "xbin.json"))

	table := func(version string) []sealed.Spec {
		return []sealed.Spec{{Name: "counter", Source: sealedCounter(version), Authored: "apps/counter"}}
	}
	addr, stop := serving(t, ws, table("release-1"))
	base := "http://" + addr

	// 1. the registry row: there, marked sealed, with the table's manifest
	code, body := httpDo(t, "GET", base+"/api/xbin/components", nil)
	if code != 200 || strings.Contains(body, `"xbin/counter"`) {
		t.Fatalf("GET /components lists the sealed component to a caller that didn't ask: %d %s", code, body)
	}
	code, body = httpDo(t, "GET", base+"/api/xbin/components?sealed=1", nil)
	if code != 200 {
		t.Fatalf("GET /components?sealed=1: %d %s", code, body)
	}
	var rows []struct {
		Path, Runtime, Scope, ManifestError string
		Sealed, HasIndex, Chrome            bool
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		switch r.Path {
		case "xbin/counter":
			found = true
			if !r.Sealed || r.Runtime != "go" || !r.HasIndex || r.Chrome || r.Scope != "xbin/counter" || r.ManifestError != "" {
				t.Errorf("the sealed row is not the table's: %+v", r)
			}
		case "xbin~counter":
			// the squatter stays a tile, but the sealed scope holds the data key
			if !strings.Contains(r.ManifestError, "held by scope xbin/counter") {
				t.Errorf("xbin~counter shares the sealed scope's data key unrefused: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("no xbin/counter row in /components: %s", body)
	}

	// 2. the frontend: the embedded page with the one sanctioned transform,
	// and the embedded module byte-exact — never the workspace's copy
	code, body = httpDo(t, "GET", base+"/c/xbin/counter/", nil)
	if code != 200 || !strings.Contains(body, "release-1") || strings.Contains(body, "WORKSPACE COPY") {
		t.Fatalf("GET /c/xbin/counter/: %d %s", code, body)
	}
	if !strings.Contains(body, "xbin-client") || !strings.Contains(body, `content="xbin/counter"`) {
		t.Errorf("the page lacks the D4 injection for xbin/counter:\n%s", body)
	}
	if code, body = httpDo(t, "GET", base+"/c/xbin/counter/app.js", nil); code != 200 || body != "export const version = 'release-1';\n" {
		t.Errorf("GET app.js: %d %q", code, body)
	}
	for _, p := range []string{"/c/xbin/counter/backend/server.js", "/c/xbin/other/", "/c/xbin/"} {
		if code, _ := httpDo(t, "GET", base+p, nil); code == 200 {
			t.Errorf("GET %s served something: the reserved tree is the table's alone", p)
		}
	}

	// 3. the backend: built from the materialized embedded source, run in
	// it, reached at /api/xbin/counter/… with the sealed path as identity
	code, body = httpDo(t, "GET", base+"/api/xbin/counter/hello", nil)
	if code != 200 || !strings.HasPrefix(body, "release-1 self=xbin/counter ") {
		t.Fatalf("GET /api/xbin/counter/hello: %d %s", code, body)
	}
	cwd := body[strings.LastIndex(body, "cwd=")+4:]
	if want := sealed.Dir(ws, "counter"); !strings.HasPrefix(cwd, want+string(filepath.Separator)) {
		t.Errorf("the backend runs in %s, not beneath %s", cwd, want)
	}
	// xbind's own API is still xbind's
	if code, _ := httpDo(t, "GET", base+"/api/xbin/status", nil); code != 200 {
		t.Errorf("GET /api/xbin/status: %d", code)
	}

	// 4. no code or terminal surface, for an admin too (--no-auth: every
	// request is the owner)
	for _, p := range []string{
		"/api/xbin/code/tree?component=xbin/counter",
		"/api/xbin/code/file?component=xbin/counter&path=xbin.json",
		"/api/xbin/git/log?component=xbin/counter",
	} {
		if code, body := httpDo(t, "GET", base+p, nil); code != 400 {
			t.Errorf("GET %s: %d %s, want 400", p, code, body)
		}
	}
	if code, body := httpDo(t, "GET", base+"/ws/term?cwd=xbin/counter", nil); code != 403 {
		t.Errorf("a terminal on the sealed component: %d %s, want 403", code, body)
	}
	if code, body := httpDo(t, "POST", base+"/api/xbin/clone", map[string]string{"from": "xbin/counter", "to": "apps/counter2"}); code != 400 || !strings.Contains(body, "is a system component, managed by xbin") {
		t.Errorf("cloning the sealed component: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(ws, "apps", "counter2")); err == nil {
		t.Error("the clone copied sealed code into the workspace")
	}
	if code, body := httpDo(t, "POST", base+"/api/xbin/lifecycle", map[string]string{"component": "xbin/counter", "state": "offloaded-full"}); code != 400 {
		t.Errorf("offloading the sealed component: %d %s, want 400", code, body)
	}
	owner := auth.Principal{Owner: true}
	if owner.CanTerminalTile("xbin/counter") || owner.CanTerminalTileVia("xbin/counter") || !owner.CanReadTile("xbin/counter") {
		t.Error("the owner's levels on a sealed path: want read, never terminal")
	}
	// its logs are the admins'
	if code, body := httpDo(t, "GET", base+"/api/xbin/logs?component=xbin/counter", nil); code != 200 || !strings.Contains(body, "gen 1 start") {
		t.Errorf("GET /logs: %d %s", code, body)
	}

	// 5. nothing of it is in the workspace tree, and boot wrote no manifest
	if b, _ := os.ReadFile(filepath.Join(ws, "xbin.json")); !bytes.Equal(b, rootManifest) {
		t.Error("the workspace xbin.json changed: a sealed component is declared by the table alone")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "xbin", "counter", "index.html")); !strings.Contains(string(b), "WORKSPACE COPY") {
		t.Error("the workspace's own xbin/counter directory was touched")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "go.work")); strings.Contains(string(b), ".xbin/sealed") {
		t.Errorf("the workspace go.work lists the sealed tree:\n%s", b)
	}
	olds, _ := os.ReadDir(sealed.Dir(ws, "counter"))
	if len(olds) != 1 {
		t.Fatalf("want one materialized tree, got %d", len(olds))
	}
	// a tampered tree is rewritten at the next boot
	tampered := filepath.Join(sealed.Dir(ws, "counter"), olds[0].Name(), "app.js")
	_ = os.Chmod(tampered, 0o644)
	if err := os.WriteFile(tampered, []byte("export const version = 'TAMPERED';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stop()

	// 6. the "upgrade": the same workspace under a binary carrying other
	// code. The page, the module and the backend are the new release's; the
	// old tree is gone.
	addr, stop = serving(t, ws, table("release-1"))
	if code, body = httpDo(t, "GET", "http://"+addr+"/c/xbin/counter/app.js", nil); code != 200 || !strings.Contains(body, "release-1") {
		t.Errorf("after a restart the tampered module is served: %d %q", code, body)
	}
	stop()
	addr, _ = serving(t, ws, table("release-2"))
	base = "http://" + addr
	if code, body = httpDo(t, "GET", base+"/c/xbin/counter/", nil); code != 200 || !strings.Contains(body, "release-2") {
		t.Errorf("after the upgrade the page is not the new release's: %d %s", code, body)
	}
	if code, body = httpDo(t, "GET", base+"/api/xbin/counter/hello", nil); code != 200 || !strings.HasPrefix(body, "release-2 ") {
		t.Errorf("after the upgrade the backend is not the new release's: %d %s", code, body)
	}
	news, _ := os.ReadDir(sealed.Dir(ws, "counter"))
	if len(news) != 1 || news[0].Name() == olds[0].Name() {
		t.Errorf("the old release's tree was kept: %v", news)
	}
}

// A save in a workspace directory at a sealed path is no change of the
// sealed component: the watcher sees top-level xbin/ (it is reserved as an
// id, not ignored as a directory), and nothing may reload or rebuild.
func TestSealedIgnoresWorkspaceSaves(t *testing.T) {
	ws := t.TempDir()
	set, err := sealed.Load([]sealed.Spec{{Name: "counter", Source: sealedCounter("r1")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Materialize(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "apps", "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "apps", "real", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.OpenSealed(ws, set.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := reg.Component("xbin/counter"); !ok || !c.Sealed {
		t.Fatal("the sealed component isn't registered")
	}
	reload, restart := changedComponents(reg, []string{"xbin/counter/index.html", "xbin/counter/backend/main.go", "apps/real/index.html"})
	if len(reload) != 1 || reload["apps/real"] == nil || restart["xbin/counter"] {
		t.Errorf("a workspace save reached the sealed component: reload=%v restart=%v", reload, restart)
	}
}

// A sealed name that is a first segment of xbind's own API can't boot:
// "agent" is taken by /api/xbin/agent/providers and /agent/history.
func TestSealedNameShadowingAPIRefusesBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := testConfig(ws, ln)
	cfg.Sealed = []sealed.Spec{{Name: "agent", Source: sealedCounter("x")}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = Run(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "would shadow xbind's API route") {
		t.Fatalf("boot with a sealed component named agent: %v", err)
	}
}

// The agent template itself hosted sealed: rendered from the release's
// embedded tree, registered at its reserved path with the template's
// manifest (interfaces, resources, partition request) and no manifest
// error, its frontend served. Its backend's build is measured in the
// findings (plans/wsagent/s1-sealed.md); XBIN_SEALED_AGENT_BACKEND=1 builds
// and starts it here too (module downloads on a cold cache).
func TestSealedAgentTemplateHosted(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	src, err := fs.Sub(xbin.BuiltinTemplatesFS(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	addr, _ := serving(t, ws, []sealed.Spec{{Name: "wsagent", Source: src, Authored: "apps/agent", Template: true}}, func(c *Config) {
		if os.Getenv("XBIN_SEALED_AGENT_BACKEND") != "" { // its sqlite is an encrypted resource: a vault, and XBIN_GOCRYPTFS
			c.InsecureVault, c.VaultPassphrase = false, "sealed-test"
		}
	})
	base := "http://" + addr

	code, body := httpDo(t, "GET", base+"/api/xbin/components/xbin/wsagent", nil)
	if code != 200 {
		t.Fatalf("GET /components/xbin/wsagent: %d %s", code, body)
	}
	var one struct {
		Component struct {
			Path, Runtime, ManifestError string
			Sealed, HasIndex, Template   bool
			Uses                         []struct{ Target string }
			Native                       *struct{ Entry string }
		}
		APIDoc string `json:"apiDoc"`
	}
	if err := json.Unmarshal([]byte(body), &one); err != nil {
		t.Fatal(err)
	}
	c := one.Component
	if !c.Sealed || c.Template || c.Runtime != "go" || !c.HasIndex || c.ManifestError != "" || c.Native == nil {
		t.Errorf("the sealed agent's row: %+v", c)
	}
	for _, u := range c.Uses {
		if strings.Contains(u.Target, "apps/agent") {
			t.Errorf("a grant request still names the authored path: %s", u.Target)
		}
	}
	if !strings.Contains(one.APIDoc, "#") {
		t.Error("its API.md isn't read from the materialized tree")
	}
	if code, body = httpDo(t, "GET", base+"/c/xbin/wsagent/", nil); code != 200 || !strings.Contains(body, "xbin-client") {
		t.Fatalf("GET /c/xbin/wsagent/: %d %.200s", code, body)
	}
	if code, _ = httpDo(t, "GET", base+"/c/xbin/wsagent/chat-view.js", nil); code != 200 {
		t.Errorf("GET chat-view.js: %d", code)
	}
	// its resources are provisioned at the sealed scope's data key
	if _, err := os.Stat(filepath.Join(ws, "data", "resources", util.ScopeKey("xbin/wsagent"))); err != nil {
		t.Logf("data/resources/%s: %v (provisioned lazily?)", util.ScopeKey("xbin/wsagent"), err)
	}
	if os.Getenv("XBIN_SEALED_AGENT_BACKEND") == "" {
		return
	}
	start := time.Now()
	code, body = httpDo(t, "GET", base+"/api/xbin/wsagent/api/me", nil)
	t.Logf("first call to the sealed agent backend: %d after %s: %.300s", code, time.Since(start).Round(time.Millisecond), body)
	if code >= 500 {
		t.Errorf("the sealed agent backend did not answer: %d %s", code, body)
	}
	// what one idle instance costs: the resident memory of the backend
	// process this boot started (its binary is under .xbin/build/)
	procs, _ := filepath.Glob("/proc/[0-9]*/exe")
	for _, exe := range procs {
		if target, err := os.Readlink(exe); err == nil && strings.HasPrefix(target, filepath.Join(ws, ".xbin", "build")) {
			status, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "status"))
			for _, ln := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(ln, "VmRSS") || strings.HasPrefix(ln, "RssAnon") || strings.HasPrefix(ln, "Threads") {
					t.Logf("sealed agent backend %s", strings.Join(strings.Fields(ln), " "))
				}
			}
		}
	}
}

// ownCall sends a request with the sealed component's own frame token (the
// owner's frame of it): how its page, and so its people, reach it.
func (d *sealedDaemon) ownCall(component, method, path string, body any) (int, string) {
	d.t.Helper()
	code, tok := d.do("GET", "/api/xbin/frame-token?component="+component, nil)
	var ft struct{ Token string }
	if code != 200 || json.Unmarshal([]byte(tok), &ft) != nil || ft.Token == "" {
		d.t.Fatalf("the frame token of %s: %d %s", component, code, tok)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, d.base+path, rd)
	req.Header.Set("X-XBin-Frame-Token", ft.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (d *sealedDaemon) sealedRow(path string) sealedRow {
	d.t.Helper()
	code, body := d.do("GET", "/api/xbin/sealed", nil)
	var out struct{ Components []sealedRow }
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
		d.t.Fatalf("GET /sealed: %d %s", code, body)
	}
	for _, r := range out.Components {
		if r.Path == path {
			return r
		}
	}
	d.t.Fatalf("GET /sealed has no %s: %s", path, body)
	return sealedRow{}
}

// A production xbind never compiles a sealed component: without the
// release's build of it the component is held, and says so everywhere.
func TestSealedProductionNeverCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	table := []sealed.Spec{{Name: "counter", Source: sealedCounter("r1"), Authored: "apps/counter"}}
	// a bin/sealed holding a build of other source, and nothing else
	other := prebuilt(t, []sealed.Spec{{Name: "counter", Source: sealedCounter("other"), Authored: "apps/counter"}})
	d := startSealed(t, ws, table, production(other))
	code, body := d.do("GET", "/api/xbin/counter/hello", nil)
	if code != 502 || !strings.Contains(body, "this xbin install has no build of it") || !strings.Contains(body, "never compiles") {
		t.Errorf("a call to a component without its build: %d %s", code, body)
	}
	if row := d.sealedRow("xbin/counter"); row.Hold == nil || row.Hold.Code != sealed.HoldNoBuild || row.Build != "none" {
		t.Errorf("GET /sealed: %+v", row)
	}
	if ents, _ := os.ReadDir(filepath.Join(ws, ".xbin", "build")); len(ents) > 0 {
		t.Errorf("a production xbind compiled: %v", ents)
	}
	// its page is still the release's (the frontend needs no build)
	if code, body := d.do("GET", "/c/xbin/counter/app.js", nil); code != 200 || !strings.Contains(body, "r1") {
		t.Errorf("its frontend: %d %s", code, body)
	}
}

// The upgrade and downgrade path of a sealed component, as a production
// xbind: each release ships its own code and build; the data format rises
// with an upgrade's first start; a downgrade below it holds the component
// and touches nothing; a release from before the component leaves its data
// alone; going forward again runs it on the same data.
func TestSealedUpgradeAndDowngrade(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace five times and builds two backends")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	release := func(code string, data int) []sealed.Spec {
		return []sealed.Spec{{Name: "counter", Source: sealedCounter(code), Authored: "apps/counter", Data: data}}
	}
	v1, v2 := release("code-1", 1), release("code-2", 2)
	bin1, bin2 := prebuilt(t, v1), prebuilt(t, v2)
	boot := func(version string, table []sealed.Spec, bin string) *sealedDaemon {
		return startSealed(t, ws, table, production(bin), func(c *Config) { c.Version = version })
	}
	stamp := func() sealed.Stamp {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(ws, "data", "sealed", "counter.json"))
		if err != nil {
			t.Fatal(err)
		}
		var st sealed.Stamp
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	const key = "/api/xbin/kv/res:xbin/counter/state/marker"

	// release 1
	d := boot("v1", v1, bin1)
	if st := stamp(); st.Data != 1 || st.Release != "v1" {
		t.Fatalf("the first boot's stamp: %+v", st)
	}
	if code, body := d.do("GET", "/api/xbin/counter/hello", nil); code != 200 || !strings.HasPrefix(body, "code-1 ") {
		t.Fatalf("release 1: %d %s", code, body)
	}
	if code, body := d.ownCall("xbin/counter", "PUT", key, "written-by-v1"); code != 200 {
		t.Fatalf("its kv write: %d %s", code, body)
	}
	digest1 := d.sealedRow("xbin/counter").Digest
	d.stop()

	// the upgrade: new code, new build, the same data; the stamp rises when
	// the new code first starts, not before
	d = boot("v2", v2, bin2)
	row := d.sealedRow("xbin/counter")
	if row.Digest == digest1 || row.Hold != nil || row.Build != "release" || row.DataFormat != 2 || row.Stamp.Data != 1 {
		t.Errorf("after the upgrade, before a start: %+v", row)
	}
	if code, body := d.do("GET", "/c/xbin/counter/app.js", nil); code != 200 || !strings.Contains(body, "code-2") {
		t.Errorf("the upgraded frontend: %d %s", code, body)
	}
	if code, body := d.do("GET", "/api/xbin/counter/hello", nil); code != 200 || !strings.HasPrefix(body, "code-2 ") {
		t.Fatalf("release 2: %d %s", code, body)
	}
	if st := stamp(); st.Data != 2 || st.Release != "v2" {
		t.Errorf("the stamp after release 2's first start: %+v", st)
	}
	if code, body := d.ownCall("xbin/counter", "GET", key, nil); code != 200 || !strings.Contains(body, "written-by-v1") {
		t.Errorf("release 1's data under release 2: %d %s", code, body)
	}
	if ents, _ := os.ReadDir(sealed.Dir(ws, "counter")); len(ents) != 1 {
		t.Errorf("%d code trees after the upgrade", len(ents))
	}
	d.stop()
	kvBefore, _ := os.ReadFile(filepath.Join(ws, "data", "kv.db"))

	// the downgrade: held, with the reason; nothing of its data is touched
	d = boot("v1", v1, bin1)
	code, body := d.do("GET", "/api/xbin/counter/hello", nil)
	for _, want := range []string{"is held: its data was written by xbin v2 (data format 2)", "this xbin (v1) reads up to format 1", "Nothing was changed"} {
		if code != 502 || !strings.Contains(body, want) {
			t.Errorf("a call after the downgrade: %d %s\n\twant 502 with %q", code, body, want)
		}
	}
	if row := d.sealedRow("xbin/counter"); row.Hold == nil || row.Hold.Code != sealed.HoldNewerData || row.Backend == "healthy" {
		t.Errorf("GET /sealed after the downgrade: %+v", row)
	}
	if code, body := d.ownCall("xbin/counter", "GET", key, nil); code == 200 {
		t.Errorf("held over newer data, its kv is still served: %s", body)
	}
	if _, body := d.do("GET", "/api/xbin/resources?sealed=1", nil); strings.Contains(body, "xbin/counter") {
		t.Errorf("held over newer data, its resources are still declared: %s", body)
	}
	if st := stamp(); st.Data != 2 || st.Release != "v2" {
		t.Errorf("the downgraded release rewrote the stamp: %+v", st)
	}
	d.stop()
	if kvAfter, _ := os.ReadFile(filepath.Join(ws, "data", "kv.db")); !bytes.Equal(kvBefore, kvAfter) {
		t.Error("the workspace's kv store changed while the component was held")
	}

	// a release from before the component: it isn't there, its data is
	d = boot("v0", []sealed.Spec{}, bin1)
	if code, _ := d.do("GET", "/c/xbin/counter/", nil); code == 200 {
		t.Error("a release without the component serves it")
	}
	if code, body := d.do("GET", "/api/xbin/sealed", nil); code != 200 || strings.Contains(body, "xbin/counter") {
		t.Errorf("GET /sealed on a release without it: %d %s", code, body)
	}
	d.stop()
	if st := stamp(); st.Data != 2 {
		t.Errorf("a release without the component touched its stamp: %+v", st)
	}

	// forward again: it runs, on the data it had
	d = boot("v2", v2, bin2)
	if code, body := d.do("GET", "/api/xbin/counter/hello", nil); code != 200 || !strings.HasPrefix(body, "code-2 ") {
		t.Fatalf("release 2 again: %d %s", code, body)
	}
	if code, body := d.ownCall("xbin/counter", "GET", key, nil); code != 200 || !strings.Contains(body, "written-by-v1") {
		t.Errorf("its data after the round trip: %d %s", code, body)
	}
}

// The strict tile asset modes serve a sealed component's frontend as they
// serve a pinned tile's: tokens mode from the asset plane under the
// document's <base>, origins mode on an origin of its own.
func TestSealedFrontendInStrictAssetModes(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace twice")
	}
	quiet(t)
	table := []sealed.Spec{probeSpec()}

	t.Run("tokens", func(t *testing.T) {
		d := startSealed(t, probeWorkspace(t), table, func(c *Config) { c.TileAssets = "tokens" })
		code, body := d.do("GET", "/c/"+probePath+"/", nil)
		m := regexp.MustCompile(`<base href="(/c/~[^"]+/xbin/probe/)"`).FindStringSubmatch(body)
		if code != 200 || m == nil {
			t.Fatalf("its document has no asset <base>: %d %s", code, body)
		}
		if code, body := d.do("GET", m[1]+"app.js", nil, ""); code != 200 || !strings.Contains(body, "r1") {
			t.Errorf("its module through the asset token: %d %s", code, body)
		}
		for _, outside := range []string{"../counter/app.js", "../../apps/plain/index.html", "../../../.xbin/token"} {
			if code, body := d.do("GET", m[1]+outside, nil, ""); code == 200 && strings.Contains(body, "r1") {
				t.Errorf("the asset token reached %s: %s", outside, body)
			}
		}
	})

	t.Run("origins", func(t *testing.T) {
		d := startSealed(t, probeWorkspace(t), table, func(c *Config) {
			c.TileAssets, c.TilesDomain = "origins", "xbin.localhost"
			c.ExternalURL = "http://xbin.localhost:" + c.Listen[strings.LastIndex(c.Listen, ":")+1:]
		})
		code, body := d.do("GET", "/api/xbin/components?sealed=1", nil)
		var rows []struct{ Path, Origin string }
		if code != 200 || json.Unmarshal([]byte(body), &rows) != nil {
			t.Fatalf("GET /components?sealed=1: %d %s", code, body)
		}
		origins := map[string]string{}
		for _, r := range rows {
			origins[r.Path] = r.Origin
		}
		if origins[probePath] == "" || origins[probePath] == origins["apps/plain"] {
			t.Fatalf("its origin: %q (a tile's: %q)", origins[probePath], origins["apps/plain"])
		}
		// on its origin, without the tile's credential: a tile's answer
		at := func(path string) (int, string) {
			req, _ := http.NewRequest("GET", d.base+"/c/"+path+"/app.js", nil)
			req.Host = strings.TrimPrefix(origins[path], "http://")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		sc, sb := at(probePath)
		tc, tb := at("apps/plain")
		if sc != tc || sc == 200 || sb != tb {
			t.Errorf("its origin without a credential answers %d %q; a tile's %d %q", sc, sb, tc, tb)
		}
	})
}
