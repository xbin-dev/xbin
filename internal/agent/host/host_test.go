package host

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

// The reaper owns SIGCHLD for every child started in this test binary
// (which is why nothing in this package calls cmd.Wait).
func TestMain(m *testing.M) {
	StartReaper()
	os.Exit(m.Run())
}

func newHost(t *testing.T, cwd string) *Host {
	t.Helper()
	h := New(nil, io.Discard, io.Discard)
	h.reaper, h.cwd = reaper, cwd
	return h
}

func TestReaperClaimBeforeAndAfterExit(t *testing.T) {
	// exits before the claim
	c1 := exec.Command("true")
	if err := c1.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	select {
	case st := <-reaper.Claim(c1.Process.Pid):
		if st.Code != 0 {
			t.Fatalf("true exited %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an exit before the claim was lost")
	}
	// claimed first, then exits with a code; a signalled one names the signal
	c2 := exec.Command("sh", "-c", "exit 7")
	if err := c2.Start(); err != nil {
		t.Fatal(err)
	}
	ch2 := reaper.Claim(c2.Process.Pid)
	c3 := exec.Command("sleep", "30")
	if err := c3.Start(); err != nil {
		t.Fatal(err)
	}
	ch3 := reaper.Claim(c3.Process.Pid)
	_ = c3.Process.Kill()
	select {
	case st := <-ch2:
		if st.Code != 7 {
			t.Fatalf("exit 7 → %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no status for exit 7")
	}
	select {
	case st := <-ch3:
		if st.Signal != "SIGKILL" || st.Code != -1 {
			t.Fatalf("killed → %+v", st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no status for the killed child")
	}
}

func TestFsReadAndWriteScoping(t *testing.T) {
	dir := t.TempDir()
	h := newHost(t, filepath.Join(dir, "tile"))
	if err := os.MkdirAll(h.cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, rerr := h.readTextFile(acp.FsReadParams{Path: filepath.Join(dir, "outside.txt")})
	if rerr != nil || res.(acp.FsReadResult).Content != "l1\nl2\nl3\nl4\n" {
		t.Fatalf("read whole: %v %v", res, rerr)
	}
	line, limit := 2, 2
	res, _ = h.readTextFile(acp.FsReadParams{Path: filepath.Join(dir, "outside.txt"), Line: &line, Limit: &limit})
	if res.(acp.FsReadResult).Content != "l2\nl3\n" {
		t.Fatalf("line/limit: %q", res.(acp.FsReadResult).Content)
	}
	if _, rerr := h.readTextFile(acp.FsReadParams{Path: "relative.txt"}); rerr == nil || rerr.Code != acp.ErrInvalidParam {
		t.Fatalf("relative path: %v", rerr)
	}
	if _, rerr := h.readTextFile(acp.FsReadParams{Path: filepath.Join(dir, "nope")}); rerr == nil || rerr.Code != acp.ErrNoResource {
		t.Fatalf("missing file: %v", rerr)
	}
	// writes: inside the tile (parents created), never outside
	if _, rerr := h.writeTextFile(acp.FsWriteParams{Path: filepath.Join(h.cwd, "sub", "new.txt"), Content: "x"}); rerr != nil {
		t.Fatalf("write inside: %v", rerr)
	}
	if b, _ := os.ReadFile(filepath.Join(h.cwd, "sub", "new.txt")); string(b) != "x" {
		t.Fatal("the write did not land")
	}
	for _, p := range []string{filepath.Join(dir, "outside.txt"), filepath.Join(h.cwd, "..", "escape.txt"), filepath.Join(dir, "tile-sibling", "x")} {
		if _, rerr := h.writeTextFile(acp.FsWriteParams{Path: p, Content: "x"}); rerr == nil || rerr.Code != acp.ErrInvalidParam {
			t.Fatalf("write outside %s: %v", p, rerr)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("an escape landed")
	}
}

func TestTerminalLifecycle(t *testing.T) {
	h := newHost(t, t.TempDir())
	res, rerr := h.termCreate(acp.TermCreateParams{Command: "sh", Args: []string{"-c", "echo hi $FOO; exit 3"}, Env: []acp.EnvVar{{Name: "FOO", Value: "bar"}}})
	if rerr != nil {
		t.Fatal(rerr)
	}
	id := res.(acp.TermCreateResult).TerminalID
	w, rerr := h.termWait(acp.TermIDParams{TerminalID: id})
	if rerr != nil || w.(*acp.ExitStatus).ExitCode == nil || *w.(*acp.ExitStatus).ExitCode != 3 {
		t.Fatalf("wait: %+v %v", w, rerr)
	}
	out, _ := h.termOutput(acp.TermIDParams{TerminalID: id})
	if o := out.(acp.TermOutputResult); !strings.Contains(o.Output, "hi bar") || o.Truncated || o.ExitStatus == nil {
		t.Fatalf("output: %+v", o)
	}
	// a second one, killed; two live at once
	res, _ = h.termCreate(acp.TermCreateParams{Command: "sh", Args: []string{"-c", "echo start; sleep 30"}})
	id2 := res.(acp.TermCreateResult).TerminalID
	if id2 == id {
		t.Fatal("ids repeat")
	}
	time.Sleep(200 * time.Millisecond)
	if _, rerr := h.termKill(acp.TermIDParams{TerminalID: id2}); rerr != nil {
		t.Fatal(rerr)
	}
	w, _ = h.termWait(acp.TermIDParams{TerminalID: id2})
	if w.(*acp.ExitStatus).Signal == nil || *w.(*acp.ExitStatus).Signal != "SIGKILL" {
		t.Fatalf("killed: %+v", w)
	}
	out, _ = h.termOutput(acp.TermIDParams{TerminalID: id2})
	if !strings.Contains(out.(acp.TermOutputResult).Output, "start") {
		t.Fatal("output before the kill is kept")
	}
	if _, rerr := h.termRelease(acp.TermIDParams{TerminalID: id2}); rerr != nil {
		t.Fatal(rerr)
	}
	if _, rerr := h.termOutput(acp.TermIDParams{TerminalID: id2}); rerr == nil || rerr.Code != acp.ErrNoResource {
		t.Fatal("released terminal still answers")
	}
	// the output limit truncates from the start on a character boundary
	lim := int64(10)
	res, _ = h.termCreate(acp.TermCreateParams{Command: "sh", Args: []string{"-c", "printf 'aaaaaaaaéééé'"}, OutputByteLimit: &lim})
	id3 := res.(acp.TermCreateResult).TerminalID
	h.termWait(acp.TermIDParams{TerminalID: id3})
	out, _ = h.termOutput(acp.TermIDParams{TerminalID: id3})
	if o := out.(acp.TermOutputResult); !o.Truncated || len(o.Output) > 10 || !strings.HasSuffix(o.Output, "éééé") || strings.ContainsRune(o.Output, '�') {
		t.Fatalf("truncation: %+v", o)
	}
}

// The proxy: with `cat` as the agent, every daemon frame comes back as an
// agent frame; an fs request printed by the agent is answered by the host
// and the answer (echoed by cat) reaches the daemon as a response.
func TestProxyAndServedRequests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	var out safeBuf
	h := New(inR, &out, io.Discard)
	done := make(chan int, 1)
	go func() { done <- h.Run() }()
	enc := func(m *acp.Message) {
		var b bytes.Buffer
		_ = acp.Encode(&b, m)
		if _, err := inW.Write(b.Bytes()); err != nil {
			t.Error(err)
		}
	}
	fsReq := `{"jsonrpc":"2.0","id":"a1","method":"fs/read_text_file","params":{"sessionId":"s","path":"` + filepath.Join(dir, "f.txt") + `"}}`
	spawn, _ := json.Marshal(acp.SpawnParams{Argv: []string{"sh", "-c", "echo '" + fsReq + "'; echo 'garbage line'; cat"}, Env: []string{"PATH=" + os.Getenv("PATH")}, Cwd: dir})
	enc(&acp.Message{Method: acp.MXbinSpawn, Params: spawn})
	enc(&acp.Message{ID: json.RawMessage("1"), Method: acp.MInitialize, Params: json.RawMessage(`{"protocolVersion":1}`)})
	wait := func(pred func(m *acp.Message) bool) *acp.Message {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, line := range strings.Split(out.String(), "\n") {
				var m acp.Message
				if json.Unmarshal([]byte(line), &m) == nil && pred(&m) {
					return &m
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("not seen; daemon got:\n%s", out.String())
		return nil
	}
	wait(func(m *acp.Message) bool { return m.Method == acp.MXbinHello })
	// the initialize request went to cat and came back unchanged
	m := wait(func(m *acp.Message) bool { return m.Method == acp.MInitialize })
	if string(m.ID) != "1" || string(m.Params) != `{"protocolVersion":1}` {
		t.Fatalf("proxied frame changed: %+v", m)
	}
	// the agent's fs request was served (its echoed answer is a response with id a1)
	m = wait(func(m *acp.Message) bool { return m.IsResponse() && string(m.ID) == `"a1"` })
	var rd acp.FsReadResult
	if json.Unmarshal(m.Result, &rd) != nil || rd.Content != "content\n" {
		t.Fatalf("fs answer: %s", m.Result)
	}
	// the garbage line reached the daemon as a log note
	m = wait(func(m *acp.Message) bool { return m.Method == acp.MXbinLog })
	if !strings.Contains(string(m.Params), "garbage line") {
		t.Fatalf("log: %s", m.Params)
	}
	// the daemon goes away: the host ends and the agent with it
	inW.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host did not end when the daemon left")
	}
	if h.cmd.Process != nil && h.cmd.Process.Signal(nil) == nil {
		// Signal(nil) errors once the process is gone (reaped)
		time.Sleep(300 * time.Millisecond)
		if err := h.cmd.Process.Signal(nil); err == nil {
			t.Fatal("the agent outlived the host")
		}
	}
}

// The agent exits by itself: the host returns its code.
func TestAgentExitCode(t *testing.T) {
	inR, inW := io.Pipe()
	defer inW.Close()
	h := New(inR, io.Discard, io.Discard)
	done := make(chan int, 1)
	go func() { done <- h.Run() }()
	spawn, _ := json.Marshal(acp.SpawnParams{Argv: []string{"sh", "-c", "exit 5"}, Env: []string{"PATH=" + os.Getenv("PATH")}, Cwd: t.TempDir()})
	var b bytes.Buffer
	_ = acp.Encode(&b, &acp.Message{Method: acp.MXbinSpawn, Params: spawn})
	_, _ = inW.Write(b.Bytes())
	select {
	case code := <-done:
		if code != 5 {
			t.Fatalf("exit code %d, want 5", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host did not end with the agent")
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
