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
//  8. writes the row of the person's audit log: every act, tile call,
//     refusal and audited read — not the listing reads a UI makes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	if v.tile == "" {
		v.deny = "no such tile"
	}
	return v
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
	streams map[string]map[uint64]context.CancelFunc
	nextID  uint64
	now     func() time.Time // tests
}

type actBucket struct {
	tokens float64
	at     time.Time
}

func (d *delegateRuntime) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// allowAct takes one act from user's bucket; wait is how long until the
// next one when it is empty.
func (d *delegateRuntime) allowAct(user string) (ok bool, wait time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.clock()
	if d.buckets == nil {
		d.buckets = map[string]*actBucket{}
	}
	b := d.buckets[user]
	if b == nil {
		b = &actBucket{tokens: delegateActBurst, at: now}
		d.buckets[user] = b
	}
	b.tokens = min(delegateActBurst, b.tokens+now.Sub(b.at).Seconds()/delegateActRefill.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(delegateActRefill))
	}
	b.tokens--
	return true, 0
}

// track registers a running delegate call of user, which cut cancels.
func (d *delegateRuntime) track(user string, cancel context.CancelFunc) (done func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.streams == nil {
		d.streams = map[string]map[uint64]context.CancelFunc{}
	}
	if d.streams[user] == nil {
		d.streams[user] = map[uint64]context.CancelFunc{}
	}
	d.nextID++
	id := d.nextID
	d.streams[user][id] = cancel
	return func() {
		d.mu.Lock()
		delete(d.streams[user], id)
		if len(d.streams[user]) == 0 {
			delete(d.streams, user)
		}
		d.mu.Unlock()
	}
}

// CutDelegateStreams cancels the delegate calls running now for user — all
// people's when user is "": what pausing does at once (a session's event
// stream an agent follows would otherwise outlive the pause).
func (s *Server) CutDelegateStreams(user string) int {
	d := &s.delegateRT
	d.mu.Lock()
	var cancels []context.CancelFunc
	for u, m := range d.streams {
		if user == "" || u == user {
			for _, c := range m {
				cancels = append(cancels, c)
			}
		}
	}
	d.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	return len(cancels)
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
	return p.Agent != "" && s.agentPolicy().TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}) != nil
}

// DelegateHides is delegateHides for the listings other packages serve.
func (s *Server) DelegateHides(p auth.Principal, tile string) bool { return s.delegateHides(p, tile) }

// delegateDrives lets a delegate drive only the sessions it opened: a
// session the person opened themselves may be in a mode that asks nothing,
// and prompting it would be acting unasked. Everyone else passes. It
// answers the refusal itself.
func (s *Server) delegateDrives(w http.ResponseWriter, r *http.Request, id string) bool {
	p := auth.PrincipalOf(r)
	if p.Agent == "" || s.Term == nil {
		return true
	}
	if _, live := s.Term.Info(id); live && s.Term.AgentOrigin(id) != p.Agent {
		WriteError(w, http.StatusForbidden, "a workspace agent drives the sessions it started: this one is its person's own", "/docs/auth.md")
		return false
	}
	return true
}

// ---- the gate ----

func delegateRefuse(w http.ResponseWriter, status int, refusal, class, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg, "refusal": refusal, "class": class, "docs": "/docs/auth.md"})
}

// agentModel is the model header, bounded and printable.
func agentModel(r *http.Request) string {
	m := strings.TrimSpace(r.Header.Get(HeaderAgentModel))
	if len(m) > 128 {
		return ""
	}
	for _, c := range m {
		if c < 0x21 || c > 0x7e {
			return ""
		}
	}
	return m
}

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
	p.AgentModel = agentModel(r)
	v := s.delegateVerdict(r, p)
	row := delegation.Call{User: p.UserID, Agent: p.Agent, Method: r.Method, Path: r.URL.Path, Tile: v.tile, Class: v.class, Effect: v.effect}
	refuse := func(status int, refusal, msg string) {
		row.Status, row.Refused = status, msg
		s.delegateAudit(row)
		delegateRefuse(w, status, refusal, v.class, msg)
	}
	if v.deny != "" {
		refuse(http.StatusForbidden, "not-delegated", v.deny)
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
		if !p.CanReadTile(v.tile) { // at the person's level, whatever the handler asks
			refuse(http.StatusForbidden, "tile", p.UserID+" can't read "+v.tile+": their agent reaches only tiles they can read")
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
			if a, ok := s.Delegation.Covers(p.UserID, v.tile); ok {
				preview["needsApproval"], preview["under"] = false, "approval:"+a.ID
			}
		}
		preview["allowed"] = true
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

	// 7. run it, under a context the person's pause cancels
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.delegateRT.track(p.UserID, cancel)()
	dw := &delegateWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(dw, r.WithContext(auth.WithPrincipal(ctx, run)))
	row.Status = dw.status
	if v.effect || v.audit {
		s.delegateAudit(row)
	}
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
		row.Tile, _ = sessionAsk(body)
	}
	const ask = " — ask for one: POST /api/xbin/delegate/requests {method, path, body}, then send its id as X-XBin-Ticket once the person approved"
	switch {
	case ticket != "":
		req, err := s.Delegation.Redeem(p.UserID, ticket, r.Method, requestTarget(r), body)
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
			start = &term.DelegateStart{Approved: true, Provider: prov, Mode: mode}
		}
		s.delegateEvent(p.UserID, "request", req.ID)
		return start, 0, "", ""
	case v.tileAPI:
		a, ok := s.Delegation.Covers(p.UserID, v.tile)
		if !ok {
			return nil, http.StatusForbidden, "needs-approval", "a write to " + v.tile + " needs its person's approval" + ask
		}
		row.Under = "approval:" + a.ID
		return nil, 0, "", ""
	case session:
		cwd, provider := sessionAsk(body)
		prefs, ok := s.agentPolicy().StartPrefs(p.UserID, cwd, provider)
		if !ok {
			return nil, http.StatusForbidden, "needs-approval", "starting a tile agent needs its person's approval" + ask
		}
		row.Under = "preference"
		return &term.DelegateStart{Prefs: &prefs}, 0, "", ""
	}
	return nil, http.StatusForbidden, "needs-approval", "this needs its person's approval" + ask
}

// sessionAsk reads the tile and provider a POST /term/sessions body asks
// for ("" what it doesn't say).
func sessionAsk(body []byte) (cwd, provider string) {
	var b struct{ Cwd, Provider string }
	_ = json.Unmarshal(body, &b)
	return strings.Trim(b.Cwd, "/"), b.Provider
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
// writes use).
func (s *Server) delegateAudit(c delegation.Call) {
	if err := s.delegateAuditLog().Append(c); err != nil {
		slog.Warn("delegate audit: row not written", "user", c.User, "err", err)
	}
	if c.Effect || c.Refused != "" {
		slog.Info("audit", "who", "user:"+c.User, "agent", c.Agent, "method", c.Method, "path", c.Path,
			"class", c.Class, "status", c.Status, "refused", c.Refused, "dryRun", c.DryRun, "ticket", c.Ticket, "under", c.Under)
	}
}

// delegateWriter records the status a delegate call was answered with and
// keeps streaming working (an agent follows a session's events). It doesn't
// hijack: a delegate makes no socket.
type delegateWriter struct {
	http.ResponseWriter
	status int
}

func (d *delegateWriter) WriteHeader(code int) {
	d.status = code
	d.ResponseWriter.WriteHeader(code)
}

func (d *delegateWriter) Flush() {
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (d *delegateWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }
