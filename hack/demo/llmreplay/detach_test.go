package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestMain lets a test start this command: with LLMREPLAY_RUN_MAIN=1 the
// test binary is llmreplay (detachSelf runs its own executable again).
func TestMain(m *testing.M) {
	if os.Getenv("LLMREPLAY_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// -detach starts the server in a session of its own and returns once it
// listens, printing nothing on stdout (a Claude Code SessionStart hook's
// stdout would land in the model's context); run again, it leaves the
// running one alone.
func TestDetach(t *testing.T) {
	cas := filepath.Join(t.TempDir(), "claude.jsonl")
	writeCassette(t, cas, Exchange{Lane: "claude", Method: "POST", Path: "/v1/messages",
		ReqBody: json.RawMessage(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`),
		Status:  200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: `{"type":"message","content":[]}`})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	logPath := filepath.Join(t.TempDir(), "replay.log")
	t.Setenv("LLMREPLAY_RUN_MAIN", "1")
	args := []string{"-listen", addr, "-log", logPath, "-cassette", cas, "-detach"}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	first := detachSelf("replay", options{listen: addr}, logPath, args)
	again := detachSelf("replay", options{listen: addr}, logPath, args)
	os.Stdout = stdout
	w.Close()
	said, _ := io.ReadAll(r)

	st, err := http.Get("http://" + addr + "/_llmreplay/status")
	if err != nil {
		t.Fatalf("not listening after -detach: %v (log: %s)", err, readFile(logPath))
	}
	var status statusOut
	_ = json.NewDecoder(st.Body).Decode(&status)
	st.Body.Close()
	if status.PID > 0 && status.PID != os.Getpid() {
		t.Cleanup(func() { _ = syscall.Kill(status.PID, syscall.SIGTERM) })
	}
	if first != 0 || again != 0 || status.Mode != "replay" || status.PID == os.Getpid() {
		t.Fatalf("detach: %d then %d, status %+v (log: %s)", first, again, status, readFile(logPath))
	}
	if len(said) != 0 {
		t.Fatalf("-detach wrote to stdout: %q", said)
	}
	got := send(t, "http://"+addr+"/lane/claude/v1/messages", map[string]string{"Anthropic-Version": "2023-06-01"},
		json.RawMessage(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	if got.body != `{"type":"message","content":[]}` {
		t.Fatalf("the detached replayer answered %d %q", got.status, got.body)
	}
	if lg := readFile(logPath); !strings.Contains(lg, "llmreplay replay on") || strings.Count(lg, "llmreplay replay on") != 1 {
		t.Fatalf("one server should have started:\n%s", lg)
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
