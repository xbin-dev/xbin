package deployments

// ops_code.go — the operations that change which deployments a tile has and
// move code between them (05-model §5, §10; 11-contract §1.4–§1.6;
// 07-runtime §8.6): attaching live reload to another deployment, adding a
// deployment, removing one, and promoting one deployment's code onto
// another. They register with the dispatcher beside ops.go's and share its
// operation context: the tile's lock, the seq check, the capture, the
// commit with the grant rechecked, the queue.
//
// Each refuses what it must before changing anything. A move off the work
// tree (the former live reload target an attach pins, a promotion onto the
// live reload target) writes the record's pointer at request time, as
// ops.go's pause does; a pinned → pinned promotion writes it after the swap
// (commitSwap). A new deployment's pointer is written with the deployment,
// at request time, and its code is prepared in the background without
// starting a process: it starts on its first request (07-runtime §8.6).
// A non-primary deployment's checkpoint is materialized on a turn of the
// runner's build limiter (D127q). A dry run is judged exactly as for real and
// changes nothing; on a tile without a record it captures nothing and
// creates no store (D119c). The record event's reader form goes out only when
// the reader view changed, so readers learn nothing of the non-primary
// deployments (11-contract §1.3, §3.4).

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	register(OpAttach, Handler[AttachRequest]{Subject: attachSubject, Run: runAttach})
	register(OpAdd, Handler[AddRequest]{Subject: addSubject, Run: runAddSeeded}) // runAdd, then a seed (ops_addseed.go)
	register(OpRemove, Handler[RemoveRequest]{Subject: removeSubject, Run: runRemove})
	register(OpPromote, Handler[PromoteRequest]{Subject: promoteSubject, Run: runPromote})
}

// ---- subjects ----

// namedSubject is the subject of op on the deployment a request must name:
// its deployment field or its ref's qualifier.
func (p *Plane) namedSubject(op Op, ref, field string) (Op, Subject, error) {
	tile, sel, err := p.resolveTile(ref)
	if err != nil {
		return "", Subject{}, err
	}
	dep, err := named(field, sel)
	switch {
	case err != nil:
		return "", Subject{}, err
	case dep == "":
		return "", Subject{}, badRequest("bad request body: deployment is required")
	}
	return op, p.subjectOf(tile, dep), nil
}

func attachSubject(p *Plane, r *AttachRequest) (Op, Subject, error) {
	return p.namedSubject(OpAttach, r.Tile, r.Deployment)
}

// addSubject: the deployment added. A qualified ref resolves only to a
// deployment that exists, so a new name comes in the deployment field.
func addSubject(p *Plane, r *AddRequest) (Op, Subject, error) {
	return p.namedSubject(OpAdd, r.Tile, r.Deployment)
}

func removeSubject(p *Plane, r *RemoveRequest) (Op, Subject, error) {
	return p.namedSubject(OpRemove, r.Tile, r.Deployment)
}

// promoteSubject: promotion is judged as a code move onto its target
// (11-contract §1.6, "as deploy, for the target B"), which a qualified ref
// may name instead of to.
func promoteSubject(p *Plane, r *PromoteRequest) (Op, Subject, error) {
	tile, sel, err := p.resolveTile(r.Tile)
	if err != nil {
		return "", Subject{}, err
	}
	to, err := named(r.To, sel)
	switch {
	case err != nil:
		return "", Subject{}, err
	case r.From == "" || to == "":
		return "", Subject{}, badRequest("bad request body: from and to are required")
	case !util.DeploymentNameOK(r.From):
		return "", Subject{}, badRequest(badNameMsg)
	}
	return OpPromote, p.subjectOf(tile, to), nil
}

// ---- attach live reload ----

// runAttach attaches live reload to y: the former target x is pinned to a
// fresh checkpoint of the work tree (its entry how attach, the pointer at
// request time), and y follows the work tree from the commit on (07-runtime
// §8.6). Saves drive nothing between the request and the commit.
func runAttach(ctx context.Context, p *Plane, g Grant, r *AttachRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	y, x := g.Subject.Deployment, o.rec.LiveReload
	d := o.rec.Deployments[y]
	switch {
	case d == nil:
		return nil, opError(o.tile, util.NoDeployment(o.tile, y))
	case x == y: // already following the work tree
		return p.answer(ctx, r.DryRun, nil, Impact{}, false)
	case x == "":
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: "live reload is paused: resume it onto " + y + " instead"}
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	if r.DryRun {
		im := Impact{Data: "none", Affects: affects(o.rec, y), Reloads: []string{y},
			Code: &CodeImpact{Deployment: y, From: p.shortOf(ctx, o.tile, *d.Checkpoint), To: "work-tree"}}
		p.measure(ctx, o.c, im.Code, o.by, *d.Checkpoint, "")
		return p.answer(ctx, true, nil, im, false)
	}
	if p.full(o.tile, x) {
		return nil, queueFull(x)
	}
	release := p.pausing.on(o.tile)
	defer release()
	res, err := p.capture(ctx, o, false)
	if err != nil {
		p.catchUp(o, x)
		return nil, opError(o.tile, err)
	}
	pin, err := p.pinAttempt(o, x, res.Hash)
	if err != nil {
		p.catchUp(o, x)
		return nil, err
	}
	follow := o.newAttempt("attach", y, res.Hash)
	follow.FollowsWorkTree = true
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commitMoves(ctx, o, r.Seq, []*attempt{pin}, []*attempt{follow}, func(r *Record) error {
		dy := r.Deployments[y]
		if dy == nil {
			return util.NoDeployment(o.tile, y)
		}
		dy.Checkpoint, dy.State = nil, ""
		r.LiveReload, r.LastLiveReload, r.LiveReloadSince = y, y, since
		return nil
	})
	if err != nil {
		p.catchUp(o, x)
		return nil, err
	}
	p.followed(ctx, o, rec, follow)
	p.startAttempt(o, pin)
	return p.answer(ctx, false, follow, Impact{}, false)
}

// pinAttempt prepares tree for dep, the live reload target an operation
// moves off the work tree, and makes its attempt: how attach, the pointer at
// request time. The pin is a pause of dep as its swap rechecks it (a grant
// on dep itself, judged now), whatever operation takes it.
func (p *Plane) pinAttempt(o *op, dep, tree string) (*attempt, error) {
	if err := p.prepareFor(o, dep, tree); err != nil {
		return nil, opError(o.tile, err)
	}
	g, err := p.Authorize(o.g.P, OpPause, subjectFrom(o.tile, dep, o.rec))
	if err != nil {
		return nil, err
	}
	a := o.newAttempt("attach", dep, tree)
	a.Pointer, a.g = pointerRequest, g
	return a, nil
}

// commitMoves accepts pins and rest (journaled as queued) and commits, in
// one change, each pin's deployment moved off the work tree onto the pin's
// checkpoint (07-runtime §8.2's second row: live reload detaches from it),
// then change. A pin applies only while its deployment still follows the
// work tree. Nothing committed: the attempts never happened.
func (p *Plane) commitMoves(ctx context.Context, o *op, seq *int64, pins, rest []*attempt, change func(*Record) error) (*Record, error) {
	as := append(append([]*attempt(nil), pins...), rest...)
	for i, a := range as {
		if err := p.accept(ctx, o.tile, o.rec, a); err != nil {
			p.discardAll(as[:i])
			return nil, opError(o.tile, err)
		}
	}
	rec, err := p.commit(o, seq, func(r *Record) error {
		for _, a := range pins {
			d := r.Deployments[a.Deployment]
			switch {
			case d == nil:
				return util.NoDeployment(o.tile, a.Deployment)
			case d.Checkpoint != nil || r.LiveReload != a.Deployment:
				return moved(o.tile)
			}
			tree := a.Tree
			d.Checkpoint, d.State, r.LiveReload = &tree, "", ""
		}
		if err := change(r); err != nil {
			return err
		}
		for _, a := range as {
			r.NextDeploy = max(r.NextDeploy, a.ID+1)
		}
		return nil
	})
	if err != nil {
		p.discardAll(as)
		return nil, opError(o.tile, err)
	}
	p.syncJournal(o.tile) // an opt-in's journal appears with its record
	for _, a := range pins {
		if a.Deployment == rec.Primary {
			p.primaryCodeMoved() // its code was prepared (prepareFor)
		}
	}
	return rec, nil
}

// followed finishes an attach whose follow attempt put its deployment on
// the work tree: no checkpoint may land there any more, the record is
// announced, the view repository refreshed, and the deployment rebuilds
// from the work tree, whose frames reload (as ops.go's resume does).
func (p *Plane) followed(ctx context.Context, o *op, rec *Record, follow *attempt) {
	y := follow.Deployment
	p.cancelWaiting(o.tile, y, "cancelled: live reload drives "+y+" again")
	p.finish(follow, resultOK, nil)
	p.announce(o.tile, rec, o.by, []string{"liveReload", "deployments"}, readerChange(o.rec, rec))
	if y == rec.Primary {
		p.prep.drop(o.tile) // the primary follows the work tree again
		p.primaryCodeMoved()
	}
	p.syncView(ctx, o.tile, rec)
	if p.Run != nil {
		p.Run.ChangedDeployment(o.c, y)
	}
	p.publishReload(o.tile, y)
}

func (p *Plane) discardAll(as []*attempt) {
	for _, a := range as {
		p.discard(a)
	}
}

// ---- add a deployment ----

// runAdd adds deployment y (05-model §5): its code from the work tree (a
// fresh checkpoint), the primary's code or a named checkpoint; empty data in
// its own (scope, y) namespace, which joins a sibling's when one claims it;
// a vault of placeholders; deliveries on (their default: its cron jobs and
// bus subscriptions fire for it, D127h); alwaysOn and limit overrides off. Any
// file an earlier deployment of the name left is dropped before the record
// commits (D119i, D127i). With attach, y follows the work tree and the former
// live reload target is pinned where it stands. On a tile without a record
// it is an opt-in: the store and the record are made by the commit.
func runAdd(ctx context.Context, p *Plane, g Grant, r *AddRequest) (any, error) {
	switch {
	case r.From != "" && r.From != FromWorkTree && r.From != FromPrimary && !strings.HasPrefix(r.From, "c:"):
		return nil, badRequest(`bad request body: from takes "work-tree", "primary" or a checkpoint id`)
	case r.Data != "" && r.Data != DataEmpty && r.Data != DataSeed:
		return nil, badRequest(`bad request body: data takes "empty" or "seed"`)
	case r.Attach && r.From != "" && r.From != FromWorkTree:
		return nil, badRequest("attach:true makes the new deployment follow the work tree: send no from with it")
	}
	if _, err := parseFrom(r.From); err != nil {
		return nil, err
	}
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	y := g.Subject.Deployment
	if err := p.addable(o, y); err != nil {
		return nil, err
	}
	if r.Data == DataSeed { // a tile manager's act, and a copy of personal data (08-data §8.1)
		if e := p.managerAct(g.P, acts[OpSeed], o.tile); e != nil {
			return nil, e
		}
		if err := confirmed(r.Confirm, ConfirmCopyData, "seeding "+y+" copies "+o.rec.Primary+"'s data, which may be personal"); err != nil {
			return nil, err
		}
		if err := p.seedable(o, y); err != nil {
			return nil, err
		}
	}
	joins, err := p.joining(o, g.P, y)
	if err != nil {
		return nil, err
	}
	if r.Attach {
		return p.addAttached(ctx, o, r, y, joins)
	}
	tree, im, err := p.addCode(ctx, o, r, y)
	if err != nil || r.DryRun {
		im.Joins = joins
		return p.addAnswer(ctx, r.DryRun, nil, im, joins, err)
	}
	if err := p.prepareFor(o, y, tree); err != nil {
		return nil, opError(o.tile, err)
	}
	if err := p.chromeCode(o, y, tree); err != nil {
		return nil, err
	}
	if err := p.dropStale(o.tile, y); err != nil {
		return nil, err
	}
	a := o.newAttempt("add", y, tree)
	a.Pointer = pointerRequest
	if r.From == FromPrimary {
		a.From = o.rec.Primary
	}
	rec, err := p.commitMoves(ctx, o, r.Seq, nil, []*attempt{a}, func(r *Record) error {
		return p.created(o, r, y, &tree)
	})
	if err != nil {
		return nil, err
	}
	p.announce(o.tile, rec, o.by, []string{"deployments"}, readerChange(o.rec, rec))
	p.syncView(ctx, o.tile, rec)
	p.startAttempt(o, a) // prepared, built and committed; its process starts on the first request
	return p.addAnswer(ctx, false, a, Impact{}, joins, nil)
}

// addAttached adds y following the work tree, live reload attached to it;
// the former target, if live reload was attached, is pinned to a fresh
// checkpoint of the work tree, as an attach pins it. y's entry (how add)
// names that capture; it is the answer's deploy.
func (p *Plane) addAttached(ctx context.Context, o *op, r *AddRequest, y string, joins *Joins) (any, error) {
	x := o.rec.LiveReload
	if r.DryRun {
		im := Impact{Data: "none", Affects: "nobody", Joins: joins}
		if x != "" && o.state != RecordNone {
			res, err := p.capture(ctx, o, true)
			if err != nil {
				return nil, opError(o.tile, err)
			}
			im.Code = &CodeImpact{Deployment: x, From: "work-tree", To: res.ID, WorkTreeAt: p.stamp()}
		} else if err := p.estimate(ctx, o); err != nil {
			return nil, err
		}
		return p.answer(ctx, true, nil, im, false)
	}
	if x != "" && p.full(o.tile, x) {
		return nil, queueFull(x)
	}
	if err := p.chromeCode(o, y, ""); err != nil {
		return nil, err
	}
	release := p.pausing.on(o.tile)
	defer release()
	res, err := p.capture(ctx, o, false)
	if err != nil {
		p.catchUp(o, x)
		return nil, opError(o.tile, err)
	}
	var pins []*attempt
	if x != "" {
		pin, err := p.pinAttempt(o, x, res.Hash)
		if err != nil {
			p.catchUp(o, x)
			return nil, err
		}
		pins = append(pins, pin)
	}
	if err := p.dropStale(o.tile, y); err != nil {
		p.catchUp(o, x)
		return nil, err
	}
	add := o.newAttempt("add", y, res.Hash)
	add.FollowsWorkTree = true
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commitMoves(ctx, o, r.Seq, pins, []*attempt{add}, func(r *Record) error {
		if err := p.created(o, r, y, nil); err != nil {
			return err
		}
		r.LiveReload, r.LastLiveReload, r.LiveReloadSince = y, y, since
		return nil
	})
	if err != nil {
		p.catchUp(o, x)
		return nil, err
	}
	p.followed(ctx, o, rec, add)
	for _, pin := range pins {
		p.startAttempt(o, pin)
	}
	return p.addAnswer(ctx, false, add, Impact{}, joins, nil)
}

// addCode is the code a new deployment y starts with, and the dry run's
// impact: a fresh checkpoint of the work tree (on a tile without a record a
// dry run captures nothing and only checks the caps: its code is null), the
// primary's checkpoint (its work tree's capture while it follows it), or a
// named checkpoint, which a tile without a record doesn't have (its store,
// if any, is inert: never read).
func (p *Plane) addCode(ctx context.Context, o *op, r *AddRequest, y string) (string, Impact, error) {
	im := Impact{Data: "none", Affects: "nobody"}
	var tree, from string
	switch primary := o.rec.Deployments[o.rec.Primary]; {
	case strings.HasPrefix(r.From, "c:") && o.state == RecordNone:
		return "", im, &Error{Status: http.StatusNotFound, Msg: fmt.Sprintf("%s has no checkpoint %s", o.tile, r.From)}
	case strings.HasPrefix(r.From, "c:"):
		cp, err := p.store().Resolve(ctx, o.tile, r.From)
		if err != nil {
			return "", im, opError(o.tile, err)
		}
		tree = cp.Hash
	case r.From == FromPrimary && primary.Checkpoint != nil:
		tree = *primary.Checkpoint
	case r.DryRun && o.state == RecordNone:
		return "", im, p.estimate(ctx, o)
	default:
		res, err := p.capture(ctx, o, r.DryRun)
		if err != nil {
			return "", im, opError(o.tile, err)
		}
		tree, from = res.Hash, "work-tree"
	}
	id := p.shortOf(ctx, o.tile, tree)
	im.Code = &CodeImpact{Deployment: y, From: cmp.Or(from, id), To: id}
	if from != "" {
		im.Code.WorkTreeAt = p.stamp()
	}
	return tree, im, nil
}

// estimate is a zero-state dry run's check of the work tree against the
// capture caps: nothing captured, no store made (D119c).
func (p *Plane) estimate(ctx context.Context, o *op) error {
	est, err := p.store().Estimate(ctx, p.source(o.c))
	if err == nil {
		err = est.Check(o.tile, p.store().Caps())
	}
	return opError(o.tile, err)
}

// created adds y's entry to r, pinned to tree or (nil) following the work
// tree, stamped; a tile's first opt-in stamps main's entry too.
func (p *Plane) created(o *op, r *Record, y string, tree *string) error {
	if r.Deployments[y] != nil {
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: fmt.Sprintf("%s already has a deployment %q", o.tile, y)}
	}
	at := p.stamp()
	if m := r.Deployments[util.MainDeployment]; m.Created == "" {
		m.Created, m.By = at, o.by
	}
	r.Deployments[y] = &DeploymentRecord{Checkpoint: tree, Created: at, By: o.by}
	return nil
}

// dropStale drops what an earlier deployment called y left under the tile's
// key (its vault, prefs, registration files and derived state: the broker's
// DropDeploymentFiles), before the new one's record commits (D119i, D127i).
// The broker removes the registration files through RemoveDeploymentFile,
// so this runs outside the records directory's lock.
func (p *Plane) dropStale(tile, y string) error {
	if p.DropDeploymentFiles == nil {
		return nil
	}
	if err := p.DropDeploymentFiles(tile, y); err != nil {
		return &Error{Status: http.StatusInternalServerError, Msg: fmt.Sprintf("%s: an earlier deployment %q left files that can't be removed: %v", tile, y, err)}
	}
	return nil
}

// addAnswer is add's answer: the entry and the namespace joined, or the
// dry run's impact.
func (p *Plane) addAnswer(ctx context.Context, dry bool, a *attempt, im Impact, joins *Joins, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	ans, err := p.answer(ctx, dry, a, im, false)
	if dry || err != nil {
		return ans, err
	}
	return AddAnswer{Answer: ans.(Answer), Joins: joins}, nil
}

// joinsOf answers the (scope, y) data namespace a new deployment y of tile
// would join, from the namespace's ns.json: the broker's namespace plane
// (08-data §6.1–§6.4), asked as a manager, since joinGate judges the actor.
// Without that plane no namespace is reported shared.
func (p *Plane) joinsOf(tile, y string) (*Joins, error) {
	if p.JoinData == nil {
		return nil, nil
	}
	return p.JoinData(tile, y, true)
}

// dropNamespace deletes the (scope, y) data namespace once no member tile of
// tile's scope claims y any more (08-data §9.2 step 3, §6.4): the broker's
// namespace plane. Without that plane a removed deployment's namespace
// stays, an orphan the namespace sweep lists and collects (§9.3).
func (p *Plane) dropNamespace(tile, y string) error {
	if p.DropData == nil {
		return nil
	}
	_, err := p.DropData(tile, y, false)
	return err
}

// ---- remove a deployment ----

// runRemove removes y (05-model §5; 08-data §9.2): never main, never the
// primary, confirmed with "erase". The record lets go of y first, so nothing
// starts it or writes its registration files again (the record has no
// removing state: 11-contract §10.1); live reload detaches if y held it,
// and lastLiveReload falls to the primary. Then y stops, its deploys end
// cancelled, and its registrations, vault, prefs, derived state and (with
// its last claimant) its data namespace go; its checkpoints fall to GC,
// which runs once the removal is committed (07-runtime §2.8).
func runRemove(ctx context.Context, p *Plane, g Grant, r *RemoveRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	y := g.Subject.Deployment
	switch {
	case o.rec.Deployments[y] == nil:
		return nil, opError(o.tile, util.NoDeployment(o.tile, y))
	case y == util.MainDeployment:
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: "main can't be removed"}
	case y == o.rec.Primary:
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: y + " is the primary of " + o.tile}
	}
	if err := confirmed(r.Confirm, ConfirmErase, "removing "+y+" deletes its data, secrets and logs"); err != nil {
		return nil, err
	}
	if r.DryRun {
		return p.answer(ctx, true, nil, Impact{Data: "erase", PausesLiveReload: o.rec.LiveReload == y,
			Stops: []string{y}, Affects: "deployment"}, false)
	}
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commit(o, r.Seq, func(r *Record) error {
		switch {
		case r.Deployments[y] == nil:
			return util.NoDeployment(o.tile, y)
		case y == r.Primary:
			return moved(o.tile)
		case r.LiveReload == y:
			r.LiveReload, r.LastLiveReload, r.LiveReloadSince = "", r.Primary, since
		case r.LastLiveReload == y:
			r.LastLiveReload = r.Primary
		}
		delete(r.Deployments, y)
		return nil
	})
	if err != nil {
		return nil, opError(o.tile, err)
	}
	p.endDeploys(o.tile, y)
	if p.Run != nil {
		p.Run.StopDeployment(o.tile, y)
	}
	var errs []error
	if p.DropRegistrations != nil {
		errs = append(errs, p.DropRegistrations(o.tile, y))
	}
	if p.DropDeploymentFiles != nil {
		errs = append(errs, p.DropDeploymentFiles(o.tile, y))
	}
	if err := errors.Join(append(errs, p.dropNamespace(o.tile, y))...); err != nil {
		warn("removing "+y+"'s files; the leftovers stay listed until they are swept", o.tile, err)
	}
	what := []string{"deployments"}
	if o.rec.LiveReload == y {
		what = []string{"liveReload", "deployments"}
	}
	p.announce(o.tile, rec, o.by, what, readerChange(o.rec, rec))
	p.syncView(ctx, o.tile, rec)
	p.retain(o.tile) // GC: y's trees and artifacts are no longer referenced
	return p.answer(ctx, false, nil, Impact{}, false)
}

// endDeploys ends a removed deployment's deploys, cancelled: those waiting,
// and the one in flight, whose swap the record now refuses; and forgets
// what the plane saw of its generations, so a new deployment of the name
// starts clean.
func (p *Plane) endDeploys(tile, dep string) {
	why := "cancelled: " + dep + " was removed"
	p.cancelWaiting(tile, dep, why)
	p.q.mu.Lock()
	var running *attempt
	if l := p.q.lanes[laneKey(tile, dep)]; l != nil && l.running != nil && !l.running.finished() {
		running = l.running
	}
	if t := p.q.tiles[tile]; t != nil {
		delete(t.lastErr, dep)
		delete(t.serving, dep)
	}
	p.q.mu.Unlock()
	if running != nil {
		p.finish(running, resultCancelled, errors.New(why))
	}
}

// ---- promote ----

// runPromote gives to (B) from's (A) current code (D127e): A's checkpoint, or
// a fresh checkpoint of the work tree when A follows it. expect names the
// code the caller reviewed (a diff's X-XBin-Checkpoint-To, a dry run's
// impact.code.to): the promotion moves exactly that or answers 409. Onto a
// protected primary expect and seq are required (D127m). Only code moves:
// B's data, vault, registrations, switches and routing stay B's.
func runPromote(ctx context.Context, p *Plane, g Grant, r *PromoteRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	a, b := r.From, g.Subject.Deployment
	da := o.rec.Deployments[a]
	switch {
	case o.rec.Deployments[b] == nil:
		return nil, opError(o.tile, util.NoDeployment(o.tile, b))
	case da == nil:
		return nil, opError(o.tile, util.NoDeployment(o.tile, a))
	case a == b:
		return nil, &Error{Status: http.StatusConflict, Kind: KindState, Msg: "promotion moves one deployment's code onto another: from and to are both " + a}
	case o.rec.ProtectedPrimary && b == o.rec.Primary && (r.Expect == "" || r.Seq == nil):
		return nil, badRequest("the primary of " + o.tile + " is protected: name the checkpoint you reviewed (send expect and seq)")
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	prefix, err := parseExpect(r.Expect)
	if err != nil {
		return nil, err
	}
	var tree, id string
	if da.Checkpoint == nil {
		res, err := p.capture(ctx, o, r.DryRun)
		if err != nil {
			return nil, opError(o.tile, err)
		}
		tree, id = res.Hash, res.ID
	} else {
		tree = *da.Checkpoint
		id = p.shortOf(ctx, o.tile, tree)
	}
	if prefix != "" && !strings.HasPrefix(tree, prefix) {
		return nil, expectMismatch(r.Expect, id)
	}
	return p.promoteCode(ctx, o, r.Seq, r.DryRun, a, b, tree, da.Checkpoint == nil)
}

// promoteCode puts tree, from's code, on dep as ops.go's moveCode puts a
// deploy's, its entry naming from: a deployment that follows the work tree
// leaves it (live reload pauses, the pointer is written now); a pinned one
// gets a queued deploy whose swap writes the pointer. A request equal to
// the lane's tail merges into it; code dep already runs, healthy, with
// nothing queued, answers unchanged.
func (p *Plane) promoteCode(ctx context.Context, o *op, seq *int64, dry bool, from, dep, tree string, captured bool) (any, error) {
	d := o.rec.Deployments[dep]
	tail := p.tail(o.tile, dep)
	unchanged := tail == nil && d.Checkpoint != nil && *d.Checkpoint == tree && d.State != "failed" && !p.down(o.c, dep)
	if dry {
		im := Impact{Data: "none", PausesLiveReload: d.Checkpoint == nil, Affects: affects(o.rec, dep),
			Code: &CodeImpact{Deployment: dep, From: "work-tree", To: p.shortOf(ctx, o.tile, tree)}}
		if captured {
			im.Code.WorkTreeAt = p.stamp()
		}
		if d.Checkpoint != nil {
			im.Code.From = p.shortOf(ctx, o.tile, *d.Checkpoint)
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
	if d.Checkpoint != nil && tail != nil {
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
	if d.Checkpoint == nil {
		release := p.pausing.on(o.tile) // saves drive nothing until the commit
		defer release()
	}
	if err := p.prepareFor(o, dep, tree); err != nil {
		if d.Checkpoint == nil {
			p.catchUp(o, dep)
		}
		return nil, opError(o.tile, err)
	}
	a := o.newAttempt("promote", dep, tree)
	a.From = from
	if d.Checkpoint != nil {
		if *d.Checkpoint == tree {
			a.How = "restart" // a failed or down deployment's own code, from its kept artifact (07-runtime §8.7)
		}
		if err := p.accept(ctx, o.tile, o.rec, a); err != nil {
			return nil, opError(o.tile, err)
		}
		p.startAttempt(o, a)
		return p.answer(ctx, false, a, Impact{}, false)
	}
	a.Pointer = pointerRequest
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commitMoves(ctx, o, seq, []*attempt{a}, nil, func(r *Record) error {
		r.LastLiveReload, r.LiveReloadSince = dep, since
		return nil
	})
	if err != nil {
		p.catchUp(o, dep)
		return nil, err
	}
	p.announce(o.tile, rec, o.by, []string{"liveReload", "deployments"}, readerChange(o.rec, rec))
	p.syncView(ctx, o.tile, rec)
	p.startAttempt(o, a)
	return p.answer(ctx, false, a, Impact{}, false)
}

// ---- helpers ----

// prepareFor prepares tree for dep, as ops.go's prepareCode does: the
// primary's code is read, and refused when it can't start; any other
// deployment's is materialized on a turn of the runner's build limiter, so
// the plane's work for non-primary code never crowds out the primary's
// (07-runtime §10.3, D127q).
func (p *Plane) prepareFor(o *op, dep, tree string) error {
	if dep == o.rec.Primary {
		return p.prepareCode(o, dep, tree)
	}
	release := runner.NonPrimaryBuildTurn()
	defer release()
	_, err := p.store().Materialize(o.tile, tree)
	return err
}

// parseFrom validates add's from: a checkpoint id's prefix, "" otherwise.
func parseFrom(from string) (string, error) {
	if !strings.HasPrefix(from, "c:") {
		return "", nil
	}
	return parseExpect(from)
}
