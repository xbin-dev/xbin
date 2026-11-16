package server

// delegate.go — the delegate gate (docs/auth.md §Delegation; D228): the one
// place a delegate credential (auth/delegate.go) is honoured. authedAPI
// hands every /api/ request that carries the delegate scheme here, before
// any other credential on it is looked at; no other plane resolves the
// scheme, so there a delegate is nobody (401).
//
// For each request the gate, in order:
//
//  1. resolves the person (auth.DelegateFromRequest: the gateway socket,
//     the sealed holder, the live person, the off switches) — 401 for a
//     credential that delegates nothing, 403 with the reason when
//     delegation is off;
//  2. classifies it (delegateVerdict): a row of delegateClasses for
//     /api/xbin/*, the tile class for /api/<tile>/…; refused otherwise —
//     path tickets, WebSocket upgrades, never-listed, refused and unlisted
//     routes;
//  3. asks the rules of the tile the request is about: the tile's org's
//     agent rules for a read xbind serves (delegation.Policy.TileAllows),
//     the broker's whole rule for a tile call (DelegatePolicy.DelegateTile:
//     the person's level, the tile's manifest, the policy ceiling, the
//     org's rules); an admin read needs the person's opt-in;
//  4. answers a dry run (X-XBin-Dry-Run: 1) with what the call would be,
//     running nothing;
//  5. limits a person's acts per minute;
//  6. for a side effect that needs it, requires the PERSON's approval, kept
//     by xbind (delegation/requests.go): a one-shot ticket (X-XBin-Ticket)
//     bound to this method, path and body; a standing approval of the
//     tile's API; or, for a session start, the person's preference. So a
//     credential stolen from the agent's sandbox is read-only;
//  7. runs the call as the person's principal through the handlers everyone
//     uses (handleAPI: the deployment and partition gates, the audit line;
//     the component proxy: the broker's Route), under a context the
//     person's pause cancels; and
//  8. writes the row of the person's audit log: every act; tile reads,
//     refusals, dry runs and audited reads up to a rate, the rest counted
//     into the next row (an agent in a loop can't grow the log) — not the
//     listing reads a UI makes.
//
// A call that runs long is checked again every few seconds, and at once
// when people or tiles change (SweepDelegation): it ends when its person is
// disabled or deleted, loses the tile, pauses, or revokes the standing
// approval it ran under.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
)

// Headers of a delegate call. Only the gate reads them; the proxy strips
// them like every X-XBin-* before a backend sees the request.
const (
	// HeaderDryRun asks what a call would do instead of making it.
	HeaderDryRun = "X-XBin-Dry-Run"
	// HeaderTicket names the approved request a side effect runs under.
	HeaderTicket = "X-XBin-Ticket"
	// HeaderAgentModel is the model the agent says its conversation is on,
	// for the orgs' model rules (auth.Principal.AgentModel).
	HeaderAgentModel = "X-XBin-Agent-Model"
)

// DelegatePolicy is a Policy that answers what a delegate's call to a tile
// carries (the broker's DelegateTile): the level its person holds on the
// tile, the role the call gets and whether the tile lets agents read only —
// or why it is refused. Without it (the broker-less server of tests) a tile
// call is refused.
type DelegatePolicy interface {
	DelegateTile(p auth.Principal, tile string, write bool) (level, role string, readOnly bool, err error)
}

// agentPolicy is the installed agent policy, or the one that refuses.
func (s *Server) agentPolicy() delegation.Policy { return delegation.PolicyOr(s.AgentPolicy) }

// delegateAuditLog is the audit log, nil without a store.
func (s *Server) delegateAuditLog() *delegation.Log {
	if s.Delegation == nil {
		return nil
	}
	return s.Delegation.Audit
}

// DelegationGate is the off switches boot installs with
// auth.SetDelegationGate: delegation runs only with sandboxes on (isolated;
// without them a sibling tile reads the holder's token from its
// environment — open is the --dev / --no-auth exception), while the
// workspace's switch is on and the person hasn't paused their agent.
func DelegationGate(store *delegation.Store, isolated, open bool) func(component, userID string) error {
	return func(_, userID string) error {
		switch {
		case !isolated && !open:
			return &OffError{"no-sandbox", "this xbind runs tiles without sandboxes (--isolate is off): a workspace agent's credential isn't safe here"}
		case !store.Workspace().Enabled:
			return &OffError{"workspace-off", "an admin hasn't turned workspace agents on for this workspace"}
		case store.State(userID).Paused:
			return &OffError{"paused", "you paused it — resume it in your agent's settings"}
		}
		return nil
	}
}

// OffError is why delegation is off for a person now; Code is the refusal
// the gate answers ("paused", "workspace-off", "no-sandbox"), so an agent
// can tell its person paused it from the workspace having none.
type OffError struct{ Code, Msg string }

func (e *OffError) Error() string { return e.Msg }

// ---- classification ----

// delegateVerdict is the gate's judgement of one request.
type delegateVerdict struct {
	class   string // the audit row's class
	pattern string // the /api/xbin route, "" for a tile call
	row     delegateRow
	tileAPI bool   // /api/<tile>/…
	effect  bool   // a side effect
	audit   bool   // a read the audit log keeps
	tile    string // the tile the request is about, when the gate knows it
	deny    string // the refusal; "" when the call may go on
}

const (
	classTileRead  = "tile-read"
	classTileWrite = "tile-write"
)

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// delegateVerdict classifies r for delegate p.
func (s *Server) delegateVerdict(r *http.Request, p auth.Principal) delegateVerdict {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/")
	switch {
	case !ok:
		return delegateVerdict{class: DelegateUnlisted.String(), deny: "an agent reaches xbind's API and tiles' APIs only"}
	case isPathTicket(r):
		return delegateVerdict{class: DelegateNever.String(), deny: neverIdentity}
	case r.Header.Get("Upgrade") != "":
		return delegateVerdict{class: DelegateUnlisted.String(), deny: "an agent makes plain requests: a socket would outlive the person pausing it"}
	}
	if rest == "xbin" || strings.HasPrefix(rest, "xbin/") {
		r2 := r.Clone(r.Context())
		if !xbinInner(r.URL, r2.URL) || dotSegment(r2.URL.Path) || s.apiMux == nil {
			return delegateVerdict{class: DelegateUnlisted.String(), deny: "no such route"}
		}
		_, pattern := s.apiMux.Handler(r2)
		row := delegateClasses[pattern]
		v := delegateVerdict{class: row.class.String(), pattern: pattern, row: row, effect: row.class == DelegateAct, audit: row.audit}
		switch {
		case pattern == "":
			v.deny = "no such route" // nothing would run; the agent learns nothing a 404 wouldn't say
		case row.class == DelegateNever, row.class == DelegateRefused:
			v.deny = row.why
		case !row.class.Allowed():
			v.deny = "this route isn't delegated to a person's agent"
		case delegateUnconverted[pattern] != "":
			v.deny = "this route isn't available to a person's agent yet (" + delegateUnconverted[pattern] + ")"
		case row.tile != nil:
			v.tile = row.tile(s, p, r, strings.Trim(r2.URL.Path, "/"))
		}
		return v
	}
	v := delegateVerdict{class: classTileRead, tileAPI: true, audit: true}
	if !isRead(r.Method) { // OPTIONS and the rest: a tile decides what a method does
		v.class, v.effect = classTileWrite, true
	}
	if s.Reg != nil {
		if comp, _, _, _, _ := s.Reg.ResolveRef(rest, nil); comp != nil {
			v.tile = comp.Path
		}
	}
	// no tile at the path: answered like a tile the person can't read
	// (noTileFor), never as "no such tile"
	return v
}

// noTileFor is the gate's one answer for a tile a person's agent may not
// name: it isn't there, or the person can't read it. The two are the same
// refusal, status and words — an agent that could tell them apart would
// map the workspace's tiles by asking — and neither names the tile.
func noTileFor(user string) string {
	return "nothing here for " + user + "'s agent: it reaches only tiles " + user + " can read"
}

// tileHidden reports a tile a delegate's request names that p's agent may
// not know of: none resolved, none registered, or one p can't read.
func (s *Server) tileHidden(p auth.Principal, tile string) bool {
	return tile == "" || !s.tileExists(tile) || !p.CanReadTile(tile)
}

// ---- the gate's own state: the rate limit and the running calls ----

// Acts a person's agent may make: a burst, refilled over a minute. Enough
// for a busy conversation, too few to flood a tile or a person's approvals.
const (
	delegateActBurst  = 30
	delegateActRefill = 2 * time.Second // one more act
)

type delegateRuntime struct {
	mu      sync.Mutex
	buckets map[string]*actBucket
	noise   map[string]*noiseBucket
	web     map[string]*actBucket // a person's web fetches (delegateweb.go)
	calls   map[string]map[uint64]*runningCall
	nextID  uint64
	pushed  map[string]time.Time // person + kind → the last "something waits" push
	now     func() time.Time     // tests (setClock)
	recheck time.Duration        // tests (setRecheck); 0: delegateRecheck
}

// setClock and setRecheck are the tests' knobs, set under the lock the
// gate reads them under.
func (d *delegateRuntime) setClock(now func() time.Time) {
	d.mu.Lock()
	d.now = now
	d.mu.Unlock()
}

func (d *delegateRuntime) setRecheck(every time.Duration) {
	d.mu.Lock()
	d.recheck = every
	d.mu.Unlock()
}

// delegatePushEvery spaces the "your agent waits for you" pushes of one
// kind to one person: requests that pile up are one notification (the
// same collapse key, a count), not one each.
const delegatePushEvery = time.Minute

// pushDue reports whether user is due a push of kind now, and notes it.
func (d *delegateRuntime) pushDue(user, kind string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now, key := d.clock(), user+"\x00"+kind
	if last, ok := d.pushed[key]; ok && now.Sub(last) < delegatePushEvery {
		return false
	}
	if d.pushed == nil {
		d.pushed = map[string]time.Time{}
	}
	for k, at := range d.pushed { // bounded by the people who were pushed to this minute
		if now.Sub(at) >= delegatePushEvery {
			delete(d.pushed, k)
		}
	}
	d.pushed[key] = now
	return true
}

func (d *delegateRuntime) recheckEvery() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.recheck > 0 {
		return d.recheck
	}
	return delegateRecheck
}

type actBucket struct {
	tokens float64
	at     time.Time
}

// noiseBucket bounds the audit rows a person's agent writes for what isn't
// an act: refusals, dry runs, reads. Past the rate they are counted, and
// the next row written says how many were left out.
type noiseBucket struct {
	actBucket
	left int
}

// Audit rows for non-acts: a burst, then one a second.
const (
	delegateNoiseBurst  = 60
	delegateNoiseRefill = time.Second
)

// runningCall is a delegate call in flight: cancel ends it, still says
// whether it may go on ("" yes, else why not).
type runningCall struct {
	cancel context.CancelFunc
	still  func() string
}

func (d *delegateRuntime) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (b *actBucket) take(now time.Time, burst float64, refill time.Duration) (ok bool, wait time.Duration) {
	if b.at.IsZero() {
		b.tokens = burst
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.at).Seconds()/refill.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(refill))
	}
	b.tokens--
	return true, 0
}

// allowAct takes one act from user's bucket; wait is how long until the
// next one when it is empty.
func (d *delegateRuntime) allowAct(user string) (ok bool, wait time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.buckets == nil {
		d.buckets = map[string]*actBucket{}
	}
	b := d.buckets[user]
	if b == nil {
		b = &actBucket{}
		d.buckets[user] = b
	}
	return b.take(d.clock(), delegateActBurst, delegateActRefill)
}

// allowNoise reports whether a non-act row of user's is written now; left
// is how many were left out since the last one written.
func (d *delegateRuntime) allowNoise(user string) (ok bool, left int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.noise == nil {
		d.noise = map[string]*noiseBucket{}
	}
	b := d.noise[user]
	if b == nil {
		b = &noiseBucket{}
		d.noise[user] = b
	}
	if ok, _ := b.take(d.clock(), delegateNoiseBurst, delegateNoiseRefill); !ok {
		b.left++
		return false, 0
	}
	left, b.left = b.left, 0
	return true, left
}

// track registers a running delegate call of user.
func (d *delegateRuntime) track(user string, c *runningCall) (done func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls == nil {
		d.calls = map[string]map[uint64]*runningCall{}
	}
	if d.calls[user] == nil {
		d.calls[user] = map[uint64]*runningCall{}
	}
	d.nextID++
	id := d.nextID
	d.calls[user][id] = c
	return func() {
		d.mu.Lock()
		delete(d.calls[user], id)
		if len(d.calls[user]) == 0 {
			delete(d.calls, user)
		}
		d.mu.Unlock()
	}
}

// running lists user's calls in flight (all people's when user is "").
func (d *delegateRuntime) running(user string) []*runningCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*runningCall
	for u, m := range d.calls {
		if user == "" || u == user {
			for _, c := range m {
				out = append(out, c)
			}
		}
	}
	return out
}

// CutDelegateStreams cancels the delegate calls running now for user — all
// people's when user is "": what pausing does at once (a session's event
// stream an agent follows would otherwise outlive the pause).
func (s *Server) CutDelegateStreams(user string) int {
	calls := s.delegateRT.running(user)
	for _, c := range calls {
		c.cancel()
	}
	return len(calls)
}

// delegateRecheck is how often a delegate call in flight is asked whether
// it may still run. A constant: what tests change is a field of their own
// server's delegateRuntime (setRecheck, setClock), under its lock — never
// a package variable a gate goroutine of another test could be reading.
const delegateRecheck = 3 * time.Second

// DelegationOff is why user's agent holder may not act now ("" nil: it
// may) — the gate's off switches (paused, the workspace's switch, no
// sandbox), for callers that aren't the delegate gate: the LLM connector
// asks it before a holder's model call, so a paused or switched-off agent
// runs no model turn either.
func (s *Server) DelegationOff(holder, user string) error {
	if s.delegationGate == nil {
		return nil
	}
	return s.delegationGate(holder, user)
}

// SetDelegationGate installs the off-switch check DelegationOff asks (boot:
// the same function the auth layer's delegate resolver uses).
func (s *Server) SetDelegationGate(gate func(holder, user string) error) { s.delegationGate = gate }

func (s *Server) tileExists(tile string) bool {
	if s.Reg == nil {
		return true
	}
	_, ok := s.Reg.Component(tile)
	return ok
}

// ---- what the gate tells the handlers ----

type delegateStartKey struct{}
type delegateAdminReadKey struct{}

// delegateStartOf is how the gate authorised a delegate's session start
// (term.DelegateStart); nil for every other request.
func delegateStartOf(r *http.Request) *term.DelegateStart {
	d, _ := r.Context().Value(delegateStartKey{}).(*term.DelegateStart)
	return d
}

// IsDelegateAdminRead reports a request the gate runs as an admin read for
// a person's agent (DelegateAdminRead): the handler answers the person's own
// view, less what an agent has no use for (an org's SSO group rules).
func IsDelegateAdminRead(r *http.Request) bool {
	v, _ := r.Context().Value(delegateAdminReadKey{}).(bool)
	return v
}

// delegateHides reports whether a listing leaves tile out for p: p is a
// delegate, and the tile's org's agent rules don't let the agent in (on the
// model it is on). Everyone else sees every row they always did.
func (s *Server) delegateHides(p auth.Principal, tile string) bool {
	if p.Agent == "" {
		return false
	}
	if h, ok := s.handleOf(p); ok && !s.inSessionScope(h, tile) {
		return true // hidden by the person: outside the conversation's scope
	}
	if s.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}) != nil {
		return true
	}
	s.taint(p, tile) // a row that is shown is that tile's orgs' data in the conversation
	return false
}

// DelegateHides is delegateHides for the listings other packages serve.
func (s *Server) DelegateHides(p auth.Principal, tile string) bool { return s.delegateHides(p, tile) }

// ---- the gate ----

func delegateRefuse(w http.ResponseWriter, status int, refusal, class, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg, "refusal": refusal, "class": class, "docs": "/docs/auth.md"})
}

// agentModel is the model a delegate call is judged on: the model of the
// agent session (handle) the call runs under — xbind's own record, which
// the LLM connector enforces for the holder's calls too. This function is
// the ONE source of auth.Principal.AgentModel and so of
// delegation.Subject.Model. No request header names a model any more
// (X-XBin-Agent-Model is ignored); a call without a handle has no model,
// which only an org with no model rule allows.
func agentModel(h delegation.AgentSession) string { return h.Model }

// requestTarget is what a ticket binds a request by: its path as sent,
// with its query.
func requestTarget(r *http.Request) string {
	t := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		t += "?" + r.URL.RawQuery
	}
	return t
}

// serveDelegate is the gate. next is the /api/ handler (handleAPI).
func (s *Server) serveDelegate(w http.ResponseWriter, r *http.Request, next http.Handler) {
	p, _, err := s.Auth.DelegateFromRequest(r)
	if err != nil {
		if errors.Is(err, auth.ErrDelegateOff) {
			code, why := "off", "delegation isn't set up on this xbind"
			if off := (*OffError)(nil); errors.As(err, &off) {
				code, why = off.Code, off.Msg
			}
			delegateRefuse(w, http.StatusForbidden, code, "", "your workspace agent is off: "+why)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized) // delegates nothing: as any bad token
		return
	}
	// the handle: xbind's record of this conversation's scope and model.
	// The model the orgs' rules are asked with is the handle's — never
	// something the request says (agentModel).
	handle, hasHandle := s.delegateHandle(r, p)
	p.AgentModel, p.AgentSession = agentModel(handle), handle.ID
	recheck := s.Auth.DelegateRecheck(r)      // before any handler strips the credential
	for _, h := range methodOverrideHeaders { // the method judged is the method made
		r.Header.Del(h)
	}
	v := s.delegateVerdict(r, p)
	if r.URL.Query().Has("_method") && v.deny == "" {
		v.deny = "a call that names another method in its query (_method) isn't made for a person: the method judged must be the method made"
	}
	row := delegation.Call{User: p.UserID, Agent: p.Agent, Method: r.Method, Path: r.URL.Path, Tile: v.tile, Class: v.class, Effect: v.effect, Session: handle.ID}
	refuse := func(status int, refusal, msg string) {
		row.Status, row.Refused = status, msg
		s.delegateAudit(row)
		delegateRefuse(w, status, refusal, v.class, msg)
	}
	if v.deny != "" {
		refuse(http.StatusForbidden, "not-delegated", v.deny)
		return
	}
	if !hasHandle && !delegateBootstrap[v.pattern] {
		refuse(http.StatusForbidden, "no-session", "this call needs an agent session: make one (POST /api/xbin/delegate/sessions {scope, model}) and send its id as "+HeaderAgentSession+" — it says what this conversation may see and which model it is on")
		return
	}
	dry := r.Header.Get(HeaderDryRun) != ""
	preview := map[string]any{"dryRun": true, "class": v.class, "as": p.UserID, "agent": p.Agent, "sideEffect": v.effect}

	// 3. the rules of the tile the request is about
	run := p // the principal the handler sees
	switch {
	case v.tileAPI:
		dp, ok := s.policy().(DelegatePolicy)
		if !ok {
			refuse(http.StatusForbidden, "not-delegated", "this xbind has no tile routing for a person's agent")
			return
		}
		if s.tileHidden(p, v.tile) { // before anything is said about the tile, a dry run included
			row.Tile = ""
			refuse(http.StatusForbidden, "tile", noTileFor(p.UserID))
			return
		}
		if !s.inSessionScope(handle, v.tile) {
			refuse(http.StatusForbidden, "hidden", hiddenByScope(p.UserID))
			return
		}
		level, role, readOnly, err := dp.DelegateTile(p, v.tile, v.effect)
		if err != nil {
			if dry {
				preview["tile"], preview["allowed"], preview["refusal"] = v.tile, false, err.Error()
				s.writeDryRun(w, &row, preview)
				return
			}
			refuse(http.StatusForbidden, "tile", err.Error())
			return
		}
		preview["tile"], preview["owner"], preview["level"], preview["role"] = v.tile, s.policy().OwnerOf(v.tile), level, role
		if readOnly {
			preview["agentAccess"] = "read"
		}
	case v.row.class == DelegateAdminRead:
		st := s.Delegation.State(p.UserID)
		admin := p.User != nil && p.User.IsAdmin() || p.Access != nil && len(p.Access.AdminOrgs()) > 0
		if !admin || !st.AdminChanges {
			refuse(http.StatusForbidden, "admin-read", "an agent reads admin state only for an admin who let it propose admin changes (their agent's settings)")
			return
		}
		run.Agent, run.AgentModel = "", "" // the person's own view of this one GET (delegateclass.go)
	case v.tile != "":
		if s.tileHidden(p, v.tile) { // at the person's level, whatever the handler asks
			row.Tile = ""
			refuse(http.StatusForbidden, "tile", noTileFor(p.UserID))
			return
		}
		if hasHandle && !s.inSessionScope(handle, v.tile) {
			refuse(http.StatusForbidden, "hidden", hiddenByScope(p.UserID))
			return
		}
		if err := s.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: v.tile, Model: p.AgentModel}); err != nil {
			refuse(http.StatusForbidden, "tile", err.Error())
			return
		}
		preview["tile"], preview["owner"] = v.tile, s.policy().OwnerOf(v.tile)
	}

	needsApproval := v.effect && (v.tileAPI || v.row.ticket)
	if v.effect {
		preview["reversible"] = false // xbind undoes nothing a tile or a tile agent did
		preview["needsApproval"] = needsApproval
	}
	if dry {
		if needsApproval && v.tileAPI {
			if a, ok := s.Delegation.Covers(p.UserID, v.tile, r.Method, r.URL.EscapedPath()); ok {
				preview["needsApproval"], preview["under"] = false, "approval:"+a.ID
			}
		}
		preview["allowed"] = true
		if v.pattern == webFetchPattern && hasHandle { // what this fetch would need now: the reach, the URL (delegateweb.go)
			body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
			switch why, asks := s.webDry(p, handle, body); {
			case why != "":
				preview["allowed"], preview["refusal"] = false, why
			case asks:
				preview["needsApproval"] = true
			}
		}
		if v.pattern == "POST /term/sessions" { // what THIS start would need: the person's preference may start it unasked
			body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, delegation.MaxBody))
			ask := sessionAskOf(body)
			row.Tile, preview["tile"] = ask.Cwd, ask.Cwd
			if _, why := s.startRefusal(p, ask); why != "" {
				preview["allowed"], preview["refusal"] = false, why
			} else if s.startsUnasked(p, ask) {
				preview["needsApproval"], preview["under"] = false, "preference"
			}
		}
		s.writeDryRun(w, &row, preview)
		return
	}

	// 5. the rate limit on acts
	if v.effect {
		if ok, wait := s.delegateRT.allowAct(p.UserID); !ok {
			secs := int(wait/time.Second) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			refuse(http.StatusTooManyRequests, "rate-limited", fmt.Sprintf("your agent is acting too fast (%d acts a minute at most): it may try again in %d s", int(time.Minute/delegateActRefill), secs))
			return
		}
	}

	// 6. the person's approval
	ctx := r.Context()
	if needsApproval {
		start, status, refusal, msg := s.delegateApproval(w, r, p, v, &row)
		if msg != "" {
			refuse(status, refusal, msg)
			return
		}
		if start != nil {
			ctx = context.WithValue(ctx, delegateStartKey{}, start)
		}
	}
	if v.row.class == DelegateAdminRead {
		ctx = context.WithValue(ctx, delegateAdminReadKey{}, true)
	}
	ctx = context.WithValue(ctx, delegateRowKey{}, &row) // a handler that knows more of the call says it in the row (delegateRowOf)

	// 7. run it, under a context that ends when it may no longer run: the
	// call is registered FIRST and judged again after, so a pause that lands
	// between the gate's check and here still cuts it
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	still := s.delegateStill(recheck, p, v, row.Under)
	defer s.delegateRT.track(p.UserID, &runningCall{cancel: cancel, still: still})()
	if why := still(); why != "" {
		refuse(http.StatusForbidden, "off", why)
		return
	}
	go func() { // a call that runs long is asked again
		tk := time.NewTicker(s.delegateRT.recheckEvery())
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if still() != "" {
					cancel()
					return
				}
			}
		}
	}()
	if v.tileAPI { // the tile is sent the headers on the list and no other (delegateForwards)
		for h := range r.Header {
			if !delegateForwards[h] {
				r.Header.Del(h)
			}
		}
	}
	s.taint(p, v.tile) // the conversation has touched this tile's orgs' data, reads included
	dw := &delegateWriter{ResponseWriter: w, status: http.StatusOK, tile: v.tileAPI}
	next.ServeHTTP(dw, r.WithContext(auth.WithPrincipal(ctx, run)))
	row.Status = dw.status
	if v.effect || v.audit {
		s.delegateAudit(row)
	}
}

// methodOverrideHeaders let a request say it is another method than the
// one it is made with; frameworks behind a tile's API honour them. A
// delegate call is judged — read or write, ticket or none — by its method,
// so none of them reaches a handler.
var methodOverrideHeaders = []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override"}

// delegateForwards is every request header a delegate's call to a tile's
// API carries to the tile (canonical names); xbind then adds its own
// X-XBin-* attribution. Everything else the agent sent is dropped. A person
// approves a method, a path, a query, a body and a Content-Type: a header a
// tile could act on — a cookie, an origin, a forwarded address, a
// framework's X-… switch, a Content-Encoding that changes how the approved
// bytes are read — would be a part of the call nobody was shown. What is
// left negotiates the answer (Accept…), conditions the call without
// widening it (If-…, Range) or names the body that was approved.
var delegateForwards = map[string]bool{
	"Accept": true, "Accept-Encoding": true, "Accept-Language": true,
	"Content-Type": true, "Content-Length": true,
	"If-Match": true, "If-None-Match": true, "If-Modified-Since": true, "If-Unmodified-Since": true, "Range": true,
	"Cache-Control": true, "User-Agent": true,
}

// delegateStill answers whether a call the gate let through may still run
// ("" yes, else why not): its credential still resolves (the person lives,
// holds their agent, hasn't paused; delegation is on), the tile it is about
// still lets this person's agent in, and the standing approval it ran
// under still holds.
func (s *Server) delegateStill(recheck func() (auth.Principal, error), was auth.Principal, v delegateVerdict, under string) func() string {
	return func() string {
		if recheck == nil {
			return "the credential is gone"
		}
		p, err := recheck()
		if err != nil {
			return "delegation ended for this person"
		}
		p.AgentModel = was.AgentModel
		switch {
		case v.tileAPI:
			if dp, ok := s.policy().(DelegatePolicy); ok {
				if _, _, _, err := dp.DelegateTile(p, v.tile, v.effect); err != nil {
					return err.Error()
				}
			}
		case v.tile != "" && v.row.class != DelegateAdminRead:
			if !p.CanReadTile(v.tile) {
				return p.UserID + " can no longer read " + v.tile
			}
			if err := s.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: v.tile, Model: p.AgentModel}); err != nil {
				return err.Error()
			}
		}
		if id, ok := strings.CutPrefix(under, "approval:"); ok {
			aps, _ := s.Delegation.Approvals(p.UserID)
			if !slices.ContainsFunc(aps, func(a delegation.Approval) bool { return a.ID == id }) {
				return "the standing approval it ran under was revoked"
			}
		}
		return ""
	}
}

// contentTypeOf is a request's Content-Type in one spelling — the media
// type lower-cased and its parameters in order ("" none): what a ticket
// binds beside the body. The whole of it: "application/json" and
// "application/json; charset=utf-7" are different calls.
func contentTypeOf(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	mt, params, err := mime.ParseMediaType(header)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(header)) // not one: it must still be the same
	}
	if full := mime.FormatMediaType(mt, params); full != "" {
		return full // the type and every parameter (sorted): a charset or a boundary is part of how the body reads
	}
	return strings.ToLower(strings.TrimSpace(header))
}

// delegateApproval settles step 6 for a side effect that needs the
// person's approval: a ticket, a standing approval of the tile's API, or —
// a session start only — the person's preference. msg is the refusal ("":
// approved); start is set for a session start. r's body is replaced when
// the gate had to read it.
func (s *Server) delegateApproval(w http.ResponseWriter, r *http.Request, p auth.Principal, v delegateVerdict, row *delegation.Call) (start *term.DelegateStart, status int, refusal, msg string) {
	session := v.pattern == "POST /term/sessions"
	ticket := strings.TrimSpace(r.Header.Get(HeaderTicket))
	var body []byte
	if ticket != "" || session {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, delegation.MaxBody))
		if err != nil {
			return nil, http.StatusRequestEntityTooLarge, "too-large", fmt.Sprintf("an approved call carries at most %d bytes", delegation.MaxBody)
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	if session {
		asked := sessionAskOf(body)
		row.Tile = asked.Cwd
		if refusal, why := s.startRefusal(p, asked); why != "" { // before a ticket is spent
			return nil, http.StatusForbidden, refusal, why
		}
	}
	const ask = " — ask for one: POST /api/xbin/delegate/requests {method, path, body}, then send its id as X-XBin-Ticket once the person approved"
	switch {
	case ticket != "":
		req, err := s.Delegation.Redeem(p.UserID, ticket, r.Method, requestTarget(r), v.tile, contentTypeOf(r.Header.Get("Content-Type")), body)
		if err != nil {
			return nil, http.StatusForbidden, "ticket", err.Error()
		}
		row.Ticket = req.ID
		if session {
			// the harness and mode the person was shown (sessionPreview):
			// the open refuses anything else
			shown, _ := req.Preview["session"].(map[string]any)
			prov, _ := shown["provider"].(string)
			mode, _ := shown["mode"].(string)
			if prov == "" || mode == "" {
				return nil, http.StatusForbidden, "ticket", "the ticket says no harness and mode: ask again"
			}
			net, _ := shown["net"].(string)
			start = &term.DelegateStart{Approved: true, Provider: prov, Mode: mode, Net: net}
		}
		s.delegateEvent(p.UserID, "request", req.ID)
		return start, 0, "", ""
	case v.tileAPI:
		a, ok := s.Delegation.Covers(p.UserID, v.tile, r.Method, r.URL.EscapedPath())
		if !ok {
			return nil, http.StatusForbidden, "needs-approval", "a write to " + v.tile + " needs its person's approval" + ask
		}
		row.Under = "approval:" + a.ID
		return nil, 0, "", ""
	case session:
		asked := sessionAskOf(body)
		cwd, provider := asked.Cwd, asked.Provider
		if provider == "" { // no harness named: the person's default for this tile (startDefaults)
			provider = s.startDefaults(p, cwd).Harness
		}
		prefs, ok := s.agentPolicy().StartPrefs(p.UserID, cwd, provider)
		if !ok {
			return nil, http.StatusForbidden, "needs-approval", "starting a tile agent needs its person's approval" + ask
		}
		row.Under = "preference"
		return &term.DelegateStart{Prefs: &prefs, Provider: provider}, 0, "", "" // the harness the preference was read for: what opens must be it
	}
	return nil, http.StatusForbidden, "needs-approval", "this needs its person's approval" + ask
}

// writeDryRun answers a dry run and records it when it described an act.
func (s *Server) writeDryRun(w http.ResponseWriter, row *delegation.Call, preview map[string]any) {
	row.DryRun, row.Status = true, http.StatusOK
	if row.Effect {
		s.delegateAudit(*row)
	}
	WriteJSON(w, http.StatusOK, preview)
}

// delegateAudit writes one delegate call to the person's audit log, and
// its acts and refusals to the daemon's (the "audit" line governance
// writes use). An act that ran is always a row — the act limit bounds
// those. Everything else (a refusal, a dry run, a read) is written up to a
// rate; past it the rows are counted and the next one written carries the
// count (N), so an agent looping on a refused call grows neither log.
func (s *Server) delegateAudit(c delegation.Call) {
	if !(c.Effect && c.Refused == "" && !c.DryRun) {
		ok, left := s.delegateRT.allowNoise(c.User)
		if !ok {
			return
		}
		c.N = left
	}
	if err := s.delegateAuditLog().Append(c); err != nil {
		slog.Warn("delegate audit: row not written", "user", c.User, "err", err)
	}
	if c.Effect || c.Refused != "" {
		slog.Info("audit", "who", "user:"+c.User, "agent", c.Agent, "method", c.Method, "path", c.Path,
			"class", c.Class, "status", c.Status, "refused", c.Refused, "dryRun", c.DryRun, "ticket", c.Ticket, "under", c.Under, "leftOut", c.N)
	}
}

// delegateWriter records the status a delegate call was answered with and
// keeps streaming working (an agent follows a session's events). It doesn't
// hijack: a delegate makes no socket.
type delegateWriter struct {
	http.ResponseWriter
	status int
	tile   bool // a tile's answer: its redirect goes nowhere
}

// redirectHeaders are how an answer sends a client on to another URL. A
// tile's answer to a delegate carries none: the call a person approved is
// one call, and a client that followed would repeat it — method, body,
// credential — at a path the tile chose. The status stays (the agent sees
// the tile answered 3xx); every request that arrives is judged on its own
// anyway, by its own method, path, ticket or approval.
var redirectHeaders = []string{"Location", "Content-Location", "Refresh"}

func (d *delegateWriter) WriteHeader(code int) {
	d.status = code
	if d.tile {
		for _, h := range redirectHeaders {
			d.Header().Del(h)
		}
	}
	d.ResponseWriter.WriteHeader(code)
}

func (d *delegateWriter) Write(b []byte) (int, error) {
	if d.tile { // an answer that never called WriteHeader: the same rule
		for _, h := range redirectHeaders {
			d.Header().Del(h)
		}
	}
	return d.ResponseWriter.Write(b)
}

func (d *delegateWriter) Flush() {
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (d *delegateWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }
