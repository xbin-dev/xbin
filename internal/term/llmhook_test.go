package term

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agent/acp"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/termwire"
	"github.com/xbin-dev/xbin/internal/users"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// Sessions on the LLM connector (llmhook.go), non-isolated: the real `bx
// __agent-host` settles who serves and the scripted agent reports its own
// environment (`printenv`), so what is asserted is what a CLI would start
// with. The connector is a recording double.

type fakeLLM struct {
	mu      sync.Mutex
	offer   LLMOffer
	off     bool // Offer answers false
	n       int
	live    map[string]string // token → "user|tile|label"
	revoked []string
	kinds   []string
	refused func(status int, code, message string) // the last minted key's
	deploys map[string]string                      // token → the deployment it was minted for
	mintErr error                                  // Mint fails with it
}

func newFakeLLM() *fakeLLM {
	return &fakeLLM{live: map[string]string{}, offer: LLMOffer{Agents: true, Terminals: true,
		Wires: []string{sdkacp.WireChat, sdkacp.WireResponses}, Model: "up/m1", Models: []sdkacp.ConnectorModel{{ID: "up/m1"}, {ID: "up/m2"}}}}
}

func (f *fakeLLM) Offer(_ context.Context, user, tile, kind string) (LLMOffer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kinds = append(f.kinds, kind)
	o := f.offer
	if kind != KindAgent {
		o.Models = nil
	}
	return o, !f.off
}

func (f *fakeLLM) Mint(user, tile, deployment, label string, refused func(int, string, string)) (string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mintErr != nil {
		return "", nil, f.mintErr
	}
	f.n++
	tok := "xbl_test" + strings.Repeat("k", f.n)
	f.live[tok] = user + "|" + tile + "|" + label
	if f.deploys == nil {
		f.deploys = map[string]string{}
	}
	f.deploys[tok] = deployment
	f.refused = refused
	return tok, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.live[tok]; ok {
			delete(f.live, tok)
			f.revoked = append(f.revoked, tok)
		}
	}, nil
}

func (f *fakeLLM) liveKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for k := range f.live {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (f *fakeLLM) minted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// policy is a Manager.Credentials as the agents policy answers: a mode for
// everyone (own-first | connector-first | connector-only), people's saved
// choices by "user/harness", and the session's own choice over both unless
// the mode is connector-only (forced, with why).
func policy(mode, reason string, saved map[string]string) func(userID, tile, harness, choice string) CredentialRule {
	return func(userID, tile, harness, choice string) CredentialRule {
		if mode == "connector-only" {
			return CredentialRule{Use: sdkacp.UseConnector, Forced: true, Reason: reason}
		}
		for _, c := range []string{choice, saved[userID+"/"+harness]} {
			if c == sdkacp.UseOwn || c == sdkacp.UseConnector {
				return CredentialRule{Use: c}
			}
		}
		if mode == "connector-first" {
			return CredentialRule{Use: sdkacp.UseConnector}
		}
		return CredentialRule{Use: sdkacp.UseOwnFirst}
	}
}

// llmRig is an agent rig with a connector and an XBIN_URL for sessions.
func llmRig(t *testing.T) (*agentRig, *fakeLLM) {
	t.Helper()
	r := newAgentRig(t)
	f := newFakeLLM()
	r.m.LLM = f
	r.m.Env = func() []string { return []string{"XBIN_URL=http://127.0.0.1:1"} }
	return r, f
}

// agentEnv asks the session's agent for one variable of its own environment.
func (r *agentRig) agentEnv(t *testing.T, id, name string) string {
	t.Helper()
	if _, err := r.m.AgentPrompt(context.Background(), id, "printenv "+name); err != nil {
		t.Fatal(err)
	}
	e := r.until(t, func(e SessionEvent) bool {
		text, _ := edata(e.Event)["text"].(string)
		return e.ID == id && e.Type == agent.EvMessageDelta && strings.HasPrefix(text, "printenv: ")
	})
	text, _ := edata(e.Event)["text"].(string)
	r.until(t, func(e SessionEvent) bool { return e.ID == id && e.Type == agent.EvTurnEnd })
	return strings.TrimPrefix(text, "printenv: ")
}

// openLLM opens a fake-agent session and waits until it is idle.
func (r *agentRig) openLLM(t *testing.T, p auth.Principal, a AgentOpen) SessionInfo {
	t.Helper()
	a.Cwd, a.Provider = "apps/x", "fake"
	info, code, err := r.m.OpenAgentWith(p, a)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	return info
}

// endSession kills a session and waits for its teardown.
func (r *agentRig) endSession(t *testing.T, id string) {
	t.Helper()
	_, st, err := r.m.agentOf(id)
	if err != nil {
		t.Fatal(err)
	}
	r.m.Kill(id)
	select {
	case <-st.gone:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
}

func signIn(t *testing.T, root, homeKey string) {
	t.Helper()
	dir := filepath.Join(HomeDir(root, homeKey), ".fakeacp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte("fake-login\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

var owner = auth.Principal{Owner: true}

// Without a connector nothing is injected and nothing is minted: today's
// session.
func TestAgentLLMAbsent(t *testing.T) {
	for name, set := range map[string]func(r *agentRig, f *fakeLLM){
		"no connector":   func(r *agentRig, f *fakeLLM) { r.m.LLM = nil },
		"not configured": func(r *agentRig, f *fakeLLM) { f.off = true },
		"agents off":     func(r *agentRig, f *fakeLLM) { f.offer.Agents = false },
	} {
		t.Run(name, func(t *testing.T) {
			r, f := llmRig(t)
			set(r, f)
			info := r.openLLM(t, owner, AgentOpen{})
			if got := r.agentEnv(t, info.ID, "XBIN_LLM_KEY") + r.agentEnv(t, info.ID, "XBIN_LLM_URL") + r.agentEnv(t, info.ID, "FAKEACP_LLM_MODEL"); got != "" {
				t.Errorf("the agent's environment carries the connector: %q", got)
			}
			if row, _ := r.m.Info(info.ID); row.LLM != "" {
				t.Errorf("row llm %q", row.LLM)
			}
			if f.minted() != 0 {
				t.Errorf("%d keys minted", f.minted())
			}
			if llm := r.m.AgentLLM("", "owner", "apps/x", agent.Providers()); llm != nil {
				t.Errorf("the launcher is told %v", llm)
			}
			r.endSession(t, info.ID)
		})
	}
}

// The precedence, each branch: the session's choice, the person's saved
// choice, the workspace's mode — and own-first's look at the session's
// $HOME.
func TestAgentLLMPrecedence(t *testing.T) {
	alice := auth.Principal{UserID: "alice", User: &users.User{ID: "alice", Role: "admin"}}
	for _, c := range []struct {
		name                string
		mode, saved, choice string
		signedIn            bool
		serves              string
	}{
		{name: "own-first, no sign-in: the connector", serves: sdkacp.ServeConnector},
		{name: "own-first, signed in: their own", mode: "own-first", signedIn: true, serves: sdkacp.ServeOwn},
		{name: "connector-first, signed in: the connector", mode: "connector-first", signedIn: true, serves: sdkacp.ServeConnector},
		{name: "a saved choice of own outranks connector-first", mode: "connector-first", saved: sdkacp.UseOwn, serves: sdkacp.ServeOwn},
		{name: "a saved choice of the connector outranks own-first", saved: sdkacp.UseConnector, signedIn: true, serves: sdkacp.ServeConnector},
		{name: "the session's own outranks a saved connector", saved: sdkacp.UseConnector, choice: sdkacp.UseOwn, serves: sdkacp.ServeOwn},
		{name: "the session's connector outranks a saved own", saved: sdkacp.UseOwn, choice: sdkacp.UseConnector, signedIn: true, serves: sdkacp.ServeConnector},
		{name: "connector-only, signed in and saved as own", mode: "connector-only", saved: sdkacp.UseOwn, signedIn: true, serves: sdkacp.ServeConnector},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, f := llmRig(t)
			var asked []string
			pol := policy(c.mode, "", map[string]string{"alice/fake": c.saved})
			r.m.Credentials = func(userID, tile, harness, choice string) CredentialRule {
				asked = append(asked, userID+"|"+tile+"|"+harness+"|"+choice)
				return pol(userID, tile, harness, choice)
			}
			if c.signedIn {
				signIn(t, r.root, "alice")
			}
			info := r.openLLM(t, alice, AgentOpen{LLM: c.choice})
			if len(asked) == 0 || asked[0] != "alice|apps/x|fake|"+c.choice {
				t.Errorf("the policy was asked %v", asked)
			}
			key, url, model := r.agentEnv(t, info.ID, "XBIN_LLM_KEY"), r.agentEnv(t, info.ID, "XBIN_LLM_URL"), r.agentEnv(t, info.ID, "FAKEACP_LLM_MODEL")
			row, _ := r.m.Info(info.ID)
			if row.LLM != c.serves {
				t.Errorf("row llm %q, want %q", row.LLM, c.serves)
			}
			if c.serves == sdkacp.ServeConnector {
				if !strings.HasPrefix(key, "xbl_") || url != "http://127.0.0.1:1/api/xbin/llm/v1" || model != "up/m1" {
					t.Errorf("the connector's environment: key %q url %q model %q", key, url, model)
				}
				if live := f.liveKeys(); len(live) != 1 || live[0] != key || f.live[key] != "alice|apps/x|agent:fake" {
					t.Errorf("the session's key: live %v, grant %q", live, f.live[key])
				}
			} else {
				if key != "" || url != "" || model != "" {
					t.Errorf("their own sign-in serves, yet the environment has key %q url %q model %q", key, url, model)
				}
				if live := f.liveKeys(); len(live) != 0 {
					t.Errorf("a key outlives the decision that it isn't used: %v", live)
				}
			}
			r.endSession(t, info.ID)
			if live := f.liveKeys(); len(live) != 0 {
				t.Errorf("keys alive after the session ended: %v", live)
			}
		})
	}
}

// Where the policy gives a session the connector alone it is not the
// session's to override, a session it can't be kept for doesn't open, and
// the reason is the policy's.
func TestAgentLLMConnectorOnly(t *testing.T) {
	r, f := llmRig(t)
	r.m.Credentials = policy("connector-only", "Finance allows private models only", nil)
	open := func(provider, choice string) (int, error) {
		info, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: provider, LLM: choice})
		if err == nil {
			r.endSession(t, info.ID)
		}
		return code, err
	}
	if code, err := open("fake", sdkacp.UseOwn); code != 403 || err == nil || !strings.Contains(err.Error(), "Finance allows private models only") || !strings.Contains(err.Error(), "not on your own sign-in") {
		t.Errorf("choosing their own under connector-only: %d %v", code, err)
	}
	if code, err := open("claude", ""); code != 403 || err == nil || !strings.Contains(err.Error(), "Finance allows private models only") {
		t.Errorf("a harness the connector can't serve: %d %v", code, err)
	}
	if code, err := open("gemini", ""); code != 403 || err == nil {
		t.Errorf("gemini under connector-only: %d %v", code, err)
	}
	info := r.openLLM(t, owner, AgentOpen{})
	if row, _ := r.m.Info(info.ID); row.LLM != sdkacp.ServeConnector {
		t.Errorf("row llm %q", row.LLM)
	}
	// which of the connector's models it calls: its default — the harness's
	// own name for its setting is no model of the connector's — and what a
	// start would be vouched for beforehand (the workspace agent's taint
	// rule asks both: server/delegatesend.go)
	if got := r.m.AgentConnectorModel(info.ID); got != f.offer.Model || r.m.AgentConnectorModel("nope") != "" {
		t.Errorf("the connector's model the session calls: %q, want its default %q", got, f.offer.Model)
	}
	if held, models := r.m.DelegateStartSource("alice", "apps/x", "fake", ""); !held || len(models) != 1 || models[0] != f.offer.Model {
		t.Errorf("a start under connector-only: held %v on %v, want the connector's default", held, models)
	}
	if held, models := r.m.DelegateStartSource("alice", "apps/x", "fake", "up/other"); !held || len(models) != 2 || models[0] != "up/other" {
		t.Errorf("a start that names a model: held %v on %v, want both it and the default", held, models)
	}
	r.endSession(t, info.ID)
	// no model the tile may use: no connector, no session
	f.offer.Model = ""
	if code, err := open("fake", ""); code != 403 || err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("no model to start on: %d %v", code, err)
	}
	f.offer.Model = "up/m1"
	// the connector gone: the rule still stands
	f.off = true
	if code, err := open("fake", ""); code != 403 || err == nil {
		t.Errorf("connector-only with no connector: %d %v", code, err)
	}
	// the listing says so beforehand, with the policy's reason
	f.off = false
	llm := r.m.AgentLLM("", "owner", "apps/x", agent.Providers())
	if llm["claude"].Use != "unavailable" || llm["gemini"].Use != "unavailable" || llm["fake"].Use != sdkacp.ServeConnector || llm["opencode"].Use != sdkacp.ServeConnector ||
		!llm["fake"].Forced || llm["fake"].Reason != "Finance allows private models only" {
		t.Errorf("the listing under connector-only: claude %+v fake %+v", llm["claude"], llm["fake"])
	}
	// nothing forces the connector: nothing vouches for a start's model
	r.m.Credentials = nil
	if held, models := r.m.DelegateStartSource("alice", "apps/x", "fake", "up/m1"); held || models != nil {
		t.Errorf("a start where the person may use their own sign-in: held %v %v", held, models)
	}
	// the workspace's own connector-only: forced, with no org's reason
	r.m.Credentials = policy("connector-only", "", nil)
	if code, err := open("fake", sdkacp.UseOwn); code != 403 || err == nil || !strings.Contains(err.Error(), "This workspace runs coding agents on its LLM connector only") {
		t.Errorf("the workspace's connector-only: %d %v", code, err)
	}
}

// An additive CLI (opencode's kind) always gets the connector declared; the
// default model only when the connector serves.
func TestAgentLLMAdditive(t *testing.T) {
	t.Setenv(agent.FakeEnv, fakeBin+" --llm-additive")
	r, f := llmRig(t)
	signIn(t, r.root, "owner")
	info := r.openLLM(t, owner, AgentOpen{})
	key, model := r.agentEnv(t, info.ID, "XBIN_LLM_KEY"), r.agentEnv(t, info.ID, "FAKEACP_LLM_MODEL")
	if !strings.HasPrefix(key, "xbl_") || model != "" {
		t.Fatalf("signed in: key %q (declared), model %q (theirs stays)", key, model)
	}
	if row, _ := r.m.Info(info.ID); row.LLM != sdkacp.ServeOwn || row.LLMOffered {
		t.Errorf("row llm %q offered %v", row.LLM, row.LLMOffered)
	}
	if live := f.liveKeys(); len(live) != 1 || live[0] != key {
		t.Errorf("the declared provider's key must stay alive: %v", live)
	}
	r.endSession(t, info.ID)
	if live := f.liveKeys(); len(live) != 0 {
		t.Errorf("keys alive after the session ended: %v", live)
	}

	// no sign-in at all: the connector's model IS the default
	if err := os.RemoveAll(filepath.Join(HomeDir(r.root, "owner"), ".fakeacp")); err != nil {
		t.Fatal(err)
	}
	info = r.openLLM(t, owner, AgentOpen{})
	if model := r.agentEnv(t, info.ID, "FAKEACP_LLM_MODEL"); model != "up/m1" {
		t.Errorf("no sign-in: model %q, want the connector's", model)
	}
	// …offered, not held: no rule, and an additive CLI's own or project
	// config can leave the connector out — the row, the page and the
	// notice say so
	if row, _ := r.m.Info(info.ID); row.LLM != sdkacp.ServeConnector || !row.LLMOffered {
		t.Errorf("row llm %q offered %v, want connector, offered", row.LLM, row.LLMOffered)
	}
	p, err := r.m.AgentPage(info.ID, 0, 50)
	if err != nil || p.State.LLM != sdkacp.ServeConnector || !p.State.LLMOffered {
		t.Errorf("page state llm %q offered %v (%v)", p.State.LLM, p.State.LLMOffered, err)
	}
	var notice map[string]any
	for _, e := range p.Events {
		if e.Type == EvNotice {
			notice = edata(e)
		}
	}
	if text, _ := notice["text"].(string); notice["llm"] != sdkacp.ServeConnector || notice["offered"] != true || !strings.Contains(text, "is offered to the agent") {
		t.Errorf("notice: %v", notice)
	}
	r.endSession(t, info.ID)

	// where the rule gives it the connector alone, the CLI is held to it
	r.m.Credentials = policy("connector-only", "Finance allows private models only", nil)
	info = r.openLLM(t, owner, AgentOpen{})
	if row, _ := r.m.Info(info.ID); row.LLM != sdkacp.ServeConnector || row.LLMOffered {
		t.Errorf("forced: row llm %q offered %v, want connector, held", row.LLM, row.LLMOffered)
	}
	r.endSession(t, info.ID)
}

// A session the connector serves says so once, as a notice that names it;
// a CLI whose connector configuration replaces the person's (not additive)
// is on it, not offered it.
func TestAgentLLMNotice(t *testing.T) {
	r, _ := llmRig(t)
	info, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake"})
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	e := r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == EvNotice })
	if d := edata(e.Event); d["llm"] != sdkacp.ServeConnector || !strings.Contains(d["text"].(string), "go through this workspace's LLM connector") || d["offered"] != nil {
		t.Fatalf("notice: %v", d)
	}
	if row, _ := r.m.Info(info.ID); row.LLM != sdkacp.ServeConnector || row.LLMOffered {
		t.Errorf("row llm %q offered %v", row.LLM, row.LLMOffered)
	}
	r.endSession(t, info.ID)
}

// The key ends on every way out: a kill, the agent's own exit, a restart
// (whose new session — resumed or not — has a key of its own).
func TestAgentLLMKeyRevoked(t *testing.T) {
	r, f := llmRig(t)
	ctx := context.Background()

	// the agent exits on its own
	info := r.openLLM(t, owner, AgentOpen{})
	_, st, _ := r.m.agentOf(info.ID)
	if len(f.liveKeys()) != 1 {
		t.Fatalf("live keys: %v", f.liveKeys())
	}
	if _, err := r.m.AgentPrompt(ctx, info.ID, "crash"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.gone:
	case <-time.After(10 * time.Second):
		t.Fatal("the crashed session did not end")
	}
	if live := f.liveKeys(); len(live) != 0 {
		t.Fatalf("the key outlived a crashed agent: %v", live)
	}

	// a restart: the old key ends, the new session has another
	info = r.openLLM(t, owner, AgentOpen{LLM: sdkacp.UseConnector})
	first := r.agentEnv(t, info.ID, "XBIN_LLM_KEY")
	next, resumed, code, err := r.m.RestartAgent(owner, info.ID, "", "", true, nil)
	if err != nil || code != 200 {
		t.Fatalf("restart: %d %v", code, err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == next.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	second := r.agentEnv(t, next.ID, "XBIN_LLM_KEY")
	if !resumed || !strings.HasPrefix(second, "xbl_") || second == first {
		t.Fatalf("the restarted (resumed %v) session's key %q, the first %q", resumed, second, first)
	}
	if live := f.liveKeys(); len(live) != 1 || live[0] != second {
		t.Fatalf("after the restart: live %v, want only the new session's", live)
	}
	_, st2, _ := r.m.agentOf(next.ID)
	if st2.llmChoice != sdkacp.UseConnector {
		t.Errorf("the session's own choice did not carry over the restart: %q", st2.llmChoice)
	}
	r.endSession(t, next.ID)
	if live := f.liveKeys(); len(live) != 0 {
		t.Fatalf("keys alive at the end: %v", live)
	}
	if f.minted() != 3 || len(f.revoked) != 3 {
		t.Errorf("%d minted, %d revoked", f.minted(), len(f.revoked))
	}
}

// A session that can't start still ends its key.
func TestAgentLLMKeyRevokedOnSpawnFailure(t *testing.T) {
	r, f := llmRig(t)
	r.m.BxPath = filepath.Join(t.TempDir(), "no-such-bx")
	if _, _, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake"}); err == nil {
		t.Fatal("the session opened without its host")
	}
	if f.minted() != 1 || len(f.liveKeys()) != 0 {
		t.Fatalf("%d minted, live %v", f.minted(), f.liveKeys())
	}
}

// Asking for the connector where it can't serve is refused with the reason;
// a harness the connector has no wire for runs on the person's own sign-in.
func TestAgentLLMChoiceRefused(t *testing.T) {
	r, f := llmRig(t)
	open := func(provider, choice string) (int, error) {
		info, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: provider, LLM: choice})
		if err == nil {
			r.endSession(t, info.ID)
		}
		return code, err
	}
	if code, err := open("fake", "bogus"); code != 400 || err == nil {
		t.Errorf("an unknown choice: %d %v", code, err)
	}
	if code, err := open("gemini", sdkacp.UseConnector); code != 409 || err == nil || !strings.Contains(err.Error(), "own sign-in") {
		t.Errorf("gemini on the connector: %d %v", code, err)
	}
	if code, err := open("claude", sdkacp.UseConnector); code != 409 || err == nil || !strings.Contains(err.Error(), "not available through the LLM connector yet") {
		t.Errorf("claude on the connector: %d %v", code, err)
	}
	f.offer.Model = ""
	if code, err := open("fake", sdkacp.UseConnector); code != 409 || err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("no model: %d %v", code, err)
	}
	f.offer.Model = "up/m1"
	f.off = true
	if code, err := open("fake", sdkacp.UseConnector); code != 409 || err == nil {
		t.Errorf("connector off: %d %v", code, err)
	}
	r.m.LLM = nil
	if code, err := open("fake", sdkacp.UseConnector); code != 409 || err == nil {
		t.Errorf("no connector: %d %v", code, err)
	}
	if code, err := open("fake", sdkacp.UseOwn); code != 200 || err != nil {
		t.Errorf("choosing their own never needs a connector: %d %v", code, err)
	}
	if f.minted() != 0 {
		t.Errorf("%d keys minted for refused sessions", f.minted())
	}
}

// A session with no network reaches the connector through one door — when
// this xbind serves the LLM-only listener — and nothing else; without the
// listener it gets no key and says so. A session that targets a deployment
// other than the primary gets a key that names the deployment.
func TestLLMDoorAndDeployment(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	f := newFakeLLM()
	m.LLM, m.Isolate, m.Listen = f, true, "127.0.0.1:8080"
	fake, _ := agent.Lookup("fake")
	arm := func(o *openOpts, prov *agent.Provider) {
		t.Helper()
		if _, err := m.llmArm(o, "apps/x", prov, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	// no listener: no key, said in an agent session, refused when asked for
	o := openOpts{net: NetNone, kind: KindAgent}
	arm(&o, &fake)
	if f.minted() != 0 || o.llm == nil || !strings.Contains(o.llm.note, "net: none") {
		t.Fatalf("offline without the listener: minted %d, llm %+v", f.minted(), o.llm)
	}
	if code, err := m.llmGate(&o, "apps/x", fake, sdkacp.UseConnector, nil); code != 409 || err == nil || !strings.Contains(err.Error(), "net: none") {
		t.Errorf("asking for the connector offline without the listener: %d %v", code, err)
	}
	o = openOpts{net: NetNone}
	arm(&o, nil)
	if f.minted() != 0 || o.llm != nil {
		t.Errorf("an offline terminal without the listener: minted %d, llm %+v", f.minted(), o.llm)
	}

	// with it: a key, the door's address, a relay that reaches the door alone
	m.LLMDoor = func() string { return "/run/xbin/llm.sock" }
	for _, prov := range []*agent.Provider{&fake, nil} {
		o = openOpts{net: NetNone}
		if prov != nil {
			o.kind = KindAgent
		}
		if code, err := m.llmGate(&o, "apps/x", fake, sdkacp.UseConnector, nil); prov != nil && (code != 0 || err != nil) {
			t.Fatalf("asking for the connector offline: %d %v", code, err)
		}
		arm(&o, prov)
		if o.llm == nil || o.llm.token == "" || !o.llm.door {
			t.Fatalf("offline with the listener (agent %v): %+v", prov != nil, o.llm)
		}
		env := []string{"XBIN_URL=http://127.0.0.1:8080"}
		if got := o.llmAddr(env); got != "http://10.0.2.2:4180/v1" {
			t.Errorf("the door's address: %q", got)
		}
		spec := &sandbox.Spec{Net: "none"}
		if !m.llmDoorNet(o, spec) || spec.Net != "relay" {
			t.Errorf("no relay for the door: %+v", spec.Net)
		}
		cfg := relay.Config{HostFwd: m.hostForward()}
		m.llmDoorRelay(o, &cfg)
		if cfg.HostFwd != nil || len(cfg.Doors) != 1 || cfg.Doors[4180] != "unix:/run/xbin/llm.sock" || !cfg.DNSRefuse || cfg.Resolver != "" {
			t.Errorf("the door's relay: forwards %v, doors %v — xbind's own listener must not be there", cfg.HostFwd, cfg.Doors)
		}
		if prov == nil {
			if got := o.llmShellEnv(env); len(got) != 2 || got[0] != "XBIN_LLM_URL=http://10.0.2.2:4180/v1" {
				t.Errorf("an offline terminal's variables: %v", got)
			}
		}
	}
	// a session with network: no door, the address under its XBIN_URL, the forward kept
	o = openOpts{net: NetInternet}
	arm(&o, nil)
	spec := &sandbox.Spec{Net: "relay"}
	cfg := relay.Config{HostFwd: m.hostForward()}
	m.llmDoorRelay(o, &cfg)
	if o.llm == nil || o.llm.door || m.llmDoorNet(o, spec) || cfg.Doors != nil || cfg.HostFwd == nil {
		t.Errorf("a session with network got the door: %+v, doors %v", o.llm, cfg.Doors)
	}
	// offline with no key (terminals switched off): no relay at all
	f.offer.Terminals = false
	o = openOpts{net: NetNone}
	arm(&o, nil)
	if spec := (&sandbox.Spec{Net: "none"}); o.llm != nil || m.llmDoorNet(o, spec) || spec.Net != "none" {
		t.Errorf("an offline terminal without a key got a relay: %+v", o.llm)
	}
	f.offer.Terminals = true

	// a non-primary deployment: a key that names it
	m.Isolate = false
	for _, prov := range []*agent.Provider{&fake, nil} {
		o = openOpts{}
		o.target.env = "dev"
		arm(&o, prov)
		if o.llm == nil || o.llm.token == "" || f.deploys[o.llm.token] != "dev" {
			t.Fatalf("a session on deployment dev (agent %v): %+v, minted for %q", prov != nil, o.llm, f.deploys[o.llm.token])
		}
	}
	o = openOpts{}
	arm(&o, nil)
	if f.deploys[o.llm.token] != "" {
		t.Errorf("a session on the primary names a deployment: %q", f.deploys[o.llm.token])
	}
}

// Where the connector alone may serve, nothing fails open: a key that
// can't be made refuses the session; a host that doesn't answer
// "connector" — or answers nothing — ends it with the reason; the row never
// says connector then; and a model option outside the connector's is
// refused at the start and later.
func TestAgentLLMOnlyFailsClosed(t *testing.T) {
	r, f := llmRig(t)
	r.m.Credentials = policy("connector-only", "Finance allows private models only", nil)

	// no key: no session (and without the rule, a session without the connector)
	f.mintErr = errors.New("too many keys")
	_, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake"})
	if code != 403 || err == nil || !strings.Contains(err.Error(), "Finance allows private models only") || !strings.Contains(err.Error(), "too many keys") {
		t.Fatalf("a session without a key under connector-only: %d %v", code, err)
	}
	if n := len(r.m.List()); n != 0 {
		t.Fatalf("%d sessions left after the refusal", n)
	}
	r.m.Credentials = nil
	info := r.openLLM(t, owner, AgentOpen{})
	if row, _ := r.m.Info(info.ID); row.LLM != "" {
		t.Errorf("a session with no key says llm %q", row.LLM)
	}
	r.endSession(t, info.ID)
	f.mintErr = nil
	r.m.Credentials = policy("connector-only", "Finance allows private models only", nil)

	// the agent is told so, and the session is on the connector
	info = r.openLLM(t, owner, AgentOpen{})
	if got := r.agentEnv(t, info.ID, "FAKEACP_LLM_ONLY"); got != "1" {
		t.Errorf("the agent is not told the connector alone serves: %q", got)
	}
	// a second hello changes nothing (the host says it once)
	s, st, _ := r.m.agentOf(info.ID)
	s.llmHello(r.m, acp.HelloParams{LLM: sdkacp.ServeOwn})
	if st.llmServes() != sdkacp.ServeConnector || len(f.liveKeys()) != 1 {
		t.Fatalf("a later hello moved the session: serves %q, live %v", st.llmServes(), f.liveKeys())
	}

	// its model option: the connector's models, at the start and later
	ctx := context.Background()
	if err := r.m.AgentSetOption(ctx, info.ID, "model", "fake-fast"); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "not one of its models") {
		t.Errorf("a model outside the connector's: %v", err)
	}
	if err := r.m.AgentSetOption(ctx, info.ID, "model", "up/m2"); errors.Is(err, ErrForbidden) {
		t.Errorf("a model of the connector's: %v", err)
	}
	r.endSession(t, info.ID)
	if _, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake", Options: map[string]string{"model": "fake-fast"}}); code != 403 || err == nil || !strings.Contains(err.Error(), "not one of its models") {
		t.Errorf("starting on a model outside the connector's: %d %v", code, err)
	}
	if n := f.minted(); n != 1 {
		t.Errorf("%d keys minted: the refused start made one", n)
	}
	// without the rule both pass through to the agent
	r.m.Credentials = nil
	info = r.openLLM(t, owner, AgentOpen{Options: map[string]string{"model": "fake-fast"}})
	if err := r.m.AgentSetOption(ctx, info.ID, "model", "fake-default"); err != nil {
		t.Errorf("a model option without the rule: %v", err)
	}
	r.endSession(t, info.ID)
	r.m.Credentials = policy("connector-only", "", nil)

	// a host that says the person's own sign-in serves: the session ends, with the reason
	for _, how := range []string{"hello", "nothing"} {
		info = r.openLLM(t, owner, AgentOpen{})
		s, st, _ = r.m.agentOf(info.ID)
		st.llm.mu.Lock()
		st.llm.serves = "" // as if the host had not spoken yet
		st.llm.mu.Unlock()
		if how == "hello" {
			s.llmHello(r.m, acp.HelloParams{LLM: sdkacp.ServeOwn})
		} else if err := s.llmHeld(r.m); !errors.Is(err, ErrForbidden) {
			t.Fatalf("a session whose host answered nothing is held: %v", err)
		}
		select {
		case <-st.gone:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the session runs on", how)
		}
		if st.llmServes() != "" || len(f.liveKeys()) != 0 {
			t.Errorf("%s: serves %q, live keys %v", how, st.llmServes(), f.liveKeys())
		}
		if !strings.Contains(string(st.text), "LLM connector only") || !strings.Contains(string(st.text), "The session was ended") {
			t.Errorf("%s: the reason is not said: %q", how, st.text)
		}
	}
}

// The tail xbind logs of a dead terminal: a key the cut would split is not
// left half in it.
func TestLLMTailMasksBeforeTheCut(t *testing.T) {
	key := "xbl_" + strings.Repeat("Ab0-_", 8)
	h := termwire.NewHub(1 << 16)
	h.Output([]byte("first line\nXBIN_LLM_KEY=" + key + "\n" + strings.Repeat("x", 60) + "\n"))
	for n := 61; n < 61+len(key)+4; n++ { // every cut through the key
		got := llmTail(h, n)
		for i := 6; i <= len(key); i++ {
			if strings.Contains(got, key[len(key)-i:]) {
				t.Fatalf("cut at %d: %d characters of the key are in the tail %q", n, i, got)
			}
		}
	}
	if got := llmTail(h, 4096); strings.Contains(got, key) || !strings.Contains(got, "XBIN_LLM_KEY=xbl_[redacted]") || !strings.Contains(got, "first line") {
		t.Errorf("the whole tail: %q", got)
	}
}

// A plain terminal's variables: XBIN_LLM_* by the terminals switch, the
// OPENAI_* pair only by its own; never for a person whose terminals hold no
// tile API token, a sign-in's shell, or an agent session's sandbox.
func TestTerminalLLMEnv(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	f := newFakeLLM()
	m.LLM = f
	env := []string{"PATH=/bin", "XBIN_URL=http://10.0.2.2:8080"}
	arm := func(o openOpts) []string {
		m.llmArm(&o, "apps/x", nil, func() {})
		return o.llmShellEnv(env)
	}
	got := arm(openOpts{})
	if len(got) != 2 || got[0] != "XBIN_LLM_URL=http://10.0.2.2:8080/api/xbin/llm/v1" || !strings.HasPrefix(got[1], "XBIN_LLM_KEY=xbl_") {
		t.Fatalf("a terminal's variables: %v", got)
	}
	if f.live[strings.TrimPrefix(got[1], "XBIN_LLM_KEY=")] != "owner|apps/x|terminal" {
		t.Errorf("the key's grant: %v", f.live)
	}
	f.offer.OpenAIEnv = true
	got = arm(openOpts{userID: "bob"})
	if len(got) != 4 || got[2] != "OPENAI_BASE_URL=http://10.0.2.2:8080/api/xbin/llm/v1" || !strings.HasPrefix(got[3], "OPENAI_API_KEY=xbl_") {
		t.Fatalf("with the OPENAI_* switch: %v", got)
	}
	n := f.minted()
	f.offer.Terminals = false
	if got := arm(openOpts{}); got != nil {
		t.Errorf("terminals switched off: %v", got)
	}
	f.offer.Terminals = true
	noGrant := openOpts{}
	noGrant.noLLM = true
	if got := arm(noGrant); got != nil {
		t.Errorf("a person without the terminal API grant: %v", got)
	}
	if got := arm(openOpts{purpose: PurposeSignin}); got != nil {
		t.Errorf("a sign-in's shell: %v", got)
	}
	if f.minted() != n {
		t.Errorf("keys minted for terminals that get none: %d", f.minted()-n)
	}
	// an agent session's sandbox env never carries the key (the spawn frame does)
	fake, _ := agent.Lookup("fake")
	o := openOpts{kind: KindAgent}
	m.llmArm(&o, "apps/x", &fake, func() {})
	if o.llm == nil || o.llm.token == "" || o.llmShellEnv(env) != nil {
		t.Errorf("an agent session's sandbox env: %v", o.llmShellEnv(env))
	}
	// a terminal's open never asks for the model list
	if slices.Contains(f.kinds[:len(f.kinds)-1], KindAgent) {
		t.Errorf("kinds asked: %v", f.kinds)
	}
	// no XBIN_URL, no address to name
	o = openOpts{}
	m.llmArm(&o, "apps/x", nil, func() {})
	if got := o.llmShellEnv([]string{"PATH=/bin"}); got != nil {
		t.Errorf("without XBIN_URL: %v", got)
	}
}

// A real terminal: the variables are in the shell's environment and the key
// ends with the session.
func TestTerminalLLMSession(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root, func() []string { return []string{"XBIN_URL=http://127.0.0.1:1"} })
	f := newFakeLLM()
	m.LLM = f
	o := m.openOptsFor(owner, "apps/x", "apps/x", "", "", true)
	s, err := m.create(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pty.Write([]byte("echo \"K=[$XBIN_LLM_KEY] U=[$XBIN_LLM_URL] O=[$OPENAI_API_KEY]\"\n")); err != nil {
		t.Fatal(err)
	}
	live := f.liveKeys()
	if len(live) != 1 {
		t.Fatalf("live keys: %v", live)
	}
	want := "K=[" + live[0] + "] U=[http://127.0.0.1:1/api/xbin/llm/v1] O=[]"
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(string(s.hub.Tail(4096)), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the shell's environment: %q, want %q", s.hub.Tail(4096), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.Kill(s.ID)
	for len(f.liveKeys()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the terminal's key outlived it: %v", f.liveKeys())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// What the launcher is told, per provider, and how a session's look at the
// home sharpens it.
func TestAgentLLMProviders(t *testing.T) {
	r, f := llmRig(t)
	provs := agent.Providers()
	use := func(llm map[string]*ProviderLLM) map[string]string {
		out := map[string]string{}
		for id, row := range llm {
			out[id] = row.Use
		}
		return out
	}
	llm := r.m.AgentLLM("", "owner", "apps/x", provs)
	got := use(llm)
	if got["opencode"] != sdkacp.UseOwnFirst || got["codex"] != sdkacp.UseOwnFirst || got["fake"] != sdkacp.UseOwnFirst ||
		got["claude"] != sdkacp.ServeOwn || got["gemini"] != sdkacp.ServeOwn {
		t.Fatalf("before any session looked: %v", got)
	}
	if !llm["opencode"].Connector || llm["claude"].Connector || llm["gemini"].Connector ||
		!strings.Contains(llm["claude"].Note, "not available through the LLM connector yet") || llm["gemini"].Note != "" || llm["opencode"].SignedIn != nil {
		t.Errorf("rows: claude %+v gemini %+v opencode %+v", llm["claude"], llm["gemini"], llm["opencode"])
	}
	// a session's host looks at the home: nobody is signed in to anything
	info := r.openLLM(t, owner, AgentOpen{})
	r.endSession(t, info.ID)
	llm = r.m.AgentLLM("", "owner", "apps/x", provs)
	got = use(llm)
	if got["opencode"] != sdkacp.ServeConnector || got["fake"] != sdkacp.ServeConnector || got["claude"] != "signin" || got["gemini"] != "signin" {
		t.Fatalf("after a session looked (no sign-ins): %v", got)
	}
	if s := llm["fake"].SignedIn; s == nil || *s {
		t.Errorf("fake signedIn: %v", s)
	}
	// signed in to the fake: the next session's look says so
	signIn(t, r.root, "owner")
	info = r.openLLM(t, owner, AgentOpen{})
	r.endSession(t, info.ID)
	if got = use(r.m.AgentLLM("", "owner", "apps/x", provs)); got["fake"] != sdkacp.ServeOwn || got["opencode"] != sdkacp.ServeConnector {
		t.Fatalf("after signing in to the fake: %v", got)
	}
	// another person's home was never looked at
	if got = use(r.m.AgentLLM("bob", "bob", "apps/x", provs)); got["fake"] != sdkacp.UseOwnFirst {
		t.Errorf("bob: %v", got)
	}
	// the workspace's mode is connector-first; bob saved codex as his own
	r.m.Credentials = policy("connector-first", "", map[string]string{"bob/codex": sdkacp.UseOwn})
	if got = use(r.m.AgentLLM("bob", "bob", "apps/x", provs)); got["fake"] != sdkacp.ServeConnector || got["codex"] != sdkacp.ServeOwn || got["claude"] != sdkacp.ServeOwn {
		t.Errorf("connector-first, bob's codex saved as his own: %v", got)
	}
	// the listing never waits on a provider's model list
	if slices.Contains(f.kinds[len(f.kinds)-1:], KindAgent) {
		t.Errorf("the listing asked for models: %v", f.kinds)
	}
}

// What the connector refuses a session's CLI is said in the session — a
// CLI retrying a 429 says nothing — once per kind while it repeats.
func TestAgentLLMRefusalSaid(t *testing.T) {
	r, f := llmRig(t)
	info := r.openLLM(t, owner, AgentOpen{})
	f.mu.Lock()
	refused := f.refused
	f.mu.Unlock()
	if refused == nil {
		t.Fatal("the session's key is not watched")
	}
	notice := func(want string) {
		t.Helper()
		e := r.until(t, func(e SessionEvent) bool {
			text, _ := edata(e.Event)["text"].(string)
			return e.ID == info.ID && e.Type == EvNotice && strings.Contains(text, "refused a model call")
		})
		if text, _ := edata(e.Event)["text"].(string); !strings.Contains(text, want) {
			t.Fatalf("notice %q, want %q in it", text, want)
		}
	}
	refused(429, "xbin_llm_quota_exceeded", "your daily token quota is reached; it resets at 00:00 UTC")
	notice("(429 xbin_llm_quota_exceeded): your daily token quota is reached")
	refused(429, "xbin_llm_quota_exceeded", "again") // the CLI's retry: not said twice
	refused(429, "xbin_llm_busy", "too many calls at once")
	notice("(429 xbin_llm_busy): too many calls at once")
	refused(502, "", "")
	notice("(502): no reason given")
	r.endSession(t, info.ID)
	// a terminal's key is not watched: its tools show the answer themselves
	m := NewManager(t.TempDir(), nil)
	m.LLM = f
	o := openOpts{}
	m.llmArm(&o, "apps/x", nil, func() {})
	if f.refused != nil {
		t.Error("a terminal's key is watched")
	}
}

// What xbind keeps of a session's output — the agent's stderr in the
// session's text log, a dead session's last lines in xbind's own log —
// never holds an LLM key: a hook or a tool that printed the environment.
func TestLLMKeyMaskedInLogs(t *testing.T) {
	key := "xbl_" + strings.Repeat("Ab0-_", 8)
	got := maskLLMKeys("hook: XBIN_LLM_KEY=" + key + "\nBearer " + key + " and xbl_short stays\n")
	if strings.Contains(got, key) || !strings.Contains(got, "XBIN_LLM_KEY=xbl_[redacted]\n") || !strings.Contains(got, "xbl_short stays") {
		t.Fatalf("masked: %q", got)
	}
	st := &agentState{}
	st.logf("hook: XBIN_LLM_KEY=" + key + "\n")
	if s := string(st.text); strings.Contains(s, key) || !strings.Contains(s, "xbl_[redacted]") {
		t.Fatalf("the session's text log: %q", s)
	}
}
