package deployments

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers 05-model §5 P9 NP-02-11 NP-07-2 — every M1 operation onto main
// runs on a literal of the plane's inputs (a checkpoint store, a runner
// facade, a hub), with no broker: flow A on a static tile (pause, reload
// now, roll back, deploy a named checkpoint, restart, resume back to the
// zero state) and on a Go tile, whose code moves through the runner's
// blue/green deploy; the record's pointer moves at request time off the work
// tree and after the swap between checkpoints (07-runtime §8.2); a failed
// pause leaves live reload detached with the attempted checkpoint pinned,
// state failed, which reload now retries; a failed pinned → pinned deploy
// leaves the previous code; a failed capture or materialization changes
// nothing and catches the deployment up; the queue runs one deploy per
// deployment, merges a request into an equal tail, and refuses a ninth;
// the requests' fields (expect, checkpoint, restart, seq) and refusals
// answer 11-contract §1.14's texts.
func TestDeployPlaneOperations(t *testing.T) {
	t.Run("flow A on a static tile", func(t *testing.T) {
		f := newOpsFx(t, false)
		if z := f.p.Lookup(opSite); z.State != RecordNone || z.Record.Seq != 0 || z.Record.LiveReload != "main" ||
			!reflect.DeepEqual(f.p.Deploys(t.Context(), opSite, "main"), DeployFacts{}) {
			t.Fatalf("zero state = %+v", z)
		}
		zs := f.p.subjectOf(opSite, "main")
		if !f.p.Can(ownerP, OpPause, zs).OK || !f.p.Policy(OpPause, zs).OK || f.p.Can(ownerP, OpResume, zs).OK {
			t.Errorf("zero-state permissions: pause %+v %+v, resume %+v", f.p.Can(ownerP, OpPause, zs), f.p.Policy(OpPause, zs), f.p.Can(ownerP, OpResume, zs))
		}
		f.evs.take()

		// Pause: main pinned to the work tree's checkpoint, at request time.
		ans := f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		r := f.rec(opSite)
		if r == nil || r.Seq != 1 || r.LiveReload != "" || r.LastLiveReload != "main" || r.Deployments["main"].Checkpoint == nil {
			t.Fatalf("record after pause = %+v", r)
		}
		tree1 := *r.Deployments["main"].Checkpoint
		if d := ans.Deploy; d == nil || d.How != "pause" || d.Result != resultOK || d.Checkpoint != "c:"+tree1[:7] ||
			d.By != "owner" || d.FollowsWorkTree || d.Feed != checkpoint.FeedWorkTree {
			t.Fatalf("pause's deploy = %+v", ans.Deploy)
		}
		if df := f.p.Deploys(t.Context(), opSite, "main"); df.Serving != "c:"+tree1[:7] || df.Failed || df.Deploying != nil ||
			df.LastDeploy == nil || df.LastDeploy.How != "pause" || r.LiveReloadSince == nil || r.LiveReloadSince.By != "owner" ||
			r.Deployments["main"].By != "owner" {
			t.Errorf("after pause: facts %+v, record %+v", df, r)
		}
		if dep, on := f.p.LiveReload(opSite); dep != "" || on {
			t.Errorf("LiveReload after pause = %q %v", dep, on)
		}
		if code, err := f.p.CodeFor(opSite, "main"); err != nil || code.Tree != tree1 {
			t.Errorf("CodeFor = %+v %v", code, err)
		}
		c, _ := f.reg.Component(opSite)
		if pc, ok := f.p.PinnedPrimary(opSite); !ok || pc.ManifestErr != "" || !pc.HasIndex || c.WorkTree == nil {
			t.Errorf("PinnedPrimary = %+v %v; the registry composes the checkpoint: %v", pc, ok, c.WorkTree != nil)
		}
		if root, pinned, err := f.p.CodeRoot(c, ""); err != nil || !pinned || !strings.HasSuffix(root, tree1) {
			t.Errorf("CodeRoot = %q %v %v", root, pinned, err)
		}
		evs := f.evs.take()
		wantEvents(t, evs, "pause", []string{
			`deployments {"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}`,
			`deployments {"op":"record","what":["liveReload","deployments"]}`,
			`deployments {"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:` + tree1[:7] + `","result":"running","phase":"swap","by":"owner"}`,
			`deployments {"op":"deploy","deployment":"main","checkpoint":"c:` + tree1[:7] + `","result":"running","phase":"swap","by":"owner"}`,
			`deployments {"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:` + tree1[:7] + `","result":"ok","phase":"swap","by":"owner"}`,
			`deployments {"op":"deploy","deployment":"main","checkpoint":"c:` + tree1[:7] + `","result":"ok","phase":"swap","by":"owner"}`,
		})
		if got := f.st.logged(opSite); len(got) != 1 || got[0].How != "pause" || got[0].Tree != tree1 || got[0].Result != resultOK {
			t.Errorf("deploy log after pause = %+v", got)
		}

		// Pausing again changes nothing.
		again := f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		if again.Deploy != nil || f.rec(opSite).Seq != 1 {
			t.Errorf("a second pause: %+v, seq %d", again.Deploy, f.rec(opSite).Seq)
		}

		// Saves reload nothing; reload now ships the work tree once.
		f.write(opSite+"/index.html", "<h1>v2</h1>")
		ans = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite})
		tree2 := *f.rec(opSite).Deployments["main"].Checkpoint
		if tree2 == tree1 || ans.Deploy == nil || ans.Deploy.How != "reload-now" || ans.Deploy.Previous != "c:"+tree1[:7] ||
			ans.Deploy.Result != resultOK || f.rec(opSite).Seq != 2 {
			t.Fatalf("reload now: %+v, record %+v", ans.Deploy, f.rec(opSite))
		}
		if !hasEvent(f.evs.take(), `reload {} `+opSite) {
			t.Error("reload now onto the primary announced no bare reload")
		}
		if ans := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite}); !ans.Unchanged || ans.Deploy != nil {
			t.Errorf("reload now of an unchanged work tree: %+v", ans)
		}
		f.write(opSite+"/index.html", "<h1>v3</h1>")
		_, err := f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: "c:" + tree2[:7]})
		if code, msg := errStatus(err); code != http.StatusConflict || !strings.HasPrefix(msg, "the code changed since you reviewed c:"+tree2[:7]+" (now c:") {
			t.Errorf("reload now with a stale expect: %d %s", code, msg)
		}
		f.write(opSite+"/index.html", "<h1>v2</h1>")
		if ans := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: "c:" + tree2[:7]}); !ans.Unchanged {
			t.Errorf("reload now with the reviewed expect: %+v", ans)
		}

		// Roll back: the newest ok entry whose checkpoint differs.
		ans = f.must(ownerP, OpRollback, &RollbackRequest{Tile: opSite})
		if ans.Deploy == nil || ans.Deploy.How != "rollback" || *f.rec(opSite).Deployments["main"].Checkpoint != tree1 {
			t.Fatalf("roll back: %+v, now %s", ans.Deploy, *f.rec(opSite).Deployments["main"].Checkpoint)
		}
		// Deploy a named checkpoint; the rollback target is then tree1 again.
		ans = f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "main", Checkpoint: "c:" + tree2[:7]})
		if ans.Deploy == nil || ans.Deploy.How != "deploy" || *f.rec(opSite).Deployments["main"].Checkpoint != tree2 {
			t.Fatalf("deploy c:%s: %+v", tree2[:7], ans.Deploy)
		}
		ans = f.must(ownerP, OpRollback, &RollbackRequest{Tile: opSite, Deployment: "main"})
		if *f.rec(opSite).Deployments["main"].Checkpoint != tree1 {
			t.Errorf("the second roll back went to %s", *f.rec(opSite).Deployments["main"].Checkpoint)
		}
		// Restart: the current code, a new generation; the record stays.
		seq := f.rec(opSite).Seq
		ans = f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Restart: true})
		if ans.Deploy == nil || ans.Deploy.How != "restart" || ans.Deploy.Result != resultOK || f.rec(opSite).Seq != seq {
			t.Errorf("restart: %+v, seq %d → %d", ans.Deploy, seq, f.rec(opSite).Seq)
		}

		// The log holds every attempt, newest first, and each one by id.
		entries, more := f.p.Log(t.Context(), opSite, "", 0, 0)
		var hows []string
		for _, e := range entries {
			hows = append(hows, e.How)
		}
		if more || strings.Join(hows, " ") != "restart rollback deploy rollback reload-now pause" {
			t.Errorf("log = %v (more %v)", hows, more)
		}
		if e, err := f.p.Entry(t.Context(), opSite, entries[0].ID, 0); err != nil || e.How != "restart" {
			t.Errorf("Entry(%d) = %+v %v", entries[0].ID, e, err)
		}
		if _, err := f.p.Entry(t.Context(), opSite, 999, 0); !errors.Is(err, ErrNoAttempt) {
			t.Errorf("Entry(999): %v", err)
		}

		// Resume: back to the work tree, which returns main-only to the zero state.
		f.evs.take()
		ans = f.must(ownerP, OpResume, &ResumeRequest{Tile: opSite})
		if f.rec(opSite) != nil || ans.Deploy == nil || ans.Deploy.How != "resume" || !ans.Deploy.FollowsWorkTree {
			t.Fatalf("resume: record %+v, deploy %+v", f.rec(opSite), ans.Deploy)
		}
		if dep, on := f.p.LiveReload(opSite); dep != "main" || !on {
			t.Errorf("LiveReload after resume = %q %v", dep, on)
		}
		if !hasEvent(f.evs.take(), `reload {} `+opSite) {
			t.Error("resume announced no reload")
		}
	})

	t.Run("a Go tile deploys through the runner", func(t *testing.T) {
		f := newOpsFx(t, true)
		ans := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		tree1 := *f.rec(opAPI).Deployments["main"].Checkpoint
		if ans.Deploy == nil || (ans.Deploy.Result != resultQueued && ans.Deploy.Result != resultRunning) {
			t.Fatalf("pause's deploy = %+v, want it queued behind the runner", ans.Deploy)
		}
		if e := f.wait(opAPI, ans.Deploy.ID); e.Result != resultOK {
			t.Fatalf("pause's deploy finished %+v", e)
		}
		if n, _ := f.run.counts(); n != 1 {
			t.Errorf("the runner deployed %d times", n)
		}
		// A failed pinned → pinned deploy keeps the previous code (P9).
		f.write(opAPI+"/main.go", "package main // v2\n")
		f.run.set(func(r *fakeRunner) { r.fail = errors.New("build failed: main.go:1: syntax") })
		ans = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if e := f.wait(opAPI, ans.Deploy.ID); e.Result != resultFailed || !strings.Contains(e.Error, "syntax") {
			t.Fatalf("a failing reload now = %+v", e)
		}
		if r := f.rec(opAPI); *r.Deployments["main"].Checkpoint != tree1 || r.Deployments["main"].State != "" {
			t.Errorf("a failed pinned → pinned deploy moved the record: %+v", r.Deployments["main"])
		}
		if df := f.p.Deploys(t.Context(), opAPI, "main"); !strings.Contains(df.Error, "syntax") || df.LastDeploy == nil ||
			df.LastDeploy.Result != resultFailed || df.Serving != "c:"+tree1[:7] {
			t.Errorf("facts after a failed deploy = %+v, last %+v", df, df.LastDeploy)
		}
		f.run.set(func(r *fakeRunner) { r.fail = nil })
		ans = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if e := f.wait(opAPI, ans.Deploy.ID); e.Result != resultOK || *f.rec(opAPI).Deployments["main"].Checkpoint == tree1 {
			t.Errorf("the retried reload now = %+v", e)
		}
	})

	t.Run("a restart runs through the runner's Restart", func(t *testing.T) {
		f := newOpsFx(t, true)
		rr := &restartingRunner{fakeRunner: f.run}
		f.p.Run = rr
		a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		f.wait(opAPI, a.Deploy.ID)
		seq := f.rec(opAPI).Seq
		a = f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Restart: true})
		if e := f.wait(opAPI, a.Deploy.ID); e.How != "restart" || e.Result != resultOK || len(rr.restarts) != 1 || f.rec(opAPI).Seq != seq {
			t.Errorf("restart = %+v, restarts %v, seq %d → %d", e, rr.restarts, seq, f.rec(opAPI).Seq)
		}
		rr.set(func(r *fakeRunner) { r.fail = errors.New("start: health check failed") })
		a = f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Restart: true})
		if e := f.wait(opAPI, a.Deploy.ID); e.Result != resultFailed || f.rec(opAPI).Deployments["main"].State != "" {
			t.Errorf("a failed restart = %+v, state %q", e, f.rec(opAPI).Deployments["main"].State)
		}
	})

	t.Run("a failed pause leaves the attempted checkpoint pinned, failed", func(t *testing.T) {
		f := newOpsFx(t, true)
		f.run.set(func(r *fakeRunner) { r.fail = errors.New("start: health check failed") })
		ans := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		if e := f.wait(opAPI, ans.Deploy.ID); e.Result != resultFailed {
			t.Fatalf("pause's deploy = %+v", e)
		}
		r := f.rec(opAPI)
		if r.LiveReload != "" || r.Deployments["main"].Checkpoint == nil || r.Deployments["main"].State != "failed" {
			t.Fatalf("record after a failed pause = %+v %+v", r, r.Deployments["main"])
		}
		tree := *r.Deployments["main"].Checkpoint
		if code, _ := f.p.CodeFor(opAPI, "main"); code.Tree != tree {
			t.Errorf("restarts run %+v, want the attempted checkpoint", code)
		}
		// Reload now retries the same checkpoint (a restart from its artifact).
		f.run.set(func(r *fakeRunner) { r.fail = nil })
		ans = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if ans.Unchanged || ans.Deploy == nil || ans.Deploy.How != "restart" {
			t.Fatalf("reload now after a failed pause = %+v", ans)
		}
		if e := f.wait(opAPI, ans.Deploy.ID); e.Result != resultOK || f.rec(opAPI).Deployments["main"].State != "" {
			t.Errorf("the retry = %+v, state %q", e, f.rec(opAPI).Deployments["main"].State)
		}
	})

	t.Run("a failed capture or materialization changes nothing", func(t *testing.T) {
		for name, set := range map[string]func(s *fakeStore){
			"capture":     func(s *fakeStore) { s.failCapture = errors.New("store unwell") },
			"materialize": func(s *fakeStore) { s.failMaterialize = errors.New("disk full") },
			"refused": func(s *fakeStore) {
				s.failCapture = &checkpoint.Refusal{Tile: opAPI, Rule: checkpoint.RuleBytes, Detail: "too big"}
			},
			"rate": func(s *fakeStore) { s.failCapture = &checkpoint.RateLimited{Tile: opAPI, RetryAfter: time.Second} },
		} {
			t.Run(name, func(t *testing.T) {
				f := newOpsFx(t, true)
				f.st.set(set)
				_, err := f.do(ownerP, OpPause, &PauseRequest{Tile: opAPI})
				code, _ := errStatus(err)
				want := map[string]int{"capture": 500, "materialize": 500, "refused": 409, "rate": 429}[name]
				if code != want {
					t.Errorf("pause = %v, want %d", err, want)
				}
				if f.rec(opAPI) != nil {
					t.Error("a failed pause committed a record")
				}
				if dep, on := f.p.LiveReload(opAPI); dep != "main" || !on {
					t.Errorf("LiveReload = %q %v: live reload must stay attached", dep, on)
				}
				if _, changed := f.run.counts(); changed != 1 {
					t.Errorf("ChangedDeployment ran %d times, want once to catch up on the saves ignored meanwhile", changed)
				}
			})
		}
	})

	t.Run("one deploy in flight, a merged tail, eight waiting", func(t *testing.T) {
		f := newOpsFx(t, true)
		ans := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		f.wait(opAPI, ans.Deploy.ID)
		hold := make(chan struct{})
		f.run.set(func(r *fakeRunner) { r.before = hold })
		var first Answer
		var ids []int64
		for i := 0; i < 9; i++ {
			f.write(opAPI+"/main.go", "package main // "+string(rune('a'+i))+"\n")
			a := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
			if i == 0 {
				first = a
				waitRunning(t, f, opAPI, a.Deploy.ID)
			}
			ids = append(ids, a.Deploy.ID)
		}
		// The ninth request's tree equals the tail's: merged into it.
		merged := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if merged.Deploy.ID != ids[8] {
			t.Errorf("an equal request got deploy %d, want the tail's %d", merged.Deploy.ID, ids[8])
		}
		if df := f.p.Deploys(t.Context(), opAPI, "main"); df.Deploying == nil || df.Deploying.ID != first.Deploy.ID || len(df.Queued) != 8 {
			t.Errorf("facts = %+v", df)
		}
		f.write(opAPI+"/main.go", "package main // overflow\n")
		_, err := f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if code, msg := errStatus(err); code != http.StatusConflict || msg != "main already has 8 deploys waiting; try again when one finishes" {
			t.Errorf("a ninth waiting deploy: %d %s", code, msg)
		}
		close(hold)
		last := f.wait(opAPI, ids[8])
		if last.Result != resultOK || *f.rec(opAPI).Deployments["main"].Checkpoint == "" {
			t.Errorf("the tail = %+v", last)
		}
		if n, _ := f.run.counts(); n != 1+9 {
			t.Errorf("the runner deployed %d times, want 10 (one per accepted attempt)", n)
		}
	})

	t.Run("requests and refusals", func(t *testing.T) {
		f := newOpsFx(t, true)
		cases := []struct {
			name   string
			op     Op
			req    any
			status int
			msg    string
		}{
			{"resume without a record", OpResume, &ResumeRequest{Tile: opSite}, 409, "apps/site has no deployments yet: pause live reload or add a deployment first"},
			{"reload now without a record", OpReloadNow, &ReloadNowRequest{Tile: opSite}, 409, "apps/site has no deployments yet"},
			{"deploy without a record", OpDeploy, &DeployRequest{Tile: opSite}, 409, "apps/site has no deployments yet"},
			{"unknown tile", OpPause, &PauseRequest{Tile: "apps/nope"}, 404, "no such tile: apps/nope"},
			{"no tile", OpPause, &PauseRequest{}, 400, "bad request body: tile is required"},
			{"a zero-state qualifier", OpPause, &PauseRequest{Tile: opSite + "+main"}, 404, "no such tile: apps/site+main"},
		}
		for _, c := range cases {
			_, err := f.do(ownerP, c.op, c.req)
			if code, msg := errStatus(err); code != c.status || !strings.HasPrefix(msg, c.msg) {
				t.Errorf("%s: %d %q, want %d %q", c.name, code, msg, c.status, c.msg)
			}
		}
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		seq := f.rec(opSite).Seq
		cases = []struct {
			name   string
			op     Op
			req    any
			status int
			msg    string
		}{
			{"checkpoint and expect", OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: "c:1234567", Expect: "c:1234567"}, 400, "send checkpoint or expect, not both"},
			{"restart with a checkpoint", OpDeploy, &DeployRequest{Tile: opSite, Restart: true, Checkpoint: "c:1234567"}, 400, "restart runs main's current code: send no checkpoint or expect with it"},
			{"a bad id", OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: "3f2a"}, 400, `"3f2a" is not a checkpoint id`},
			{"an unknown checkpoint", OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: "c:0000000"}, 404, "apps/site has no checkpoint c:0000000"},
			{"a bad expect", OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: "HEAD"}, 400, `"HEAD" is not a checkpoint id`},
			{"an unknown deployment", OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev"}, 404, `apps/site has no deployment "dev"`},
			{"a bad name", OpRollback, &RollbackRequest{Tile: opSite, Deployment: "Dev"}, 400, badNameMsg},
			{"a qualifier and a field disagree", OpDeploy, &DeployRequest{Tile: opSite + "+main", Deployment: "dev"}, 400, `the tile ref names deployment "main" and the body "dev"`},
			{"nothing to roll back to", OpRollback, &RollbackRequest{Tile: opSite}, 409, "main has no earlier checkpoint in its deploy log"},
			{"a stale seq", OpReloadNow, &ReloadNowRequest{Tile: opSite, Seq: ptr(seq - 1)}, 409, "the deployments of apps/site changed (seq 1); reload and retry"},
			{"resume onto a deployment the tile doesn't have", OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev"}, 404, `apps/site has no deployment "dev"`},
		}
		for _, c := range cases {
			_, err := f.do(ownerP, c.op, c.req)
			if code, msg := errStatus(err); code != c.status || !strings.HasPrefix(msg, c.msg) {
				t.Errorf("%s: %d %q, want %d %q", c.name, code, msg, c.status, c.msg)
			}
		}
		if f.rec(opSite).Seq != seq {
			t.Errorf("refused requests moved seq %d → %d", seq, f.rec(opSite).Seq)
		}
		// A qualified ref selects main on a tile with a record; seq matches.
		if ans := f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite + "+main", Restart: true, Seq: ptr(seq)}); ans.Deploy == nil {
			t.Errorf("a restart through the qualified ref: %+v", ans)
		}
		// Confirm tokens guard data (the operations beyond main's take them).
		if err := confirmed("", "erase", "removing dev erases its data"); err == nil || err.Error() != `removing dev erases its data: send confirm:"erase" to proceed` {
			t.Errorf("a missing confirm: %v", err)
		}
		if confirmed("erase", "erase", "") != nil {
			t.Error("the right confirm token was refused")
		}
		// A record written by a newer xbind holds the tile: every write is 409.
		mustWrite(t, recordPath(f.root, opAPI), `{"schema":9,"tile":"`+opAPI+`"}`)
		f.boot()
		_, err := f.do(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		if code, msg := errStatus(err); code != http.StatusConflict || msg != "apps/api's deployment record was written by a newer xbind (schema 9)" {
			t.Errorf("pause over a newer record: %d %s", code, msg)
		}
	})

	t.Run("the ship-dark switch and view-as sessions", func(t *testing.T) {
		f := newOpsFx(t, true)
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		viewer := ownerP
		viewer.Impersonator = "owner"
		if _, err := f.do(viewer, OpReloadNow, &ReloadNowRequest{Tile: opSite}); !strings.HasPrefix(errText(err), "read-only: ") {
			t.Errorf("reload now in a view-as session: %v", err)
		}
		f.p.OptInClosed = true
		for _, c := range []struct {
			op   Op
			tile string
			req  any
		}{
			{OpPause, opAPI, &PauseRequest{Tile: opAPI}},
			{OpReloadNow, opSite, &ReloadNowRequest{Tile: opSite}},
			{OpDeploy, opSite, &DeployRequest{Tile: opSite}},
			{OpRollback, opSite, &RollbackRequest{Tile: opSite}},
		} {
			_, err := f.do(ownerP, c.op, c.req)
			if code, msg := errStatus(err); code != http.StatusConflict || !strings.Contains(msg, "--tile-deployments=off") {
				t.Errorf("%s with the switch off: %d %s", c.op, code, msg)
			}
		}
		if f.rec(opAPI) != nil || f.rec(opSite).Seq != 1 {
			t.Error("refused operations changed a record")
		}
		// Returning to the zero state stays open.
		f.must(ownerP, OpResume, &ResumeRequest{Tile: opSite})
		if f.rec(opSite) != nil {
			t.Error("resume onto main with the switch off kept the record")
		}
	})

	t.Run("dry runs judge as for real and change nothing", func(t *testing.T) {
		f := newOpsFx(t, true)
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		f.write(opSite+"/index.html", "<h1>next</h1>")
		before := *f.rec(opSite)
		for _, c := range []struct {
			op  Op
			req any
		}{
			{OpReloadNow, &ReloadNowRequest{Tile: opSite, DryRun: true}},
			{OpDeploy, &DeployRequest{Tile: opSite, DryRun: true}},
			{OpResume, &ResumeRequest{Tile: opSite, DryRun: true}},
			{OpDeploy, &DeployRequest{Tile: opSite, Restart: true, DryRun: true}},
		} {
			res, err := f.do(ownerP, c.op, c.req)
			dr, ok := res.(DryRunAnswer)
			if err != nil || !ok || dr.Impact.Stops == nil || dr.Impact.Reloads == nil {
				t.Fatalf("dry %s = %#v %v", c.op, res, err)
			}
			if c.op == OpReloadNow && (dr.Impact.Code == nil || dr.Impact.Code.To == dr.Impact.Code.From || dr.Impact.Affects != "everyone" ||
				!reflect.DeepEqual(dr.Impact.Reloads, []string{"main"})) {
				t.Errorf("reload now's impact = %+v %+v", dr.Impact, dr.Impact.Code)
			}
			if c.op == OpResume && (dr.Impact.Code == nil || dr.Impact.Code.To != "work-tree") {
				t.Errorf("resume's impact = %+v", dr.Impact.Code)
			}
		}
		if after := f.rec(opSite); after.Seq != before.Seq || *after.Deployments["main"].Checkpoint != *before.Deployments["main"].Checkpoint {
			t.Errorf("dry runs changed the record: %+v → %+v", before, after)
		}
		if es, _ := f.p.Log(t.Context(), opSite, "", 0, 0); len(es) != 1 {
			t.Errorf("dry runs logged: %d entries", len(es))
		}
	})
}

// wantEvents compares the deployments and reload events of a step with want,
// each as "<type> <data JSON>", in order.
func wantEvents(t *testing.T, evs []events.Event, step string, want []string) {
	t.Helper()
	var got []string
	for _, e := range evs {
		b, _ := json.Marshal(e.Data)
		got = append(got, e.Type+" "+string(b))
		if e.Component == "" || strings.Contains(e.Component, "+") {
			t.Errorf("%s: an event with component %q", step, e.Component)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s published\n  %s\nwant\n  %s", step, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// hasEvent reports whether evs holds "<type> <data JSON> <component>".
func hasEvent(evs []events.Event, want string) bool {
	for _, e := range evs {
		b, _ := json.Marshal(e.Data)
		if string(b) == "null" {
			b = []byte("{}")
		}
		if e.Type+" "+string(b)+" "+e.Component == want {
			return true
		}
	}
	return false
}

// waitRunning waits until attempt id of tile is in flight.
func waitRunning(t *testing.T, f *opsFx, tile string, id int64) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if e, err := f.p.Entry(t.Context(), tile, id, 0); err == nil && e.Result == resultRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deploy %d of %s never started", id, tile)
}

func ptr[T any](v T) *T { return &v }

// fileExists reports whether p exists.
func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

var _ = util.MainDeployment

// errText is err's text, "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
