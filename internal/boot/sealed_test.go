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
		"xbin.json":   {Data: []byte(`{"runtime":"go","title":"Sealed counter","expose":{"roles":{"reader":"read","writer":"write"}}}`)},
		"scope.json":  {Data: []byte(`{"resources":{"state":{"type":"kv"}}}`)},
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

// serving boots ws in-process with the given sealed table and returns its
// address; the daemon stops when the test ends (or stop is called).
func serving(t *testing.T, ws string, table []sealed.Spec, tune ...func(*Config)) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(ws, ln)
	cfg.Sealed = table
	for _, f := range tune {
		f(cfg)
	}
	ready := make(chan string, 1)
	cfg.Ready = func(a string) { ready <- a }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	select {
	case addr = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("boot ended before serving: %v", err)
	case <-time.After(60 * time.Second):
		cancel()
		t.Fatal("boot never served")
	}
	stopped := false
	stop = func() {
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
	t.Cleanup(stop)
	return addr, stop
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
	if code != 200 {
		t.Fatalf("GET /components: %d %s", code, body)
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
	if code, body := httpDo(t, "POST", base+"/api/xbin/clone", map[string]string{"from": "xbin/counter", "to": "apps/counter2"}); code != 400 || !strings.Contains(body, "part of xbin itself") {
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
