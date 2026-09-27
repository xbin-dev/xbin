//go:build linux

package tilesbx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeUsers map[string]bool

func (f fakeUsers) NoTerminal(user string) bool { return f[user] }

// ttyClient is a gorilla client on a TTY route.
type ttyClient struct {
	t    *testing.T
	c    *websocket.Conn
	out  strings.Builder // binary frames so far
	ctl  []map[string]any
	exit map[string]any
}

func dialTTY(t *testing.T, srv *httptest.Server, path string) (*ttyClient, int) {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { c.Close() })
	return &ttyClient{t: t, c: c}, http.StatusSwitchingProtocols
}

// next reads one frame: its JSON when it's text.
func (tc *ttyClient) next(d time.Duration) (text bool, b []byte, ctl map[string]any) {
	tc.t.Helper()
	_ = tc.c.SetReadDeadline(time.Now().Add(d))
	mt, b, err := tc.c.ReadMessage()
	if err != nil {
		tc.t.Fatalf("read: %v (output so far %q)", err, tc.out.String())
	}
	if mt == websocket.TextMessage {
		if err := json.Unmarshal(b, &ctl); err != nil {
			tc.t.Fatalf("a text frame that isn't JSON: %q", b)
		}
		tc.ctl = append(tc.ctl, ctl)
		if ctl["op"] == "exit" {
			tc.exit = ctl
		}
		return true, b, ctl
	}
	tc.out.Write(b)
	return false, b, nil
}

// until reads frames until the output holds want (or, want "", the exit).
func (tc *ttyClient) until(want string) {
	tc.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for (want == "" && tc.exit == nil) || (want != "" && !strings.Contains(tc.out.String(), want)) {
		if time.Now().After(deadline) {
			tc.t.Fatalf("never got %q: output %q, control %v", want, tc.out.String(), tc.ctl)
		}
		tc.next(10 * time.Second)
	}
}

func (tc *ttyClient) send(s string) {
	tc.t.Helper()
	if err := tc.c.WriteMessage(websocket.BinaryMessage, []byte(s)); err != nil {
		tc.t.Fatal(err)
	}
}

func (tc *ttyClient) hasCtl(op string) map[string]any {
	for _, c := range tc.ctl {
		if c["op"] == op {
			return c
		}
	}
	return nil
}

// The TTY WebSocket speaks /ws/term's wire: the session frame first (the
// manager's ids in it), the command's terminal both ways, an echo ack, and
// the exit with its code; an attach to an ended one replays its ring and
// says exit again.
func TestTTY(t *testing.T) {
	fe := startedEnv(t)
	srv := fe.server(mgr)
	q := url.Values{"cmd": {"read x; echo got $x; exit $x"}, "sessionId": {"S1"}, "sandboxId": {"X.1"}, "rows": {"30"}, "cols": {"100"}}
	tc, code := dialTTY(t, srv, "/sandboxes/sb-1/tty?"+q.Encode())
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("dial: %d", code)
	}
	text, _, hello := tc.next(10 * time.Second)
	if !text || hello["op"] != "session" || hello["id"] != "S1" || hello["sandbox"] != "X.1" || hello["echoAck"] != true {
		t.Fatalf("the first frame: %v", hello)
	}
	execs := fe.execList("sb-1")
	if len(execs) != 1 || !execs[0].TTY || execs[0].Label != "terminal" || execs[0].State != ExecRunning {
		t.Fatalf("the tty exec: %+v", execs)
	}
	id := execs[0].ID
	b := fe.box("sb-1")
	if n := b.act.clients.Load(); n != 1 {
		t.Fatalf("clients attached: %d", n)
	}
	tc.send("7") // echoed, then acked while it still runs
	for tc.hasCtl("ack") == nil {
		tc.next(10 * time.Second)
	}
	if ack := tc.hasCtl("ack"); ack["n"] != float64(1) || !strings.Contains(tc.out.String(), "7") {
		t.Fatalf("the echo ack for the input: %v, output %q", tc.ctl, tc.out.String())
	}
	tc.send("\r")
	tc.until("got 7")
	tc.until("")
	if code, ok := tc.exit["code"].(float64); !ok || code != 7 {
		t.Fatalf("the exit frame: %v", tc.exit)
	}
	if x := fe.waitEnded("sb-1", id); x.State != ExecExited || *x.ExitCode != 7 {
		t.Fatalf("the exec: %+v", x)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.act.clients.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("clients after the exit: %d", b.act.clients.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// an attach to it, ended: the session frame (its own ids), its replay, exit
	tc2, _ := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+id+"/tty")
	if _, _, h := tc2.next(10 * time.Second); h["id"] != id || h["sandbox"] != "sb-1" {
		t.Fatalf("the session frame: %v", h)
	}
	tc2.until("")
	if !strings.Contains(tc2.out.String(), "got 7") || tc2.exit["code"] != float64(7) {
		t.Fatalf("an ended tty's replay %q, exit %v", tc2.out.String(), tc2.exit)
	}

	// a live one: detach, reattach and replay, resize by the route, input
	// by stdin, the end by ^D
	x := fe.exec("sb-1", map[string]any{"tty": true, "cmd": "read x; stty size; exec cat", "rows": 24, "cols": 80})
	tc3, _ := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+x.ID+"/tty")
	tc3.next(10 * time.Second)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/resize", `{"rows":40,"cols":120}`), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/stdin", "go\r"), http.StatusNoContent, "")
	tc3.until("40 120")
	tc3.send("hello\r")
	tc3.until("hello")
	tc3.c.Close()
	tc4, _ := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+x.ID+"/tty")
	tc4.next(10 * time.Second)
	tc4.until("hello") // the replay
	if err := tc4.c.WriteMessage(websocket.TextMessage, []byte(`{"op":"ping","t":"x1"}`)); err != nil {
		t.Fatal(err)
	}
	for tc4.hasCtl("pong") == nil {
		tc4.next(10 * time.Second)
	}
	if p := tc4.hasCtl("pong"); p["t"] != "x1" {
		t.Fatalf("the pong: %v", p)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+x.ID+"/stdin?eof=1", ""), http.StatusBadRequest, RefInvalid)
	tc4.send("\x04")
	tc4.until("")
	if tc4.exit["code"] != float64(0) {
		t.Fatalf("after ^D: %v", tc4.exit)
	}

	// refusals, before any upgrade
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/tty", nil), http.StatusBadRequest, RefInvalid)
	plain := fe.exec("sb-1", map[string]any{"argv": []string{"sleep", "30"}})
	if _, code := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+plain.ID+"/tty"); code != http.StatusBadRequest {
		t.Fatalf("a non-tty exec's attach: %d", code)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs/"+plain.ID+"/resize", `{"rows":40,"cols":120}`), http.StatusBadRequest, RefInvalid)
	if _, code := dialTTY(t, srv, "/sandboxes/sb-1/tty?sessionId=a/b"); code != http.StatusBadRequest {
		t.Fatalf("a bad sessionId: %d", code)
	}
	if _, code := dialTTY(t, fe.server(mgrFrame), "/sandboxes/sb-1/tty"); code != http.StatusForbidden {
		t.Fatalf("the manager's frame: %d", code)
	}
}

// D88: a tty exec claimed for a user with noTerminal is refused — at its
// start and at every attach, the attach's own forUser too — and switching
// noTerminal on kills the tty execs claimed for that user, and those they
// attached to. Non-tty execs aren't restricted.
func TestTTYNoTerminal(t *testing.T) {
	users := fakeUsers{"alice": true}
	fe := startedEnv(t, func(o *Options) { o.Deps.Users = users })
	srv := fe.server(mgr)
	if _, code := dialTTY(t, srv, "/sandboxes/sb-1/tty?forUser=alice"); code != http.StatusForbidden {
		t.Fatalf("a tty for alice: %d", code)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs", map[string]any{"tty": true, "argv": []string{"sleep", "30"}, "forUser": "alice"}), http.StatusForbidden, RefNotAllowed)
	fe.exec("sb-1", map[string]any{"argv": []string{"true"}, "forUser": "alice"}) // not a terminal
	bob := fe.exec("sb-1", map[string]any{"tty": true, "argv": []string{"sleep", "100"}, "forUser": "bob"})
	if _, code := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+bob.ID+"/tty?forUser=alice"); code != http.StatusForbidden {
		t.Fatalf("alice attaching to bob's: %d", code)
	}
	carol := fe.exec("sb-1", map[string]any{"tty": true, "argv": []string{"sleep", "100"}, "forUser": "carol"})
	if _, code := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+carol.ID+"/tty?forUser=dave"); code != http.StatusSwitchingProtocols {
		t.Fatalf("dave attaching to carol's: %d", code)
	}
	// bob loses terminals: his exec goes, and a new attach is refused
	users["bob"] = true
	fe.m.OnNoTerminal("bob")
	if x := fe.waitEnded("sb-1", bob.ID); x.State != ExecKilled || x.Signal != "KILL" {
		t.Fatalf("bob's tty: %+v", x)
	}
	if _, code := dialTTY(t, srv, "/sandboxes/sb-1/execs/"+bob.ID+"/tty"); code != http.StatusForbidden {
		t.Fatalf("an attach to bob's: %d", code)
	}
	if x := fe.execGet("sb-1", carol.ID); x.State != ExecRunning {
		t.Fatalf("carol's went with bob's: %+v", x)
	}
	// dave loses them: carol's, which he attached to, goes
	fe.m.OnNoTerminal("dave")
	if x := fe.waitEnded("sb-1", carol.ID); x.State != ExecKilled {
		t.Fatalf("carol's, dave attached: %+v", x)
	}
}
