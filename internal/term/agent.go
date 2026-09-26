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
// `claude /login` / `codex login` / … done once in a terminal serves the
// agent on every tile. There are no provider keys in the tile vault.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agent/acp"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/util"
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
}

func (e SessionEvent) Owner() string { return e.User }

// agentState is the agent half of a KindAgent session.
type agentState struct {
	drv       agent.Driver
	log       *agent.Log
	perms     *agent.Permissions
	provider  agent.Provider
	ready     chan struct{} // closed once the driver's Start returned
	done      chan struct{} // closed once the pump ended (the session is over)
	gone      chan struct{} // closed after the teardown: history saved, layer released, row removed
	mu        sync.Mutex
	startErr  error
	mode      string
	model     string // the agent's current model option, when it exposes one
	status    string
	turn      uint64
	text      []byte
	acpID     string    // the agent's own session id (after the handshake)
	loadable  bool      // the agent can reopen acpID later (session/load) — resume
	resumed   string    // the history entry this session reopened (superseded when this one is saved)
	snap      *snapper  // files.changed snapshots of the tile (agentdiff.go); nil = off
	published statusKey // the last summary handed to OnStatus (agentstatus.go)
}

func (st *agentState) logf(line string) {
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
// GPU), the provider and its mode/settings, a name, a past session to resume.
type AgentOpen struct {
	Cwd, Net, GPU, Provider, Mode, Name, Resume string
	NoAPI                                       bool // a code-only sandbox: no terminal token (api=0 on a shell)
	VM                                          bool // a VM sandbox (vm.go)
	Options                                     map[string]string
}

// OpenAgentWith is OpenAgent with the sandbox pickers.
func (m *Manager) OpenAgentWith(p auth.Principal, a AgentOpen) (SessionInfo, int, error) {
	cwd, netMode, providerID, mode, name, resume, options := a.Cwd, a.Net, a.Provider, a.Mode, a.Name, a.Resume, a.Options
	if cwd == "" {
		return SessionInfo{}, 403, errors.New("an agent session runs on a tile — pass cwd")
	}
	_, rel, err := util.SafeJoin(m.Root, cwd)
	if err != nil || rel == "" || !p.CanTerminalTileVia(rel) {
		return SessionInfo{}, 403, errors.New("your account doesn't have terminal access to this tile")
	}
	// resume: reopen one of the caller's past sessions on this tile
	// (history.go) — its provider, mode and name carry over; the agent must
	// have advertised loadSession, else 409 and the UI offers a fresh start
	var resumeID string
	if resume != "" {
		meta, _, err := m.ReadHistory(HomeKey(p), resume)
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
		if mode == "" {
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
	if mode, err = prov.ResolveMode(mode); err != nil {
		return SessionInfo{}, 400, err
	}
	if m.BxPath == "" {
		return SessionInfo{}, 503, errors.New("agent sessions need the bx binary the daemon could not find at startup (build it: CGO_ENABLED=0 go build -o bin/bx ./cmd/bx, or set XBIN_BIN)")
	}
	o := m.openOptsFor(p, rel, cwd, normalizeNet(netMode), a.GPU, !a.NoAPI)
	o.kind, o.vm = KindAgent, a.VM
	s, err := m.createAgent(o, prov, mode, options, resumeID, resume)
	if err != nil {
		if errors.Is(err, errLimit) {
			return SessionInfo{}, 409, err
		}
		return SessionInfo{}, 400, err
	}
	if name != "" {
		m.Rename(s.ID, name)
	}
	return s.info(), 200, nil
}

// Info is one session's directory row.
func (m *Manager) Info(id string) (SessionInfo, bool) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return SessionInfo{}, false
	}
	return s.info(), true
}

// MayDrive is the gate on the per-session API routes: the creator (while
// still terminal-level on the tile — the reattach rule) or an admin.
// ErrNoSession for an unknown id, ErrForbidden otherwise.
func (m *Manager) MayDrive(id string, p auth.Principal) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	if p.IsAdmin() {
		return nil
	}
	if s.homeKey != HomeKey(p) {
		return fmt.Errorf("%w: session belongs to another user", ErrForbidden)
	}
	if !p.CanTerminalTileVia(s.Cwd) {
		return fmt.Errorf("%w: terminal access to this tile was revoked", ErrForbidden)
	}
	return nil
}

// createAgent is create() for the agent kind: pipes instead of a PTY, the
// driver started in the background (the session reports `starting` until
// the handshake is done — a prompt waits for it).
func (m *Manager) createAgent(o openOpts, prov agent.Provider, mode string, options map[string]string, resumeID, resumed string) (*Session, error) {
	dir, rel, homeDir, token, revokeTok, err := m.prepare(o)
	if err != nil {
		return nil, err
	}
	cmd, cleanup, postStart, envKey, env, err := m.shellCmd(dir, rel, homeDir, token, o)
	if err != nil {
		revokeTok()
		return nil, err
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
		return fail(fmt.Errorf("spawn agent host: %w", err))
	}
	id := util.RandomToken(8)
	vmLeaf := o.vm && m.Cgroup != nil && cmd.Process != nil // before the guest touches memory
	if vmLeaf {
		m.Cgroup.AddMem("term-"+id, cmd.Process.Pid, m.vmLeafBytes())
	}
	var rl *relay.Relay
	if postStart != nil {
		rl = postStart()
	}
	st := &agentState{log: agent.NewLog(0, 0), perms: agent.NewPermissions(), provider: prov,
		ready: make(chan struct{}), done: make(chan struct{}), gone: make(chan struct{}), mode: mode, status: agent.StatusStarting, resumed: resumed}
	s := &Session{
		ID: id, Cwd: rel, Net: o.net, cmd: cmd, kind: KindAgent, agent: st, pgid: postStart == nil, vm: o.vm,
		NetNote: o.netNote, Label: o.label, Scopes: o.scopes,
		cleanup: cleanup, relay: rl, envKey: envKey, homeKey: o.homeKey, token: token,
		baseOld: m.layerOutdated(envKey), gpu: o.gpu, api: o.api,
		born: time.Now(), clients: map[*client]struct{}{}, lastActive: time.Now(),
	}
	st.snap = newSnapper(dir, func(e agent.Event) { s.logEvent(m, e) })
	drv := acp.New()
	st.drv = drv // before the session is visible: info() and the API read it
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	m.changed("open", s)
	limited := o.restricted && m.Cgroup != nil && cmd.Process != nil && !vmLeaf
	if limited {
		m.Cgroup.Add("term-"+s.ID, cmd.Process.Pid)
	}
	limited = limited || vmLeaf

	// The agent's env: the sandbox env (with the per-user $HOME the CLI reads
	// its login from) plus the provider's own non-secret knobs. No API keys —
	// the CLI authenticates from its home, exactly as a shell terminal does.
	agentEnv := append([]string(nil), env...)
	for _, k := range sortedKeys(prov.Env) {
		agentEnv = append(agentEnv, k+"="+prov.Env[k])
	}
	spawn := func(ctx context.Context, cfg agent.Config) (*agent.Process, error) {
		params, _ := json.Marshal(acp.SpawnParams{Argv: cfg.Argv, Env: cfg.Env, Cwd: dir})
		if err := acp.Encode(stdin, &acp.Message{Method: acp.MXbinSpawn, Params: params}); err != nil {
			return nil, err
		}
		return &agent.Process{Stdin: stdin, Stdout: stdout, Stderr: stderr, Kill: s.kill}, nil
	}
	cfg := agent.Config{Provider: prov, Mode: mode, Options: options, ResumeID: resumeID, Cwd: dir, Env: agentEnv, Argv: prov.Argv, Spawn: spawn,
		Perms: st.perms, Version: Version, Log: st.logf, Meta: map[string]string{"tile": rel}}
	go s.agentPump(m, func() {
		m.remove(s.ID)
		m.changed("close", s)
		revokeTok()
		if envKey != "" {
			m.releaseEnv(envKey)
		}
		if limited {
			m.Cgroup.Remove("term-" + s.ID)
		}
	})
	go func() {
		err := drv.Start(context.Background(), cfg)
		id, loadable := drv.Session()
		st.mu.Lock()
		st.startErr = err
		st.acpID, st.loadable = id, loadable
		st.mu.Unlock()
		close(st.ready)
	}()
	slog.Info("agent session created", "id", s.ID, "cwd", filepath.ToSlash(rel), "provider", prov.ID, "mode", mode, "net", o.net, "restricted", o.restricted)
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

// delta is a message/thought delta being coalesced.
type delta struct {
	Role        string          `json:"role,omitempty"`
	Text        string          `json:"text"`
	MessageID   string          `json:"messageId,omitempty"`
	Parent      string          `json:"parent,omitempty"`      // a subagent's text never merges into the main thread's
	Attachments json.RawMessage `json:"attachments,omitempty"` // a prompt's files (names, types, sizes): never merged
}

// agentPump drains the driver's events into the log and the hub, merging
// runs of message/thought deltas (flushed after deltaCoalesce or on any
// other event) so a token burst is a few events, not hundreds — a slow
// /ws/events subscriber would otherwise be evicted. It owns the session's
// end: when the driver closes its channel the process is gone (or killed
// here), and the session is torn down like a shell's.
func (s *Session) agentPump(m *Manager, onExit func()) {
	st := s.agent
	var pend *agent.Event
	var pd delta
	var timer, stTimer <-chan time.Time // delta coalescing; status-change coalescing (agentstatus.go)
	flush := func() {
		if pend != nil {
			pend.Data, _ = json.Marshal(pd)
			s.logEvent(m, *pend)
			pend = nil
		}
		timer = nil
	}
	for {
		select {
		case e, ok := <-st.drv.Events():
			if !ok {
				flush()
				goto ended
			}
			if e.Type == agent.EvMessageDelta || e.Type == agent.EvThoughtDelta {
				var d delta
				_ = json.Unmarshal(e.Data, &d)
				if pend != nil && pend.Type == e.Type && pd.Role == d.Role && pd.MessageID == d.MessageID && pd.Parent == d.Parent &&
					len(pd.Attachments) == 0 && len(d.Attachments) == 0 {
					pd.Text += d.Text
					continue
				}
				flush()
				e2 := e
				pend, pd = &e2, d
				timer = time.After(deltaCoalesce)
				continue
			}
			flush()
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
	s.publishStatus(m) // the last word before the directory's close
	s.mu.Lock()
	s.dead = true
	s.mu.Unlock()
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
	st := s.agent
	ev := st.log.Append(e)
	renamed := false
	if e.Type == agent.EvStatus {
		var d struct {
			Status, CurrentMode, Title string
			Options                    []struct{ ID, CurrentValue string }
		}
		_ = json.Unmarshal(e.Data, &d)
		st.mu.Lock()
		if d.Status != "" {
			st.status = d.Status
		}
		if d.CurrentMode != "" {
			st.mode = d.CurrentMode
		}
		for _, o := range d.Options {
			if o.ID == "model" {
				st.model = o.CurrentValue
			}
		}
		st.mu.Unlock()
		// the agent's own title (most adapters generate one after the first
		// turn) names a tab the user has not named — it follows the user like
		// any name (D73)
		if d.Title != "" {
			s.mu.Lock()
			if s.name == "" {
				s.name, renamed = d.Title, true
			}
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
	if m.OnEvent != nil {
		m.OnEvent(s.Cwd, SessionEvent{Event: ev, User: s.homeKey, ID: s.ID})
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
	busy := st.status == agent.StatusRunning || st.status == agent.StatusWaiting || st.status == agent.StatusCancelling
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
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
	return turn, nil
}

// AgentCancel interrupts the running turn (a no-op when idle).
func (m *Manager) AgentCancel(id string) error {
	_, st, err := m.agentOf(id)
	if err != nil {
		return err
	}
	return st.drv.Cancel()
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
