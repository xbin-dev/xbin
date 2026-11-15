package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// GET /components/<path> carries the tile's API.md to every signed-in
// caller, read with xbind's own privileges out of a directory the tile's
// sandboxes write: an API.md that is a link out of the tile (-> the owner
// token, the users file) reads as no API.md. It was read with os.ReadFile.
func TestComponentAPIDocNeverLeavesTheTile(t *testing.T) {
	s, _ := nativeWorkspace(t)
	root := s.Reg.Root
	if err := os.MkdirAll(filepath.Join(root, ".xbin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".xbin", "token"), []byte("NOT-THE-TILES-owner-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := func(tile string) string {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/xbin/components/"+tile, nil)
		r.SetPathValue("path", tile)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		s.apiComponent(w, r)
		var out struct {
			APIDoc string `json:"apiDoc"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", tile, w.Code, w.Body.String())
		}
		return out.APIDoc
	}
	web := filepath.Join(root, "apps", "web")
	if err := os.WriteFile(filepath.Join(web, "API.md"), []byte("# web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := doc("apps/web"); got != "# web\n" {
		t.Errorf("a tile's own API.md: %q", got)
	}
	// a link that stays inside the tile is the tile's own content
	if err := os.MkdirAll(filepath.Join(root, "apps", "conv", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "conv", "docs", "api.md"), []byte("# conv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs/api.md", filepath.Join(root, "apps", "conv", "API.md")); err != nil {
		t.Fatal(err)
	}
	if got := doc("apps/conv"); got != "# conv\n" {
		t.Errorf("an API.md linked inside its tile: %q", got)
	}
	// one that leaves it is no API.md
	for _, target := range []string{"../../.xbin/token", filepath.Join(root, ".xbin", "token"), "../web/API.md"} {
		p := filepath.Join(root, "apps", "ifc", "API.md")
		_ = os.Remove(p)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if got := doc("apps/ifc"); got != "" || strings.Contains(got, "NOT-THE-TILES") {
			t.Errorf("API.md -> %s: the component's apiDoc is %q", target, got)
		}
	}
}
