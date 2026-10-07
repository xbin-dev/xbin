package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// relayFix: the real route table behind an HTTP server (a WebSocket needs
// one), a fake manager with alice's private sandbox, and alice's coding-agent
// conversation in it (carol a participant, dave a viewer) at cwd.
type relayFix struct {
	ag   *Agent
	mux  *http.ServeMux
	srv  *httptest.Server
	m    *sbxTestManager
	nt   *sbxTestManager // a manager without terminals
	box  *sbxSandbox
	ref  string
	cwd  string
	run  int64
	conn *sbxConn
}

func relayFixture(t *testing.T) *relayFix {
	t.Helper()
	ag, mux := accessFixture(t)
	ms := bindSbx(t, "apps/cs", "apps/nt")
	f := &relayFix{ag: ag, mux: mux, m: ms["apps/cs"], nt: ms["apps/nt"]}
	f.nt.Caps = []string{"exec", "files", "tar"}
	f.m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: acptest.Command("--require-login"),
		Login: "GORACE=atexit_sleep_ms=0 " + strings.Join(acptest.Command("login"), " ")}}
	f.box = mkSandbox(t, "apps/cs", "alice", sbxCreate{})
	f.ref = sandboxRef("apps/cs", f.box.ID)
	f.cwd = filepath.Join(f.box.Workdir, "proj")
	if err := os.MkdirAll(f.cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	f.run = harnessRunAs(t, ag, f.ref, f.cwd)
	var err error
	if f.conn, err = sbxDial("apps/cs", "alice"); err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	setTTYGrace(t, 100*time.Millisecond)
	return f
}

func setTTYGrace(t *testing.T, d time.Duration) {
	old := ttyEndGrace.Swap(int64(d))
	t.Cleanup(func() { ttyEndGrace.Store(old) })
}

// harnessRunAs is alice's private coding-agent conversation in sandbox ref,
// carol a participant and dave a viewer.
func harnessRunAs(t *testing.T, ag *Agent, ref, cwd string) int64 {
	t.Helper()
	cfg := defaultConfig()
	cfg.Engine = engineHarness
	cfg.Sandbox = &SandboxBinding{Ref: ref, Name: "box", By: "alice"}
	cfg.Harness = &HarnessConfig{Provider: "fake", Ref: ref, Cwd: cwd, By: "alice"}
	r, err := ag.startRunOpts(runOpts{Title: "h", Cfg: cfg, Hold: true,
		Stamp: runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat", Engine: engineHarness}})
	if err != nil {
		t.Fatal(err)
	}
	for u, role := range map[string]string{"carol": roleParticipant, "dave": roleViewer} {
		if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, 1)`, r.ID, u, role); err != nil {
			t.Fatal(err)
		}
	}
	ag.acl.flush(r.ID)
	return r.ID
}

func (c caller) header() http.Header {
	h := http.Header{}
	if c.from != "" {
		h.Set("X-XBin-From", c.from)
		h.Set("X-XBin-Role", "admin")
	}
	if c.user != "" {
		h.Set("X-XBin-User", c.user)
		h.Set("X-XBin-User-Level", c.level)
	}
	if c.viewedBy != "" {
		h.Set("X-XBin-Viewed-By", c.viewedBy)
	}
	return h
}

// upgradeAs sends a WebSocket handshake through the route table without a
// connection behind it: what a refusal before the upgrade answers.
func upgradeAs(mux *http.ServeMux, c caller, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	r.Header = c.header()
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// term is a relayed terminal as its client reads it.
type term struct {
	t       *testing.T
	c       *ws.Conn
	session string
	out     strings.Builder
	exit    bool
}

func (f *relayFix) dial(t *testing.T, c caller, target string) *term {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, resp, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(f.srv.URL, "http")+target, c.header(), nil)
	if err != nil {
		st := 0
		if resp != nil {
			st = resp.StatusCode
		}
		t.Fatalf("dial %s: %d %v", target, st, err)
	}
	tm := &term{t: t, c: conn}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	typ, msg, err := conn.ReadMessage()
	var s struct{ Op, ID string }
	if err != nil || typ != ws.TextMessage || json.Unmarshal(msg, &s) != nil || s.Op != "session" || s.ID == "" {
		t.Fatalf("%s: the session frame %d %q %v", target, typ, msg, err)
	}
	tm.session = s.ID
	return tm
}

func (tm *term) send(s string) {
	if err := tm.c.WriteMessage(ws.BinaryMessage, []byte(s)); err != nil {
		tm.t.Fatal(err)
	}
}

func (tm *term) ctl(v any) {
	b, _ := json.Marshal(v)
	if err := tm.c.WriteMessage(ws.TextMessage, b); err != nil {
		tm.t.Fatal(err)
	}
}

// until reads until the output holds want (or, want "", the exit frame).
func (tm *term) until(want string) {
	tm.t.Helper()
	tm.c.SetReadDeadline(time.Now().Add(20 * time.Second))
	for (want == "" && !tm.exit) || (want != "" && !strings.Contains(tm.out.String(), want)) {
		typ, msg, err := tm.c.ReadMessage()
		if err != nil {
			tm.t.Fatalf("waiting for %q: %v; the terminal said %q", want, err, tm.out.String())
		}
		if typ == ws.BinaryMessage {
			tm.out.Write(msg)
			continue
		}
		var ctl struct{ Op string }
		if json.Unmarshal(msg, &ctl) == nil && ctl.Op == "exit" {
			tm.exit = true
		}
	}
}

// execState is exec eid's state at the manager ("gone" once forgotten).
func (f *relayFix) execState(t *testing.T, eid string) string {
	t.Helper()
	ex, err := f.conn.ExecGet(context.Background(), f.box.ID, eid)
	if sbxRefusal(err) == "not-found" {
		return "gone"
	}
	if err != nil {
		t.Fatal(err)
	}
	return ex.State
}

func (f *relayFix) deletes(eid string) int {
	return f.m.count("DELETE", "/sbx/sandboxes/"+f.box.ID+"/execs/"+eid)
}

// A coding agent's run terminal: a shell in its sandbox at its cwd, dialled
// at the manager as the tile for the person (Sbx-User), relayed both ways
// with resizes; the relay started it, so it ends it when its client goes.
func TestRunTerminalRelay(t *testing.T) {
	f := relayFixture(t)
	tm := f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal?rows=24&cols=80", f.run))
	tm.send("echo relay-$((6*7))\r")
	tm.until("relay-42")
	tm.send("echo \"at:$(pwd)\"\r")
	tm.until("at:" + f.cwd)
	tm.ctl(map[string]any{"op": "resize", "rows": 40, "cols": 100})
	tm.send("stty size\r")
	tm.until("40 100")
	var saw *fsbCall
	for _, c := range f.m.Calls() {
		if c.Path == "/sbx/sandboxes/"+f.box.ID+"/tty" {
			saw = &c
		}
	}
	if saw == nil {
		t.Fatal("no terminal reached the manager")
	}
	if q, _ := url.ParseQuery(saw.Query); saw.SbxUser != "alice" || saw.User != "" || q.Get("cwd") != f.cwd || q.Get("cmd") != "" ||
		q.Get("rows") != "24" || q.Get("cols") != "80" {
		t.Fatalf("the manager saw %+v", saw)
	}
	if st := f.execState(t, tm.session); st != "running" {
		t.Fatalf("while its client is on: %s", st)
	}
	tm.c.Close()
	waitFor(t, "the terminal its relay started ended", func() bool { return f.execState(t, tm.session) == "gone" })
	if f.deletes(tm.session) != 1 {
		t.Fatalf("deleted %d times", f.deletes(tm.session))
	}
}

// exec= attaches again to a terminal: a client that came back to one keeps
// it — neither its starter's client leaving nor its own ends it.
func TestRunTerminalReattach(t *testing.T) {
	f := relayFixture(t)
	a := f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal", f.run))
	b := f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal?exec=%s&rows=5&login=1", f.run, a.session))
	if b.session != a.session {
		t.Fatalf("attached to %s, not %s", b.session, a.session)
	}
	b.send("echo again-$((2+3))\r")
	b.until("again-5")
	a.until("again-5") // the same terminal
	a.c.Close()
	time.Sleep(400 * time.Millisecond)
	b.send("echo still-$((3+4))\r")
	b.until("still-7")
	b.c.Close()
	time.Sleep(400 * time.Millisecond)
	if st := f.execState(t, a.session); st != "running" || f.deletes(a.session) != 0 {
		t.Fatalf("a terminal a client came back to: %s, deleted %d times", st, f.deletes(a.session))
	}
	for _, c := range f.m.Calls() {
		if c.Path == "/sbx/sandboxes/"+f.box.ID+"/execs/"+a.session+"/tty" && (c.SbxUser != "alice" || c.Query != "") {
			t.Fatalf("the attach: %+v", c)
		}
	}
	// a client that comes back within the grace keeps it too
	c := f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal", f.run))
	ttyEndGrace.Store(int64(time.Second))
	c.c.Close()
	time.Sleep(100 * time.Millisecond)
	d := f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal?exec=%s", f.run, c.session))
	time.Sleep(1500 * time.Millisecond)
	d.send("echo back-$((4+4))\r")
	d.until("back-8")
	if f.deletes(c.session) != 0 {
		t.Fatal("a terminal a client came back to in time was ended")
	}
	_ = f.conn.ExecDelete(context.Background(), f.box.ID, a.session)
	_ = f.conn.ExecDelete(context.Background(), f.box.ID, c.session)
}

// login=1 runs the coding agent's sign-in command: the adapter's own when
// its session said one, else the manager's advertisement (the fake's
// `login` subcommand). One that exits isn't ended again.
func TestRunTerminalLogin(t *testing.T) {
	f := relayFixture(t)
	tm := f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal?login=1", f.run))
	tm.until("paste the code:")
	tm.send("fake-code\r")
	tm.until("Signed in.")
	tm.until("")
	if _, err := os.Stat(filepath.Join(f.box.Home, ".fakeacp", "credentials")); err != nil {
		t.Fatalf("signed in: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if f.deletes(tm.session) != 0 {
		t.Fatal("a terminal that exited was deleted")
	}
	// the adapter's own sign-in command (harness.login.command) wins
	if err := f.ag.db.putHarnessSession(&harnessSession{RunID: f.run, RootID: f.run, Ref: f.ref, Provider: "fake", Gen: 1, State: hsLogin,
		Login: `{"command":"echo adapter-$((5*5))","methods":[]}`}); err != nil {
		t.Fatal(err)
	}
	tm = f.dial(t, asAlice, fmt.Sprintf("/runs/%d/harness/terminal?login=1", f.run))
	tm.until("adapter-25")
	tm.until("")
}

// Any terminal in a sandbox the caller may use: cmd in cwd, as the
// contract's tty route runs it; relayed as the person.
func TestSandboxTerminalRelay(t *testing.T) {
	f := relayFixture(t)
	q := url.Values{"cwd": {f.cwd}, "cmd": {"echo \"in:$(pwd)\"; echo done-$((1+1))"}}
	tm := f.dial(t, asAlice, "/sandboxes/"+url.PathEscape(f.ref)+"/terminal?"+q.Encode())
	tm.until("in:" + f.cwd)
	tm.until("done-2")
	tm.until("")
	var saw *fsbCall
	for _, c := range f.m.Calls() {
		if c.Path == "/sbx/sandboxes/"+f.box.ID+"/tty" {
			saw = &c
		}
	}
	if saw == nil {
		t.Fatal("no terminal reached the manager")
	}
	if mq, _ := url.ParseQuery(saw.Query); saw.SbxUser != "alice" || mq.Get("cwd") != f.cwd || mq.Get("cmd") != q.Get("cmd") {
		t.Fatalf("the manager saw %+v", saw)
	}
	// the path the native view dials (model/sandboxes.js relaySrc: each
	// segment of the ref encoded), a login shell by default
	tm = f.dial(t, asAlice, "/sandboxes/apps/cs%7C"+f.box.ID+"/terminal")
	tm.send("echo shell-$((9*9))\r")
	tm.until("shell-81")
	tm.c.Close()
	waitFor(t, "the shell its relay started ended", func() bool { return f.execState(t, tm.session) == "gone" })
	// GET /sandboxes/{ref} is still the sandbox
	if w := callAs(t, f.mux, asAlice, "GET", "/sandboxes/"+url.PathEscape(f.ref), nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+f.box.ID+`"`) {
		t.Fatalf("GET the sandbox: %d %s", w.Code, w.Body)
	}
}

// Every refusal comes before the upgrade, as JSON.
func TestTerminalRelayRefusals(t *testing.T) {
	f := relayFixture(t)
	builtin := runAs(t, f.ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, true)
	gone := harnessRunAs(t, f.ag, "apps/cs|sb-gone", "")
	ntBox := mkSandbox(t, "apps/nt", "alice", sbxCreate{})
	ntRun := harnessRunAs(t, f.ag, sandboxRef("apps/nt", ntBox.ID), "")
	run := fmt.Sprintf("/runs/%d/harness/terminal", f.run)
	sb := "/sandboxes/" + url.PathEscape(f.ref) + "/terminal"
	for _, c := range []struct {
		name   string
		who    caller
		target string
		plain  bool // not an upgrade
		status int
		want   string
	}{
		{"run: not an upgrade", asAlice, run, true, 400, "a terminal is a WebSocket upgrade"},
		{"run: not a person", asSystem, run, false, 403, "only a person can open a terminal"},
		{"run: an admin viewing as alice", asViewAs, fmt.Sprintf("/runs/%d/harness/terminal", builtin), false, 404, "no such run"},
		{"run: built-in", asAlice, fmt.Sprintf("/runs/%d/harness/terminal", builtin), false, 409, "not a coding-agent conversation"},
		{"run: a viewer", asDave, run, false, 403, "that needs participant"},
		{"run: not theirs", asBob, run, false, 404, "no such run"},
		{"run: a participant who may not use the sandbox", asCarol, run, false, 403, "you may not use box — ask alice"},
		{"run: rows", asAlice, run + "?rows=many", false, 400, "rows: a number"},
		{"run: its sandbox is gone", asAlice, fmt.Sprintf("/runs/%d/harness/terminal", gone), false, 404, `"refusal":"not-found"`},
		{"run: no such exec", asAlice, run + "?exec=e999", false, 404, `"refusal":"not-found"`},
		{"run: a manager without terminals", asAlice, fmt.Sprintf("/runs/%d/harness/terminal", ntRun), false, 501, `"refusal":"unsupported"`},
		{"sandbox: not an upgrade", asAlice, sb, true, 400, "a terminal is a WebSocket upgrade"},
		{"sandbox: not a person", asElement, sb, false, 403, "only a person can open a terminal"},
		{"sandbox: the tile itself", asSystem, sb, false, 403, "only a person can open a terminal"},
		{"sandbox: someone else's private one", asBob, sb, false, 404, "no such sandbox"},
		{"sandbox: a manager who may not use it", asMgr, sb, false, 403, "you may not use box — ask alice"},
		{"sandbox: no such sandbox", asAlice, "/sandboxes/" + url.PathEscape("apps/cs|sb-gone") + "/terminal", false, 404, `"refusal":"not-found"`},
		{"sandbox: no terminals", asAlice, "/sandboxes/" + url.PathEscape(sandboxRef("apps/nt", ntBox.ID)) + "/terminal", false, 501, `"refusal":"unsupported"`},
	} {
		var w *httptest.ResponseRecorder
		if c.plain {
			w = callAs(t, f.mux, c.who, "GET", c.target, nil)
		} else {
			w = upgradeAs(f.mux, c.who, c.target)
		}
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.want) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: %d %s (%s)", c.name, w.Code, w.Body, w.Header().Get("Content-Type"))
		}
	}
	for _, c := range f.m.Calls() { // but the manager's own refusals
		if strings.HasSuffix(c.Path, "/tty") && !strings.Contains(c.Path, "/e999/") && !strings.Contains(c.Path, "/sb-gone/") {
			t.Fatalf("a refused terminal reached the manager: %+v", c)
		}
	}
}

// GET /runs/{id}/harness/log: the tail of the current generation's
// stderr — the baseline wrapper's file, or a split exec's own stream —
// for a person who may use the sandbox.
func TestHarnessLogRoute(t *testing.T) {
	f := relayFixture(t)
	logURL := fmt.Sprintf("/runs/%d/harness/log", f.run)
	if w := callAs(t, f.mux, asAlice, "GET", logURL, nil); w.Code != 404 || !strings.Contains(w.Body.String(), "no log yet") {
		t.Fatalf("no session yet: %d %s", w.Code, w.Body)
	}
	sess := &harnessSession{RunID: f.run, RootID: f.run, Ref: f.ref, Provider: "fake", Gen: 1, State: hsLive}
	if err := f.ag.db.putHarnessSession(sess); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, f.mux, asAlice, "GET", logURL, nil); w.Code != 404 || !strings.Contains(w.Body.String(), "no log yet") {
		t.Fatalf("no file yet: %d %s", w.Code, w.Body)
	}
	dir := filepath.Join(f.box.Home, ".cache", "xbin-harness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, harnessLogName(f.run, 1)+".log"), []byte("fakeacp: up\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := callAs(t, f.mux, asAlice, "GET", logURL, nil)
	if w.Code != 200 || w.Body.String() != "fakeacp: up\nline two\n" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("the log: %d %q %s", w.Code, w.Body, w.Header())
	}
	if w := callAs(t, f.mux, asAlice, "GET", logURL+"?max=9", nil); w.Code != 200 || w.Body.String() != "line two\n" {
		t.Fatalf("its tail: %d %q", w.Code, w.Body)
	}
	if w := callAs(t, f.mux, asAlice, "GET", logURL+"?max=1000000", nil); w.Code != 200 || w.Body.String() != "fakeacp: up\nline two\n" {
		t.Fatalf("a max over the cap: %d %q", w.Code, w.Body)
	}
	for _, c := range f.m.Calls() {
		if c.Path == "/sbx/sandboxes/"+f.box.ID+"/run" && c.SbxUser != "alice" {
			t.Fatalf("read as %+v", c)
		}
	}

	// a split exec's own stderr (a manager offering stdio)
	ex, err := f.conn.ExecStart(context.Background(), f.box.ID, sbxExecReq{Argv: []string{"sh", "-c", "echo on-stdout; echo on-stderr >&2"}, Split: true})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the split exec ends", func() bool { return f.execState(t, ex.ID) == "exited" })
	sess.ExecID, sess.Gen = ex.ID, 2
	if err := f.ag.db.putHarnessSession(sess); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, f.mux, asAlice, "GET", logURL, nil); w.Code != 200 || w.Body.String() != "on-stderr\n" {
		t.Fatalf("a split exec's stderr: %d %q", w.Code, w.Body)
	}

	builtin := runAs(t, f.ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, true)
	for _, c := range []struct {
		name   string
		who    caller
		target string
		status int
		want   string
	}{
		{"built-in", asAlice, fmt.Sprintf("/runs/%d/harness/log", builtin), 409, "not a coding-agent conversation"},
		{"not a person", asSystem, logURL, 403, "only a person who may use box can read its log"},
		{"a participant who may not use the sandbox", asCarol, logURL, 403, "only a person who may use box can read its log"},
		{"a viewer who may not use the sandbox", asDave, logURL, 403, "only a person who may use box can read its log"},
		{"not theirs", asBob, logURL, 404, "no such run"},
		{"max", asAlice, logURL + "?max=lots", 400, "max: a number of bytes"},
	} {
		if w := callAs(t, f.mux, c.who, "GET", c.target, nil); w.Code != c.status || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
}

// tab=<name>: a person's terminal tab outlives its client (a tab not shown
// closes its socket) — dialling the tab again attaches to it, another
// person's tab of the same name is their own, an exited shell is replaced,
// DELETE /terminals/{tab} ends it, and one left with no client is ended
// after ttyTabIdle.
func TestTerminalTabs(t *testing.T) {
	f := relayFixture(t)
	old := ttyTabIdle.Swap(int64(time.Hour))
	t.Cleanup(func() { ttyTabIdle.Store(old) })
	tab := func(c caller, name string) *term {
		return f.dial(t, c, fmt.Sprintf("/sandboxes/%s/terminal?tab=%s", url.PathEscape(f.ref), name))
	}
	a := tab(asAlice, "t1")
	a.send("export MARK=tab-$((7*6))\r")
	a.c.Close()
	time.Sleep(400 * time.Millisecond) // past the starter's grace: a tab's terminal stays
	if st := f.execState(t, a.session); st != "running" {
		t.Fatalf("a tab's terminal with its client gone: %s", st)
	}
	b := tab(asAlice, "t1")
	if b.session != a.session {
		t.Fatalf("the tab again: %s, not %s", b.session, a.session)
	}
	b.send("echo \"$MARK\"\r")
	b.until("tab-42")
	c := tab(asAlice, "t2")
	if c.session == a.session {
		t.Fatal("another tab got the same terminal")
	}
	if w := upgradeAs(f.mux, asAlice, fmt.Sprintf("/sandboxes/%s/terminal?tab=no%%20good", url.PathEscape(f.ref))); w.Code != http.StatusBadRequest {
		t.Fatalf("a bad tab name: %d", w.Code)
	}
	// the tab's ✕ ends it (only the caller's)
	del := func(c caller, name string) int {
		r := httptest.NewRequest("DELETE", "/terminals/"+name, nil)
		r.Header = c.header()
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		var out struct{ Ended int }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("DELETE /terminals/%s: %d %s", name, w.Code, w.Body)
		}
		return out.Ended
	}
	if n := del(asCarol, "t1"); n != 0 {
		t.Fatalf("someone else's DELETE ended %d", n)
	}
	b.c.Close()
	if n := del(asAlice, "t1"); n != 1 {
		t.Fatalf("DELETE ended %d", n)
	}
	waitFor(t, "the closed tab's terminal ended", func() bool { return f.execState(t, a.session) == "gone" })
	// a shell that exited: the tab starts another
	c.send("exit\r")
	c.until("")
	d := tab(asAlice, "t2")
	if d.session == c.session {
		t.Fatal("attached to an exited shell")
	}
	// left with no client past ttyTabIdle: ended
	ttyTabIdle.Store(int64(200 * time.Millisecond))
	d.c.Close()
	waitFor(t, "an idle tab's terminal ended", func() bool { return f.execState(t, d.session) == "gone" })
}
