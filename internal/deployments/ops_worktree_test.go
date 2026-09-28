package deployments

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// covers SC-WORKTREE D119g — every M1 operation (pausing and resuming live
// reload, reload now, deploy, roll back, restart, and their dry runs) leaves
// the tile's directory byte-identical by hash, its own git repository (the
// index, HEAD, refs, config) included, on a static tile and on a Go tile:
// checkpoints go to the xbind-owned store through a real, confined capture,
// and nothing is written into the work tree.
func TestOperationsNeverWriteWorkTree(t *testing.T) {
	for _, tile := range []string{opSite, opAPI} {
		t.Run(tile, func(t *testing.T) {
			f := newGitOpsFx(t, true)
			dir := filepath.Join(f.root, tile)
			f.write(tile+"/.gitignore", "node_modules/\n.env\n")
			f.write(tile+"/node_modules/dep/index.js", "module.exports = 1\n")
			f.write(tile+"/.env", "SECRET=1\n")
			if err := os.Symlink("index.html", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			gitInit(t, dir)

			step := func(name string, run func()) {
				t.Helper()
				before := treeHash(t, dir)
				run()
				f.p.q.mu.Lock()
				f.p.q.mu.Unlock()
				if after := treeHash(t, dir); after != before {
					t.Errorf("%s wrote into the work tree:\n%s", name, diffHashes(before, after))
				}
			}
			finish := func(a Answer) { f.settle(tile, a) }
			step("a dry pause", func() { f.do(ownerP, OpPause, &PauseRequest{Tile: tile, DryRun: true}) })
			step("pause", func() { finish(f.must(ownerP, OpPause, &PauseRequest{Tile: tile})) })
			f.write(tile+"/extra.txt", "edit 1\n")
			step("a dry reload now", func() { f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: tile, DryRun: true}) })
			step("reload now", func() { finish(f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: tile})) })
			first := f.st.logged(tile)[0].Tree
			f.write(tile+"/extra.txt", "edit 2\n")
			step("deploy a fresh capture", func() { finish(f.must(ownerP, OpDeploy, &DeployRequest{Tile: tile})) })
			step("deploy a named checkpoint", func() {
				finish(f.must(ownerP, OpDeploy, &DeployRequest{Tile: tile, Checkpoint: "c:" + first}))
			})
			step("roll back", func() { finish(f.must(ownerP, OpRollback, &RollbackRequest{Tile: tile})) })
			step("restart", func() { finish(f.must(ownerP, OpDeploy, &DeployRequest{Tile: tile, Restart: true})) })
			step("a dry resume", func() { f.do(ownerP, OpResume, &ResumeRequest{Tile: tile, DryRun: true}) })
			step("resume", func() { finish(f.must(ownerP, OpResume, &ResumeRequest{Tile: tile})) })
			if n := len(f.st.logged(tile)); n != 7 {
				t.Errorf("the deploy log has %d entries, want 7", n)
			}
		})
	}
}

// gitInit makes dir a git repository with one commit, as a tile's own.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "one"},
	} {
		// No auto-maintenance: a commit may detach one, whose
		// objects/maintenance.lock comes and goes under treeHash's walk.
		cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// treeHash lists every entry beneath dir with its type, mode and content
// hash (a symlink's target), one line each, sorted.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := filepath.ToSlash(rel) + " " + fi.Mode().String()
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			line += " -> " + target
		case fi.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			line += " " + hex.EncodeToString(sum[:8])
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// diffHashes names the lines two treeHash listings don't share.
func diffHashes(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for l := range ma {
		if !mb[l] {
			out = append(out, "  - "+l)
		}
	}
	for l := range mb {
		if !ma[l] {
			out = append(out, "  + "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}
