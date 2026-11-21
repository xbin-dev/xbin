//go:build linux && integration

package vm

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// A VM terminal's resize reaches the guest's PTY before the keys typed after
// it, as xbind's terminal sends them: TIOCSWINSZ on the host PTY, then the
// keys at once, with only a SIGWINCH to tell the shim — none at all here
// (the shim's PTY isn't its controlling terminal), the slowest a SIGWINCH
// can be. The shim sees the size change on the read that carries the keys.
func TestVMSandboxTerminalResize(t *testing.T) {
	m, ws := harness(t)
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no PTY: %v", err)
	}
	defer master.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 10, Cols: 20}); err != nil {
		t.Fatal(err)
	}
	spec := &sandbox.Spec{
		Lower:   []string{m.Rootfs},
		Binds:   []sandbox.Bind{{Src: ws, Dst: ws, RO: true, Layout: true}},
		Entry:   "/bin/sh",
		Argv:    []string{"sh", "-c", `stty size; echo ready; read a; stty size; echo "got:$a"; exit 4`},
		Env:     []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:     ws,
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	if err := m.Apply(context.Background(), spec, Options{MemMiB: 512, TTY: true}); err != nil {
		t.Fatal(err)
	}
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close() // the shim holds its own
	if err := h.SetupUserns(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ended := false
	defer func() {
		if !ended {
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	var mu sync.Mutex
	var out strings.Builder
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := master.Read(b)
			mu.Lock()
			out.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	await := func(want string) string {
		t.Helper()
		for deadline := time.Now().Add(vmTimeout); ; time.Sleep(20 * time.Millisecond) {
			mu.Lock()
			s := out.String()
			mu.Unlock()
			if strings.Contains(s, want) {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %q from the VM terminal within %s: %q", want, vmTimeout, s)
			}
		}
	}
	await("ready")
	if err := pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("hi\r")); err != nil {
		t.Fatal(err)
	}
	s := await("got:hi")
	if after := s[strings.Index(s, "ready"):]; !strings.Contains(after, "40 100") {
		t.Fatalf("the keys overtook the resize: %q", s)
	}
	select {
	case err := <-done:
		ended = true
		if ee, ok := err.(interface{ ExitCode() int }); !ok || ee.ExitCode() != 4 {
			t.Fatalf("the VM terminal ended %v, want exit 4", err)
		}
	case <-time.After(vmTimeout):
		t.Fatalf("the VM terminal didn't end within %s", vmTimeout)
	}
}
