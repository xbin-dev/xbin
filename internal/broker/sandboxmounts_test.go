package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// A tile sandbox mounts only a filesystem resource of its tile's own scope
// that the tile holds; its role rides along (a reader's mount is read-only).
func TestResourceMount(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1,"grants":[{"from":"apps/other","target":"res:apps/mgr/work","role":"writer"},
		{"from":"apps/mgr/left","target":"res:apps/mgr/work","role":"writer"}]}`)
	write("apps/mgr/scope.json", `{"resources":{"work":{"type":"filesystem"},"cache":{"type":"filesystem"},
		"db":{"type":"sqlite"},"kv":{"type":"kv"}}}`)
	write("apps/mgr/xbin.json", `{"runtime":"go","uses":[
		{"target":"res:apps/mgr/work","role":"writer"},
		{"target":"res:apps/mgr/cache","role":"reader"},
		{"target":"res:apps/mgr/db","role":"writer"},
		{"target":"res:apps/mgr/kv","role":"writer"}]}`)
	write("apps/mgr/other/xbin.json", `{"runtime":"go"}`)
	write("apps/mgr/left/xbin.json", `{"runtime":"go"}`) // a grant left over after its uses entry went
	write("apps/other/xbin.json", `{"runtime":"go","uses":[{"target":"res:apps/mgr/work","role":"writer"}]}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	if b.Users, err = users.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	m, err := b.ResourceMount("apps/mgr", "res:apps/mgr/work")
	if err != nil || m.Role != "writer" || m.Kind != "filesystem" || !m.Encrypted || m.Ready ||
		m.Src != b.fsResPath("apps/mgr", "work", false) {
		t.Fatalf("own writer resource: %+v %v", m, err)
	}
	if m, err := b.ResourceMount("apps/mgr", "res:apps/mgr/cache"); err != nil || m.Role != "reader" {
		t.Fatalf("own reader resource: %+v %v", m, err)
	}
	for _, c := range []struct{ tile, res, why string }{
		{"apps/mgr", "res:apps/mgr/db", "only filesystem"},
		{"apps/mgr", "res:apps/mgr/kv", "only filesystem"},
		{"apps/mgr", "res:apps/mgr/nope", "not a declared resource"},
		{"apps/mgr", "apps/mgr", "not a declared resource"},
		{"apps/mgr/other", "res:apps/mgr/work", "declare it in uses"},         // same scope, not declared
		{"apps/mgr/left", "res:apps/mgr/work", "declare it in uses"},          // same scope, granted, not declared
		{"apps/other", "res:apps/mgr/work", "another scope"},                  // granted, but another scope's
		{"apps/gone", "res:apps/mgr/work", "no tile"},                         // no such tile
		{"apps/mgr", "res:workspace/x", "not a declared resource"},            // no workspace resource
		{"apps/mgr", "res:apps/mgr/work/../cache", "not a declared resource"}, // no path games
	} {
		if _, err := b.ResourceMount(c.tile, c.res); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s %s: %v, want %q", c.tile, c.res, err, c.why)
		}
	}
}
