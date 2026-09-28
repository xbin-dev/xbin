package sandboxcontract

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- run --------------------------------------------------------------------------------

var runChecks = []check{
	{"results", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "runner"})
		id := sb.ID
		if r := a.Run(id, map[string]any{"cmd": "exit 3"}); r.ExitCode == nil || *r.ExitCode != 3 || r.TimedOut || r.Signal != "" {
			t.Fatalf("exit 3: %+v", r)
		}
		r := a.Run(id, map[string]any{"cmd": "echo out; echo err >&2"})
		if r.Stdout.Head != "out\n" || r.Stderr.Head != "err\n" || r.Stdout.Bytes != 4 || r.Stdout.Elided != 0 || r.Output != nil {
			t.Fatalf("split streams: %+v %+v", r.Stdout, r.Stderr)
		}
		r = a.Run(id, map[string]any{"cmd": "echo a; echo b >&2; echo c", "merge": true})
		if r.Output == nil || r.Output.Head != "a\nb\nc\n" || r.Stdout != nil || r.Stderr != nil {
			t.Fatalf("merge: %+v", r)
		}
		env := a.Sh(id, `echo "$IN_SANDBOX|$SANDBOX_ID|$SANDBOX_NAME|$CI"`)
		if r := a.Run(id, map[string]any{"cmd": `echo "$IN_SANDBOX|$SANDBOX_ID|$SANDBOX_NAME|$CI"`, "env": map[string]string{"CI": "1"}}); r.Stdout.Head != "1|"+id+"|runner|1\n" {
			t.Fatalf("env: %q (without env: %q)", r.Stdout.Head, env)
		}
		if pwd := a.Sh(id, "pwd"); pwd != sb.Workdir+"\n" {
			t.Fatalf("cwd defaults to workdir %s: %q", sb.Workdir, pwd)
		}
		a.Sh(id, "mkdir -p sub/dir")
		if r := a.Run(id, map[string]any{"cmd": "pwd", "cwd": sb.Workdir + "/sub/dir"}); r.Stdout.Head != sb.Workdir+"/sub/dir\n" {
			t.Fatalf("cwd: %q", r.Stdout.Head)
		}
		a.Refused("POST", "/sandboxes/"+id+"/run", map[string]any{"cmd": "pwd", "cwd": sb.Workdir + "/no-such"}, 400, "invalid")
		a.Refused("POST", "/sandboxes/"+id+"/run", map[string]any{"cmd": "pwd", "cwd": "sub"}, 400, "invalid")
		a.Refused("POST", "/sandboxes/"+id+"/run", map[string]any{}, 400, "invalid")
		if r := a.Run(id, map[string]any{"cmd": "cat", "stdin": "fed in"}); r.Stdout.Head != "fed in" {
			t.Fatalf("stdin: %q", r.Stdout.Head)
		}
		if r := a.Run(id, map[string]any{"argv": []string{"printf", "%s|", "a b", "$HOME"}}); r.Stdout.Head != "a b|$HOME|" {
			t.Fatalf("argv: %q", r.Stdout.Head)
		}
		if r := a.Run(id, map[string]any{"cmd": `printf 'ok\377'`}); r.Stdout.Head != "ok�" {
			t.Fatalf("output is UTF-8, invalid bytes replaced: %q", r.Stdout.Head)
		}
	}},
	{"shaping", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "shape"}).ID
		const s = "0123456789abcdefghijklmnopqrstuvwxyz" // 36 bytes
		r := a.Run(id, map[string]any{"cmd": "printf " + s + "; printf " + s + " >&2", "maxOutput": 16})
		for _, o := range []*Output{r.Stdout, r.Stderr} { // per stream: a quarter of head, three of tail
			if o.Head != "0123" || o.Tail != "opqrstuvwxyz" || o.Elided != 20 || o.Bytes != 36 {
				t.Fatalf("maxOutput 16: %+v", o)
			}
		}
		r = a.Run(id, map[string]any{"cmd": "printf " + s, "maxOutput": 36})
		if r.Stdout.Head != s || r.Stdout.Tail != "" || r.Stdout.Elided != 0 {
			t.Fatalf("output that fits is all in head: %+v", r.Stdout)
		}
		r = a.Run(id, map[string]any{"cmd": "printf " + s + "; printf " + s + " >&2", "maxOutput": 40, "merge": true})
		if o := r.Output; o.Head != "0123456789" || o.Tail != s[6:] || o.Elided != 32 || o.Bytes != 72 {
			t.Fatalf("merged, maxOutput 40: %+v", o)
		}
	}},
	{"timeout", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "slow"})
		wd, grace := sb.Workdir, e.tg.grace()
		// TERM ends it
		start := time.Now()
		r := a.Run(sb.ID, map[string]any{"cmd": "sleep 30", "timeoutMs": 200})
		if !r.TimedOut || r.Signal != "TERM" || r.ExitCode != nil || time.Since(start) > 10*time.Second {
			t.Fatalf("a timeout: %+v", r)
		}
		// TERM is ignored: KILL after the grace, the whole group — a child too
		r = a.Run(sb.ID, map[string]any{"cmd": "trap '' TERM; sleep 30 & echo $! > " + wd + "/child; wait", "timeoutMs": 200})
		if !r.TimedOut || r.Signal != "KILL" || r.Ms < 200+(grace*3/4).Milliseconds() {
			t.Fatalf("TERM ignored (grace %s): %+v", grace, r)
		}
		eventually(t, 3*time.Second, "the child that ignored TERM is killed", func() bool { return a.Gone(sb.ID, wd+"/child") })
		// a member that outlives its leader (and ignores TERM) is still killed
		r = a.Run(sb.ID, map[string]any{"cmd": "(trap '' TERM; exec sleep 30) >/dev/null 2>&1 & echo $! > " + wd + "/orphan; sleep 30", "timeoutMs": 200})
		if !r.TimedOut {
			t.Fatalf("timedOut: %+v", r)
		}
		eventually(t, 3*time.Second+grace, "the detached member is killed after the grace", func() bool { return a.Gone(sb.ID, wd+"/orphan") })
	}},
	{"hangup", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "hangup"})
		pidf := sb.Workdir + "/pid"
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		answered := make(chan error, 1)
		go func() {
			_, _, err := a.Do(ctx, "POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "echo $$ > " + pidf + "; exec sleep 30"})
			answered <- err
		}()
		eventually(t, 5*time.Second, "the run starts", func() bool {
			resp, _, err := a.Do(context.Background(), "GET", "/sandboxes/"+sb.ID+"/files/stat?path="+q(pidf), nil)
			return err == nil && resp.StatusCode == 200
		})
		cancel()
		if err := <-answered; err == nil {
			t.Fatal("the run answered before the hang-up")
		}
		eventually(t, 3*time.Second, "a run whose caller hung up is killed", func() bool { return a.Gone(sb.ID, pidf) })
	}},
}

// --- background execs -------------------------------------------------------------------

var execChecks = []check{
	{"basics", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "ex"})
		x := a.Exec(sb.ID, map[string]any{"cmd": "echo hi; echo err >&2; exit 4", "label": "build"})
		if x.ID == "" || x.Label != "build" || x.Cwd != sb.Workdir || x.Started == 0 || x.TTY || (x.State != "running" && x.State != "exited") {
			t.Fatalf("exec: %+v", x)
		}
		out, c := a.Drain(sb.ID, x.ID)
		if out != "hi\nerr\n" || c.State != "exited" || c.ExitCode == nil || *c.ExitCode != 4 { // one combined stream
			t.Fatalf("output %q, chunk %+v", out, c)
		}
		var g Exec
		a.Call("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 200, &g)
		if g.State != "exited" || *g.ExitCode != 4 || g.Total != 7 || g.Ended == 0 {
			t.Fatalf("exec after: %+v", g)
		}
		y := a.Exec(sb.ID, map[string]any{"argv": []string{"printf", "%s", "a b"}, "cwd": sb.Home})
		if out, _ := a.Drain(sb.ID, y.ID); out != "a b" || y.Cwd != sb.Home {
			t.Fatalf("argv exec: %q in %s", out, y.Cwd)
		}
		var l struct{ Execs []Exec }
		a.Call("GET", "/sandboxes/"+sb.ID+"/execs", nil, 200, &l)
		if len(l.Execs) != 2 || l.Execs[0].ID != x.ID || l.Execs[1].ID != y.ID {
			t.Fatalf("execs: %+v", l.Execs)
		}
		a.Refused("GET", "/sandboxes/"+sb.ID+"/execs/nope", nil, 404, "not-found")
		a.Refused("GET", "/sandboxes/"+sb.ID+"/execs/nope/output", nil, 404, "not-found")
		a.Refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "true", "cwd": sb.Workdir + "/no-such"}, 400, "invalid")
		a.Refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"label": "nothing"}, 400, "invalid")
		a.Refused("POST", "/sandboxes/nope/execs", map[string]any{"cmd": "true"}, 404, "not-found")
	}},
	{"output", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "poll"}).ID
		x := a.Exec(id, map[string]any{"cmd": "sleep 1; printf late"})
		if c := a.Chunk(id, x.ID, "since=0&waitMs=0"); c.Data != "" || c.State != "running" || c.Start != 0 || c.End != 0 {
			t.Fatalf("waitMs=0 with nothing yet: %+v", c)
		}
		if c := a.Chunk(id, x.ID, "since=0&waitMs=5000"); c.Data != "late" || c.End != 4 { // it waited for the bytes
			t.Fatalf("a long poll: %+v", c)
		}
		a.Drain(id, x.ID)
		y := a.Exec(id, map[string]any{"cmd": "printf 0123456789"})
		a.Drain(id, y.ID)
		if c := a.Chunk(id, y.ID, "since=3&max=4"); c.Data != "3456" || c.Start != 3 || c.End != 7 || c.Total != 10 || c.Encoding != "text" {
			t.Fatalf("since=3&max=4: %+v", c)
		}
		if c := a.Chunk(id, y.ID, "since=10&waitMs=5000"); c.Data != "" || c.Start != 10 || c.End != 10 || c.State != "exited" { // ended: no wait
			t.Fatalf("at the end: %+v", c)
		}
		z := a.Exec(id, map[string]any{"cmd": `printf '\377\000x'`})
		a.Drain(id, z.ID)
		c := a.Chunk(id, z.ID, "since=0&encoding=base64")
		if b, _ := base64.StdEncoding.DecodeString(c.Data); c.Encoding != "base64" || string(b) != "\xff\x00x" {
			t.Fatalf("base64 is exact: %+v", c)
		}
		if c := a.Chunk(id, z.ID, "since=0&encoding=text"); c.Data != "�\x00x" {
			t.Fatalf("text replaces invalid bytes: %q", c.Data)
		}
		a.Refused("GET", "/sandboxes/"+id+"/execs/"+z.ID+"/output?encoding=hex", nil, 400, "invalid")
	}},
	{"ring", func(t *testing.T, e *env) {
		f, _ := e.fresh(Knobs{OutputRing: 64})
		a := f.as("a")
		ring := f.hello.Limits["outputRing"]
		id := a.Create(map[string]any{"name": "ring"}).ID
		// 16-byte lines, over three rings' worth: the oldest bytes are dropped
		const line = "0123456789abcde\n"
		n := 3*ring + 4096
		n -= n % 16
		x := a.Exec(id, map[string]any{"cmd": fmt.Sprintf("yes 0123456789abcde | head -c %d", n)})
		eventually(t, 30*time.Second, "the exec ends", func() bool {
			var g Exec
			a.Call("GET", "/sandboxes/"+id+"/execs/"+x.ID, nil, 200, &g)
			return g.State == "exited"
		})
		c := a.Chunk(id, x.ID, fmt.Sprintf("since=0&max=%d", 1<<20))
		switch {
		case c.Total != n, c.RingStart <= 0, c.Start != c.RingStart, c.End != min(c.Total, c.Start+1<<20):
			t.Fatalf("after the ring overflowed (%d bytes): %+v", n, chunkHead(c))
		case c.Total-c.RingStart < ring: // a ring of at least limits.outputRing
			t.Fatalf("the ring keeps %d bytes, less than outputRing %d", c.Total-c.RingStart, ring)
		}
		want := strings.Repeat(line, int(n/16))
		if c.Data != want[c.Start:c.End] {
			t.Fatalf("the ring's data isn't the stream's bytes %d–%d", c.Start, c.End)
		}
	}},
	{"stdin", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "stdin"}).ID
		x := a.Exec(id, map[string]any{"cmd": "cat; echo done", "stdin": true})
		a.Call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin", []byte("abc"), http.StatusNoContent, nil)
		a.Call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin?eof=1", []byte("def\n"), http.StatusNoContent, nil)
		if out, c := a.Drain(id, x.ID); out != "abcdef\ndone\n" || *c.ExitCode != 0 {
			t.Fatalf("stdin: %q %+v", out, c)
		}
		if resp, _, _ := a.Do(context.Background(), "POST", "/sandboxes/"+id+"/execs/"+x.ID+"/stdin", []byte("late")); resp.StatusCode < 400 {
			t.Fatalf("stdin after the end: %d", resp.StatusCode)
		}
		y := a.Exec(id, map[string]any{"cmd": "sleep 5"})
		if resp, _, _ := a.Do(context.Background(), "POST", "/sandboxes/"+id+"/execs/"+y.ID+"/stdin", []byte("x")); resp.StatusCode < 400 {
			t.Fatalf("stdin to an exec without stdin: %d", resp.StatusCode)
		}
		a.Call("DELETE", "/sandboxes/"+id+"/execs/"+y.ID, nil, http.StatusNoContent, nil)
	}},
	{"signal-delete", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "sig"})
		id, wd := sb.ID, sb.Workdir
		// the group (default): the leader and its child
		x := a.Exec(id, map[string]any{"cmd": "sleep 30 & echo $! > " + wd + "/child; wait"})
		eventually(t, 3*time.Second, "the child's pid", func() bool { return strings.TrimSpace(a.Sh(id, "cat child 2>/dev/null")) != "" })
		a.Call("POST", "/sandboxes/"+id+"/execs/"+x.ID+"/signal", map[string]any{"signal": "TERM"}, http.StatusNoContent, nil)
		if _, c := a.Drain(id, x.ID); c.State != "killed" || c.Signal != "TERM" || c.ExitCode != nil {
			t.Fatalf("after TERM: %+v", c)
		}
		eventually(t, 3*time.Second, "the group's child ends", func() bool { return a.Gone(id, wd+"/child") })
		// the leader alone: it handles TERM and exits by itself
		y := a.Exec(id, map[string]any{"cmd": "trap 'echo caught; exit 7' TERM; echo ready; while :; do sleep 0.05; done"})
		eventually(t, 3*time.Second, "the trap is set", func() bool { return a.Chunk(id, y.ID, "since=0&waitMs=500").Data == "ready\n" })
		a.Call("POST", "/sandboxes/"+id+"/execs/"+y.ID+"/signal", map[string]any{"signal": "TERM", "group": false}, http.StatusNoContent, nil)
		if out, c := a.Drain(id, y.ID); out != "ready\ncaught\n" || c.State != "exited" || *c.ExitCode != 7 {
			t.Fatalf("a handled TERM: %q %+v", out, c)
		}
		a.Refused("POST", "/sandboxes/"+id+"/execs/"+y.ID+"/signal", map[string]any{"signal": "STOP"}, 400, "invalid")
		// DELETE kills the group and forgets the exec
		z := a.Exec(id, map[string]any{"cmd": "echo $$ > " + wd + "/z; exec sleep 30"})
		eventually(t, 3*time.Second, "its pid", func() bool { return strings.TrimSpace(a.Sh(id, "cat z 2>/dev/null")) != "" })
		a.Call("DELETE", "/sandboxes/"+id+"/execs/"+z.ID, nil, http.StatusNoContent, nil)
		a.Refused("GET", "/sandboxes/"+id+"/execs/"+z.ID, nil, 404, "not-found")
		a.Refused("DELETE", "/sandboxes/"+id+"/execs/"+z.ID, nil, 404, "not-found")
		eventually(t, 3*time.Second, "a deleted exec's process ends", func() bool { return a.Gone(id, wd+"/z") })
	}},
	{"idempotent-timeout", func(t *testing.T, e *env) {
		a := e.as("a")
		id := a.Create(map[string]any{"name": "idem"}).ID
		x := a.Exec(id, map[string]any{"cmd": "sleep 5", "clientId": "k"})
		var again Exec
		a.Call("POST", "/sandboxes/"+id+"/execs", map[string]any{"cmd": "sleep 5", "clientId": "k"}, http.StatusOK, &again)
		if again.ID != x.ID || again.ClientID != "k" {
			t.Fatalf("a repeated clientId: %+v, want %s", again, x.ID)
		}
		a.Refused("POST", "/sandboxes/"+id+"/execs", map[string]any{"cmd": "sleep 6", "clientId": "k"}, 409, "exists")
		a.Call("DELETE", "/sandboxes/"+id+"/execs/"+x.ID, nil, http.StatusNoContent, nil)
		// timeoutMs: TERM at the timeout
		y := a.Exec(id, map[string]any{"cmd": "sleep 30", "timeoutMs": 200})
		if _, c := a.Drain(id, y.ID); c.State != "killed" || c.Signal != "TERM" {
			t.Fatalf("an exec's timeout: %+v", c)
		}
		// and KILL after the grace when TERM is ignored
		z := a.Exec(id, map[string]any{"cmd": "trap '' TERM; sleep 30", "timeoutMs": 200})
		if _, c := a.Drain(id, z.ID); c.State != "killed" || c.Signal != "KILL" {
			t.Fatalf("an exec's timeout, TERM ignored: %+v", c)
		}
	}},
}

// chunkHead is a chunk without its data, for a failure message.
func chunkHead(c Chunk) Chunk {
	c.Data = fmt.Sprintf("(%d bytes)", len(c.Data))
	return c
}
