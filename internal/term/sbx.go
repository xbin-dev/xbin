package term

// sbx.go — the terminal manager's side of the sandbox registry
// (internal/sbx, D112): a session is listed while its process lives (an
// agent restart unlists the old one before the new one is listed, so
// nothing counts twice), and what the sandbox layer refused or failed at is
// recorded. The session's cgroup leaf — a VM's, sized from its reservation;
// a restricted user's (D17d) — is made and dropped here too.

import (
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// sbxLaunch is what a session's setup learnt (applyVM fills a VM's; the
// session's openOpts carries it back).
type sbxLaunch struct {
	memMiB, vcpus int
	emulated      bool
	disk          string
	baseMoved     string // the base the tile's layer moved off at this start ("" = none; claimLayer, D174)
}

// setVM records a VM session's sizes and disk (applyVM), field by field:
// what the setup learnt before it — a base move — stays (it once replaced
// the whole struct, and a VM terminal's move went unsaid). nil-safe.
func (l *sbxLaunch) setVM(memMiB, vcpus int, emulated bool, disk string) {
	if l == nil {
		return
	}
	l.memMiB, l.vcpus, l.emulated, l.disk = memMiB, vcpus, emulated, disk
}

func sbxKind(kind string) sbx.Kind {
	if kind == KindAgent {
		return sbx.Agent
	}
	return sbx.Terminal
}

// sbxMode is how a session is isolated here.
func (m *Manager) sbxMode(isVM bool) sbx.Mode {
	switch {
	case isVM:
		return sbx.VM
	case m.Isolate:
		return sbx.Namespace
	}
	return sbx.Host
}

// startLeaf puts a new session's process into its cgroup leaf — before a VM
// can touch guest memory — and returns the leaf ("" = none): a VM's is
// capped at its guest memory plus the VMM's overhead, a restricted user's
// gets the D17d limits. Admin terminals stay unlimited (dev builds are
// hungry).
func (m *Manager) startLeaf(o openOpts, id string, p *os.Process) string {
	if m.Cgroup == nil || p == nil {
		return ""
	}
	leaf := "term-" + id
	switch {
	case o.vm && o.launch != nil:
		m.Cgroup.AddMem(leaf, p.Pid, int64(o.launch.memMiB+m.VM.OverheadMiB())<<20)
	case o.restricted:
		m.Cgroup.Add(leaf, p.Pid)
	default:
		return ""
	}
	return leaf
}

func (m *Manager) dropLeaf(leaf string) {
	if leaf != "" {
		m.Cgroup.Remove(leaf)
	}
}

// register lists a started session until the returned remove.
func (m *Manager) register(s *Session, o openOpts, leaf string) func() {
	if m.Sandboxes == nil {
		return func() {}
	}
	e := sbx.Entry{ID: s.ID, Kind: sbxKind(s.kind), Tile: s.Cwd, User: s.homeKey, Mode: m.sbxMode(o.vm),
		Started: s.born, Leaf: leaf, Net: s.Net, Restricted: o.restricted}
	if s.cmd != nil && s.cmd.Process != nil {
		e.PID = s.cmd.Process.Pid
	}
	if s.agent != nil {
		e.Label = s.agent.provider.ID
	}
	if l := o.launch; o.vm && l != nil {
		e.MemMiB, e.VCPUs, e.Disk, e.Accel = l.memMiB, l.vcpus, l.disk, sbx.KVM
		if l.emulated {
			e.Accel = sbx.Emulate
		}
	}
	return m.Sandboxes.Add(e)
}

// sbxFail records a session whose sandbox was refused or couldn't start.
func (m *Manager) sbxFail(o openOpts, rel string, err error) {
	m.Sandboxes.Fail(sbx.Failure{Kind: sbxKind(o.kind), Tile: rel, User: o.homeKey, Mode: m.sbxMode(o.vm),
		Stage: sbx.StageOf(err, sbx.Start), Error: err.Error()})
}

// sbxExited records a session whose sandbox layer died: the VM shim's
// failure exit (125), or the sandbox init's (127) before the session was
// ten seconds old. tail is its last output (where the init's error went).
func (m *Manager) sbxExited(s *Session, isVM bool, tail string) {
	mode := m.sbxMode(isVM)
	ps := s.cmd.ProcessState
	if ps == nil || mode == sbx.Host {
		return
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return // closed or killed
	}
	var why string
	switch code := ps.ExitCode(); {
	case code == 125 && isVM:
		why = "the VM exited (125)"
	case code == 127 && time.Since(s.born) < 10*time.Second:
		why = "the sandbox couldn't start the session (127)"
	default:
		return
	}
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	if tail != "" {
		why += ": " + tail
	}
	m.Sandboxes.Fail(sbx.Failure{Kind: sbxKind(s.kind), Tile: s.Cwd, User: s.homeKey, Mode: mode, Stage: sbx.Exit,
		Error: errors.New(why).Error()})
}
