package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

func TestHomeKey(t *testing.T) {
	if k := HomeKey(auth.Principal{Owner: true}); k != "owner" {
		t.Fatalf("token principal: %q", k)
	}
	if k := HomeKey(auth.Principal{UserID: "magik", User: &users.User{ID: "magik"}}); k != "magik" {
		t.Fatalf("user principal: %q", k)
	}
	// Hostile ids can't escape homes/ or collide with . / ..
	for id, want := range map[string]string{
		"../x": ".._x", "a/b": "a_b", "..": "owner", ".": "owner", "": "owner",
	} {
		if got := sanitizeHomeKey(id); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", id, got, want)
		}
	}
}

// pristineIs treats the given name→content pairs as the template skeleton.
func pristineIs(files map[string]string) func(string, []byte) bool {
	return func(rel string, data []byte) bool { return files[rel] == string(data) }
}

func TestMigrateHomesLegacy(t *testing.T) {
	root := t.TempDir()
	// Legacy shared home with real user data.
	if err := os.MkdirAll(filepath.Join(root, "home", ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "home", ".zshrc"), []byte("custom"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".xbin/\ndata/\nhome/\n"), 0o644)

	moved, err := MigrateHomes(root, "magik", pristineIs(nil))
	if err != nil {
		t.Fatal(err)
	}
	if moved != HomeDir(root, "magik") {
		t.Fatalf("moved = %q", moved)
	}
	// Data preserved at the new location; old form gone.
	if b, _ := os.ReadFile(filepath.Join(moved, ".zshrc")); string(b) != "custom" {
		t.Fatal("dotfile lost in migration")
	}
	if _, err := os.Stat(filepath.Join(moved, ".claude")); err != nil {
		t.Fatal("agent config dir lost in migration")
	}
	if _, err := os.Stat(filepath.Join(root, "home")); !os.IsNotExist(err) {
		t.Fatal("legacy home/ should be gone")
	}
	// .gitignore now covers homes/.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(gi), "homes/") {
		t.Fatalf("gitignore missing homes/: %q", gi)
	}
	// Idempotent second run; gitignore not duplicated.
	if _, err := MigrateHomes(root, "magik", pristineIs(nil)); err != nil {
		t.Fatal(err)
	}
	gi2, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Count(string(gi2), "homes/") != 1 {
		t.Fatalf("gitignore homes/ duplicated: %q", gi2)
	}
}

func TestMigrateHomesFresh(t *testing.T) {
	root := t.TempDir()
	if _, err := MigrateHomes(root, "whoever", pristineIs(nil)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "homes")); err != nil || !fi.IsDir() {
		t.Fatal("homes/ should exist after migration on a fresh workspace")
	}
}

func TestMigrateHomesBothForms(t *testing.T) {
	skel := map[string]string{".zshrc": "skel-zshrc"}

	// Pristine legacy home (recreated by an old xbind's backfill) → removed.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "homes", "magik"), 0o755)
	os.MkdirAll(filepath.Join(root, "home"), 0o755)
	os.WriteFile(filepath.Join(root, "home", ".zshrc"), []byte("skel-zshrc"), 0o644)
	if _, err := MigrateHomes(root, "magik", pristineIs(skel)); err != nil {
		t.Fatalf("pristine both-forms should clean up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "home")); !os.IsNotExist(err) {
		t.Fatal("pristine legacy home should be removed")
	}

	// Legacy home with REAL data alongside homes/ → refuse (never guess).
	root2 := t.TempDir()
	os.MkdirAll(filepath.Join(root2, "homes", "magik"), 0o755)
	os.MkdirAll(filepath.Join(root2, "home"), 0o755)
	os.WriteFile(filepath.Join(root2, "home", ".zshrc"), []byte("edited by hand"), 0o644)
	if _, err := MigrateHomes(root2, "magik", pristineIs(skel)); err == nil {
		t.Fatal("both forms with real data must be an error (startup bail)")
	}
	if _, err := os.Stat(filepath.Join(root2, "home", ".zshrc")); err != nil {
		t.Fatal("bail must not touch the data")
	}
}
