package tilesbx

// state.go — a sandbox's state on disk at a start (plans/tile-sandbox-runtime.md
// §1.3, §7 steps 2–3): the lock an earlier run's processes may still hold,
// and cur/ with its pinned base and overlay flavour.

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// errStillEnding is a start (a reset, a rebase) refused because an earlier
// run of the sandbox still holds it.
func errStillEnding() *Error {
	return &Error{Refusal: RefState, State: StateStopping, Msg: "an earlier run of this sandbox still holds its state (it is still ending): try again", RetryAfter: 5 * time.Second}
}

// lockState takes a sandbox's state lock (§7 step 2): <state>/lock,
// flocked exclusive. A lock another holder has — the processes of an
// earlier run, still dying — is waited for, at most lockWait; then the
// start is refused. The fd goes to the sandbox as Spec.Lock.
func lockState(dir string) (*os.File, error) { return lockStateAnd(dir, nil, lockWait) }

// lockStateAnd is lockState that waits at most wait, and also, within it,
// until busy (when given) reports the earlier run gone.
func lockStateAnd(dir string, busy func() bool, wait time.Duration) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	locked := false
	for {
		if !locked {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err != nil && !errors.Is(err, syscall.EWOULDBLOCK) {
				f.Close()
				return nil, err
			}
			locked = err == nil
		}
		if locked && (busy == nil || !busy()) {
			return f, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, errStillEnding()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lockRun takes k's sandbox's state lock for a start, a reset or a
// rebase: the lock, and its cgroup leaf empty — an orphan from a crashed
// xbind may hold either while it dies (the boot's sweep killed its leaf;
// a process stuck in the kernel can outlive that for a moment).
func (m *Manager) lockRun(k Key, d *Def, dir string) (*os.File, error) {
	leaf := Leaf(k, d.Name)
	return lockStateAnd(dir, func() bool { return m.cg.Populated(leaf) }, m.lockWait)
}

// pin readies a sandbox's state for a start (§7 step 3): cur/ made for a
// sandbox that has none yet, its base and overlay flavour pinned
// (internal/layers), Def.base kept in step. It returns the rootfs dir its
// pinned base is served from. A missing cur/ for a sandbox that had state,
// a base that is no longer installed, or an upper another overlay flavour
// wrote puts it in error — never a silently blank root.
func (m *Manager) pin(k Key, d *Def, cur string) (string, error) {
	if m.rootfs == "" {
		return "", refuse(RefUnavailable, "tile sandboxes need a base rootfs (xbind --isolate --rootfs)")
	}
	fi, err := os.Lstat(cur)
	switch {
	case os.IsNotExist(err) && d.Base != "":
		return "", &Error{Refusal: RefState, State: StateError, Msg: "its state is missing — reset it to start from a fresh base"}
	case os.IsNotExist(err):
		if err := os.Mkdir(cur, 0o700); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !fi.IsDir():
		return "", &Error{Refusal: RefState, State: StateError, Msg: "its state dir isn't a directory — reset it"}
	}
	for _, sub := range []struct {
		name string
		mode os.FileMode
	}{{"upper", 0o755}, {"work", 0o700}} { // the upper is the sandbox's /: others traverse it
		if err := os.Mkdir(filepath.Join(cur, sub.name), sub.mode); err != nil && !os.IsExist(err) {
			return "", err
		}
	}
	flavour := ""
	if d.Mode == ModeNamespace {
		flavour = overlayFlavour()
	}
	st, base, err := pinLayer(cur, m.rootfs, flavour)
	if err != nil {
		return "", &Error{Refusal: RefState, State: StateError, Msg: err.Error()}
	}
	if st.Base != d.Base {
		if err := m.recordBase(k, d, st.Base); err != nil {
			slog.Warn("tile sandbox: recording its base", "tile", k.Tile, "sandbox", d.Name, "err", err)
		}
		d.Base = st.Base
	}
	return base, nil
}
