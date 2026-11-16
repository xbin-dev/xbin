package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// Test credentials: obvious fakes in the shapes the catalog knows.
const (
	fakeSigninToken = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-route-0001"
	fakeSigninKey   = "sk-ant-api03-FAKEFAKEFAKEFAKE-route-key1"
)

const fakeSigninURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=test&state=s"

// scriptedMint is a Runner whose "CLI" is this test: the page and the
// prompt, then a code without '#' is refused as malformed, good#… prints a
// fake token, anything else is the CLI's refusal.
type scriptedMint struct{ started, closed int }

type mintExec struct {
	m      *scriptedMint
	mu     sync.Mutex
	out    []string
	ended  bool
	wake   chan struct{}
	closed bool
}

func (m *scriptedMint) Start(acp.Signin, time.Duration) (acp.GuidedExec, error) {
	m.started++
	x := &mintExec{m: m, wake: make(chan struct{}, 16)}
	x.say("\x1b]8;id=t;" + fakeSigninURL + "\x07" + fakeSigninURL + "\x1b]8;;\x07\r\n\r\nPaste code here if prompted >\r\n")
	return x, nil
}

func (x *mintExec) say(s string) {
	x.mu.Lock()
	x.out = append(x.out, s)
	x.mu.Unlock()
	x.wake <- struct{}{}
}

func (x *mintExec) Read(ctx context.Context) ([]byte, bool, *int, error) {
	for {
		x.mu.Lock()
		if len(x.out) > 0 {
			s := x.out[0]
			x.out = x.out[1:]
			x.mu.Unlock()
			return []byte(s), true, nil, nil
		}
		if x.ended || x.closed {
			x.mu.Unlock()
			code := 1
			return nil, false, &code, nil
		}
		x.mu.Unlock()
		select {
		case <-x.wake:
		case <-ctx.Done():
			return nil, true, nil, ctx.Err()
		}
	}
}

func (x *mintExec) Write(_ context.Context, p []byte) error {
	code := strings.TrimRight(string(p), "\r\n")
	switch {
	case !strings.Contains(code, "#"):
		x.say("Invalid code. Please make sure the full code was copied.\r\n")
	case strings.HasPrefix(code, "good"):
		x.say("Your OAuth token (valid for 1 year):\r\n\r\n" + fakeSigninToken + "\r\n")
	default:
		x.say("OAuth error: Request failed with status code 400\r\n")
		x.mu.Lock()
		x.ended = true
		x.mu.Unlock()
	}
	return nil
}

func (x *mintExec) Close() error {
	x.mu.Lock()
	if !x.closed {
		x.closed = true
		x.m.closed++
	}
	x.mu.Unlock()
	x.wake <- struct{}{}
	return nil
}

func signinsServer(t *testing.T) (http.Handler, *Server, *scriptedMint) {
	t.Helper()
	h, s := termServer(t)
	s.Signins = signins.New(signins.Options{Dir: filepath.Join(t.TempDir(), "signins"), AllowPlaintext: true,
		Person: func(id string) (int64, bool) {
			u, ok := s.Auth.Users.Get(id)
			if !ok || u.Disabled {
				return 0, false
			}
			return u.Created, true
		}})
	mint := &scriptedMint{}
	s.SigninFlows = signins.NewFlows(s.Signins, mint)
	t.Cleanup(s.SigninFlows.Close)
	return h, s, mint
}

// covers D230 — a person's saved sign-ins over the API: saved, listed,
// renamed, made default and removed by their own session only; no answer
// carries a secret; a terminal's token, the root token, view-as and
// another person never reach them.
func TestSigninsRoutes(t *testing.T) {
	h, s, _ := signinsServer(t)
	alice, bob := s.Auth.NewSession("alice", ""), s.Auth.NewSession("bob", "")
	var seen strings.Builder // every answer, to prove none carries a secret
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin/agent/signins"+path, body, sid))
		seen.WriteString(w.Body.String())
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	var view struct {
		Available bool
		Vault     string
		Admin     bool
		Policy    signins.Policy
		Notice    *signins.Notice
		Harnesses []struct {
			ID      string
			Guided  bool
			Keys    []struct{ Env, Kind string }
			Signins []signins.Signin
		}
	}
	list := func(sid string) {
		t.Helper()
		c, b := do(sid, "GET", "", "")
		view.Notice, view.Harnesses, view.Admin = nil, nil, false
		if c != 200 || json.Unmarshal([]byte(b), &view) != nil {
			t.Fatalf("list: %d %s", c, b)
		}
	}
	of := func(harness string) []signins.Signin {
		for _, hv := range view.Harnesses {
			if hv.ID == harness {
				return hv.Signins
			}
		}
		return nil
	}
	list(alice)
	if !view.Available || view.Vault != "plaintext" || !view.Policy.Enabled || !view.Admin || len(view.Harnesses) != 4 ||
		view.Harnesses[0].ID != "claude" || !view.Harnesses[0].Guided || view.Harnesses[1].Guided || len(of("claude")) != 0 {
		t.Fatalf("an empty list: %+v", view)
	}

	c, b := do(alice, "POST", "", `{"harness":"claude","secret":"`+fakeSigninToken+`","name":"Personal"}`)
	if c != 201 || !strings.Contains(b, `"kind":"setup-token"`) || !strings.Contains(b, `"env":"CLAUDE_CODE_OAUTH_TOKEN"`) || !strings.Contains(b, `"default":true`) {
		t.Fatalf("save: %d %s", c, b)
	}
	if c, b := do(alice, "POST", "", `{"harness":"claude","secret":"`+fakeSigninKey+`"}`); c != 201 || !strings.Contains(b, `"name":"Claude Code"`) || !strings.Contains(b, `"default":false`) {
		t.Fatalf("a second: %d %s", c, b)
	}
	list(alice)
	mine := of("claude")
	if len(mine) != 2 {
		t.Fatalf("listed: %+v", mine)
	}
	key, tok := mine[0], mine[1]
	if tok.Name != "Personal" {
		key, tok = tok, key
	}
	list(bob)
	if len(of("claude")) != 0 || view.Admin {
		t.Fatalf("another person's list: %+v", view)
	}
	// another person can't touch one by its id
	for _, try := range [][2]string{{"PUT", `{"default":true}`}, {"DELETE", ""}} {
		if c, _ := do(bob, try[0], "/"+key.ID, try[1]); c != 404 {
			t.Errorf("bob %s alice's sign-in: %d", try[0], c)
		}
	}
	if c, b := do(alice, "PUT", "/"+key.ID, `{"name":"Work","default":true}`); c != 200 || !strings.Contains(b, `"name":"Work"`) || !strings.Contains(b, `"default":true`) {
		t.Fatalf("rename + default: %d %s", c, b)
	}
	for body, want := range map[string]int{
		`{"harness":"claude","secret":"has a space"}`:                       400,
		`{"harness":"nope","secret":"` + fakeSigninKey + `"}`:               400,
		`{"harness":"claude"}`:                                              400,
		`{"secret":"` + fakeSigninKey + `"}`:                                400,
		`{"harness":"claude","secret":"x","extra":1}`:                       400,
		`{"harness":"claude","secret":"` + strings.Repeat("k", 5000) + `"}`: 400,
	} {
		if c, b := do(alice, "POST", "", body); c != want {
			t.Errorf("POST %.60s: %d %s, want %d", body, c, b, want)
		}
	}
	if c, _ := do(alice, "PUT", "/"+key.ID, `{"default":false}`); c != 400 {
		t.Errorf("default false: %d", c)
	}

	// the use log, once a session took one
	if _, err := s.Signins.Use("alice", "claude", "", signins.UseFor{Tile: "apps/x", Session: "s1", Origin: "person"}); err != nil {
		t.Fatal(err)
	}
	if c, b := do(alice, "GET", "/uses", ""); c != 200 || !strings.Contains(b, `"tile":"apps/x"`) || !strings.Contains(b, `"name":"Work"`) {
		t.Fatalf("uses: %d %s", c, b)
	}
	if c, b := do(bob, "GET", "/uses", ""); c != 200 || strings.Contains(b, "apps/x") {
		t.Fatalf("bob's uses: %d %s", c, b)
	}

	if c, b := do(alice, "DELETE", "/"+key.ID, ""); c != 200 || !strings.Contains(b, `"ok":true`) {
		t.Fatalf("remove: %d %s", c, b)
	}
	list(alice)
	if mine := of("claude"); len(mine) != 1 || !mine[0].Default || mine[0].ID != tok.ID {
		t.Fatalf("after the removal: %+v", mine)
	}

	// credentials that name alice and are not her own session
	bearer := func(tok, method, path, body string) int {
		r := httptest.NewRequest(method, "/api/xbin/agent/signins"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		seen.WriteString(w.Body.String())
		return w.Code
	}
	for name, cred := range map[string]string{
		"a tile terminal's token":      s.Auth.MintTerminal("apps/x", "alice"),
		"a workspace terminal's token": s.Auth.MintTerminal("", "alice"),
		"the root token":               s.Auth.OwnerTokenValue(),
	} {
		for _, try := range [][3]string{{"GET", "", ""}, {"GET", "/uses", ""}, {"POST", "", `{"harness":"claude","secret":"` + fakeSigninKey + `"}`},
			{"PUT", "/" + tok.ID, `{"default":true}`}, {"DELETE", "/" + tok.ID, ""}, {"POST", "/guided", `{"harness":"claude"}`}} {
			if c := bearer(cred, try[0], try[1], try[2]); c != 403 {
				t.Errorf("%s, %s %s: %d", name, try[0], try[1], c)
			}
		}
	}
	// the handlers hold on their own too (were they reached past the route table)
	for name, p := range map[string]auth.Principal{
		"a frame":     {Component: "apps/x", Via: "frame", UserID: "alice"},
		"view-as":     {UserID: "alice", Impersonator: "dave"},
		"nobody":      {},
		"a ws pty":    {Via: "terminal", UserID: "alice"},
		"their agent": {UserID: "alice", Via: "delegate", Agent: "xbin/wsagent"},
	} {
		for hn, fn := range map[string]http.HandlerFunc{"get": s.apiSigninsGet, "post": s.apiSigninsPost, "delete": s.apiSigninsDelete, "guided": s.apiSigninsGuidedStart} {
			r := httptest.NewRequest("POST", "/agent/signins", strings.NewReader(`{"harness":"claude","secret":"`+fakeSigninKey+`"}`))
			w := httptest.NewRecorder()
			fn(w, r.WithContext(auth.WithPrincipal(context.Background(), p)))
			if w.Code != 403 {
				t.Errorf("%s reached %s: %d %s", name, hn, w.Code, w.Body.String())
			}
		}
	}

	if out := seen.String(); strings.Contains(out, "FAKEFAKE") {
		t.Fatalf("an answer carried a secret:\n%s", out)
	}
}

// covers D230 — the guided sign-in over the API: the page, a malformed
// code, the credential kept and never answered; the flow is its starter's.
func TestSigninsGuidedRoutes(t *testing.T) {
	h, s, mint := signinsServer(t)
	alice, bob := s.Auth.NewSession("alice", ""), s.Auth.NewSession("bob", "")
	var seen strings.Builder
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin/agent/signins"+path, body, sid))
		seen.WriteString(w.Body.String())
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	c, b := do(alice, "POST", "/guided", `{"harness":"claude","name":"Work","default":true}`)
	var started struct{ Flow signins.Flow }
	if c != 202 || json.Unmarshal([]byte(b), &started) != nil || started.Flow.URL != fakeSigninURL || !started.Flow.Paste {
		t.Fatalf("start: %d %s", c, b)
	}
	id := started.Flow.ID
	if c, b := do(alice, "GET", "", ""); c != 200 || !strings.Contains(b, `"flows":[{"id":"`+id) {
		t.Fatalf("the list names the flow: %d %s", c, b)
	}
	if c, b := do(bob, "GET", "", ""); c != 200 || strings.Contains(b, id) {
		t.Fatalf("bob sees it: %d %s", c, b)
	}
	if c, _ := do(bob, "POST", "/guided/"+id, `{"code":"good#s"}`); c != 404 {
		t.Fatalf("bob's code: %d", c)
	}
	if c, _ := do(bob, "DELETE", "/guided/"+id, ""); c != 404 {
		t.Fatalf("bob's cancel: %d", c)
	}
	if c, b := do(alice, "POST", "/guided/"+id, `{"code":"half"}`); c != 409 || !strings.Contains(b, "whole code") {
		t.Fatalf("a malformed code: %d %s", c, b)
	}
	c, b = do(alice, "POST", "/guided/"+id, `{"code":" good#s "}`)
	if c != 201 || !strings.Contains(b, `"name":"Work"`) || !strings.Contains(b, `"kind":"setup-token"`) || !strings.Contains(b, `"default":true`) {
		t.Fatalf("the code: %d %s", c, b)
	}
	if mint.closed != 1 {
		t.Fatalf("the flow's command was closed %d times", mint.closed)
	}
	if cred, err := s.Signins.Use("alice", "claude", "", signins.UseFor{}); err != nil || cred.Secret != fakeSigninToken {
		t.Fatalf("the kept credential: %v", err)
	}
	// a refusal ends the flow; codex has no guided sign-in
	_, b = do(bob, "POST", "/guided", `{"harness":"claude"}`)
	_ = json.Unmarshal([]byte(b), &started)
	if c, b := do(bob, "POST", "/guided/"+started.Flow.ID, `{"code":"refuse#s"}`); c != 502 || !strings.Contains(b, "OAuth error") {
		t.Fatalf("a refusal: %d %s", c, b)
	}
	if c, _ := do(bob, "POST", "/guided/"+started.Flow.ID, `{"code":"good#s"}`); c != 404 {
		t.Fatalf("a code after the end: %d", c)
	}
	if c, b := do(bob, "POST", "/guided", `{"harness":"codex"}`); c != 409 || !strings.Contains(b, "paste a key") {
		t.Fatalf("codex: %d %s", c, b)
	}
	// cancel
	_, b = do(bob, "POST", "/guided", `{"harness":"claude"}`)
	_ = json.Unmarshal([]byte(b), &started)
	if c, _ := do(bob, "DELETE", "/guided/"+started.Flow.ID, ""); c != 200 {
		t.Fatalf("cancel: %d", c)
	}
	if mint.started != 3 || mint.closed != 3 {
		t.Fatalf("started %d, closed %d", mint.started, mint.closed)
	}
	if out := seen.String(); strings.Contains(out, "FAKEFAKE") {
		t.Fatalf("an answer carried the credential:\n%s", out)
	}

	// where no sign-ins are kept (no sandboxes): every write says why
	s.SigninsAvail = func() SigninsAvailability { return SigninsAvailability{Why: "saved sign-ins need sandboxes"} }
	if c, b := do(alice, "GET", "", ""); c != 200 || !strings.Contains(b, `"available":false`) || !strings.Contains(b, "need sandboxes") {
		t.Fatalf("unavailable list: %d %s", c, b)
	}
	for _, try := range [][3]string{{"POST", "", `{"harness":"claude","secret":"` + fakeSigninKey + `"}`}, {"POST", "/guided", `{"harness":"claude"}`}} {
		if c, b := do(alice, try[0], try[1], try[2]); c != 409 || !strings.Contains(b, "need sandboxes") {
			t.Errorf("%s %s where none are kept: %d %s", try[0], try[1], c, b)
		}
	}
}

// covers D230 — admins see that people have sign-ins, never what; revoke
// one; set the workspace's switch. Nobody else does.
func TestSigninsAdminRoutes(t *testing.T) {
	h, s, _ := signinsServer(t)
	alice, bob := s.Auth.NewSession("alice", ""), s.Auth.NewSession("bob", "") // alice is the admin
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin/agent/signins"+path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	sg, _, err := s.Signins.Save("bob", "claude", "Bob's own label", fakeSigninToken, false)
	if err != nil {
		t.Fatal(err)
	}
	c, b := do(alice, "GET", "/people", "")
	if c != 200 || !strings.Contains(b, `"user":"bob"`) || !strings.Contains(b, `"total":1`) || !strings.Contains(b, sg.ID) ||
		strings.Contains(b, "own label") || strings.Contains(b, "FAKEFAKE") {
		t.Fatalf("people: %d %s", c, b)
	}
	for _, try := range [][3]string{{"GET", "/people", ""}, {"DELETE", "/people/alice/" + sg.ID, ""}, {"PUT", "/policy", `{"enabled":false}`}} {
		if c, _ := do(bob, try[0], try[1], try[2]); c != 403 {
			t.Errorf("a non-admin %s %s: %d", try[0], try[1], c)
		}
	}
	// view-as never writes
	for name, p := range map[string]auth.Principal{"view-as": {Owner: true, UserID: "bob", Impersonator: "owner"}, "an admin's agent": {UserID: "alice", Via: "delegate", Agent: "xbin/wsagent"}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("PUT", "/agent/signins/policy", strings.NewReader(`{"enabled":false}`))
		s.apiSigninsPolicyPut(w, r.WithContext(auth.WithPrincipal(context.Background(), p)))
		if w.Code != 403 {
			t.Errorf("%s set the switch: %d", name, w.Code)
		}
	}
	if c, b := do(alice, "DELETE", "/people/bob/"+sg.ID, ""); c != 200 {
		t.Fatalf("revoke: %d %s", c, b)
	}
	if c, _ := do(alice, "DELETE", "/people/bob/"+sg.ID, ""); c != 404 {
		t.Fatalf("revoke twice: %d", c)
	}
	if c, b := do(bob, "GET", "", ""); c != 200 || !strings.Contains(b, "removed by alice") || strings.Contains(b, sg.ID) {
		t.Fatalf("the person is told: %d %s", c, b)
	}
	if c, _ := do(bob, "DELETE", "/notice", ""); c != 200 {
		t.Fatal("dismiss")
	}
	if _, b := do(bob, "GET", "", ""); strings.Contains(b, "removed by") {
		t.Fatalf("the notice stayed: %s", b)
	}

	// the switch
	if c, b := do(alice, "PUT", "/policy", `{"enabled":false}`); c != 200 || !strings.Contains(b, `"enabled":false`) {
		t.Fatalf("off: %d %s", c, b)
	}
	if c, b := do(bob, "POST", "", `{"harness":"claude","secret":"`+fakeSigninKey+`"}`); c != 403 || !strings.Contains(b, "switched off") {
		t.Fatalf("saving while off: %d %s", c, b)
	}
	if c, _ := do(alice, "PUT", "/policy", `{}`); c != 400 {
		t.Errorf("an empty body: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(s.Signins.PolicyPath()), "policy.json")); !strings.Contains(string(b), "false") {
		t.Fatalf("the switch wasn't written: %s", b)
	}
}
