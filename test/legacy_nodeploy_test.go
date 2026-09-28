//go:build integration

package test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// covers D119c D119f SC-ZERO PO-7 PO-8 — 12-compat §6.1 on the aged workspace,
// with the real binary: booting it twice creates no tile-deployment state
// (no data/deployments, data/checkpoints or .xbin/deploy, no dot-level
// namespace root), leaves the cron and bus-subscription stores byte-equal
// (data/ is in the fixture's allowed list, so its own assertions would let a
// rewrite through), and gives no tile repository a remote, ref or config
// entry (the fixture's /.git/ wildcard would hide one). A new function
// beside TestLegacyWorkspaceBootsTwice, reusing its helpers, so neither the
// fixture nor its allowed list changes (NP-14-9).
func TestLegacyWorkspaceNoDeploymentState(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command(xbindBin, "init", ws).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	// aged exactly as TestLegacyWorkspaceBootsTwice ages it
	_ = os.RemoveAll(filepath.Join(ws, "homes"))
	must(t, os.MkdirAll(filepath.Join(ws, "home", ".claude"), 0o755))
	must(t, os.WriteFile(filepath.Join(ws, "home", ".claude", "settings.json"), []byte(`{"kept":true}`), 0o644))
	_ = os.Remove(filepath.Join(ws, "data", "backfills.json"))
	_ = os.Remove(filepath.Join(ws, ".xbin", "builtins.json"))
	_ = os.RemoveAll(filepath.Join(ws, ".xbin", "builtins"))
	_ = os.RemoveAll(filepath.Join(ws, "tiles", "organisations"))
	gi := filepath.Join(ws, ".gitignore")
	if b, err := os.ReadFile(gi); err == nil {
		must(t, os.WriteFile(gi, []byte(strings.ReplaceAll(string(b), "homes/\n", "")), 0o644))
	}
	// a tile an older xbind had already made a repository, with a branch, a
	// remote and a config entry of its own
	welcome := filepath.Join(ws, "apps", "welcome")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "older xbind"},
		{"branch", "feature"},
		{"remote", "add", "origin", "https://example.com/welcome.git"},
		{"config", "tile.note", "kept"},
	} {
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = welcome
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// the registration stores an older xbind left, spelled as today's persist
	// never writes them (compact, a trailing newline), so a rewrite shows
	stores := map[string]string{
		"data/cron-jobs.json": `[{"name":"tick","resource":"res:apps/welcome/cron","schedule":"@every 1h",` +
			`"component":"apps/welcome","path":"/tick","role":"writer"}]` + "\n",
		"data/bus-subscriptions.json": `[{"name":"s1","resource":"res:apps/welcome/bus","component":"apps/welcome",` +
			`"path":"/on","role":"writer"}]` + "\n",
	}
	for rel, content := range stores {
		must(t, os.WriteFile(filepath.Join(ws, rel), []byte(content), 0o644))
	}
	repos0 := legacyRepoState(t, ws)
	if len(repos0) == 0 {
		t.Fatal("fixture: the pre-existing tile repo is missing")
	}

	check := func(when string) {
		t.Helper()
		for _, rel := range []string{"data/deployments", "data/checkpoints", ".xbin/deploy"} {
			if _, err := os.Lstat(filepath.Join(ws, rel)); err == nil {
				t.Errorf("after the %s %s exists: a workspace that never opted in gains no deployment state", when, rel)
			}
		}
		for _, top := range []string{"data", ".xbin"} {
			_ = filepath.WalkDir(filepath.Join(ws, top), func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Name() == ".deployments" {
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

	log1 := bootOnce(t, ws)
	check("first boot")
	repos1 := legacyRepoState(t, ws)
	for k, v := range repos0 {
		if repos1[k] != v {
			t.Errorf("first boot changed %s in a tile repository (12-compat §6.1; boot log:\n%s)", k, log1)
		}
	}
	bootOnce(t, ws)
	check("second boot")
	if diff := changed(repos1, legacyRepoState(t, ws)); len(diff) > 0 {
		t.Errorf("a second boot changed tile repositories:\n  %s", strings.Join(diff, "\n  "))
	}
}

// legacyRepoState maps every tile repository's HEAD, config, packed-refs
// and loose refs (repo-relative path → content). The workspace's own .git is
// not a tile repository.
func legacyRepoState(t *testing.T, ws string) map[string]string {
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
