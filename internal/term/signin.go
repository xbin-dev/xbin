package term

// signin.go — a person's saved coding-agent sign-in in their agent sessions
// (D230; docs/agent-signins.md). xbind keeps the sign-ins (internal/signins)
// and, when a person's agent session starts, puts their default one for the
// session's coding agent into the agent's environment — through the spawn
// frame, to the agent's process alone (the path the LLM connector's key
// takes) — where the rule below allows it.
//
// THE RULE (signinWhy; the one place it is decided — the launcher and the
// UI repeat its answer). A session is a sandbox of its own, so nobody else
// reads its environment. But what runs in it comes from the tile's
// persistent environment layer (the binaries, shared by everyone who may
// open a terminal on the tile) and the tile's directory (the project's own
// hooks and scripts): someone else with terminal access to the tile can
// change what the session runs, and so reach what it holds. A saved sign-in
// therefore goes in only when
//
//  1. the session is its person's own: opened by their own signed-in
//     session, app or device — or by their workspace agent, and then only
//     where the person approved a start that named the sign-in (the agent
//     steers such a session and reads what it prints) — never view-as, a component's credentials (a tile's
//     frame or backend, a terminal's or an agent session's token), the root
//     token;
//  2. sandboxes are on (a development workspace excepted: SigninDev);
//  3. nobody else can change what the sandbox runs: no other person who
//     isn't a workspace admin holds write level or more on the tile the
//     session's directory lies in (SigninOthers: by any source — ownership,
//     an entry, a pattern, an org). A partitioned tile is no exception: a
//     partition keeps people's data apart, and the tile's directory — its
//     code — is bound read-write into every partition's sessions. Admins
//     aren't counted: they can change any tile and are in the trust base;
//  4. the connector isn't the only one allowed to serve the session, and
//     the person didn't choose it.
//
// Otherwise the session starts without it and says why. A session that
// carries one is its person's alone: no admin pass to drive or read it
// (adminPass, viewAsBarred), and it is ended when someone gains terminal
// access to its tile (SigninSweep), when the sign-in is removed or replaced
// (EndSigninSessions), and when its person is signed out.
//
// What xbind keeps of such a session is redacted (redact): the exact secret
// in an event's data or a log line becomes "[saved sign-in]" before it is
// logged, published or written to history.

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
}

// SigninHook is the Manager's share of saved sign-ins: what boot wires.
type SigninHook struct {
	// Signins is the store; nil ⇒ sessions are exactly as before.
	Signins SigninStore
	// SigninOthers answers, for a tile (the rule resolves a session's
	// directory with TileOf first; the broker's answer resolves again and
	// says the tile it judged), the registered tile and the
	// other people — enabled, not workspace admins — who hold write level or
	// more there (the broker's SigninOthers): each could change what a
	// session there runs. ok false: the path lies in no tile. nil ⇒ unknown,
	// and no saved sign-in goes into any session.
	SigninOthers func(user, path string) (tile string, others []string, ok bool)
	// SigninsOff reports saved sign-ins switched off for the workspace (the
	// admins' switch, or the host's file): running sessions that carry one
	// are ended, as when a sign-in is removed (SigninSweep). nil ⇒ never.
	SigninsOff func() bool
	offSeen    atomic.Bool
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
}

// sessionSignin is a live session's: what it carries. secret is the
// credential in its agent's environment (nil: none) — kept to redact what
// the session prints, never logged, dropped when the session ends.
type sessionSignin struct {
	view   SessionSignin
	user   string
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
// on tile would get: the person's saved sign-in (nil: none) and why it
// would be left out ("" = it goes in). forced: the connector alone may
// serve there.
func (m *Manager) SigninPreview(p auth.Principal, tile string, prov agent.Provider, forced bool) (*signins.Signin, string) {
	if m.Signins == nil {
		return nil, ""
	}
	var o openOpts
	pl := m.signinPlanFor(p, o, tile, prov, "", forced, false, nil)
	return pl.saved, pl.why
}

// SigninForApproval answers, for the approval of a start by p — a
// person's workspace agent — on tile: the person's default sign-in for
// prov that the start would use if they approve it (nil: none, or it would
// not go in — why says). The approval names it by its id, and the open is
// bound to that id (DelegateStart.Signin).
func (m *Manager) SigninForApproval(p auth.Principal, tile string, prov agent.Provider, forced bool) (*signins.Signin, string) {
	if m.Signins == nil || p.Agent == "" {
		return nil, ""
	}
	def, err := m.Signins.Peek(p.UserID, prov.ID, "")
	if err != nil || def == nil {
		return nil, ""
	}
	pl := m.signinPlanFor(p, openOpts{}, tile, prov, "", forced, false, &DelegateStart{Approved: true, Signin: def.ID})
	if pl.saved == nil || pl.why != "" {
		return nil, pl.why
	}
	return pl.saved, ""
}

// SigninRule answers the rule alone for a launcher or a sign-in card: why a
// saved sign-in of p's would not go into a session of prov they open on
// tile ("" = it would), whether or not they have one saved yet.
func (m *Manager) SigninRule(p auth.Principal, tile string, prov agent.Provider) string {
	if m.Signins == nil {
		return "this xbind keeps no saved sign-ins"
	}
	var o openOpts
	o.userID = p.UserID
	return m.signinWhy(p, o, tile, prov, m.credentialRule(o, tile, prov.ID, "").Forced, false)
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
		// a workspace agent's start: the agent steers the session and reads
		// what it prints, so a saved sign-in goes in only where the person
		// approved a start that named it (DelegateStart.Signin: xbind's own
		// words on the approval) — never by default, never on a start their
		// preference lets through unasked — and then exactly that one
		if d == nil || !d.Approved || d.Signin == "" {
			if sg, err := m.Signins.Peek(p.UserID, prov.ID, ""); err == nil && sg != nil {
				pl.saved = sg
				pl.why = "your workspace agent started this session, and a saved sign-in goes into such a session only when you approve a start that names it"
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
	pl.why = m.signinWhy(p, o, rel, prov, forced, chosen)
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

// signinWhy is the rule: why a saved sign-in of p's does not go into a
// session of prov they open on rel ("" = it does).
func (m *Manager) signinWhy(p auth.Principal, o openOpts, rel string, prov agent.Provider, forced, chosen bool) string {
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
	case m.SigninOthers == nil:
		return "xbind can't tell who else can change " + rel
	}
	// the tile the directory lies in, never the directory as written: a
	// person's access is to the tile, and its hooks run in every directory
	// of it — on a partitioned tile too, whose directory is shared code
	tile, ok := m.TileOf(rel) // the one resolver every session decision uses (sessiondir.go)
	var others []string
	if ok {
		tile, others, ok = m.SigninOthers(p.UserID, tile)
	}
	if !ok {
		return rel + " is in no registered tile, so xbind can't tell who else can change what runs there"
	}
	if len(others) > 0 {
		return othersWords(others) + " can also change " + tile + " and could change what your agent runs there"
	}
	return ""
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
		return ss, nil
	}
	ss.view.ID, ss.view.Name = pl.saved.ID, pl.saved.Name
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
			want = ss.view.ID
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
		m.Signins.MarkRefused(ss.user, ss.view.ID, strings.ReplaceAll(msg, secret, signinMask))
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
	case v.Source == SigninSaved:
		text = "Using your saved sign-in " + v.Name + "."
	case v.Why != "":
		text = "Your saved sign-in " + v.Name + " isn't used here: " + v.Why + "."
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

// EndSigninSessions ends user's running agent sessions that carry saved
// sign-in id ("": any saved sign-in), saying why in each, and answers how
// many it ended.
func (m *Manager) EndSigninSessions(user, id, why string) int {
	n := 0
	for _, s := range m.sorted() {
		s.mu.Lock()
		ss := s.sig()
		hit := ss.carries() && ss.user == user && (id == "" || ss.view.ID == id)
		s.mu.Unlock()
		if hit {
			m.endForSignin(s, why)
			n++
		}
	}
	return n
}

// SigninSweep judges the running sessions that carry a saved sign-in
// again, after people or their access changed, or the workspace's switch
// did: with saved sign-ins switched off every one is ended; else one whose
// tile someone else can now change is (the next start is judged afresh and
// says why it runs without). It answers how many it ended.
func (m *Manager) SigninSweep() int {
	off := m.signinsOff()
	if m.SigninOthers == nil && !off {
		return 0
	}
	n := 0
	for _, s := range m.sorted() {
		s.mu.Lock()
		ss := s.sig()
		carries, user := ss.carries(), ""
		if carries {
			user = ss.user
		}
		s.mu.Unlock()
		if !carries {
			continue
		}
		if off {
			m.endForSignin(s, "Saved coding-agent sign-ins were switched off for this workspace, so this session — which ran on yours — was ended.")
			n++
			continue
		}
		tile, ok := m.TileOf(s.Cwd) // judged afresh, on the tile as the registry has it now
		var others []string
		if ok && m.SigninOthers != nil {
			tile, others, ok = m.SigninOthers(user, tile)
		}
		if !ok { // its tile is gone from the registry: nothing vouches for it any more
			m.endForSignin(s, s.Cwd+" is no longer a registered tile, so this session — which ran on your saved sign-in — was ended.")
			n++
		} else if len(others) > 0 {
			m.endForSignin(s, othersWords(others)+" can now change "+tile+", so this session — which ran on your saved sign-in — was ended. Start it again: it will say what it runs on.")
			n++
		}
	}
	return n
}

// signinsOff reads the workspace's switch, and — the first time it is seen
// off — sweeps: the host's file can turn saved sign-ins off with no request
// to xbind, so every judgement of the rule (signinWhy) looks.
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
