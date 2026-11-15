package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// The code API reads a tile's files with xbind's own privileges, out of a
// directory the tile's sandboxes write: nothing it answers comes from
// outside the tile. GET /code/file once joined the path and read it, so a
// link planted in the tile (API.md -> ../../.xbin/token, a directory ->
// ../../data, an absolute path) handed whoever may read the tile's code the
// owner token, the users file, another person's home, another tile's source
// or any host file xbind can read; and ?component= took a path through such
// a link as the component itself.
func TestCodeAPINeverLeavesTheTile(t *testing.T) {
	b := testBroker(t) // apps/calendar, apps/email
	root := b.Reg.Root
	tile := filepath.Join(root, "apps", "calendar")
	outside := filepath.Join(t.TempDir(), "host.txt")
	put := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const marker = "NOT-THE-TILES-"
	put(filepath.Join(root, ".xbin", "token"), marker+"owner-token\n")
	put(filepath.Join(root, "data", "users.json"), `{"`+marker+`users":1}`)
	put(filepath.Join(root, "homes", "ann", "notes.txt"), marker+"home\n")
	put(filepath.Join(root, "apps", "email", "secret.txt"), marker+"other-tile\n")
	put(outside, marker+"host\n")
	put(filepath.Join(tile, "AGENTS.md"), "the tile's own notes\n")
	put(filepath.Join(tile, "sub", "own.txt"), "own\n")
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(tile, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	link("../../.xbin/token", "API.md")
	link("../../data/users.json", "L-users")
	link("../../homes/ann/notes.txt", "L-home")
	link("../email/secret.txt", "L-email")
	link(outside, "L-abs")
	link("../../data", "L-data")   // a directory out of the tile
	link("../email", "L-emaildir") // another tile's directory
	link("../../../.xbin/token", "sub/L-deep")
	link("API.md", "L-chain")      // a link to a link that leaves
	link("AGENTS.md", "CLAUDE.md") // stays inside the tile
	link("sub", "L-sub")           // a directory inside the tile
	if err := syscall.Mkfifo(filepath.Join(tile, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}

	owner := auth.Principal{Owner: true} // the widest reader there is
	get := func(h http.HandlerFunc, url string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); h(w, r) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("GET %s never answered", url)
		}
		return w.Code, w.Body.String()
	}
	file := func(comp, name string) (int, string) {
		return get(b.apiCodeFile, "/code/file?component="+comp+"&file="+name)
	}

	// nothing from outside the tile, by a file link, a directory link, a
	// chain, an absolute target — or a FIFO, which would hang the read
	for _, name := range []string{"API.md", "L-users", "L-home", "L-email", "L-abs", "L-chain", "sub/L-deep",
		"L-data/users.json", "L-emaildir/secret.txt", "L-emaildir/xbin.json", "fifo"} {
		code, body := file("apps/calendar", name)
		if code != http.StatusNotFound || strings.Contains(body, marker) {
			t.Errorf("file=%s: %d %s, want 404 and nothing of the target", name, code, body)
		}
	}
	// nor with the link as (part of) the component path, on any route
	for _, comp := range []string{"apps/calendar/L-data", "apps/calendar/L-emaildir"} {
		if code, body := file(comp, "users.json"); code != http.StatusNotFound || strings.Contains(body, marker) {
			t.Errorf("component=%s file=users.json: %d %s, want 404", comp, code, body)
		}
		if code, body := file(comp, "secret.txt"); code != http.StatusNotFound || strings.Contains(body, marker) {
			t.Errorf("component=%s file=secret.txt: %d %s, want 404", comp, code, body)
		}
		for name, h := range map[string]http.HandlerFunc{"/code/tree": b.apiCodeTree, "/git/log": b.apiGitLog, "/git/diff": b.apiGitDiff, "/git/activity": b.apiGitActivity} {
			if code, body := get(h, name+"?component="+comp); code != http.StatusNotFound {
				t.Errorf("%s?component=%s: %d %s, want 404", name, comp, code, body)
			}
		}
	}

	// the tile's own files read as before — through a link that stays inside
	// the tile too
	for name, want := range map[string]string{"AGENTS.md": "the tile's own notes\n", "CLAUDE.md": "the tile's own notes\n",
		"sub/own.txt": "own\n", "L-sub/own.txt": "own\n"} {
		code, body := file("apps/calendar", name)
		var out struct{ Content string }
		if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.Content != want {
			t.Errorf("file=%s: %d %s, want its content", name, code, body)
		}
	}
	if code, _ := file("apps/calendar", "../email/secret.txt"); code != http.StatusBadRequest {
		t.Errorf("a path with ..: %d, want 400", code)
	}
	if code, _ := file("apps/calendar", "sub"); code != http.StatusNotFound {
		t.Errorf("a directory as the file: %d, want 404", code)
	}

	// the tree lists the tile's regular files: no link, no FIFO, nothing
	// from where a link points
	code, body := get(b.apiCodeTree, "/code/tree?component=apps/calendar")
	var tree struct{ Files []struct{ Path string } }
	if code != 200 || json.Unmarshal([]byte(body), &tree) != nil {
		t.Fatalf("the tree: %d %s", code, body)
	}
	listed := map[string]bool{}
	for _, f := range tree.Files {
		listed[f.Path] = true
		if strings.HasPrefix(filepath.Base(f.Path), "L-") || strings.HasPrefix(f.Path, "L-") || f.Path == "API.md" || f.Path == "CLAUDE.md" || f.Path == "fifo" {
			t.Errorf("the tree lists %s", f.Path)
		}
	}
	if !listed["AGENTS.md"] || !listed["sub/own.txt"] || !listed["xbin.json"] {
		t.Errorf("the tree lost the tile's own files: %v", tree.Files)
	}
}
