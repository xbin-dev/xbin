package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-51 — the stores "holds data" reads, one by one: main's plaintext
// resource directory, deployment namespaces, a deployment's vault and
// registration files, main's bus subscriptions, interface instances,
// ingress hosts, an offloaded tile; a nested scope's kv buckets aren't the
// tile's.
func TestTileHoldsData(t *testing.T) {
	w := newPartWS(t, map[string]string{
		"apps/x/scope.json":     `{"resources":{"db":{"type":"kv"}}}`,
		"apps/x/xbin.json":      `{"runtime":"go"}`,
		"apps/x/sub/scope.json": `{"resources":{"db":{"type":"kv"}}}`,
		"apps/x/sub/xbin.json":  `{"runtime":"go"}`,
	}, nil)
	ask := registry.PartitionAsk{Tile: "apps/x", Scope: "apps/x", RootsScope: true}
	holds := func() string {
		t.Helper()
		held, s, sealed := w.b.tileHoldsData(ask)
		if held != (s != "") || sealed {
			t.Fatalf("tileHoldsData: held %v, store %q, sealed %v", held, s, sealed)
		}
		return s
	}
	if s := holds(); s != "" {
		t.Fatalf("an empty tile holds data in %s", s)
	}
	w.kvPut("apps/x/sub", "db")
	if s := holds(); s != "" {
		t.Errorf("a nested scope's key counted for its parent (%s)", s)
	}
	// main's plaintext resource directory (the vault-off layout).
	main, _ := scopeKeys("apps/x", util.MainDeployment)
	plain := filepath.Join(w.root, filepath.FromSlash(main.Plain))
	w.write(map[string]string{mustRel(t, w.root, filepath.Join(plain, "files", "notes.txt")): "x"})
	if s := holds(); s != "namespaces" {
		t.Errorf("a plaintext resource directory's entry: %q", s)
	}
	if err := os.RemoveAll(plain); err != nil {
		t.Fatal(err)
	}
	// A deployment namespace's volume with a file.
	vol := filepath.Join(w.root, "data", "resources-enc", deploymentsLevel, escS("apps/x"), "dev", "fs", "files")
	w.write(map[string]string{mustRel(t, w.root, filepath.Join(vol, "gocryptfs.conf")): "{}"})
	if s := holds(); s != "" {
		t.Errorf("an empty deployment volume holds data (%s)", s)
	}
	w.write(map[string]string{mustRel(t, w.root, filepath.Join(vol, "AbCdEf")): "cipher"})
	if s := holds(); s != "namespaces" {
		t.Errorf("a deployment volume's file: %q", s)
	}
	if err := os.RemoveAll(filepath.Join(w.root, "data", "resources-enc", deploymentsLevel)); err != nil {
		t.Fatal(err)
	}
	// A deployment's vault key.
	if err := w.b.vaultWriteIn("apps/x", "dev", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if s := holds(); s != "vault" {
		t.Errorf("a deployment vault key: %q", s)
	}
	if err := os.RemoveAll(filepath.Join(w.root, "data", "vault", deploymentsLevel)); err != nil {
		t.Fatal(err)
	}
	// A deployment's registration file.
	reg := filepath.Join(w.root, "data", "deployments", util.TileKey("apps/x"), "dev", depBusFile)
	w.write(map[string]string{mustRel(t, w.root, reg): `{"schema":1}`})
	if s := holds(); s != "registrations" {
		t.Errorf("a deployment's registration file: %q", s)
	}
	if err := os.Remove(reg); err != nil {
		t.Fatal(err)
	}
	// main's bus subscription.
	if err := w.b.bus.put(busSub{Name: "feed", Resource: "res:apps/x/bus", Component: "apps/x", Path: "/bus", Role: "writer"}); err != nil {
		t.Fatal(err)
	}
	if s := holds(); s != "registrations" {
		t.Errorf("a bus subscription: %q", s)
	}
	w.b.bus.remove("apps/x", "feed")
	// Interface instances, ingress hosts.
	for what, set := range map[string]func(ws *registry.WorkspaceManifest){
		"an interface instance": func(ws *registry.WorkspaceManifest) {
			ws.IfaceInstances = map[string]map[string]string{"apps/x": {"llm": "/v1"}}
		},
		"an ingress host": func(ws *registry.WorkspaceManifest) {
			ws.IngressHosts = map[string][]string{"apps/x": {"a.example.com"}}
		},
	} {
		if err := w.b.Reg.MutateWorkspace(set); err != nil {
			t.Fatal(err)
		}
		if s := holds(); s != "registrations" {
			t.Errorf("%s: %q", what, s)
		}
		if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.IfaceInstances, ws.IngressHosts = nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	// An offloaded tile: its data is in the offload archive.
	for _, st := range []string{registry.StateOffloaded, registry.StateOffloadedFull} {
		if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
			ws.Lifecycle = map[string]string{"apps/x": st}
		}); err != nil {
			t.Fatal(err)
		}
		if s := holds(); s != "lifecycle" {
			t.Errorf("%s: %q", st, s)
		}
	}
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.Lifecycle = nil }); err != nil {
		t.Fatal(err)
	}
	if s := holds(); s != "" {
		t.Fatalf("the cleaned-up tile holds data in %s", s)
	}
	// A tile that doesn't root its scope holds none of the scope's data.
	w.kvPut("apps/x", "db")
	if held, _, _ := w.b.tileHoldsData(registry.PartitionAsk{Tile: "apps/x/y", Scope: "apps/x"}); held {
		t.Error("a non-root tile holds its scope's data")
	}
	if s := holds(); s != "namespaces" {
		t.Errorf("apps/x's kv key: %q", s)
	}
}

// PD-44 PD-51 — a store that can't tell (EACCES, EIO) holds data: only a
// missing file or directory is "no data". Each store's directory made
// unreadable in turn: the plaintext resource directory, the deployments
// level of the ciphertext tree, a main volume, the deployment vaults, the
// deployment records and one deployment's records.
func TestTileHoldsDataUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	w := newPartWS(t, map[string]string{
		"apps/x/scope.json": `{"resources":{"db":{"type":"kv"}}}`,
		"apps/x/xbin.json":  `{"runtime":"go"}`,
	}, nil)
	ask := registry.PartitionAsk{Tile: "apps/x", Scope: "apps/x", RootsScope: true}
	main, _ := scopeKeys("apps/x", util.MainDeployment)
	recs := filepath.Join(w.root, "data", "deployments", util.TileKey("apps/x"))
	for _, c := range []struct{ store, dir string }{
		{"namespaces", filepath.Join(w.root, filepath.FromSlash(main.Plain))},
		{"namespaces", filepath.Join(w.root, "data", "resources-enc", deploymentsLevel, escS("apps/x"))},
		{"namespaces", filepath.Join(w.root, filepath.FromSlash(main.Enc))},
		{"vault", filepath.Join(w.root, "data", "vault", deploymentsLevel, util.TileKey("apps/x"))},
		{"registrations", recs},
		{"registrations", filepath.Join(recs, "dev")},
	} {
		if err := os.MkdirAll(c.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if held, s, _ := w.b.tileHoldsData(ask); held {
			t.Fatalf("%s: an empty, readable directory holds data (%s)", c.dir, s)
		}
		if err := os.Chmod(c.dir, 0); err != nil {
			t.Fatal(err)
		}
		held, s, sealed := w.b.tileHoldsData(ask)
		if err := os.Chmod(c.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if !held || s != c.store || sealed {
			t.Errorf("%s unreadable: held %v in %q (sealed %v), want held in %s", mustRel(t, w.root, c.dir), held, s, sealed, c.store)
		}
	}
}

// PD-44 — a sealed vault can't tell whether a tile holds data: the tile
// pauses (pending) but nothing is recorded on that answer alone, and the
// unseal settles it again: auto for an empty vault, a recorded request for
// one with a key.
func TestPartitionSealedVault(t *testing.T) {
	w := newPartWS(t, map[string]string{
		"apps/sv/xbin.json": `{"runtime":"go"}`,
		"apps/sk/xbin.json": `{"runtime":"go"}`,
	}, nil)
	initBarrier(t, w.b)
	if err := w.b.vaultWrite("apps/sv", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := w.b.vaultWrite("apps/sk", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	w.b.Barrier().Seal()
	w.write(map[string]string{
		"apps/sv/xbin.json": `{"runtime":"go","partition":["user"]}`,
		"apps/sk/xbin.json": `{"runtime":"go","partition":["user"]}`,
	})
	w.rescan()
	for _, tile := range []string{"apps/sv", "apps/sk"} {
		if st, _, _ := w.state(tile); st != registry.PartitionPending {
			t.Errorf("%s while sealed: %v, want pending", tile, st)
		}
		if w.record(tile) != nil {
			t.Errorf("%s: a sealed vault's answer recorded a request: %+v", tile, w.record(tile))
		}
	}
	if err := w.b.UnsealOrInit("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if st, r, _ := w.state("apps/sv"); st != registry.PartitionPartitioned || r != userSpec || w.ops("apps/sv") != "auto" {
		t.Errorf("apps/sv after the unseal: %v %v %q, want auto to user", st, r, w.ops("apps/sv"))
	}
	if st, _, _ := w.state("apps/sk"); st != registry.PartitionPending || w.ops("apps/sk") != "request" {
		t.Errorf("apps/sk after the unseal: %v %q, want a recorded request", st, w.ops("apps/sk"))
	}
}

// PD-44 — a mode record this xbind can't read (a newer schema after a
// downgrade, a corrupt file) holds its tile invalid whatever its code asks,
// and is never decided on or written over: the file stays byte-identical
// across rescans and a decision.
func TestPartitionUnreadableRecord(t *testing.T) {
	recs := map[string]string{
		"apps/new": `{"schema": 2, "tile": "apps/new", "mode": {"user": true}, "future": {}}` + "\n",
		"apps/bad": `{"schema": 1, "tile": "apps/bad", "mode": {"us` + "\n",
		"apps/odd": `{"schema": 1, "tile": "apps/other", "mode": {"user": true}}` + "\n",
	}
	files := map[string]string{
		"apps/new/xbin.json": `{"runtime":"go"}`,
		"apps/bad/xbin.json": `{"runtime":"go","partition":["user"]}`,
		"apps/odd/xbin.json": `{"runtime":"go"}`,
	}
	for tile, body := range recs {
		files["data/partitions/"+util.TileKey(tile)+"/mode.json"] = body
	}
	w := newPartWS(t, files, nil)
	for round := 0; round < 2; round++ {
		w.rescan()
		for tile := range recs {
			c, _ := w.b.Reg.Component(tile)
			st, _, _ := c.PartitionState()
			if st != registry.PartitionInvalid || !strings.Contains(c.PartitionErr, "record can't be read") {
				t.Errorf("%s: %v %q, want invalid: the record can't be read", tile, st, c.PartitionErr)
			}
			if why := w.b.PartitionHoldReason(tile); !strings.Contains(why, "record can't be read") {
				t.Errorf("%s isn't held: %q", tile, why)
			}
			if _, ok := w.b.Reg.PartitionedScope(tile); ok {
				t.Errorf("%s: an unreadable record reads as a known partitioned scope", tile)
			}
		}
	}
	if err := w.b.recordDecision("apps/bad", modeOpKeep, registry.PartitionSpec{}, userSpec, "alice", nil); err == nil {
		t.Error("a decision on an unreadable record was recorded")
	}
	// R is unknown: a grant a partitioned tile can't hold is refused (fail
	// closed), whatever the tile's code asks.
	body, _ := json.Marshal(registry.Grant{From: "apps/new", Target: "cap:net-admin", Role: "writer"})
	req := httptest.NewRequest(http.MethodPost, "/api/xbin/grants", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Owner: true}))
	rec := httptest.NewRecorder()
	w.b.grantMutation(rec, req, func(ws *registry.WorkspaceManifest, g registry.Grant) { ws.Grants = append(ws.Grants, g) })
	if rec.Code != http.StatusConflict {
		t.Errorf("cap:net-admin for a tile whose record can't be read: %d, want 409", rec.Code)
	}
	for tile, body := range recs {
		got, err := os.ReadFile(filepath.Join(w.root, "data", "partitions", util.TileKey(tile), "mode.json"))
		if err != nil || string(got) != body {
			t.Errorf("%s's record changed: %q %v", tile, got, err)
		}
	}
}
