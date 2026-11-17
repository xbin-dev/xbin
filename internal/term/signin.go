package term

// signin.go — a person's saved coding-agent sign-in in their agent sessions
// (D230; docs/agent-signins.md). xbind keeps the sign-ins (internal/signins)
// and, when a person's agent session starts, puts their default one for the
// session's coding agent into the agent's environment — through the spawn
// frame, to the agent's process alone (the path the LLM connector's key
// takes) — where the rule below allows it.
//
// THE RULE (signinJudge; the one place it is decided — the launcher and
// the UI repeat its answer), as the owner set it on 2026-11-17: a person's
// saved sign-in is used in their own sessions BY DEFAULT, and on a tile
// whose source is not only their own it waits for "I trust this tile's
// source". A session is a sandbox of its own, so nobody else reads its
// environment. But what runs in it comes from the tile's directory (the
// project's own hooks and scripts) and its environment layer: code in the
// tile runs with the sign-in. So it goes in when
//
//  1. the session is its person's own: opened by their own signed-in
//     session, app or device, or by their workspace agent for them — never
//     view-as, a component's credentials (a tile's frame or backend, a
//     terminal's or an agent session's token), the root token;
//  2. sandboxes are on (a development workspace excepted: SigninDev);
//  3. the workspace's switch is on, and the connector isn't the only one
//     allowed to serve the session, and the person didn't choose it;
//  4. the tile's source is only the person's own — or they said they trust
//     it (signins.Source, internal/signins/trust.go). It is not only their
//     own when EITHER the tile is owned by someone else (another person,
//     an org, the workspace) OR someone else who isn't a workspace admin
//     can change its code (write level or more, by any source; partitioned
//     tiles too). Trust is the person's own answer, once per tile, for the
//     tile as it stood: a new person able to change it, another owner, or
//     a tile made again at the same path asks again.
//
// A start by the person's workspace agent: one they approve names the
// sign-in on xbin's sheet, and where the tile needs trust the sheet carries
// the trust line — approving without it starts the session without the
// sign-in; one their "start without asking" preference lets through gets
// the default on their own and trusted tiles, and never into a tile they
// haven't trusted. Nothing but the person's own answer in xbin's chrome
// is trust: not an admin's word, an owner's, the agent's, a page's.
//
// Otherwise the session starts without it and says why. A session that
// carries one is its person's alone: no admin pass to drive or read it
// (adminPass, viewAsBarred), and it is ended when the basis changes
// (SigninSweep: trust revoked, a new editor, another owner), when the
// sign-in is removed or replaced (EndSigninSessions), and when its person
// is signed out. A restart never widens what a session was opened with.
//
// What xbind keeps of such a session is masked (signinredact.go).

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agent/acp"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/signins"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// SigninStore is the sign-in store as sessions use it (*signins.Store).
type SigninStore interface {
	Peek(user, harness, id string) (*signins.Signin, error)
	Use(user, harness, id string, u signins.UseFor) (*signins.Credential, error)
	MarkRefused(user, id, why string)
	// Trusts: user said they trust src's source, and the answer still holds
	// (signins/trust.go). Policy: the workspace's switches.
	Trusts(user string, src signins.Source) bool
	Policy() signins.Policy
}

// SigninHook is the Manager's share of saved sign-ins: what boot wires.
type SigninHook struct {
	// Signins is the store; nil ⇒ sessions are exactly as before.
	Signins SigninStore
	// SigninSource answers the registered tile a path is or lies in and
	// where its code comes from for user (the broker's SigninSource: who
	// owns it, who else can change it). ok false: the path lies in no tile.
	// nil ⇒ unknown, and no saved sign-in goes into any session.
	SigninSource func(user, path string) (src signins.Source, ok bool)
	// SigninsOff reports saved sign-ins switched off for the workspace (the
	// admins' switch, or the host's file): running sessions that carry one
	// are ended, as when a sign-in is removed (SigninSweep). nil ⇒ never.
	SigninsOff func() bool
	offSeen    atomic.Bool
	// SigninTrustOff reports trusting a tile's source switched off for the
	// workspace (the admins' switch, or the host's file): the running
	// sessions that carry a sign-in on a trust are ended (SigninSweep).
	// nil ⇒ never.
	SigninTrustOff func() bool
	trustOffSeen   atomic.Bool
	// SigninDev: a development workspace (--dev, --no-auth), where saved
	// sign-ins work without sandboxes.
	SigninDev bool

	homeSeen homeLogins // which providers each home was last found signed in to
}

// homeLogins remembers, per home and provider, whether an agent host last
// found the CLI's own login there. In memory: a hint for a launcher and a
// brief's card, never a decision.
type homeLogins struct {
	mu sync.Mutex
	by map[string]bool // homeKey \x00 provider
}

// HomeLogin reports whether an agent session last found provider's own
// login in homeKey's $HOME (known false: none looked yet).
func (m *Manager) HomeLogin(homeKey, provider string) (signedIn, known bool) {
	if in, ok := m.knownLogin(homeKey, provider); ok { // the connector's hook-up looked (llmhook.go)
		return in, true
	}
	m.homeSeen.mu.Lock()
	defer m.homeSeen.mu.Unlock()
	signedIn, known = m.homeSeen.by[homeKey+"\x00"+provider]
	return
}

// NoteHomeLogin records what an agent session found (tests set it too).
func (m *Manager) NoteHomeLogin(homeKey, provider string, signedIn bool) {
	m.noteHome(homeKey, provider, signedIn)
}

func (m *Manager) noteHome(homeKey, provider string, signedIn bool) {
	m.homeSeen.mu.Lock()
	defer m.homeSeen.mu.Unlock()
	if m.homeSeen.by == nil {
		m.homeSeen.by = map[string]bool{}
	}
	m.homeSeen.by[homeKey+"\x00"+provider] = signedIn
}

// SigninNone is a session's choice of no saved sign-in (POST /term/sessions
// {signin: "none"}).
const SigninNone = "none"

// Where a session's sign-in comes from (SessionSignin.Source).
const (
	SigninSaved     = "saved"     // a saved sign-in of the person's, in the agent's environment
	SigninHome      = "home"      // the CLI's own login in the session's $HOME (the tile's own login)
	SigninConnector = "connector" // none: the workspace's LLM connector serves
	SigninNoneYet   = "none"      // none found: the agent will say it is signed out
)

// SessionSignin is what an agent session runs on, for its row and its
// first notice.
type SessionSignin struct {
	Source string `json:"source"`
	// ID and Name: the saved sign-in in use (source saved), or the one that
	// was left out (Why says why).
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Why a saved sign-in the person has isn't used here.
	Why string `json:"why,omitempty"`
	// Trust: it isn't used because the tile's source isn't only the
	// person's own and they haven't said they trust it — what a card asks.
	Trust *SigninTrust `json:"trust,omitempty"`
}

// SigninTrust is the question "do you trust this tile's source?", in
// xbind's words: whose tile it is and who else can change it. Only xbin's
// own chrome asks it, and only the person answers.
type SigninTrust struct {
	Tile string `json:"tile"`
	Name string `json:"name,omitempty"`
	// Basis is the token of what is asked about — the tile itself, its
	// owner and everyone who can change it (signins.Source.Basis). The
	// answer carries it back, and one for another basis is refused (409):
	// the person trusts what they were shown.
	Basis string `json:"basis"`
	// OwnerKind: you | person | org | workspace. Owner is the owner's
	// account or org id — what xbind enforces — and OwnerName its display
	// name where it has another (shown beside the id, never instead).
	// Admin: a workspace-owned tile, and the person administers the
	// workspace.
	OwnerKind string `json:"ownerKind"`
	Owner     string `json:"owner,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	// Editors: the account ids of ALL the other people who can change the
	// tile's code (the person must be able to see everyone they trust);
	// Names their display names by id, where they have another.
	Editors []string          `json:"editors,omitempty"`
	Names   map[string]string `json:"names,omitempty"`
	// Allowed false: the workspace doesn't let people trust a tile's
	// source — Forbidden says who can change that.
	Allowed   bool   `json:"allowed"`
	Forbidden string `json:"forbidden,omitempty"`
}

// trustNeed is src as a card asks about it.
func trustNeed(src signins.Source, pol signins.TrustPolicy) *SigninTrust {
	t := &SigninTrust{Tile: src.Tile, Name: src.Name, Basis: src.Basis(), OwnerKind: src.OwnerKind, Owner: src.OwnerID, OwnerName: src.OwnerName,
		Admin: src.OwnerKind == "workspace" && src.Admin, Allowed: pol.Allowed}
	if signins.CoEditorsNeedTrust {
		t.Editors = src.Editors
		if len(src.Names) > 0 {
			t.Names = src.Names
		}
	}
	switch {
	case pol.Allowed:
	case pol.Locked:
		t.Forbidden = "Trusting a tile's source is switched off for this workspace — " + pol.Reason
	default:
		t.Forbidden = "Trusting a tile's source is switched off for this workspace — a workspace admin can change it"
	}
	return t
}

// SigninQuestion is the trust question for src as it stands (a card's
// redraw after "this changed").
func (m *Manager) SigninQuestion(src signins.Source, pol signins.TrustPolicy) *SigninTrust {
	return trustNeed(src, pol)
}

// sourceWords says why src isn't only the person's own.
func sourceWords(src signins.Source) string {
	var parts []string
	switch src.OwnerKind {
	case "person": // account and org ids: what xbind enforces
		parts = append(parts, "it belongs to "+src.OwnerID)
	case "org":
		parts = append(parts, "it belongs to the org "+src.OwnerID)
	case "workspace":
		if src.NotOwn {
			parts = append(parts, "it belongs to the workspace")
		}
	}
	if signins.CoEditorsNeedTrust && len(src.Editors) > 0 {
		parts = append(parts, othersWords(src.Editors)+" can change it")
	}
	return strings.Join(parts, ", and ")
}

// signinPlan is a new session's share (openOpts): what was decided before
// the session exists. It never holds a secret.
type signinPlan struct {
	choice string          // the start's own: "" | "none" | a sign-in id
	saved  *signins.Signin // the person's sign-in for the harness (nil: none)
	why    string          // why it is left out ("" with saved: it goes in)
	user   string          // whose it is
	origin string          // "person" | "agent", for the use log
	// past: the session resumes a past one that ran on a saved sign-in
	past bool
	// bound: a workspace agent's start whose approval named this sign-in —
	// it opens with exactly that one or not at all (OpenAgentWith)
	bound bool
	// trust: it is left out because the tile's source needs the person's
	// trust (why says it too)
	trust *SigninTrust
	// agentWhy: a workspace agent's start without the person's tick — why
	// it has no saved sign-in, said without naming one
	agentWhy string
}

// sessionSignin is a live session's: what it carries. secret is the
// credential in its agent's environment (nil: none) — kept to redact what
// the session prints, never logged, dropped when the session ends.
type sessionSignin struct {
	view   SessionSignin
	user   string
	id     string // the saved sign-in's id (the view names it only in a session its person started)
	secret []byte
	// red masks the sign-in in what the session says (signinredact.go);
	// kept while the session lives, also once the secret was taken away
	red *redactor
	// held: the session's agent held a saved sign-in at some point (also
	// after it was taken away), or resumes a past session that did — its
	// log may hold what its tools printed, so it stays its person's alone
	// (carriesSignin) and its history says so (HistoryMeta.Signin)
	held bool
	// choice is what the open asked for: "" (the person's default),
	// SigninNone, or a sign-in's id — kept so a restart asks for no more
	// (signinRestart)
	choice string
	// ending: a sweep or a removal is ending the session for its sign-in
	// (endClaim) — the one that claimed it says why and counts it
	ending bool
}

// sig is s's sign-in state: nil for a terminal, and where no store is
// wired. s.mu held.
func (s *Session) sig() *sessionSignin {
	if s.agent == nil {
		return nil
	}
	return s.agent.signin
}

func (ss *sessionSignin) carries() bool { return ss != nil && len(ss.secret) > 0 }

// signinMask is what a redacted secret reads as.
const signinMask = "[saved sign-in]"

// SigninPreview answers, for a launcher, what a session of p's of harness
// on tile would get: the person's saved sign-in (nil: none), why it would
// be left out ("" = it goes in), and the trust the tile asks for first.
// forced: the connector alone may serve there.
func (m *Manager) SigninPreview(p auth.Principal, tile string, prov agent.Provider, forced bool) (*signins.Signin, string, *SigninTrust) {
	if m.Signins == nil {
		return nil, "", nil
	}
	var o openOpts
	pl := m.signinPlanFor(p, o, tile, prov, "", forced, false, nil)
	return pl.saved, pl.why, pl.trust
}

// SigninForStart answers, for a start by p — a person's workspace agent —
// on tile that the person is about to APPROVE, the person's default
// sign-in for prov that the sheet OFFERS (nil: none, or it could not go
// in): a line of its own, unticked — "Use my … sign-in for this session".
// Ticked, the open is bound to its id (DelegateStart.Signin); not ticked,
// the session has none. need: the tile's source isn't only theirs — the
// sheet carries the trust line too, tickable only with the first. A start
// that is not approved (their "start without asking" preference) is never
// asked about: it never has a sign-in.
func (m *Manager) SigninForStart(p auth.Principal, tile string, prov agent.Provider, forced bool) (sg *signins.Signin, need *SigninTrust) {
	if m.Signins == nil || p.Agent == "" {
		return nil, nil
	}
	def, err := m.Signins.Peek(p.UserID, prov.ID, "")
	if err != nil || def == nil {
		return nil, nil
	}
	pl := m.signinPlanFor(p, openOpts{}, tile, prov, "", forced, false, &DelegateStart{Approved: true, Signin: def.ID, TrustAsked: true})
	switch {
	case pl.saved == nil:
		return nil, nil
	case pl.why == "":
		return pl.saved, nil
	case pl.trust != nil && pl.trust.Allowed:
		return pl.saved, pl.trust
	}
	return nil, nil
}

// SigninRule answers the rule alone for a launcher or a sign-in card: why a
// saved sign-in of p's would not go into a session of prov they open on
// tile ("" = it would), whether or not they have one saved yet — and the
// trust the tile asks for first.
func (m *Manager) SigninRule(p auth.Principal, tile string, prov agent.Provider) (string, *SigninTrust) {
	if m.Signins == nil {
		return "this xbind keeps no saved sign-ins", nil
	}
	var o openOpts
	o.userID = p.UserID
	return m.signinJudge(p, o, tile, prov, m.credentialRule(o, tile, prov.ID, "").Forced, false)
}

// signinPlanFor decides a new session's saved sign-in: which one, and
// whether the rule lets it in. chosen: the start named the connector.
func (m *Manager) signinPlanFor(p auth.Principal, o openOpts, rel string, prov agent.Provider, choice string, forced, chosen bool, d *DelegateStart) signinPlan {
	pl := signinPlan{choice: choice, user: p.UserID, origin: "person"}
	if p.Agent != "" {
		pl.origin = "agent"
	}
	if m.Signins == nil || len(prov.Keys) == 0 || choice == SigninNone {
		return pl
	}
	if !signinPerson(p) {
		return pl // not a person's own session: nothing of theirs is even looked up
	}
	if p.Agent != "" {
		// a workspace agent's start, for its person. The agent directs the
		// session and reads what it prints, so a sign-in goes in only where
		// the person TICKED it for this very start, on xbin's own sheet
		// (DelegateStart.Signin: the approval's "Use my … sign-in for this
		// session", unticked by default) — each time. A start their
		// "start without asking" preference lets through never has one, on
		// any tile; nor one approved without the tick. Nothing of the
		// person's is looked up for those: the session's row, which the
		// agent reads, says only that it has none.
		if d == nil || !d.Approved || d.Signin == "" {
			if sg, err := m.Signins.Peek(p.UserID, prov.ID, ""); err == nil && sg != nil { // (that one exists, nothing of it)
				pl.agentWhy = "your workspace agent started this session, and a saved sign-in goes into such a session only where you tick it on the start's approval"
			}
			return pl
		}
		choice, pl.bound = d.Signin, true
	}
	sg, err := m.Signins.Peek(p.UserID, prov.ID, choice)
	if err != nil || sg == nil {
		if pl.bound {
			pl.why = "the saved sign-in the approval named is no longer there"
		}
		return pl
	}
	pl.saved = sg
	pl.why, pl.trust = m.signinJudge(p, o, rel, prov, forced, chosen)
	if pl.trust != nil && pl.bound && d.TrustAsked {
		// the sheet carried the trust line and the person approved without
		// it: the start they approved is one without their sign-in
		pl.bound = false
		pl.why = "you approved this start without trusting the source of " + pl.trust.Tile
	}
	if pl.why == "" && sg.Expired(time.Now()) {
		pl.why = fmt.Sprintf("your saved sign-in %s expired on %s — sign in again", sg.Name, time.UnixMilli(sg.ExpiresAt).UTC().Format("2006-01-02"))
	}
	return pl
}

// signinPerson reports a session opened by a person themselves, or by
// their workspace agent for them: the only openers a saved sign-in serves.
func signinPerson(p auth.Principal) bool {
	if p.UserID == "" || p.Component != "" || p.ReadOnly() || p.Owner {
		return false
	}
	switch p.Via {
	case "terminal", "instance", "frame", "cron":
		return false
	}
	return true
}

// signinJudge is the rule: why a saved sign-in of p's does not go into a
// session of prov they open on rel ("" = it does) — and, when the reason
// is that the tile's source needs their trust, the question to ask.
func (m *Manager) signinJudge(p auth.Principal, o openOpts, rel string, prov agent.Provider, forced, chosen bool) (string, *SigninTrust) {
	if why := m.signinBefore(p, rel, forced, chosen); why != "" {
		return why, nil
	}
	if m.SigninSource == nil {
		return "xbind can't tell whose " + rel + " is", nil
	}
	// the tile the directory lies in, never the directory as written: a
	// person's access is to the tile, and its hooks run in every directory
	// of it — on a partitioned tile too, whose directory is shared code
	tile, ok := m.TileOf(rel) // the one resolver every session decision uses (sessiondir.go)
	var src signins.Source
	if ok {
		src, ok = m.SigninSource(p.UserID, tile)
	}
	if !ok {
		return rel + " is in no registered tile, so xbind can't tell whose code runs there", nil
	}
	m.signinTrustOff() // (seen forbidden for the first time: the sessions on a trust end)
	// its source is only the person's own, or they said they trust it
	if !src.NeedsTrust() || m.Signins.Trusts(p.UserID, src) {
		return "", nil
	}
	need := trustNeed(src, m.Signins.Policy().Trust)
	if !need.Allowed {
		return src.Tile + " isn't only yours — " + sourceWords(src) + " — and trusting a tile's source is switched off for this workspace", need
	}
	return src.Tile + " isn't only yours — " + sourceWords(src) + " — and you haven't said you trust its source", need
}

// signinBefore is the rule's part that asks nothing of the tile's source:
// the switch, whose session it is, sandboxes, the connector.
func (m *Manager) signinBefore(p auth.Principal, rel string, forced, chosen bool) string {
	if m.signinsOff() { // (seen off for the first time: the running sessions end)
		return "saved coding-agent sign-ins are switched off for this workspace"
	}
	switch {
	case !signinPerson(p):
		return "a saved sign-in goes only into a session its person opened themselves"
	case !m.Isolate && !m.SigninDev:
		return "this workspace runs without sandboxes, where every terminal could read a coding agent's environment"
	case forced:
		return "coding agents on " + rel + " run on the workspace's LLM connector only"
	case chosen:
		return "this session was started on the workspace's LLM connector"
	}
	return ""
}

// SigninForPlanned answers, for a start by p — a person's workspace agent —
// on a tile the same plan is about to create (src: the broker's
// SigninSourcePlanned), the person's default sign-in the start line names:
// only where the new tile will be the person's own, with nobody else able
// to change it. A tile that would need trust has no identity to trust
// yet: it starts without. The open judges the tile itself once it exists.
func (m *Manager) SigninForPlanned(p auth.Principal, prov agent.Provider, src signins.Source, forced bool) *signins.Signin {
	if m.Signins == nil || p.Agent == "" || len(prov.Keys) == 0 {
		return nil
	}
	def, err := m.Signins.Peek(p.UserID, prov.ID, "")
	if err != nil || def == nil || def.Expired(time.Now()) {
		return nil
	}
	if m.signinBefore(p, src.Tile, forced, false) != "" || src.NeedsTrust() {
		return nil
	}
	return def
}

// othersWords names up to two people and counts the rest.
func othersWords(others []string) string {
	switch n := len(others); {
	case n == 1:
		return others[0]
	case n == 2:
		return others[0] + " and " + others[1]
	default:
		return fmt.Sprintf("%s, %s and %d more", others[0], others[1], n-2)
	}
}

// signinTake takes the planned sign-in for session id out of the store
// (the one use, logged there) and answers what the session carries and the
// spawn frame's part. A sign-in that can't be had — the vault sealed, the
// switch off — is left out with the reason. connector: the connector
// serves this session by the person's standing choice.
func (m *Manager) signinTake(pl signinPlan, id, rel string, prov agent.Provider, connector bool) (*sessionSignin, *acp.SpawnSignin) {
	if m.Signins == nil {
		if pl.past {
			return &sessionSignin{user: pl.user, held: true, red: newRedactor(nil)}, nil
		}
		return nil, nil // no store: the session is exactly as before — nothing said, nothing noted
	}
	ss := &sessionSignin{user: pl.user, choice: pl.choice, held: pl.past}
	if pl.past {
		ss.red = newRedactor(nil) // the transcript it resumes was made on a sign-in: the shapes at least
	}
	if pl.saved == nil {
		ss.view.Why = pl.agentWhy
		return ss, nil
	}
	ss.id = pl.saved.ID
	if pl.origin != "agent" {
		ss.view.ID, ss.view.Name, ss.view.Trust = pl.saved.ID, pl.saved.Name, pl.trust
	} // (a session the workspace agent started is read by it — its row, its events: they name no sign-in and no one's access)
	if pl.why == "" && connector {
		pl.why = "you chose the workspace's LLM connector for " + prov.Name
	}
	if pl.why != "" {
		ss.view.Why = pl.why
		return ss, nil
	}
	cred, err := m.Signins.Use(pl.user, prov.ID, pl.saved.ID, signins.UseFor{Tile: rel, Session: id, Origin: pl.origin})
	if err != nil || cred == nil {
		ss.view.Why = "your saved sign-in could not be used"
		if err != nil {
			ss.view.Why = err.Error()
		}
		return ss, nil
	}
	ss.secret, ss.held, ss.red = []byte(cred.Secret), true, newRedactor([]byte(cred.Secret))
	sp := &acp.SpawnSignin{Env: cred.Env + "=" + cred.Secret}
	for _, k := range prov.Keys { // none of the provider's other credential variables outranks it
		if k.Env != cred.Env {
			sp.Unset = append(sp.Unset, k.Env)
		}
	}
	if k := prov.KeyHome; k != nil && cred.Kind == signins.KindAPIKey {
		sp.KeyHome = &acp.SpawnKeyHome{Env: k.Env, Dir: k.Dir, File: k.File}
	}
	return ss, sp
}

// signinRestart is the sign-in a restarted session asks for (RestartAgent):
// what it was opened with and never more — a restart may be one nobody in
// the session asked for (a tile manager's protect or reassign). A session
// opened with none stays without; one opened on a named sign-in keeps that
// one, or none once it is gone; one the person's workspace agent started
// (which the agent still steers) keeps the sign-in its approval put in, or
// none. Only a session opened on the person's default asks for the default
// again, and the rule is judged anew.
func (m *Manager) signinRestart(s *Session, provider string) string {
	s.mu.Lock()
	ss, origin := s.sig(), s.origin
	s.mu.Unlock()
	if ss == nil || m.Signins == nil {
		return ""
	}
	want := ss.choice
	if origin != "" { // agent-started: exactly what it carried
		want = SigninNone
		if ss.carries() {
			want = ss.id
		}
	}
	if want == "" || want == SigninNone {
		return want
	}
	if sg, err := m.Signins.Peek(ss.user, provider, want); err != nil || sg == nil {
		return SigninNone // removed or revoked since: never another in its place
	}
	return want
}

// signinCredential is what the ACP client is told of a session that
// carries a saved sign-in: the key an adapter wants through authenticate,
// and where a refusal goes.
func (s *Session) signinCredential(m *Manager, prov agent.Provider, sp *acp.SpawnSignin) *sdkacp.Credential {
	ss := s.sig()
	if sp == nil || !ss.carries() {
		return nil
	}
	secret := string(ss.secret)
	c := &sdkacp.Credential{OnRefused: func(msg string) {
		m.Signins.MarkRefused(ss.user, ss.id, strings.ReplaceAll(msg, secret, signinMask))
	}}
	if sp.KeyHome != nil || prov.ID == "gemini" { // an adapter that takes a key only through authenticate
		c.APIKey = secret
	}
	return c
}

// signinHello takes the host's word on the saved sign-in — in the agent's
// environment, or left out because the CLI is signed in on its own in this
// $HOME — and says, in the session, what it runs on.
func (s *Session) signinHello(m *Manager, h acp.HelloParams) {
	ss := s.sig()
	if ss == nil {
		return
	}
	m.noteHome(s.homeKey, s.agent.provider.ID, h.HomeLogin || h.Signin == "home")
	s.mu.Lock()
	if ss.view.Source != "" { // the host says it once
		s.mu.Unlock()
		return
	}
	switch {
	case ss.carries() && h.Signin == "home":
		// the host left it out: nothing of it is in the sandbox
		for i := range ss.secret {
			ss.secret[i] = 0
		}
		ss.secret = nil
		ss.view.Source = SigninHome
		ss.view.Why = s.agent.provider.Name + " is signed in on its own in your home here, and starts on that login"
	case ss.carries():
		ss.view.Source = SigninSaved
	case h.LLM == sdkacp.ServeConnector:
		ss.view.Source = SigninConnector
	case h.HomeLogin:
		ss.view.Source = SigninHome
	default:
		ss.view.Source = SigninNoneYet
	}
	v := ss.view
	s.mu.Unlock()
	text := ""
	switch {
	case v.Source == SigninSaved && v.Name == "": // (a workspace agent's start: ticked on its approval, named there)
		text = "Using your saved sign-in, as you ticked on this start's approval."
	case v.Source == SigninSaved:
		text = "Using your saved sign-in " + v.Name + "."
	case v.Why != "" && v.Name == "": // (a workspace agent's start: no sign-in is named to it)
		text = "No saved sign-in is used in this session: " + v.Why + "."
	case v.Why != "":
		text = "Your saved sign-in " + v.Name + " isn't used here: " + v.Why + "."
		if v.Trust != nil && v.Trust.Allowed {
			// where to answer, for every client (the app has no card of its own)
			text += " To use it here, say you trust this tile's source: xbin asks in the tile's Agent tab, on the web."
		}
		switch v.Source {
		case SigninHome:
			text += " This session uses this tile's own login."
		case SigninConnector:
			text += " This session runs on the workspace's LLM connector."
		}
	}
	if text != "" {
		s.logEvent(m, agent.New(EvNotice, signinNotice{Text: text, Signin: v}))
		s.agent.logf(text)
	}
	m.changed("rename", s) // the directory row gained its sign-in: clients re-read it
}

// signinNotice is the notice a session logs about its sign-in: a notice's
// text, plus what a client's header shows.
type signinNotice struct {
	Text   string        `json:"text"`
	Signin SessionSignin `json:"signin"`
}

// signinView is s's sign-in for its row (nil: nothing to say). s.mu held.
func (s *Session) signinView() *SessionSignin {
	ss := s.sig()
	if ss == nil || (ss.view.Source == "" && ss.view.Why == "") {
		return nil
	}
	v := ss.view
	return &v
}

// carriesSignin reports a session whose agent holds a saved sign-in — or
// held one, or resumes a past session that did: its events, log and diff
// are its person's alone (partition.go adminPass, viewAsBarred), in history
// too (history.go).
func (s *Session) carriesSignin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sig()
	return ss != nil && (ss.held || ss.carries())
}

// redact is a line of the session's own log with its saved sign-in masked
// (as it came when the session holds none).
func (s *Session) redact(line string) string {
	return s.redactor().mask(line)
}

// redactEvents is e as it may leave the session (signinredact.go): masked,
// with the held ends of the streams it closes before it.
func (s *Session) redactEvents(e agent.Event) []agent.Event {
	return s.redactor().event(e)
}

func (s *Session) redactor() *redactor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.sig(); ss != nil {
		return ss.red
	}
	return nil
}

// dropSignin forgets the session's copy of the secret (the session ended).
func (s *Session) dropSignin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.sig(); ss != nil {
		for i := range ss.secret {
			ss.secret[i] = 0
		}
		ss.secret = nil
		ss.red.drop()
	}
}

// endForSignin ends s, saying why in it first.
func (m *Manager) endForSignin(s *Session, why string) {
	if s.agent != nil {
		s.logEvent(m, agent.New(EvNotice, noticeData{Text: why}))
		s.agent.logf(why)
	}
	slog.Info("agent session ended: its saved sign-in", "id", s.ID, "tile", s.Cwd)
	s.kill()
}

// endClaim claims s, which carries a saved sign-in, for ending: false when
// another sweep or removal already did — it is ending, said and counted
// there. s.mu held.
func (ss *sessionSignin) endClaim() bool {
	if ss.ending {
		return false
	}
	ss.ending = true
	return true
}

// EndSigninSessions ends user's running agent sessions that carry saved
// sign-in id ("": any saved sign-in), saying why in each, and answers how
// many it ended.
func (m *Manager) EndSigninSessions(user, id, why string) int {
	n := 0
	for _, s := range m.sorted() {
		s.mu.Lock()
		ss := s.sig()
		hit := ss.carries() && ss.user == user && (id == "" || ss.id == id) && ss.endClaim()
		s.mu.Unlock()
		if hit {
			m.endForSignin(s, why)
			n++
		}
	}
	return n
}

// EndSigninSessionsOn ends user's running sessions on tile that carry a
// saved sign-in (they stopped trusting its source), saying why.
func (m *Manager) EndSigninSessionsOn(user, tile, why string) int {
	n := 0
	for _, s := range m.sorted() {
		s.mu.Lock()
		ss := s.sig()
		hit := ss.carries() && ss.user == user && m.tileOf(s) == tile && ss.endClaim()
		s.mu.Unlock()
		if hit {
			m.endForSignin(s, why)
			n++
		}
	}
	return n
}

// SigninSweep judges the running sessions that carry a saved sign-in
// again, after people, their access or a tile's owner changed, or one of
// the workspace's switches did: with saved sign-ins switched off every one
// is ended; else one whose tile's source is no longer only its person's
// own, and isn't covered by their trust as it stands now — a new person
// can change it, it has another owner, it was made again — is (the next
// start asks). It answers how many it ended.
func (m *Manager) SigninSweep() int {
	// this is the sweep: the switch seen off here starts no other one
	// (signinsOff), which would race this one for the sessions it ends and
	// leave the count the switch's route reports short
	off := m.SigninsOff != nil && m.SigninsOff()
	m.offSeen.Store(off)
	m.trustOffSeen.Store(m.SigninTrustOff != nil && m.SigninTrustOff())
	if m.SigninSource == nil && !off {
		return 0
	}
	n := 0
	for _, s := range m.sorted() {
		s.mu.Lock()
		ss := s.sig()
		carries, user := ss.carries() && !ss.ending, ""
		if carries {
			user = ss.user
		}
		s.mu.Unlock()
		if !carries {
			continue
		}
		why := ""
		if off {
			why = "Saved coding-agent sign-ins were switched off for this workspace, so this session — which ran on yours — was ended."
		} else {
			tile, ok := m.TileOf(s.Cwd) // judged afresh, on the tile as the registry has it now
			var src signins.Source
			if ok && m.SigninSource != nil {
				src, ok = m.SigninSource(user, tile)
			}
			switch {
			case !ok: // its tile is gone from the registry: nothing vouches for it any more
				why = s.Cwd + " is no longer a registered tile, so this session — which ran on your saved sign-in — was ended."
			case !src.NeedsTrust() || m.Signins.Trusts(user, src):
			case !m.Signins.Policy().Trust.Allowed:
				why = "Trusting a tile's source was switched off for this workspace, and " + src.Tile + " isn't only yours, so this session — which ran on your saved sign-in — was ended."
			default:
				why = src.Tile + " isn't only yours — " + sourceWords(src) + " — and you haven't said you trust its source as it is now, so this session — which ran on your saved sign-in — was ended. Start it again: xbin asks first."
			}
		}
		if why == "" {
			continue
		}
		s.mu.Lock()
		claimed := s.sig().endClaim()
		s.mu.Unlock()
		if claimed {
			m.endForSignin(s, why)
			n++
		}
	}
	return n
}

// signinsOff reads the workspace's switch, and — the first time it is seen
// off — sweeps: the host's file can turn saved sign-ins off with no request
// to xbind, so every judgement of the rule (signinJudge) looks.
func (m *Manager) signinsOff() bool {
	if m.SigninsOff == nil {
		return false
	}
	off := m.SigninsOff()
	if !off {
		m.offSeen.Store(false)
	} else if !m.offSeen.Swap(true) {
		go m.SigninSweep()
	}
	return off
}

// signinTrustOff is signinsOff for the switch on trusting: the host's file
// can forbid it with no request to xbind, so every judgement of a tile's
// source looks — and the first time it is seen off, sweeps: the sessions
// that ran on a trust end.
func (m *Manager) signinTrustOff() {
	if m.SigninTrustOff == nil {
		return
	}
	if !m.SigninTrustOff() {
		m.trustOffSeen.Store(false)
	} else if !m.trustOffSeen.Swap(true) {
		go m.SigninSweep()
	}
}

// keyHomeDir makes the directory a non-isolated session's saved key lives
// in (isolation off: the host shares xbind's /tmp and is SIGKILLed at the
// end, so the daemon owns the directory and removes it with the session).
func keyHomeDir() (string, func()) {
	d, err := os.MkdirTemp("", "xbin-signin-")
	if err != nil {
		return "", func() {}
	}
	return d, func() { _ = os.RemoveAll(d) }
}
