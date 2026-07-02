package util

import (
	"strings"
	"testing"
)

func TestSafeJoin(t *testing.T) {
	cases := []struct {
		rel  string
		ok   bool
		want string // cleaned rel when ok
	}{
		{"apps/x/index.html", true, "apps/x/index.html"},
		{"/apps/x", true, "apps/x"},
		{"../etc/passwd", false, ""},
		{"apps/../../etc", false, ""},
		{"apps/./x", true, "apps/x"},
		{"apps//x", true, "apps/x"},
		{"..", false, ""},
	}
	for _, c := range cases {
		_, cleaned, err := SafeJoin("/ws", c.rel)
		if (err == nil) != c.ok {
			t.Errorf("SafeJoin(%q) err=%v want ok=%v", c.rel, err, c.ok)
			continue
		}
		if c.ok && cleaned != c.want {
			t.Errorf("SafeJoin(%q) cleaned=%q want %q", c.rel, cleaned, c.want)
		}
	}
}

func TestComponentPathOK(t *testing.T) {
	good := []string{"apps/x", "root", "lib/ui-kit/button"}
	bad := []string{"", "/abs", "a/../b", ".hidden", "apps/.x", "vendor/x",
		"data/x", "home/x", ".buxon/x", "buxon", "apps/node_modules/x",
		"apps/deps/y", "a\\b"}
	for _, p := range good {
		if !ComponentPathOK(p) {
			t.Errorf("ComponentPathOK(%q) = false, want true", p)
		}
	}
	for _, p := range bad {
		if ComponentPathOK(p) {
			t.Errorf("ComponentPathOK(%q) = true, want false", p)
		}
	}
}

func TestCompKey(t *testing.T) {
	k := CompKey("apps/some/deeply/nested/component/path")
	if len(k) > 34 { // 24 base + "-" + 8 hex + margin
		t.Fatalf("CompKey too long (%d): %s", len(k), k)
	}
	if strings.ContainsAny(k, "/\\") {
		t.Fatalf("CompKey not fs-safe: %s", k)
	}
	if CompKey("a/b") == CompKey("a~b") {
		t.Fatal("distinct paths must not collide just via separator mangling")
	}
	if CompKey("x") != CompKey("x") {
		t.Fatal("CompKey must be deterministic")
	}
}
