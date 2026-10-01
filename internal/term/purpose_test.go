package term

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// The D178 security review (L11): a guided sign-in's shell is marked by the
// server (purpose), not by a name anyone could give a session.

func TestPurposeGate(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	stub(m, "s1", "owner", "apps/mine", time.Now())
	for _, q := range []string{"cwd=apps/mine&purpose=x", "cwd=apps/mine&purpose=SIGNIN", "cwd=apps/mine&purpose=agent", "session=s1&purpose=x"} {
		r := httptest.NewRequest("GET", "/ws/term?"+q, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		m.ServeWS(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "purpose") {
			t.Fatalf("%s: %d %q, want 400 purpose", q, w.Code, w.Body.String())
		}
	}
}

func TestReservedNameIsNeverTaken(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	stub(m, "s1", "alice", "apps/mine", time.Now())
	for _, name := range []string{SigninName, " XBIN:Sign-In ", "xbin:SIGN-IN"} {
		if code, _ := m.RenameRefused("s1", name); code != 400 {
			t.Fatalf("RenameRefused(%q) = %d, want 400", name, code)
		}
		if !m.Rename("s1", name) { // an agent's open (?name=) or restart: the session stays as it was
			t.Fatal("Rename of a live session said no such session")
		}
		if got := m.ListFor("alice", "", nil)[0].Name; got != "" {
			t.Fatalf("renamed to %q", got)
		}
	}
	if code, _ := m.RenameRefused("s1", "build"); code != 0 {
		t.Fatalf("an ordinary name refused: %d", code)
	}
	if code, _ := m.RenameRefused("nope", "x"); code != 0 {
		t.Fatalf("an unknown id: %d, want 0 (the rename answers 404)", code)
	}
	// an agent's own title never names its tab xbin:sign-in; another does
	a := stub(m, "a1", "alice", "apps/mine", time.Now().Add(time.Second))
	a.kind, a.agent = KindAgent, &agentState{log: agent.NewLog(0, 0)}
	named := func(title string) string {
		a.logEvent(m, agent.Event{Type: agent.EvStatus, Data: json.RawMessage(`{"title":"` + title + `"}`)})
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.name
	}
	if got := named("XBIN:sign-in"); got != "" {
		t.Fatalf("an agent named itself %q", got)
	}
	if got := named("fix the build"); got != "fix the build" {
		t.Fatalf("an agent's own title: %q", got)
	}
}

// A sign-in session over a real PTY (isolation off): opened with
// ?purpose=signin it is listed with its purpose and the reserved name, no
// one renames it, and it ends at its time (the injectable SigninLife; the
// test waits on its close event) while a shell beside it lives on.
func TestSigninSession(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", "/bin/sh")
	m := NewManager(root, nil)
	events := make(chan string, 16)
	m.OnChange = func(op, homeKey, id, cwd string) { events <- op + ":" + id }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	defer srv.Close()
	// open dials a session and returns its id (from the response header)
	open := func(q string) string {
		t.Helper()
		c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term?"+q, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return resp.Header.Get("X-XBin-Session")
	}
	next := func(want string) {
		t.Helper()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event %q, want %q", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %q event", want)
		}
	}

	shell := open("cwd=apps/x&net=none")
	next("open:" + shell)
	signin := open("cwd=apps/x&net=none&purpose=signin")
	next("open:" + signin)

	rows := m.ListFor(HomeKey(auth.Principal{Owner: true}), "", nil)
	if len(rows) != 2 || rows[0].ID != shell || rows[1].ID != signin {
		t.Fatalf("rows = %+v", rows)
	}
	if r := rows[0]; r.Purpose != "" || r.Name != "" {
		t.Fatalf("the shell's row = %+v", r)
	}
	if r := rows[1]; r.Purpose != PurposeSignin || r.Name != SigninName || r.Kind != KindShell {
		t.Fatalf("the sign-in's row = %+v", r)
	}
	if l := m.List(); l[1]["purpose"] != PurposeSignin || l[0]["purpose"] != nil {
		t.Fatalf("the status listing = %v", l)
	}
	if m.PurposeOf(signin) != PurposeSignin || m.PurposeOf(shell) != "" {
		t.Fatal("PurposeOf")
	}
	// no one renames a sign-in's session (older clients hide it by its name)
	if code, why := m.RenameRefused(signin, "mine now"); code != 409 || why == "" {
		t.Fatalf("RenameRefused(sign-in) = %d %q, want 409", code, why)
	}
	m.Rename(signin, "mine now")
	if got := m.ListFor(HomeKey(auth.Principal{Owner: true}), "", nil)[1].Name; got != SigninName {
		t.Fatalf("the sign-in session renamed to %q", got)
	}

	// its time: a session opened under a short SigninLife ends by itself;
	// the two above (opened under the default 15 minutes) live on
	m.mu.Lock()
	m.SigninLife = time.Millisecond
	m.mu.Unlock()
	short := open("cwd=apps/x&net=none&purpose=signin")
	next("open:" + short)
	next("close:" + short)
	if rows := m.ListFor(HomeKey(auth.Principal{Owner: true}), "", nil); len(rows) != 2 || rows[0].ID != shell || rows[1].ID != signin {
		t.Fatalf("after the short one's time: %+v", rows)
	}
	m.Kill(shell)
	next("close:" + shell)
	m.Kill(signin)
	next("close:" + signin)
}
