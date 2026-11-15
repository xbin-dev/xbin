package broker

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// GET /code/file reads a tile's files as xbind, for the tile's readers and
// for the tile's own code. A tile's directory is written by its sandbox, so
// a file there may be a link: the read must never follow one out of the
// tile. (It did: `API.md -> ../../data/users.json` handed any reader of
// the tile — and the tile's own backend, with its instance token — any file
// xbind can read, the root token's among them.)
func TestCodeFileNeverLeavesTheTile(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	secret := filepath.Join(w.root, "data", "secret.json")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(`{"token":"s3cr3t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tile := filepath.Join(w.root, "apps", "x")
	w.write(map[string]string{"apps/x/real.md": "the tile's own file", "apps/x/sub/inner.md": "inner", "apps/pg/private.md": "another tile's"})
	for link, target := range map[string]string{
		"API.md":       "../../data/secret.json",         // a file link out of the tile
		"abs.md":       secret,                           // an absolute one
		"d":            "../../data",                     // a directory link: d/secret.json
		"sub/up.md":    "../../../data/secret.json",      // from a sub-directory
		"other.md":     "../pg/private.md",               // into another tile
		"chain.md":     "API.md",                         // a link to a link
		"own-link.md":  "real.md",                        // inside the tile: fine
		"own-link2.md": filepath.Join("sub", "inner.md"), // inside the tile: fine
	} {
		if err := os.Symlink(target, filepath.Join(tile, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(tile, "pipe.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(p auth.Principal, file string) (int, string) {
		rec := call(t, b.apiCodeFile, p, "GET", "/code/file?component=apps/x&file="+file, "", nil)
		return rec.Code, rec.Body.String()
	}
	callers := map[string]auth.Principal{
		"the tile's own backend": {Component: "apps/x", Via: "instance"},
		"a reader of the tile":   principalFor(t, w.b.Users, "alice"),
		"an admin":               principalFor(t, w.b.Users, "bob"),
	}
	for who, p := range callers {
		for _, file := range []string{"API.md", "abs.md", "d/secret.json", "sub/up.md", "other.md", "chain.md", "pipe.md"} {
			if code, body := read(p, file); code == 200 || strings.Contains(body, "s3cr3t") || strings.Contains(body, "another tile's") {
				t.Errorf("%s reads %s: %d %s", who, file, code, body)
			}
		}
		for file, want := range map[string]string{"real.md": "the tile's own file", "sub/inner.md": "inner", "own-link.md": "the tile's own file", "own-link2.md": "inner"} {
			if code, body := read(p, file); code != 200 || !strings.Contains(body, want) {
				t.Errorf("%s reads the tile's own %s: %d %s", who, file, code, body)
			}
		}
	}
}
