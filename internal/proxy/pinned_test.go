package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
)

// covers D119e T16 — the runtime a request is served with comes from the
// primary's own code: while apps/t's primary is pinned, a work-tree edit of
// runtime (node → static, → go, → the removed cgi) or template never
// changes what /api/apps/t/ runs as or whether it answers — the gates read
// the pinned checkpoint the registry composes, and the runner is handed that
// code. A pinned primary without a backend stays without one whatever the
// work tree declares, and one whose code isn't prepared has none (it fails
// closed). The same work tree on a zero-state twin answers as today.
func TestProxyRuntimeFromDeployment(t *testing.T) {
	root := t.TempDir()
	put := func(dir, rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(root, "xbin.json", `{}`)
	put(root, "apps/t/xbin.json", `{"runtime":"node"}`)
	put(root, "apps/t/backend/server.js", `// never runs`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run := runner.New(root, nil, events.NewHub(), reg)
	run.ShouldRun = func(string) bool { return false } // the runner answers what it was handed, and starts nothing
	px := &Proxy{Reg: reg, Runner: run}

	pinned := map[string]*registry.PinnedCode{}
	reg.PinnedPrimary = func(rel string) (*registry.PinnedCode, bool) {
		pc, ok := pinned[rel]
		return pc, ok
	}
	pin := func(manifest string) {
		t.Helper()
		ckpt := t.TempDir()
		put(ckpt, "xbin.json", manifest)
		put(ckpt, "backend/server.js", `// never runs`)
		pc, err := registry.ReadCheckpoint(ckpt)
		if err != nil {
			t.Fatal(err)
		}
		pinned["apps/t"] = pc
	}
	edit := func(manifest string) {
		t.Helper()
		put(root, "apps/t/xbin.json", manifest)
		if err := reg.Rescan(); err != nil {
			t.Fatal(err)
		}
	}
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/apps/t/x", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true, Via: "bearer"}))
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, r)
		return rec
	}
	// reachesBackend: the gates passed and the runner was handed a backend
	// runtime (it refuses only for ShouldRun, after the runtime check).
	reachesBackend := func(label string) {
		t.Helper()
		if rec := call(); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "is not enabled") {
			t.Errorf("%s: %d %s, want the pinned node backend reached", label, rec.Code, rec.Body.String())
		}
	}
	refused := func(label string, code int, why string) {
		t.Helper()
		if rec := call(); rec.Code != code || !strings.Contains(rec.Body.String(), why) {
			t.Errorf("%s: %d %s, want %d naming %q", label, rec.Code, rec.Body.String(), code, why)
		}
	}

	reachesBackend("zero state, node")
	edit(`{}`)
	refused("zero state, the work tree went static", http.StatusNotFound, "has no backend")

	// Paused on node: the work tree's runtime no longer decides.
	pin(`{"runtime":"node"}`)
	for _, m := range []string{`{}`, `{"runtime":"static"}`, `{"runtime":"go"}`, `{"runtime":"cgi"}`, `{"template":{"title":"x"}}`, `{"runtime":`} {
		edit(m)
		reachesBackend("pinned node, work tree " + m)
	}

	// Pinned on static code: no backend, whatever the work tree declares.
	pin(`{}`)
	edit(`{"runtime":"go"}`)
	refused("pinned static, work tree go", http.StatusNotFound, `has no backend (runtime \"\")`)

	// Pinned on a template: the template gate follows the primary.
	pin(`{"runtime":"node","template":{"title":"x"}}`)
	edit(`{"runtime":"node"}`)
	refused("pinned template", http.StatusNotFound, "is a template")

	// Pinned on removed-runtime code: 410, whatever the work tree says.
	pin(`{"runtime":"cgi"}`)
	edit(`{"runtime":"node"}`)
	refused("pinned cgi", http.StatusGone, `runtime \"cgi\" was removed`)

	// A pinned primary whose code isn't prepared composes with no backend.
	pinned["apps/t"] = &registry.PinnedCode{ManifestErr: "apps/t: the primary (main) is pinned to checkpoint c:1, which isn't prepared"}
	edit(`{"runtime":"node"}`)
	refused("pinned, not prepared", http.StatusNotFound, "has no backend")

	// Unpinned again: the work tree's runtime, as today.
	delete(pinned, "apps/t")
	edit(`{"runtime":"node"}`)
	reachesBackend("unpinned, node")
}
