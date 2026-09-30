package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers PD-13 05§2 — the consent plane's boot wiring (records/F10.md):
// the runner's stop of one person's instance is installed (without it a
// revoked consent stops nothing), and the registry's partition-change hook
// takes every consent naming a partitioned tile that goes, so a new tile
// at its path never inherits what people allowed the old one's code.
func TestPartitionConsentWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/pc/xbin.json":  `{"partition":["user"]}`,
		"apps/pc/index.html": "<!doctype html><html><head></head><body>pc</body></html>\n",
		"apps/pd/xbin.json":  `{"partition":["user"]}`,
		"apps/pd/index.html": "<!doctype html><html><head></head><body>pd</body></html>\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	if !d.st.Broker.PartitionEdgeStopWired() {
		t.Error("boot didn't install the consent plane's stop (SetPartitionEdgeStop): a revocation stops nothing")
	}
	u, err := d.st.Users.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead}}, "password1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := d.st.Users.EnsureUID(u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(ws, "data", "partitions", "consents", uid+".json")
	doc := map[string]any{"schema": 1, "user": "alice", "uid": uid, "edges": map[string]any{
		"apps/pc→apps/pd": map[string]any{"at": "2026-09-30T12:00:00Z"},
		"apps/pd→apps/pc": map[string]any{"at": "2026-09-30T12:00:00Z"},
		"apps/zz→apps/pd": map[string]any{"at": "2026-09-30T12:00:00Z"},
	}}
	raw, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(ws, "apps", "pc")); err != nil {
		t.Fatal(err)
	}
	if err := d.st.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Edges map[string]any }
	if err := json.Unmarshal(raw, &got); err != nil || len(got.Edges) != 1 || got.Edges["apps/zz→apps/pd"] == nil {
		t.Errorf("after apps/pc went, alice's consents: %s (%v); want only apps/zz→apps/pd", raw, err)
	}
}
