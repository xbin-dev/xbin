package deployments

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers T9 P21 06-security-T9.4 — authority is judged again where an
// operation commits, against the record it changes: a terminal-level
// person's resume whose record is protected while it captures is refused
// at its commit (409, live reload never attaches to a protected primary)
// and commits nothing; a reload now accepted for them, protected while it
// waits for its swap, is refused at the swap (403 naming the protection),
// so the record keeps its previous code, while a manager's commits; a deploy
// in flight when the tile opted out finds no record at its swap and writes
// none back.
func TestAuthorizationRecheckedAtCommit(t *testing.T) {
	protect := func(f *opsFx, tile string) {
		t.Helper()
		if _, err := f.p.idx.commit(tile, -1, func(r *Record) error {
			r.ProtectedPrimary = true
			return nil
		}); err != nil {
			t.Fatalf("protecting %s: %v", tile, err)
		}
	}
	dev := userP("dev", opSite, "terminal")

	t.Run("a request-time commit", func(t *testing.T) {
		f := newOpsFx(t, true)
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		seq := f.rec(opSite).Seq
		f.st.set(func(s *fakeStore) { s.hook = func(tile string) { protect(f, tile) } })
		_, err := f.do(dev, OpResume, &ResumeRequest{Tile: opSite})
		if code, msg := errStatus(err); code != http.StatusConflict || !strings.Contains(msg, "live reload never attaches to a protected primary") {
			t.Fatalf("resume protected meanwhile: %d %s", code, msg)
		}
		if r := f.rec(opSite); r == nil || r.LiveReload != "" || r.Seq != seq+1 || !r.ProtectedPrimary {
			t.Errorf("record after the refused commit = %+v (protected at seq %d)", r, seq+1)
		}
	})

	t.Run("a deploy's swap", func(t *testing.T) {
		f := newOpsFx(t, true)
		api := userP("dev", opAPI, "terminal")
		a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		f.wait(opAPI, a.Deploy.ID)
		pinned := *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint
		hold := make(chan struct{})
		started := make(chan string, 4)
		f.run.set(func(r *fakeRunner) { r.before, r.started = hold, started })
		f.write(opAPI+"/main.go", "package main // v2\n")
		ans := f.must(api, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		<-started
		protect(f, opAPI)
		close(hold)
		e := f.wait(opAPI, ans.Deploy.ID)
		if e.Result != resultFailed || !strings.HasPrefix(e.Error, "the primary of apps/api (main) is protected") {
			t.Errorf("the swap after protection = %+v", e)
		}
		if got := *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint; got != pinned {
			t.Errorf("the refused swap moved main to %s", got)
		}
		var ce *Error
		if errs := f.run.commitErrs; len(errs) == 0 || !errors.As(errs[len(errs)-1], &ce) || ce.Status != http.StatusForbidden {
			t.Errorf("commit answered the runner %v, want the 403", errs)
		}

		// The owner, a manager, commits onto the protected primary, naming
		// the checkpoint it reviewed and the seq it read (P21).
		f.run.set(func(r *fakeRunner) { r.before, r.started = nil, nil })
		f.write(opAPI+"/main.go", "package main // v3\n")
		c, _ := f.p.component(opAPI)
		v3, err := f.st.Capture(t.Context(), checkpoint.CaptureRequest{Source: f.p.source(c), By: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		ans = f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Checkpoint: v3.ID, Seq: ptr(f.rec(opAPI).Seq)})
		if e := f.wait(opAPI, ans.Deploy.ID); e.Result != resultOK || *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint == pinned {
			t.Errorf("a manager's deploy onto the protected primary = %+v", e)
		}
	})

	t.Run("a swap after the tile opted out", func(t *testing.T) {
		f := newOpsFx(t, true)
		a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		f.wait(opAPI, a.Deploy.ID)
		hold := make(chan struct{})
		started := make(chan string, 4)
		f.run.set(func(r *fakeRunner) { r.before, r.started = hold, started })
		f.write(opAPI+"/main.go", "package main // v2\n")
		f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		<-started
		if f.must(ownerP, OpResume, &ResumeRequest{Tile: opAPI}); f.rec(opAPI) != nil {
			t.Fatal("resume didn't opt out")
		}
		n := len(runnerCommitErrs(f))
		close(hold)
		// The tile has no record, so no log to read: the runner's side says it.
		for i := 0; i < 500 && len(runnerCommitErrs(f)) == n; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if errs := runnerCommitErrs(f); len(errs) != n+1 || errs[n] == nil || !strings.Contains(errs[n].Error(), "has no deployments yet") {
			t.Errorf("the swap after the opt-out: commit answered %v", errs)
		}
		if f.rec(opAPI) != nil || fileExists(recordPath(f.root, opAPI)) {
			t.Error("a swap after the opt-out wrote a record back")
		}
	})
}

// runnerCommitErrs is what commit answered the fake runner, in order.
func runnerCommitErrs(f *opsFx) []error {
	f.run.mu.Lock()
	defer f.run.mu.Unlock()
	return append([]error(nil), f.run.commitErrs...)
}
