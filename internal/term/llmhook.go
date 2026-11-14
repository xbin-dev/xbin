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
// is a new session with a new key. A session whose sandbox has no route to
// xbind (net: none) gets none, and neither does one that targets a
// non-primary deployment (a key carries no deployment, so the tile's edge
// policy could not be held to its calls); an agent session says so.
//
// Without a connector (Manager.LLM nil, or its Offer false) nothing here
// runs: sessions are exactly as before.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agent/acp"
	"github.com/xbin-dev/xbin/internal/auth"
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
	Mint(user, tile, label string, refused func(status int, code, message string)) (token string, revoke func(), err error)
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

// CredentialRule is the credential mode in force for one person's agent
// session of one harness on one tile, and the connector models it may use:
// what the agents policy answers (D229).
type CredentialRule struct {
	// Mode: CredOwnFirst (and ""), CredOwn, CredConnector or
	// CredConnectorFirst, CredConnectorOnly.
	Mode string
	// Models, when not nil, are the only connector models the session may
	// be given ("<upstream>/<model>"); nil: any.
	Models []string
}

// The credential modes.
const (
	CredOwnFirst       = "own-first"       // the person's own sign-in when the session finds one, else the connector
	CredOwn            = "own"             // their own sign-in (their saved choice)
	CredConnector      = "connector"       // the connector (their saved choice, or the workspace's default)
	CredConnectorFirst = "connector-first" // the same, as a workspace default: the connector unless a session says own
	CredConnectorOnly  = "connector-only"  // the connector and nothing else: not a session's to override
)

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
	// Credentials answers the credential rule in force for a person (user
	// id; "" the root token) opening an agent session of harness on tile:
	// their saved default, the workspace's, what the tile's orgs force —
	// the agents policy's one answer (D229; boot wires it). nil ⇒ own-first,
	// any model.
	Credentials func(userID, tile, harness string) CredentialRule

	llmSeen llmLogins // which providers each home was last found signed in to
}

// llmOpts is a new session's share (openOpts).
type llmOpts struct {
	llm       *llmSession // the session's hold on the connector (llmArm); nil = none
	llmChoice string      // an agent session's own choice of who serves it ("" | connector | own)
	noLLM     bool        // a person whose terminals hold no tile API token (D17): no LLM key in one either
}

// llmOwner is the connector's name for the root token's sessions
// (internal/llm's OwnerUser).
const llmOwner = "owner"

// llmSession is one session's hold on the connector.
type llmSession struct {
	offer  LLMOffer
	rule   string // agents: what the host settles — connector | own | own-first
	token  string
	revoke func()

	mu     sync.Mutex
	serves string // agents: who serves, once the host said (sdkacp.ServeConnector | ServeOwn)
	note   string // agents: said in the session once it is listed
	ended  bool
	say    func(text string)    // agents: logs a notice in the session, once it is listed (llmSay)
	told   map[string]time.Time // agents: when each refusal code was last said
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

// credentialRule is the rule in force for a new agent session — the one
// place sessions ask the agents policy.
func (m *Manager) credentialRule(o openOpts, rel, harness string) CredentialRule {
	if m.Credentials == nil {
		return CredentialRule{Mode: CredOwnFirst}
	}
	return m.Credentials(o.userID, rel, harness)
}

// llmRule is what the agent host is told to settle: the rule, with the
// session's own choice over it unless the rule is the connector alone.
func llmRule(choice string, r CredentialRule) string {
	switch {
	case r.Mode == CredConnectorOnly:
		return sdkacp.UseConnector
	case choice == sdkacp.UseOwn || choice == sdkacp.UseConnector:
		return choice
	case r.Mode == CredOwn:
		return sdkacp.UseOwn
	case r.Mode == CredConnector || r.Mode == CredConnectorFirst:
		return sdkacp.UseConnector
	}
	return sdkacp.UseOwnFirst
}

// allowed narrows an offer to the models a rule allows: the list, and the
// default — the offered one when allowed, else the first allowed.
func (o LLMOffer) allowed(models []string) LLMOffer {
	if models == nil {
		return o
	}
	var keep []sdkacp.ConnectorModel
	for _, m := range o.Models {
		if slices.Contains(models, m.ID) {
			keep = append(keep, m)
		}
	}
	o.Models = keep
	switch {
	case slices.Contains(models, o.Model):
	case len(keep) > 0:
		o.Model = keep[0].ID
	case len(models) > 0:
		o.Model = models[0]
	default:
		o.Model = ""
	}
	return o
}

// llmWhyNot is why prov can't run on the connector under offer ("" = it can).
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
// may): its sandbox has no route to xbind, or it targets a non-primary
// deployment — a key carries no deployment, so the tile's edge policy,
// which says what such a deployment may use, could not be held to it.
func (m *Manager) llmNoKey(o openOpts) string {
	switch {
	case !m.llmReach(o):
		return "it can't be reached from a session without network (net: none)"
	case o.target.env != "":
		return "it is not offered to a session that targets a tile deployment other than the primary (" + o.target.env + ")"
	}
	return ""
}

// llmGate checks an agent session's own choice (POST /term/sessions {llm})
// against what can be had, and keeps it in o: "" (the rule decides),
// "connector" or "own". Asking for the connector where it can't serve is
// refused with why (409) — a choice is never silently dropped — and so is
// a session the rule gives the connector alone (CredConnectorOnly) when it
// asks for its own sign-in or the connector can't serve it (403: the rule
// stands, the session doesn't open). The int is the refusal's HTTP status.
func (m *Manager) llmGate(o *openOpts, rel string, prov agent.Provider, choice string) (int, error) {
	switch choice {
	case "", sdkacp.UseOwn, sdkacp.UseConnector:
	default:
		return 400, fmt.Errorf("llm: %q — connector or own", choice)
	}
	only := m.credentialRule(*o, rel, prov.ID).Mode == CredConnectorOnly
	if choice != sdkacp.UseConnector && !only {
		o.llmChoice = choice
		return 0, nil
	}
	code, lead := 409, ""
	if only {
		code, lead = 403, "this tile's rules let "+prov.Name+" run on the workspace's LLM connector only, and "
		if choice == sdkacp.UseOwn {
			return 403, errors.New("this tile's rules let " + prov.Name + " run on the workspace's LLM connector only, not on your own sign-in")
		}
	}
	why := ""
	if m.LLM == nil {
		why = "this workspace has no LLM connector configured (a workspace admin adds an upstream: bx llm upstream add)"
	} else if offer, ok := m.LLM.Offer(context.Background(), llmUser(*o), rel, kindLaunch); !ok || !offer.Agents {
		why = "the workspace's LLM connector is not offered to agent sessions (not configured, or switched off)"
	} else if w := llmWhyNot(prov, offer.allowed(m.credentialRule(*o, rel, prov.ID).Models)); w != "" {
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
// once per session, right after prepare.
func (m *Manager) llmArm(o *openOpts, rel string, prov *agent.Provider, revokeTok func()) func() {
	if m.LLM == nil || rel == "" || o.purpose != "" {
		return revokeTok
	}
	kind := KindShell
	if prov != nil {
		kind = KindAgent
	}
	offer, ok := m.LLM.Offer(context.Background(), llmUser(*o), rel, kind)
	if !ok {
		return revokeTok
	}
	l := &llmSession{offer: offer}
	if prov == nil {
		if !offer.Terminals || o.noLLM || m.llmNoKey(*o) != "" {
			return revokeTok
		}
	} else {
		if !offer.Agents {
			return revokeTok
		}
		rule := m.credentialRule(*o, rel, prov.ID)
		l.offer = offer.allowed(rule.Models)
		l.rule = llmRule(o.llmChoice, rule)
		why, noKey := llmWhyNot(*prov, l.offer), m.llmNoKey(*o)
		switch {
		case why != "":
			if prov.Connector != nil { // a CLI that never could says nothing new
				o.llm = &llmSession{note: why}
			}
			return revokeTok
		case noKey != "":
			if l.rule != sdkacp.UseOwn {
				o.llm = &llmSession{note: "The workspace's LLM connector doesn't serve this session — " + noKey + ": " + prov.Name + " runs on your own sign-in."}
			}
			return revokeTok
		case l.rule == sdkacp.UseOwn && !prov.Connector.Additive:
			l.serves = sdkacp.ServeOwn // nothing of the connector's goes in: no key
			o.llm = l
			return revokeTok
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
	token, revoke, err := m.LLM.Mint(llmUser(*o), rel, label, refused)
	if err != nil {
		slog.Warn("session: no LLM key (the session opens without the connector)", "tile", rel, "err", err)
		return revokeTok
	}
	l.token, l.revoke = token, revoke
	o.llm = l
	return func() {
		revokeTok()
		l.end()
	}
}

// llmURL is the connector's OpenAI base URL as a session with env reaches
// it: under the XBIN_URL the session itself was given ("" without one).
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
	url := llmURL(env)
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
// key (no connector, or its person's own sign-in serves by choice).
func (m *Manager) llmSpawn(o openOpts, prov agent.Provider, env []string) *acp.SpawnLLM {
	l := o.llm
	if l == nil || l.token == "" {
		return nil
	}
	url := llmURL(env)
	if url == "" {
		l.end() // no address to give: the key is of no use
		return nil
	}
	params := sdkacp.ConnectorParams{URL: url, Key: l.token, Models: l.offer.Models, Model: l.offer.Model}
	delta := func(serves string) acp.EnvDelta {
		params.Serves = serves
		e, _ := prov.ConnectorEnv(params)
		return acp.EnvDelta{Set: e.Lines(), Unset: e.Unset}
	}
	out := &acp.SpawnLLM{Rule: l.rule, LoginFiles: prov.LoginFiles, Logins: map[string][]string{},
		Connector: delta(sdkacp.ServeConnector), Own: delta(sdkacp.ServeOwn)}
	for _, k := range prov.Keys {
		out.KeyEnvs = append(out.KeyEnvs, k.Env)
	}
	for _, p := range agent.Providers() {
		if len(p.LoginFiles) > 0 {
			out.Logins[p.ID] = p.LoginFiles
		}
	}
	return out
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
	l.serves = h.LLM
	serves := l.serves
	l.mu.Unlock()
	switch {
	case serves == sdkacp.ServeConnector:
		note := "Model calls go through this workspace's LLM connector (" + sdkacp.ConnectorName + "), metered to you."
		s.logEvent(m, agent.New(EvNotice, llmNotice{Text: note, LLM: serves}))
		st.logf(note)
	case serves == sdkacp.ServeOwn && st.provider.Connector != nil && st.provider.Connector.Additive:
		// declared next to the person's own providers: the key stays
	default: // their own sign-in serves (or a host that settled nothing): the key is of no use
		l.end()
	}
}

// llmNotice is the notice an agent session logs when the connector serves
// it: a notice's text, plus who serves (a client's header may say so).
type llmNotice struct {
	Text string `json:"text"`
	LLM  string `json:"llm"`
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
		cr := m.credentialRule(o, tile, prov.ID)
		why := llmWhyNot(prov, offer.allowed(cr.Models))
		row.Connector = why == ""
		if prov.Connector != nil {
			row.Note = why
		}
		signedIn, known := m.knownLogin(homeKey, prov.ID)
		if known {
			row.SignedIn = &signedIn
		}
		rule := llmRule("", cr)
		switch {
		case !row.Connector && cr.Mode == CredConnectorOnly:
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
	o := m.openOptsFor(p, s.Cwd, s.Cwd, normalizeNet(net), gpu, api)
	if _, err := m.pickTarget(p, &o, s.Cwd, deployment); err != nil {
		return "" // the restart's own target check refuses it, before the session ends
	}
	if _, err := m.llmGate(&o, s.Cwd, st.provider, st.llmChoice); err != nil {
		return ""
	}
	return st.llmChoice
}
