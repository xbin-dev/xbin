package tilesbx

import (
	"bytes"
	"strings"
	"testing"
)

// A run's output past maxOutput keeps its first quarter in head and its
// last three quarters in tail, elided bytes between — the contract's
// example: maxOutput 65536 and 66560 bytes give 16384 + 49152, 1024 elided.
// Output that fits is all in head.
func TestHeadTail(t *testing.T) {
	stream := make([]byte, 66560)
	for i := range stream {
		stream[i] = 'a' + byte(i%26)
	}
	for _, chunk := range []int{1, 7, 4096, 66560} { // however it arrives
		h := newHeadTail(65536)
		for p := stream; len(p) > 0; {
			n := min(chunk, len(p))
			h.Write(p[:n])
			p = p[n:]
		}
		out := h.result()
		if len(out.Head) != 16384 || len(out.Tail) != 49152 || out.Elided != 1024 || out.Bytes != 66560 {
			t.Fatalf("chunks of %d: head %d, tail %d, elided %d, bytes %d", chunk, len(out.Head), len(out.Tail), out.Elided, out.Bytes)
		}
		if out.Head != string(stream[:16384]) || out.Tail != string(stream[66560-49152:]) {
			t.Fatalf("chunks of %d: head or tail isn't the stream's", chunk)
		}
	}
	h := newHeadTail(65536)
	h.Write(stream[:65536])
	if out := h.result(); len(out.Head) != 65536 || out.Tail != "" || out.Elided != 0 || out.Bytes != 65536 {
		t.Fatalf("output that fits: head %d, tail %d, elided %d", len(out.Head), len(out.Tail), out.Elided)
	}
	h = newHeadTail(65536)
	h.Write(stream[:65537])
	if out := h.result(); len(out.Head) != 16384 || len(out.Tail) != 49152 || out.Elided != 1 {
		t.Fatalf("one byte over: head %d, tail %d, elided %d", len(out.Head), len(out.Tail), out.Elided)
	}
	// a small cap, and an empty stream
	h = newHeadTail(10)
	h.Write([]byte("0123456789ABCDEFGHIJ"))
	if out := h.result(); out.Head != "01" || out.Tail != "CDEFGHIJ" || out.Elided != 10 || out.Bytes != 20 {
		t.Fatalf("a 10-byte cap: %+v", out)
	}
	if out := newHeadTail(10).result(); out.Head != "" || out.Bytes != 0 {
		t.Fatalf("nothing: %+v", out)
	}
}

// A run's text is UTF-8 with invalid bytes replaced — the bytes a head or
// tail cut splits included.
func TestHeadTailUTF8(t *testing.T) {
	h := newHeadTail(1 << 10)
	h.Write([]byte("ok \xff done"))
	if out := h.result(); out.Head != "ok \uFFFD done" || out.Bytes != 9 {
		t.Fatalf("invalid bytes: %q", out.Head)
	}
	h = newHeadTail(8) // head 2 bytes: "€" (3 bytes) is cut
	h.Write([]byte("€uro and more text"))
	out := h.result()
	if out.Head != "\uFFFD" || out.Tail != "e text" || out.Bytes != 20 {
		t.Fatalf("a cut character: %+v", out)
	}
	if !bytes.Equal([]byte(out.Head), []byte("\uFFFD")) {
		t.Fatal("not valid UTF-8")
	}
}

func TestSignalWords(t *testing.T) {
	for n, want := range map[int]string{9: "KILL", 15: "TERM", 2: "INT", 1: "HUP", 11: "SEGV", 40: "SIG40"} {
		if got := signalWord(n); got != want {
			t.Errorf("signalWord(%d) = %q, want %q", n, got, want)
		}
	}
}

// A command's environment: IN_SANDBOX always, SANDBOX_ID, SANDBOX_NAME and
// HOME from xbind, then defaults.env and the command's own over them; never
// an XBIN_* variable.
func TestSessionEnv(t *testing.T) {
	d := &Def{Name: "sb-1", Defaults: Defaults{Env: map[string]string{"SANDBOX_NAME": "My box", "A": "def", "XBIN_TOKEN": "x"}}}
	env := strings.Join(sessionEnv(d, map[string]string{"A": "cmd", "IN_SANDBOX": "0", "xbin_gateway": "y"}, nil), "\n")
	for _, want := range []string{"IN_SANDBOX=1", "SANDBOX_ID=sb-1", "SANDBOX_NAME=My box", "HOME=/root", "A=cmd", "PATH=" + defaultPATH} {
		if !strings.Contains("\n"+env+"\n", "\n"+want+"\n") {
			t.Errorf("no %s in\n%s", want, env)
		}
	}
	if strings.Contains(strings.ToUpper(env), "XBIN_") {
		t.Errorf("an xbin variable got through:\n%s", env)
	}
	u := uint32(1000)
	if env := strings.Join(sessionEnv(d, nil, &u), "\n"); !strings.Contains(env, "HOME=/\n") {
		t.Errorf("a non-root user's HOME:\n%s", env)
	}
	d.Defaults.Env["HOME"] = "/home/dev"
	if env := strings.Join(sessionEnv(d, nil, &u), "\n"); !strings.Contains(env, "HOME=/home/dev") {
		t.Errorf("defaults.env's HOME:\n%s", env)
	}
}
