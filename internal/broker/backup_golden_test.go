package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-56 11§4 11§Tests — the previous release's writer's archive
// (internal/backup/testdata/plaintext-schema1.tar, its bytes pinned there)
// restores through restoreTile into a workspace that seals its backups
// now, byte for byte: every source and terminal-layer file, the sqlite
// volume's file and the kv value.
func TestGoldenPlaintextArchiveRestores(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "backup", "testdata", "plaintext-schema1.tar"))
	if err != nil {
		t.Fatal(err)
	}
	b := testBroker(t)
	const comp = "apps/golden"
	for rel, content := range map[string]string{
		"apps/golden/xbin.json":  `{"runtime":"go"}`,
		"apps/golden/scope.json": `{"resources":{"db":{"type":"sqlite"},"state":{"type":"kv"}}}`,
	} {
		if err := os.MkdirAll(filepath.Join(b.Reg.Root, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(b.Reg.Root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	fakeResEnc(t, b)
	if err := b.barrier.Init("golden-pass"); err != nil { // a workspace that seals now
		t.Fatal(err)
	}
	arch := &memArchiver{keys: map[string][]memVersion{}}
	b.ProxyHandler = arch
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: {{Ref: "apps/archiver"}}}}
	}); err != nil {
		t.Fatal(err)
	}
	v := arch.set(backupKey(comp), golden)
	r, _, err := b.restoreTile(comp, v)
	if err != nil || r.Schema != 1 || r.DataErased != "" || r.DataMissing != "" {
		t.Fatalf("restore: %+v %v", r, err)
	}
	digest := func(p string) string {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(data)
		return hex.EncodeToString(s[:])
	}
	for p, want := range map[string]string{ // internal/backup's goldenMembers
		filepath.Join(b.Reg.Root, comp, "xbin.json"):                             "f8284a1ee28e3feb31f7f4d2397238fc346347063e3d1c9148ba689d79ca284b",
		filepath.Join(b.Reg.Root, comp, "backend", "main.go"):                    "55a60bb97151b2b4b680462447ce60ec34511b14fa10d77440c97b9777101566",
		filepath.Join(b.resenc.MountDir(util.ScopeKey(comp), "db"), "db.sqlite"): "5b448daf62c54baf52c8142af22b7e7a8aedd86569fccc608bf9e88479403dd5",
		filepath.Join(b.termDir(comp), "upper", "etc", "profile"):                "7eca7f7ea45b3cf0d34824b555b7d722ccfcc56343102ba2ef2b809fa68f3487",
	} {
		if got := digest(p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	ns := &nsFx{t: t, b: b, root: b.Reg.Root}
	if got := ns.getKV(comp, util.MainDeployment, "state", "k"); got != "v" { // data/kv.json: {"state":{"k":"dg=="}}
		t.Errorf("the kv value: %q", got)
	}
}
