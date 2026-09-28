package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// The branch-assigned deployments' tests (D131): the explicit ops' check and
// its override, the save path's guard, the trailer's second check, the
// branch route, add's newBranch, and the record as an older xbind keeps it.

// checkout points opSite's work tree at branch b, as a git checkout would
// (only HEAD: the plane reads nothing else); "" detaches it.
func (f *opsFx) checkout(b string) {
	f.t.Helper()
	head := "ref: refs/heads/" + b + "\n"
	if b == "" {
		head = strings.Repeat("ab", 20) + "\n"
	}
	f.write(opSite+"/.git/HEAD", head)
}

// addDep adds dep to opSite with req's other fields and fails on a refusal.
func (f *opsFx) addDep(req AddRequest) {
	f.t.Helper()
	req.Tile = opSite
	if _, err := f.do(ownerP, OpAdd, &req); err != nil {
		f.t.Fatalf("add %s: %v", req.Deployment, err)
	}
}

func wantConflict(t *testing.T, what string, err error, parts ...string) {
	t.Helper()
	status, msg := errStatus(err)
	if status != 409 {
		t.Fatalf("%s: status %d (%s), want 409", what, status, msg)
	}
	for _, p := range parts {
		if !strings.Contains(msg, p) {
			t.Errorf("%s: %q doesn't say %q", what, msg, p)
		}
	}
}

// covers D131 — the ops that feed a deployment from the work tree refuse a
// work tree on another branch than its own (409 naming both), and
// confirm:"other-branch" takes it this time: kept as the target's override
// by the ops that make it the live reload target, lapsing when live reload
// moves. Ops that don't feed it from the work tree (a deploy of a
// checkpoint, promote) aren't asked; main and the primary take no branch.
func TestBranchExplicitOps(t *testing.T) {
	f := newOpsFx(t, false)
	f.checkout("feature")
	f.addDep(AddRequest{Deployment: "dev", Attach: true, Branch: "feature"})
	rec := f.rec(opSite)
	if rec.LiveReload != "dev" || rec.Deployments["dev"].Branch != "feature" || rec.AssignedBranch("dev") != "feature" {
		t.Fatalf("add with attach and branch: live reload %q, dev %+v", rec.LiveReload, rec.Deployments["dev"])
	}
	f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})

	f.checkout("main")
	_, err := f.do(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev"})
	wantConflict(t, "resume on main", err, "dev is assigned branch feature", "the work tree is on main", `confirm:"other-branch"`)
	_, err = f.do(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev", DryRun: true})
	wantConflict(t, "a dry run of it", err, "feature", "main")
	_, err = f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite})
	wantConflict(t, "reload now", err, "feature", "main")
	_, err = f.do(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev"})
	wantConflict(t, "a deploy of the work tree", err, "feature", "main")
	if _, err := f.do(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev", Confirm: "erase"}); err == nil {
		t.Error("resume took a confirm other than other-branch")
	}

	// The dry run reports both branches; the override is the target's.
	res, err := f.do(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmOtherBranch, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if b := res.(DryRunAnswer).Impact.Branch; b == nil || b.Assigned != "feature" || b.WorkTree != "main" || !b.Other {
		t.Errorf("resume's dry run branch %+v", b)
	}
	f.must(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmOtherBranch})
	if rec := f.rec(opSite); rec.LiveReload != "dev" || rec.BranchOverride("dev") != "main" {
		t.Fatalf("resume with other-branch: live reload %q, override %q", rec.LiveReload, rec.BranchOverride("dev"))
	}
	// While dev takes main this time, the other ops on it do too.
	if res, err := f.do(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev", DryRun: true}); err != nil {
		t.Fatalf("a deploy on the override's branch: %v", err)
	} else if b := res.(DryRunAnswer).Impact.Branch; b == nil || !b.Other || b.WorkTree != "main" {
		t.Errorf("its impact.branch %+v", b)
	}

	// Live reload moves: the override lapses. main takes any branch.
	f.must(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "main"})
	if rec := f.rec(opSite); rec.LiveReload != "main" || rec.Deployments["dev"].BranchOverride != "" {
		t.Fatalf("attach to main: live reload %q, dev %+v", rec.LiveReload, rec.Deployments["dev"])
	}
	_, err = f.do(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"})
	wantConflict(t, "attach on main", err, "feature", "main")
	f.must(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmOtherBranch})
	if o := f.rec(opSite).BranchOverride("dev"); o != "main" {
		t.Errorf("attach with other-branch: override %q", o)
	}

	// A detached HEAD is no branch: a one-shot op may take it, live reload never.
	f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
	f.checkout("")
	_, err = f.do(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmOtherBranch})
	wantConflict(t, "resume detached", err, "isn't on a branch")
	f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, Confirm: ConfirmOtherBranch})

	// A deploy of a checkpoint and a promote aren't fed by the work tree.
	f.checkout("main")
	cp := f.rec(opSite).Deployments["main"].Checkpoint
	f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev", Checkpoint: shortTree(*cp)})
	f.must(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: "main", To: "dev"})

	// Add from the work tree onto a branch the work tree isn't on.
	_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "qa", Branch: "release"})
	wantConflict(t, "add from the work tree", err, "qa is assigned branch release", "main")
	f.addDep(AddRequest{Deployment: "qa", Branch: "release", From: FromPrimary})
	if b := f.rec(opSite).AssignedBranch("qa"); b != "release" {
		t.Errorf("add from the primary with a branch: %q", b)
	}

	// The branch route: never main or the primary; the primary reassigned
	// to a deployment clears its branch.
	_, err = f.do(ownerP, OpBranch, &BranchRequest{Tile: opSite, Deployment: "main", Branch: nstr("x")})
	wantConflict(t, "main's branch", err, "takes no assigned branch")
	if _, err := f.do(ownerP, OpBranch, &BranchRequest{Tile: opSite, Deployment: "dev"}); err == nil {
		t.Error("the branch route took a body without branch")
	}
	if _, err := f.do(ownerP, OpBranch, &BranchRequest{Tile: opSite, Deployment: "dev", Branch: nstr("-x")}); err == nil {
		t.Error("the branch route took -x")
	}
	f.must(ownerP, OpBranch, &BranchRequest{Tile: opSite, Deployment: "qa", Branch: NullString{Set: true}})
	if d := f.rec(opSite).Deployments["qa"]; d.Branch != "" {
		t.Errorf("branch null left %q", d.Branch)
	}
	f.must(ownerP, OpBranch, &BranchRequest{Tile: opSite, Deployment: "qa", Branch: nstr("release")})
	if _, err := f.do(ownerP, OpBranch, &BranchRequest{Tile: opSite, Deployment: "qa", Branch: nstr("release")}); err != nil {
		t.Errorf("the same branch again: %v", err)
	}
	rec = f.rec(opSite).Clone()
	rec.Primary = "qa"
	if rec.AssignedBranch("qa") != "" {
		t.Error("the primary reads as assigned a branch")
	}
}

func nstr(s string) NullString { return NullString{Set: true, Value: &s} }

// guardFx is the operations fixture with a deploy recorder for guarded
// batches: GuardBatch as the watcher loop calls it, and a wait for the
// tile's guard worker to go idle.
type guardFx struct {
	*opsFx
	mu       sync.Mutex
	deployed int
}

func (g *guardFx) batch() {
	g.t.Helper()
	c, ok := g.reg.Component(opSite)
	if !ok {
		g.t.Fatal("no " + opSite)
	}
	rec := g.rec(opSite)
	g.p.GuardBatch(c, rec.LiveReload, true, func(bool) {
		g.mu.Lock()
		g.deployed++
		g.mu.Unlock()
	})
	g.idle()
}

func (g *guardFx) idle() {
	g.t.Helper()
	for i := 0; i < 500; i++ {
		g.p.guard.mu.Lock()
		busy := g.p.guard.tiles[opSite] != nil && g.p.guard.tiles[opSite].busy
		g.p.guard.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.t.Fatal("the guard worker never went idle")
}

func (g *guardFx) deploys() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.deployed
}

// branchEvents are the op branch events published since the last take.
func (f *opsFx) branchEvents() []branchEvent {
	var out []branchEvent
	for _, e := range f.evs.take() {
		if b, ok := e.Data.(branchEvent); ok && e.Type == "deployments" {
			out = append(out, b)
		}
	}
	return out
}

// covers D131 — a save batch whose live reload target has an assigned
// branch deploys only while the work tree is on it: on another branch
// nothing of the batch deploys, live reload pauses with the target pinned to
// the checkpoint it runs (the last batch's capture), and op branch names
// the deployment, both branches and the deployment assigned the work tree's
// (related). Tiles without a branch are never guarded.
func TestBranchSaveGuard(t *testing.T) {
	g := &guardFx{opsFx: newOpsFx(t, false)}
	if aware, guarded := g.p.Branches(opSite, "main"); aware || guarded {
		t.Fatal("a zero-state tile is branch-aware")
	}
	g.checkout("feature")
	g.addDep(AddRequest{Deployment: "dev", Attach: true, Branch: "feature"})
	g.addDep(AddRequest{Deployment: "staging", Branch: "release", From: FromPrimary})
	if aware, guarded := g.p.Branches(opSite, "dev"); !aware || !guarded {
		t.Fatalf("Branches(dev) = %v, %v", aware, guarded)
	}
	if aware, guarded := g.p.Branches(opSite, "main"); !aware || guarded {
		t.Fatalf("Branches(main) = %v, %v", aware, guarded)
	}

	g.write(opSite+"/index.html", "<h1>v2</h1>")
	g.evs.take()
	g.batch()
	if g.deploys() != 1 {
		t.Fatalf("a batch on dev's branch deployed %d times", g.deploys())
	}
	good := g.p.guard.good(opSite, "dev")
	if good == "" || g.st.files[good]["index.html"] != "<h1>v2</h1>" {
		t.Fatalf("the batch's capture %q holds %v", good, g.st.files[good])
	}

	// The work tree switches to staging's branch, and a save follows.
	g.checkout("release")
	g.write(opSite+"/index.html", "<h1>release</h1>")
	g.batch()
	if g.deploys() != 1 {
		t.Fatal("a batch on another branch deployed")
	}
	rec := g.rec(opSite)
	if rec.LiveReload != "" || rec.LastLiveReload != "dev" || rec.Deployments["dev"].Checkpoint == nil || *rec.Deployments["dev"].Checkpoint != good {
		t.Fatalf("after the switch: live reload %q, last %q, dev %+v; want paused, dev pinned to %s",
			rec.LiveReload, rec.LastLiveReload, rec.Deployments["dev"], good)
	}
	evs := g.branchEvents()
	want := branchEvent{Op: "branch", Deployment: "dev", Assigned: "feature", WorkTree: "release", Related: "staging", Paused: true}
	if len(evs) != 1 || evs[0] != want {
		t.Fatalf("op branch events %+v, want [%+v]", evs, want)
	}
	if log := g.st.logged(opSite); len(log) == 0 || log[len(log)-1].How != "pause" || log[len(log)-1].By != "xbind" {
		t.Errorf("the deploy log's last entry %+v, want xbind's pause", log[len(log)-1])
	}

	// Switching back while paused announces it (the offer to resume).
	g.checkout("feature")
	g.p.WorkTreeMoved(opSite)
	g.idle()
	evs = g.branchEvents()
	if len(evs) != 1 || evs[0].Deployment != "dev" || evs[0].WorkTree != "feature" || evs[0].Paused {
		t.Errorf("switching back: %+v", evs)
	}
}

// covers D131 — the override lapses when the work tree's branch changes
// again: a save on the override's branch deploys, one back on the assigned
// branch deploys and clears it, one on a third pauses.
func TestBranchOverrideLapses(t *testing.T) {
	g := &guardFx{opsFx: newOpsFx(t, false)}
	g.checkout("feature")
	g.addDep(AddRequest{Deployment: "dev", Attach: true, Branch: "feature"})
	added := g.rec(opSite)
	g.checkout("hotfix")
	g.write(opSite+"/index.html", "<h1>hotfix</h1>")
	g.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
	// Pausing on another branch pins dev to the code it runs, never the
	// other branch's work tree.
	if a := g.st.logged(opSite); len(a) == 0 || g.st.files[*g.rec(opSite).Deployments["dev"].Checkpoint]["index.html"] != "<h1>v1</h1>" {
		t.Fatalf("pausing off dev's branch pinned it to %v (added %+v)", g.st.files[*g.rec(opSite).Deployments["dev"].Checkpoint], added.Deployments["dev"])
	}
	g.must(ownerP, OpResume, &ResumeRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmOtherBranch})
	g.batch()
	if g.deploys() != 1 || g.rec(opSite).BranchOverride("dev") != "hotfix" {
		t.Fatalf("a save on the override's branch: %d deploys, override %q", g.deploys(), g.rec(opSite).BranchOverride("dev"))
	}
	g.checkout("feature")
	g.batch()
	if g.deploys() != 2 || g.rec(opSite).Deployments["dev"].BranchOverride != "" || g.rec(opSite).LiveReload != "dev" {
		t.Fatalf("back on feature: %d deploys, dev %+v", g.deploys(), g.rec(opSite).Deployments["dev"])
	}
	g.checkout("hotfix")
	g.batch()
	if g.deploys() != 2 || g.rec(opSite).LiveReload != "" {
		t.Fatalf("hotfix again after the override lapsed: %d deploys, live reload %q", g.deploys(), g.rec(opSite).LiveReload)
	}
}

// covers D131 — the second check: a checkout that races the capture (HEAD
// moves between its two reads) refuses the swap: a guarded save deploys
// nothing and pauses, and an explicit op answers 409 with nothing shipped.
func TestBranchTrailerCheckRefusesRacedCheckout(t *testing.T) {
	g := &guardFx{opsFx: newOpsFx(t, false)}
	g.checkout("feature")
	g.addDep(AddRequest{Deployment: "dev", Attach: true, Branch: "feature"})
	g.batch() // a good checkpoint first
	if g.deploys() != 1 {
		t.Fatal("the first batch didn't deploy")
	}
	g.st.mu.Lock()
	g.st.race = func(string) { g.checkout("main") }
	g.st.mu.Unlock()
	g.batch()
	if g.deploys() != 1 || g.rec(opSite).LiveReload != "" {
		t.Fatalf("a raced batch: %d deploys, live reload %q", g.deploys(), g.rec(opSite).LiveReload)
	}
	if evs := g.branchEvents(); len(evs) == 0 || evs[len(evs)-1].WorkTree != "main" || !evs[len(evs)-1].Paused {
		t.Errorf("a raced batch's events: %+v", evs)
	}
}

// covers D131 — an explicit op's capture that a checkout raced is refused.
func TestBranchExplicitOpRace(t *testing.T) {
	f := newOpsFx(t, false)
	f.checkout("feature")
	f.addDep(AddRequest{Deployment: "dev", Branch: "feature", From: FromPrimary})
	f.st.mu.Lock()
	f.st.race = func(string) { f.checkout("main") }
	f.st.mu.Unlock()
	before := *f.rec(opSite).Deployments["dev"].Checkpoint
	_, err := f.do(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"})
	wantConflict(t, "a raced attach", err, "a checkout raced this request", "nothing shipped")
	rec := f.rec(opSite)
	if rec.LiveReload != util.MainDeployment || *rec.Deployments["dev"].Checkpoint != before {
		t.Errorf("a raced attach changed the record: live reload %q, dev %+v", rec.LiveReload, rec.Deployments["dev"])
	}
	f.checkout("feature")
	_, err = f.do(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev"})
	wantConflict(t, "a raced deploy", err, "a checkout raced")
}

// covers D131 — newBranch creates the branch in the tile with a confined git
// switch (TestNoDirectExec keeps it confined) and assigns it; an existing
// name is refused, by the dry run too, and nothing else is switched to.
func TestBranchNewBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	f := newOpsFx(t, false)
	dir := filepath.Join(f.root, opSite)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_, err := f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", NewBranch: "main", DryRun: true})
	wantConflict(t, "a dry run onto an existing branch", err, "already has a branch main")
	if _, err := f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", NewBranch: "feat/x", DryRun: true}); err != nil {
		t.Fatalf("a dry run of a new branch: %v", err)
	}
	if checkpoint.BranchExists(dir, "feat/x") || checkpoint.WorkTreeBranch(dir) != "main" {
		t.Fatal("a dry run created or switched a branch")
	}
	f.addDep(AddRequest{Deployment: "dev", Attach: true, NewBranch: "feat/x"})
	if b := checkpoint.WorkTreeBranch(dir); b != "feat/x" {
		t.Fatalf("after newBranch the work tree is on %q", b)
	}
	if rec := f.rec(opSite); rec.AssignedBranch("dev") != "feat/x" || rec.LiveReload != "dev" {
		t.Fatalf("newBranch: dev %+v, live reload %q", rec.Deployments["dev"], rec.LiveReload)
	}
	_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "qa", NewBranch: "main", From: FromPrimary})
	wantConflict(t, "newBranch onto an existing branch", err, "already has a branch main")
	if b := checkpoint.WorkTreeBranch(dir); b != "feat/x" {
		t.Errorf("a refused newBranch switched to %q", b)
	}
	o := &op{tile: opSite}
	o.c, _ = f.reg.Component(opSite)
	if err := f.p.createBranch(context.Background(), o, "main"); err == nil || !strings.Contains(err.Error(), "already has a branch main") {
		t.Errorf("git switch onto main: %v", err)
	}
	if _, err := f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "qa", Branch: "a", NewBranch: "b"}); err == nil {
		t.Error("add took branch and newBranch together")
	}
}

// covers D131 12-compat — an xbind that predates assigned branches keeps a
// deployment's branch and branchOverride verbatim through its codec's
// unknown fields; a primary that kept a branch (an older xbind reassigned
// it) reads as assigned none and holds nothing.
func TestBranchRecordOlderXbind(t *testing.T) {
	in := []byte(`{"checkpoint":null,"branch":"feature","branchOverride":"main","created":"2026-09-28T00:00:00Z"}`)
	var d DeploymentRecord
	if err := json.Unmarshal(in, &d); err != nil || d.Branch != "feature" || d.BranchOverride != "main" {
		t.Fatalf("decode: %v %+v", err, d)
	}
	// The older codec: the same, less the two keys it doesn't know.
	var older []field
	for _, k := range d.fields() {
		if k.key != "branch" && k.key != "branchOverride" {
			older = append(older, k)
		}
	}
	var od DeploymentRecord
	extra, err := decodeObject(in, fieldsOf(&od, older))
	if err != nil {
		t.Fatal(err)
	}
	if string(extra["branch"]) != `"feature"` || string(extra["branchOverride"]) != `"main"` {
		t.Fatalf("the older codec's unknown fields: %v", extra)
	}
	out, err := encodeObject(fieldsOf(&od, older), extra)
	if err != nil {
		t.Fatal(err)
	}
	var back DeploymentRecord
	if err := json.Unmarshal(out, &back); err != nil || back.Branch != "feature" || back.BranchOverride != "main" {
		t.Errorf("an older xbind's rewrite lost the branch: %s (%v)", out, err)
	}

	rec := []byte(`{"schema":1,"tile":"apps/x","owner":"","created":"2026-09-28T00:00:00Z","seq":3,"liveReload":"dev","lastLiveReload":"dev","primary":"dev",` +
		`"protectedPrimary":false,"nextDeploy":2,"deployments":{"main":{"checkpoint":"` + strings.Repeat("a", 40) + `","branch":"x"},"dev":{"checkpoint":null,"branch":"feature"}}}`)
	r, err := ParseRecord(rec, "apps/x")
	if err != nil {
		t.Fatalf("a record whose primary and main keep a branch doesn't load: %v", err)
	}
	if r.AssignedBranch("dev") != "" || r.AssignedBranch("main") != "" || r.branchAware() {
		t.Errorf("main or the primary reads as assigned a branch")
	}
	enc, _ := r.encode()
	if !bytes.Contains(enc, []byte(`"branch": "feature"`)) {
		t.Errorf("the rewrite dropped the primary's stored branch:\n%s", enc)
	}
}

// fieldsOf is known's keys decoding into d's fields.
func fieldsOf(d *DeploymentRecord, known []field) []field {
	mine := map[string]field{}
	for _, f := range d.fields() {
		mine[f.key] = f
	}
	out := make([]field, 0, len(known))
	for _, k := range known {
		out = append(out, mine[k.key])
	}
	return out
}

// covers D131 — the work tree's branch is read beneath the tile with no git:
// a branch, a detached HEAD, a gitfile, a reftable placeholder.
func TestWorkTreeBranchRead(t *testing.T) {
	dir := t.TempDir()
	if b := checkpoint.WorkTreeBranch(dir); b != "" {
		t.Errorf("no repository: %q", b)
	}
	must := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for head, want := range map[string]string{
		"ref: refs/heads/feat/x\n":        "feat/x",
		"ref: refs/heads/.invalid\n":      "",
		"ref: refs/tags/v1\n":             "",
		strings.Repeat("0a", 20) + "\n":   "",
		"ref: refs/heads/-oops\n":         "",
		"ref: refs/heads/a..b\n":          "",
		"ref: refs/heads/main\n":          "main",
		"ref: refs/heads/release-1.2+x\n": "release-1.2+x",
	} {
		must(".git/HEAD", head)
		if b := checkpoint.WorkTreeBranch(dir); b != want {
			t.Errorf("HEAD %q: %q, want %q", strings.TrimSpace(head), b, want)
		}
	}
}

// covers D131 — op branch reaches only through the deployments event type;
// its data carries no field the reader forms take.
func TestBranchEventShape(t *testing.T) {
	b, _ := json.Marshal(events.Event{Type: "deployments", Component: opSite, Data: branchEvent{Op: "branch", Deployment: "dev",
		Assigned: "feature", WorkTree: "main", Related: "", Paused: true}})
	want := `{"type":"deployments","component":"apps/site","data":{"op":"branch","deployment":"dev","assigned":"feature","workTree":"main","related":"","paused":true}}`
	if !strings.Contains(string(b), `"data":{"op":"branch","deployment":"dev","assigned":"feature","workTree":"main","related":"","paused":true}`) {
		t.Errorf("event %s, want %s", b, want)
	}
}
