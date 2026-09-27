//go:build linux

package host

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/fusefs"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// A VM that doesn't do what it should — a backend that never listens —
// otherwise leaves nothing to go on: the guest is a separate kernel, and the
// shim's stderr (the backend's log) shows only its console when it dies.
// xbind sends the shim SIGQUIT when it gives up on a backend's health check;
// the shim then writes a dump to its stderr and quits (128+SIGQUIT):
//
//   - where the shim is (booting, configuring, the session started,
//     listening) and for how long;
//   - each export's file traffic: requests the guest still waits on (the
//     operation, the path, the guest pid, for how long) and when it last
//     asked anything;
//   - the agent's own report (guest/dump_linux.go): sessions, every guest
//     process and thread with its wait channel and kernel stack, the agent's
//     goroutines — or that the agent didn't answer (its runtime is stuck);
//   - the shim's goroutines when a file request is stuck on the host side;
//   - the tail of the guest console.

// dumpAnswer is how long the shim waits for the agent's report.
const dumpAnswer = 3 * time.Second

func (s *shim) watchQuit() {
	q := make(chan os.Signal, 1)
	signal.Notify(q, unix.SIGQUIT)
	go func() {
		<-q
		s.writeDump()
		s.restore()
		os.Exit(128 + int(unix.SIGQUIT))
	}()
}

func (s *shim) writeDump() {
	now := time.Now()
	var b strings.Builder
	s.mu.Lock()
	phase, since, served, agent := s.phase, now.Sub(s.phaseAt), append([]*fusefs.FS(nil), s.files...), s.agent
	s.mu.Unlock()
	accel := "Firecracker (KVM)"
	if s.hs.Emulated() {
		accel = "QEMU (emulated)"
	}
	fmt.Fprintf(&b, "--- VM dump (SIGQUIT) ---\n%s, %d vCPUs, %d MiB; up %s; %s (for %s)\n",
		accel, s.hs.VCPUs, s.hs.MemMiB, now.Sub(s.started).Round(time.Millisecond), phase, since.Round(time.Millisecond))
	stalled := false
	for _, f := range served {
		fmt.Fprintf(&b, "file server %s: %s\n", f.Root(), f.Report(now))
		stalled = stalled || f.Stalled(now) > time.Second
	}
	if len(served) == 0 {
		b.WriteString("file server: no exports attached\n")
	}
	switch {
	case agent == nil:
		b.WriteString("guest agent: not connected yet\n")
	case s.hs.Resident: // its control lines are bounded by MaxEvent, and a dump can outgrow them
		b.WriteString("guest agent: not asked (a resident VM's control lines are bounded; a dump would cut them)\n")
	default:
		wait := s.slow(dumpAnswer)
		if err := agent.Send(proto.Msg{Op: "dump"}); err != nil {
			fmt.Fprintf(&b, "guest agent: can't ask it (%v)\n", err)
			break
		}
		select {
		case d := <-s.dumps:
			fmt.Fprintf(&b, "--- guest ---\n%s", d)
		case <-time.After(wait):
			fmt.Fprintf(&b, "guest agent: no answer within %s — its control loop or its runtime is stuck (the console below may say more)\n", wait)
		}
	}
	if stalled {
		buf := make([]byte, 64<<10)
		n := runtime.Stack(buf, true)
		fmt.Fprintf(&b, "--- shim goroutines (a file request is stuck on the host) ---\n%s\n", buf[:n])
	}
	if tail := s.serial.tail(8 << 10); tail != "" {
		fmt.Fprintf(&b, "--- VM console ---\n%s\n", tail)
	}
	b.WriteString("--- end of VM dump ---\n")
	_, _ = os.Stderr.WriteString(crlf(b.String(), s.tty))
}
