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
	if _, err := b.restoreFileDest("apps~calendar", "db/db.sqlite"); err == nil || !strings.Contains(err.Error(), "resource data key") {
		t.Fatalf("restore into a refused scope's key: %v", err)
	}
	if err := b.loadKV("apps~calendar", []byte(`{"events":{"k":"dg=="}}`)); err == nil || !strings.Contains(err.Error(), "resource data key") {
		t.Fatalf("kv restore into a refused scope: %v", err)
	}
}
