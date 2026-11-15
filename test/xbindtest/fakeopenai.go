//go:build linux

package xbindtest

import (
	"bytes"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// FakeOpenAI builds hack/fakeopenai — the scripted OpenAI-compatible server
// the UI harness uses — and runs it on a free loopback port until the test
// ends: an upstream for the LLM connector that calls no real provider. It
// answers its base URL (http://127.0.0.1:<port>; the API is under /v1).
func FakeOpenAI(t testing.TB, a *Assets) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	bin := filepath.Join(t.TempDir(), "fakeopenai")
	build := exec.Command("go", "build", "-o", bin, "./hack/fakeopenai")
	build.Dir = a.Repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeopenai: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "-addr", addr)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	url := "http://" + addr
	Eventually(t, 20*time.Second, "fakeopenai answering", func() (bool, string) {
		r, err := http.Get(url + "/v1/models")
		if err != nil {
			return false, err.Error()
		}
		r.Body.Close()
		return r.StatusCode == 200, r.Status
	})
	return url
}
