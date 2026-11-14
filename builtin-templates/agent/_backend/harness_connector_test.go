package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// connectorRig stands in for xbind's LLM connector and its key routes: the
// bound provider (GET /preferred), the address a sandbox reaches it at (POST
// /v1/chat/completions, for the minted key alone), and the SDK's key calls.
type connectorRig struct {
	srv *httptest.Server

	mu      sync.Mutex
	minted  []xbin.LLMKeyOpts
	revoked []string
	calls   []string // "<model>" per chat call that carried the live key
	denied  int      // chat calls without it
	token   string
}

const connectorTestKey = "xbl_TESTKEYTESTKEYTESTKEYTESTKEYTESTKEYTEST"

func newConnectorRig(t *testing.T) *connectorRig {
	t.Helper()
	c := &connectorRig{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/preferred":
			_, _ = w.Write([]byte(`{"use":"harness","model":"up/m1"}`))
		case r.Method == "POST" && r.URL.Path == "/v1/chat/completions":
			var body struct{ Model string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.mu.Lock()
			ok := c.token != "" && r.Header.Get("Authorization") == "Bearer "+c.token
			if ok {
				c.calls = append(c.calls, body.Model)
			} else {
				c.denied++
			}
			c.mu.Unlock()
			if !ok {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":{"message":"this LLM key is not valid","code":"invalid_api_key"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"the connector answered"}}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(c.srv.Close)
	t.Setenv("XBIN_IFACE_LLM", `[{"provider":"xbin:llm","url":"`+c.srv.URL+`","service":"openai"}]`)
	oldClient, oldMint, oldRevoke := llmClient, llmKeyFor, revokeLLMKeys
	llmClient = func() *http.Client { return http.DefaultClient }
	llmKeyFor = func(_ context.Context, o xbin.LLMKeyOpts) (xbin.LLMKey, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.minted = append(c.minted, o)
		c.token = connectorTestKey
		return xbin.LLMKey{ID: "lk_test", Token: connectorTestKey, User: "alice", Component: "apps/agent"}, nil
	}
	revokeLLMKeys = func(_ context.Context, label string) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.revoked = append(c.revoked, label)
		return nil
	}
	t.Cleanup(func() { llmClient, llmKeyFor, revokeLLMKeys = oldClient, oldMint, oldRevoke })
	return c
}

func (c *connectorRig) mints() []xbin.LLMKeyOpts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.minted)
}

func (c *connectorRig) revokes(label string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, l := range c.revoked {
		if l == label {
			n++
		}
	}
	return n
}

// covers D226 — a coding agent with no sign-in starts on the workspace's
// LLM connector: in a sandbox created with the connector asked for (the
// manager offers llm, this agent's slot is bound to xbin:llm) the adapter
// gets the connector's address and a key minted for the run, calls it, and
// says so; the key is masked wherever it shows and ended by the run's label
// when the adapter goes. A sign-in in the sandbox keeps its priority, a
// sandbox without the connector starts as before, and a manager that
// doesn't offer llm is never sent the member.
func TestHarnessOnConnector(t *testing.T) {
	_ = memVault(t)
	ag, h, plain, m, _, _ := harnessPartitionG(t)
	c := newConnectorRig(t)

	// the fixture's sandbox was made before the manager offered llm: no
	// connector there, and the agent starts as it always did
	if plain.llmURL() != "" {
		t.Fatalf("the fixture's sandbox has a connector: %+v", plain.LLM)
	}
	run := askIn(t, h, plain, "llm-call hi")
	hAnswered(t, ag, run.ID, "llm: no connector")
	if n := len(c.mints()); n != 0 {
		t.Fatalf("%d keys minted for a sandbox without the connector", n)
	}

	// a manager that doesn't offer llm is never sent the member (the fake
	// refuses one it doesn't know, as a manager from before it may)
	forgetHellos()
	if b := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "old", Egress: "internet"}); b.llmURL() != "" {
		t.Fatalf("a manager without llm gave a connector: %+v", b.LLM)
	}

	// the manager offers it: a new sandbox asks, and its row names the address
	m.LLM = c.srv.URL + "/v1"
	forgetHellos()
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "coder", Egress: "internet"})
	if box.llmURL() != c.srv.URL+"/v1" {
		t.Fatalf("a sandbox created at a manager that offers llm: %+v", box.LLM)
	}
	run = askIn(t, h, box, "llm-call hi")
	hAnswered(t, ag, run.ID, "llm: the connector answered")
	mints := c.mints()
	if len(mints) != 1 || mints[0].Label != connectorLabel(run.ID) || mints[0].Sandbox != box.ID || mints[0].User != "" {
		t.Fatalf("the key asked for: %+v", mints)
	}
	if c.revokes(connectorLabel(run.ID)) != 1 {
		t.Errorf("an earlier generation's keys weren't ended before the mint: %v", c.revoked)
	}
	c.mu.Lock()
	calls, denied := slices.Clone(c.calls), c.denied
	c.mu.Unlock()
	if len(calls) != 1 || calls[0] != "up/m1" || denied != 0 {
		t.Fatalf("the adapter's calls: %v (denied %d): the harness model and the run's key", calls, denied)
	}
	if notes := notesOf(t, ag.db, run.ID); !strings.Contains(notes, "runs on this workspace's LLM connector") {
		t.Errorf("the conversation doesn't say what the agent runs on: %s", notes)
	}

	// the key, printed by a tool: masked in what the page reads
	tell(t, h, run, "printenv XBIN_LLM_KEY")
	hAnswered(t, ag, run.ID, "printenv: "+redactMark)
	for _, p := range []string{fmt.Sprintf("/runs/%d", run.ID), fmt.Sprintf("/runs/%d/view", run.ID), fmt.Sprintf("/runs/%d/harness", run.ID)} {
		if code, body := aliceCall(t, h, "GET", p, nil); code != 200 || strings.Contains(body, connectorTestKey) {
			t.Errorf("GET %s (%d) shows the key", p, code)
		}
	}
	if hs, _ := ag.db.harnessSession(run.ID); hs == nil || strings.Contains(mustJSON(hs), connectorTestKey) {
		t.Errorf("the session row holds the key: %+v", hs)
	}

	// signed in in the sandbox on its own: that sign-in keeps its priority
	creds := filepath.Join(box.Home, ".fakeacp", "credentials")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("fake-login\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	own := askIn(t, h, box, "llm-call hi")
	hAnswered(t, ag, own.ID, "llm: no connector")
	if n := len(c.mints()); n != 1 {
		t.Fatalf("%d keys minted in all: one for the sandbox's own sign-in too", n)
	}

	// the adapter is stopped: the run's key is ended by its label
	r, err := ag.db.getRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ag.eng.stopHarnessNow(context.Background(), r, "stopped by the test")
	hwait(t, "the run's key ended", func() bool { return c.revokes(connectorLabel(run.ID)) >= 2 })
}

// The redactor masks an LLM key by its shape: a process that never knew the
// key (a successor after a restart) keeps it out of what it shows too.
func TestRedactLLMKeyShape(t *testing.T) {
	in := "env: XBIN_LLM_KEY=" + connectorTestKey + " and Bearer " + connectorTestKey + "\n"
	out := newRedactor().text(in)
	if strings.Contains(out, "xbl_TEST") || len(out) != len(in) || !strings.Contains(out, redactMark) {
		t.Fatalf("masked: %q", out)
	}
	if s := newRedactor().text("xbl_short and a word like xblock\n"); s != "xbl_short and a word like xblock\n" {
		t.Fatalf("ordinary text was masked: %q", s)
	}
}
