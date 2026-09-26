package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// The agent routes' gates over the term server (no bx binary: creation
// stops at 503 after every gate has passed). The session itself is
// exercised in internal/term with the scripted agent.
func TestAgentRoutesGates(t *testing.T) {
	h, s := termServer(t)
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin"+path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if c, b := do(bob, "GET", "/agent/providers", ""); c != 200 || !strings.Contains(b, `"id":"claude"`) || !strings.Contains(b, `"explicit":true`) {
		t.Fatalf("providers: %d %s", c, b)
	}
	// kind is required; a user without terminal level is refused; an admin
	// passes the gates and stops at the missing bx (503)
	if c, _ := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","provider":"claude"}`); c != 400 {
		t.Fatalf("no kind: %d", c)
	}
	if c, _ := do(bob, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude"}`); c != 403 {
		t.Fatalf("user: %d", c)
	}
	if c, b := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"nope"}`); c != 400 || !strings.Contains(b, "unknown provider") {
		t.Fatalf("provider: %d %s", c, b)
	}
	if c, b := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"bypassPermissions"}`); c != 503 || !strings.Contains(b, "bx binary") {
		t.Fatalf("admin, no bx: %d %s", c, b)
	}
	// per-session routes: unknown ids are 404 for everyone
	for _, r := range [][3]string{{"GET", "/term/sessions/nope", ""}, {"GET", "/term/sessions/nope/events", ""}, {"GET", "/term/sessions/nope/log", ""},
		{"POST", "/term/sessions/nope/prompt", `{"text":"hi"}`}, {"POST", "/term/sessions/nope/cancel", ""},
		{"POST", "/term/sessions/nope/permissions/p1", `{"decision":"allow_once"}`}, {"POST", "/term/sessions/nope/options", `{"id":"model","value":"x"}`},
		{"POST", "/term/sessions/nope/elicitations/e1", `{"action":"accept","content":{}}`}, {"GET", "/term/sessions/nope/diff?turn=1", ""},
		{"DELETE", "/term/sessions/nope", ""}} {
		if c, b := do(alice, r[0], r[1], r[2]); c != 404 {
			t.Fatalf("%s %s: %d %s", r[0], r[1], c, b)
		}
	}
	// the driving routes are the data plane for the audit log; create/end are not
	if auditable("POST", "/term/sessions/a1/prompt") || auditable("POST", "/term/sessions/a1/cancel") || auditable("POST", "/term/sessions/a1/permissions/p1") || auditable("POST", "/term/sessions/a1/options") || auditable("POST", "/term/sessions/a1/elicitations/e1") {
		t.Fatal("driving an agent must not be audited per call")
	}
	if !auditable("POST", "/term/sessions") || !auditable("DELETE", "/term/sessions/a1") {
		t.Fatal("creating/ending a session is audited")
	}
}

func TestSessionEventFilter(t *testing.T) {
	ev := events.Event{Type: "session", Topic: "session.a1", Component: "apps/x",
		Data: term.SessionEvent{Event: agent.Event{Seq: 1, Type: "status"}, User: "alice", ID: "a1"}}
	alice := auth.Principal{UserID: "alice", Via: "session", User: &users.User{ID: "alice", Role: "user"}}
	bob := auth.Principal{UserID: "bob", Via: "session", User: &users.User{ID: "bob", Role: "user"}}
	if !termEventFor(alice, ev) || termEventFor(bob, ev) || !termEventFor(auth.Principal{Owner: true}, ev) {
		t.Fatal("session events go to the owner and admins")
	}
	// a terminal token of the same user (bx agent attach inside a shell) too
	tok := auth.Principal{Component: "apps/x", UserID: "alice", Via: "terminal", User: alice.User}
	if !termEventFor(tok, ev) {
		t.Fatal("the user's terminal token is the user")
	}
	// the SessionEvent flattens on the wire
	s := &Server{Hub: events.NewHub()}
	ch, cancel := s.Hub.Subscribe(func(events.Event) bool { return true })
	defer cancel()
	s.SessionEvent("apps/x", term.SessionEvent{Event: agent.Event{Seq: 7, Type: "turn.end"}, User: "alice", ID: "a1"})
	got := <-ch
	if got.Type != "session" || got.Topic != "session.a1" || got.Data.(term.SessionEvent).Seq != 7 {
		t.Fatalf("published: %+v", got)
	}
}

// The past-session routes (term/history.go) over the term server: a seeded
// entry lists for its owner only (terminal level), reads back in the /events
// shape, resumes (the create path resolves the provider from it and stops at
// the missing bx) or refuses a non-loadable one, and deletes.
func TestAgentHistoryRoutes(t *testing.T) {
	h, s := termServer(t)
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin"+path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	seed := func(id string, loadable bool) {
		dir := filepath.Join(s.Term.Root, "data", "agent-history", "alice", util.CompKey("apps/x"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f := fmt.Sprintf(`{"meta":{"id":%q,"cwd":"apps/x","provider":"claude","mode":"plan","name":"old","created":"2026-01-01T00:00:00Z","ended":"2026-01-01T00:01:00Z","turns":2,"preview":"fix the tests","acpSessionId":"c-123","loadable":%v},"events":[{"seq":1,"ts":1,"type":"status","data":{"status":"idle"}}]}`, id, loadable)
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seed("h1", true)
	seed("h2", false)
	if c, b := do(alice, "GET", "/agent/history?cwd=apps/x", ""); c != 200 || !strings.Contains(b, `"id":"h1"`) || !strings.Contains(b, `"loadable":true`) || !strings.Contains(b, `"preview":"fix the tests"`) {
		t.Fatalf("list: %d %s", c, b)
	}
	if c, _ := do(bob, "GET", "/agent/history", ""); c != 403 {
		t.Fatalf("no terminal level: %d", c)
	}
	if c, b := do(alice, "GET", "/agent/history/h1/events", ""); c != 200 || !strings.Contains(b, `"events":[{"seq":1`) || !strings.Contains(b, `"acpSessionId":"c-123"`) {
		t.Fatalf("events: %d %s", c, b)
	}
	if c, _ := do(alice, "GET", "/agent/history/nope/events", ""); c != 404 {
		t.Fatalf("unknown: %d", c)
	}
	// resume: the provider comes from the entry; every gate passes and creation stops at the missing bx
	if c, b := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","resume":"h1"}`); c != 503 || !strings.Contains(b, "bx binary") {
		t.Fatalf("resume h1: %d %s", c, b)
	}
	if c, b := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","resume":"h2"}`); c != 409 || !strings.Contains(b, "cannot reopen") {
		t.Fatalf("resume a non-loadable entry: %d %s", c, b)
	}
	if c, _ := do(alice, "POST", "/term/sessions", `{"cwd":"apps/x","kind":"agent","resume":"nope"}`); c != 404 {
		t.Fatalf("resume unknown: %d", c)
	}
	if c, _ := do(alice, "DELETE", "/agent/history/h1", ""); c != 204 {
		t.Fatalf("delete: %d", c)
	}
	if c, _ := do(alice, "GET", "/agent/history/h1/events", ""); c != 404 {
		t.Fatalf("deleted: %d", c)
	}
}

func TestDiffQuery(t *testing.T) {
	for q, want := range map[string]string{"toolCallId=t1": "t1/0", "turn=3": "/3", "": "err", "toolCallId=t1&turn=2": "err", "turn=0": "err", "turn=x": "err", "turn=-1": "err"} {
		v, _ := url.ParseQuery(q)
		tool, turn, err := diffQuery(v)
		got := fmt.Sprintf("%s/%d", tool, turn)
		if err != nil {
			got = "err"
		}
		if got != want {
			t.Errorf("%q: %s, want %s", q, got, want)
		}
	}
}

// A prompt body: text only as before; attachments decoded (padded or raw
// base64) and normalised; refusals are 400 (shape, base64, count) or 413
// (sizes, the body cap).
func TestDecodePrompt(t *testing.T) {
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	p, code, err := decodePrompt(strings.NewReader(`{"text":"hi"}`))
	if err != nil || p.Text != "hi" || p.Attachments != nil {
		t.Fatalf("text only: %+v %d %v", p, code, err)
	}
	body := fmt.Sprintf(`{"text":"look","attachments":[{"name":"a.png","mime":"image/png","data":%q},{"name":"n.txt","data":%q}]}`,
		b64(png), base64.RawStdEncoding.EncodeToString([]byte("hello")))
	p, _, err = decodePrompt(strings.NewReader(body))
	if err != nil || len(p.Attachments) != 2 || p.Attachments[0].Mime != "image/png" || string(p.Attachments[1].Data) != "hello" || p.Attachments[1].Mime != "text/plain" {
		t.Fatalf("attachments: %+v %v", p, err)
	}
	if p, _, err := decodePrompt(strings.NewReader(fmt.Sprintf(`{"attachments":[{"name":"a.png","data":%q}]}`, b64(png)))); err != nil || p.Text != "" || len(p.Attachments) != 1 {
		t.Fatalf("files only: %+v %v", p, err)
	}
	many := `{"text":"x","attachments":[` + strings.TrimSuffix(strings.Repeat(`{"name":"a","data":""},`, agent.MaxAttachments+1), ",") + `]}`
	bigImg := append(append([]byte(nil), png...), make([]byte, agent.MaxFileBytes)...)
	// an image too big to go inline is still taken: a file for the agent
	overInline := append(append([]byte(nil), png...), make([]byte, agent.MaxImageBytes)...)
	if p, _, err := decodePrompt(strings.NewReader(fmt.Sprintf(`{"attachments":[{"name":"a.png","data":%q}]}`, b64(overInline)))); err != nil || len(p.Attachments) != 1 || p.Attachments[0].Mime != "image/png" {
		t.Fatalf("an image over the inline limit: %v", err)
	}
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"text":"  "}`, 400}, {`{}`, 400}, {`nope`, 400}, {`{"text":5}`, 400},
		{`{"text":"x","attachments":[{"name":"a","data":"%%%"}]}`, 400},
		{many, 400},
		{fmt.Sprintf(`{"text":"x","attachments":[{"name":"a.png","data":%q}]}`, b64(bigImg)), 413},
	} {
		if _, code, err := decodePrompt(strings.NewReader(c.body)); err == nil || code != c.code {
			t.Errorf("%.60s: %d %v, want %d", c.body, code, err, c.code)
		}
	}
	// the route caps the body itself: past it, 413
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"`+strings.Repeat("x", maxPromptBody)+`"}`))
	if _, code, err := decodePrompt(http.MaxBytesReader(w, r.Body, maxPromptBody)); err == nil || code != 413 {
		t.Fatalf("over the body cap: %d %v", code, err)
	}
}

// The routes an agent may not call from its sandbox refuse any agent
// session's own terminal token (its sandbox's XBIN_TOKEN) — on every
// session, not only its own (review: an agent opened a sibling, or used
// one already open, and the two answered each other's permission requests).
// Another shell's token of the same user and tile, a browser, the owner
// pass.
func TestAgentDriven(t *testing.T) {
	isAgent := func(tok string) bool { return tok == "agent-a" || tok == "agent-b" }
	req := func(auth string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	shell := auth.Principal{Component: "apps/x", UserID: "alice", Via: "terminal"}
	for _, tok := range []string{"agent-a", "agent-b"} {
		if !agentDriven(shell, req("Bearer "+tok), isAgent) {
			t.Fatalf("an agent's own token (%s)", tok)
		}
	}
	for _, c := range []struct {
		p    auth.Principal
		auth string
	}{
		{shell, "Bearer shell"}, // a shell's token (bx agent in a terminal)
		{shell, ""},             // no bearer
		{auth.Principal{Owner: true, Via: "bearer"}, "Bearer agent-a"}, // not a terminal principal
		{auth.Principal{UserID: "alice", Via: "session"}, ""},
	} {
		if agentDriven(c.p, req(c.auth), isAgent) {
			t.Errorf("%+v %q refused", c.p, c.auth)
		}
	}
}
