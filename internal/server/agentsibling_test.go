package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// agentBins: bx (the agent host) and the scripted ACP agent, built once for
// the tests here that open real agent sessions over HTTP.
var agentBins struct {
	once      sync.Once
	bx, fake  string
	buildErrs error
}

func realAgentBins(t *testing.T) (bx, fake string) {
	t.Helper()
	agentBins.once.Do(func() {
		dir, err := os.MkdirTemp("", "xbin-server-agent-*")
		if err != nil {
			agentBins.buildErrs = err
			return
		}
		repo, _ := filepath.Abs("../..")
		agentBins.bx, agentBins.fake = filepath.Join(dir, "bx"), filepath.Join(dir, "fakeacp")
		for _, b := range [][2]string{{agentBins.bx, "./cmd/bx"}, {agentBins.fake, "./hack/fakeacp"}} {
			build := exec.Command("go", "build", "-o", b[0], b[1])
			build.Dir = repo
			if out, err := build.CombinedOutput(); err != nil {
				agentBins.buildErrs = errors.New("build " + b[1] + ": " + string(out))
				return
			}
		}
	})
	if agentBins.buildErrs != nil {
		t.Fatal(agentBins.buildErrs)
	}
	return agentBins.bx, agentBins.fake
}

// recTokens is the daemon's terminal-token minter, recording what it mints
// (the sandbox's XBIN_TOKEN of each session, in order).
type recTokens struct {
	a      *auth.Auth
	mu     sync.Mutex
	minted []string
}

func (r *recTokens) MintTerminal(component, userID string) string {
	tok := r.a.MintTerminal(component, userID)
	r.mu.Lock()
	r.minted = append(r.minted, tok)
	r.mu.Unlock()
	return tok
}
func (r *recTokens) RevokeTerminal(tok string) { r.a.RevokeTerminal(tok) }

func (r *recTokens) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.minted[len(r.minted)-1]
}

// D97 per session wasn't enough (review): an agent's sandbox token opened
// a sibling — in the explicit auto-approve mode, with wider pickers — and
// the two answered each other's permission requests and restarted each
// other; two agents already open on a tile did it without opening
// anything. Now no agent session's own token opens an agent session or
// drives any (prompt, permissions, elicitations, options, restart, diff),
// while a shell's token on the tile still does (bx agent in a terminal).
func TestAgentTokenOpensAndDrivesNoAgent(t *testing.T) {
	bx, fake := realAgentBins(t)
	t.Setenv(agent.FakeEnv, fake)
	h, s := impServer(t)
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755))
	m := term.NewManager(root, nil)
	m.BxPath = bx
	rec := &recTokens{a: s.Auth}
	m.Tokens = rec
	closed := make(chan string, 64)
	m.OnChange = func(op, _, id, _ string) {
		if op == "close" {
			closed <- id
		}
	}
	s.Term, s.Hub = m, events.NewHub()

	alice := s.Auth.NewSession("alice", "")
	call := func(r *http.Request) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	bearer := func(method, path, body, tok string) (int, string) {
		r := httptest.NewRequest(method, "/api/xbin"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Content-Type", "application/json")
		return call(r)
	}
	open := func() (string, string) {
		t.Helper()
		c, b := call(withCookie("POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"fake","mode":"ask"}`, alice))
		var info struct{ ID string }
		if c != 200 || json.Unmarshal([]byte(b), &info) != nil || info.ID == "" {
			t.Fatalf("open: %d %s", c, b)
		}
		return info.ID, rec.last()
	}
	a, tokA := open()
	b, tokB := open()
	defer func() {
		for _, id := range []string{a, b} {
			m.Kill(id)
		}
		deadline := time.After(8 * time.Second)
		for n := 0; n < 2; {
			select {
			case <-closed:
				n++
			case <-deadline:
				t.Fatal("sessions did not close")
			}
		}
	}()

	// A's token opens no sibling (the escalation's first step)…
	if c, body := bearer("POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"fake","mode":"yolo","net":"internet"}`, tokA); c != http.StatusForbidden {
		t.Fatalf("an agent's token opened an agent session: %d %s", c, body)
	}
	// …and drives no agent session, its own or another's.
	for _, c := range []struct{ tok, id string }{{tokA, a}, {tokA, b}, {tokB, a}} {
		for _, r := range [][2]string{
			{"/prompt", `{"text":"hi"}`},
			{"/permissions/p1", `{"decision":"allow_once"}`},
			{"/elicitations/e1", `{"action":"accept","content":{}}`},
			{"/options", `{"id":"model","value":"x"}`},
			{"/restart", `{"net":"internet"}`},
		} {
			if code, body := bearer("POST", "/term/sessions/"+c.id+r[0], r[1], c.tok); code != http.StatusForbidden {
				t.Errorf("agent token → %s%s: %d %s", c.id, r[0], code, body)
			}
		}
		if code, _ := bearer("GET", "/term/sessions/"+c.id+"/diff?turn=1", "", c.tok); code != http.StatusForbidden {
			t.Errorf("agent token → %s diff: %d", c.id, code)
		}
	}
	// A shell's token on the tile (bx agent in a terminal) still drives.
	shell := s.Auth.MintTerminal("apps/x", "alice")
	if code, body := bearer("POST", "/term/sessions/"+a+"/prompt", `{"text":"hi"}`, shell); code != http.StatusOK {
		t.Fatalf("a shell's token prompting the agent: %d %s", code, body)
	}
}

// Who may answer a tile agent's permission request is xbind's rule, not a
// page's: the session's own person (or an admin) — every option of it,
// "Allow always" included — and nobody else, whatever a client draws
// (review finding 3: the dock's card answers through this route, and its
// buttons are no part of the rule).
func TestOnlyWhoMayDriveAnswersAPermission(t *testing.T) {
	bx, fake := realAgentBins(t)
	t.Setenv(agent.FakeEnv, fake)
	h, s := impServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := term.NewManager(root, nil)
	m.BxPath = bx
	m.Tokens = &recTokens{a: s.Auth}
	closed := make(chan string, 8)
	m.OnChange = func(op, _, id, _ string) {
		if op == "close" {
			closed <- id
		}
	}
	s.Term, s.Hub = m, events.NewHub()
	if _, err := s.Auth.Users.Upsert(users.User{ID: "mallory", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}}, "pw-mallory-1"); err != nil {
		t.Fatal(err)
	}
	alice, mallory := s.Auth.NewSession("alice", ""), s.Auth.NewSession("mallory", "")
	call := func(method, path, body, sid string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin"+path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	c, b := call("POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"fake","mode":"ask"}`, alice)
	var info struct{ ID string }
	if c != 200 || json.Unmarshal([]byte(b), &info) != nil || info.ID == "" {
		t.Fatalf("open: %d %s", c, b)
	}
	defer func() { // the session's teardown writes under root: the test ends when it has (the temp dir is removed after)
		m.Kill(info.ID)
		select {
		case <-closed:
		case <-time.After(8 * time.Second):
			t.Error("the session did not close")
		}
	}()
	if c, b := call("POST", "/term/sessions/"+info.ID+"/prompt", `{"text":"perm"}`, alice); c != 200 {
		t.Fatalf("prompt: %d %s", c, b)
	}
	pid := ""
	for i := 0; i < 200 && pid == ""; i++ {
		_, b := call("GET", "/term/sessions/"+info.ID, "", alice)
		var d struct {
			Permissions []struct {
				PID     string `json:"pid"`
				Options []struct{ OptionID, Kind string }
			}
		}
		if json.Unmarshal([]byte(b), &d) == nil && len(d.Permissions) == 1 {
			pid = d.Permissions[0].PID
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if pid == "" {
		t.Fatal("the agent's permission request never came")
	}
	// another person, with terminal access to the same tile: no reading the request, no option of it
	if c, b := call("GET", "/term/sessions/"+info.ID, "", mallory); c != http.StatusForbidden {
		t.Fatalf("another person reads the session's pending request: %d %s", c, b)
	}
	for _, body := range []string{`{"optionId":"always"}`, `{"optionId":"once"}`, `{"decision":"allow_always"}`, `{"optionId":"no"}`} {
		if c, b := call("POST", "/term/sessions/"+info.ID+"/permissions/"+pid, body, mallory); c != http.StatusForbidden {
			t.Fatalf("another person answers %s: %d %s", body, c, b)
		}
	}
	// its own person does — "Allow always" too
	if c, b := call("POST", "/term/sessions/"+info.ID+"/permissions/"+pid, `{"optionId":"always"}`, alice); c != 200 {
		t.Fatalf("alice answers her session's request: %d %s", c, b)
	}
}
