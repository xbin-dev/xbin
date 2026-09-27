package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
)

// Importing a retired builtin (devbox) answers 410 with what replaces it —
// what `bx tile import devbox` prints — with or without a target path.
func TestBuiltinsImportRetired(t *testing.T) {
	b := testBroker(t)
	set, err := builtins.Load(fstest.MapFS{
		"hello/tile.json": {Data: []byte(`{"name":"hello"}`)},
		"hello/xbin.json": {Data: []byte(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	b.SetBuiltins(set)
	for _, body := range []string{`{"name":"devbox"}`, `{"name":"devbox","path":"apps/box"}`} {
		r := httptest.NewRequest("POST", "/builtins/import", strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		b.apiBuiltinsImport(w, r)
		if w.Code != http.StatusGone {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		var out struct{ Error string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"retired", "coding-sandbox", "sandbox-terminal", "/docs/sandbox-manager.md"} {
			if !strings.Contains(out.Error, want) {
				t.Errorf("%s: error %q does not mention %q", body, out.Error, want)
			}
		}
	}
	// An unknown name is still a plain 404.
	r := httptest.NewRequest("POST", "/builtins/import", strings.NewReader(`{"name":"nope"}`))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiBuiltinsImport(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown tile: %d %s", w.Code, w.Body.String())
	}
}
