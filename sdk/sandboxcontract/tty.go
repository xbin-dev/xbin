package sandboxcontract

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// --- terminals (tty) --------------------------------------------------------------------
//
// The /ws/term framing (docs/protocol.md §/ws/term): binary frames are the
// terminal's bytes both ways, the ring's tail first; the server's JSON
// starts with {op:"session"} and ends with {op:"exit"}; the client sends
// {op:"resize"} and {op:"ping"}, and anything else it sends is ignored.

// term is one attached terminal, read from the check's goroutine.
type term struct {
	t       *testing.T
	c       *ws.Conn
	session map[string]any
	out     strings.Builder
	pongs   []json.RawMessage
	exit    map[string]any
	end     error // how the connection ended after the exit frame
}

// attach dials path (a terminal route) as a and reads the session frame,
// which comes first.
func attach(t *testing.T, a Caller, path string) *term {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, resp, err := a.Dial(ctx, path)
	if err != nil {
		body := ""
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			body = string(b)
		}
		t.Fatalf("attach %s: %v %s", path, err, body)
	}
	t.Cleanup(func() { c.Close() })
	tm := &term{t: t, c: c}
	typ, msg, err := tm.next(10 * time.Second)
	if err != nil || typ != ws.TextMessage {
		t.Fatalf("attach %s: the first frame: type %d %q %v", path, typ, msg, err)
	}
	if json.Unmarshal(msg, &tm.session) != nil || tm.session["op"] != "session" {
		t.Fatalf("attach %s: the first frame isn't the session: %s", path, msg)
	}
	if id, _ := tm.session["id"].(string); id == "" {
		t.Fatalf("the session frame names no exec: %s", msg)
	}
	if _, ok := tm.session["echoAck"].(bool); !ok {
		t.Fatalf("the session frame has no echoAck: %s", msg)
	}
	return tm
}

func (tm *term) id() string { s, _ := tm.session["id"].(string); return s }

func (tm *term) next(d time.Duration) (int, []byte, error) {
	_ = tm.c.SetReadDeadline(time.Now().Add(d))
	return tm.c.ReadMessage()
}

// read takes one frame: output into out, control frames noted.
func (tm *term) read(d time.Duration) error {
	typ, msg, err := tm.next(d)
	if err != nil {
		return err
	}
	if typ == ws.BinaryMessage {
		tm.out.Write(msg)
		return nil
	}
	var ctl map[string]any
	if json.Unmarshal(msg, &ctl) != nil {
		tm.t.Fatalf("a text frame that isn't JSON: %q", msg)
	}
	switch ctl["op"] {
	case "pong":
		var p struct{ T json.RawMessage }
		_ = json.Unmarshal(msg, &p)
		tm.pongs = append(tm.pongs, p.T)
	case "exit":
		tm.exit = ctl
	case "session":
		tm.t.Fatalf("a second session frame: %s", msg)
	}
	return nil
}

// expect reads until the output holds each of want, in order.
func (tm *term) expect(want ...string) {
	tm.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rest, ok := tm.out.String(), true
		for _, w := range want {
			i := strings.Index(rest, w)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(w):]
		}
		if ok {
			return
		}
		if tm.exit != nil {
			tm.t.Fatalf("the terminal exited (%v) without %q: %q", tm.exit, want, tm.out.String())
		}
		if err := tm.read(time.Until(deadline)); err != nil {
			tm.t.Fatalf("waiting for %q: %v; the terminal said %q", want, err, tm.out.String())
		}
	}
}

// send types into the terminal (a binary frame).
func (tm *term) send(keys string) {
	tm.t.Helper()
	if err := tm.c.WriteMessage(ws.BinaryMessage, []byte(keys)); err != nil {
		tm.t.Fatalf("typing %q: %v", keys, err)
	}
}

// control sends a JSON control frame.
func (tm *term) control(v any) {
	tm.t.Helper()
	b, _ := json.Marshal(v)
	if err := tm.c.WriteMessage(ws.TextMessage, b); err != nil {
		tm.t.Fatalf("control %s: %v", b, err)
	}
}

// exited reads to the exit frame, wants code, and the connection's end.
func (tm *term) exited(code int) {
	tm.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for tm.exit == nil {
		if err := tm.read(time.Until(deadline)); err != nil {
			tm.t.Fatalf("no exit frame: %v; the terminal said %q", err, tm.out.String())
		}
	}
	if c, ok := tm.exit["code"].(float64); !ok || int(c) != code {
		tm.t.Fatalf("exit %v, want code %d", tm.exit, code)
	}
	for tm.end == nil { // then the server closes
		tm.end = tm.read(10 * time.Second)
	}
	if errors.Is(tm.end, os.ErrDeadlineExceeded) {
		tm.t.Fatalf("the connection stayed open after the exit frame: %v", tm.end)
	}
}

// ttyPath is GET …/tty with a query.
func ttyPath(id string, qs url.Values) string {
	return "/sandboxes/" + id + "/tty?" + qs.Encode()
}

// dialRefused wants a terminal route to refuse the upgrade with a JSON
// refusal.
func dialRefused(t *testing.T, a Caller, path string, status int, refusal string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, resp, err := a.Dial(ctx, path)
	if err == nil {
		c.Close()
		t.Fatalf("GET %s as %s upgraded, want %d %s", path, a.who(), status, refusal)
	}
	if resp == nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	a.refusal("GET "+path+" (a WebSocket)", resp, b, status, refusal)
}

// backendTTYWarn: a consumer's backend opens a terminal (a command that
// exits at once) for a person it asserts whom the manager wouldn't admit on
// a verified call. The contract always left that person to the consumer
// (docs/sandbox-manager.md §Who is asking), but the suite checked it for
// reads and run only, so a manager built to it may refuse the terminal (403
// or 404). In the release that adds the check (2026-09-30) that is a
// warning — the check skips, saying why; from the next release it fails,
// and with Target.Strict it fails now. Any other failure is attach's to
// report.
func backendTTYWarn(t *testing.T, e *env, a Caller, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, resp, err := a.Dial(ctx, ttyPath(id, url.Values{"cmd": {"true"}}))
	if err == nil {
		c.Close()
		return
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		return
	}
	b, _ := io.ReadAll(resp.Body)
	msg := "the manager refused a consumer backend's terminal for " + a.who() + " (" + resp.Status + " " + strings.TrimSpace(string(b)) +
		"): a backend call's person is asserted and the consumer's to check, on terminals too — docs/changes/2026-09-30-manager-terminals-for-backends.md"
	if e.tg.Strict {
		t.Fatal(msg)
	}
	t.Skip("WARNING (a failure from the next release): " + msg)
}

var ttyChecks = []check{
	{"unsupported", func(t *testing.T, e *env) {
		if e.has("tty") {
			t.Skip("the manager has terminals (the caps section checks a manager without)")
		}
		noTTY(t, e.as("a"))
	}},
	{"shell", func(t *testing.T, e *env) {
		if !e.has("tty") {
			t.Skip("no tty capability")
		}
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "shell"})
		tm := attach(t, a, ttyPath(sb.ID, url.Values{"rows": {"33"}, "cols": {"111"}}))
		if tm.session["sandbox"] != sb.ID {
			t.Fatalf("the session frame's sandbox: %v", tm.session)
		}
		// the login shell, on a terminal of the size asked for
		tm.send("stty size; test -t 0 && test -t 1 && echo \"tty:$((6*7))\"\r")
		tm.expect("33 111", "tty:42")
		var x Exec
		a.Call("GET", "/sandboxes/"+sb.ID+"/execs/"+tm.id(), nil, http.StatusOK, &x)
		if !x.TTY || x.State != "running" {
			t.Fatalf("the terminal's exec: %+v", x)
		}
		tm.send("exit 5\r")
		tm.exited(5)
	}},
	{"command", func(t *testing.T, e *env) {
		if !e.has("tty") {
			t.Skip("no tty capability")
		}
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "cmd"})
		a.Sh(sb.ID, "mkdir -p sub")
		script := `pwd; stty size; echo ready; read line; stty size; echo "got:$line"; exit 3`
		tm := attach(t, a, ttyPath(sb.ID, url.Values{"cmd": {script}, "cwd": {sb.Workdir + "/sub"}, "rows": {"10"}, "cols": {"20"}}))
		tm.expect(sb.Workdir+"/sub", "10 20", "ready")
		// a ping is answered, an unknown op ignored; a resize reaches the
		// terminal before the keys that follow it
		tm.control(map[string]any{"op": "ping", "t": "x1"})
		tm.control(map[string]any{"op": "no-such-op", "n": 1})
		tm.control(map[string]any{"op": "resize", "cols": 100, "rows": 40})
		tm.send("hi\r")
		tm.expect("40 100", "got:hi")
		tm.exited(3)
		if len(tm.pongs) != 1 || string(tm.pongs[0]) != `"x1"` {
			t.Fatalf("pongs %s, want one with t \"x1\"", tm.pongs)
		}
	}},
	{"attach", func(t *testing.T, e *env) {
		if !e.has("tty") {
			t.Skip("no tty capability")
		}
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "attach"})
		x := a.Exec(sb.ID, map[string]any{"cmd": `echo first; read x; echo "second:$x"`, "tty": true, "rows": 12, "cols": 34})
		if !x.TTY {
			t.Fatalf("a tty exec: %+v", x)
		}
		eventually(t, 10*time.Second, "the exec's first line", func() bool {
			return strings.Contains(a.Chunk(sb.ID, x.ID, "since=0&waitMs=500").Data, "first")
		})
		a.Call("POST", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/resize", map[string]any{"rows": 20, "cols": 60}, http.StatusNoContent, nil)
		tm := attach(t, a, "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/tty")
		if tm.id() != x.ID {
			t.Fatalf("attached to %s, want %s", tm.id(), x.ID)
		}
		tm.expect("first") // the ring replays
		tm.send("again\r")
		tm.expect("second:again")
		tm.exited(0)
		// the terminal's bytes are the exec's output
		out, c := a.Drain(sb.ID, x.ID)
		if !strings.Contains(out, "first") || !strings.Contains(out, "second:again") || c.State != "exited" {
			t.Fatalf("the exec's output: %q %+v", out, c)
		}
		a.Refused("POST", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/resize", map[string]any{"rows": 20, "cols": 60}, 409, "state")
		// attaching to an ended one: the replay, then the exit
		late := attach(t, a, "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/tty")
		late.expect("first", "second:again")
		late.exited(0)
	}},
	{"backend", func(t *testing.T, e *env) {
		// a consumer's backend opens terminals too — to drive them, or to
		// relay one to its page or app — naming its person in Sbx-User: an
		// assertion the manager records and doesn't verify (the consumer
		// polices its people), on its own sandboxes and shared ones alike;
		// the partitions hold
		if !e.has("tty") {
			t.Skip("no tty capability")
		}
		a, b, c := e.as("a"), e.as("b"), e.as("c")
		sb := a.Verified("alice").Create(map[string]any{"name": "backend", "visibility": "private"})
		be := a.Asserting("bob") // neither its owner nor a member
		backendTTYWarn(t, e, be, sb.ID)
		script := `stty size; echo ready; read line; stty size; echo "got:$line"; exit 4`
		tm := attach(t, be, ttyPath(sb.ID, url.Values{"cmd": {script}, "rows": {"10"}, "cols": {"20"}}))
		if tm.session["sandbox"] != sb.ID {
			t.Fatalf("the session frame's sandbox: %v", tm.session)
		}
		tm.expect("10 20", "ready")
		tm.control(map[string]any{"op": "resize", "cols": 100, "rows": 40})
		tm.send("hi\r")
		tm.expect("40 100", "got:hi")
		tm.exited(4)
		var x Exec
		be.Call("GET", "/sandboxes/"+sb.ID+"/execs/"+tm.id(), nil, http.StatusOK, &x)
		if !x.TTY || x.State != "exited" {
			t.Fatalf("the backend's terminal, as an exec: %+v", x)
		}
		// a tty exec the backend started, attached by it
		y := be.Exec(sb.ID, map[string]any{"cmd": `echo started; read l; echo "line:$l"`, "tty": true, "rows": 5, "cols": 50})
		at := attach(t, be, "/sandboxes/"+sb.ID+"/execs/"+y.ID+"/tty")
		if at.id() != y.ID {
			t.Fatalf("attached to %s, want %s", at.id(), y.ID)
		}
		at.expect("started")
		at.send("x\r")
		at.expect("line:x")
		at.exited(0)
		// shared with another consumer: its backend opens one for whoever
		// it names, the share's users being its to apply
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": b.Consumer(), "users": []string{"carol"}}}}, http.StatusOK, nil)
		backendTTYWarn(t, e, b.Asserting("dave"), sb.ID)
		sh := attach(t, b.Asserting("dave"), ttyPath(sb.ID, url.Values{"cmd": {"echo shared-$((6*7))"}}))
		sh.expect("shared-42")
		sh.exited(0)
		// a consumer it isn't shared with sees nothing, whoever it names
		dialRefused(t, c.Asserting("alice"), ttyPath(sb.ID, nil), 404, "not-found")
		dialRefused(t, c.Asserting("alice"), "/sandboxes/"+sb.ID+"/execs/"+y.ID+"/tty", 404, "not-found")
	}},
	{"refusals", func(t *testing.T, e *env) {
		if !e.has("tty") {
			t.Skip("no tty capability")
		}
		a, b := e.as("a"), e.as("b")
		sb := a.Create(map[string]any{"name": "refusals"})
		plain := a.Exec(sb.ID, map[string]any{"cmd": "sleep 30"})
		defer a.Do(context.Background(), "DELETE", "/sandboxes/"+sb.ID+"/execs/"+plain.ID, nil)
		// not a WebSocket: refused before anything starts
		a.Refused("GET", ttyPath(sb.ID, url.Values{"cmd": {"true"}}), nil, 400, "invalid")
		dialRefused(t, a, ttyPath(sb.ID, url.Values{"cwd": {sb.Workdir + "/no-such"}}), 400, "invalid")
		dialRefused(t, a, "/sandboxes/"+sb.ID+"/execs/"+plain.ID+"/tty", 400, "invalid") // no terminal
		a.Refused("POST", "/sandboxes/"+sb.ID+"/execs/"+plain.ID+"/resize", map[string]any{"rows": 5, "cols": 5}, 400, "invalid")
		dialRefused(t, a, "/sandboxes/"+sb.ID+"/execs/nope/tty", 404, "not-found")
		dialRefused(t, b, ttyPath(sb.ID, nil), 404, "not-found") // another consumer's
		dialRefused(t, a.Verified("mallory"), ttyPath(sb.ID, nil), 403, "not-allowed")
		// a stopped sandbox starts on a terminal, as on an exec
		a.Call("POST", "/sandboxes/"+sb.ID+"/stop?wait=30", nil, http.StatusOK, nil)
		tm := attach(t, a, ttyPath(sb.ID, url.Values{"cmd": {"echo up"}}))
		tm.expect("up")
		tm.exited(0)
		if s := a.Get(sb.ID); s.State != "running" {
			t.Fatalf("after a terminal on a stopped sandbox: %s", s.State)
		}
		if e.has("archive") {
			a.Call("POST", "/sandboxes/"+sb.ID+"/archive?wait=60", nil, http.StatusOK, nil)
			dialRefused(t, a, ttyPath(sb.ID, nil), 409, "state")
		}
	}},
}

// noTTY: a manager without the tty capability refuses every terminal route.
func noTTY(t *testing.T, a Caller) {
	t.Helper()
	sb := a.Create(map[string]any{"name": "no-tty"})
	a.Refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "sh", "tty": true}, 501, "unsupported")
	a.Refused("GET", "/sandboxes/"+sb.ID+"/tty", nil, 501, "unsupported")
	x := a.Exec(sb.ID, map[string]any{"cmd": "true"})
	a.Refused("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/tty", nil, 501, "unsupported")
	a.Refused("POST", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/resize", map[string]any{"rows": 10, "cols": 10}, 501, "unsupported")
	dialRefused(t, a, "/sandboxes/"+sb.ID+"/tty", 501, "unsupported")
}
