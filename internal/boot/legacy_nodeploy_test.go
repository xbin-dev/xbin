package boot

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
)

// covers D119c D119f SC-ZERO PO-7 PO-8 — 12-compat §6.1 on the aged workspace, in
// process: booting it twice creates no tile-deployment state (no
// data/deployments, data/checkpoints or .xbin/deploy, no dot-level namespace
// root), leaves the cron and bus-subscription stores byte-equal (data/ is in
// the fixture's allowed list, so its own assertions would let a rewrite
// through), and gives no tile repository a remote, ref or config entry (the
// fixture's /.git/ wildcard would hide one). A new function beside
// TestLegacyWorkspaceBootsTwiceInProcess, reusing its helpers, so neither the
// fixture nor its allowed list changes (NP-14-9).
func TestLegacyWorkspaceNoDeploymentStateInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	nodeployAge(t, ws)
	haveGit := nodeployTileRepo(t, ws, "apps/welcome")
	stores := nodeployPlantStores(t, ws)
	repos0 := nodeployRepoState(t, ws)
	if haveGit && len(repos0) == 0 {
		t.Fatal("fixture: the pre-existing tile repo is missing")
	}

	bootOnce(t, ws)
	nodeployCheck(t, ws, "first boot", stores)
	repos1 := nodeployRepoState(t, ws)
	for k, v := range repos0 { // a repo that existed before the upgrade
		if repos1[k] != v {
			t.Errorf("first boot changed %s in a tile repository (12-compat §6.1)", k)
		}
	}

	bootOnce(t, ws)
	nodeployCheck(t, ws, "second boot", stores)
	if diff := changed(repos1, nodeployRepoState(t, ws)); len(diff) > 0 {
		t.Errorf("a second boot changed tile repositories:\n  %s", strings.Join(diff, "\n  "))
	}
}

// nodeployAge ages a fresh scaffold exactly as
// TestLegacyWorkspaceBootsTwiceInProcess does: a legacy shared home/ with
// real data, no homes/, no backfill ledger, no builtins provenance, no
// tiles/organisations, a .gitignore without homes/.
func nodeployAge(t *testing.T, ws string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = os.RemoveAll(filepath.Join(ws, "homes"))
	must(os.MkdirAll(filepath.Join(ws, "home", ".claude"), 0o755))
	must(os.WriteFile(filepath.Join(ws, "home", ".claude", "settings.json"), []byte(`{"kept":true}`), 0o644))
	_ = os.Remove(filepath.Join(ws, "data", "backfills.json"))
	_ = os.Remove(filepath.Join(ws, ".xbin", "builtins.json"))
	_ = os.RemoveAll(filepath.Join(ws, ".xbin", "builtins"))
	_ = os.RemoveAll(filepath.Join(ws, "tiles", "organisations"))
	gi := filepath.Join(ws, ".gitignore")
	if b, err := os.ReadFile(gi); err == nil {
		must(os.WriteFile(gi, []byte(strings.ReplaceAll(string(b), "homes/\n", "")), 0o644))
	}
}

// nodeployTileRepo makes tile its own repository before the first boot, as
// an older xbind left it: a commit, a branch, an origin remote and a config
// entry of the tile's own. Reports false (and makes nothing) without git.
func nodeployTileRepo(t *testing.T, ws, tile string) bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "older xbind"},
		{"branch", "feature"},
		{"remote", "add", "origin", "https://example.com/welcome.git"},
		{"config", "tile.note", "kept"},
	} {
		if _, err := confine.Git(context.Background(), dir, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return true
}

// nodeployPlantStores writes the cron and bus-subscription stores an older
// xbind left, in a spelling today's persist never produces (compact, with a
// trailing newline), so any rewrite shows; it returns their bytes.
func nodeployPlantStores(t *testing.T, ws string) map[string]string {
	t.Helper()
	stores := map[string]string{
		"data/cron-jobs.json": `[{"name":"tick","resource":"res:apps/welcome/cron","schedule":"@every 1h",` +
			`"component":"apps/welcome","path":"/tick","role":"writer"}]` + "\n",
		"data/bus-subscriptions.json": `[{"name":"s1","resource":"res:apps/welcome/bus","component":"apps/welcome",` +
			`"path":"/on","role":"writer"}]` + "\n",
	}
	for rel, content := range stores {
		if err := os.WriteFile(filepath.Join(ws, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return stores
}

// nodeployCheck asserts that no deployment state exists and the planted
// stores are byte-equal.
func nodeployCheck(t *testing.T, ws, when string, stores map[string]string) {
	t.Helper()
	for _, rel := range []string{"data/deployments", "data/checkpoints", ".xbin/deploy"} {
		if _, err := os.Lstat(filepath.Join(ws, rel)); err == nil {
			t.Errorf("after the %s %s exists: a workspace that never opted in gains no deployment state", when, rel)
		}
	}
	for _, top := range []string{"data", ".xbin"} {
		_ = filepath.WalkDir(filepath.Join(ws, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Name() == ".deployments" {
				rel, _ := filepath.Rel(ws, p)
				t.Errorf("after the %s a dot-level namespace root exists: %s", when, filepath.ToSlash(rel))
				return fs.SkipDir
			}
			return nil
		})
	}
	for rel, want := range stores {
		if got, err := os.ReadFile(filepath.Join(ws, rel)); err != nil || string(got) != want {
			t.Errorf("the %s rewrote %s (err %v):\n got: %q\nwant: %q", when, rel, err, got, want)
		}
	}
}

// nodeployRepoState maps every tile repository's HEAD, config, packed-refs
// and loose refs (repo-relative path → content). The workspace's own .git is
// not a tile repository.
func nodeployRepoState(t *testing.T, ws string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".git" || rel == ".xbin" || rel == "data" || rel == "homes" || rel == "home":
			return fs.SkipDir
		case d.Name() != ".git":
			return nil
		}
		for _, f := range []string{"HEAD", "config", "packed-refs"} {
			if b, err := os.ReadFile(filepath.Join(p, f)); err == nil {
				out[rel+"/"+f] = string(b)
			}
		}
		_ = filepath.WalkDir(filepath.Join(p, "refs"), func(rp string, rd fs.DirEntry, err error) error {
			if err == nil && !rd.IsDir() {
				if b, err := os.ReadFile(rp); err == nil {
					r, _ := filepath.Rel(p, rp)
					out[rel+"/"+filepath.ToSlash(r)] = string(b)
				}
			}
			return nil
		})
		return fs.SkipDir
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
