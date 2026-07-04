package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	// A source tree on disk, including a dir to exclude.
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "backend"), 0o755)
	os.WriteFile(filepath.Join(src, "buxon.json"), []byte(`{"runtime":"go"}`), 0o644)
	os.WriteFile(filepath.Join(src, "backend", "main.go"), []byte("package main"), 0o644)
	os.MkdirAll(filepath.Join(src, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(src, "node_modules", "junk"), []byte("x"), 0o644)

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Manifest(Manifest{Component: "apps/thing", Scope: "apps/thing", ScopeRoot: true,
		Resources: map[string]string{"db": "sqlite"}, Includes: []string{"source", "data"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Tree(SourcePrefix, src, func(rel string) bool { return rel == "node_modules" }); err != nil {
		t.Fatal(err)
	}
	if err := w.File(KVName, 0o644, []byte(`{"state":{"k":"dg=="}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if r.M.Component != "apps/thing" || !r.M.ScopeRoot || r.M.Resources["db"] != "sqlite" {
		t.Fatalf("manifest lost fidelity: %+v", r.M)
	}
	if !r.M.Has("source") || r.M.Has("term-env") {
		t.Fatalf("includes wrong: %v", r.M.Includes)
	}
	got := map[string]string{}
	for {
		name, rd, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rd)
		got[name] = string(b)
	}
	if got["source/buxon.json"] != `{"runtime":"go"}` {
		t.Errorf("source/buxon.json = %q", got["source/buxon.json"])
	}
	if got["source/backend/main.go"] != "package main" {
		t.Errorf("nested source file lost: %q", got["source/backend/main.go"])
	}
	if _, ok := got["source/node_modules/junk"]; ok {
		t.Errorf("skip() did not exclude node_modules")
	}
	if got[KVName] == "" {
		t.Errorf("kv.json missing")
	}
}

func TestSafeJoin(t *testing.T) {
	base := "/ws/apps/x"
	if p := SafeJoin(base, "backend/main.go"); p != filepath.Join(base, "backend/main.go") {
		t.Errorf("normal join = %q", p)
	}
	// Traversal must be neutralised — the result always stays within base.
	for _, evil := range []string{"../../etc/passwd", "a/../../b", "../x", "/abs"} {
		p := SafeJoin(base, evil)
		if p != base && !strings.HasPrefix(p, base+string(filepath.Separator)) {
			t.Errorf("traversal %q escaped base: %q", evil, p)
		}
	}
}
