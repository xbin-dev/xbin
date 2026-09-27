package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// runtime "cgi" executed a tile's backend/handler on the host as xbind
// (D117 removed it). A tile that still declares it answers 410 with the
// removal message — and its handler never runs, even for a caller the
// policy admits at admin. (Runner is nil: reaching a backend would panic.)
func TestRemovedCGINeverRuns(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "handler-ran")
	tile := filepath.Join(root, "apps", "old")
	if err := os.MkdirAll(filepath.Join(tile, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"xbin.json":       `{"runtime": "cgi"}`,
		"index.html":      `<p>old</p>`,
		"backend/handler": "#!/bin/sh\ntouch " + marker + "\necho 'Content-Type: text/plain'\necho\necho ran\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(tile, filepath.FromSlash(rel)), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	px := &Proxy{Reg: reg, Policy: func(auth.Principal, *registry.Component) (string, bool) { return "admin", true }}

	for _, target := range []string{"/api/apps/old", "/api/apps/old/", "/api/apps/old/x?y=1"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := httptest.NewRecorder()
			px.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader("{}")))
			if rec.Code != http.StatusGone {
				t.Errorf("%s %s = %d, want 410", method, target, rec.Code)
			}
			var body struct{ Error string }
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if !strings.Contains(body.Error, `runtime "cgi" was removed`) || !strings.HasPrefix(body.Error, "apps/old: ") {
				t.Errorf("%s %s: error %q does not name the tile and the removal", method, target, body.Error)
			}
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the cgi handler ran — tile code executed outside the sandbox")
	}
}
