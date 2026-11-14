package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/term"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// offerLLM is a connector that offers what it holds (ok false: nothing).
type offerLLM struct {
	offer term.LLMOffer
	ok    bool
}

func (o offerLLM) Offer(context.Context, string, string, string) (term.LLMOffer, bool) {
	return o.offer, o.ok
}
func (offerLLM) Mint(string, string, string, func(int, string, string)) (string, func(), error) {
	return "xbl_t", func() {}, nil
}

// covers D226 — GET /agent/providers: exactly the catalog while agent
// sessions get nothing of a connector (the golden), and each row with its
// `llm` once they do; POST /term/sessions refuses a choice of the connector
// it can't honour, and an unknown word.
func TestAgentProvidersLLM(t *testing.T) {
	h, s := termServer(t)
	t.Setenv(agent.FakeEnv, "")
	alice := s.Auth.NewSession("alice", "")
	get := func(q string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", "/api/xbin/agent/providers"+q, "", alice))
		if w.Code != 200 {
			t.Fatalf("providers: %d %s", w.Code, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}
	offer := term.LLMOffer{Agents: true, Terminals: true, Wires: []string{sdkacp.WireChat, sdkacp.WireResponses}, Model: "up/m1"}
	for name, llm := range map[string]term.LLMConnector{"not configured": offerLLM{ok: false}, "agents off": offerLLM{term.LLMOffer{Terminals: true}, true}} {
		s.Term.LLM = llm
		if got := get(""); got != wantProviders {
			t.Errorf("%s: the rows changed\n got %s", name, got)
		}
	}
	s.Term.LLM = offerLLM{offer, true}
	var rows []struct {
		ID  string
		LLM *term.ProviderLLM
	}
	body := get("?cwd=apps/x")
	if err := json.Unmarshal([]byte(body), &rows); err != nil || len(rows) != 4 {
		t.Fatalf("rows: %v %s", err, body)
	}
	want := map[string]string{"claude": "own", "codex": "own-first", "gemini": "own", "opencode": "own-first"}
	for _, r := range rows {
		if r.LLM == nil || r.LLM.Use != want[r.ID] || r.LLM.Connector != (want[r.ID] == "own-first") || r.LLM.SignedIn != nil {
			t.Errorf("%s: %+v", r.ID, r.LLM)
		}
	}
	// the catalog's own members are untouched beside it
	if !strings.HasPrefix(body, `[{"id":"claude","name":"Claude Code","login":"claude auth login","modes":[`) || !strings.Contains(body, `"llm":{"use":"own-first","connector":true}`) {
		t.Errorf("the rows' shape: %s", body)
	}

	s.Term.BxPath = "/bin/true" // the choice is judged before anything is spawned
	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("POST", "/api/xbin/term/sessions", body, alice))
		return w.Code, w.Body.String()
	}
	if c, b := post(`{"cwd":"apps/x","kind":"agent","provider":"gemini","llm":"connector"}`); c != 409 || !strings.Contains(b, "own sign-in") {
		t.Errorf("gemini on the connector: %d %s", c, b)
	}
	if c, b := post(`{"cwd":"apps/x","kind":"agent","provider":"opencode","llm":"sometimes"}`); c != 400 {
		t.Errorf("an unknown llm choice: %d %s", c, b)
	}
}
