package term

// llmhook.go — sessions on the workspace's LLM connector (D226 part 2,
// docs/llm.md "Coding agents and terminals"). With a connector configured:
//
//   - an AGENT session's CLI is pointed at it from its environment (sdk/acp
//     connector.go has the table), unless the person's own sign-in serves
//     the session. Who serves is decided by the session's own choice, else
//     the credential rule in force for that person, tile and harness
//     (Credentials: the person's saved default, the workspace's, an org's
//     — the agents policy, D229) — "own-first" by default: their own
//     sign-in when the session's $HOME has one, else the connector. A rule
//     of "connector-only" is not the session's to override. Only the agent
//     host sees that $HOME
//     (the daemon runs and trusts nothing there), so the daemon renders
//     both outcomes into the spawn frame and the host settles it and says
//     which (_xbin/hello);
//   - a plain TERMINAL gets XBIN_LLM_URL and XBIN_LLM_KEY (and, by a
//     separate switch, OPENAI_BASE_URL and OPENAI_API_KEY).
//
// The credential is never the session's terminal token: each session gets
// an LLM key of its own — good for the connector's routes only, metered to
// the session's person and tile — minted when the session opens and revoked
// with the terminal token on every way out. A restarted or resumed session
// is a new session with a new key. A session whose sandbox has no network
// (net: none) reaches the connector through one door — a port of its
// gateway forwarded to xbind's LLM-only listener, which serves the model
// routes to an LLM key and nothing else — and nothing but it. A session
// that targets a non-primary deployment gets a key that names the
// deployment.
//
// Where the connector ALONE may serve (CredentialRule.Forced) everything
// here fails closed: no key, no address or a host that doesn't answer
// "connector" ends the session with the reason instead of letting it run on
// something else; the CLI is configured so nothing else can be picked
// (sdkacp's ConnectorParams.Only); and a model option a client sets — at
// the start or later — must be one of the connector's (llmOption).
//
// Without a connector (Manager.LLM nil, or its Offer false) nothing here
// runs: sessions are exactly as before.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agent/acp"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/termwire"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// LLMConnector is the workspace's LLM connector as sessions use it (boot
// wires internal/llm's). nil ⇒ sessions get nothing of it.
type LLMConnector interface {
	// Offer answers what a new session of kind (KindShell | KindAgent) of
	// user ("owner": the root token) on tile is offered; false ⇒ nothing:
	// no connector is configured, it is switched off, or this session may
	// not have it. Only KindAgent needs Models; any other kind (a shell, a
	// launcher's listing) must not wait on a provider for them.
	Offer(ctx context.Context, user, tile, kind string) (LLMOffer, bool)
	// Mint makes the session's key — good for the connector's routes only,
	// metered to user and tile — and the func that ends it. refused (may be
	// nil) is told of every call made with the key that the connector
	// answers with an error: a coding CLI often keeps such an answer to
	// itself. It must not block.
	// deployment is the tile deployment the session targets ("" = the
	// primary): the key carries it.
	Mint(user, tile, deployment, label string, refused func(status int, code, message string)) (token string, revoke func(), err error)
}

// LLMOffer is the connector for one new session.
type LLMOffer struct {
	// Agents: agent sessions are offered the connector. Terminals: plain
	// terminals get XBIN_LLM_*; OpenAIEnv: and OPENAI_BASE_URL /
	// OPENAI_API_KEY.
	Agents    bool
	Terminals bool
	OpenAIEnv bool
	// Wires are the wires the connector serves (sdkacp.WireChat, …): a
	// harness whose CLI speaks another can't use it yet.
	Wires []string
	// Model is the default model for a harness ("" = none: the connector
	// can't be a CLI's default), Models what the person may pick.
	Model  string
	Models []sdkacp.ConnectorModel
}

// CredentialRule is who serves one agent session's model calls, as the
// agents policy answers it (D229: the broker's AgentSessionLLM).
type CredentialRule struct {
	// Use: sdkacp.UseOwn, UseConnector or UseOwnFirst (and "") — the
	// workspace's mode with the person's saved choice for the harness and
	// the session's own applied.
	Use string
	// Forced: the connector alone serves, whatever anyone chose (the
	// workspace's connector-only, or a model rule of one of the tile's
	// orgs). Reason says which rule, for a person to read ("Finance allows
	// private models only"); "" when the workspace's own mode is the reason.
	Forced bool
	Reason string
}

// kindLaunch is the kind Offer is asked with for a launcher's listing and a
// choice's check: what an agent session would get, without the model list
// (Model alone says whether the connector can serve one).
const kindLaunch = "agent-launch"

// LLMHook is the Manager's share of the hook-up: what boot wires, and what
// sessions' hosts reported.
type LLMHook struct {
	// LLM is the workspace's LLM connector as sessions use it: with one
	// configured, an agent session's CLI and a terminal's tools are pointed
	// at it with a key of the session's own. nil ⇒ sessions get nothing of
	// it — exactly as before.
	LLM LLMConnector
	// Credentials answers who serves the model calls of an agent session of
	// harness that a person (user id; "" the root token) opens on tile
	// stating choice ("" none, "own", "connector"): their saved choice, the
	// workspace's mode, what the tile's orgs force — the agents policy's
	// one answer (D229; boot points it at the broker's AgentSessionLLM).
	// nil ⇒ the choice, else own-first; nothing forced.
	Credentials func(userID, tile, harness, choice string) CredentialRule
	// LLMDoor is the socket of xbind's LLM-only listener ("" = none yet): a
	// session with no network gets it as one port of its sandbox's gateway
	// (llmDoorPort) and reaches the connector there. nil ⇒ such a session
	// gets no connector.
	LLMDoor func() string

	llmSeen llmLogins // which providers each home was last found signed in to
}

// llmOpts is a new session's share (openOpts).
type llmOpts struct {
	llm       *llmSession // the session's hold on the connector (llmArm); nil = none
	llmChoice string      // an agent session's own choice of who serves it ("" | connector | own)
	noLLM     bool        // a person whose terminals hold no tile API token (D17): no LLM key in one either
}

// llmKeyShape is an LLM key as it shows in text (internal/llm's KeyPrefix).
var llmKeyShape = regexp.MustCompile(`xbl_[A-Za-z0-9_-]{20,}`)

// maskLLMKeys is text with every LLM key in it masked: for what xbind
// itself keeps or logs of a session's output — the agent's stderr, a dead
// session's last lines — where a hook or a tool may have printed the
// environment. (A session's transcript is its person's own, and its key
// dies with it.)
func maskLLMKeys(text string) string {
	if !strings.Contains(text, "xbl_") {
		return text
	}
	return llmKeyShape.ReplaceAllString(text, "xbl_[redacted]")
}

// llmOwner is the connector's name for the root token's sessions
// (internal/llm's OwnerUser).
const llmOwner = "owner"

// llmSession is one session's hold on the connector.
type llmSession struct {
	offer  LLMOffer
	rule   string // agents: what the host settles — connector | own | own-first
	forced bool   // agents: the connector alone may serve (CredentialRule.Forced): everything fails closed
	why    string // …and the rule that says so, for a person to read
	door   bool   // reached through the LLM-only door (a session with no network), not under XBIN_URL
	token  string
	revoke func()

	mu     sync.Mutex
	serves string // agents: who serves, once the host said (sdkacp.ServeConnector | ServeOwn)
	// offered: the host said the connector serves, but nothing holds the CLI
	// to it — no rule gives it the connector alone and the harness keeps the
	// person's and the project's own providers beside it (an Additive one,
	// opencode), whose own config can leave the connector out. Set with serves.
	offered bool
	note    string // agents: said in the session once it is listed
	ended   bool
	say     func(text string)    // agents: logs a notice in the session, once it is listed (llmSay)
	told    map[string]time.Time // agents: when each refusal code was last said
	cats    map[string]string    // agents, forced: the category of each config option the agent advertised
}

// The LLM-only door of a session with no network: the gateway port its
// relay forwards to xbind's LLM-only listener (the same a tile sandbox
// created with net.llm has, tilesbx.LLMGatewayPort) and the OpenAI base URL
// that makes inside the sandbox.
const (
	llmDoorPort = 4180
	llmDoorURL  = "http://" + sandbox.GatewayIP + ":4180/v1"
)

// llmDoorSock is the LLM-only listener's socket ("" = none).
func (m *Manager) llmDoorSock() string {
	if m.LLMDoor == nil {
		return ""
	}
	return m.LLMDoor()
}

// llmDoor is the socket the relay of o's sandbox forwards llmDoorPort to
// ("" = no door): a session with no network that holds an LLM key.
func (m *Manager) llmDoor(o openOpts) string {
	if o.llm == nil || !o.llm.door || o.llm.token == "" {
		return ""
	}
	return m.llmDoorSock()
}

// llmRefusedEvery is how often a session repeats one kind of refusal: a CLI
// that retries is refused again every few seconds.
const llmRefusedEvery = 2 * time.Minute

// refused says, in the session, that the connector refused one of its
// model calls — what a coding CLI retrying in silence never tells its
// person (a quota reached, too many calls at once, a model nothing serves).
func (l *llmSession) refused(status int, code, message string) {
	l.mu.Lock()
	say, now := l.say, time.Now()
	if l.told == nil {
		l.told = map[string]time.Time{}
	}
	key := fmt.Sprint(status, " ", code)
	recent := now.Sub(l.told[key]) < llmRefusedEvery
	if !recent {
		l.told[key] = now
	}
	l.mu.Unlock()
	if say == nil || recent {
		return
	}
	what := fmt.Sprint(status)
	if code != "" {
		what += " " + code
	}
	if message == "" {
		message = "no reason given"
	}
	say("The workspace's LLM connector refused a model call (" + what + "): " + message)
}

func (l *llmSession) end() {
	if l == nil {
		return
	}
	l.mu.Lock()
	done := l.ended
	l.ended = true
	l.mu.Unlock()
	if !done && l.revoke != nil {
		l.revoke()
	}
}

func llmUser(o openOpts) string {
	if o.userID == "" {
		return llmOwner
	}
	return o.userID
}

// llmReach reports whether the session's sandbox has a route to xbind: every
// session but an isolated one with no network.
func (m *Manager) llmReach(o openOpts) bool { return !m.Isolate || o.netHost || o.net != NetNone }

// credentialRule is who serves a new agent session that states choice —
// the one place sessions ask the agents policy.
func (m *Manager) credentialRule(o openOpts, rel, harness, choice string) CredentialRule {
	if m.Credentials != nil {
		return m.Credentials(o.userID, rel, harness, choice)
	}
	if choice == sdkacp.UseOwn || choice == sdkacp.UseConnector {
		return CredentialRule{Use: choice}
	}
	return CredentialRule{Use: sdkacp.UseOwnFirst}
}

// rule is what the agent host is told to settle.
func (r CredentialRule) rule() string {
	switch {
	case r.Forced, r.Use == sdkacp.UseConnector:
		return sdkacp.UseConnector
	case r.Use == sdkacp.UseOwn:
		return sdkacp.UseOwn
	}
	return sdkacp.UseOwnFirst
}

// LLMWhyNot is why prov can't run on the connector under offer ("" = it
// can): the answer for a session's gate, a launcher's listing and the
// agents policy's question whether a harness can run where the connector
// alone may serve (the broker's AgentConnector).
func LLMWhyNot(prov agent.Provider, offer LLMOffer) string { return llmWhyNot(prov, offer) }

func llmWhyNot(prov agent.Provider, offer LLMOffer) string {
	switch k := prov.Connector; {
	case k == nil:
		return prov.Name + " can't use an OpenAI-compatible endpoint: it runs on your own sign-in"
	case !slices.Contains(offer.Wires, k.Wire):
		return prov.Name + " is not available through the LLM connector yet (it speaks a wire the connector doesn't serve): it runs on your own sign-in"
	case offer.Model == "":
		return "the LLM connector has no model to start " + prov.Name + " on (a workspace admin sets one: bx llm default set)"
	}
	return ""
}

// llmNoKey is why a session of o gets no key whatever else holds ("" = it
// may): its sandbox has no network and this xbind has no LLM-only listener
// to give it a door to.
func (m *Manager) llmNoKey(o openOpts) string {
	if !m.llmReach(o) && m.llmDoorSock() == "" {
		return "it can't be reached from a session without network here (net: none, and this xbind serves no LLM-only listener)"
	}
	return ""
}

// llmOnlyErr is why a session that only the connector may serve doesn't
// start, or ends: the rule and what is missing. It is ErrForbidden (403).
type llmOnlyErr string

func (e llmOnlyErr) Error() string   { return string(e) }
func (e llmOnlyErr) Is(t error) bool { return t == ErrForbidden }
func (l *llmSession) errOnly(what string) error {
	lead := "This workspace runs coding agents on its LLM connector only"
	if l.why != "" {
		lead = l.why + ", so a coding agent here runs on the workspace's LLM connector only"
	}
	return llmOnlyErr(lead + " — and " + what)
}

// llmGate checks an agent session's own choice (POST /term/sessions {llm})
// against what can be had, and keeps it in o: "" (the rule decides),
// "connector" or "own". Asking for the connector where it can't serve is
// refused with why (409) — a choice is never silently dropped — and so is
// a session the policy gives the connector alone when it asks for its own
// sign-in or the connector can't serve it (403: the rule stands, the
// session doesn't open). The int is the refusal's HTTP status.
func (m *Manager) llmGate(o *openOpts, rel string, prov agent.Provider, choice string, options map[string]string) (int, error) {
	switch choice {
	case "", sdkacp.UseOwn, sdkacp.UseConnector:
	default:
		return 400, fmt.Errorf("llm: %q — connector or own", choice)
	}
	rule := m.credentialRule(*o, rel, prov.ID, choice)
	if choice != sdkacp.UseConnector && !rule.Forced {
		o.llmChoice = choice
		return 0, nil
	}
	if rule.Forced && len(options) > 0 && prov.Connector != nil && m.LLM != nil { // the options it starts with: a model of the connector's, or none
		check := &llmSession{forced: true, why: rule.Reason}
		check.offer, _ = m.LLM.Offer(context.Background(), llmUser(*o), rel, KindAgent)
		for id, v := range options {
			if err := check.option(prov, id, v); err != nil {
				return 403, err
			}
		}
	}
	code, lead := 409, ""
	if rule.Forced {
		code, lead = 403, "This workspace runs coding agents on its LLM connector only"
		if rule.Reason != "" {
			lead = rule.Reason + ", so a coding agent here runs on the workspace's LLM connector"
		}
		if choice == sdkacp.UseOwn {
			return 403, errors.New(lead + " — not on your own sign-in")
		}
		lead += " — and "
	}
	why := ""
	if m.LLM == nil {
		why = "this workspace has no LLM connector configured (a workspace admin adds an upstream: bx llm upstream add)"
	} else if offer, ok := m.LLM.Offer(context.Background(), llmUser(*o), rel, kindLaunch); !ok || !offer.Agents {
		why = "the workspace's LLM connector is not offered to agent sessions (not configured, or switched off)"
	} else if w := llmWhyNot(prov, offer); w != "" {
		why = w
	} else if w := m.llmNoKey(*o); w != "" {
		why = "the workspace's LLM connector can't serve this session: " + w
	}
	if why != "" {
		return code, errors.New(lead + why)
	}
	o.llmChoice = choice
	return 0, nil
}

// llmArm gives a new session its hold on the connector, when it gets one,
// and answers the revoke that ends the session's credentials: the terminal
// token's (revokeTok) and the LLM key's. prov is nil for a shell. Called
// once per session, right after prepare. An error is a session that only
// the connector may serve and can't have it: it doesn't start (the caller
// revokes the token).
func (m *Manager) llmArm(o *openOpts, rel string, prov *agent.Provider, revokeTok func()) (func(), error) {
	if rel == "" || o.purpose != "" {
		return revokeTok, nil
	}
	l := &llmSession{door: !m.llmReach(*o)}
	if prov != nil {
		cr := m.credentialRule(*o, rel, prov.ID, o.llmChoice)
		l.rule, l.forced, l.why = cr.rule(), cr.Forced, cr.Reason
	}
	none := func(what string) (func(), error) { // the session gets nothing of the connector
		if l.forced {
			return revokeTok, l.errOnly(what)
		}
		return revokeTok, nil
	}
	if m.LLM == nil {
		return none("this workspace has no LLM connector configured")
	}
	kind := KindShell
	if prov != nil {
		kind = KindAgent
	}
	offer, ok := m.LLM.Offer(context.Background(), llmUser(*o), rel, kind)
	if !ok {
		return none("the connector is not configured, or is switched off")
	}
	l.offer = offer
	if prov == nil {
		if !offer.Terminals || o.noLLM || m.llmNoKey(*o) != "" {
			return revokeTok, nil
		}
	} else {
		if !offer.Agents {
			return none("the connector is not offered to agent sessions")
		}
		why, noKey := llmWhyNot(*prov, l.offer), m.llmNoKey(*o)
		switch {
		case why != "":
			if prov.Connector != nil { // a CLI that never could says nothing new
				o.llm = &llmSession{note: why}
			}
			return none(why)
		case noKey != "":
			if l.rule != sdkacp.UseOwn {
				o.llm = &llmSession{note: "The workspace's LLM connector doesn't serve this session — " + noKey + ": " + prov.Name + " runs on your own sign-in."}
			}
			return none(noKey)
		case l.rule == sdkacp.UseOwn && !prov.Connector.Additive:
			l.serves = sdkacp.ServeOwn // nothing of the connector's goes in: no key
			o.llm = l
			return revokeTok, nil
		}
	}
	label := "terminal"
	if prov != nil {
		label = "agent:" + prov.ID
	}
	var refused func(int, string, string)
	if prov != nil {
		refused = l.refused // said in the session (llmRefused)
	}
	token, revoke, err := m.LLM.Mint(llmUser(*o), rel, o.target.env, label, refused)
	if err != nil {
		slog.Warn("session: no LLM key", "tile", rel, "forced", l.forced, "err", err)
		o.llm = nil
		return none("no key could be made for the session (" + err.Error() + ")")
	}
	l.token, l.revoke = token, revoke
	o.llm = l
	return func() {
		revokeTok()
		l.end()
	}, nil
}

// llmDoorNet sets the network of the sandbox of a session with no network:
// none — or, when the session holds an LLM key, a relay that reaches
// nothing (its policy stays empty): the relay is what the door hangs on
// (llmDoorRelay). It reports whether it gave it the relay.
func (m *Manager) llmDoorNet(o openOpts, spec *sandbox.Spec) bool {
	if spec.Net = "none"; m.llmDoor(o) == "" {
		return false
	}
	spec.Net = "relay"
	return true
}

// llmDoorRelay makes the relay of such a session's sandbox a dead end but
// for the door: no forward to xbind's own listener, one gateway port to the
// LLM-only listener, and every DNS query refused at once — a CLI that looks
// its vendor up fails fast instead of waiting out a resolver that never
// answers.
func (m *Manager) llmDoorRelay(o openOpts, cfg *relay.Config) {
	if sock := m.llmDoor(o); sock != "" && o.net == NetNone && !o.netHost {
		cfg.HostFwd, cfg.Resolver, cfg.DNSRefuse = nil, "", true
		cfg.Doors = map[int]string{llmDoorPort: "unix:" + sock}
	}
}

// llmTail is the last n bytes of a terminal's output for xbind's own log and
// the sandbox failure registry, LLM keys masked — and where the cut may
// have split a key (a full tail that starts inside a word of a key's
// characters), that leading word dropped: half a key is not left in it.
func llmTail(h *termwire.Hub, n int) string {
	b := h.Tail(n)
	if len(b) >= n {
		i := 0
		for i < len(b) && i < 64 && (b[i] == '_' || b[i] == '-' || b[i] >= '0' && b[i] <= '9' || b[i] >= 'a' && b[i] <= 'z' || b[i] >= 'A' && b[i] <= 'Z') {
			i++
		}
		b = b[i:]
	}
	return scrollTail([]byte(maskLLMKeys(string(b))), n)
}

// llmAddr is the connector's OpenAI base URL as o's session reaches it:
// the door's for a session with no network, else under the XBIN_URL the
// session itself was given ("" without one).
func (o openOpts) llmAddr(env []string) string {
	if o.llm != nil && o.llm.door {
		return llmDoorURL
	}
	return llmURL(env)
}

func llmURL(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "XBIN_URL="); ok && v != "" {
			return strings.TrimRight(v, "/") + "/api/xbin/llm/v1"
		}
	}
	return ""
}

// llmShellEnv is what a plain terminal's environment gains: the connector's
// address and the session's key, and the OPENAI_* pair when the workspace
// switched it on. Nothing for an agent session — its key travels in the
// spawn frame, to the agent's process alone.
func (o openOpts) llmShellEnv(env []string) []string {
	l := o.llm
	if l == nil || l.token == "" || o.kind == KindAgent {
		return nil
	}
	url := o.llmAddr(env)
	if url == "" {
		return nil
	}
	out := []string{sdkacp.ConnectorURLEnv + "=" + url, sdkacp.ConnectorKeyEnv + "=" + l.token}
	if l.offer.OpenAIEnv {
		out = append(out, "OPENAI_BASE_URL="+url, "OPENAI_API_KEY="+l.token)
	}
	return out
}

// llmSpawn is the connector's part of an agent session's spawn frame: both
// outcomes rendered for the host to settle. nil when the session has no
// key (no connector, or its person's own sign-in serves by choice). An
// error is a session that only the connector may serve with nothing to
// start it on: the agent is not spawned.
func (m *Manager) llmSpawn(o openOpts, prov agent.Provider, env []string) (*acp.SpawnLLM, error) {
	l := o.llm
	if l == nil || l.token == "" {
		if l != nil && l.forced {
			return nil, l.errOnly("the session holds no key for it")
		}
		return nil, nil
	}
	url := o.llmAddr(env)
	if url == "" {
		l.end() // no address to give: the key is of no use
		if l.forced {
			return nil, l.errOnly("the session has no address to reach it at")
		}
		return nil, nil
	}
	params := sdkacp.ConnectorParams{URL: url, Key: l.token, Models: l.offer.Models, Model: l.offer.Model, Only: l.forced}
	delta := func(serves string) acp.EnvDelta {
		params.Serves = serves
		e, _ := prov.ConnectorEnv(params)
		return acp.EnvDelta{Set: e.Lines(), Unset: e.Unset}
	}
	out := &acp.SpawnLLM{Rule: l.rule, LoginFiles: prov.LoginFiles, Logins: map[string][]string{},
		Connector: delta(sdkacp.ServeConnector), Own: delta(sdkacp.ServeOwn)}
	if l.forced {
		if len(out.Connector.Set) == 0 {
			l.end()
			return nil, l.errOnly("nothing could be rendered to point " + prov.Name + " at it")
		}
		out.Own = out.Connector // nothing else to settle on
	}
	for _, k := range prov.Keys {
		out.KeyEnvs = append(out.KeyEnvs, k.Env)
	}
	for _, p := range agent.Providers() {
		if len(p.LoginFiles) > 0 {
			out.Logins[p.ID] = p.LoginFiles
		}
	}
	return out, nil
}

// llmHello takes the host's word on who serves the session's model calls
// (the daemon's own when nothing was sent), ends a key the outcome has no
// use for, and remembers which providers the person is signed in to.
func (s *Session) llmHello(m *Manager, h acp.HelloParams) {
	st := s.agent
	if st == nil || st.llm == nil {
		return
	}
	l := st.llm
	m.noteLogins(s.homeKey, h.Logins)
	if l.token == "" {
		return
	}
	l.mu.Lock()
	if l.serves != "" || l.ended { // the host says it once, at the spawn: nothing later changes it
		l.mu.Unlock()
		return
	}
	l.serves = h.LLM
	serves := l.serves
	l.offered = serves == sdkacp.ServeConnector && !l.forced && st.provider.Connector != nil && st.provider.Connector.Additive
	offered := l.offered
	l.mu.Unlock()
	switch {
	case l.forced && serves != sdkacp.ServeConnector:
		s.llmEnd(m, l.errOnly("its agent did not start on it"))
	case serves == sdkacp.ServeConnector:
		note := "Model calls go through this workspace's LLM connector (" + sdkacp.ConnectorName + "), metered to you."
		if offered {
			note = "This workspace's LLM connector (" + sdkacp.ConnectorName + ") is offered to the agent as its default model, metered to you. Its own or the project's configuration can still leave it out and use another provider."
		}
		s.logEvent(m, agent.New(EvNotice, llmNotice{Text: note, LLM: serves, Offered: offered}))
		st.logf(note)
	case serves == sdkacp.ServeOwn && st.provider.Connector != nil && st.provider.Connector.Additive:
		// declared next to the person's own providers: the key stays
	default: // their own sign-in serves (or a host that settled nothing): the key is of no use
		l.end()
	}
}

// llmEnd ends a session that only the connector may serve and is not on it:
// the reason is said in the session, its key ended, its agent killed.
func (s *Session) llmEnd(m *Manager, why error) {
	st := s.agent
	st.llm.mu.Lock()
	st.llm.serves, st.llm.offered = "", false
	st.llm.mu.Unlock()
	st.llm.end()
	text := why.Error() + ". The session was ended."
	s.logEvent(m, agent.New(EvNotice, noticeData{Text: text}))
	st.logf(text)
	slog.Warn("agent session ended: not on the LLM connector, which alone may serve it", "id", s.ID, "tile", m.tileOf(s))
	s.kill()
}

// llmHeld checks, once the agent's handshake is done, that a session only
// the connector may serve is on it — the host said so (llmHello) — and ends
// it otherwise: a host that answered nothing settles nothing.
func (s *Session) llmHeld(m *Manager) error {
	st := s.agent
	if st == nil || st.llm == nil || !st.llm.forced {
		return nil
	}
	if st.llmServes() == sdkacp.ServeConnector {
		return nil
	}
	err := st.llm.errOnly("its agent did not start on it")
	s.llmEnd(m, err)
	return err
}

// llmOption checks a config option a client sets on a session — at its
// start (category "") or later — where the connector alone may serve: the
// model's must be one of the connector's (sdkacp's AllowsOption); anything
// else passes. Without the rule everything passes.
func (l *llmSession) option(prov agent.Provider, id, value string) error {
	if l == nil || !l.forced {
		return nil
	}
	l.mu.Lock()
	cat := l.cats[id]
	l.mu.Unlock()
	if prov.Connector.AllowsOption(id, cat, value, l.offer.Models) {
		return nil
	}
	return l.errOnly(fmt.Sprintf("%q is not one of its models", value))
}

// llmOptions remembers the category of each config option the agent
// advertised (a status event's options), for llmOption.
func (st *agentState) llmOptions(data []byte) {
	l := st.llm
	if l == nil || !l.forced {
		return
	}
	var d struct {
		Options []struct{ ID, Category string }
	}
	if json.Unmarshal(data, &d) != nil || len(d.Options) == 0 {
		return
	}
	l.mu.Lock()
	if l.cats == nil {
		l.cats = map[string]string{}
	}
	for _, o := range d.Options {
		l.cats[o.ID] = o.Category
	}
	l.mu.Unlock()
}

// llmNotice is the notice an agent session logs when the connector serves
// it: a notice's text, plus who serves (a client's header may say so) and
// whether the connector is only offered (llmSession.offered).
type llmNotice struct {
	Text    string `json:"text"`
	LLM     string `json:"llm"`
	Offered bool   `json:"offered,omitempty"`
}

// llmSay logs what an agent session has to say about the connector, once
// it is listed: why it runs without it when it otherwise would not.
func (s *Session) llmSay(m *Manager) {
	st := s.agent
	if st == nil || st.llm == nil {
		return
	}
	say := func(text string) {
		s.logEvent(m, agent.New(EvNotice, noticeData{Text: text}))
		st.logf(text)
	}
	st.llm.mu.Lock()
	st.llm.say = say // …and what the connector refuses it later (refused)
	st.llm.mu.Unlock()
	if st.llm.note != "" {
		say(st.llm.note)
	}
}

// llmServes is who serves an agent session's model calls, for its row:
// "connector", "own", or "" (no connector, or not settled yet).
func (st *agentState) llmServes() string {
	if st == nil || st.llm == nil {
		return ""
	}
	st.llm.mu.Lock()
	defer st.llm.mu.Unlock()
	return st.llm.serves
}

// llmOffered: the host said the connector serves, and nothing holds the
// CLI to it (llmSession.offered) — its row says llm "connector" with
// llmOffered, and a client says "offered", not "on".
func (st *agentState) llmOffered() bool {
	if st == nil || st.llm == nil {
		return false
	}
	st.llm.mu.Lock()
	defer st.llm.mu.Unlock()
	return st.llm.serves == sdkacp.ServeConnector && st.llm.offered
}

// ---- what the launcher is told (GET /agent/providers) ---------------------------

// llmLogins remembers, per home, which providers an agent host last found
// signed in there. In memory: a hint for the launcher, never a decision.
type llmLogins struct {
	mu sync.Mutex
	by map[string]map[string]bool
}

func (m *Manager) noteLogins(homeKey string, logins map[string]bool) {
	if len(logins) == 0 {
		return
	}
	m.llmSeen.mu.Lock()
	defer m.llmSeen.mu.Unlock()
	if m.llmSeen.by == nil {
		m.llmSeen.by = map[string]map[string]bool{}
	}
	m.llmSeen.by[homeKey] = logins
}

func (m *Manager) knownLogin(homeKey, provider string) (signedIn, known bool) {
	m.llmSeen.mu.Lock()
	defer m.llmSeen.mu.Unlock()
	signedIn, known = m.llmSeen.by[homeKey][provider]
	return
}

// ProviderLLM says what an agent session of one provider would run on, for
// GET /agent/providers.
type ProviderLLM struct {
	// Use: "connector" (the workspace's LLM connector), "own" (the person's
	// own sign-in), "own-first" (their own sign-in when the session finds
	// one, else the connector: nothing looked yet), "signin" (it needs a
	// sign-in: the connector can't serve it and they have none) or
	// "unavailable" (the tile's rules give it the connector alone, which
	// can't serve it: a session is refused).
	Use string `json:"use"`
	// Connector: the connector can serve this provider here. Note says why
	// not, when it could in principle.
	Connector bool   `json:"connector"`
	Note      string `json:"note,omitempty"`
	// Forced: the connector alone may serve a session here, whatever the
	// person chose; Reason names the rule that says so, when one of the
	// tile's orgs does ("Finance allows private models only").
	Forced bool   `json:"forced,omitempty"`
	Reason string `json:"reason,omitempty"`
	// SignedIn: whether the person's home had a sign-in of this provider
	// when an agent session last looked; absent before any did.
	SignedIn *bool `json:"signedIn,omitempty"`
}

// AgentLLM answers, per provider id, what a new agent session of the person
// with homeKey (user "" = the root token) would run on; nil when sessions
// get nothing of the connector — then GET /agent/providers is today's.
func (m *Manager) AgentLLM(userID, homeKey, tile string, provs []agent.Provider) map[string]*ProviderLLM {
	if m.LLM == nil {
		return nil
	}
	o := openOpts{userID: userID}
	offer, ok := m.LLM.Offer(context.Background(), llmUser(o), tile, kindLaunch)
	if !ok || !offer.Agents {
		return nil
	}
	out := map[string]*ProviderLLM{}
	for _, prov := range provs {
		row := &ProviderLLM{}
		cr := m.credentialRule(o, tile, prov.ID, "")
		why := llmWhyNot(prov, offer)
		row.Connector, row.Forced, row.Reason = why == "", cr.Forced, cr.Reason
		if prov.Connector != nil {
			row.Note = why
		}
		signedIn, known := m.knownLogin(homeKey, prov.ID)
		if known {
			row.SignedIn = &signedIn
		}
		rule := cr.rule()
		switch {
		case !row.Connector && cr.Forced:
			row.Use = "unavailable"
		case !row.Connector && known && !signedIn:
			row.Use = "signin"
		case !row.Connector, rule == sdkacp.UseOwn:
			row.Use = sdkacp.ServeOwn
		case rule == sdkacp.UseConnector:
			row.Use = sdkacp.ServeConnector
		case !known:
			row.Use = sdkacp.UseOwnFirst
		default:
			row.Use = sdkacp.ConnectorServes(rule, "", "", signedIn)
		}
		out[prov.ID] = row
	}
	return out
}

// llmRestart is the choice a restarted session carries over (RestartAgent):
// its own, unless the new session could not have it (the connector from
// another network scope or target, or switched off since; its own sign-in
// under a rule that now gives the connector alone) — then the rule decides
// again and the new session says what it runs on, rather than the restart
// failing after the old session ended.
func (m *Manager) llmRestart(p auth.Principal, s *Session, st *agentState, net, gpu string, api bool, deployment string) string {
	if st.llmChoice == "" {
		return ""
	}
	tile := m.tileOf(s)
	o := m.openOptsFor(p, tile, s.Cwd, normalizeNet(net), gpu, api)
	if _, err := m.pickTarget(p, &o, tile, deployment); err != nil {
		return "" // the restart's own target check refuses it, before the session ends
	}
	if _, err := m.llmGate(&o, tile, st.provider, st.llmChoice, nil); err != nil {
		return ""
	}
	return st.llmChoice
}
