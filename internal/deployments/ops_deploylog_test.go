package deployments

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers SC-AUDIT D119a T9.5 — an operation × principal matrix: every change to
// what main runs (pause, reload now, deploy, roll back, restart, resume)
// has exactly one deploy-log entry naming who (user:<id> or owner), the
// credential's kind (via), when it was requested and finished, the feed,
// the checkpoint and the operation; a request that changes nothing (an
// unchanged reload now, a dry run) has none. Entries survive a restart:
// from the store's log, and, while the log can't take them, from the deploy
// journal, which the next boot reads back.
func TestDeployLogAudit(t *testing.T) {
	principals := map[string]struct {
		p       auth.Principal
		by, via string
	}{
		"the owner token":     {ownerP, "owner", "bearer"},
		"a person":            {userP("ana", opAPI, "terminal"), "user:ana", "session"},
		"a terminal session":  {terminalP("ana", opAPI, "terminal"), "user:ana", "terminal"},
		"an owner's terminal": {auth.Principal{Component: opAPI, Via: "terminal"}, "owner", "terminal"},
	}
	for name, who := range principals {
		t.Run(name, func(t *testing.T) {
			f := newOpsFx(t, true)
			run := func(op Op, req any) {
				t.Helper()
				f.settle(opAPI, f.must(who.p, op, req))
			}
			run(OpPause, &PauseRequest{Tile: opAPI})
			f.write(opAPI+"/main.go", "package main // v2\n")
			run(OpReloadNow, &ReloadNowRequest{Tile: opAPI, DryRun: false})
			if a := f.must(who.p, OpReloadNow, &ReloadNowRequest{Tile: opAPI}); !a.Unchanged {
				t.Fatalf("an unchanged reload now: %+v", a)
			}
			if _, err := f.do(who.p, OpDeploy, &DeployRequest{Tile: opAPI, DryRun: true}); err != nil {
				t.Fatal(err)
			}
			first := f.st.logged(opAPI)[0].Tree
			run(OpDeploy, &DeployRequest{Tile: opAPI, Checkpoint: "c:" + first})
			run(OpRollback, &RollbackRequest{Tile: opAPI})
			run(OpDeploy, &DeployRequest{Tile: opAPI, Restart: true})

			// A restart: the store's log still has them all.
			f.boot()
			run(OpResume, &ResumeRequest{Tile: opAPI})
			logged := f.st.logged(opAPI)
			var hows []string
			for i, e := range logged {
				hows = append(hows, e.How)
				if e.By != who.by || e.Via != who.via || e.Result != resultOK || e.Feed != checkpoint.FeedWorkTree ||
					e.Deployment != util.MainDeployment || int64(i+1) != e.ID {
					t.Errorf("entry %d = %+v", i, e)
				}
				for _, at := range []string{e.RequestedAt, e.FinishedAt} {
					if _, err := time.Parse(time.RFC3339, at); err != nil {
						t.Errorf("entry %d's time %q: %v", i, at, err)
					}
				}
				if e.Tree == "" && e.How != "resume" {
					t.Errorf("entry %d names no checkpoint", i)
				}
			}
			if strings.Join(hows, " ") != "pause reload-now deploy rollback restart resume" {
				t.Errorf("the deploy log = %v", hows)
			}
			if !logged[len(logged)-1].FollowsWorkTree || logged[len(logged)-1].Tree == "" {
				t.Errorf("resume's entry = %+v, want the work tree's capture", logged[len(logged)-1])
			}
		})
	}

	t.Run("the journal keeps entries the log can't take", func(t *testing.T) {
		f := newOpsFx(t, true)
		f.st.set(func(s *fakeStore) { s.noLog = true })
		a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		f.wait(opAPI, a.Deploy.ID)
		f.write(opAPI+"/main.go", "package main // v2\n")
		a = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		f.wait(opAPI, a.Deploy.ID)
		j := readJournalFile(t, f.root, opAPI)
		if len(j.Attempts) != 2 || j.Next != 3 {
			t.Fatalf("journal = %+v", j)
		}
		f.boot()
		entries, _ := f.p.Log(t.Context(), opAPI, "", 0, 0)
		if len(entries) != 2 || entries[0].How != "reload-now" || entries[1].How != "pause" || entries[0].Result != resultOK {
			t.Errorf("after a restart the log reads %+v", entries)
		}
		// The log comes back: the next boot writes what the journal kept.
		f.st.set(func(s *fakeStore) { s.noLog = false })
		f.boot()
		if n := len(f.st.logged(opAPI)); n != 2 || len(readJournalFile(t, f.root, opAPI).Attempts) != 0 {
			t.Errorf("after the log came back: %d logged, journal %+v", n, readJournalFile(t, f.root, opAPI))
		}
		// Ids keep counting.
		f.write(opAPI+"/main.go", "package main // v3\n")
		a = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if a.Deploy.ID != 3 {
			t.Errorf("the next id after a restart = %d", a.Deploy.ID)
		}
		f.wait(opAPI, a.Deploy.ID)
	})
}

// covers SC-AUDIT NP-07-13 Q3 — the deploy journal keeps every accepted
// attempt through a crash: at the next boot, an attempt running when xbind
// died is logged failed ("interrupted"), naming the code the record runs —
// the attempted checkpoint for a pause, whose pointer was written at request
// time, the previous one for a pinned → pinned deploy that hadn't swapped;
// one that had swapped is logged ok; one still queued, cancelled. The boot
// never rewrites the record (PO-8), empties the journal once the log has the
// entries, and keeps counting ids.
func TestDeployLogSurvivesCrashMidDeploy(t *testing.T) {
	crash := func(f *opsFx) {
		// The dead xbind's worker stays parked on its gate, which is never
		// opened; the next boot is a new plane over the same files.
		f.run.set(func(r *fakeRunner) { r.before, r.after, r.started = nil, nil, nil })
		f.boot()
	}

	t.Run("pausing live reload", func(t *testing.T) {
		f := newOpsFx(t, true)
		started := make(chan string, 1)
		f.run.set(func(r *fakeRunner) { r.before, r.started = make(chan struct{}), started })
		a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		<-started
		tree := *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint
		recBefore := readFile(t, recordPath(f.root, opAPI))
		crash(f)
		e := f.wait(opAPI, a.Deploy.ID)
		if e.Result != resultFailed || e.Error != "interrupted: xbind restarted mid-deploy; main runs checkpoint c:"+tree[:7] {
			t.Errorf("the interrupted pause = %+v", e)
		}
		if logged := f.st.logged(opAPI); len(logged) != 1 || logged[0].ID != a.Deploy.ID || logged[0].Result != resultFailed {
			t.Errorf("deploy log = %+v", logged)
		}
		if got := readFile(t, recordPath(f.root, opAPI)); got != recBefore {
			t.Errorf("boot rewrote the record:\n%s\nwas\n%s", got, recBefore)
		}
		if j := readJournalFile(t, f.root, opAPI); len(j.Attempts) != 0 {
			t.Errorf("the journal still holds %+v", j.Attempts)
		}
		if code, _ := f.p.CodeFor(opAPI, util.MainDeployment); code.Tree != tree {
			t.Errorf("main runs %+v after the crash, want the attempted checkpoint", code)
		}
	})

	t.Run("a pinned deploy, before and after its swap, and one queued", func(t *testing.T) {
		for _, swapped := range []bool{false, true} {
			f := newOpsFx(t, true)
			a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
			f.wait(opAPI, a.Deploy.ID)
			previous := *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint
			started := make(chan string, 1)
			f.run.set(func(r *fakeRunner) {
				r.started = started
				if swapped {
					r.after = make(chan struct{})
				} else {
					r.before = make(chan struct{})
				}
			})
			f.write(opAPI+"/main.go", "package main // v2\n")
			running := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
			<-started
			if swapped {
				waitSwapped(t, f, opAPI, running.Deploy.ID)
			}
			f.write(opAPI+"/main.go", "package main // v3\n")
			queued := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
			now := *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint
			crash(f)
			e := f.wait(opAPI, running.Deploy.ID)
			switch {
			case swapped && (e.Result != resultOK || now == previous):
				t.Errorf("a swapped deploy = %+v (record %s, previous %s)", e, now, previous)
			case !swapped && (e.Result != resultFailed || now != previous ||
				e.Error != "interrupted: xbind restarted mid-deploy; main runs checkpoint c:"+previous[:7]):
				t.Errorf("an interrupted deploy = %+v (record %s, previous %s)", e, now, previous)
			}
			if q := f.wait(opAPI, queued.Deploy.ID); q.Result != resultCancelled {
				t.Errorf("the queued deploy = %+v", q)
			}
			if n := len(f.st.logged(opAPI)); n != 3 {
				t.Errorf("swapped %v: %d log entries, want 3", swapped, n)
			}
			f.write(opAPI+"/main.go", "package main // v4\n")
			next := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
			if next.Deploy.ID != queued.Deploy.ID+1 {
				t.Errorf("the next id after the crash = %d, want %d", next.Deploy.ID, queued.Deploy.ID+1)
			}
			f.wait(opAPI, next.Deploy.ID)
		}
	})
}

// waitSwapped waits until attempt id of tile has committed its swap.
func waitSwapped(t *testing.T, f *opsFx, tile string, id int64) {
	t.Helper()
	for i := 0; i < 500; i++ {
		for _, a := range readJournalFile(t, f.root, tile).Attempts {
			if a.ID == id && a.Swapped {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deploy %d of %s never swapped", id, tile)
}

func readJournalFile(t *testing.T, root, tile string) journal {
	t.Helper()
	var j journal
	b, err := os.ReadFile(filepath.Join(journalDir(root, tile), journalFile))
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
