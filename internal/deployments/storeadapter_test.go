package deployments

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
)

// covers D119e D119f SC-AUDIT — the plane over the real checkpoint store
// (storeAdapter): each finished attempt is written to the deploy log and
// dropped from the journal, the log reads back as the attempts it was
// written from, a save while paused is counted by the store's drift count,
// the view repository holds the pinned deployment while the
// primary is paused, and resuming onto a main-only tile removes the view
// repository and keeps the store (PO-15).
func TestStoreAdapterLogAndView(t *testing.T) {
	f := newGitOpsFx(t, true)
	f.p.cps = storeAdapter{checkpoint.New(f.root)}
	ctx := context.Background()
	tile := opSite

	f.settle(tile, f.must(ownerP, OpPause, &PauseRequest{Tile: tile}))
	f.write(tile+"/index.html", "<h1>v2</h1>")
	f.p.WorkTreeMoved(tile) // the watcher's notice: the store's drift count
	deadline := time.Now().Add(10 * time.Second)
	for {
		if d, ok := f.p.WorkTreeDrift(tile); ok && d.Counted {
			if d.Changed != 1 {
				t.Errorf("the drift count = %d, want 1 (index.html)", d.Changed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the drift count never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.settle(tile, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: tile}))

	logged, err := f.p.store().ReadLog(ctx, tile, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 2 || logged[0].How != "reload-now" || logged[1].How != "pause" {
		t.Fatalf("the deploy log = %+v, want reload-now then pause", logged)
	}
	for _, a := range logged {
		if a.Result != resultOK || len(a.Tree) != 40 || a.By != "owner" || a.Deployment != "main" || a.FinishedAt == "" {
			t.Errorf("entry %d = %+v", a.ID, a)
		}
	}
	if logged[0].Previous != logged[1].Tree || logged[0].Tree == logged[1].Tree {
		t.Errorf("reload now's previous %q, pause's checkpoint %q", logged[0].Previous, logged[1].Tree)
	}
	if b, err := os.ReadFile(filepath.Join(journalDir(f.root, tile), journalFile)); err == nil {
		var j struct {
			Attempts []json.RawMessage `json:"attempts"`
		}
		if json.Unmarshal(b, &j) == nil && len(j.Attempts) != 0 {
			t.Errorf("the journal keeps %d logged attempts", len(j.Attempts))
		}
	}

	view := viewDir(f.root, tile)
	out, err := exec.Command("git", "--git-dir="+view, "for-each-ref", "--format=%(refname) %(objectname)").CombinedOutput()
	if err != nil {
		t.Fatalf("the view repository: %v\n%s", err, out)
	}
	if refs := strings.TrimSpace(string(out)); !strings.HasPrefix(refs, "refs/heads/deploy/main ") || strings.Contains(refs, "\n") {
		t.Errorf("the view repository's refs = %q, want refs/heads/deploy/main alone", refs)
	}

	f.settle(tile, f.must(ownerP, OpResume, &ResumeRequest{Tile: tile}))
	if _, err := os.Lstat(view); !os.IsNotExist(err) {
		t.Errorf("the view repository outlived the opt-out (%v)", err)
	}
	if !f.p.store().Exists(tile) {
		t.Error("the opt-out removed the store")
	}
	if logged, _ := f.p.store().ReadLog(ctx, tile, "main"); len(logged) != 3 || logged[0].How != "resume" || !logged[0].FollowsWorkTree {
		t.Errorf("after resume the log = %+v", logged)
	}
}

// covers SC-SAFE-DEPLOY — a dry run that moves one checkpoint to another
// reports the diff it would ship (11-contract §1.1's files, added and
// removed), measured by the real store: reload now after an edit while
// paused, and resume onto the edited work tree.
func TestDryRunMeasuresCode(t *testing.T) {
	f := newGitOpsFx(t, true)
	f.p.cps = storeAdapter{checkpoint.New(f.root)}
	tile := opSite
	f.settle(tile, f.must(ownerP, OpPause, &PauseRequest{Tile: tile}))
	f.write(tile+"/index.html", "<h1>v2</h1>\n<p>new</p>\n")
	for op, req := range map[Op]any{
		OpReloadNow: &ReloadNowRequest{Tile: tile, DryRun: true},
		OpResume:    &ResumeRequest{Tile: tile, DryRun: true},
	} {
		res, err := f.do(ownerP, op, req)
		a, ok := res.(DryRunAnswer)
		if err != nil || !ok {
			t.Fatalf("%s dry run = %T, %v", op, res, err)
		}
		if c := a.Impact.Code; c == nil || c.Files != 1 || c.Added != 2 || c.Removed != 1 {
			t.Errorf("%s dry run's code = %+v, want 1 file, +2 −1", op, c)
		}
	}
}
