package deployments

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/users"
)

// purgeAct is the purge's row of the authority table, as the contract
// amendment for R-4 adds it to authz.go: a tile manager in a person's own
// session; accepted on a tile without a record (the store outlives an
// opt-out); never closed by the ship-dark switch (it only removes).
var purgeAct = act{what: "purging a checkpoint", rule: ruleManager, post: true, optIn: true}

// purgeOp is the route's Op, "purge": POST /deployments/purge.
const purgeOp = Op("purge")

// purgeTable is a registry holding the purge alone, judged by the authority
// table's row for it: the amendment's once it landed — which must then be
// purgeAct, with the operation registered — and purgeAct, for the test's
// length only, until then.
func purgeTable(t *testing.T) opTable {
	t.Helper()
	if a, ok := acts[purgeOp]; ok {
		if a != purgeAct {
			t.Fatalf("the authority table's purge row is %+v, want %+v", a, purgeAct)
		}
		if !Registered(purgeOp) {
			t.Error("the authority table has the purge, but no operation is registered for it")
		}
	} else {
		acts[purgeOp] = purgeAct
		t.Cleanup(func() { delete(acts, purgeOp) })
	}
	tbl := opTable{}
	addOp(tbl, purgeOp, Handler[PurgeRequest]{Subject: purgeSubject, Run: runPurge})
	return tbl
}

// purgingStore is the fake store with the store's Purge: its keep refuses
// as the store's does, the checkpoint leaves the list, every logged entry
// that deployed it names none, and its materialized tree goes.
type purgingStore struct {
	*fakeStore
	pmu    sync.Mutex
	purged []string // tile@tree, each purge the plane asked for
}

func (s *purgingStore) Purge(ctx context.Context, tile, tree string, keep func() []string) (checkpoint.PurgeResult, error) {
	root := filepath.Join(treesOf(s.root, tile), tree)
	for _, k := range keep() {
		if k == tree || k == root {
			return checkpoint.PurgeResult{}, checkpoint.ErrInUse
		}
	}
	s.pmu.Lock()
	s.purged = append(s.purged, tile+"@"+tree)
	s.pmu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, ok := s.cps[tile][tree]
	if !ok {
		return checkpoint.PurgeResult{}, checkpoint.ErrUnknownCheckpoint
	}
	delete(s.cps[tile], tree)
	res := checkpoint.PurgeResult{Checkpoint: cp}
	for i := range s.logs[tile] {
		if e := &s.logs[tile][i]; e.Tree == tree {
			e.Tree, e.Feed = "", ""
			res.Entries++
		}
	}
	return res, os.RemoveAll(root)
}

func (s *purgingStore) calls() []string {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	return append([]string(nil), s.purged...)
}

// rootsRunner is the fake runner whose running generations bind roots.
type rootsRunner struct {
	*fakeRunner
	rmu   sync.Mutex
	roots []string
}

func (r *rootsRunner) RootsInUse() []string {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	return append([]string(nil), r.roots...)
}

func (r *rootsRunner) bind(roots ...string) {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	r.roots = roots
}

// purgeFx is the operations fixture with the purging store and runner, and
// the manager gate of the D24/D33 shape: mia manages apps/site, admins
// manage everything, and apps/gov is an element whose tile holds xbin admin.
func purgeFx(t *testing.T, isolated bool) (*codeFx, *purgingStore, *rootsRunner, opTable) {
	t.Helper()
	tbl := purgeTable(t)
	f := newCodeFx(t, isolated)
	ps := &purgingStore{fakeStore: f.st}
	f.p.cps = ps
	rr := &rootsRunner{fakeRunner: f.run}
	f.p.Run = rr
	f.p.MayManage = func(pr auth.Principal, tile string) bool {
		return pr.Component == "" && (pr.IsAdmin() || pr.UserID == "mia" && tile == opSite)
	}
	f.p.IsAdmin = func(pr auth.Principal) bool { return pr.IsAdmin() || pr.Component == "apps/gov" }
	return f, ps, rr, tbl
}

// purge dispatches one purge as the handler would.
func purge(f *codeFx, tbl opTable, pr auth.Principal, req *PurgeRequest) (any, error) {
	f.t.Helper()
	return tbl.do(context.Background(), f.p, pr, purgeOp, req)
}

// cpID is the id a request names tree by.
func cpID(tree string) string { return "c:" + tree[:12] }

// logTrees lists tile's deploy log in the fake store, oldest first:
// "<deployment> <tree>", "-" for an entry that names no checkpoint.
func logTrees(s *fakeStore, tile string) []string {
	var out []string
	for _, a := range s.logged(tile) {
		tr := a.Tree
		if tr == "" {
			tr = "-"
		}
		out = append(out, a.Deployment+" "+tr)
	}
	return out
}

// covers T20 T9 C4 NP-06-16 D127f — purging a checkpoint is a tile manager's
// act in a person's own session (05-model §10): the tile's owner or org
// admins (the manager gate) and workspace admins may; terminal level, a
// reader, the manager's own terminal token, the tile's terminal, frame and
// instance credentials, and an element whose tile holds xbin admin may not
// (403, the contract's texts), nor a view-as session of the manager (the
// read-only text); their dry runs are refused alike, and nothing reaches the
// store. The manager's dry run purges nothing; the purge answers the id and
// the deploy-log entries it rewrote, which then name no checkpoint, in the
// store and in the plane's own memory of recent attempts. The act is
// accepted on a tile without a record (its store outlives the opt-out) and
// while the ship-dark switch is off. Until the R-4 amendment lands the
// route is registered nowhere: this test judges the purge by the authority
// table row the amendment adds, and pins it once it has landed.
func TestPurgeManagerOnly(t *testing.T) {
	f, ps, _, tbl := purgeFx(t, false)
	f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
	var trees []string
	for _, v := range []string{"v2", "v3", "v4"} {
		trees = append(trees, cp(f.rec(opSite), "main"))
		f.write(opSite+"/index.html", "<h1>"+v+"</h1>")
		f.settle(opSite, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite}))
	}
	t1, t2, t3 := trees[0], trees[1], trees[2]
	if slices.Contains(trees, cp(f.rec(opSite), "main")) {
		t.Fatal("main still runs a checkpoint the test purges")
	}

	mia := userP("mia", opSite, users.LevelWrite)
	ada := auth.Principal{UserID: "ada", User: &users.User{ID: "ada", Role: users.RoleAdmin}, Via: "session"}
	viewAs := mia
	viewAs.Impersonator = "owner"
	const notManager = "purging a checkpoint is a tile manager's act: the tile's owner, its org's admins, or a workspace admin"
	const ownSession = "purging a checkpoint is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it"
	for _, c := range []struct {
		name string
		pr   auth.Principal
		want string
	}{
		{"terminal level", userP("dev", opSite, users.LevelTerminal), notManager},
		{"a reader", userP("rita", opSite, users.LevelRead), notManager},
		{"the manager's terminal token", terminalP("mia", opSite, users.LevelTerminal), ownSession},
		{"the owner's terminal token", auth.Principal{Component: opSite, Via: "terminal"}, ownSession},
		{"the tile's frame", auth.Principal{Component: opSite, Via: "frame"}, ownSession},
		{"the tile's instance", auth.Principal{Component: opSite, Via: "instance"}, ownSession},
		{"an element holding xbin admin", auth.Principal{Component: "apps/gov", Via: "instance"}, ownSession},
		{"view-as the manager", viewAs, readOnlyMsg},
	} {
		for _, dry := range []bool{true, false} {
			_, err := purge(f, tbl, c.pr, &PurgeRequest{Tile: opSite, Checkpoint: cpID(t1), DryRun: dry})
			wantErr(t, c.name, err, http.StatusForbidden, c.want)
		}
	}
	if got := ps.calls(); len(got) != 0 {
		t.Fatalf("refused purges reached the store: %v", got)
	}

	// the manager: a dry run changes nothing, the purge rewrites the log
	res, err := purge(f, tbl, mia, &PurgeRequest{Tile: opSite, Checkpoint: cpID(t1), DryRun: true})
	if _, ok := res.(DryRunAnswer); err != nil || !ok {
		t.Fatalf("the manager's dry run: %T %v", res, err)
	}
	if len(ps.calls()) != 0 {
		t.Fatal("a dry run purged")
	}
	res, err = purge(f, tbl, mia, &PurgeRequest{Tile: opSite, Checkpoint: cpID(t1)})
	if err != nil {
		t.Fatalf("the manager's purge: %v", err)
	}
	if a, ok := res.(PurgeAnswer); !ok || !strings.HasPrefix(t1, strings.TrimPrefix(a.Purged, "c:")) || a.Entries != 1 {
		t.Errorf("the purge answered %+v", res)
	}
	if got := ps.calls(); !slices.Equal(got, []string{opSite + "@" + t1}) {
		t.Errorf("the store purged %v", got)
	}
	if got := logTrees(ps.fakeStore, opSite); got[0] != "main -" || slices.Contains(got, "main "+t1) {
		t.Errorf("the deploy log after the purge: %v", got)
	}
	for _, a := range f.p.attempts(context.Background(), opSite, "") {
		if a.Tree == t1 {
			t.Errorf("attempt %d still names the purged checkpoint", a.ID)
		}
	}
	_, err = purge(f, tbl, mia, &PurgeRequest{Tile: opSite, Checkpoint: cpID(t1)})
	wantErr(t, "a second purge", err, http.StatusNotFound, opSite+" has no checkpoint")

	// a workspace admin, with the ship-dark switch off
	f.p.OptInClosed = true
	if _, err := purge(f, tbl, ada, &PurgeRequest{Tile: opSite, Checkpoint: cpID(t2)}); err != nil {
		t.Errorf("an admin's purge with tile deployments switched off: %v", err)
	}
	f.p.OptInClosed = false

	// a tile without a record: the store outlives the opt-out
	f.must(ownerP, OpResume, &ResumeRequest{Tile: opSite})
	if f.rec(opSite) != nil {
		t.Fatal("resuming onto main left a record")
	}
	_, err = purge(f, tbl, userP("dev", opSite, users.LevelTerminal), &PurgeRequest{Tile: opSite, Checkpoint: cpID(t3)})
	wantErr(t, "terminal level on a tile without a record", err, http.StatusForbidden, notManager)
	if _, err := purge(f, tbl, ownerP, &PurgeRequest{Tile: opSite, Checkpoint: cpID(t3)}); err != nil {
		t.Errorf("the owner's purge on a tile without a record: %v", err)
	}
	if f.rec(opSite) != nil {
		t.Error("a purge created a record")
	}

	// malformed requests are answered before authority
	for _, c := range []struct {
		name string
		req  *PurgeRequest
		code int
		want string
	}{
		{"no checkpoint", &PurgeRequest{Tile: opSite}, 400, "bad request body: checkpoint is required"},
		{"a bad id", &PurgeRequest{Tile: opSite, Checkpoint: "c:xyz"}, 400, `"c:xyz" is not a checkpoint id`},
		{"no tile", &PurgeRequest{Tile: "apps/nope", Checkpoint: cpID(t1)}, 404, "no such tile: apps/nope"},
	} {
		_, err := purge(f, tbl, userP("rita", opSite, users.LevelRead), c.req)
		wantErr(t, c.name, err, c.code, c.want)
	}
}

// covers T20 D119e T18 NP-06-16 — a purge is refused (409, nothing reaches the
// store) while any deployment runs the checkpoint: the record points at it,
// main's or a non-primary deployment's; a deploy of it is queued or
// running; a running generation still binds its tree. Once nothing does,
// it goes. Over the real store, a static tile: the pinned primary's
// checkpoint is refused, dry run included, and stays; once main moved on,
// the purge rewrites the pause's deploy-log entry to name no checkpoint,
// the checkpoint no longer resolves, its materialized tree goes while the
// primary's stays, the view repository still shows main's, and a roll back
// finds nothing earlier to go to.
func TestPurgeRefusesDeployedCheckpoint(t *testing.T) {
	t.Run("the record, a deploy, a running generation", func(t *testing.T) {
		f, ps, rr, tbl := purgeFx(t, true)
		f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
		t1 := cp(f.rec(opAPI), "main")
		refuse := func(what, tree, why string) {
			t.Helper()
			for _, dry := range []bool{true, false} {
				_, err := purge(f, tbl, ownerP, &PurgeRequest{Tile: opAPI, Checkpoint: cpID(tree), DryRun: dry})
				code, msg := errStatus(err)
				if code != http.StatusConflict || !strings.Contains(msg, "is in use — "+why) {
					t.Errorf("%s (dry %v): %d %q, want 409 …%s…", what, dry, code, msg, why)
				}
			}
		}
		refuse("main's checkpoint", t1, "main runs it")

		// a deploy of t2 held before its swap: main still runs t1
		hold := make(chan struct{})
		f.run.set(func(r *fakeRunner) { r.before = hold })
		f.write(opAPI+"/main.go", "package main // v2\n")
		a := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
		if a.Deploy == nil {
			t.Fatal("the reload queued no deploy")
		}
		c2, err := f.p.store().Resolve(context.Background(), opAPI, a.Deploy.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		t2 := c2.Hash
		refuse("a deploy in flight", t2, "deploy ")
		refuse("main's checkpoint while a deploy runs", t1, "main runs it")
		close(hold)
		f.settle(opAPI, a)
		if cp(f.rec(opAPI), "main") != t2 {
			t.Fatal("the deploy didn't move main")
		}

		// a non-primary deployment
		f.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev", From: FromPrimary})
		f.write(opAPI+"/main.go", "package main // v3\n")
		f.settle(opAPI, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI}))
		if cp(f.rec(opAPI), "dev") != t2 || cp(f.rec(opAPI), "main") == t2 {
			t.Fatalf("dev runs %s and main %s", cp(f.rec(opAPI), "dev"), cp(f.rec(opAPI), "main"))
		}
		refuse("dev's checkpoint", t2, "dev runs it")

		// a generation that still binds t1's tree
		rr.bind(filepath.Join(treesOf(f.root, opAPI), t1) + "/")
		refuse("a running generation's tree", t1, "a running generation of "+opAPI+" still runs it")
		if got := ps.calls(); len(got) != 0 {
			t.Fatalf("refused purges reached the store: %v", got)
		}
		rr.bind()
		if _, err := purge(f, tbl, ownerP, &PurgeRequest{Tile: opAPI, Checkpoint: cpID(t1)}); err != nil {
			t.Fatalf("purging a checkpoint nothing runs: %v", err)
		}
		if got := ps.calls(); !slices.Equal(got, []string{opAPI + "@" + t1}) {
			t.Errorf("the store purged %v", got)
		}
	})

	t.Run("over the real store", func(t *testing.T) {
		needGit(t)
		tbl := purgeTable(t)
		f := newOpsFx(t, false)
		real := checkpoint.New(f.root)
		p := &Plane{Root: f.root, Reg: f.reg, Hub: f.hub, Run: f.run, cps: storeAdapter{real},
			isolated: func() bool { return false }}
		f.reg.PinnedPrimary = p.PinnedPrimary
		if err := p.Boot(); err != nil {
			t.Fatal(err)
		}
		f.p = p
		ctx := context.Background()
		do := func(req *PurgeRequest) (any, error) {
			return tbl.do(ctx, p, ownerP, purgeOp, req)
		}

		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		t1 := cp(f.rec(opSite), "main")
		for _, dry := range []bool{true, false} {
			_, err := do(&PurgeRequest{Tile: opSite, Checkpoint: cpID(t1), DryRun: dry})
			if code, msg := errStatus(err); code != http.StatusConflict || !strings.Contains(msg, "main runs it") {
				t.Errorf("purging the pinned primary's checkpoint (dry %v): %d %q", dry, code, msg)
			}
		}
		if _, err := real.Get(ctx, opSite, t1); err != nil {
			t.Fatalf("a refused purge removed the checkpoint: %v", err)
		}

		f.write(opSite+"/index.html", "<h1>v2</h1>")
		f.settle(opSite, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite}))
		t2 := cp(f.rec(opSite), "main")
		root1, root2 := filepath.Join(real.TreesDir(opSite), t1), filepath.Join(real.TreesDir(opSite), t2)
		if !dirPresent(root1) || !dirPresent(root2) {
			t.Fatal("the fixture's checkpoints aren't materialized")
		}
		res, err := do(&PurgeRequest{Tile: opSite, Checkpoint: cpID(t1)})
		if err != nil {
			t.Fatalf("purging a checkpoint nothing runs: %v", err)
		}
		if a := res.(PurgeAnswer); a.Entries != 1 || !strings.HasPrefix(t1, strings.TrimPrefix(a.Purged, "c:")) {
			t.Errorf("the purge answered %+v", a)
		}
		if _, err := real.Get(ctx, opSite, t1); !errors.Is(err, checkpoint.ErrUnknownCheckpoint) {
			t.Errorf("the purged checkpoint is still in the store: %v", err)
		}
		entries, _, err := real.Log(ctx, opSite, checkpoint.LogQuery{Deployment: "main"})
		if err != nil || len(entries) != 2 {
			t.Fatalf("main's deploy log: %+v, %v", entries, err)
		}
		if old := entries[1]; old.How != "pause" || old.Checkpoint != "" || old.Result != checkpoint.LogOK || old.By != "owner" {
			t.Errorf("the pause's entry after the purge: %+v", old)
		}
		if cur := entries[0]; cur.Checkpoint != t2 || cur.Previous != t1 {
			t.Errorf("the reload's entry after the purge: %+v", cur)
		}
		if dirPresent(root1) || !dirPresent(root2) {
			t.Errorf("materialized trees after the purge: purged %v, primary's %v", dirPresent(root1), dirPresent(root2))
		}
		if view := viewTree(t, real.ViewDir(opSite), "refs/heads/deploy/main"); view != t2 {
			t.Errorf("the view repository shows main at %s, want %s", view, t2)
		}
		_, err = f.do(ownerP, OpRollback, &RollbackRequest{Tile: opSite})
		wantErr(t, "a roll back after the purge", err, http.StatusConflict, "main has no earlier checkpoint in its deploy log")
		if _, err := do(&PurgeRequest{Tile: opSite, Checkpoint: cpID(t2)}); err == nil {
			t.Error("the primary's checkpoint was purged")
		}
	})
}

// dirPresent reports whether p is a directory, not following a symlink.
func dirPresent(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// viewTree is the tree a ref of the view repository at dir points at.
func viewTree(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+dir, "rev-parse", ref+"^{tree}")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("reading %s of %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}
