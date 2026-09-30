package sandboxcontract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// --- stdio sockets (stdio) --------------------------------------------------------------
//
// A split exec keeps its stderr apart (…/output?stream=stderr), and
// GET …/execs/{eid}/stdio?since=&errSince= is a non-tty exec's streams on one
// WebSocket: {"op":"hello"} first; stdout as binary frames from since, a
// {"op":"gap","stream"} where the ring dropped bytes; a split exec's stderr as
// {"op":"stderr","off","data"} (base64) from errSince; {"op":"exit"} once the
// exec ended and all its output is out, then a normal close. The client
// sends stdin as binary frames, {"op":"eof"} and {"op":"ping"}. The socket
// attached last holds stdin: the one before it is closed with 4001.

// stdioReplaced is the close code of a socket a newer attach replaced.
const stdioReplaced = 4001

// pipe is one attached stdio socket, read from the check's goroutine.
type pipe struct {
	t      *testing.T
	c      *ws.Conn
	hello  map[string]any
	out    []byte // stdout since the last gap (or since the offset attached at)
	outAt  int64  // the offset of out[0]
	outOff int64  // stdout's next offset
	errOut []byte // stderr, likewise
	errAt  int64
	errOff int64
	gaps   []map[string]any
	errs   []map[string]any // error frames
	pongs  []json.RawMessage
	exit   map[string]any
	end    error // how the connection ended
}

// stdioPath is the stdio route of exec eid of sandbox id at the offsets.
func stdioPath(id, eid string, since, errSince int64) string {
	return fmt.Sprintf("/sandboxes/%s/execs/%s/stdio?since=%d&errSince=%d", id, eid, since, errSince)
}

// openPipe dials exec eid's stdio socket as a from the offsets and reads
// the hello frame, which comes first.
func openPipe(t *testing.T, a Caller, id, eid string, since, errSince int64) *pipe {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := stdioPath(id, eid, since, errSince)
	c, resp, err := a.Dial(ctx, path)
	if err != nil {
		body := ""
		if resp != nil {
			b := make([]byte, 4096)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
		}
		t.Fatalf("attach %s: %v %s", path, err, body)
	}
	t.Cleanup(func() { c.Close() })
	p := &pipe{t: t, c: c, outAt: since, outOff: since, errAt: errSince, errOff: errSince}
	typ, msg, err := p.next(10 * time.Second)
	if err != nil || typ != ws.TextMessage || json.Unmarshal(msg, &p.hello) != nil || p.hello["op"] != "hello" {
		t.Fatalf("attach %s: the first frame isn't hello: %d %q %v", path, typ, msg, err)
	}
	h := p.hello
	_, total := h["total"].(float64)
	_, errTotal := h["errTotal"].(float64)
	_, state := h["state"].(string)
	_, stdin := h["stdin"].(bool)
	_, split := h["split"].(bool)
	if h["id"] != eid || !total || !errTotal || !state || !stdin || !split {
		t.Fatalf("hello %s, want {op, id %q, total, errTotal, state, stdin, split}", msg, eid)
	}
	return p
}

func (p *pipe) next(d time.Duration) (int, []byte, error) {
	_ = p.c.SetReadDeadline(time.Now().Add(d))
	return p.c.ReadMessage()
}

// read takes one frame: stdout and stderr into their buffers, at the
// offsets they must follow, control frames noted.
func (p *pipe) read(d time.Duration) error {
	p.t.Helper()
	typ, msg, err := p.next(d)
	if err != nil {
		return err
	}
	if typ == ws.BinaryMessage {
		p.out = append(p.out, msg...)
		p.outOff += int64(len(msg))
		return nil
	}
	var ctl map[string]any
	if json.Unmarshal(msg, &ctl) != nil {
		p.t.Fatalf("a text frame that isn't JSON: %q", msg)
	}
	num := func(k string) int64 {
		f, ok := ctl[k].(float64)
		if !ok {
			p.t.Fatalf("%s frame without %s: %s", ctl["op"], k, msg)
		}
		return int64(f)
	}
	switch ctl["op"] {
	case "gap":
		from, to := num("from"), num("to")
		switch ctl["stream"] {
		case "stdout":
			if from != p.outOff || to <= from {
				p.t.Fatalf("stdout gap %s at offset %d", msg, p.outOff)
			}
			p.out, p.outAt, p.outOff = nil, to, to
		case "stderr":
			if from != p.errOff || to <= from {
				p.t.Fatalf("stderr gap %s at offset %d", msg, p.errOff)
			}
			p.errOut, p.errAt, p.errOff = nil, to, to
		default:
			p.t.Fatalf("a gap of no stream: %s", msg)
		}
		p.gaps = append(p.gaps, ctl)
	case "stderr":
		data, err := base64.StdEncoding.DecodeString(fmt.Sprint(ctl["data"]))
		if off := num("off"); err != nil || off != p.errOff {
			p.t.Fatalf("stderr frame %s at offset %d (%v)", msg, p.errOff, err)
		}
		p.errOut = append(p.errOut, data...)
		p.errOff += int64(len(data))
	case "pong":
		var pg struct{ T json.RawMessage }
		_ = json.Unmarshal(msg, &pg)
		p.pongs = append(p.pongs, pg.T)
	case "error":
		if r, _ := ctl["refusal"].(string); r == "" || ctl["error"] == "" {
			p.t.Fatalf("an error frame without its refusal: %s", msg)
		}
		p.errs = append(p.errs, ctl)
	case "exit":
		if p.exit != nil {
			p.t.Fatalf("a second exit: %s", msg)
		}
		if num("total") != p.outOff || num("errTotal") != p.errOff {
			p.t.Fatalf("exit %s, but stdout reached %d and stderr %d", msg, p.outOff, p.errOff)
		}
		p.exit = ctl
	case "hello":
		p.t.Fatalf("a second hello: %s", msg)
	}
	return nil
}

// until reads until cond holds.
func (p *pipe) until(what string, cond func() bool) {
	p.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if err := p.read(time.Until(deadline)); err != nil {
			p.t.Fatalf("waiting for %s: %v; stdout %s, stderr %s", what, err, short(p.out), short(p.errOut))
		}
	}
}

// expect reads until stdout (since the offset attached at) is want.
func (p *pipe) expect(want string) {
	p.t.Helper()
	p.until(fmt.Sprintf("stdout %q", want), func() bool { return len(p.out) >= len(want) })
	if string(p.out) != want {
		p.t.Fatalf("stdout %s, want %q", short(p.out), want)
	}
}

// send writes stdin.
func (p *pipe) send(b string) {
	p.t.Helper()
	if err := p.c.WriteMessage(ws.BinaryMessage, []byte(b)); err != nil {
		p.t.Fatalf("stdin %s: %v", short([]byte(b)), err)
	}
}

// op sends a JSON frame.
func (p *pipe) op(v any) {
	p.t.Helper()
	b, _ := json.Marshal(v)
	if err := p.c.WriteMessage(ws.TextMessage, b); err != nil {
		p.t.Fatalf("frame %s: %v", b, err)
	}
}

// exited reads to the exit frame and the normal close after it, and wants
// code (nil: a signal, sig).
func (p *pipe) exited(code *int, sig string) {
	p.t.Helper()
	p.until("the exit frame", func() bool { return p.exit != nil })
	c, isCode := p.exit["code"].(float64)
	switch {
	case code == nil && (p.exit["code"] != nil || p.exit["signal"] != sig):
		p.t.Fatalf("exit %v, want no code and signal %s", p.exit, sig)
	case code != nil && (!isCode || int(c) != *code || p.exit["signal"] != ""):
		p.t.Fatalf("exit %v, want code %d", p.exit, *code)
	}
	p.closed(ws.CloseNormalClosure)
}

// closed reads to the connection's end and wants a close frame with code.
func (p *pipe) closed(code int) {
	p.t.Helper()
	for p.end == nil {
		p.end = p.read(10 * time.Second)
	}
	if errors.Is(p.end, os.ErrDeadlineExceeded) {
		p.t.Fatalf("the connection stayed open: %v", p.end)
	}
	var ce *ws.CloseError
	if !errors.As(p.end, &ce) || ce.Code != code {
		p.t.Fatalf("the connection ended with %v, want a close %d", p.end, code)
	}
}

// short is b for a failure message.
func short(b []byte) string {
	if len(b) > 200 {
		return fmt.Sprintf("%q… (%d bytes)", b[:200], len(b))
	}
	return fmt.Sprintf("%q", b)
}

func code(n int) *int { return &n }

// drainStream is Drain for one stream of a split exec ("stderr").
func (c Caller) drainStream(id, eid, stream string) string {
	c.t.Helper()
	var buf []byte
	since := int64(0)
	for deadline := time.Now().Add(10*time.Second + c.tg.grace()); ; {
		ch := c.Chunk(id, eid, fmt.Sprintf("since=%d&waitMs=2000&encoding=base64&stream=%s", since, stream))
		d, err := base64.StdEncoding.DecodeString(ch.Data)
		if err != nil {
			c.t.Fatalf("output: %v", err)
		}
		buf, since = append(buf, d...), ch.End
		if ch.State != "running" && ch.End >= ch.Total {
			return string(buf)
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("exec %s still %s: %q", eid, ch.State, buf)
		}
	}
}

var stdioChecks = []check{
	{"split", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "split"})
		x := a.Exec(sb.ID, map[string]any{"cmd": "printf out1; printf err1 >&2; printf out2; printf err22 >&2", "split": true})
		if !x.Split {
			t.Fatalf("a split exec: %+v", x)
		}
		if out, c := a.Drain(sb.ID, x.ID); out != "out1out2" || c.State != "exited" {
			t.Fatalf("stdout %q %+v", out, c)
		}
		if errOut := a.drainStream(sb.ID, x.ID, "stderr"); errOut != "err1err22" {
			t.Fatalf("stderr %q", errOut)
		}
		var g Exec
		a.Call("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, http.StatusOK, &g)
		if !g.Split || g.Total != 8 || g.ErrTotal != 9 {
			t.Fatalf("the exec counts both streams: %+v", g)
		}
		if c := a.Chunk(sb.ID, x.ID, "stream=stderr&since=3&max=2"); c.Data != "1e" || c.Start != 3 || c.End != 5 || c.Total != 9 {
			t.Fatalf("stderr from 3: %+v", c)
		}
		if c := a.Chunk(sb.ID, x.ID, "stream=stdout&since=4"); c.Data != "out2" || c.Total != 8 {
			t.Fatalf("stream=stdout: %+v", c)
		}
		a.Refused("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/output?stream=both", nil, 400, "invalid")
		// without split: one stream, and no stderr of its own
		y := a.Exec(sb.ID, map[string]any{"cmd": "printf a; printf b >&2"})
		if out, _ := a.Drain(sb.ID, y.ID); out != "ab" || y.Split {
			t.Fatalf("merged %q: %+v", out, y)
		}
		a.Refused("GET", "/sandboxes/"+sb.ID+"/execs/"+y.ID+"/output?stream=stderr", nil, 400, "invalid")
		if e.has("tty") { // a terminal is one stream
			a.Refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "true", "tty": true, "split": true}, 400, "invalid")
		}
	}},
	{"replay", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "replay"}).ID
		x := a.Exec(id, map[string]any{"cmd": `printf 'a\nb\n'; cat; echo done >&2; exit 3`, "stdin": true, "split": true})
		eventually(t, 10*time.Second, "the first lines", func() bool { return a.Chunk(id, x.ID, "since=0&waitMs=500").Data == "a\nb\n" })
		p := openPipe(t, a, id, x.ID, 2, 0)
		if h := p.hello; h["total"] != 4.0 || h["errTotal"] != 0.0 || h["state"] != "running" || h["stdin"] != true || h["split"] != true {
			t.Fatalf("hello %v", h)
		}
		p.expect("b\n") // from since
		p.send("typed\n")
		p.expect("b\ntyped\n") // stdin through, live stdout after the replay
		p.op(map[string]any{"op": "ping", "t": "x1"})
		p.op(map[string]any{"op": "no-such-op", "n": 1})
		p.until("a pong", func() bool { return len(p.pongs) > 0 })
		if string(p.pongs[0]) != `"x1"` {
			t.Fatalf("pong %s, want t \"x1\"", p.pongs[0])
		}
		p.op(map[string]any{"op": "eof"})
		p.exited(code(3), "")
		if string(p.errOut) != "done\n" || len(p.gaps) != 0 || len(p.errs) != 0 {
			t.Fatalf("stderr %q, gaps %v, errors %v", p.errOut, p.gaps, p.errs)
		}
		// attached after the end: the replay from the offsets, then the exit
		late := openPipe(t, a, id, x.ID, 4, 2)
		if late.hello["state"] == "running" {
			t.Fatalf("hello after the end: %v", late.hello)
		}
		late.exited(code(3), "")
		if string(late.out) != "typed\n" || string(late.errOut) != "ne\n" {
			t.Fatalf("the late replay: %q %q", late.out, late.errOut)
		}
		// at the very end: nothing to replay
		end := openPipe(t, a, id, x.ID, 10, 5)
		end.exited(code(3), "")
		if len(end.out) != 0 || len(end.errOut) != 0 {
			t.Fatalf("at the end: %q %q", end.out, end.errOut)
		}
	}},
	{"gap", func(t *testing.T, e *env) {
		f, _ := e.fresh(Knobs{OutputRing: 64})
		a := f.as("a")
		ring := f.hello.Limits["outputRing"]
		id := a.Create(map[string]any{"name": "gap"}).ID
		// 16-byte lines, over three rings' worth on each stream
		n := 3*ring + 4096
		n -= n % 16
		x := a.Exec(id, map[string]any{"cmd": fmt.Sprintf("yes 0123456789abcde | head -c %d; yes 0123456789abcde | head -c %d >&2", n, n), "split": true})
		eventually(t, 30*time.Second, "the exec ends", func() bool {
			var g Exec
			a.Call("GET", "/sandboxes/"+id+"/execs/"+x.ID, nil, http.StatusOK, &g)
			return g.State == "exited"
		})
		p := openPipe(t, a, id, x.ID, 0, 0)
		p.exited(code(0), "")
		line := strings.Repeat("0123456789abcde\n", int(n/16))
		for _, s := range []struct {
			name string
			at   int64
			got  []byte
		}{{"stdout", p.outAt, p.out}, {"stderr", p.errAt, p.errOut}} {
			var first map[string]any
			for _, g := range p.gaps {
				if g["stream"] == s.name && first == nil {
					first = g
				}
			}
			if first == nil || first["from"] != 0.0 {
				t.Fatalf("%s overflowed its ring (%d bytes of %d): gaps %v", s.name, n, ring, p.gaps)
			}
			if string(s.got) != line[s.at:] {
				t.Fatalf("%s after its gap: %d bytes from %d aren't the stream's", s.name, len(s.got), s.at)
			}
		}
	}},
	{"exit", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "exit"}).ID
		x := a.Exec(id, map[string]any{"cmd": "echo bye; exit 5"})
		p := openPipe(t, a, id, x.ID, 0, 0)
		p.exited(code(5), "")
		if string(p.out) != "bye\n" || p.exit["total"] != 4.0 || p.exit["errTotal"] != 0.0 {
			t.Fatalf("stdout %q, exit %v", p.out, p.exit)
		}
		// a signal ends it: no code, the signal named
		y := a.Exec(id, map[string]any{"cmd": "echo up; exec sleep 30"})
		q := openPipe(t, a, id, y.ID, 0, 0)
		q.expect("up\n")
		a.Call("POST", "/sandboxes/"+id+"/execs/"+y.ID+"/signal", map[string]any{"signal": "TERM"}, http.StatusNoContent, nil)
		q.exited(nil, "TERM")
	}},
	{"newest", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "newest"}).ID
		x := a.Exec(id, map[string]any{"cmd": "cat", "stdin": true})
		first := openPipe(t, a, id, x.ID, 0, 0)
		first.send("one\n")
		first.expect("one\n")
		second := openPipe(t, a, id, x.ID, 4, 0)
		first.closed(stdioReplaced) // the socket attached last holds stdin
		second.send("two\n")
		second.expect("two\n")
		third := openPipe(t, a, id, x.ID, 0, 0)
		second.closed(stdioReplaced)
		third.expect("one\ntwo\n")
		third.op(map[string]any{"op": "eof"})
		third.exited(code(0), "")
	}},
	{"stdin", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "stdin"}).ID
		// a command that reads late: the socket waits for it, nothing is refused
		x := a.Exec(id, map[string]any{"cmd": "sleep 1; cat", "stdin": true})
		p := openPipe(t, a, id, x.ID, 0, 0)
		big := strings.Repeat("0123456789abcde\n", (256<<10)/16)
		p.send(big[:128<<10])
		p.send(big[128<<10:])
		p.op(map[string]any{"op": "eof"})
		p.exited(code(0), "")
		if string(p.out) != big || len(p.errs) != 0 {
			t.Fatalf("256 KiB through a late reader: %d bytes, errors %v", len(p.out), p.errs)
		}
		// no stdin: a frame is answered with an error, and the socket goes on
		y := a.Exec(id, map[string]any{"cmd": "sleep 1; echo fin"})
		q := openPipe(t, a, id, y.ID, 0, 0)
		if q.hello["stdin"] != false || q.hello["split"] != false {
			t.Fatalf("hello without stdin: %v", q.hello)
		}
		q.send("x")
		q.until("an error frame", func() bool { return len(q.errs) > 0 })
		if q.errs[0]["refusal"] != "invalid" {
			t.Fatalf("stdin to an exec without it: %v", q.errs[0])
		}
		q.exited(code(0), "")
		if string(q.out) != "fin\n" {
			t.Fatalf("its output: %q", q.out)
		}
	}},
	{"refusals", func(t *testing.T, e *env) {
		a, b := e.as("a"), e.as("b")
		sb := a.Create(map[string]any{"name": "refusals"})
		x := a.Exec(sb.ID, map[string]any{"cmd": "printf 12345", "stdin": true})
		a.Drain(sb.ID, x.ID)
		p := "/sandboxes/" + sb.ID + "/execs/"
		a.Refused("GET", p+x.ID+"/stdio", nil, 400, "invalid") // not a WebSocket
		dialRefused(t, a, p+x.ID+"/stdio?since=6", 400, "invalid")
		dialRefused(t, a, p+x.ID+"/stdio?errSince=1", 400, "invalid") // not split: no stderr
		dialRefused(t, a, p+x.ID+"/stdio?since=-1", 400, "invalid")
		dialRefused(t, a, p+"nope/stdio", 404, "not-found")
		dialRefused(t, b, p+x.ID+"/stdio", 404, "not-found") // another consumer's
		dialRefused(t, a.Verified("mallory"), p+x.ID+"/stdio", 403, "not-allowed")
		if e.has("tty") {
			y := a.Exec(sb.ID, map[string]any{"cmd": "true", "tty": true})
			dialRefused(t, a, p+y.ID+"/stdio", 400, "invalid") // a terminal: …/tty
		}
	}},
}

// noStdio: a manager without the stdio capability ignores split (a field it
// doesn't know) and refuses the stdio route: 501 unsupported — or, a manager
// from before the capability, 404 not-found, as its unknown routes answer
// (docs/sandbox-manager.md §hello: a consumer checks caps first). So a
// manager that passed caps/missing before stdio existed still passes it.
func noStdio(t *testing.T, a Caller) {
	t.Helper()
	sb := a.Create(map[string]any{"name": "no-stdio"})
	x := a.Exec(sb.ID, map[string]any{"cmd": "printf o; printf e >&2", "stdin": true, "split": true})
	if out, _ := a.Drain(sb.ID, x.ID); out != "oe" || x.Split {
		t.Fatalf("split without stdio: %q %+v", out, x)
	}
	path := "/sandboxes/" + sb.ID + "/execs/" + x.ID + "/stdio"
	resp, b, err := a.Do(context.Background(), "GET", path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	status, refusal := http.StatusNotImplemented, "unsupported"
	if resp.StatusCode == http.StatusNotFound {
		status, refusal = http.StatusNotFound, "not-found" // a manager from before stdio
	}
	a.refusal("GET "+path, resp, b, status, refusal)
	dialRefused(t, a, path, status, refusal)
}
