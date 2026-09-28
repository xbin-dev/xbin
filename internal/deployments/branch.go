package deployments

// branch.go — branch-assigned deployments (D131): a deployment beyond main
// and the primary may name the work tree's branch it requires. It is a
// requirement and a label, not a feed: the deployment still runs the work
// tree's code (while live reload follows it) or its checkpoints, but only a
// work tree on its branch may feed it.
//
//   - The branch op sets or clears a deployment's branch (terminal level, as
//     add, which takes branch and newBranch).
//   - The explicit ops that feed a deployment from the work tree — attach,
//     resume, reload now, a deploy that captures, an add from the work tree
//     — check the work tree's branch at the request (409 naming both
//     branches) and again at their capture (the Xbin-Work-Tree-Branch
//     trailer: a checkout that raced the request refuses it).
//     confirm:"other-branch" takes the work tree's branch this time; for an
//     op that makes the deployment the live reload target it is kept as its
//     branchOverride until live reload moves or the branch changes again.
//   - Saves: the watcher loop asks Branches, one in-memory lookup, and hands
//     a batch whose live reload target has a branch to the tile's guard
//     worker, which reads HEAD, captures the work tree in the background
//     (the capture's trailer is the second check) and only then deploys the
//     batch. A work tree on another branch — or a checkout that raced the
//     capture — deploys nothing: live reload pauses, the target pinned to
//     the checkpoint it runs (the last batch deployed on its branch), and a
//     deployments event op branch names the deployment, its branch, the work
//     tree's and the deployment assigned that one (related), which clients
//     offer to follow. Tiles without an assigned branch keep the save path
//     exactly as it was (D119d).
//   - add's newBranch creates the branch in the tile: a confined git switch,
//     never onto an existing branch; the one place xbind checks out a
//     branch in a tile.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	register(OpBranch, Handler[BranchRequest]{Subject: branchSubject, Run: runBranch})
}

// ConfirmOtherBranch is the confirm token of an explicit op that feeds a
// deployment from a work tree on another branch than its assigned one.
const ConfirmOtherBranch = "other-branch"

// BranchRequest is POST /deployments/branch's body: a deployment's assigned
// branch, or null to clear it. Branch is required: absent is a 400, so a
// body that forgot it never clears one.
type BranchRequest struct {
	Tile       string     `json:"tile"`
	Deployment string     `json:"deployment,omitempty"`
	Branch     NullString `json:"branch"`
	Seq        *int64     `json:"seq,omitempty"`
	DryRun     bool       `json:"dryRun,omitempty"`
}

// NullString is a string key that may be null: Set says it was sent, Value
// is nil for null.
type NullString struct {
	Set   bool
	Value *string
}

// UnmarshalJSON records that the key was present, and its string or null.
func (n *NullString) UnmarshalJSON(b []byte) error {
	n.Set, n.Value = true, nil
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// MarshalJSON writes the string, or null.
func (n NullString) MarshalJSON() ([]byte, error) { return json.Marshal(n.Value) }

func branchSubject(p *Plane, r *BranchRequest) (Op, Subject, error) {
	op, s, err := p.namedSubject(OpBranch, r.Tile, r.Deployment)
	switch {
	case err != nil:
		return "", Subject{}, err
	case !r.Branch.Set:
		return "", Subject{}, badRequest("bad request body: branch is required (a branch name, or null to clear it)")
	case r.Branch.Value == nil:
		return OpBranchClear, s, nil
	case !checkpoint.BranchNameOK(*r.Branch.Value):
		return "", Subject{}, badRequest(badBranchMsg(*r.Branch.Value))
	}
	return op, s, nil
}

func badBranchMsg(b string) string {
	return fmt.Sprintf("%q isn't a branch name xbind takes: letters, digits and . _ + / -, not starting with - or ., no .., at most 200 bytes", b)
}

// runBranch sets or clears y's assigned branch (D131): never main's or the
// primary's. Its override goes with it. The dry run reports the work tree's
// branch beside the new one, so a client can say that live reload on y
// pauses at the next save while they differ.
func runBranch(ctx context.Context, p *Plane, g Grant, r *BranchRequest) (any, error) {
	y := g.Subject.Deployment
	o, err := p.start(g, r.DryRun, true, r.Seq, true, true, " — the primary takes no assigned branch")
	if err != nil {
		return nil, err
	}
	defer o.done()
	if y == util.MainDeployment {
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: "main takes no assigned branch: it is every tile's first deployment"}
	}
	b := ""
	if r.Branch.Value != nil {
		b = *r.Branch.Value
	}
	im := Impact{Data: "none", Affects: "nobody"}
	if b != "" {
		im.Branch = &BranchImpact{Deployment: y, Assigned: b, WorkTree: checkpoint.WorkTreeBranch(o.c.Dir)}
		im.PausesLiveReload = o.rec.LiveReload == y && im.Branch.WorkTree != b
	}
	ans, _, err := p.edit(ctx, o, r.Seq, r.DryRun, "branch", im, func(rec *Record) (bool, error) {
		d := rec.Deployments[y]
		if d == nil || y == rec.Primary {
			return false, moved(o.tile)
		}
		changed := d.Branch != b || d.BranchOverride != ""
		d.Branch, d.BranchOverride = b, ""
		return changed, nil
	})
	return ans, err
}

// BranchImpact is Impact.branch: the assigned branch of the deployment an
// operation feeds from the work tree, and the work tree's ("" when it isn't
// on a branch); Other when confirm:"other-branch" takes the work tree's
// this time.
type BranchImpact struct {
	Deployment string `json:"deployment"`
	Assigned   string `json:"assigned"`
	WorkTree   string `json:"workTree"`
	Other      bool   `json:"other,omitempty"`
}

// ---- the explicit ops' check ----

// branchCheck is an explicit op's judgement of the work tree's branch
// against the branch of the deployment it feeds from the work tree.
type branchCheck struct {
	dep, assigned, workTree string
	other                   bool // confirm:"other-branch" took the work tree's branch this time
}

// allowed is the branch the op's capture must have been taken on; "" when
// the deployment has none (nothing to check).
func (b branchCheck) allowed() string {
	if b.other {
		return b.workTree
	}
	return b.assigned
}

// impact is the dry run's Impact.branch: nil for a deployment without one.
func (b branchCheck) impact() *BranchImpact {
	if b.assigned == "" {
		return nil
	}
	return &BranchImpact{Deployment: b.dep, Assigned: b.assigned, WorkTree: b.workTree, Other: b.other}
}

// override is the branch the op keeps as dep's branchOverride once it is
// the live reload target: the work tree's, when the op took it this time.
func (b branchCheck) override() string {
	if b.other && b.workTree != b.assigned {
		return b.workTree
	}
	return ""
}

// feedCheck is checkBranch for an op that feeds dep, its assigned branch
// read from o's record, once confirm is known to be other-branch or none
// (the op takes no other token).
func (p *Plane) feedCheck(o *op, dep, confirm string, follow bool) (branchCheck, error) {
	if confirm != "" && confirm != ConfirmOtherBranch {
		return branchCheck{}, badRequest(fmt.Sprintf("bad request body: confirm takes %q here", ConfirmOtherBranch))
	}
	return p.checkBranch(o, dep, o.rec.AssignedBranch(dep), confirm, follow)
}

// after is captured for a real run; a dry run's capture isn't checked.
func (b branchCheck) after(res checkpoint.Result, dry bool) error {
	if dry {
		return nil
	}
	return b.captured(res)
}

// follows sets the branch o took this time as dep's override, for the
// commit that makes dep the live reload target (D131).
func (o *op) follows(dep string, b branchCheck) {
	if ov := b.override(); ov != "" {
		o.override, o.overrideDep = ov, dep
	}
}

// checkBranch judges an explicit op that feeds dep, assigned branch
// assigned ("" for none), from o's work tree: the work tree must be on that
// branch, or the request must confirm another this time. follow says the op
// makes dep the live reload target, which a work tree on no branch can't
// feed even when confirmed: every save would pause it.
func (p *Plane) checkBranch(o *op, dep, assigned, confirm string, follow bool) (branchCheck, error) {
	if assigned == "" {
		return branchCheck{}, nil
	}
	b := branchCheck{dep: dep, assigned: assigned, workTree: checkpoint.WorkTreeBranch(o.c.Dir)}
	switch {
	case b.workTree == assigned:
		return b, nil
	case b.workTree != "" && b.workTree == o.rec.BranchOverride(dep): // the other branch it takes this time
		b.other = true
		return b, nil
	case hasToken(confirm, ConfirmOtherBranch) && (b.workTree != "" || !follow):
		b.other = true
		return b, nil
	}
	return b, branchMismatchErr(dep, assigned, b.workTree, follow)
}

// branchMismatchErr is the 409 of an explicit op on a work tree on another
// branch (D131), naming both branches.
func branchMismatchErr(dep, assigned, workTree string, follow bool) error {
	if workTree == "" {
		msg := fmt.Sprintf("%s is assigned branch %s, and the work tree isn't on a branch (a detached HEAD, or no repository xbind can read): check out %s", dep, assigned, assigned)
		if !follow {
			msg += fmt.Sprintf(", or send confirm:%q to use it this time", ConfirmOtherBranch)
		}
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: msg}
	}
	return &Error{Status: http.StatusConflict, Kind: KindState,
		Msg: fmt.Sprintf("%s is assigned branch %s, and the work tree is on %s: check out %s, or send confirm:%q to use %s this time",
			dep, assigned, workTree, assigned, ConfirmOtherBranch, workTree)}
}

// captured is the second check, at capture time (the trailer): the capture
// must have been taken on the branch the request was judged on. A checkout
// that raced the request refuses it before anything is committed.
func (b branchCheck) captured(res checkpoint.Result) error {
	if b.assigned == "" || res.Branch == b.allowed() {
		return nil
	}
	now := res.Branch
	if now == "" {
		now = "no one branch"
	}
	return &Error{Status: http.StatusConflict, Kind: KindState,
		Msg: fmt.Sprintf("the work tree's branch moved while xbind captured it for %s (%s, now %s): a checkout raced this request, and nothing shipped; check the branch and retry",
			b.dep, b.allowed(), now)}
}

// hasToken reports whether a confirm carries want: one token, or on add
// several joined with commas (add's seed and other-branch together).
func hasToken(confirm, want string) bool {
	for _, t := range strings.Split(confirm, ",") {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

// settleOverrides is every commit's last step: when live reload moved, each
// branchOverride lapses (D131); the op that took another branch this time
// keeps it on the deployment it made the live reload target.
func settleOverrides(r *Record, before string, o *op) {
	if r.LiveReload != before {
		for _, d := range r.Deployments {
			if d != nil {
				d.BranchOverride = ""
			}
		}
	}
	if o != nil && o.overrideDep != "" && o.overrideDep == r.LiveReload {
		if d := r.Deployments[o.overrideDep]; d != nil {
			d.BranchOverride = o.override
		}
	}
}

// ---- creating a branch (add's newBranch) ----

// createBranch creates branch b in o's tile and checks it out: a confined
// git switch that never switches to an existing branch (git refuses the
// name, 409), with the name attached to its option and --end-of-options
// after it, so no name is ever read as an option. It changes no file of the
// work tree — HEAD and one ref, under .git, which the watcher doesn't watch
// — so nothing reloads. xbind's one checkout in a tile (D131).
func (p *Plane) createBranch(ctx context.Context, o *op, b string) error {
	if checkpoint.BranchExists(o.c.Dir, b) {
		return branchExists(o.tile, b)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := confine.Git(ctx, o.c.Dir, nil, "switch", "--create="+b, "--end-of-options"); err != nil {
		msg := strings.TrimSpace(err.Error())
		if strings.Contains(msg, "already exists") {
			return branchExists(o.tile, b)
		}
		return &Error{Status: http.StatusConflict, Kind: KindState,
			Msg: fmt.Sprintf("%s: creating branch %s failed: %s", o.tile, b, firstLine(msg))}
	}
	if got := checkpoint.WorkTreeBranch(o.c.Dir); got != b {
		return &Error{Status: http.StatusConflict, Kind: KindState,
			Msg: fmt.Sprintf("%s: after creating branch %s the work tree is on %q", o.tile, b, got)}
	}
	return nil
}

func branchExists(tile, b string) error {
	return &Error{Status: http.StatusConflict, Kind: KindState,
		Msg: fmt.Sprintf("%s already has a branch %s: newBranch only creates one (send branch to assign an existing one)", tile, b)}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- saves (the watcher's side) ----

// Branches answers the watcher loop (liveroute.go) for tile, whose live
// reload drives dep: aware when its record assigns any deployment a branch
// (the batch notes the work tree's branch, for the follow offers), guarded
// when dep has one (the batch goes through GuardBatch instead of deploying
// at once). An in-memory lookup: false, false for a tile without a record,
// so every other tile's save path stays as it was (D119d).
func (p *Plane) Branches(tile, dep string) (aware, guarded bool) {
	rec, _ := p.record(tile)
	if rec == nil {
		return false, false
	}
	return rec.branchAware(), rec.AssignedBranch(dep) != ""
}

// GuardBatch takes one batch for a live reload target with an assigned
// branch: deploy (its reload event and rebuild) runs only once the tile's
// guard worker found the work tree on that branch, at the batch and at its
// capture. Off the watcher loop; batches that arrive while one is checked
// merge into the next check.
func (p *Plane) GuardBatch(c *registry.Component, dep string, restart bool, deploy func(restart bool)) {
	p.guard.queue(p, c.Path, func(g *guardTile) {
		if g.batch != nil && g.batch.dep == dep {
			restart = restart || g.batch.restart
		}
		g.batch = &guardBatch{c: c, dep: dep, restart: restart, deploy: deploy}
	})
}

// NoteBranch tells the plane a batch touched tile, whose record assigns a
// branch but whose live reload target has none (or is paused): the worker
// reads the work tree's branch and announces a switch (op branch), which is
// what clients offer to follow.
func (p *Plane) NoteBranch(tile string) {
	p.guard.queue(p, tile, func(g *guardTile) { g.note = true })
}

// branchGuard is the plane's per-tile guard workers.
type branchGuard struct {
	mu    sync.Mutex
	tiles map[string]*guardTile
}

// guardTile is one tile's worker state.
type guardTile struct {
	busy  bool
	batch *guardBatch // the batch waiting
	note  bool        // a branch note waiting
	seen  *string     // the work tree's branch as last seen; nil until first read
	good  map[string]string
}

// guardBatch is a batch waiting for its check.
type guardBatch struct {
	c       *registry.Component
	dep     string
	restart bool
	deploy  func(restart bool)
}

// queue changes tile's waiting work and starts its worker when it is idle.
func (bg *branchGuard) queue(p *Plane, tile string, set func(*guardTile)) {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	if bg.tiles == nil {
		bg.tiles = map[string]*guardTile{}
	}
	g := bg.tiles[tile]
	if g == nil {
		g = &guardTile{good: map[string]string{}}
		bg.tiles[tile] = g
	}
	set(g)
	if !g.busy {
		g.busy = true
		go p.guardLoop(tile)
	}
}

// guardLoop is tile's worker: one batch or note at a time, until none waits.
func (p *Plane) guardLoop(tile string) {
	for {
		p.guard.mu.Lock()
		g := p.guard.tiles[tile]
		b, note := g.batch, g.note
		g.batch, g.note = nil, false
		if b == nil && !note {
			g.busy = false
			p.guard.mu.Unlock()
			return
		}
		p.guard.mu.Unlock()
		if b != nil {
			p.guardBatch(tile, b)
		} else {
			p.noteBranch(tile)
		}
	}
}

// good is the checkpoint of the last batch deployed on dep's branch: the
// code it runs, which a pause on a switch pins it to.
func (bg *branchGuard) good(tile, dep string) string {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	if g := bg.tiles[tile]; g != nil {
		return g.good[dep]
	}
	return ""
}

func (bg *branchGuard) setGood(tile, dep, tree string) {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	if g := bg.tiles[tile]; g != nil {
		g.good[dep] = tree
	}
}

// see records the work tree's branch and reports whether it changed since
// the last read (the first read changes nothing).
func (bg *branchGuard) see(tile, branch string) bool {
	bg.mu.Lock()
	defer bg.mu.Unlock()
	g := bg.tiles[tile]
	if g == nil {
		return false
	}
	changed := g.seen != nil && *g.seen != branch
	g.seen = &branch
	return changed
}

// guardBatch checks one batch of a guarded target (D131): the work tree's
// branch against the target's (or its override, which lapses when the
// branch changed again), a background capture whose trailer must agree, and
// only then the batch's deploy. Anything else deploys nothing and pauses
// live reload.
func (p *Plane) guardBatch(tile string, b *guardBatch) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rec, _ := p.record(tile)
	switch {
	case rec == nil || rec.LiveReload != b.dep || p.pausing.has(tile):
		return // live reload moved, paused or is being detached: the batch is the new state's
	case rec.AssignedBranch(b.dep) == "":
		b.deploy(b.restart) // the branch was cleared meanwhile
		p.noteBranch(tile)
		return
	}
	assigned := rec.AssignedBranch(b.dep)
	w := checkpoint.WorkTreeBranch(b.c.Dir)
	allowed := assigned
	if o := rec.BranchOverride(b.dep); o != "" && o == w {
		allowed = o
	} else if o != "" {
		p.lapseOverride(tile, b.dep, o) // the branch changed again
	}
	if w != allowed {
		p.branchSwitched(ctx, tile, b, assigned, w)
		return
	}
	res, err := p.store().Capture(ctx, checkpoint.CaptureRequest{Source: p.source(b.c), By: "xbind", Background: true})
	switch {
	case err == nil && res.Branch != allowed:
		p.branchSwitched(ctx, tile, b, assigned, checkpoint.WorkTreeBranch(b.c.Dir)) // a checkout raced the batch
		return
	case err == nil:
		p.guard.setGood(tile, b.dep, res.Hash)
	default:
		// Rate-limited, refused or failed: the batch still passed the branch
		// check, and the last good checkpoint stays what a switch pins to.
		slog.Debug("deployments: a guarded save's capture", "tile", tile, "deployment", b.dep, "err", err)
	}
	b.deploy(b.restart)
	if p.guard.see(tile, w) {
		p.publishBranch(tile, branchEvent{Op: "branch", Deployment: b.dep, Assigned: assigned, WorkTree: w,
			Related: rec.branchHolder(w, b.dep)})
	}
}

// branchSwitched is a guarded batch on a work tree off the target's branch:
// nothing of it deploys, live reload pauses with the target pinned to the
// code it runs, and op branch says so.
func (p *Plane) branchSwitched(ctx context.Context, tile string, b *guardBatch, assigned, w string) {
	paused := p.branchPause(ctx, tile, b.c, b.dep, assigned)
	p.guard.see(tile, w)
	rec := p.current(tile)
	related := ""
	if rec != nil {
		related = rec.branchHolder(w, b.dep)
	}
	p.publishBranch(tile, branchEvent{Op: "branch", Deployment: b.dep, Assigned: assigned, WorkTree: w,
		Related: related, Paused: paused})
}

// branchPause pauses live reload on dep, pinned to the checkpoint it runs
// (pinPoint): xbind's own act, logged as a pause by "xbind". false when it
// couldn't: live reload moved meanwhile, no checkpoint of dep's is known,
// or the pause failed — the batch is dropped either way, never deployed.
func (p *Plane) branchPause(ctx context.Context, tile string, c *registry.Component, dep, assigned string) bool {
	pin := p.pinPoint(ctx, tile, dep, assigned)
	if pin == "" {
		slog.Warn("deployments: the work tree left a deployment's branch, and no checkpoint of it is known: live reload stays attached, and saves reach nothing until the branch is back",
			"tile", tile, "deployment", dep, "branch", assigned)
		return false
	}
	rec := p.current(tile)
	if rec == nil {
		return false
	}
	g := Grant{P: auth.Principal{Owner: true, Via: "xbind"}, Op: OpPause, Subject: subjectFrom(tile, dep, rec), system: true}
	o, err := p.begin(g, false)
	if err != nil {
		return false
	}
	defer o.done()
	if o.rec.LiveReload != dep || p.full(tile, dep) {
		return false
	}
	o.by = "xbind"
	release := p.pausing.on(tile)
	defer release()
	if _, err := p.leaveWorkTree(ctx, o, nil, dep, "pause", func(context.Context) (string, error) { return pin, nil }); err != nil {
		warn("pausing live reload on a branch switch", tile, err)
		return false
	}
	return true
}

// pinPoint is the checkpoint dep runs while live reload follows it: the
// last batch deployed on its branch (this xbind's run), else the newest of
// its deploy log's checkpoints taken on its branch (the attach, resume or
// add that made it follow), else its newest.
func (p *Plane) pinPoint(ctx context.Context, tile, dep, assigned string) string {
	if t := p.guard.good(tile, dep); t != "" {
		return t
	}
	fallback := ""
	for _, a := range p.attempts(ctx, tile, dep) {
		p.q.mu.Lock()
		tree := a.Tree
		p.q.mu.Unlock()
		if tree == "" {
			continue
		}
		if cp, err := p.store().Get(ctx, tile, tree); err == nil && cp.WorkTreeBranch == assigned {
			return tree
		}
		if fallback == "" {
			fallback = tree
		}
	}
	return fallback
}

// offBranchPin is what pausing live reload pins its target dep to when the
// work tree isn't on the branch dep takes (its assigned one, or its
// override): the checkpoint it runs (pinPoint), never a capture of the other
// branch's work tree. "" for every other target: the pause captures.
func (p *Plane) offBranchPin(ctx context.Context, o *op, dep string) string {
	assigned := o.rec.AssignedBranch(dep)
	if assigned == "" {
		return ""
	}
	w := checkpoint.WorkTreeBranch(o.c.Dir)
	if w == assigned || w == o.rec.BranchOverride(dep) && w != "" {
		return ""
	}
	return p.pinPoint(ctx, o.tile, dep, assigned)
}

// lapseOverride clears dep's branchOverride when it still names o: the work
// tree's branch changed again (D131).
func (p *Plane) lapseOverride(tile, dep, o string) {
	rec, err := p.idx.commit(tile, -1, func(r *Record) error {
		d := r.Deployments[dep]
		if d == nil || d.BranchOverride != o {
			return errNoChange
		}
		d.BranchOverride = ""
		return nil
	})
	switch {
	case err == nil:
		p.publishRecord(tile, rec, "xbind", []string{"branch"})
	case !errors.Is(err, errNoChange):
		warn("clearing a branch override", tile, err)
	}
}

// noteBranch reads an aware tile's work tree branch and announces a switch
// since the last read: the deployment live reload follows (or last
// followed), its branch, the work tree's, and the deployment assigned that
// one.
func (p *Plane) noteBranch(tile string) {
	rec, _ := p.record(tile)
	c, ok := p.component(tile)
	if rec == nil || !ok || !rec.branchAware() {
		return
	}
	w := checkpoint.WorkTreeBranch(c.Dir)
	if !p.guard.see(tile, w) {
		return
	}
	dep := firstNonEmpty(rec.LiveReload, rec.LastLiveReload, rec.Primary)
	p.publishBranch(tile, branchEvent{Op: "branch", Deployment: dep, Assigned: rec.AssignedBranch(dep), WorkTree: w,
		Related: rec.branchHolder(w, dep)})
}

// branchEvent is the deployments event op branch (D131): the work tree's
// branch switched away from (or back to) what deployment requires. related
// is the deployment assigned the work tree's branch ("" for none); paused
// says this switch paused live reload on deployment.
type branchEvent struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
	Assigned   string `json:"assigned"`
	WorkTree   string `json:"workTree"`
	Related    string `json:"related"`
	Paused     bool   `json:"paused"`
}

// publishBranch announces op branch to the tile's write audience.
func (p *Plane) publishBranch(tile string, e branchEvent) {
	if p.Hub != nil {
		p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: e})
	}
}

// WorkTreeBranch answers State.workTree.branch for tile: the branch its
// work tree has checked out, "" when none is (D131). A small file read
// beneath the tile, no git run.
func (p *Plane) WorkTreeBranch(tile string) string {
	c, ok := p.component(tile)
	if !ok {
		return ""
	}
	return checkpoint.WorkTreeBranch(c.Dir)
}

// addBranch is add's branch (D131): the one it assigns, and the check of a
// work tree that feeds the new deployment (attach, or code from the work
// tree). newBranch creates it first, in the tile, so the work tree is on it
// (a dry run only checks that it doesn't exist yet); an add refused after
// that keeps the branch it created.
func (p *Plane) addBranch(ctx context.Context, o *op, r *AddRequest, y string) (string, branchCheck, error) {
	b := cmp.Or(r.Branch, r.NewBranch)
	if b == "" {
		return "", branchCheck{}, nil
	}
	fromWorkTree := r.From == "" || r.From == FromWorkTree
	if r.NewBranch != "" {
		if checkpoint.BranchExists(o.c.Dir, b) {
			return "", branchCheck{}, branchExists(o.tile, b)
		}
		if r.DryRun {
			if !fromWorkTree {
				return b, branchCheck{}, nil
			}
			return b, branchCheck{dep: y, assigned: b, workTree: b}, nil // as it will be, once created
		}
		if err := p.createBranch(ctx, o, b); err != nil {
			return "", branchCheck{}, err
		}
	}
	if !fromWorkTree {
		return b, branchCheck{}, nil
	}
	bc, err := p.checkBranch(o, y, b, r.Confirm, r.Attach)
	return b, bc, err
}

// addBranchImpact is add's Impact.branch: the branch the new deployment is
// assigned, and the work tree's.
func addBranchImpact(o *op, y, b string, bc branchCheck) *BranchImpact {
	switch {
	case b == "":
		return nil
	case bc.assigned != "":
		return bc.impact()
	}
	return &BranchImpact{Deployment: y, Assigned: b, WorkTree: checkpoint.WorkTreeBranch(o.c.Dir)}
}

// tokenOf is want when confirm carries it, "" otherwise.
func tokenOf(confirm, want string) string {
	if hasToken(confirm, want) {
		return want
	}
	return ""
}
