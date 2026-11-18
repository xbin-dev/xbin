package term

// AGENT SESSIONS (D74): a terminal session whose sandbox runs a coding-agent
// CLI instead of a shell. The sandbox is the shell's (same mounts, home,
// net scope, tile token); the entry is the daemon's own bx bound in as
// `bx __agent-host` (internal/agent/host), which spawns the agent from the
// first frame we send and proxies ACP between us and it — its stdio are
// pipes, there is no PTY and no socket client. The driver
// (internal/agent/acp) turns the protocol into agent.Events; agentPump
// numbers them into the session's log (replay by cursor) and hands each to
// Manager.OnEvent (the live `session` event). Prompts, cancels and
// permission answers come through the Manager from the API; any client may
// answer a permission — the first wins. The agent authenticates from the
// session's per-user $HOME (D6) — the same home a shell terminal gets, so a
// `claude auth login` / `codex login` / … done once in a terminal serves the
// agent on every tile. There are no provider keys in the tile vault. With an
// LLM connector configured the CLI may be pointed at it instead, with a key
// of the session's own (llmhook.go).

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agent/acp"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/termwire"
	"github.com/xbin-dev/xbin/internal/util"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// Session kinds (SessionInfo.kind).
const (
	KindShell = "shell"
	KindAgent = "agent"
)

const (
	agentHostPath = "/opt/xbin/bin/bx" // where the daemon's bx is bound inside an agent sandbox
	maxAgentText  = 64 << 10           // the session's text log (host stderr + notes)
	deltaCoalesce = 32 * time.Millisecond
)

// Version is what the driver reports to the agent as the client version
// (set by boot).
var Version = "dev"

// Errors the API maps to statuses.
var (
	ErrNoSession    = errors.New("no such session")
	ErrNotAgent     = errors.New("not an agent session")
	ErrNoPermission = errors.New("no such pending permission")
	ErrNoQuestion   = errors.New("no such pending question")
	ErrForbidden    = errors.New("forbidden")
	errLimit        = errors.New("")
)

// SessionEvent is one logged event as published on the hub (`session`
// events on /ws/events): the event, flattened, plus whose session it is
// and which. Owner is the events filter's hook.
type SessionEvent struct {
	agent.Event
	User string `json:"user"`
	ID   string `json:"id"`
	// private: the session carries its person's saved sign-in (signin.go)
	// and is theirs alone — no admin follows it.
	private bool
}

func (e SessionEvent) Owner() string { return e.User }

// PrivateSessionEvent is an event of a session that carries its person's
// saved sign-in, for a consumer's tests of who may follow it.
func PrivateSessionEvent(user, id string) SessionEvent {
	return SessionEvent{User: user, ID: id, private: true}
}

// Private reports an event of a session only its person may follow: one
// that carries their saved sign-in (signin.go).
func (e SessionEvent) Private() bool { return e.private }

// agentState is the agent half of a KindAgent session.
type agentState struct {
	drv       agent.Driver
	log       *agent.Log
	perms     *agent.Permissions
	provider  agent.Provider
	ready     chan struct{} // closed once the driver's Start returned
	never     chan struct{} // closed when the session ended before its driver started: the pump ends it
	done      chan struct{} // closed once the pump ended (the session is over)
	gone      chan struct{} // closed after the teardown: history saved, layer released, row removed
	mu        sync.Mutex
	startErr  error
	mode      string
	model     string // the agent's current model option, when it exposes one
	status    string
	turn      uint64
	text      []byte
	acpID     string              // the agent's own session id (after the handshake)
	loadable  bool                // the agent can reopen acpID later (session/load) — resume
	resumed   string              // the history entry this session reopened (superseded when this one is saved)
	snap      *snapper            // files.changed snapshots of the tile (agentdiff.go); nil = off
	published sdkacp.Summary      // the last summary handed to OnStatus (agentstatus.go)
	prompting atomic.Bool         // a prompt is being taken (ReservePrompt)
	attachDir string              // the daemon's attachments dir (isolation off), removed with the session
	replay    replayState         // a resume's replay, logged but not published one by one (agentpage.go); under mu
	queue     []queueItem         // messages held for the running turn's end (agentqueue.go, D197); under mu
	flushing  bool                // the queue is being sent as a prompt; under mu
	queueing  atomic.Bool         // a message for the queue is being taken (ReserveQueue)
	det       sdkacp.DetachedTurn // a turn the agent runs on its own (agentdetached.go); under mu
	statusMu  sync.Mutex          // a status's logging, one step against det's begin (logEvent, beginDetached); before mu
	quiet     *time.Timer         // looks at det's deadline; under mu
	said      []byte              // the tail of what the agent wrote since its last prompt (agentstatus.go); under mu
	llm       *llmSession         // the session's hold on the LLM connector (llmhook.go); nil = none
	llmChoice string              // the session's own choice of who serves it, kept for a restart
	redact    func(string) string // masks the session's saved sign-in in a log line (signin.go); nil = none
	signin    *sessionSignin      // the session's sign-in and the saved one it carries (signin.go); under the Session's mu

	// killed: the session is being ended (Session.kill) — the turn that
	// ends with its agent flushes no held message into it (flushQueue):
	// they are dropped at the teardown, and said so (dropQueue)
	killed atomic.Bool
}

func (st *agentState) logf(line string) {
	line = maskLLMKeys(line) // a hook that printed the environment (llmhook.go)
	if st.redact != nil {
		line = st.redact(line) // …or the saved sign-in (signin.go)
	}
	st.mu.Lock()
	st.text = append(st.text, line...)
	st.text = append(st.text, '\n')
	if len(st.text) > maxAgentText {
		st.text = st.text[len(st.text)-maxAgentText:]
	}
	st.mu.Unlock()
}

// OpenAgent opens an agent session for p on a tile (the API's POST
// /term/sessions): the gates, the provider and mode, then createAgent.
// The int is the HTTP status for a refusal.
func (m *Manager) OpenAgent(p auth.Principal, cwd, netMode, providerID, mode, name, resume string, options map[string]string) (SessionInfo, int, error) {
	return m.OpenAgentWith(p, AgentOpen{Cwd: cwd, Net: netMode, Provider: providerID, Mode: mode, Name: name, Resume: resume, Options: options})
}

// AgentOpen is everything an agent session opens with: where, the
// sandbox's pickers (the same as a shell's: network scope, tile-API access,
// GPU), the target deployment, the provider and its mode/settings, a name, a
// past session to resume.
type AgentOpen struct {
	Cwd, Net, GPU, Provider, Mode, Name, Resume string
	NoAPI                                       bool // a code-only sandbox: no terminal token (api=0 on a shell)
	VM                                          bool // a VM sandbox (vm.go)
	Options                                     map[string]string
	// Deployment is the requested target (D127p), from ?deployment=: a deployment
	// of the tile, "" for the default (target.go). A resumed session doesn't
	// take its history entry's: the client names it again.
	Deployment string
	// LLM is the session's own choice of who serves its model calls where
	// the workspace has an LLM connector: "connector", "own", or "" — the
	// person's saved default, then the workspace's (llmhook.go).
	LLM string
	// Signin is the session's own choice of saved sign-in (signin.go): ""
	// (the person's default for the harness), "none", or a sign-in's id.
	Signin string
	// Delegate says how a delegate's open was authorised (delegate.go): the
	// server's delegate gate fills it; nil for everyone else, and a
	// delegate's open without it is refused.
	Delegate *DelegateStart
	origin   string // a restart: the session's origin carries over
	// home: a restart — HomeCopy when the session it restarts was on the
	// tile's copy of the home: it stays on it (home.go)
	home string
	// originMode: a restart — the mode the session was first opened in by
	// its origin carries over too (the restart's own mode is the person's)
	originMode string
}

// OpenAgentWith is OpenAgent with the sandbox pickers.
func (m *Manager) OpenAgentWith(p auth.Principal, a AgentOpen) (SessionInfo, int, error) {
	cwd, netMode, providerID, mode, name, resume, options := a.Cwd, a.Net, a.Provider, a.Mode, a.Name, a.Resume, a.Options
	if cwd == "" {
		return SessionInfo{}, 403, errors.New("an agent session runs on a tile — pass cwd")
	}
	_, rel, err := util.SafeJoin(m.Root, cwd)
	tile := ""
	if err == nil && rel != "" {
		var why string
		if tile, why = m.sessionTile(rel); why != "" { // the level, rules and target are the tile's (sessiondir.go)
			return SessionInfo{}, 403, errors.New(why)
		}
	}
	if err != nil || rel == "" || !p.CanTerminalTileVia(tile) {
		return SessionInfo{}, 403, errors.New(noTerminalLevel)
	}
	// resume: reopen one of the caller's past sessions on this tile
	// (history.go) — its provider, mode and name carry over; the agent must
	// have advertised loadSession, else 409 and the UI offers a fresh start
	var resumeID string
	var resumeFrom *HistoryMeta
	var fromPart bool // the entry is partition history (resumeHere)
	if resume != "" {
		var meta HistoryMeta
		meta, fromPart, err = m.historyMetaFrom(HistoryOf(p), resume)
		if err != nil {
			return SessionInfo{}, 404, fmt.Errorf("no such past session %q", resume)
		}
		if meta.Cwd != rel {
			return SessionInfo{}, 400, errors.New("a past session resumes on the tile it ran on")
		}
		if !meta.Loadable || meta.ACPSessionID == "" {
			return SessionInfo{}, 409, agent.ErrResumeUnsupported
		}
		providerID, resumeID = meta.Provider, meta.ACPSessionID
		resumeFrom = &meta
		if mode == "" && p.Agent == "" { // a delegate's mode is its ticket's or the person's preference
			mode = meta.Mode
		}
		if name == "" {
			name = meta.Name
		}
	}
	prov, ok := agent.Lookup(providerID)
	if !ok {
		return SessionInfo{}, 400, fmt.Errorf("unknown provider %q (GET /api/xbin/agent/providers lists them)", providerID)
	}
	if p.Agent != "" { // a person's workspace agent: narrowed beyond the person's own gates (delegate.go)
		var why string
		if mode, netMode, why = m.delegateOpen(p, &a, tile, prov, mode, netMode, resumeFrom); why != "" {
			return SessionInfo{}, 403, errors.New(why)
		}
	}
	if mode, err = prov.ResolveMode(mode); err != nil {
		return SessionInfo{}, 400, err
	}
	if d := a.Delegate; p.Agent != "" && d != nil && d.Approved && (d.Provider != prov.ID || d.Mode != mode) {
		// what would open now isn't what the person was shown (the history
		// entry it resumes changed, or the catalog did)
		return SessionInfo{}, 403, fmt.Errorf("a workspace agent opens exactly the session its person approved (%s in mode %s): this would be %s in mode %s — ask again",
			d.Provider, d.Mode, prov.ID, mode)
	}
	if err := m.HarnessAllowed(p, tile, prov.ID); err != nil { // the tile's orgs' harness rules (D229)
		if resume != "" {
			return SessionInfo{}, 403, fmt.Errorf("this session can't be resumed here any more: %w", err)
		}
		return SessionInfo{}, 403, err
	}
	if m.BxPath == "" {
		return SessionInfo{}, 503, errors.New("agent sessions need the bx binary the daemon could not find at startup (build it: CGO_ENABLED=0 go build -o bin/bx ./cmd/bx, or set XBIN_BIN)")
	}
	o := m.openOptsFor(p, tile, cwd, normalizeNet(netMode), a.GPU, !a.NoAPI)
	o.kind, o.vm = KindAgent, a.VM
	o.origin = cmp.Or(p.Agent, a.origin) // a delegate's open; a restart keeps its session's (delegate.go)
	if o.originMode = a.originMode; p.Agent != "" {
		o.originMode = mode // the mode its person approved, or their preference's
	}
	if a.home == HomeCopy || (resumeFrom != nil && resumeFrom.Home == HomeCopy) {
		// it restarts, or resumes, a session that ran on this tile's copy of
		// the home: it stays on the copy (its agent's own session files are
		// there), whatever the rule says now — never the other way (home.go)
		m.setHome(p, &o, m.homePlanFor(p, tile, true))
	}
	if code, err := m.pickTarget(p, &o, tile, a.Deployment); err != nil {
		return SessionInfo{}, code, err
	}
	if code, err := m.llmGate(&o, tile, prov, a.LLM, options); err != nil { // who serves its model calls (llmhook.go)
		return SessionInfo{}, code, err
	}
	if code, err := m.pickPartition(p, &o, tile); err != nil { // partition.go
		return SessionInfo{}, code, err
	}
	// the person's saved sign-in for the harness, and whether the rule lets
	// it into this session (signin.go) — after the partition is known
	o.signin = m.signinPlanFor(p, o, rel, prov, a.Signin, m.credentialRule(o, rel, prov.ID, o.llmChoice).Forced, a.LLM == sdkacp.UseConnector, a.Delegate)
	o.signin.past = resumeFrom != nil && resumeFrom.Signin // its transcript comes back with it
	if o.signin.bound && (o.signin.saved == nil || o.signin.why != "") {
		// its person approved a start that uses their saved sign-in: it
		// opens with that one, or not at all
		m.partOpened(&o)
		return SessionInfo{}, 403, fmt.Errorf("a workspace agent opens exactly the session its person approved, and that one used their saved %s sign-in, which can't go in now (%s) — ask again", prov.Name, o.signin.why)
	}
	if a.Signin != "" && a.Signin != SigninNone && o.signin.saved == nil {
		m.partOpened(&o)
		return SessionInfo{}, 404, errors.New("signin: you have no such saved sign-in for " + prov.Name + " (GET /api/xbin/agent/signins lists them)")
	}
	if code, err := m.resumeHere(resume, fromPart, o); err != nil { // partition.go
		m.partOpened(&o)
		return SessionInfo{}, code, err
	}
	s, err := m.createAgent(o, prov, mode, options, resumeID, resume)
	m.partOpened(&o) // registered or failed: a stop sees it now (partitionhold.go)
	if err != nil {
		if errors.Is(err, errLimit) {
			return SessionInfo{}, 409, err
		}
		if errors.Is(err, ErrForbidden) { // only the connector may serve it, and it can't (llmhook.go)
			return SessionInfo{}, 403, err
		}
		return SessionInfo{}, 400, err
	}
	if name != "" {
		m.Rename(s.ID, name)
	}
	return m.row(s), 200, nil
}

// HarnessAllowed answers why an agent session of harness may not open on
// tile for p (AgentGate: the tile's orgs' harness rules, D229), nil when it
// may — and always nil with no gate installed.
func (m *Manager) HarnessAllowed(p auth.Principal, tile, harness string) error {
	if m.AgentGate == nil {
		return nil
	}
	return m.AgentGate(p, tile, harness)
}

// Info is one session's directory row.
func (m *Manager) Info(id string) (SessionInfo, bool) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return SessionInfo{}, false
	}
	return m.row(s), true
}

// MayDrive is the gate on the per-session API routes: the creator (while
// still terminal-level on the tile — the reattach rule) or an admin — but
// on a partitioned tile never an admin in another person's session, nor an
// admin viewing as its person (PD-09: their agent's sandbox holds that
// person's terminal token). ErrNoSession for an unknown id, ErrForbidden
// otherwise.
func (m *Manager) MayDrive(id string, p auth.Principal) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	if why := m.viewAsBarred(s, p); why != "" {
		return fmt.Errorf("%w: %s", ErrForbidden, why)
	}
	if m.adminPass(s, p) {
		return nil
	}
	if s.homeKey != HomeKey(p) {
		if p.IsAdmin() {
			return fmt.Errorf("%w: %s", ErrForbidden, notYours(m.tileOf(s)))
		}
		return fmt.Errorf("%w: session belongs to another user", ErrForbidden)
	}
	if !p.CanTerminalTileVia(m.tileOf(s)) {
		return fmt.Errorf("%w: terminal access to this tile was revoked", ErrForbidden)
	}
	return nil
}

// AgentToken reports whether tok is a live agent session's own terminal
// token — the XBIN_TOKEN its sandbox holds. The server keeps such a token
// from opening or driving any agent session: per session only, an agent
// would open a sibling (in a bypass mode, with wider pickers) and have the
// two answer each other's permission requests and restart each other. A
// shell's token is not an agent's.
func (m *Manager) AgentToken(tok string) bool {
	if tok == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.kind == KindAgent && s.token != "" && subtle.ConstantTimeCompare([]byte(s.token), []byte(tok)) == 1 {
			return true
		}
	}
	return false
}

// onAgentConfig sees each agent session's driver config (tests);
// onAgentStart is told a session's driver is about to start — the moment
// its first frame, with any saved sign-in, goes into the sandbox.
var (
	onAgentConfig func(agent.Config)
	onAgentStart  func(id string)
)

// createAgent is create() for the agent kind: pipes instead of a PTY, the
// driver started in the background (the session reports `starting` until
// the handshake is done — a prompt waits for it).
func (m *Manager) createAgent(o openOpts, prov agent.Provider, mode string, options map[string]string, resumeID, resumed string) (*Session, error) {
	dir, rel, homeDir, token, revokeTok, err := m.prepare(o)
	if err != nil {
		return nil, err
	}
	o.launch = &sbxLaunch{}
	if revokeTok, err = m.llmArm(&o, o.onTile(rel), &prov, revokeTok); err != nil { // the session's LLM key, ended with its token (llmhook.go)
		revokeTok()
		return nil, err
	}
	start := o.startDir(dir, homeDir) // $HOME on a partitioned tile (partition.go)
	cmd, cleanup, postStart, envKey, env, err := m.shellCmd(start, rel, homeDir, token, o)
	if err != nil {
		revokeTok()
		m.sbxFail(o, o.onTile(rel), err)
		return nil, err
	}
	// A prompt's files (agent/host/attach.go): in a sandbox they go to its
	// own /tmp, a tmpfs that dies with it. With isolation off the host is a
	// plain child sharing xbind's /tmp and is SIGKILLed at the end, so the
	// daemon owns the directory and removes it with the session.
	attachDir := ""
	if !m.Isolate {
		if d, err := os.MkdirTemp("", "xbin-attachments-"); err == nil {
			attachDir = d
			sandboxCleanup := cleanup
			cleanup = func() { sandboxCleanup(); _ = os.RemoveAll(d) }
		} else {
			slog.Warn("agent session: no attachments dir; the host makes its own", "err", err)
		}
	}
	fail := func(err error) (*Session, error) {
		cleanup()
		revokeTok()
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		slog.Error("agent session spawn failed", "cwd", filepath.ToSlash(rel), "provider", prov.ID, "err", err)
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		m.sbxFail(o, o.onTile(rel), err)
		return fail(fmt.Errorf("spawn agent host: %w", err))
	}
	id := util.RandomToken(8)
	leaf := m.startLeaf(o, id, cmd.Process) // sbx.go: a VM's before the guest touches memory
	var rl *relay.Relay
	if postStart != nil {
		rl = postStart()
	}
	st := &agentState{log: agent.NewLog(0, 0), perms: agent.NewPermissions(), provider: prov,
		ready: make(chan struct{}), done: make(chan struct{}), gone: make(chan struct{}), mode: mode, status: agent.StatusStarting, resumed: resumed,
		attachDir: attachDir, replay: replayState{on: resumeID != ""}, llm: o.llm, llmChoice: o.llmChoice}
	s := &Session{
		ID: id, Cwd: rel, tile: o.onTile(rel), Net: o.net, cmd: cmd, kind: KindAgent, agent: st, pgid: postStart == nil, vm: o.vm,
		NetNote: o.netNote, Label: o.label, Scopes: o.scopes,
		cleanup: cleanup, relay: rl, envKey: envKey, homeKey: o.homeKey, token: token,
		baseOld: m.layerOutdated(envKey), gpu: o.gpu, api: o.api, target: o.target, part: o.part,
		origin: o.origin, originMode: o.originMode,
		home: sessionHome{plan: o.home, dir: o.launch.homeCopy},
		born: time.Now(), hub: termwire.NewHub(0), // no terminal socket: the hub keeps its activity clock
	}
	st.snap = newSnapper(dir, func(e agent.Event) { s.logEvent(m, e) })
	// the person's saved sign-in, taken for this session (signin.go): what
	// the session carries, and the spawn frame's part
	var signinSpawn *acp.SpawnSignin
	st.signin, signinSpawn = m.signinTake(o.signin, id, rel, prov, o.llm != nil && o.llm.token != "" && o.llm.rule == sdkacp.UseConnector)
	if signinSpawn != nil && signinSpawn.KeyHome != nil && !m.Isolate {
		d, rm := keyHomeDir() // isolation off: the daemon owns the key's directory, as it does the attachments'
		signinSpawn.KeyHome.At = d
		sandboxCleanup := s.cleanup
		s.cleanup = func() { sandboxCleanup(); rm() }
	}
	st.redact = s.redact
	drv := acp.NewFor(func(h acp.HelloParams) {
		s.llmHello(m, h)    // who serves its model calls (llmhook.go)
		s.signinHello(m, h) // what it signs in with (signin.go)
	})
	st.drv = drv // before the session is visible: info() and the API read it
	if m.beforeRegister != nil {
		m.beforeRegister()
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	m.changed("open", s)
	unlist := m.register(s, o, leaf)
	if o.launch.baseMoved != "" { // D175: this start moved the tile's layer to the current base
		s.sayBaseMoved(m, o.layerKey(rel), o.movedLayer()) // the layer it moved: on a partitioned tile its person's (partition.go)
	}
	s.llmSay(m)     // why it runs without the workspace's LLM connector, when that needs saying (llmhook.go)
	s.sayHome(m, o) // the first session on a tile's copy of the home says what that means (home.go)

	// The agent's env: the sandbox env (with the per-user $HOME the CLI reads
	// its login from) plus the provider's own non-secret knobs. No API keys —
	// the CLI authenticates from its home, exactly as a shell terminal does.
	agentEnv := append([]string(nil), env...)
	for _, k := range sortedKeys(prov.Env) {
		agentEnv = append(agentEnv, k+"="+prov.Env[k])
	}
	spawn := func(ctx context.Context, cfg agent.Config) (*agent.Process, error) {
		llm, err := m.llmSpawn(o, prov, cfg.Env) // the connector's environment, settled by the host (llmhook.go)
		if err != nil {
			return nil, err
		}
		// the saved sign-in rides this one frame, to the agent's process alone (signin.go)
		params, _ := json.Marshal(acp.SpawnParams{Argv: cfg.Argv, Env: cfg.Env, Cwd: start, AttachDir: attachDir, LLM: llm,
			Signin: signinSpawn, LoginFiles: prov.LoginFiles})
		if err := acp.Encode(stdin, &acp.Message{Method: acp.MXbinSpawn, Params: params}); err != nil {
			return nil, err
		}
		return &agent.Process{Stdin: stdin, Stdout: stdout, Stderr: stderr, Kill: s.kill}, nil
	}
	cfg := agent.Config{Provider: prov, Mode: mode, Options: options, ResumeID: resumeID, Cwd: start, Env: agentEnv, Argv: prov.Argv, Spawn: spawn,
		Perms: st.perms, Version: Version, Log: st.logf, Meta: map[string]string{"tile": o.onTile(rel)},
		// a session a person's workspace agent opened: its mode is the one
		// its person approved, and no option may move it (an agent's config
		// option of category mode is set after the mode; delegate.go)
		SkipModeOptions: o.origin != "",
		// a workspace agent's start is the harness and mode its person
		// approved (or their preference) or it isn't at all (acp.Config.Strict)
		Strict: o.origin != "",
		// a saved sign-in in its environment: the adapter's probe isn't
		// believed, a key it wants through authenticate is handed over
		Credential: s.signinCredential(m, prov, signinSpawn)}
	if onAgentConfig != nil {
		onAgentConfig(cfg)
	}
	st.never = make(chan struct{}) // closed when the session ends before its driver ever starts (below)
	go s.agentPump(m, func() {
		unlist()
		st.mu.Lock()
		tail := string(st.text)
		st.mu.Unlock()
		m.sbxExited(s, o.vm, tail)
		m.remove(s.ID)
		m.changed("close", s)
		revokeTok()
		s.dropSignin() // its copy of the saved sign-in (signin.go)
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		m.dropLeaf(leaf)
	})
	slog.Info("agent session created", "id", s.ID, "cwd", filepath.ToSlash(rel), "provider", prov.ID, "mode", mode, "net", o.net, "restricted", o.restricted)
	// judged BEFORE the driver starts: a sweep that ran while the sandbox
	// came up never saw this session (home.go registered), and the driver's
	// first frame carries the saved sign-in into the sandbox — it is not
	// sent to a session whose trust stopped holding in between
	if m.registered(s) {
		st.mu.Lock()
		st.startErr = errors.New("the session was ended as it started: what it was allowed changed while its sandbox came up")
		st.mu.Unlock()
		close(st.ready)
		close(st.never) // the pump tears the session down: no driver will close its events
		return s, nil
	}
	go func() {
		if onAgentStart != nil {
			onAgentStart(s.ID)
		}
		err := drv.Start(context.Background(), cfg)
		if err == nil {
			err = s.llmHeld(m) // only the connector may serve it: it is on it, or it ends (llmhook.go)
		}
		id, loadable := drv.Session()
		st.mu.Lock()
		st.startErr = err
		st.acpID, st.loadable = id, loadable
		st.mu.Unlock()
		close(st.ready)
	}()
	return s, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// delta is a message/thought delta being coalesced (a tool's output run:
// sdk/acp's OutputChunk, agentpage.go).
type delta struct {
	Role        string          `json:"role,omitempty"`
	Text        string          `json:"text"`
	MessageID   string          `json:"messageId,omitempty"`
	Parent      string          `json:"parent,omitempty"`      // a subagent's text never merges into the main thread's
	Attachments json.RawMessage `json:"attachments,omitempty"` // a prompt's files (names, types, sizes): never merged
	Steered     bool            `json:"steered,omitempty"`     // a message the running turn took mid-turn (D197): never merged
}

// agentPump drains the driver's events into the log and the hub, merging
// runs of message/thought deltas and of a tool call's output chunks (flushed
// after deltaCoalesce or on any other event) so a token burst or a chatty
// command is a few events, not hundreds — a slow /ws/events subscriber
// would otherwise be evicted, and the ring would hold minutes instead of
// hours. It owns the session's end: when the driver closes its channel the
// process is gone (or killed here), and the session is torn down like a
// shell's.
func (s *Session) agentPump(m *Manager, onExit func()) {
	st := s.agent
	var pend *agent.Event
	var pd delta
	var po sdkacp.OutputChunk
	var timer, stTimer <-chan time.Time // delta coalescing; status-change coalescing (agentstatus.go)
	flush := func() {
		if pend != nil {
			if pend.Type == agent.EvToolUpdate {
				pend.Data, _ = json.Marshal(po)
			} else {
				pend.Data, _ = json.Marshal(pd)
			}
			s.logEvent(m, *pend)
			pend = nil
		}
		timer = nil
	}
	for {
		select {
		case <-st.never: // ended before its driver started (createAgent): nothing was sent into the sandbox
			flush()
			goto ended
		case e, ok := <-st.drv.Events():
			if !ok {
				flush()
				goto ended
			}
			e = s.detachedEvent(m, e) // the agent's own turn goes on (agentdetached.go)
			if e.Type == agent.EvMessageDelta || e.Type == agent.EvThoughtDelta {
				var d delta
				_ = json.Unmarshal(e.Data, &d)
				if pend != nil && pend.Type == e.Type && pd.Role == d.Role && pd.MessageID == d.MessageID && pd.Parent == d.Parent &&
					len(pd.Attachments) == 0 && len(d.Attachments) == 0 && !pd.Steered && !d.Steered {
					pd.Text += d.Text
					continue
				}
				flush()
				e2 := e
				pend, pd = &e2, d
				timer = time.After(deltaCoalesce)
				continue
			}
			if o, ok := outputChunk(e); ok {
				if pend != nil && pend.Type == agent.EvToolUpdate && po.Add(o) {
					continue
				}
				flush()
				e2 := e
				pend, po = &e2, o
				timer = time.After(deltaCoalesce)
				continue
			}
			flush()
			st.snap.expect(e) // before the event is visible: a page keeps its snapshot's call open (agentdiff.go)
			s.logEvent(m, e)
			st.snap.observe(e)
			if stTimer == nil && movesStatus(e.Type) {
				stTimer = time.After(statusCoalesce)
			}
		case <-timer:
			flush()
		case <-stTimer:
			stTimer = nil
			s.publishStatus(m)
		}
	}
ended:
	s.stopDetached()                    // the agent's own turn ends with the session, unsaid (agentdetached.go)
	s.dropQueue(m, "the session ended") // held messages go with it, said in the log (D197)
	s.endReplay(m)                      // a replay the session never finished (agentpage.go)
	s.publishStatus(m)                  // the last word before the directory's close
	s.hub.End(termwire.Exit{})
	s.kill() // the host and the agent go with the session, whatever ended first
	waitErr := s.cmd.Wait()
	if s.relay != nil {
		s.relay.Close()
	}
	if s.cleanup != nil {
		s.cleanup()
	}
	st.snap.close()
	close(st.done)
	m.saveHistory(s) // the transcript outlives the session (history.go: read back, resume)
	onExit()
	close(st.gone)
	slog.Info("agent session ended", "id", s.ID, "uptime", time.Since(s.born).Round(time.Second), "exit", exitString(waitErr))
	if time.Since(s.born) < 10*time.Second {
		st.mu.Lock()
		tail := string(st.text)
		st.mu.Unlock()
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		if tail != "" {
			slog.Warn("agent session died at start; last output", "id", s.ID, "tail", tail)
		}
	}
}

// logEvent numbers an event into the log, updates the session's summary
// fields from it, and publishes it.
func (s *Session) logEvent(m *Manager, e agent.Event) {
	// a saved sign-in is masked before the log, the hub or history see the
	// event — on the joined stream, so a piece may wait for the next one
	// (signinredact.go)
	for _, e := range s.redactEvents(e) {
		s.logOne(m, e)
	}
}

func (s *Session) logOne(m *Manager, e agent.Event) {
	st := s.agent
	if e.Type == agent.EvStatus {
		st.statusMu.Lock() // unlocked once st.status says it (below)
		e = s.restated(e)  // the agent's own turn runs: not idle (agentdetached.go)
	}
	ev := st.log.Append(e)
	st.noteSaid(e) // its last line, for a status poll (agentstatus.go)
	renamed := false
	if e.Type == agent.EvStatus {
		var d struct {
			Status, CurrentMode, Title string
			Options                    []struct{ ID, CurrentValue string }
		}
		_ = json.Unmarshal(e.Data, &d)
		st.llmOptions(e.Data) // their categories, where a model option is checked (llmhook.go)
		st.mu.Lock()
		if d.Status != "" {
			st.status = d.Status
		}
		flush := sdkacp.TurnOver(d.Status) && st.queueStateLocked().Flush() // the turn ended: what is held goes in (D197)
		if d.CurrentMode != "" {
			st.mode = d.CurrentMode
		}
		for _, o := range d.Options {
			if o.ID == "model" {
				st.model = o.CurrentValue
			}
		}
		st.mu.Unlock()
		st.statusMu.Unlock()
		// the agent's own title (most adapters generate one after the first
		// turn) names a tab the user has not named — it follows the user like
		// any name (D73) — never the sign-in's reserved one (purpose.go)
		if d.Title != "" && !ReservedName(d.Title) {
			s.mu.Lock()
			if s.name == "" {
				s.name, renamed = d.Title, true
			}
			s.mu.Unlock()
		}
		if flush {
			defer m.kickQueue(s) // after this status is logged: the queue's prompt follows it
		}
	}
	s.hub.Touch()
	if m.OnEvent != nil && !s.replayed(m, ev) {
		m.OnEvent(m.tileOf(s), SessionEvent{Event: ev, User: s.homeKey, ID: s.ID, private: s.carriesSignin()})
	}
	if renamed {
		m.changed("rename", s)
	}
}

// agentOf finds an agent session by id.
func (m *Manager) agentOf(id string) (*Session, *agentState, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, nil, ErrNoSession
	}
	if s.agent == nil {
		return nil, nil, ErrNotAgent
	}
	return s, s.agent, nil
}

// AgentPrompt starts a turn with the user's text; it waits for the
// handshake first (ctx bounds the wait). Returns the turn number.
// agent.ErrBusy while a turn runs.
func (m *Manager) AgentPrompt(ctx context.Context, id, text string) (uint64, error) {
	return m.AgentPromptWith(ctx, id, agent.Prompt{Text: text})
}

// ReservePrompt takes the session's one prompt slot before a prompt's
// body is read — its attachments are tens of MiB decoded — so a second
// prompt while one is being taken, or while a turn runs, is refused at
// once (agent.ErrBusy) instead of buffering its files first. The caller
// calls AgentPromptWith, then release. (The driver serializes prompts on
// its own; this bounds what concurrent requests hold in memory.)
func (m *Manager) ReservePrompt(id string) (release func(), err error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	running := st.busyLocked()
	st.mu.Unlock()
	if running || !st.prompting.CompareAndSwap(false, true) {
		return nil, agent.ErrBusy
	}
	return func() { st.prompting.Store(false) }, nil
}

// AgentPromptWith is AgentPrompt with attachments (agent.PrepareAttachments
// has normalised them).
func (m *Manager) AgentPromptWith(ctx context.Context, id string, p agent.Prompt) (uint64, error) {
	s, st, err := m.agentOf(id)
	if err != nil {
		return 0, err
	}
	select {
	case <-st.ready:
	case <-st.done:
		return 0, agent.ErrEnded
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	st.mu.Lock()
	startErr := st.startErr
	st.mu.Unlock()
	if startErr != nil {
		return 0, fmt.Errorf("the agent did not start: %w", startErr)
	}
	st.mu.Lock()
	busy := st.busyLocked()
	st.mu.Unlock()
	if !busy { // (a refused prompt must not move a running turn's base)
		st.snap.turnStart(2 * time.Second) // the turn's base: edits made between turns are not the agent's
	}
	if err := st.drv.Prompt(ctx, p); err != nil {
		return 0, err
	}
	st.mu.Lock()
	st.turn++
	turn := st.turn
	st.mu.Unlock()
	s.hub.Touch()
	return turn, nil
}

// AgentCancel interrupts the running turn (a no-op when idle) — the
// client's, or the agent's own (agentdetached.go).
func (m *Manager) AgentCancel(id string) error {
	s, _, err := m.agentOf(id)
	if err != nil {
		return err
	}
	return m.cancelTurn(s)
}

// AgentSetOption changes one of the agent's session settings (a config
// option it advertised: model, effort, …). Allowed any time; the agent
// applies it to the next turn.
func (m *Manager) AgentSetOption(ctx context.Context, id, optionID, value string) error {
	_, st, err := m.agentOf(id)
	if err != nil {
		return err
	}
	select {
	case <-st.ready:
	case <-st.done:
		return agent.ErrEnded
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := st.llm.option(st.provider, optionID, value); err != nil { // only the connector may serve it (llmhook.go)
		return err
	}
	return st.drv.SetOption(ctx, optionID, value)
}

// AgentPermit answers pending permission pid with an option id or a
// decision (an option kind); by names the answerer. First answer wins:
// ErrNoPermission afterwards.
// AgentElicit answers a question the agent asked (elicitation.request):
// action accept (content = the form's values) | decline | cancel. The first
// answer wins; ErrNoQuestion after.
func (m *Manager) AgentElicit(id, eid, action string, content json.RawMessage, by string) error {
	_, st, err := m.agentOf(id)
	if err != nil {
		return err
	}
	if err := st.drv.RespondElicitation(eid, action, content, by); err != nil {
		if errors.Is(err, agent.ErrNoElicitation) {
			return fmt.Errorf("%w: %s", ErrNoQuestion, eid)
		}
		return err
	}
	return nil
}

func (m *Manager) AgentPermit(id, pid, optionID, decision, by string) error {
	_, st, err := m.agentOf(id)
	if err != nil {
		return err
	}
	res, err := st.perms.Resolve(pid, optionID, decision, by)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoPermission, err)
	}
	return st.drv.RespondPermission(res)
}

// AgentEvents replays the log after cursor `since` (0 = from the start):
// the events, the next cursor, and whether the cursor predates the ring.
func (m *Manager) AgentEvents(id string, since uint64) (evs []agent.Event, next uint64, truncated bool, err error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, 0, false, err
	}
	evs, truncated = st.log.Since(since)
	next = since
	if n := len(evs); n > 0 {
		next = evs[n-1].Seq
	}
	return evs, next, truncated, nil
}

// AgentWait is the follow hook: a channel closed on the next logged event
// and one closed when the session ends. Take it BEFORE AgentEvents so no
// append slips between the replay and the wait.
func (m *Manager) AgentWait(id string) (next, done <-chan struct{}, err error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, nil, err
	}
	return st.log.Wait(), st.done, nil
}

// AgentPending lists the unanswered permission requests.
func (m *Manager) AgentPending(id string) ([]agent.Pending, error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, err
	}
	return st.perms.List(), nil
}

// AgentQuestions lists the unanswered questions (elicitation.request
// payloads), oldest first.
func (m *Manager) AgentQuestions(id string) ([]agent.Elicitation, error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, err
	}
	return st.drv.PendingElicitations(), nil
}

// AgentLog is the session's text log (the host's stderr, the driver's
// notes) — for debugging a session that will not start.
func (m *Manager) AgentLog(id string) (string, error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return string(st.text), nil
}
