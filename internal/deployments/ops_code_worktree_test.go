package deployments

import (
	"os"
	"path/filepath"
	"testing"
)

// covers SC-WORKTREE P16 — every code operation beyond main (adding a
// deployment from the work tree, from the primary and from a named
// checkpoint, with live reload attached or not; attaching live reload;
// promoting onto the primary and onto a non-primary deployment; removing a
// deployment, the live reload target among them) and each one's dry run
// leave the tile's directory byte-identical by hash, its own git repository
// included, on a static tile and on a Go tile: checkpoints go to the
// xbind-owned store through a real, confined capture, and nothing is
// written into the work tree.
func TestOperationsNeverWriteWorkTreeCode(t *testing.T) {
	for _, tile := range []string{opSite, opAPI} {
		t.Run(tile, func(t *testing.T) {
			f := &codeFx{opsFx: newGitOpsFx(t, true)}
			f.hook()
			dir := filepath.Join(f.root, tile)
			f.write(tile+"/.gitignore", "node_modules/\n.env\n")
			f.write(tile+"/node_modules/dep/index.js", "module.exports = 1\n")
			f.write(tile+"/.env", "SECRET=1\n")
			if err := os.Symlink("xbin.json", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			gitInit(t, dir)

			step := func(name string, run func()) {
				t.Helper()
				before := treeHash(t, dir)
				run()
				if after := treeHash(t, dir); after != before {
					t.Errorf("%s wrote into the work tree:\n%s", name, diffHashes(before, after))
				}
			}
			done := func(a Answer) { f.settle(tile, a) }
			step("a dry add", func() { f.do(ownerP, OpAdd, &AddRequest{Tile: tile, Deployment: "dev", DryRun: true}) })
			step("add", func() { f.add(ownerP, &AddRequest{Tile: tile, Deployment: "dev"}) })
			step("a dry attach", func() { f.do(ownerP, OpAttach, &AttachRequest{Tile: tile, Deployment: "dev", DryRun: true}) })
			step("attach", func() { done(f.must(ownerP, OpAttach, &AttachRequest{Tile: tile, Deployment: "dev"})) })
			f.write(tile+"/extra.txt", "edit 1\n")
			step("a dry promote", func() { f.do(ownerP, OpPromote, &PromoteRequest{Tile: tile, From: "dev", To: "main", DryRun: true}) })
			step("promote onto the primary", func() { done(f.must(ownerP, OpPromote, &PromoteRequest{Tile: tile, From: "dev", To: "main"})) })
			step("add from the primary", func() { f.add(ownerP, &AddRequest{Tile: tile, Deployment: "exp", From: FromPrimary}) })
			first := f.st.logged(tile)[0].Tree
			step("add from a named checkpoint", func() { f.add(ownerP, &AddRequest{Tile: tile, Deployment: "old", From: "c:" + first}) })
			f.write(tile+"/extra.txt", "edit 2\n")
			step("promote onto a non-primary deployment", func() {
				done(f.must(ownerP, OpPromote, &PromoteRequest{Tile: tile, From: "dev", To: "exp"}))
			})
			step("a dry remove", func() {
				f.do(ownerP, OpRemove, &RemoveRequest{Tile: tile, Deployment: "old", Confirm: ConfirmErase, DryRun: true})
			})
			step("remove", func() { f.must(ownerP, OpRemove, &RemoveRequest{Tile: tile, Deployment: "old", Confirm: ConfirmErase}) })
			step("remove the live reload target", func() {
				f.must(ownerP, OpRemove, &RemoveRequest{Tile: tile, Deployment: "dev", Confirm: ConfirmErase})
			})
			step("add with live reload attached", func() { f.add(ownerP, &AddRequest{Tile: tile, Deployment: "new", Attach: true}) })
			if r := f.rec(tile); r.LiveReload != "new" || r.Deployments["exp"] == nil || r.Deployments["dev"] != nil {
				t.Errorf("the record after the steps: %+v", r)
			}
		})
	}
}
