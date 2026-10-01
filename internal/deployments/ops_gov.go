package deployments

// ops_gov.go — the governance operations (05-model §5, §10; 11-contract
// §1.7–§1.9; 09-fabric §7, §8), tile managers' acts: reassigning and
// protecting the primary, edge policies, deliveries, alwaysOn, limits; and
// the data and delivery acts, delegating to their planes: seed, reset, vault
// copy, run now (ops_gov_data.go). They share ops.go's context (the tile's lock, the seq
// check, the commit with the grant rechecked, T9). What they need beyond the
// plane's hooks comes through GovHooks: without its hook an act answers 501,
// or leaves out the step it can do without.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	register(OpPrimary, Handler[PrimaryRequest]{Subject: namedBy(OpPrimary, (*PrimaryRequest).ref), Run: runPrimary})
	register(OpProtect, Handler[ProtectRequest]{Subject: protectSubject, Run: runProtect})
	register(OpEdge, Handler[EdgeRequest]{Subject: edgeSubject, Run: runEdge})
	register(OpDeliveries, Handler[SwitchRequest]{Subject: namedBy(OpDeliveries, (*SwitchRequest).ref), Run: runSwitch})
	register(OpAlwaysOn, Handler[SwitchRequest]{Subject: namedBy(OpAlwaysOn, (*SwitchRequest).ref), Run: runSwitch})
	register(OpLimits, Handler[LimitsRequest]{Subject: namedBy(OpLimits, (*LimitsRequest).ref), Run: runLimits})
	register(OpSeed, Handler[SeedRequest]{Subject: namedBy(OpSeed, (*SeedRequest).ref), Run: runSeed})
	register(OpReset, Handler[ResetRequest]{Subject: namedBy(OpReset, (*ResetRequest).ref), Run: runReset})
	register(OpVaultCopy, Handler[VaultCopyRequest]{Subject: namedBy(OpVaultCopy, (*VaultCopyRequest).ref), Run: runVaultCopy})
	register(OpRunNow, Handler[RunNowRequest]{Subject: namedBy(OpRunNow, (*RunNowRequest).ref), Run: runRunNow})
	// The code moves ops.go built before protection (promote checks its own).
	guardReviewed(OpDeploy, "checkpoint", func(r *DeployRequest) bool { return r.Checkpoint != "" && r.Seq != nil })
	guardReviewed(OpRollback, "checkpoint", func(r *RollbackRequest) bool { return r.Checkpoint != "" && r.Seq != nil })
	guardReviewed(OpReloadNow, "expect", func(r *ReloadNowRequest) bool { return r.Expect != "" && r.Seq != nil })
}

// ref answers a request's tile ref and the deployment it names, for namedBy.
func (r *PrimaryRequest) ref() (string, string)   { return r.Tile, r.Deployment }
func (r *SeedRequest) ref() (string, string)      { return r.Tile, r.Deployment }
func (r *ResetRequest) ref() (string, string)     { return r.Tile, r.Deployment }
func (r *VaultCopyRequest) ref() (string, string) { return r.Tile, r.Deployment }
func (r *SwitchRequest) ref() (string, string)    { return r.Tile, r.Deployment }
func (r *LimitsRequest) ref() (string, string)    { return r.Tile, r.Deployment }
func (r *RunNowRequest) ref() (string, string)    { return r.Tile, r.Deployment }

// GovHooks are what the governance acts ask of the terminal manager and the
// broker beyond the plane's hooks, nil until boot installs them.
type GovHooks struct {
	// SessionsProtected and SessionsReassigned are term.Manager's
	// PrimaryProtected and PrimaryReassigned (D127p), called after the commit,
	// outside the plane's locks; without them a moved session's calls fail.
	SessionsProtected  func(tile string) (restarted, ended int)
	SessionsReassigned func(tile, from, to string) (restarted, ended int)
	// RoutesReassigned is the broker's PrimaryReassigned (09-fabric §8 steps
	// 3-4), called after the commit, outside the plane's locks: the tile's
	// consumers re-bound when its active interface instances changed, and
	// the ingress reconciled when its active hosts did.
	RoutesReassigned func(tile, from, to string)
	// ValidateEdge and EdgeRestarts are the broker's ValidateEdgePolicy (404
	// no such edge, 400 a value it doesn't take) and EdgeChangeRestarts.
	ValidateEdge func(tile, edge, policy string) error
	EdgeRestarts func(tile, edge string) bool
	// DiskCeiling is the per-scope disk quota in bytes, diskGiB's ceiling.
	DiskCeiling func() int64
	// SeedData fills req's deployment's namespace from the primary's
	// (08-data §8), the broker's SeedDeploymentData: it judges the manager
	// gate again with p (a person's own session, no view-as), authorize
	// judges every other claimant and stop stops one deployment; req.Stop
	// is the point-in-time mode. It answers the facts it judged.
	SeedData func(p auth.Principal, req SeedRequest, authorize func(tile string) error,
		stop func(tile, dep string)) (SeedFacts, error)
}

// govByPlane holds each plane's GovHooks, installed before the daemon
// serves (boot), or by a test on the literal it builds.
var govByPlane = struct {
	sync.RWMutex
	m map[*Plane]GovHooks
}{m: map[*Plane]GovHooks{}}

// SetGovHooks installs p's governance hooks: set fills those its caller has
// (the broker's, the terminal manager's), keeping the others.
func (p *Plane) SetGovHooks(set func(h *GovHooks)) {
	govByPlane.Lock()
	defer govByPlane.Unlock()
	h := govByPlane.m[p]
	set(&h)
	govByPlane.m[p] = h
}

func (p *Plane) gov() GovHooks {
	govByPlane.RLock()
	defer govByPlane.RUnlock()
	return govByPlane.m[p]
}

// The runner's facets these acts use where it has them (*runner.Runner).
type (
	reassigner interface {
		Reassign(ctx context.Context, c *registry.Component, from, to string) error
	}
	alwaysOnWaker interface{ WakeAlwaysOn() }
)

// namedBy is the subject of op on the deployment a request names: its
// deployment field or its ref's qualifier.
func namedBy[R any](op Op, ref func(*R) (tile, dep string)) func(*Plane, *R) (Op, Subject, error) {
	return func(p *Plane, r *R) (Op, Subject, error) { tile, dep := ref(r); return p.namedSubject(op, tile, dep) }
}

// protectSubject: the tile; on is required, so no body unprotects by omission.
func protectSubject(p *Plane, r *ProtectRequest) (Op, Subject, error) {
	tile, _, err := p.resolveTile(r.Tile)
	switch {
	case err != nil:
		return "", Subject{}, err
	case r.On == nil:
		return "", Subject{}, badRequest("bad request body: on is required (true protects the primary, false unprotects it)")
	case !*r.On:
		return OpUnprotect, p.subjectOf(tile, ""), nil
	}
	return OpProtect, p.subjectOf(tile, ""), nil
}

// edgeSubject: the tile. Without the broker's edge check the route answers
// 501 first, as a reserved one does: this xbind can't judge an edge.
func edgeSubject(p *Plane, r *EdgeRequest) (Op, Subject, error) {
	tile, _, err := p.resolveTile(r.Tile)
	if err == nil && p.gov().ValidateEdge == nil {
		err = notBuilt("setting an edge policy", "the broker's edge check isn't wired")
	} else if err == nil && (r.Edge == "" || r.Policy == "") {
		err = badRequest("bad request body: edge and policy are required")
	}
	return OpEdge, p.subjectOf(tile, ""), err
}

// start begins an operation (ops.go's begin; lock false never locks: an act
// that changes no record) and refuses a stale seq; with on, a grant's
// deployment that doesn't exist (404) or, notPrimary, is the primary (409).
func (p *Plane) start(g Grant, dry, lock bool, seq *int64, on, notPrimary bool, why string) (*op, error) {
	o, err := p.begin(g, dry || !lock)
	if err != nil {
		return nil, err
	}
	switch y, d := g.Subject.Deployment, o.rec.Deployments[g.Subject.Deployment]; {
	case o.checkSeq(seq) != nil:
		err = o.checkSeq(seq)
	case on && d == nil:
		err = opError(o.tile, util.NoDeployment(o.tile, y))
	case on && notPrimary && y == o.rec.Primary:
		err = &Error{Status: http.StatusConflict, Kind: KindState, Msg: y + " is the primary of " + o.tile + why}
	}
	if err != nil {
		o.done()
		return nil, err
	}
	return o, nil
}

// notBuilt answers an act, or part of one, this xbind can't do yet.
func notBuilt(what, why string) error {
	return &Error{Status: http.StatusNotImplemented, Msg: what + " isn't built in this xbind yet: " + why}
}

// edit commits change (a setting: false means unchanged) and announces what
// moved; a dry run applies it to a copy. rec is nil unless written.
func (p *Plane) edit(ctx context.Context, o *op, seq *int64, dry bool, what string, im Impact,
	change func(*Record) (bool, error)) (ans any, rec *Record, err error) {
	if dry {
		if _, err := change(o.rec.Clone()); err != nil {
			return nil, nil, opError(o.tile, err)
		}
		ans, err = p.answer(ctx, true, nil, im, false)
		return ans, nil, err
	}
	rec, err = p.commit(o, seq, func(r *Record) error {
		changed, err := change(r)
		if err == nil && !changed {
			return errNoChange
		}
		return err
	})
	if errors.Is(err, errNoChange) {
		ans, err = p.answer(ctx, false, nil, Impact{}, true)
		return ans, nil, err
	} else if err != nil {
		return nil, nil, opError(o.tile, err)
	}
	p.announce(o.tile, rec, o.by, []string{what}, readerChange(o.rec, rec))
	ans, err = p.answer(ctx, false, nil, Impact{}, false)
	return ans, rec, err
}

// runPrimary reassigns the primary from x to y (05-model §5; 09-fabric §8;
// 11-contract §1.7): routing only, confirmed "data-stays", onto a healthy y
// of a tile no scope shares (D127t). While the primary is protected the
// request names y's reviewed code and seq, and a y following the work tree
// is pinned to exactly that capture in the same commit (how reassign, D127m).
// Then the sessions naming either move, the tile's consumers and ingress
// follow y's registrations, and the runner restarts y, then x.
func runPrimary(ctx context.Context, p *Plane, g Grant, r *PrimaryRequest) (any, error) {
	o, err := p.start(g, r.DryRun, true, r.Seq, true, false, "")
	if err != nil {
		return nil, err
	}
	unlock := sync.OnceFunc(o.done)
	defer unlock()
	y, x, protected := g.Subject.Deployment, o.rec.Primary, o.rec.ProtectedPrimary
	d := o.rec.Deployments[y]
	if y == x { // already the primary
		return p.answer(ctx, r.DryRun, nil, Impact{}, true)
	}
	if err := partitionedPrimary(o.c, x); err != nil { // partition.go (PD-17)
		return nil, err
	}
	err = confirmed(r.Confirm, ConfirmDataStays, "making "+y+" the primary sends everyone to "+y+"'s data, and "+x+"'s data stays behind")
	if err == nil && protected && (r.Expect == "" || r.Seq == nil) {
		err = unreviewed(o.tile, "expect")
	}
	if e := p.splitsScope(o.c); err == nil && e != nil {
		err = e
	}
	prefix, perr := parseExpect(r.Expect)
	for _, e := range []error{perr, p.needsIsolation(o.c), p.healthy(o, y, d)} {
		if err == nil {
			err = e
		}
	}
	if err != nil {
		return nil, err
	}
	// tree is y's code once primary ("" the work tree), seen what its reviewer
	// saw: a capture while y follows the work tree, when needed.
	pin := protected && d.Checkpoint == nil
	fail := func(err error) (any, error) {
		if pin && !r.DryRun {
			p.catchUp(o, y)
		}
		return nil, err
	}
	if pin && !r.DryRun {
		defer p.pausing.on(o.tile)() // saves drive nothing until y is pinned
	}
	tree, seen, id := "", "", ""
	if d.Checkpoint != nil {
		tree, seen, id = *d.Checkpoint, *d.Checkpoint, p.shortOf(ctx, o.tile, *d.Checkpoint)
	} else if protected || prefix != "" {
		res, err := p.capture(ctx, o, r.DryRun)
		if err != nil {
			return fail(opError(o.tile, err))
		}
		seen, id = res.Hash, res.ID
	}
	if pin {
		tree = seen
	}
	switch {
	case prefix != "" && !strings.HasPrefix(seen, prefix):
		return fail(expectMismatch(r.Expect, id))
	case r.DryRun:
		return p.reassignImpact(ctx, o, y, tree, pin)
	case tree != "":
		if err := p.primeCode(o, tree); err != nil {
			return fail(err)
		}
	}
	var a *attempt
	var pins []*attempt
	if pin {
		a = o.newAttempt("reassign", y, tree)
		a.Pointer, pins = pointerRequest, []*attempt{a}
	}
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commitMoves(ctx, o, r.Seq, pins, nil, func(rec *Record) error {
		dy := rec.Deployments[y]
		switch {
		case dy == nil:
			return util.NoDeployment(o.tile, y)
		case rec.Primary != x || (dy.Checkpoint == nil) != (tree == "") || dy.Checkpoint != nil && *dy.Checkpoint != tree:
			return moved(o.tile)
		case pin:
			rec.LastLiveReload, rec.LiveReloadSince = y, since
		}
		rec.Primary = y
		dy.Branch, dy.BranchOverride = "", "" // the primary takes no assigned branch (D131)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	what := []string{"primary", "deployments"}
	if tree == "" {
		p.prep.drop(o.tile) // the primary follows the work tree
	} else {
		p.prep.keep(o.tile, tree)
	}
	if pin {
		what = append(what, "liveReload") // commitMoves recomposed the tile from y's code
	} else {
		p.primaryCodeMoved()
	}
	p.announce(o.tile, rec, o.by, what, readerChange(o.rec, rec))
	p.syncView(ctx, o.tile, rec)
	unlock()
	if h := p.gov().SessionsReassigned; h != nil {
		h(o.tile, x, y)
	}
	if h := p.gov().RoutesReassigned; h != nil {
		h(o.tile, x, y) // prov#inst consumers re-bound, the ingress reconciled
	}
	if a != nil {
		p.q.mu.Lock()
		a.Result, a.Phase = resultRunning, "start"
		p.q.mu.Unlock()
		p.publishDeploy(a)
	}
	c, ok := p.component(o.tile) // recomposed from y's code
	if !ok {
		c = o.c
	}
	go p.reassignRuntime(c, x, y, a)
	ans, err := p.answer(ctx, false, a, Impact{}, false)
	if err != nil {
		return nil, err
	}
	return PrimaryAnswer{Answer: ans.(Answer)}, nil
}

// primeCode reads tree as the primary's code before the commit that makes it
// so, refusing code that can't start, for PinnedPrimary (D119e).
func (p *Plane) primeCode(o *op, tree string) error {
	pc, err := p.readCode(o.tile, tree)
	switch {
	case err != nil:
		return opError(o.tile, err)
	case pc.ManifestErr != "":
		return &Error{Status: http.StatusConflict, Kind: KindState,
			Msg: fmt.Sprintf("%s: checkpoint %s can't start: %s", o.tile, shortTree(tree), pc.ManifestErr)}
	}
	p.prep.put(o.tile, preparation{tree: tree, pc: pc})
	return nil
}

// reassignImpact is a reassignment's dry run: the primary's code from x's to
// y's tree, measured; the vault keys y has no value for.
func (p *Plane) reassignImpact(ctx context.Context, o *op, y, tree string, pin bool) (any, error) {
	from, xTree := "work-tree", ""
	if cp := o.rec.Deployments[o.rec.Primary].Checkpoint; cp != nil {
		xTree, from = *cp, p.shortOf(ctx, o.tile, *cp)
	}
	im := Impact{Data: "none", PausesLiveReload: pin, Affects: "everyone", Reloads: []string{o.rec.Primary},
		Code: &CodeImpact{Deployment: y, From: from, To: "work-tree"}}
	if tree != "" {
		im.Code.To = p.shortOf(ctx, o.tile, tree)
	}
	if pin {
		im.Code.WorkTreeAt = p.stamp()
	}
	if xTree != tree {
		p.measure(ctx, o.c, im.Code, o.by, xTree, tree)
	}
	if p.VaultPlaceholders != nil {
		im.Placeholders, _ = p.VaultPlaceholders(o.tile, y)
	}
	return p.answer(ctx, true, nil, im, false)
}

// reassignRuntime restarts c's generations after the reassignment, y first
// (07-runtime §8.8), finishes a pin's attempt, and wakes alwaysOn.
func (p *Plane) reassignRuntime(c *registry.Component, x, y string, a *attempt) {
	var err error
	if rs, ok := p.Run.(reassigner); ok {
		err = rs.Reassign(context.Background(), c, x, y)
	}
	switch {
	case a != nil:
		p.finish(a, map[bool]string{true: resultFailed, false: resultOK}[err != nil], err)
	case err != nil:
		warn("reassigning the primary: restarting its generations", c.Path, err)
	}
	if w, ok := p.Run.(alwaysOnWaker); ok {
		w.WakeAlwaysOn()
	}
}

// ProtectAnswer is protect's answer, or its dry run's, with the tile's nested
// components and the warnings its manager reads (NP-06-8; T12).
type ProtectAnswer struct {
	Answer
	Impact   *Impact            `json:"impact,omitempty"`
	Nested   []NestedProtection `json:"nested,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
}

// runProtect protects the primary x (05-model §5; 11-contract §1.7): x
// following the work tree is pinned in place to a fresh capture (expect's,
// when given; how protect) and live reload detaches; a pinned x is rebuilt
// into the protected namespace (07-runtime §3.4). Then the sessions that
// targeted x move to D127p's default. On a tile without a record: an opt-in.
// Unprotecting moves nothing.
func runProtect(ctx context.Context, p *Plane, g Grant, r *ProtectRequest) (any, error) {
	o, err := p.start(g, r.DryRun, true, r.Seq, false, false, "")
	if err != nil {
		return nil, err
	}
	unlock := sync.OnceFunc(o.done)
	defer unlock()
	x, on := o.rec.Primary, g.Op == OpProtect
	cur := o.rec.Deployments[x].Checkpoint
	out := ProtectAnswer{}
	if on {
		out.Nested, out.Warnings = p.protectNotes(g.P, o.tile)
	}
	reply := func(ans any, err error) (any, error) {
		if v, ok := ans.(DryRunAnswer); ok {
			out.Impact = &v.Impact
		} else if v, ok := ans.(Answer); ok {
			out.Answer = v
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	prefix, err := parseExpect(r.Expect)
	switch {
	case err != nil:
		return nil, err
	case o.rec.ProtectedPrimary == on:
		return reply(p.answer(ctx, r.DryRun, nil, Impact{}, true))
	case !on && p.ProtectRequired != nil && p.ProtectRequired(o.tile) != "": // reviewed code only (plans/partitions/06 §4)
		return nil, &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: p.ProtectRequired(o.tile)}
	case !on || cur != nil: // x stays where it is
		if on && prefix != "" && !strings.HasPrefix(*cur, prefix) {
			return nil, expectMismatch(r.Expect, p.shortOf(ctx, o.tile, *cur))
		}
		ans, rec, err := p.edit(ctx, o, r.Seq, r.DryRun, "protectedPrimary", Impact{Data: "none", Affects: "nobody"},
			func(rec *Record) (bool, error) {
				if dx := rec.Deployments[x]; on && (rec.Primary != x || dx.Checkpoint == nil || *dx.Checkpoint != *cur) {
					return false, moved(o.tile)
				}
				was := rec.ProtectedPrimary
				rec.ProtectedPrimary = on
				return was != on, nil
			})
		if err == nil && rec != nil && on {
			if a := p.rebuildProtected(ctx, o, rec, x); a != nil {
				ans, err = p.answer(ctx, false, a, Impact{}, false)
			}
			unlock()
			p.sessionsProtected(o.tile)
		}
		return reply(ans, err)
	}
	if err := p.needsIsolation(o.c); err != nil {
		return nil, err
	}
	if r.DryRun {
		return reply(p.protectImpact(ctx, o, prefix, r.Expect))
	}
	release := p.pausing.on(o.tile) // saves drive nothing until x is pinned
	defer release()
	fail := func(err error) (any, error) { p.catchUp(o, x); return nil, err }
	res, err := p.capture(ctx, o, false)
	switch {
	case err != nil:
		return fail(opError(o.tile, err))
	case prefix != "" && !strings.HasPrefix(res.Hash, prefix):
		return fail(expectMismatch(r.Expect, res.ID))
	}
	if err := p.prepareCode(o, x, res.Hash); err != nil {
		return fail(opError(o.tile, err))
	}
	a := o.newAttempt("protect", x, res.Hash)
	a.Pointer = pointerRequest // its swap rechecks the act on x itself
	if a.g, err = p.Authorize(o.g.P, OpProtect, subjectFrom(o.tile, x, o.rec)); err != nil {
		return fail(err)
	}
	since := &Stamp{At: p.stamp(), By: o.by}
	rec, err := p.commitMoves(ctx, o, r.Seq, []*attempt{a}, nil, func(rec *Record) error {
		rec.ProtectedPrimary, rec.LastLiveReload, rec.LiveReloadSince = true, x, since
		if m := rec.Deployments[x]; m.Created == "" { // a tile's first opt-in
			m.Created, m.By = since.At, since.By
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	p.announce(o.tile, rec, o.by, []string{"liveReload", "protectedPrimary", "deployments"}, readerChange(o.rec, rec))
	p.syncView(ctx, o.tile, rec)
	p.startAttempt(o, a)
	release()
	unlock()
	p.sessionsProtected(o.tile)
	return reply(p.answer(ctx, false, a, Impact{}, false))
}

// protectImpact is the dry run of pinning the primary: its capture, or on a
// tile without a record the caps alone, nothing captured (D119c).
func (p *Plane) protectImpact(ctx context.Context, o *op, prefix, expect string) (any, error) {
	im := Impact{Data: "none", PausesLiveReload: true, Affects: "nobody"}
	if o.state == RecordNone {
		if err := p.estimate(ctx, o); err != nil {
			return nil, err
		}
		return p.answer(ctx, true, nil, im, false)
	}
	res, err := p.capture(ctx, o, true)
	switch {
	case err != nil:
		return nil, opError(o.tile, err)
	case prefix != "" && !strings.HasPrefix(res.Hash, prefix):
		return nil, expectMismatch(expect, res.ID)
	}
	im.Code = &CodeImpact{Deployment: o.rec.Primary, From: "work-tree", To: res.ID, WorkTreeAt: p.stamp()}
	return p.answer(ctx, true, nil, im, false)
}

// rebuildProtected queues a restart of a protected backend primary's
// checkpoint, built into the protected namespace (07-runtime §3.4).
func (p *Plane) rebuildProtected(ctx context.Context, o *op, rec *Record, x string) *attempt {
	if _, ok := p.Run.(restarter); !ok || !o.c.HasBackend() || p.full(o.tile, x) {
		return nil
	}
	a := o.newAttempt("protect", x, *rec.Deployments[x].Checkpoint)
	a.forced = true
	if err := p.accept(ctx, o.tile, rec, a); err != nil {
		warn("protecting: queueing the primary's rebuild", o.tile, err)
		return nil
	}
	p.enqueue(a)
	return a
}

func (p *Plane) sessionsProtected(tile string) { // D127p
	if h := p.gov().SessionsProtected; h != nil {
		h(tile)
	}
}

// runEdge sets an edge's policy for the tile's non-primary deployments
// (09-fabric §5; 11-contract §1.7), as the broker judges it; one that
// applies at a spawn restarts those that are up, never the primary.
func runEdge(ctx context.Context, p *Plane, g Grant, r *EdgeRequest) (any, error) {
	o, err := p.start(g, r.DryRun, true, r.Seq, false, false, "")
	if err != nil {
		return nil, err
	}
	defer o.done()
	h := p.gov()
	if err := h.ValidateEdge(o.tile, r.Edge, r.Policy); err != nil {
		return nil, opError(o.tile, err)
	}
	if !slices.Contains([]string{EdgeRead, EdgeBlock, EdgeInherit, EdgeDefault}, r.Policy) {
		return nil, badRequest(r.Edge + " takes read, block, inherit or default: " + strconv.Quote(r.Policy) + " is not an edge-policy value")
	}
	ans, rec, err := p.edit(ctx, o, r.Seq, r.DryRun, "edges", Impact{Data: "none", Affects: "deployment"}, func(rec *Record) (bool, error) {
		cur, had := rec.Edges[r.Edge]
		switch {
		case r.Policy == EdgeDefault && !had, had && cur == r.Policy:
			return false, nil
		case r.Policy == EdgeDefault:
			delete(rec.Edges, r.Edge)
		case rec.Edges == nil:
			rec.Edges = map[string]string{r.Edge: r.Policy}
		default:
			rec.Edges[r.Edge] = r.Policy
		}
		if len(rec.Edges) == 0 {
			rec.Edges = nil
		}
		return true, nil
	})
	if rec != nil && h.EdgeRestarts != nil && h.EdgeRestarts(o.tile, r.Edge) && p.Run != nil && o.c.HasBackend() {
		var deps []string
		for _, n := range sortedKeys(rec.Deployments) {
			if n != rec.Primary && !p.down(o.c, n) {
				deps = append(deps, n)
			}
		}
		if rs, ok := p.Run.(restarter); ok {
			go func() {
				for _, n := range deps {
					if err := rs.Restart(context.Background(), o.c, n, nil); err != nil {
						warn("restarting "+n+" for an edge policy", o.tile, err)
					}
				}
			}()
		}
	}
	return ans, err
}

// runSwitch sets a non-primary deployment's deliveries or alwaysOn switch
// (09-fabric §7; 11-contract §1.7): the broker asks deliveries at each tick,
// and on is their default, so on clears the stored off (D127h, revised);
// alwaysOn needs y's own code to say it, and wakes it.
func runSwitch(ctx context.Context, p *Plane, g Grant, r *SwitchRequest) (any, error) {
	if r.On == nil {
		return nil, badRequest("bad request body: on is required")
	}
	y, on, always := g.Subject.Deployment, *r.On, g.Op == OpAlwaysOn
	what, why := "deliveries", " — its cron jobs and bus subscriptions are always active"
	if always {
		what, why = "alwaysOn", ` — its own code's "alwaysOn" applies to it`
	}
	o, err := p.start(g, r.DryRun, true, r.Seq, true, true, why)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if always && on {
		if err := p.declaresAlwaysOn(o, y); err != nil {
			return nil, err
		}
	}
	ans, rec, err := p.edit(ctx, o, r.Seq, r.DryRun, what, Impact{Data: "none", Affects: "deployment"}, func(rec *Record) (bool, error) {
		d := rec.Deployments[y]
		if d == nil || y == rec.Primary {
			return false, moved(o.tile)
		}
		if !always {
			changed := d.DeliveriesOn() != on
			d.Deliveries = nil
			if !on {
				d.Deliveries = &on
			}
			return changed, nil
		}
		changed := d.AlwaysOn != on
		d.AlwaysOn = on
		return changed, nil
	})
	if w, ok := p.Run.(alwaysOnWaker); ok && rec != nil && always && on {
		w.WakeAlwaysOn()
	}
	return ans, err
}

// declaresAlwaysOn refuses alwaysOn unless y's code says it: its checkpoint
// (read on a non-primary build turn, D127q) or the work tree it follows.
func (p *Plane) declaresAlwaysOn(o *op, y string) error {
	m := o.c.WorkTreeManifest()
	if cp := o.rec.Deployments[y].Checkpoint; cp != nil {
		release := runner.NonPrimaryBuildTurn()
		pc, err := p.readCode(o.tile, *cp)
		release()
		if err != nil {
			return opError(o.tile, err)
		}
		m = pc.Manifest
	}
	if !m.AlwaysOn {
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: y + `'s code doesn't declare "alwaysOn"`}
	}
	return nil
}

// runLimits sets y's limit overrides (D127n; 11-contract §1.7), each below the
// tile's ceiling: memMiB and pids from its next generation (LimitsFor),
// diskGiB its namespace's quota; null removes one.
func runLimits(ctx context.Context, p *Plane, g Grant, r *LimitsRequest) (any, error) {
	o, err := p.start(g, r.DryRun, true, r.Seq, true, false, "")
	if err != nil {
		return nil, err
	}
	defer o.done()
	y, keys := g.Subject.Deployment, r.Limits.Keys()
	if len(keys) == 0 {
		return nil, badRequest("bad request body: limits names none of memMiB, pids and diskGiB")
	}
	for _, k := range sortedKeys(keys) {
		if err := p.limitOK(o, y, k, keys[k]); err != nil {
			return nil, err
		}
	}
	ans, _, err := p.edit(ctx, o, r.Seq, r.DryRun, "limits", Impact{Data: "none", Affects: "nobody"}, func(rec *Record) (bool, error) {
		d := rec.Deployments[y]
		if d == nil {
			return false, util.NoDeployment(o.tile, y)
		}
		before := fmt.Sprint(d.Limits)
		for k, v := range keys {
			if v == nil {
				delete(d.Limits, k)
			} else if d.Limits == nil {
				d.Limits = map[string]int64{k: *v}
			} else {
				d.Limits[k] = *v
			}
		}
		if len(d.Limits) == 0 {
			d.Limits = nil
		}
		return fmt.Sprint(d.Limits) != before, nil
	})
	return ans, err
}
