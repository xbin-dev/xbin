package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")

	// A fresh file lands with the requested mode, not CreateTemp's 0600.
	if err := WriteFileAtomic(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", st.Mode().Perm())
	}
	// A rewrite replaces the content and keeps the directory clean.
	if err := WriteFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Errorf("content = %q", b)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode after rewrite = %o, want 600", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1", len(entries))
	}

	// A failed write leaves the old file untouched and no temp behind.
	if err := WriteFileAtomic(filepath.Join(dir, "nope", "x"), []byte("x"), 0o644); err == nil {
		t.Error("write into a missing directory succeeded")
	}
	b, _ = os.ReadFile(p)
	if string(b) != "two" {
		t.Errorf("old content changed after a failed write: %q", b)
	}

	// WriteFileAtomicIn creates the parent.
	q := filepath.Join(dir, "deep", "er", "s.json")
	if err := WriteFileAtomicIn(q, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(q); string(b) != "{}" {
		t.Errorf("nested write = %q", b)
	}
}
