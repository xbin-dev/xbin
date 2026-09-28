package deployments

// ops.go — the M1 operations, onto main (05-model §5; 07-runtime §8.2–§8.7;
// 11-contract §1.4, §1.6): pausing live reload, resuming it, reloading now,
// deploying (a restart included) and rolling back. Each registers with the
// dispatcher (dispatch.go), which judges the caller before Run; Run takes
// the tile's operation lock, refuses what it must before changing anything
// (a held record, a stale seq, D119h's isolation, a checkpoint that can't
// start), captures at request time, and commits through the index with the
// grant rechecked against the record it changes (T9). A dry run is judged
// exactly as for real and changes nothing: on a tile without a record it
// captures nothing and creates no store (D119c).
//
// Where the record's pointer moves (07-runtime §8.2): a move off the work
// tree (pausing live reload, a deploy onto the live reload target) writes
// it at request time, once the checkpoint exists and is materialized; a
// pinned → pinned move writes it after the swap (commitSwap), so a failed
// deploy leaves the previous code, which every restart keeps running (D119e).

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	register(OpPause, Handler[PauseRequest]{Subject: pauseSubject, Run: runPause})
	register(OpResume, Handler[ResumeRequest]{Subject: resumeSubject, Run: runResume})
	register(OpReloadNow, Handler[ReloadNowRequest]{Subject: reloadNowSubject, Run: runReloadNow})
	register(OpDeploy, Handler[DeployRequest]{Subject: deploySubject, Run: runDeploy})
	register(OpRollback, Handler[RollbackRequest]{Subject: rollbackSubject, Run: runRollback})
}

// ---- subjects: what a request acts on, read before authority ----

// resolveTile resolves a tile ref (11-contract §0.3): the registered tile of
// that path, as today; only when there is none, and only for a tile a
// record governs, "<tile>+<name>" selects that tile's deployment name.
func (p *Plane) resolveTile(ref string) (tile, selected string, err error) {
	if ref == "" {
		return "", "", badRequest("bad request body: tile is required")
	}
	if _, ok := p.component(ref); ok {
		return ref, "", nil
	}
	if i := strings.LastIndexByte(ref, '+'); i > 0 {
		base, name := ref[:i], ref[i+1:]
		if _, ok := p.component(base); ok && util.DeploymentNameOK(name) && p.HasRecord(base) && p.HasDeployment(base, name) {
			return base, name, nil
		}
	}
	return "", "", &Error{Status: http.StatusNotFound, Msg: "no such tile: " + ref}
}

// named is the deployment a request names: its deployment field or its ref's
// qualifier (both, differing, is a 400); "" when it names none.
func named(field, selected string) (string, error) {
	switch {
	case field != "" && !util.DeploymentNameOK(field):
		return "", badRequest(badNameMsg)
	case field != "" && selected != "" && field != selected:
		return "", badRequest(fmt.Sprintf("the tile ref names deployment %q and the body %q: send one", selected, field))
	case field != "":
		return field, nil
	}
	return selected, nil
}

const badNameMsg = `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`

// subjectOf is what an act on deployment dep of tile is judged against: the
// record as the index holds it now. A held record counts as a record: the
// operation answers the hold after authority is judged.
func (p *Plane) subjectOf(tile, dep string) Subject {
	f := p.Lookup(tile)
	switch f.State {
	case RecordActive:
		return subjectFrom(tile, dep, f.Record)
	case RecordHeld:
		return Subject{Tile: tile, Deployment: dep, Record: true}
	}
	return Subject{Tile: tile, Deployment: dep}
}

// subjectFrom is the subject a record gives; the zero state's (seq 0) is no
// record.
func subjectFrom(tile, dep string, r *Record) Subject {
	return Subject{Tile: tile, Deployment: dep, Primary: r.Primary, Protected: r.ProtectedPrimary,
		Record: r.Seq > 0, Seq: uint64(max(r.Seq, 0))}
}

// current is tile's record as an operation reads it: the zero state's for a
// tile without one, nil while a record holds it.
func (p *Plane) current(tile string) *Record { return p.Lookup(tile).Record }

func pauseSubject(p *Plane, r *PauseRequest) (Op, Subject, error) {
	tile, _, err := p.resolveTile(r.Tile)
	if err != nil {
		return "", Subject{}, err
	}
	x := util.MainDeployment
	if rec := p.current(tile); rec != nil {
		x = firstNonEmpty(rec.LiveReload, rec.LastLiveReload, rec.Primary)
	}
	return OpPause, p.subjectOf(tile, x), nil
}

func resumeSubject(p *Plane, r *ResumeRequest) (Op, Subject, error) {
	tile, sel, err := p.resolveTile(r.Tile)
	if err != nil {
		return "", Subject{}, err
	}
	y, err := named(r.Deployment, sel)
	if err != nil {
		return "", Subject{}, err
	}
	if y == "" {
		y = util.MainDeployment
		if rec := p.current(tile); rec != nil {
			y = firstNonEmpty(rec.LastLiveReload, rec.Primary)
		}
	}
	return OpResume, p.subjectOf(tile, y), nil
}

func reloadNowSubject(p *Plane, r *ReloadNowRequest) (Op, Subject, error) {
	tile, _, err := p.resolveTile(r.Tile)
	if err != nil {
		return "", Subject{}, err
	}
	x := util.MainDeployment
	if rec := p.current(tile); rec != nil {
		x = firstNonEmpty(rec.LastLiveReload, rec.Primary)
	}
	return OpReloadNow, p.subjectOf(tile, x), nil
}

func deploySubject(p *Plane, r *DeployRequest) (Op, Subject, error) {
	tile, dep, err := p.targetOf(r.Tile, r.Deployment)
	if err != nil {
		return "", Subject{}, err
	}
	if r.Restart {
		return OpRestart, p.subjectOf(tile, dep), nil
	}
	return OpDeploy, p.subjectOf(tile, dep), nil
}

func rollbackSubject(p *Plane, r *RollbackRequest) (Op, Subject, error) {
	tile, dep, err := p.targetOf(r.Tile, r.Deployment)
	if err != nil {
		return "", Subject{}, err
	}
	return OpRollback, p.subjectOf(tile, dep), nil
}

// targetOf is a code move's tile and target deployment: the one named, else
// the primary.
func (p *Plane) targetOf(ref, field string) (string, string, error) {
	tile, sel, err := p.resolveTile(ref)
	if err != nil {
		return "", "", err
	}
	dep, err := named(field, sel)
	if err != nil {
		return "", "", err
	}
	if dep == "" {
		dep = p.Primary(tile)
	}
	return tile, dep, nil
}

// ---- the operation's context ----

// op is one operation in progress: the grant, the tile, its component and
// the record as the operation found it.
type op struct {
	g     Grant
	tile  string
	c     *registry.Component
	state RecordState
	rec   *Record // the record, or the zero state's for a tile without one
	by    string
	done  func()
	// override is the branch the op took this time (confirm:"other-branch")
	// for overrideDep, the deployment it makes the live reload target: its
	// commit keeps it as that deployment's branchOverride (D131).
	override, overrideDep string
}

// begin starts an operation the grant authorizes: a held record, or one
// that isn't the tile's, takes no change and answers 409 before anything
// runs. A dry run takes no lock.
func (p *Plane) begin(g Grant, dry bool) (*op, error) {
	if p.idx == nil {
		return nil, &Error{Status: http.StatusInternalServerError, Msg: "deployments: the plane isn't booted"}
	}
	o := &op{g: g, tile: g.Subject.Tile, by: actor(g.P), done: func() {}}
	c, ok := p.component(o.tile)
	if !ok {
		return nil, &Error{Status: http.StatusNotFound, Msg: "no such tile: " + o.tile}
	}
	o.c = c
	if !dry {
		o.done = p.locks.lock(o.tile)
	}
	f := p.Lookup(o.tile)
	switch f.State {
	case RecordHeld, RecordInert:
		o.done()
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: f.Err.Error()}
	}
	o.state, o.rec = f.State, f.Record
	return o, nil
}

// checkSeq refuses a request that acted on another seq than the record's.
func (o *op) checkSeq(seq *int64) error {
	if seq != nil && *seq != o.rec.Seq {
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: (&StaleSeqError{Tile: o.tile, Seq: o.rec.Seq}).Error()}
	}
	return nil
}

// actor is who acts, as records, logs and events name them: user:<id>, or
// owner (the root token, --no-auth, a credential no person drives).
func actor(pr auth.Principal) string {
	if pr.UserID != "" {
		return "user:" + pr.UserID
	}
	return "owner"
}

// newAttempt is o's attempt of how on dep, putting tree there.
func (o *op) newAttempt(how, dep, tree string) *attempt {
	a := &attempt{Deployment: dep, How: how, Tree: tree, Feed: checkpoint.FeedWorkTree,
		By: o.by, Via: o.g.P.Via, g: o.g}
	if d := o.rec.Deployments[dep]; d != nil && d.Checkpoint != nil {
		a.Previous = *d.Checkpoint
	}
	return a
}

// needsIsolation is D119h: without isolation nothing can show a checkpoint at
// a backend's canonical path, so no backend is pinned (static tiles are).
func (p *Plane) needsIsolation(c *registry.Component) error {
	if c.HasBackend() && !p.isIsolated() {
		return &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: isolationMsg}
	}
	return nil
}

const isolationMsg = "pinning a backend to a checkpoint needs isolation (--isolate)"

// source is the work tree a capture of o's tile reads.
func (p *Plane) source(c *registry.Component) checkpoint.Source {
	return checkpoint.Source{Tile: c.Path, WorkTree: c.Dir, Nested: p.nested(c.Path)}
}

// capture checkpoints o's work tree. Only a committed operation creates the
// store (a tile's first opt-in, or one whose store went missing); a dry run
// never does.
func (p *Plane) capture(ctx context.Context, o *op, dry bool) (checkpoint.Result, error) {
	return p.store().Capture(ctx, checkpoint.CaptureRequest{Source: p.source(o.c), By: o.by, Create: !dry})
}

// prepareCode materializes tree for dep and, for the primary, reads what it
// declares: a checkpoint that can't start (its xbin.json doesn't parse) is
// refused before anything is committed or queued.
func (p *Plane) prepareCode(o *op, dep, tree string) error {
	if dep != o.rec.Primary {
		_, err := p.store().Materialize(o.tile, tree)
		return err
	}
	pc, err := p.readCode(o.tile, tree)
	if err != nil {
		return err
	}
	if pc.ManifestErr != "" {
		return &Error{Status: http.StatusConflict, Kind: KindState,
			Msg: fmt.Sprintf("%s: checkpoint %s can't start: %s", o.tile, shortTree(tree), pc.ManifestErr)}
	}
	p.prep.put(o.tile, preparation{tree: tree, pc: pc})
	return nil
}

// commit changes o's record: the grant is judged again against the record
// the change applies to, inside the index's compare-and-set, so authority
// and the record can't move between the check and the write (T9).
func (p *Plane) commit(o *op, seq *int64, change func(*Record) error) (*Record, error) {
	expect := int64(-1)
	if seq != nil {
		expect = *seq
	}
	return p.idx.commit(o.tile, expect, func(r *Record) error {
		if err := p.Recheck(o.g, subjectFrom(o.tile, o.g.Subject.Deployment, r)); err != nil {
			return err
		}
		before := r.LiveReload
		if err := change(r); err != nil {
			return err
		}
		settleOverrides(r, before, o)
		return nil
	})
}

// catchUp rebuilds dep from what its record says after an operation that
// detached live reload failed before its commit: the saves ignored
// meanwhile reach it (07-runtime §8.4).
func (p *Plane) catchUp(o *op, dep string) {
	if p.Run != nil && o.c.HasBackend() {
		p.Run.ChangedDeployment(o.c, dep)
	}
}

// primaryCodeMoved reacts to a commit that moved the primary's code: the
// registry recomposes the tile from it (D119e), provisioning and the ingress
// follow (07-runtime §5.1).
func (p *Plane) primaryCodeMoved() {
	if p.Reg != nil {
		_ = p.Reg.Rescan()
	}
	if p.Provision != nil {
		p.Provision()
	}
	if p.ReconcileIngress != nil {
		p.ReconcileIngress()
	}
}

// syncView refreshes tile's view repository after its pinned set moved.
func (p *Plane) syncView(ctx context.Context, tile string, rec *Record) {
	if err := p.store().SyncView(ctx, tile, pinnedSet(rec), rec.Primary); err != nil && !isNotBuilt(err) {
		warn("refreshing the view repository", tile, err)
	}
}

// ---- pause live reload ----

func runPause(ctx context.Context, p *Plane, g Grant, r *PauseRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	x := o.rec.LiveReload
	if x == "" { // already paused: nothing to do
		return p.answer(ctx, r.DryRun, nil, Impact{}, false)
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	// A target whose work tree left its branch keeps the code it runs: the
	// work tree may not feed it (D131).
	pin := p.offBranchPin(ctx, o, x)
	if r.DryRun {
		im := Impact{Data: "none", PausesLiveReload: true, Affects: "nobody"}
		if pin != "" {
			im.Code = &CodeImpact{Deployment: x, From: "work-tree", To: p.shortOf(ctx, o.tile, pin)}
			return p.answer(ctx, true, nil, im, false)
		}
		if o.state == RecordNone {
			est, err := p.store().Estimate(ctx, p.source(o.c))
			if err != nil {
				return nil, opError(o.tile, err)
			}
			if err := est.Check(o.tile, p.store().Caps()); err != nil {
				return nil, opError(o.tile, err)
			}
			return p.answer(ctx, true, nil, im, false)
		}
		res, err := p.capture(ctx, o, true)
		if err != nil {
			return nil, opError(o.tile, err)
		}
		im.Code = &CodeImpact{Deployment: x, From: "work-tree", To: res.ID, WorkTreeAt: p.stamp()}
		return p.answer(ctx, true, nil, im, false)
	}
	if p.full(o.tile, x) {
		return nil, queueFull(x)
	}
	// Saves drive nothing from here to the commit: the checkpoint is what ships.
	release := p.pausing.on(o.tile)
	defer release()
	a, err := p.leaveWorkTree(ctx, o, r.Seq, x, "pause", func(ctx context.Context) (string, error) {
		if pin != "" {
			return pin, nil
		}
		res, err := p.capture(ctx, o, false)
		return res.Hash, err
	})
	if err != nil {
		p.catchUp(o, x)
		return nil, err
	}
	return p.answer(ctx, false, a, Impact{}, false)
}

// leaveWorkTree moves dep off the work tree (07-runtime §8.2's second row):
// the code is captured, materialized and prepared, then the record's pointer
// is written at request time, atomically with live reload pausing, and the
// deploy that swaps dep onto it is queued (a static tile has no process:
// its commit is the whole deploy). code captures the checkpoint.
func (p *Plane) leaveWorkTree(ctx context.Context, o *op, seq *int64, dep, how string, code func(context.Context) (string, error)) (*attempt, error) {
	tree, err := code(ctx)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	if err := p.prepareCode(o, dep, tree); err != nil {
		return nil, opError(o.tile, err)
	}
	a := o.newAttempt(how, dep, tree)
	a.Pointer = pointerRequest
	if err := p.accept(ctx, o.tile, o.rec, a); err != nil {
		return nil, opError(o.tile, err)
	}
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commit(o, seq, func(r *Record) error {
		d := r.Deployments[dep]
		switch {
		case d == nil:
			return util.NoDeployment(o.tile, dep)
		case d.Checkpoint != nil:
			return moved(o.tile)
		}
		if r.LiveReload == dep {
			r.LiveReload, r.LastLiveReload, r.LiveReloadSince = "", dep, since
		}
		if d.Created == "" { // main's entry, made by the tile's first opt-in
			d.Created, d.By = since.At, since.By
		}
		d.Checkpoint, d.State = &tree, ""
		r.NextDeploy = max(r.NextDeploy, a.ID+1)
		return nil
	})
	if err != nil {
		p.discard(a)
		return nil, opError(o.tile, err)
	}
	p.syncJournal(o.tile) // an opt-in's journal appears with its record
	what := []string{"liveReload", "deployments"}
	p.publishRecord(o.tile, rec, o.by, what)
	if dep == rec.Primary {
		p.primaryCodeMoved()
	}
	p.syncView(ctx, o.tile, rec)
	p.startAttempt(o, a)
	return a, nil
}

// ---- resume live reload ----

func runResume(ctx context.Context, p *Plane, g Grant, r *ResumeRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	y := g.Subject.Deployment
	d := o.rec.Deployments[y]
	switch {
	case d == nil:
		return nil, opError(o.tile, util.NoDeployment(o.tile, y))
	case o.rec.LiveReload == y: // already following the work tree
		return p.answer(ctx, r.DryRun, nil, Impact{}, false)
	case o.rec.LiveReload != "":
		return nil, notPaused(o.rec.LiveReload, "resuming live reload")
	}
	bc, err := p.feedCheck(o, y, r.Confirm, true)
	if err != nil {
		return nil, err
	}
	if r.DryRun {
		im := Impact{Data: "none", Affects: affects(o.rec, y), Reloads: []string{y},
			Code: &CodeImpact{Deployment: y, From: p.shortOf(ctx, o.tile, *d.Checkpoint), To: "work-tree"}, Branch: bc.impact()}
		p.measure(ctx, o.c, im.Code, o.by, *d.Checkpoint, "")
		return p.answer(ctx, true, nil, im, false)
	}
	// The entry names the work tree as it is now (11-contract §1.1): a
	// capture that can't be taken doesn't stop a return to the work tree.
	// One that was taken is the second branch check (D131).
	tree := ""
	if res, err := p.capture(ctx, o, false); err == nil {
		if err := bc.captured(res); err != nil {
			return nil, err
		}
		tree = res.Hash
	} else {
		warn("resume: the work tree's capture for the deploy log", o.tile, err)
	}
	o.follows(y, bc)
	a := o.newAttempt("resume", y, tree)
	a.FollowsWorkTree = true
	if err := p.accept(ctx, o.tile, o.rec, a); err != nil {
		return nil, opError(o.tile, err)
	}
	since := &Stamp{At: p.stamp(), By: o.by}
	var rec *Record
	if cur := p.current(o.tile); cur != nil && optOut(cur, y) {
		// Resuming onto main, with nothing else set, returns the tile to the
		// zero state (D119c, PO-15): the record goes, with its journal and view
		// repository; the store and its deploy log stay, inert. Authority is
		// judged against the record as it stands, which the removal
		// compares-and-sets on.
		expect := cur.Seq
		if r.Seq != nil {
			expect = *r.Seq
		}
		err = p.Recheck(o.g, subjectFrom(o.tile, y, cur))
		if err == nil {
			err = p.idx.remove(o.tile, expect)
		}
		rec = ZeroRecord(o.tile)
	} else {
		rec, err = p.commit(o, r.Seq, func(r *Record) error {
			d := r.Deployments[y]
			switch {
			case d == nil:
				return util.NoDeployment(o.tile, y)
			case r.LiveReload != "":
				return moved(o.tile)
			}
			r.LiveReload, r.LastLiveReload, r.LiveReloadSince = y, y, since
			d.Checkpoint, d.State = nil, ""
			r.NextDeploy = max(r.NextDeploy, a.ID+1)
			return nil
		})
	}
	if err != nil {
		p.discard(a)
		return nil, opError(o.tile, err)
	}
	p.cancelWaiting(o.tile, y, "cancelled: live reload drives "+y+" again")
	p.finish(a, resultOK, nil)
	if rec.Seq == 0 {
		if err := p.dropDerived(o.tile); err != nil {
			warn("opting out", o.tile, err)
		}
	} else {
		p.syncView(ctx, o.tile, rec)
	}
	p.publishRecord(o.tile, rec, o.by, []string{"liveReload", "deployments"})
	if y == rec.Primary {
		p.prep.drop(o.tile) // the primary follows the work tree again
		p.primaryCodeMoved()
	}
	if p.Run != nil {
		p.Run.ChangedDeployment(o.c, y)
	}
	p.publishReload(o.tile, y)
	return p.answer(ctx, false, a, Impact{}, false)
}

// optOut reports whether resuming onto y returns the tile to the zero
// state: y is main, main is the only deployment and the primary, and every
// setting is at its default.
func optOut(r *Record, y string) bool {
	d := r.Deployments[util.MainDeployment]
	return y == util.MainDeployment && len(r.Deployments) == 1 && r.Primary == util.MainDeployment &&
		!r.ProtectedPrimary && len(r.Edges) == 0 && d != nil && d.Deliveries == nil && !d.AlwaysOn && len(d.Limits) == 0
}

// ---- reload now ----

func runReloadNow(ctx context.Context, p *Plane, g Grant, r *ReloadNowRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	if o.rec.LiveReload != "" {
		return nil, notPaused(o.rec.LiveReload, "reloading now")
	}
	x := g.Subject.Deployment
	if o.rec.Deployments[x] == nil {
		return nil, opError(o.tile, util.NoDeployment(o.tile, x))
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	prefix, err := parseExpect(r.Expect)
	if err != nil {
		return nil, err
	}
	bc, err := p.feedCheck(o, x, r.Confirm, false)
	if err != nil {
		return nil, err
	}
	res, err := p.capture(ctx, o, r.DryRun)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	if prefix != "" && !strings.HasPrefix(res.Hash, prefix) {
		return nil, expectMismatch(r.Expect, res.ID)
	}
	if err := bc.after(res, r.DryRun); err != nil {
		return nil, err
	}
	return p.moveCode(ctx, o, r.Seq, r.DryRun, x, res.Hash, "reload-now", bc.impact())
}

// ---- deploy, restart ----

func runDeploy(ctx context.Context, p *Plane, g Grant, r *DeployRequest) (any, error) {
	switch {
	case r.Checkpoint != "" && r.Expect != "":
		return nil, badRequest("send checkpoint or expect, not both: checkpoint names the code, expect checks a fresh capture")
	case r.Restart && (r.Checkpoint != "" || r.Expect != ""):
		return nil, badRequest(fmt.Sprintf("restart runs %s's current code: send no checkpoint or expect with it", g.Subject.Deployment))
	case r.Confirm != "" && (r.Checkpoint != "" || r.Restart):
		return nil, badRequest(fmt.Sprintf("confirm:%q is for a deploy of the work tree: send no checkpoint or restart with it", ConfirmOtherBranch))
	}
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	dep := g.Subject.Deployment
	d := o.rec.Deployments[dep]
	if d == nil {
		return nil, opError(o.tile, util.NoDeployment(o.tile, dep))
	}
	if r.Restart {
		return p.restart(ctx, o, r.DryRun, dep)
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	var tree string
	var bc branchCheck
	if r.Checkpoint != "" {
		cp, err := p.store().Resolve(ctx, o.tile, r.Checkpoint)
		if err != nil {
			return nil, opError(o.tile, err)
		}
		tree = cp.Hash
	} else {
		prefix, err := parseExpect(r.Expect)
		if err != nil {
			return nil, err
		}
		if bc, err = p.feedCheck(o, dep, r.Confirm, false); err != nil {
			return nil, err
		}
		res, err := p.capture(ctx, o, r.DryRun)
		if err != nil {
			return nil, opError(o.tile, err)
		}
		if prefix != "" && !strings.HasPrefix(res.Hash, prefix) {
			return nil, expectMismatch(r.Expect, res.ID)
		}
		if err := bc.after(res, r.DryRun); err != nil {
			return nil, err
		}
		tree = res.Hash
	}
	return p.moveCode(ctx, o, r.Seq, r.DryRun, dep, tree, "deploy", bc.impact())
}

// ---- roll back ----

func runRollback(ctx context.Context, p *Plane, g Grant, r *RollbackRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	dep := g.Subject.Deployment
	d := o.rec.Deployments[dep]
	if d == nil {
		return nil, opError(o.tile, util.NoDeployment(o.tile, dep))
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	var tree string
	if r.Checkpoint != "" {
		cp, err := p.store().Resolve(ctx, o.tile, r.Checkpoint)
		if err != nil {
			return nil, opError(o.tile, err)
		}
		tree = cp.Hash
	} else if tree = p.rollbackTarget(ctx, o.tile, dep, d); tree == "" {
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: dep + " has no earlier checkpoint in its deploy log"}
	}
	return p.moveCode(ctx, o, r.Seq, r.DryRun, dep, tree, "rollback", nil)
}

// ---- moving code onto a deployment ----

// moveCode puts tree on dep: a deployment that follows the work tree leaves
// it (live reload pauses, the pointer is written now); a pinned one gets a
// queued deploy whose swap writes the pointer (commitSwap). A request whose
// code equals the lane's tail merges into it; code dep already runs, with
// nothing queued, answers unchanged — unless its last move failed, or (a
// deploy or roll back, not reload now: 11-contract §1.5, §1.6) its
// generation is down, which a move of the same checkpoint restarts.
func (p *Plane) moveCode(ctx context.Context, o *op, seq *int64, dry bool, dep, tree, how string, branch *BranchImpact) (any, error) {
	d := o.rec.Deployments[dep]
	tail := p.tail(o.tile, dep)
	unchanged := tail == nil && d.Checkpoint != nil && *d.Checkpoint == tree && d.State != "failed" &&
		(how == "reload-now" || !p.down(o.c, dep))
	if dry {
		from := "work-tree"
		if d.Checkpoint != nil {
			from = p.shortOf(ctx, o.tile, *d.Checkpoint)
		}
		im := Impact{Data: "none", PausesLiveReload: d.Checkpoint == nil, Affects: affects(o.rec, dep),
			Code: &CodeImpact{Deployment: dep, From: from, To: p.shortOf(ctx, o.tile, tree)}, Branch: branch}
		if d.Checkpoint != nil {
			p.measure(ctx, o.c, im.Code, o.by, *d.Checkpoint, tree)
		}
		if !unchanged {
			im.Reloads = []string{dep}
		}
		return p.answer(ctx, true, nil, im, false)
	}
	if unchanged {
		return p.answer(ctx, false, nil, Impact{}, true)
	}
	if d.Checkpoint == nil {
		if p.full(o.tile, dep) {
			return nil, queueFull(dep)
		}
		release := p.pausing.on(o.tile)
		defer release()
		a, err := p.leaveWorkTree(ctx, o, seq, dep, how, func(context.Context) (string, error) { return tree, nil })
		if err != nil {
			p.catchUp(o, dep)
			return nil, err
		}
		return p.answer(ctx, false, a, Impact{}, false)
	}
	if tail != nil {
		p.q.mu.Lock()
		same := tail.Tree == tree && !tail.finished()
		p.q.mu.Unlock()
		if same {
			return p.answer(ctx, false, tail, Impact{}, false)
		}
	}
	if p.full(o.tile, dep) {
		return nil, queueFull(dep)
	}
	if err := p.prepareCode(o, dep, tree); err != nil {
		return nil, opError(o.tile, err)
	}
	a := o.newAttempt(how, dep, tree)
	if *d.Checkpoint == tree {
		a.How = "restart" // a failed deployment's own checkpoint, from its kept artifact (07-runtime §8.7)
	}
	if err := p.accept(ctx, o.tile, o.rec, a); err != nil {
		return nil, opError(o.tile, err)
	}
	p.startAttempt(o, a)
	return p.answer(ctx, false, a, Impact{}, false)
}
