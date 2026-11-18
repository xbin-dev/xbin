package term

// home.go — which home a session mounts (D245;
// plans/wsagent/home-on-untrusted-tiles.md). A person has ONE home,
// homes/<user>, and every session of theirs used to mount it read-write.
// Code in a tile someone else controls runs in such a session, and what it
// wrote there — ~/.claude/settings.json, ~/.gitconfig, ~/.bashrc — ran
// later on the person's own tile, beside their saved sign-in.
//
// THE RULE (homePlanFor; the one place it is decided, for every session
// that mounts a home, on the registered tile the session is on): on a tile
// whose source needs the person's trust (signins.Source.NeedsTrust — owned
// by someone else, or changeable by someone else who isn't a workspace
// admin) and has not got it (the store's Trusts: the sign-in's own answer,
// one per person and tile identity), the home is a COPY: the session reads
// the person's real home, and every write goes to a layer kept for that
// person on that tile (homecopy.go), never to the real home. Everywhere
// else it is the person's OWN home, read-write, as it always was.
//
// It is fixed when the session starts and shown on its row. A restart, and
// a resume of a past session, never widen: one that ran on the copy stays
// on it (keep). A trust that stops holding ends the sessions that have the
// real home on that tile (HomeSweep) — they could otherwise keep writing;
// sessions on the copy are never ended by a trust change. A session with
// its person's real home is its person's alone: an admin has no pass into
// it (adminPass, mayReattach), since typing there is writing that home.
//
// Without sandboxes there is nothing to mount: a session is a process of
// xbind's own user with the real home, and nothing is protected.

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/signins"
)

// A session's home (SessionHome.Mode).
const (
	HomeOwn  = "own"  // the person's real home, read-write
	HomeCopy = "copy" // a copy-on-write view of it: writes stay on the tile
)

// HomeDocs is where the home rule is explained to a person.
const HomeDocs = "/docs/agent-signins.md#your-home-on-a-tile-that-isnt-only-yours"

// SessionHome is a session's home, for its row (SessionInfo.Home) and its
// first notice.
type SessionHome struct {
	// Mode: "own" — home: yours — or "copy" — home: this tile's copy
	// (changes stay on this tile).
	Mode string `json:"mode"`
	// Why it is the tile's copy; or, for "own", what there is to know
	// (a workspace without sandboxes protects nothing).
	Why string `json:"why,omitempty"`
	// Kept: the session is on the copy because it started (or its past
	// session ran) on it — a new session may get the real home.
	Kept bool `json:"kept,omitempty"`
	// Unsandboxed: the workspace runs without sandboxes.
	Unsandboxed bool `json:"unsandboxed,omitempty"`
	// Confined: the person is a workspace admin and this session, being on
	// the tile's copy, is confined like a member's until they trust the
	// tile (homePlan.confines).
	Confined bool `json:"confined,omitempty"`
	// Trust: the question whose answer gives new sessions on this tile the
	// real home (the sign-in's own; POST /agent/signins/trust). Absent
	// when nothing the person can answer would change it.
	Trust *SigninTrust `json:"trust,omitempty"`
	Docs  string       `json:"docs"`
}

// homePlan is a new session's home, decided before the session exists
// (openOpts.home).
type homePlan struct {
	mode string // "" = no plan was made: the real home, as before (a manager with no rule wired)
	why  string
	kept bool
	// guarded: sandboxes are on and the rule was asked — the mode is what
	// the session's mounts enforce
	guarded bool
	// refuse: the session can't have the home the rule gives it (a copy
	// without a tile identity): the open fails with this, and never falls
	// back to the real home
	refuse string
	user   string         // whose home (the account id; "" the root token's)
	src    signins.Source // the tile, as the rule saw it: a copy's layer is keyed by its identity
	trust  *SigninTrust
	// admin: the person is a workspace admin (or the root token): on a copy
	// their session is confined like a member's all the same (confines)
	admin bool
	// gen is the sweep generation the plan was made under (Manager.judged):
	// a session registered under a later one is judged again
	gen uint64
}

// confines reports a plan whose session runs with a member's confinement
// whoever its person is: its home is the tile's copy. An unrestricted
// sandbox keeps CAP_SYS_ADMIN in its user namespace, and with it the
// tile's code could bind the workspace again without its masks, remount
// that read-write and write the person's real home — the very thing the
// copy is for. So on a tile a person has not trusted, an admin's session
// is a member's: the allow-list view, the capability and namespace
// lockdown, the resource leaf. Trusting the tile gives new sessions there
// the admin's sandbox back.
func (pl homePlan) confines() bool { return pl.guarded && pl.mode == HomeCopy }

// sessionHome is a live session's home, fixed at its start.
type sessionHome struct {
	plan homePlan
	dir  string // the copy's layer directory ("" for the real home)
	told bool   // the session said, once, what the copy means (the first on this layer)
}

// homeView is a plan as a row shows it.
func (pl homePlan) view(m *Manager, agentRow bool) *SessionHome {
	if pl.mode == "" {
		return nil
	}
	v := &SessionHome{Mode: pl.mode, Why: pl.why, Kept: pl.kept, Unsandboxed: !m.Isolate, Confined: pl.confines() && pl.admin, Docs: HomeDocs}
	if pl.refuse != "" {
		v.Why = pl.refuse // a session would not start: why
	}
	if agentRow {
		// a session the person's workspace agent started is read by it: the
		// mode, and nothing of who owns the tile or who can change it
		v.Why, v.Trust = "", nil
		return v
	}
	if pl.mode == HomeCopy {
		v.Trust = pl.trust
	}
	return v
}

// HomeWords is a home mode as every client says it.
func HomeWords(mode string) string {
	if mode == HomeCopy {
		return "home: this tile's copy (changes stay on this tile)"
	}
	return "home: yours"
}

// rootHome reports the root token's own sessions — the token itself, and
// a shell's token minted for it (bx agent in the root token's terminal):
// the workspace's own admin, with no account. Its home is homes/owner.
func rootHome(p auth.Principal) bool {
	return p.UserID == "" && (p.Owner || (p.Component != "" && p.Via == "terminal"))
}

// sourcePhrase says why src's code isn't only the person's own: "<tile>
// isn't only yours — it belongs to bob", or, where it is theirs alone now
// and was not before, "<tile> wasn't always only yours — bob could change
// it until 2026-11-18".
func sourcePhrase(src signins.Source) string {
	if !src.NotOwn && !(signins.CoEditorsNeedTrust && len(src.Editors) > 0) && len(src.Past) > 0 {
		return src.Tile + " wasn't always only yours — " + sourceWords(src)
	}
	return src.Tile + " isn't only yours — " + sourceWords(src)
}

// homePlanFor decides the home of a session p opens on tile — the
// registered tile the session's directory is or lies in (sessionTile).
// keep: the session restarts or resumes one that ran on the tile's copy.
func (m *Manager) homePlanFor(p auth.Principal, tile string, keep bool) homePlan {
	// the generation first: a sweep that starts after this line judges the
	// session again when it registers (registered)
	pl := homePlan{mode: HomeOwn, user: p.UserID, admin: p.IsAdmin() || rootHome(p), gen: m.judged.Load()}
	if !m.Isolate {
		pl.why = "this workspace runs without sandboxes: every session is a process of xbind's own user with your real home, and nothing protects it from a tile's code"
		return pl
	}
	if m.SigninSource == nil {
		// no rule is wired (boot wires one whenever sandboxes are on): the
		// home is mounted as it always was, and nothing claims otherwise
		pl.mode = ""
		return pl
	}
	pl.guarded = true
	m.signinTrustOff() // (trusting seen forbidden for the first time: the sessions on a trust end)
	src, ok := m.SigninSource(p.UserID, tile)
	if !ok {
		pl.mode, pl.refuse = HomeCopy, tile+" is in no registered tile, so xbind can't tell whose code would run with your home"
		return pl
	}
	if rootHome(p) {
		// the root token: the workspace's own admin, with no account — a
		// workspace-owned tile is its own, anyone else's is not
		src.Admin, src.NotOwn = true, src.Owner != ""
	}
	pl.src = src
	copyOf := func(why string) homePlan {
		pl.mode, pl.why = HomeCopy, why
		if src.ID == "" {
			pl.refuse = "xbind can't tell " + src.Tile + " from another tile made at its path, so it can't keep a copy of your home for it — and your real home is not opened there: " + why
		}
		return pl
	}
	// whose session it is: only a person's own (theirs, or their workspace
	// agent's for them) can have their real home at all
	switch {
	case p.ReadOnly():
		return copyOf("viewing as someone never opens their real home")
	case p.Component != "" && p.Via == "terminal":
		// a shell's own token (bx agent in a terminal), on its own tile:
		// never more than the session it was minted for. Not "some session
		// of the person's there": a shell kept on the copy from before a
		// trust — which an admin may be attached to — would open, and drive,
		// a session with the real home through its own token
		if !m.minterHasRealHome(p, tile) {
			return copyOf("a session opened with a terminal's token gets your real home only when that terminal has it")
		}
	case p.Component != "":
		return copyOf("a tile's credentials never open a person's real home")
	}
	if keep {
		pl.kept = true
		return copyOf("this session started on this tile's copy of your home and stays on it — a new session is judged anew")
	}
	if !src.NeedsTrust() || (m.Signins != nil && m.Signins.Trusts(p.UserID, src)) {
		return pl // only theirs, and always was — or they said they trust its source
	}
	why := sourcePhrase(src) + " — and you haven't said you trust its source"
	switch {
	case m.Signins == nil:
	case !signinPerson(p) || p.Agent != "":
		if rootHome(p) {
			why += " (the root token keeps no trust answers: sign in as a person to trust a tile)"
		}
	default:
		pol := m.Signins.Policy().Trust
		pl.trust = trustNeed(src, pol)
		if !pol.Allowed {
			why = sourcePhrase(src) + " — and trusting a tile's source is switched off for this workspace"
		}
	}
	return copyOf(why)
}

// minterHasRealHome reports whether the session p's terminal token was
// minted for — the shell `bx agent` runs in — is on tile, is that
// person's, and has the real home.
func (m *Manager) minterHasRealHome(p auth.Principal, tile string) bool {
	if p.TermRef == "" {
		return false
	}
	for _, s := range m.sorted() {
		if s.token != "" && auth.TerminalRef(s.token) == p.TermRef {
			return s.homeKey == HomeKey(p) && m.tileOf(s) == tile && s.home.plan.mode == HomeOwn
		}
	}
	return false
}

// realHome reports a session that has its person's real home under the
// rule (sandboxes on): its person's alone.
func (s *Session) realHome() bool {
	return s.home.plan.guarded && s.home.plan.mode == HomeOwn
}

// HomeRule answers, for a launcher or a status bar, the home a session p
// opens on tile would get now (nil: this manager decides nothing).
func (m *Manager) HomeRule(p auth.Principal, tile string) *SessionHome {
	return m.homePlanFor(p, tile, false).view(m, false)
}

// homeBind is the bind of the session's home: the real one, or a clone of
// the tile's copy (homecopy.go; dir is then its layer). started is called
// once the session's sandbox has started (its init then holds its own copy
// of the mount), release once that sandbox is gone — or at once when it
// never starts. A copy that cannot be had is an error: there is no way
// from here to a bind of the real home for a session the rule gives the
// copy (D78).
func (m *Manager) homeBind(homeDir string, o openOpts) (b *sandbox.Bind, dir string, started, release func(), err error) {
	none := func() {}
	pl := o.home
	if pl.refuse != "" {
		return nil, "", none, none, errors.New(pl.refuse)
	}
	switch {
	case pl.mode == HomeCopy:
	case pl.mode == "" && m.SigninSource != nil:
		// a session nobody decided a home for, where the rule is wired: a
		// caller that skipped openOptsFor. Never the real home by default
		return nil, "", none, none, errors.New("no home was decided for this session (xbind's own fault): it is not started")
	default:
		return realHomeBind(homeDir), "", none, none, nil // the person's real home: read-write
	}
	if dir, err = m.homeCopyDir(o.homeKey, pl.src); err != nil {
		return nil, "", none, none, fmt.Errorf("this tile's copy of your home could not be mounted, and your real home is not opened on %s: %w", pl.src.Tile, err)
	}
	f, rel, err := m.copies.acquire(m, dir, homeDir, pl.src)
	if err != nil {
		return nil, "", none, none, fmt.Errorf("this tile's copy of your home could not be mounted, and your real home is not opened on %s: %w", pl.src.Tile, err)
	}
	return &sandbox.Bind{Src: homeDir, Dst: homeDir, Mount: f}, dir, func() { f.Close() }, func() { f.Close(); rel() }, nil
}

// homeNote is what a session on the tile's copy says first, once per
// layer: what changed, why, and how to get the old behaviour there.
func (m *Manager) homeNote(o openOpts) (lines []string, ok bool) {
	if o.home.mode != HomeCopy || o.launch == nil || o.launch.homeCopy == "" || !m.copies.tell(o.launch.homeCopy) {
		return nil, false
	}
	tile := o.home.src.Tile
	lines = []string{
		"Your home here is this tile's copy: what you write under ~ on " + tile + " stays on " + tile + ", and your real home is not changed. Everything in your home can still be read here.",
		"Why: " + o.home.why + ". Code in a tile that isn't only yours could otherwise leave a file in your home that runs later on your own tiles.",
	}
	if o.home.admin {
		lines = append(lines, "Until you trust this tile, this session is confined like a member's: it sees the workspace read-only through the tiles you may read, and cannot mount, or open new namespaces.")
	}
	switch {
	case o.home.trust != nil && o.home.trust.Allowed:
		lines = append(lines, "To have your real home here, as before: trust this tile's source — on the web, the home line in this tile's window (its status bar) asks, once. New sessions then use your real home. More: "+HomeDocs)
	default:
		lines = append(lines, "More: "+HomeDocs)
	}
	return lines, true
}

// homeNotice is the notice an agent session logs about its home: a
// notice's text, plus what a client's header shows.
type homeNotice struct {
	Text string       `json:"text"`
	Home *SessionHome `json:"home"`
}

// sayHome says the copy's note in a new session: a shell's first lines, an
// agent session's notice.
func (s *Session) sayHome(m *Manager, o openOpts) {
	lines, ok := m.homeNote(o)
	if !ok {
		return
	}
	s.home.told = true
	if s.kind == KindAgent {
		s.logEvent(m, agent.New(EvNotice, homeNotice{Text: strings.Join(lines, " "), Home: o.home.view(m, o.origin != "")}))
		return
	}
	for _, l := range lines {
		s.hub.Output([]byte(greyLine("[xbin] " + l)))
	}
}

// endForHome ends s, saying why in it first.
func (m *Manager) endForHome(s *Session, why string) {
	if s.agent != nil {
		s.logEvent(m, agent.New(EvNotice, noticeData{Text: why}))
		s.agent.logf(why)
	} else {
		s.hub.Output([]byte("\r\n" + greyLine("[xbin] "+why)))
	}
	slog.Info("session ended: its home", "id", s.ID, "tile", m.tileOf(s), "kind", s.kind)
	s.kill()
}

// claimEnd claims s for ending for its home: false when a sweep already
// did.
func (s *Session) claimEnd() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.homeEnding {
		return false
	}
	s.homeEnding = true
	return true
}

// HomeSweep judges the running sessions that have their person's real home
// again — after people, their access, a tile's owner or the tiles
// themselves changed, a trust was revoked or went void, or trusting was
// switched off: one whose tile now needs the person's trust and has not
// got it is ended (the next one there starts on the tile's copy). It
// answers how many it ended. Sessions on a copy are left alone.
func (m *Manager) HomeSweep() int {
	// a session whose plan was made before this line and that registers
	// after the listing below is judged when it registers (registered)
	m.judged.Add(1)
	if !m.Isolate || m.SigninSource == nil {
		return 0
	}
	n := 0
	for _, s := range m.sorted() {
		if m.homeJudge(s) {
			n++
		}
	}
	return n
}

// homeJudge judges one running session that has its person's real home
// again, and ends it when its tile now needs the person's trust and has
// not got it. It reports whether it ended it.
func (m *Manager) homeJudge(s *Session) bool {
	if !s.realHome() {
		return false
	}
	user := s.home.plan.user
	tile, ok := m.TileOf(s.Cwd) // judged afresh, on the tile as the registry has it now
	var src signins.Source
	if ok {
		src, ok = m.SigninSource(user, tile)
	}
	why := ""
	switch {
	case !ok:
		why = s.Cwd + " is no longer a registered tile, so this session — which had your real home — was ended."
	default:
		if user == "" { // the root token's (homePlanFor)
			src.Admin, src.NotOwn = true, src.Owner != ""
		}
		if !src.NeedsTrust() || (m.Signins != nil && m.Signins.Trusts(user, src)) {
			return false
		}
		why = sourcePhrase(src) + " — and you haven't said you trust its source as it is now, so this session — which had your real home — was ended. A new one there starts on this tile's copy of your home."
	}
	if s.claimEnd() {
		m.endForHome(s, why)
		return true
	}
	return false
}

// registered is told a session is in the manager's list now. A sweep that
// ran while it was being opened — its home was decided, and its saved
// sign-in chosen, before; a VM takes a while to start — never saw it: when
// the generation moved since its plan, it is judged now, as that sweep
// would have judged it, for its home and for the sign-in it carries. And a
// session on a copy that is being forgotten (ForgetHomeCopy) ends: the
// copy goes once its sessions have.
func (m *Manager) registered(s *Session) {
	if s.home.dir != "" && m.copies.forgetting(s.home.dir) {
		if s.claimEnd() {
			m.endForHome(s, "What this tile wrote to its copy of your home was being forgotten while this session started, so it was ended. Start it again.")
		}
		return
	}
	if s.home.plan.gen == m.judged.Load() {
		return
	}
	if !m.homeJudge(s) {
		m.signinJudgeLive(s) // signin.go: the sign-in it carries, by the rule as it stands
	}
}

// historyHome is what a session's history entry records of its home: the
// copy, or nothing.
func (s *Session) historyHome() string {
	if s.home.plan.guarded && s.home.plan.mode == HomeCopy {
		return HomeCopy
	}
	return ""
}

// homeRow is s's home for its row.
func (s *Session) homeRow(m *Manager) *SessionHome {
	return s.home.plan.view(m, s.origin != "")
}
