package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scope refused for sharing another's data key (D118) never touches that
// key's data: offload keeps the holder's data/resources/<key>, and a
// restore won't write its file resources into the holder's volume.
func TestScopeKeyRefusedLeavesHolderData(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	for rel, content := range map[string]string{
		"apps~calendar/scope.json": `{"resources":{"db":{"type":"sqlite"},"events":{"type":"kv"}}}`,
		"apps~calendar/xbin.json":  `{}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if !b.Reg.HoldsScopeKey("apps/calendar") || b.Reg.HoldsScopeKey("apps~calendar") {
		t.Fatal("apps/calendar existed first and holds the key")
	}
	if sm := b.Reg.Scopes()["apps~calendar"]; sm == nil || len(sm.Resources) != 0 {
		t.Fatalf("refused scope still declares resources: %+v", sm)
	}
	marker := filepath.Join(root, "data", "resources", "apps~calendar", "holder.txt")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("apps/calendar's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.removeScopeData("apps~calendar"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("offloading the refused scope removed the holder's data: %v", err)
	}
	dst := &restoreDst{b: b, comp: "apps~calendar"}
	defer dst.close()
	if _, err := dst.resource("apps~calendar", "db"); err == nil || !strings.Contains(err.Error(), "resource data key") {
		t.Fatalf("restore into a refused scope's key: %v", err)
	}
	if err := b.loadKV("apps~calendar", []byte(`{"events":{"k":"dg=="}}`)); err == nil || !strings.Contains(err.Error(), "resource data key") {
		t.Fatalf("kv restore into a refused scope: %v", err)
	}
}

// Resource names from scope.json never steer a path (D118): the registry
// drops invalid ones, so provisioning never sees them, grants can't address
// them, and a restore naming one is refused.
func TestResourceNamesNeverSteerPaths(t *testing.T) {
	b := testBroker(t)
	p := filepath.Join(b.Reg.Root, "apps", "calendar", "scope.json")
	if err := os.WriteFile(p, []byte(`{"resources":{
		"db":{"type":"sqlite"},
		"../../../../escape":{"type":"filesystem"},
		"x/../..":{"type":"blob"},
		"..":{"type":"filesystem"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	var seen []string
	b.forEachFileRes(func(scope, name, _ string) { seen = append(seen, scope+"|"+name) })
	if len(seen) != 1 || seen[0] != "apps/calendar|db" {
		t.Fatalf("file resources provisioning would see: %v", seen)
	}
	for _, target := range []string{"res:apps/calendar/..", "res:apps/calendar/x/../..", "res:apps/calendar/../../../../escape"} {
		if _, res, _ := b.parseRes(target); res != nil {
			t.Fatalf("%s resolved to a declared resource", target)
		}
	}
	dst := &restoreDst{b: b, comp: "apps/calendar"}
	defer dst.close()
	if _, err := dst.resource("apps/calendar", ".."); err == nil || !strings.Contains(err.Error(), "valid resource name") {
		t.Fatalf("restore with a traversal name: %v", err)
	}
	if err := b.loadKV("apps/calendar", []byte(`{"x/events":{"k":"dg=="}}`)); err == nil || !strings.Contains(err.Error(), "valid resource name") {
		t.Fatalf("kv restore with a slash name: %v", err)
	}
	c, _ := b.Reg.Component("apps/calendar")
	if !strings.Contains(c.ManifestErr, `resource name "../../../../escape" is not allowed`) {
		t.Fatalf("manifest error: %q", c.ManifestErr)
	}
}
