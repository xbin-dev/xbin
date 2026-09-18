package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestFilePathValidation(t *testing.T) {
	bad := []string{"", "  ", "../x", "/abs", "a//b", "a/../b", ".", "..", "a/./b", "x\x00y",
		strings.Repeat("a", 200), "a b", "a?b", "-lead"}
	for _, p := range bad {
		if got, err := normReplPath(p); err == nil {
			t.Fatalf("path %q should be rejected, normalized to %q", p, got)
		}
	}
	good := map[string]string{
		"a.js": "a.js", "./a.js": "a.js", "data/rows.json": "data/rows.json",
		"A_b-c.2.html": "A_b-c.2.html", " x.js ": "x.js",
	}
	for in, want := range good {
		got, err := normReplPath(in)
		if err != nil || got != want {
			t.Fatalf("path %q → %q, %v (want %q)", in, got, err, want)
		}
	}
}

func TestFileCaps(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.replPutFile(1, "big.txt", strings.Repeat("x", maxReplFileBytes+1), 0); err == nil {
		t.Fatal("an oversized file should be rejected")
	}
	for i := 0; i < maxReplFiles; i++ {
		if _, err := db.replPutFile(1, fmt.Sprintf("f%d.js", i), "x", 0); err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	if _, err := db.replPutFile(1, "one-too-many.js", "x", 0); err == nil {
		t.Fatal("exceeding the per-run file count should be rejected")
	}
	// Overwriting an existing file is still fine at the cap.
	if _, err := db.replPutFile(1, "f0.js", "updated", 0); err != nil {
		t.Fatalf("overwrite at the cap should work: %v", err)
	}
}

func TestFileVersioning(t *testing.T) {
	db := newTestDB(t)
	f, err := db.replPutFile(1, "a.js", "one", 0)
	if err != nil || f.Version != 1 {
		t.Fatalf("first write: v%d %v", f.Version, err)
	}
	f, _ = db.replPutFile(1, "a.js", "two", 0)
	if f.Version != 2 {
		t.Fatalf("second write should be v2, got v%d", f.Version)
	}
	if _, err := db.replPutFile(1, "a.js", "three", 1); err == nil {
		t.Fatal("a stale version should be refused (optimistic concurrency)")
	}
	if _, err := db.replPutFile(1, "a.js", "three", 2); err != nil {
		t.Fatalf("the current version should be accepted: %v", err)
	}
}

func TestFileToolGatingAndLanes(t *testing.T) {
	has := func(specs []toolSpec, name string) bool {
		for _, s := range specs {
			if s.Function.Name == name {
				return true
			}
		}
		return false
	}
	// Default-on, like every other feature key.
	for _, lane := range []string{"", "web"} {
		specs := toolSpecs(Config{Toolset: lane}, nil)
		for name := range fileToolNames {
			if !has(specs, name) {
				t.Fatalf("toolset %q: %s missing — session files have no egress and no internal "+
					"reach, so they belong in both lanes", lane, name)
			}
		}
	}
	off := toolSpecs(Config{Features: map[string]bool{"files": false}}, nil)
	for name := range fileToolNames {
		if has(off, name) {
			t.Fatalf("files:false should hide %s", name)
		}
	}
}

func TestFileToolsAreNotSideEffecting(t *testing.T) {
	for name := range fileToolNames {
		if sideEffect(name) {
			t.Fatalf("%s should not require approval", name)
		}
	}
}

func TestRenderHTMLRequiresHTMLFile(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	if _, err := db.replPutFile(id, "notes.txt", "hi", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.runFileTool(context.Background(), run, Config{}, "render_html",
		map[string]any{"path": "notes.txt"}); err == nil {
		t.Fatal("render_html should refuse a non-HTML file")
	}
	if _, err := db.replPutFile(id, "r.html", "<h1>hi</h1>", 0); err != nil {
		t.Fatal(err)
	}
	out, err := ag.runFileTool(context.Background(), run, Config{}, "render_html",
		map[string]any{"path": "r.html"})
	if err != nil {
		t.Fatalf("render_html: %v", err)
	}
	if !strings.Contains(out, "static") {
		t.Fatalf("the result should remind the model the frame is static, got %q", out)
	}
	steps, _ := db.steps(id)
	var found bool
	for _, s := range steps {
		if s.Kind == "render" && strings.Contains(s.Detail, "r.html") {
			found = true
		}
	}
	if !found {
		t.Fatal("render_html must journal a 'render' step — that is how the tile sees it")
	}
}

func TestFileEditSemantics(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	if _, err := db.replPutFile(1, "a.js", "one\ntwo\none\n", 0); err != nil {
		t.Fatal(err)
	}
	// 2 matches without replace_all is an error, not a coin flip.
	if _, err := ag.fileEdit(1, map[string]any{"path": "a.js", "old_string": "one", "new_string": "1"}); err == nil {
		t.Fatal("an ambiguous old_string should be refused")
	}
	if _, err := ag.fileEdit(1, map[string]any{"path": "a.js", "old_string": "one", "new_string": "1", "replace_all": true}); err != nil {
		t.Fatal(err)
	}
	f, _ := db.replFile(1, "a.js")
	if f.Content != "1\ntwo\n1\n" {
		t.Fatalf("replace_all: %q", f.Content)
	}
	// A whitespace near-miss should say so rather than just "not found".
	_, err := ag.fileEdit(1, map[string]any{"path": "a.js", "old_string": "1    two", "new_string": "x"})
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("expected a whitespace hint, got %v", err)
	}
}

func TestFileReadRange(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	if _, err := db.replPutFile(id, "a.txt", "l1\nl2\nl3\nl4\nl5", 0); err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) string {
		out, err := ag.runFileTool(context.Background(), run, Config{}, "file_read", args)
		if err != nil {
			t.Fatalf("file_read %v: %v", args, err)
		}
		return out
	}
	if got := call(map[string]any{"path": "a.txt"}); got != "l1\nl2\nl3\nl4\nl5" {
		t.Fatalf("whole file: %q", got)
	}
	got := call(map[string]any{"path": "a.txt", "offset": float64(2), "limit": float64(2)})
	if !strings.Contains(got, "l2\nl3") {
		t.Fatalf("window: %q", got)
	}
	// Both ends must say what was omitted, or the model cannot tell a window
	// from a whole file.
	if !strings.Contains(got, "lines 1-1 not shown") || !strings.Contains(got, "lines 4-5 not shown") {
		t.Fatalf("elision markers missing: %q", got)
	}
	if got := call(map[string]any{"path": "a.txt", "offset": float64(99)}); !strings.Contains(got, "past the end") {
		t.Fatalf("past-end: %q", got)
	}
}
