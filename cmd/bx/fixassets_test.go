package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixAssets(t *testing.T) {
	ws := t.TempDir()
	for rel, c := range map[string]string{
		"xbin.json":         `{}`,
		".xbin/token":       "x",
		"apps/t/xbin.json":  `{}`,
		"apps/t/index.html": `<link rel=stylesheet href="/c/apps/t/s.css"><img src="/c/apps/t/i.png">`,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XBIN_WORKSPACE", ws)
	t.Setenv("XBIN_COMPONENT", "")
	idx := filepath.Join(ws, "apps/t/index.html")
	if err := cmdFix([]string{"assets", "apps/t"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(idx); !strings.Contains(string(b), "/c/apps/t/s.css") {
		t.Fatal("a dry run wrote")
	}
	if err := cmdFix([]string{"assets", "apps/t", "--write"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(idx); string(b) != `<link rel=stylesheet href="s.css"><img src="i.png">` {
		t.Fatalf("rewritten: %s", b)
	}
	for _, bad := range [][]string{{}, {"assets"}, {"assets", "apps/t", "--nope"}, {"assets", "apps/none"}, {"other"}} {
		if err := cmdFix(bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}
