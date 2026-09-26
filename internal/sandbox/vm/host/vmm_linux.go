//go:build linux

package host

import (
	"os"
	"os/exec"
	"strings"
	"sync"
)

// startVMM starts the VM monitor — Firecracker, or QEMU for an emulated VM
// — as the shim's child. Its stdin is /dev/null, never the PTY (a VMM would
// claim it as the serial console), and its output (the guest console and
// the VMM's log) goes to the ring.
func (s *shim) startVMM(cmd *exec.Cmd) error {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	cmd.Stdout, cmd.Stderr = s.serial, s.serial
	if s.hs.Debug {
		s.serial.echo = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.vmm = cmd
	s.vmmDone = make(chan struct{})
	go func() { _ = cmd.Wait(); close(s.vmmDone) }()
	return nil
}

func (s *shim) killVMM() {
	if s.vmm != nil && s.vmm.Process != nil {
		_ = s.vmm.Process.Kill()
		<-s.vmmDone
	}
	if s.vsockdDone != nil {
		_ = s.vsockd.Process.Kill()
		<-s.vsockdDone
	}
}

func (s *shim) vmmExited() bool {
	if s.vmmDone == nil {
		return false
	}
	select {
	case <-s.vmmDone:
		return true
	default:
		return false
	}
}

// ring keeps the last n bytes written.
type ring struct {
	mu   sync.Mutex
	buf  []byte
	n    int
	echo interface{ Write([]byte) (int, error) }
}

func newRing(n int) *ring { return &ring{n: n} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.n {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.n:]...)
	}
	if r.echo != nil {
		_, _ = r.echo.Write([]byte(strings.ReplaceAll(string(p), "\n", "\r\n")))
	}
	return len(p), nil
}

func (r *ring) tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buf
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}
