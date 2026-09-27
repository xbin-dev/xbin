package deployments

// events.go — what clients see of a tile's deployments: the operations'
// requests and answers, the deploy entries and log reads, the facts the
// handler's state shows beside the record (11-contract §1.1; the handler
// renders the state in the caller's view, §1.3), and the "deployments"
// event (§3.3) under rule C2: a fact
// about a tile's deployments rides only that type, with the bare tile path;
// today's types carry only the primary's bare reload. Each record or deploy
// event goes out in its full form and, when it concerns the primary, in the
// reader form too; the server's hub filter gives each subscriber exactly one
// of them (internal/server/deployaudience.go).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// DeployFacts is what the plane knows of one deployment's deploys, for the
// state's Deployment.status and lastDeploy (11-contract §1.1), which the
// handler renders in the caller's view: the deploy in flight and those
// waiting, the last finished attempt, the code its generation serves as far
// as this xbind has seen, the last failure.
type DeployFacts struct {
	Serving    string        // "work-tree" or a checkpoint id; "" when unknown
	Error      string        // the last failed attempt's error, at most 500 bytes
	Failed     bool          // the record points at a failed move's attempted checkpoint
	Deploying  *DeployBrief  // the attempt in flight
	Queued     []DeployBrief // those waiting, in order
	LastDeploy *LastDeploy   // the newest finished attempt
}

// DeployBrief is a deploy in flight or waiting.
type DeployBrief struct {
	ID     int64  `json:"id"`
	How    string `json:"how"`
	Result string `json:"result,omitempty"`
	Phase  string `json:"phase,omitempty"`
}

// LastDeploy is a deployment's newest finished attempt.
type LastDeploy struct {
	ID     int64  `json:"id"`
	How    string `json:"how"`
	At     string `json:"at"`
	By     string `json:"by"`
	Result string `json:"result"`
}

// DeployEntry is one deploy attempt, queued, in flight or in the deploy log
// (11-contract §1.1).
type DeployEntry struct {
	ID              int64  `json:"id"`
	Deployment      string `json:"deployment"`
	How             string `json:"how"`
	From            string `json:"from,omitempty"`
	Checkpoint      string `json:"checkpoint,omitempty"`
	Previous        string `json:"previous,omitempty"`
	FollowsWorkTree bool   `json:"followsWorkTree"`
	Feed            string `json:"feed,omitempty"`
	By              string `json:"by"`
	Via             string `json:"via,omitempty"`
	Agent           bool   `json:"agent"`
	Session         string `json:"session,omitempty"`
	RequestedAt     string `json:"requestedAt"`
	FinishedAt      string `json:"finishedAt,omitempty"`
	Result          string `json:"result"`
	Phase           string `json:"phase,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Deploys answers dep's deploy facts. A tile a record doesn't govern has
// none: nothing of an inert store is read (P5).
func (p *Plane) Deploys(ctx context.Context, tile, dep string) DeployFacts {
	var df DeployFacts
	rec, _ := p.record(tile)
	d := (*DeploymentRecord)(nil)
	if rec != nil {
		d = rec.Deployments[dep]
	}
	if d == nil {
		return df
	}
	lastErr, served := p.runtimeOf(tile, dep)
	df.Error, df.Failed = clip(lastErr, maxErrorBytes), d.State == "failed"
	running, waiting := p.laneOf(tile, dep)
	if running != nil {
		df.Deploying = &DeployBrief{ID: running.ID, How: running.How, Result: running.Result, Phase: running.Phase}
	}
	for _, a := range waiting {
		df.Queued = append(df.Queued, DeployBrief{ID: a.ID, How: a.How})
	}
	switch {
	case d.Checkpoint == nil:
		df.Serving = "work-tree"
	case running != nil && !running.Swapped && running.Pointer == pointerRequest:
		df.Serving = "work-tree" // the move off the work tree hasn't swapped yet
	case served != "":
		df.Serving = p.shortOf(ctx, tile, served)
	case d.State != "failed":
		df.Serving = p.shortOf(ctx, tile, *d.Checkpoint)
	}
	if a := p.lastAttempt(ctx, tile, dep); a != nil {
		p.q.mu.Lock()
		df.LastDeploy = &LastDeploy{ID: a.ID, How: a.How, At: a.FinishedAt, By: a.By, Result: a.Result}
		p.q.mu.Unlock()
	}
	return df
}

// Policy is what tile itself may do, whoever asks (the state's allowed
// entries): Allowed, the ship-dark switch, and P18 — without isolation a
// backend is never pinned, so pausing live reload, reloading now, deploying
// and rolling back are refused with kind policy.
func (p *Plane) Policy(op Op, s Subject) Can {
	switch op {
	case OpPause, OpReloadNow, OpDeploy, OpRollback, OpPromote, OpAttach, OpAdd:
		if c, ok := p.component(s.Tile); ok && c.HasBackend() && !p.isIsolated() {
			return Can{Why: isolationMsg, Kind: KindPolicy}
		}
	}
	return p.Allowed(op, s)
}

// shortOf is tree's short id in tile's store (at least 7 digits, unique).
func (p *Plane) shortOf(ctx context.Context, tile, tree string) string {
	if tree == "" {
		return ""
	}
	if rec, _ := p.record(tile); rec == nil {
		return shortTree(tree) // a tile without a record: its store is inert, never read (P5)
	}
	if cp, err := p.store().Get(ctx, tile, tree); err == nil {
		return cp.ID
	}
	return shortTree(tree)
}

// entryOf is a's answer.
func (p *Plane) entryOf(ctx context.Context, a *attempt) DeployEntry {
	p.q.mu.Lock()
	c := *a
	p.q.mu.Unlock()
	return DeployEntry{ID: c.ID, Deployment: c.Deployment, How: c.How, From: c.From,
		Checkpoint: p.shortOf(ctx, c.tile, c.Tree), Previous: p.shortOf(ctx, c.tile, c.Previous),
		FollowsWorkTree: c.FollowsWorkTree, Feed: c.Feed, By: c.By, Via: c.Via, Agent: c.Agent, Session: c.Session,
		RequestedAt: c.RequestedAt, FinishedAt: c.FinishedAt, Result: c.Result, Phase: c.Phase, Error: c.Error}
}

// ---- the deployments event ----

// recordEvent is op record's full form: what changed in the record.
type recordEvent struct {
	Op   string   `json:"op"`
	Seq  int64    `json:"seq"`
	By   string   `json:"by"`
	What []string `json:"what"`
}

// recordReaderEvent is op record's reader form: which fields of the reader
// view changed.
type recordReaderEvent struct {
	Op   string   `json:"op"`
	What []string `json:"what"`
}

// deployEvent is op deploy's full form: a result or phase of one attempt.
type deployEvent struct {
	Op         string `json:"op"`
	ID         int64  `json:"id"`
	Deployment string `json:"deployment"`
	How        string `json:"how"`
	From       string `json:"from,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Result     string `json:"result"`
	Phase      string `json:"phase,omitempty"`
	By         string `json:"by"`
	Session    string `json:"session,omitempty"`
}

// deployReaderEvent is op deploy's reader form, for a deploy onto the
// primary: no id, how, from or session.
type deployReaderEvent struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Result     string `json:"result"`
	Phase      string `json:"phase,omitempty"`
	By         string `json:"by"`
}

// readerFields are the State fields the reader view shows (11-contract §1.3).
var readerFields = map[string]bool{"liveReload": true, "primary": true, "protectedPrimary": true, "deployments": true}

// publishRecord announces a committed change to tile's record: the full form
// with seq and actor, and the reader form naming the reader-visible fields
// that changed. rec is the record after the change (the zero state's after
// an opt-out).
func (p *Plane) publishRecord(tile string, rec *Record, by string, what []string) {
	if p.Hub == nil {
		return
	}
	p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: recordEvent{Op: "record", Seq: rec.Seq, By: by, What: what}})
	var rw []string
	for _, w := range what {
		if readerFields[w] {
			rw = append(rw, w)
		}
	}
	if len(rw) > 0 {
		p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: recordReaderEvent{Op: "record", What: rw}})
	}
}

// publishDeploy announces a's result or phase: the full form, and the reader
// form for a deploy onto the primary.
func (p *Plane) publishDeploy(a *attempt) {
	if p.Hub == nil {
		return
	}
	p.q.mu.Lock()
	c := *a
	p.q.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cp := p.shortOf(ctx, c.tile, c.Tree)
	p.Hub.Publish(events.Event{Type: "deployments", Component: c.tile, Data: deployEvent{Op: "deploy", ID: c.ID,
		Deployment: c.Deployment, How: c.How, From: c.From, Checkpoint: cp, Result: c.Result, Phase: c.Phase,
		By: c.By, Session: c.Session}})
	if c.Deployment == p.Primary(c.tile) {
		p.Hub.Publish(events.Event{Type: "deployments", Component: c.tile, Data: deployReaderEvent{Op: "deploy",
			Deployment: c.Deployment, Checkpoint: cp, Result: c.Result, Phase: c.Phase, By: c.By}})
	}
}

// reloadEvent is op reload: a non-primary deployment's frames reload once.
type reloadEvent struct {
	Op         string `json:"op"`
	Deployment string `json:"deployment"`
}

// publishReload tells dep's open frames its code changed: today's bare
// reload for the primary, op reload for any other deployment (C2).
func (p *Plane) publishReload(tile, dep string) {
	if p.Hub == nil {
		return
	}
	if dep == p.Primary(tile) {
		p.Hub.Publish(events.Event{Type: "reload", Component: tile})
		return
	}
	p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: reloadEvent{Op: "reload", Deployment: dep}})
}

// warn logs a failure the operation outlives.
func warn(what, tile string, err error) {
	slog.Warn("deployments: "+what, "tile", tile, "err", err)
}

// ---- the operations' requests and answers ----

// PauseRequest is POST /deployments/live-reload/pause's body.
type PauseRequest struct {
	Tile   string `json:"tile"`
	Seq    *int64 `json:"seq,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// ResumeRequest is POST /deployments/live-reload/resume's body.
type ResumeRequest struct {
	Tile       string `json:"tile"`
	Deployment string `json:"deployment,omitempty"`
	Seq        *int64 `json:"seq,omitempty"`
	DryRun     bool   `json:"dryRun,omitempty"`
}

// ReloadNowRequest is POST /deployments/live-reload/now's body.
type ReloadNowRequest struct {
	Tile   string `json:"tile"`
	Expect string `json:"expect,omitempty"`
	Seq    *int64 `json:"seq,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// DeployRequest is POST /deployments/deploy's body.
type DeployRequest struct {
	Tile       string `json:"tile"`
	Deployment string `json:"deployment,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Expect     string `json:"expect,omitempty"`
	Restart    bool   `json:"restart,omitempty"`
	Seq        *int64 `json:"seq,omitempty"`
	DryRun     bool   `json:"dryRun,omitempty"`
}

// RollbackRequest is POST /deployments/rollback's body.
type RollbackRequest struct {
	Tile       string `json:"tile"`
	Deployment string `json:"deployment,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Seq        *int64 `json:"seq,omitempty"`
	DryRun     bool   `json:"dryRun,omitempty"`
}

// Answer is a committed operation's answer (11-contract §1.2): the deploy
// it queued or ran, or unchanged when the deployment already runs that code.
// The handler adds the state.
type Answer struct {
	Deploy    *DeployEntry `json:"deploy,omitempty"`
	Unchanged bool         `json:"unchanged,omitempty"`
}

// DryRunAnswer is a dry run's answer: what the operation would do. The
// handler adds the state as it stands.
type DryRunAnswer struct {
	Impact Impact `json:"impact"`
}

// Impact is what an operation would do (11-contract §1.1), rendered by the
// confirmation dialog and bx's prompt.
type Impact struct {
	Code             *CodeImpact `json:"code"`
	Data             string      `json:"data"`
	PausesLiveReload bool        `json:"pausesLiveReload"`
	Stops            []string    `json:"stops"`
	Affects          string      `json:"affects"`
	Reloads          []string    `json:"reloads"`
}

// CodeImpact is the code an operation moves: null when no capture was taken
// (a tile without a record: the code is the work tree as it is when the
// request commits).
type CodeImpact struct {
	Deployment string `json:"deployment"`
	From       string `json:"from"` // a checkpoint id, or "work-tree"
	To         string `json:"to"`   // a checkpoint id, or "work-tree"
	WorkTreeAt string `json:"workTreeAt,omitempty"`
}

// ---- answers and errors ----

// answer is an operation's answer: the attempt it ran, or the dry run's
// impact. The handler adds the caller's state as it stands after it, in the
// caller's view (11-contract §1.2, §1.3).
func (p *Plane) answer(ctx context.Context, dry bool, a *attempt, im Impact, unchanged bool) (any, error) {
	if dry {
		if im.Data == "" {
			im.Data, im.Affects = "none", "nobody"
		}
		if im.Stops == nil {
			im.Stops = []string{}
		}
		if im.Reloads == nil {
			im.Reloads = []string{}
		}
		return DryRunAnswer{Impact: im}, nil
	}
	ans := Answer{Unchanged: unchanged}
	if a != nil {
		e := p.entryOf(ctx, a)
		ans.Deploy = &e
	}
	return ans, nil
}

// affects is who sees a change to dep's code: the primary's viewers, or a
// non-primary deployment's.
func affects(r *Record, dep string) string {
	if dep == r.Primary {
		return "everyone"
	}
	return "deployment"
}

// parseExpect validates an expect: the checkpoint id's hex prefix, "" when
// none is sent.
func parseExpect(expect string) (string, error) {
	if expect == "" {
		return "", nil
	}
	prefix, err := checkpoint.ParseID(expect)
	if err != nil {
		return "", badRequest(err.Error())
	}
	return prefix, nil
}

func badRequest(msg string) error { return &Error{Status: http.StatusBadRequest, Msg: msg} }

// confirmed checks a confirm token that guards data (11-contract §1.2,
// NP-11-5): a missing or wrong one answers 400 naming the consequence and
// the token. The operations that take one (remove, reset, seed, primary)
// call it before anything changes.
func confirmed(got, want, consequence string) error {
	if got == want {
		return nil
	}
	return badRequest(fmt.Sprintf("%s: send confirm:%q to proceed", consequence, want))
}

func queueFull(dep string) error {
	return &Error{Status: http.StatusConflict, Kind: KindState, Msg: dep + " already has 8 deploys waiting; try again when one finishes"}
}

func notPaused(attached, what string) error {
	return &Error{Status: http.StatusConflict, Kind: KindState, Msg: "live reload is attached to " + attached + "; " + what + " works while it is paused"}
}

func expectMismatch(expect, actual string) error {
	return &Error{Status: http.StatusConflict, Kind: KindState, Msg: "the code changed since you reviewed " + expect + " (now " + actual + ")"}
}

// errMoved is a record that moved between an operation's read and its
// commit in a way the operation can't apply over.
var errMoved = errors.New("the deployments changed meanwhile")

func moved(tile string) error {
	return fmt.Errorf("the deployments of %s changed meanwhile; reload and retry: %w", tile, errMoved)
}

// opError is err as the *Error an operation answers (11-contract §1.14).
func opError(tile string, err error) error {
	var e *Error
	var rl *checkpoint.RateLimited
	switch {
	case err == nil:
		return nil
	case errors.As(err, &e):
		return e
	case errors.Is(err, ErrStaleSeq), errors.Is(err, ErrRecordHeld), errors.Is(err, ErrRecordInert), errors.Is(err, errMoved):
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: err.Error()}
	case errors.As(err, &rl):
		return &Error{Status: http.StatusTooManyRequests, Kind: KindPolicy, Msg: rl.Error()}
	case errors.Is(err, checkpoint.ErrRefused):
		return &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: err.Error()}
	case errors.Is(err, checkpoint.ErrBadID):
		return &Error{Status: http.StatusBadRequest, Msg: err.Error()}
	case errors.Is(err, checkpoint.ErrUnknownCheckpoint), errors.Is(err, util.ErrNoDeployment):
		return &Error{Status: http.StatusNotFound, Msg: err.Error()}
	case errors.Is(err, checkpoint.ErrAmbiguousID):
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: err.Error()}
	case errors.Is(err, checkpoint.ErrNoStore):
		return &Error{Status: http.StatusConflict, Kind: KindState, Msg: tile + " has no deployments yet: pause live reload or add a deployment first"}
	}
	return &Error{Status: http.StatusInternalServerError, Msg: err.Error()}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- the log's readers ----

// Entry answers attempt id of tile: queued and in-flight ones from memory,
// finished ones from memory or the deploy log. wait (at most 25 s) holds the
// answer until the attempt finishes or the wait runs out. An id the tile
// never had is util.ErrNoDeployment's 404 sibling, ErrNoAttempt.
func (p *Plane) Entry(ctx context.Context, tile string, id int64, wait time.Duration) (DeployEntry, error) {
	a, err := p.findAttempt(ctx, tile, id)
	if err != nil {
		return DeployEntry{}, err
	}
	if wait > 25*time.Second {
		wait = 25 * time.Second
	}
	if wait > 0 && a.done != nil {
		t := time.NewTimer(wait)
		select {
		case <-a.done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	return p.entryOf(ctx, a), nil
}

// ErrNoAttempt is a deploy id the tile doesn't have (a 404).
var ErrNoAttempt = errors.New("no such deploy attempt")

func (p *Plane) findAttempt(ctx context.Context, tile string, id int64) (*attempt, error) {
	for _, a := range p.attempts(ctx, tile, "") {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%s has no deploy %d: %w", tile, id, ErrNoAttempt)
}

// Log answers tile's deploy log, newest first: dep's entries, or every
// deployment's (""); at most limit (default 50, at most 200), older than
// before when it is set; more reports that older ones exist.
func (p *Plane) Log(ctx context.Context, tile, dep string, limit int, before int64) (entries []DeployEntry, more bool) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	for _, a := range p.attempts(ctx, tile, dep) {
		if before > 0 && a.ID >= before {
			continue
		}
		if len(entries) == limit {
			return entries, true
		}
		entries = append(entries, p.entryOf(ctx, a))
	}
	return entries, false
}

// attempts is tile's attempts, newest first: those in memory (queued, in
// flight, recently finished, journaled) and the deploy log's, dep's or
// every deployment's (""). The memory's copy of an attempt wins. A tile a
// record doesn't govern has none.
func (p *Plane) attempts(ctx context.Context, tile, dep string) []*attempt {
	if rec, _ := p.record(tile); rec == nil {
		return nil // a tile without a record has no deploy log to read: its store is inert (P5)
	}
	seen := map[int64]bool{}
	var out []*attempt
	p.q.mu.Lock()
	if t := p.q.tiles[tile]; t != nil {
		for _, list := range [][]*attempt{t.open, t.recent} {
			for _, a := range list {
				if !seen[a.ID] && (dep == "" || a.Deployment == dep) {
					seen[a.ID] = true
					out = append(out, a)
				}
			}
		}
	}
	for _, l := range p.q.lanes {
		for _, a := range append([]*attempt{l.running}, l.waiting...) {
			if a != nil && a.tile == tile && !seen[a.ID] && (dep == "" || a.Deployment == dep) {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}
	p.q.mu.Unlock()
	if logged, err := p.store().ReadLog(ctx, tile, dep); err == nil {
		for i := range logged {
			a := logged[i]
			if !seen[a.ID] {
				seen[a.ID] = true
				a.tile = tile
				out = append(out, &a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// lastAttempt is dep's newest finished attempt, nil when it has none.
func (p *Plane) lastAttempt(ctx context.Context, tile, dep string) *attempt {
	for _, a := range p.attempts(ctx, tile, dep) {
		p.q.mu.Lock()
		done := a.finished()
		p.q.mu.Unlock()
		if done {
			return a
		}
	}
	return nil
}

// rollbackTarget is the checkpoint of dep's newest ok deploy-log entry that
// differs from its current code; "" when there is none.
func (p *Plane) rollbackTarget(ctx context.Context, tile, dep string, d *DeploymentRecord) string {
	cur := ""
	if d.Checkpoint != nil {
		cur = *d.Checkpoint
	}
	for _, a := range p.attempts(ctx, tile, dep) {
		p.q.mu.Lock()
		ok, tree := a.Result == resultOK, a.Tree
		p.q.mu.Unlock()
		if ok && tree != "" && tree != cur {
			return tree
		}
	}
	return ""
}
