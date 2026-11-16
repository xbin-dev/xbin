package server

import (
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
)

// delegatepush.go — the workspace agent's notifications (D241;
// plans/wsagent/ios-stage2.md §3). One notification a thing that waits for
// the person — a request, a change set — in xbind's words, never the
// agent's; the same thing's notifications collapse; and when the thing no
// longer waits (decided anywhere, withdrawn, expired) they are taken back.
//
// It follows delegateEvent: every op that names a request or a set is read
// again here, so whatever made the change — the person's decision on the
// web or in the app, the agent's withdrawal, a switch that withdraws
// everything — clears what stood for it. A request waits out a grace
// (delegatePushGrace) first: one decided at once, at a desk, raises
// nothing. Past the person's budget (delegatePushBurst, then one a
// delegatePushRefill) single notifications stop and one summary goes out
// when the budget allows. In memory: after a restart the app's own sweep
// (on foreground and background refresh) takes back what is stale.

// AgentPush is one notification about a person's workspace agent. Agent is
// the sealed payload's `agent` member (native/spec/push.md §2): the
// request's or set's fields the app draws the notification and its actions
// from; the push plane adds its version and the person's badge.
type AgentPush struct {
	Kind, Title, Body, Link, Collapse string
	Agent                             map[string]any
	Priority                          int
}

// viaNotification is the only `via` a decision may name: the person made
// it from a notification's action on their device.
const viaNotification = "notification"

const (
	delegatePushGrace  = 3 * time.Second
	delegatePushBurst  = 12
	delegatePushRefill = time.Minute // one more notification
)

// The collapse ids (the relay and APNs see a hash of each).
const (
	collapseRequest = "agent-approval:"
	collapseSet     = "agent-changeset:"
	collapseSummary = "agent-approval" // the one summary past the budget
)

type delegatePushes struct {
	mu      sync.Mutex
	grace   time.Duration          // tests: < 0 sends at once
	pushed  map[string]bool        // user\x00collapse → its notification went out and stands
	held    map[string]*time.Timer // user\x00collapse → waiting out the grace
	expire  map[string]*time.Timer // user\x00collapse → read again when it expires
	budget  map[string]*actBucket  // user → notifications left
	summary map[string]*time.Timer // user → a summary waits for the budget
}

func (d *delegatePushes) init() {
	if d.pushed == nil {
		d.pushed, d.held, d.expire = map[string]bool{}, map[string]*time.Timer{}, map[string]*time.Timer{}
		d.budget, d.summary = map[string]*actBucket{}, map[string]*time.Timer{}
	}
}

func pushKey(user, collapse string) string { return user + "\x00" + collapse }

// delegatePushFollow reads again what op names for user and brings their
// notifications in line: one that waits is pushed (after the grace), one
// that doesn't any more is taken back. The Live Activity follows too.
func (s *Server) delegatePushFollow(user, op, id string) {
	if user == "" {
		return
	}
	if s.AgentChanged != nil {
		s.AgentChanged(user)
	}
	if s.AgentPush == nil || s.Delegation == nil {
		return
	}
	switch {
	case op == "request" && id != "":
		key := collapseRequest + id
		if r, found := s.Delegation.Request(user, id); found && r.State == delegation.Pending {
			s.dpushArm(user, key, func() { s.dpushSendRequest(user, id) })
			return
		}
		s.dpushClear(user, []string{key})
	case op == "changeset" && id != "":
		key := collapseSet + id
		if cs, found := s.Delegation.ChangeSet(user, id); found && cs.Open() {
			s.dpushArm(user, key, func() { s.dpushSendSet(user, id) })
			return
		}
		s.dpushClear(user, []string{key})
	case op == "request", op == "changeset", op == "state":
		s.dpushSweep(user) // many at once (a switch withdrew everything)
	}
}

// dpushArm holds a notification for key the grace, then sends it unless
// one stands or is held already.
func (s *Server) dpushArm(user, key string, send func()) {
	d := &s.dpush
	d.mu.Lock()
	d.init()
	k := pushKey(user, key)
	if d.pushed[k] || d.held[k] != nil {
		d.mu.Unlock()
		return
	}
	grace := d.grace
	if grace == 0 {
		grace = delegatePushGrace
	}
	if grace < 0 {
		d.mu.Unlock()
		send()
		return
	}
	d.held[k] = time.AfterFunc(grace, func() {
		d.mu.Lock()
		delete(d.held, k)
		d.mu.Unlock()
		send()
	})
	d.mu.Unlock()
}

func (s *Server) dpushSendRequest(user, id string) {
	r, found := s.Delegation.Request(user, id)
	if !found || r.State != delegation.Pending {
		return
	}
	s.dpushSend(user, s.requestPush(r), r.Expires, func() { s.delegatePushFollow(user, "request", id) })
}

func (s *Server) dpushSendSet(user, id string) {
	cs, found := s.Delegation.ChangeSet(user, id)
	if !found || !cs.Open() {
		return
	}
	s.dpushSend(user, s.setPush(cs), cs.Expires, func() { s.delegatePushFollow(user, "changeset", id) })
}

// dpushSend sends n within the person's budget (past it: the summary,
// later), and reads the thing again when it expires.
func (s *Server) dpushSend(user string, n AgentPush, expires time.Time, onExpire func()) {
	if !s.dpushBudget(user, false) {
		s.dpushSummaryLater(user)
		return
	}
	n.Agent["badge"] = s.agentBadge(user)
	sent, limited := s.AgentPush(user, n)
	switch {
	case limited:
		s.dpushSummaryLater(user)
		return
	case !sent:
		return // nobody to tell: nothing stands to take back
	}
	s.dpushBudget(user, true)
	d := &s.dpush
	d.mu.Lock()
	k := pushKey(user, n.Collapse)
	d.pushed[k] = true
	if t := d.expire[k]; t != nil {
		t.Stop()
	}
	if left := time.Until(expires) + time.Second; left > 0 && left < 8*24*time.Hour {
		d.expire[k] = time.AfterFunc(left, onExpire)
	}
	d.mu.Unlock()
}

// dpushBudget reports whether user may get one more notification now;
// take spends it.
func (s *Server) dpushBudget(user string, take bool) bool {
	d := &s.dpush
	d.mu.Lock()
	defer d.mu.Unlock()
	d.init()
	now := time.Now()
	b := d.budget[user]
	if b == nil {
		b = &actBucket{tokens: delegatePushBurst, at: now}
		d.budget[user] = b
	}
	b.tokens = min(float64(delegatePushBurst), b.tokens+float64(now.Sub(b.at))/float64(delegatePushRefill))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	if take {
		b.tokens--
	}
	return true
}

// dpushSummaryLater sends one "N things wait" once the budget allows —
// never more than one at a time.
func (s *Server) dpushSummaryLater(user string) {
	d := &s.dpush
	d.mu.Lock()
	defer d.mu.Unlock()
	d.init()
	if d.summary[user] != nil {
		return
	}
	d.summary[user] = time.AfterFunc(delegatePushRefill, func() {
		d.mu.Lock()
		delete(d.summary, user)
		d.mu.Unlock()
		s.dpushSummary(user)
	})
}

func (s *Server) dpushSummary(user string) {
	n := s.agentWaiting(user)
	if n == 0 {
		return
	}
	if !s.dpushBudget(user, false) {
		s.dpushSummaryLater(user)
		return
	}
	push := AgentPush{Kind: "agent.approval", Title: "Your workspace agent waits for you",
		Body: fmt.Sprintf("%d things wait for you. Open your agent to see each.", n),
		Link: "xbin/agent#needs", Collapse: collapseSummary, Agent: map[string]any{"summary": map[string]any{"waiting": n}}, Priority: 10}
	s.dpushSend(user, push, time.Now().Add(24*time.Hour), func() { s.delegatePushFollow(user, "state", "") })
}

// dpushClear takes back the notifications of keys that stand (a held one
// just doesn't go).
func (s *Server) dpushClear(user string, keys []string) {
	d := &s.dpush
	d.mu.Lock()
	d.init()
	var gone []string
	for _, key := range keys {
		k := pushKey(user, key)
		if t := d.held[k]; t != nil {
			t.Stop()
			delete(d.held, k)
		}
		if t := d.expire[k]; t != nil {
			t.Stop()
			delete(d.expire, k)
		}
		if d.pushed[k] {
			delete(d.pushed, k)
			gone = append(gone, key)
		}
	}
	d.mu.Unlock()
	if len(gone) > 0 && s.AgentClear != nil {
		s.AgentClear(user, gone, s.agentBadge(user))
	}
}

// dpushSweep reads again everything that stands or is held for user.
func (s *Server) dpushSweep(user string) {
	d := &s.dpush
	d.mu.Lock()
	d.init()
	var keys []string
	prefix := user + "\x00"
	for k := range d.pushed {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, strings.TrimPrefix(k, prefix))
		}
	}
	for k := range d.held {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, strings.TrimPrefix(k, prefix))
		}
	}
	d.mu.Unlock()
	var stale []string
	for _, key := range keys {
		switch {
		case strings.HasPrefix(key, collapseRequest):
			if r, ok := s.Delegation.Request(user, strings.TrimPrefix(key, collapseRequest)); !ok || r.State != delegation.Pending {
				stale = append(stale, key)
			}
		case strings.HasPrefix(key, collapseSet):
			if cs, ok := s.Delegation.ChangeSet(user, strings.TrimPrefix(key, collapseSet)); !ok || !cs.Open() {
				stale = append(stale, key)
			}
		case key == collapseSummary:
			if s.agentWaiting(user) == 0 {
				stale = append(stale, key)
			}
		}
	}
	if len(stale) > 0 {
		s.dpushClear(user, stale)
	}
}

// shown is req as a's caller may read it: where and how the person decided
// — from a notification, on which of their devices — is theirs, never the
// agent's or its page's (review finding 7; D241).
func (a delegateActor) shown(req delegation.Request) delegation.Request {
	if !a.own {
		req.DecidedFrom, req.DecidedDevice = "", ""
	}
	return req
}

// ---- the words (xbind's; never the agent's) ----

// tileDisplay is xbind's name for a tile: its manifest's title, else the
// last part of its path.
func (s *Server) tileDisplay(tile string) string {
	if tile == "" {
		return ""
	}
	if s.Reg != nil {
		if comp, ok := s.Reg.Component(tile); ok {
			if t := comp.Manifest.DisplayTitle(); t != "" {
				return t
			}
		}
	}
	return path.Base(tile)
}

// requestPush is r's notification: the title and body xbind writes for its
// kind (an older app shows these), and the request's own fields for the
// app — the call itself only where the lock screen may offer Allow once
// (delegation.LockScreenOffer), never the agent's summary.
func (s *Server) requestPush(r delegation.Request) AgentPush {
	// the title is xbind's alone; the tile's name is its author's words, so it
	// stands in the body quoted as the tile's own (review finding 5)
	name := s.tileDisplay(r.Tile)
	quoted := quotedTileName(name, r.Tile)
	title, body := "Approval needed", ""
	switch r.Kind {
	case delegation.KindSession:
		body = "Your workspace agent wants to start a tile agent in the tile " + quoted + "."
	case delegation.KindTemplate:
		body = "Your workspace agent wants to create a tile."
	case delegation.KindWeb:
		body = "Your workspace agent wants to read a web page. Open to see the address."
	case delegation.KindMCP:
		body = "Your workspace agent wants to use a tool of the tile " + quoted + "."
	default:
		body = "Your workspace agent wants to change something in the tile " + quoted + "."
	}
	if r.Kind != delegation.KindWeb {
		body += " Open to see exactly what."
	}
	req := map[string]any{"id": r.ID, "kind": r.Kind, "tile": r.Tile, "tileName": name, "expires": r.Expires.Unix(), "offer": ""}
	if ok, _ := delegation.LockScreenOffer(r); ok {
		req["offer"] = "once"
		req["method"], req["target"], req["contentType"] = r.Method, r.Target, r.ContentType
		req["body"], req["bodySize"], req["bodySha256"] = r.Body, r.BodySize, r.BodySHA256
	}
	return AgentPush{Kind: "agent.approval", Title: title, Body: body, Link: "xbin/agent#r=" + r.ID,
		Collapse: collapseRequest + r.ID, Agent: map[string]any{"request": req}, Priority: 10}
}

// setPush is cs's notification: a plan, admin changes, or another person's
// ask for an admin's changes.
func (s *Server) setPush(cs delegation.ChangeSet) AgentPush {
	n, warned := len(cs.Items), 0
	for _, it := range cs.Items {
		if len(it.Warnings) > 0 {
			warned++
		}
	}
	title := "Admin changes proposed"
	body := fmt.Sprintf("Your workspace agent proposes %s. Nothing is applied until you approve.", plural(n, "admin change", "admin changes"))
	switch {
	case cs.From != "":
		who := cs.From
		if s.Auth != nil && s.Auth.Users != nil {
			if u, ok := s.Auth.Users.Get(cs.From); ok && u.Name != "" {
				who = u.Name
			}
		}
		title = quotedTileName(who, cs.From) + " asks for an admin's changes"
		body = fmt.Sprintf("%s wait for an admin. Nothing is applied until an admin approves.", plural(n, "change", "changes"))
	case cs.Plan:
		title = "A plan to review"
		body = fmt.Sprintf("Your workspace agent proposes a plan of %s. Nothing is applied until you approve.", plural(n, "step", "steps"))
	}
	set := map[string]any{"id": cs.ID, "items": n, "warned": warned, "plan": cs.Plan, "asks": cs.From != "", "expires": cs.Expires.Unix()}
	return AgentPush{Kind: "agent.changeset", Title: title, Body: body, Link: "xbin/agent#cs=" + cs.ID,
		Collapse: collapseSet + cs.ID, Agent: map[string]any{"changeset": set}, Priority: 10}
}

// quotedTileName is a name someone else chose — a tile's title, a person's
// name — as a notification shows it: quoted, on one line, at most 32
// characters; one that is empty, hides or reorders text, or breaks a line
// gives way to fallback's last part (the tile's path, the person's id).
func quotedTileName(name, fallback string) string {
	n := strings.TrimSpace(name)
	if n == "" || delegation.HidesText(n) || strings.ContainsAny(n, "\n\r\t\u2028\u2029\u0085") {
		n = path.Base(fallback)
	}
	if r := []rune(n); len(r) > 32 {
		n = strings.TrimSpace(string(r[:31])) + "…"
	}
	return "“" + n + "”"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- what waits, and what runs ----

// agentWaiting is what waits for user because of their agent: pending
// requests and open change sets.
func (s *Server) agentWaiting(user string) int {
	if s.Delegation == nil {
		return 0
	}
	pend, _ := s.Delegation.Requests(user, true)
	sets, _ := s.Delegation.ChangeSets(user)
	n := len(pend)
	for _, cs := range sets {
		if cs.Open() {
			n++
		}
	}
	return n
}

// agentSessions counts user's agent sessions (tile agents, coding agents):
// those that wait for them, and those their workspace agent started that
// are working.
func (s *Server) agentSessions(user string) (waiting, working int) {
	if s.Term == nil {
		return 0, 0
	}
	for _, si := range s.Term.ListFor(term.HomeKey(auth.Principal{UserID: user}), "", nil) {
		if si.Kind != term.KindAgent {
			continue
		}
		switch {
		case si.Pending > 0 || si.Questions > 0 || si.Status == "waiting_permission":
			waiting++
		case si.Origin == term.OriginAgent && (si.Status == "running" || si.Status == "starting" || si.Status == "cancelling"):
			working++
		}
	}
	return waiting, working
}

// agentBadge is the person's count for this workspace (a payload's
// `agent.badge`): what their agent waits on them for, and their agent
// sessions that wait for them.
func (s *Server) agentBadge(user string) int {
	waiting, _ := s.agentSessions(user)
	return s.agentWaiting(user) + waiting
}

// AgentPicture is what the person's workspace agent is doing, for its Live
// Activity (internal/push agentactivity.go): whether they have one that may
// act (an agent given, workspace agents on, not held), paused, what waits
// for them (the badge), and how many tile agents it started are working.
type AgentPicture struct {
	Has, Paused     bool
	Pending, Agents int
}

// AgentPictureOf reads user's picture now.
func (s *Server) AgentPictureOf(user string) AgentPicture {
	if s.Delegation == nil || user == "" {
		return AgentPicture{}
	}
	ws := s.Delegation.Workspace()
	st := s.Delegation.State(user)
	pic := AgentPicture{Has: ws.Enabled && s.personHasAgent(user), Paused: st.Paused}
	if !pic.Has {
		return pic
	}
	waiting, working := s.agentSessions(user)
	pic.Pending, pic.Agents = s.agentWaiting(user)+waiting, working
	return pic
}

// AgentBadgeOf is agentBadge for the push plane.
func (s *Server) AgentBadgeOf(user string) int { return s.agentBadge(user) }

// personHasAgent: user was given an agent on this xbind and it isn't held.
func (s *Server) personHasAgent(user string) bool {
	h := s.agentHolder()
	if h == "" || !s.userHasAgent(user, h) {
		return false
	}
	if s.AgentHeld != nil {
		if kind, _ := s.AgentHeld(); kind != "" {
			return false
		}
	}
	return true
}
