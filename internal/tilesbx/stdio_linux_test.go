//go:build linux

package tilesbx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// stdioClient is a gorilla client on the stdio route: stdout's bytes,
// stderr's by offset, the control frames.
type stdioClient struct {
	t      *testing.T
	c      *websocket.Conn
	out    []byte
	errOut []byte
	errOff int64 // where stderr's next byte is expected (-1: no stderr frame yet)
	ctl    []map[string]any
	hello  map[string]any
	exit   map[string]any
	closed *websocket.CloseError
}

func dialStdio(t *testing.T, srv *httptest.Server, path string) (*stdioClient, int) {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { c.Close() })
	sc := &stdioClient{t: t, c: c, errOff: -1}
	sc.next(10 * time.Second)
	if sc.hello == nil {
		t.Fatalf("the first frame isn't hello: %v", sc.ctl)
	}
	return sc, http.StatusSwitchingProtocols
}

// next reads one frame (false: the connection ended).
func (sc *stdioClient) next(d time.Duration) bool {
	sc.t.Helper()
	_ = sc.c.SetReadDeadline(time.Now().Add(d))
	mt, b, err := sc.c.ReadMessage()
	if err != nil {
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			sc.t.Fatalf("read: %v (stdout %s, control %v)", err, short(sc.out), sc.ctl)
		}
		sc.closed = ce
		return false
	}
	if mt == websocket.BinaryMessage {
		sc.out = append(sc.out, b...)
		return true
	}
	var ctl map[string]any
	if err := json.Unmarshal(b, &ctl); err != nil {
		sc.t.Fatalf("a text frame that isn't JSON: %q", b)
	}
	sc.ctl = append(sc.ctl, ctl)
	switch ctl["op"] {
	case "hello":
		if sc.hello != nil {
			sc.t.Fatalf("a second hello: %v", ctl)
		}
		sc.hello = ctl
	case "exit":
		sc.exit = ctl
	case "stderr":
		data, _ := base64.StdEncoding.DecodeString(ctl["data"].(string))
		off := int64(ctl["off"].(float64))
		if sc.errOff >= 0 && off != sc.errOff {
			sc.t.Fatalf("stderr at %d, want %d", off, sc.errOff)
		}
		sc.errOut, sc.errOff = append(sc.errOut, data...), off+int64(len(data))
	}
	return true
}

// until reads until cond holds.
func (sc *stdioClient) until(what string, cond func() bool) {
	sc.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) || sc.closed != nil {
			sc.t.Fatalf("never %s: stdout %s, stderr %s, control %v, closed %v", what, short(sc.out), short(sc.errOut), sc.ctl, sc.closed)
		}
		sc.next(time.Until(deadline))
	}
}

func (sc *stdioClient) send(typ int, b []byte) {
	sc.t.Helper()
	if err := sc.c.WriteMessage(typ, b); err != nil {
		sc.t.Fatal(err)
	}
}

func (sc *stdioClient) op(v any) {
	b, _ := json.Marshal(v)
	sc.send(websocket.TextMessage, b)
}

// ended reads to the exit frame and the server's close, and wants a normal one.
func (sc *stdioClient) ended() map[string]any {
	sc.t.Helper()
	sc.until("an exit", func() bool { return sc.exit != nil })
	for sc.next(10 * time.Second) {
	}
	if sc.closed.Code != websocket.CloseNormalClosure {
		sc.t.Fatalf("closed %v after the exit, want 1000", sc.closed)
	}
	return sc.exit
}

func (sc *stdioClient) has(op string) map[string]any {
	for _, c := range sc.ctl {
		if c["op"] == op {
			return c
		}
	}
	return nil
}

// A split exec keeps its stderr apart: …/output is stdout, ?stream=stderr
// its stderr, and the exec counts both. Split is a non-tty exec's; a
// non-split exec has no stderr stream of its own.
func TestExecSplit(t *testing.T) {
	fe := startedEnv(t)
	x := fe.exec("sb-1", map[string]any{"argv": sh("printf out1; printf err1 >&2; printf out2; printf err22 >&2"), "split": true})
	if !x.Split {
		t.Fatalf("a split exec: %+v", x)
	}
	x = fe.waitEnded("sb-1", x.ID)
	if x.Total != 8 || x.ErrTotal != 9 || !x.Split {
		t.Fatalf("after: %+v", x)
	}
	if c := fe.output("sb-1", x.ID, ""); c.Data != "out1out2" || c.Total != 8 {
		t.Fatalf("stdout: %+v", c)
	}
	if c := fe.output("sb-1", x.ID, "stream=stdout&since=4"); c.Data != "out2" {
		t.Fatalf("stream=stdout: %+v", c)
	}
	if c := fe.output("sb-1", x.ID, "stream=stderr&since=3&max=2"); c.Data != "1e" || c.Start != 3 || c.End != 5 || c.Total != 9 || c.State != ExecExited {
		t.Fatalf("stderr: %+v", c)
	}
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID+"/output?stream=stderr&since=10", nil), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID+"/output?stream=both", nil), http.StatusBadRequest, RefInvalid)
	m := fe.exec("sb-1", map[string]any{"argv": sh("printf a; printf b >&2")})
	m = fe.waitEnded("sb-1", m.ID)
	if c := fe.output("sb-1", m.ID, ""); c.Data != "ab" || m.Split || m.ErrTotal != 0 {
		t.Fatalf("merged: %+v %+v", c, m)
	}
	fe.want(fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+m.ID+"/output?stream=stderr", nil), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/execs", map[string]any{"argv": sh("true"), "tty": true, "split": true}), http.StatusBadRequest, RefInvalid)
}

// The stdio socket: hello, stdout replayed from since then live, stdin
// through, stderr as its op from errSince, eof, a ping, the exit and a
// normal close; attaching again after the end replays and says exit.
func TestStdio(t *testing.T) {
	fe := startedEnv(t)
	srv := fe.server(mgr)
	x := fe.exec("sb-1", map[string]any{"argv": sh(`printf 'a\nb\n'; cat; echo done >&2; exit 3`), "stdin": true, "split": true})
	fe.waitOutput("sb-1", x.ID, "a\nb\n")
	path := "/sandboxes/sb-1/execs/" + x.ID + "/stdio"
	sc, _ := dialStdio(t, srv, path+"?since=2")
	if h := sc.hello; h["id"] != x.ID || h["total"] != 4.0 || h["errTotal"] != 0.0 || h["state"] != "running" || h["stdin"] != true || h["split"] != true {
		t.Fatalf("hello: %v", h)
	}
	sc.until("the replay", func() bool { return string(sc.out) == "b\n" })
	sc.send(websocket.BinaryMessage, []byte("typed\n"))
	sc.until("the echo", func() bool { return string(sc.out) == "b\ntyped\n" })
	sc.op(map[string]any{"op": "ping", "t": 7})
	sc.op(map[string]any{"op": "no-such", "x": 1})
	sc.until("a pong", func() bool { return sc.has("pong") != nil })
	if p := sc.has("pong"); p["t"] != 7.0 {
		t.Fatalf("pong: %v", p)
	}
	sc.op(map[string]any{"op": "eof"})
	exit := sc.ended()
	if exit["code"] != 3.0 || exit["signal"] != "" || exit["total"] != 10.0 || exit["errTotal"] != 5.0 || string(sc.errOut) != "done\n" {
		t.Fatalf("exit %v, stderr %q", exit, sc.errOut)
	}
	if g := sc.has("gap"); g != nil {
		t.Fatalf("a gap in a ring that holds everything: %v", g)
	}
	// after the end: the replay from the offsets asked for, then exit
	late, _ := dialStdio(t, srv, path+"?since=4&errSince=2")
	if late.hello["state"] != ExecExited {
		t.Fatalf("hello after the end: %v", late.hello)
	}
	late.ended()
	if string(late.out) != "typed\n" || string(late.errOut) != "ne\n" || late.exit["code"] != 3.0 {
		t.Fatalf("the late replay: %q %q %v", late.out, late.errOut, late.exit)
	}
	// stdin after the end is refused as a frame, not by closing
	x2 := fe.exec("sb-1", map[string]any{"argv": sh("sleep 0.3; echo fin")})
	no, _ := dialStdio(t, srv, "/sandboxes/sb-1/execs/"+x2.ID+"/stdio")
	if no.hello["stdin"] != false || no.hello["split"] != false {
		t.Fatalf("hello without stdin: %v", no.hello)
	}
	no.send(websocket.BinaryMessage, []byte("x"))
	no.until("an error", func() bool { return no.has("error") != nil })
	if e := no.has("error"); e["refusal"] != RefInvalid || e["error"] == "" {
		t.Fatalf("stdin to an exec without it: %v", e)
	}
	no.ended()
	if string(no.out) != "fin\n" {
		t.Fatalf("its output: %q", no.out)
	}
}

// The socket attached last holds stdin: the one before it is closed with
// 4001; the command's stdin paces the client (no 503 here).
func TestStdioNewestWins(t *testing.T) {
	fe := startedEnv(t)
	srv := fe.server(mgr)
	x := fe.exec("sb-1", map[string]any{"argv": sh("sleep 1.5; cat"), "stdin": true})
	path := "/sandboxes/sb-1/execs/" + x.ID + "/stdio"
	first, _ := dialStdio(t, srv, path)
	// 2 MiB before cat reads anything: the pipe fills, and the socket waits
	big := strings.Repeat("0123456789abcde\n", 2<<20/16)
	first.send(websocket.BinaryMessage, []byte(big[:1<<20]))
	first.send(websocket.BinaryMessage, []byte(big[1<<20:]))
	first.until("the echo", func() bool { return len(first.out) == len(big) })
	if string(first.out) != big || first.has("error") != nil {
		t.Fatalf("2 MiB through a slow reader: %d bytes, control %v", len(first.out), first.ctl)
	}
	second, _ := dialStdio(t, srv, fmt.Sprintf("%s?since=%d", path, len(big)))
	for first.next(10 * time.Second) {
	}
	if first.closed.Code != stdioReplaced {
		t.Fatalf("the replaced socket closed with %v, want %d", first.closed, stdioReplaced)
	}
	second.send(websocket.BinaryMessage, []byte("second\n"))
	second.until("its echo", func() bool { return string(second.out) == "second\n" })
	second.op(map[string]any{"op": "eof"})
	if exit := second.ended(); exit["code"] != 0.0 {
		t.Fatalf("exit: %v", exit)
	}
}

// Bytes the ring dropped are a gap op before the rest, on each stream.
func TestStdioGap(t *testing.T) {
	fe := startedEnv(t)
	srv := fe.server(mgr)
	n := 3 << 20
	x := fe.exec("sb-1", map[string]any{"argv": sh(fmt.Sprintf("yes 0123456789abcde | head -c %d; yes 0123456789abcde | head -c %d >&2", n, n)), "split": true})
	x = fe.waitEnded("sb-1", x.ID)
	sc, _ := dialStdio(t, srv, "/sandboxes/sb-1/execs/"+x.ID+"/stdio")
	exit := sc.ended()
	var gaps = map[string]map[string]any{}
	for _, c := range sc.ctl {
		if c["op"] == "gap" {
			gaps[c["stream"].(string)] = c
		}
	}
	line := strings.Repeat("0123456789abcde\n", n/16)
	for _, s := range []struct {
		name string
		got  []byte
	}{{"stdout", sc.out}, {"stderr", sc.errOut}} {
		g := gaps[s.name]
		if g == nil || g["from"] != 0.0 || g["to"].(float64) <= 0 {
			t.Fatalf("%s's gap: %v", s.name, g)
		}
		to := int(g["to"].(float64))
		if string(s.got) != line[to:] {
			t.Fatalf("%s after its gap: %d bytes from %d", s.name, len(s.got), to)
		}
	}
	if exit["total"] != float64(n) || exit["errTotal"] != float64(n) {
		t.Fatalf("exit: %v", exit)
	}
}

// Refusals come before the upgrade, as JSON.
func TestStdioRefusals(t *testing.T) {
	fe := startedEnv(t)
	srv := fe.server(mgr)
	x := fe.exec("sb-1", map[string]any{"argv": sh("printf 12345"), "stdin": true})
	fe.waitEnded("sb-1", x.ID)
	tty := fe.exec("sb-1", map[string]any{"argv": sh("true"), "tty": true})
	p := "/sandboxes/sb-1/execs/"
	other := "000000"
	if fe.m.bootID == other {
		other = "ffffff"
	}
	fe.want(fe.do(mgr, "GET", p+x.ID+"/stdio", nil), http.StatusBadRequest, RefInvalid) // not a WebSocket
	for path, code := range map[string]int{
		p + tty.ID + "/stdio":                               http.StatusBadRequest, // a terminal
		p + x.ID + "/stdio?since=6":                         http.StatusBadRequest, // past the end
		p + x.ID + "/stdio?errSince=1":                      http.StatusBadRequest, // not split: no stderr
		p + x.ID + "/stdio?since=-1":                        http.StatusBadRequest,
		p + fe.m.bootID + "-999/stdio":                      http.StatusNotFound,
		p + other + "-1/stdio":                              http.StatusGone, // another xbind start's
		"/sandboxes/nope/execs/" + fe.m.bootID + "-1/stdio": http.StatusNotFound,
		p + x.ID + "/stdio?since=5&errSince":                http.StatusSwitchingProtocols,
	} {
		sc, got := dialStdio2(t, srv, path)
		if got != code {
			t.Errorf("%s: %d, want %d", path, got, code)
		}
		if sc != nil {
			sc.Close()
		}
	}
}

// sh is argv running script with sh -c (not the login shell cmd runs, whose
// profile the fake runtime can't read: it would say so on stderr).
func sh(script string) []string { return []string{"sh", "-c", script} }

// short is b for a failure message.
func short(b []byte) string {
	if len(b) > 200 {
		return fmt.Sprintf("%q… (%d bytes)", b[:200], len(b))
	}
	return fmt.Sprintf("%q", b)
}

// dialStdio2 dials without reading anything.
func dialStdio2(t *testing.T, srv *httptest.Server, path string) (*websocket.Conn, int) {
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		t.Fatalf("dial %s: %v", path, err)
	}
	return c, http.StatusSwitchingProtocols
}
