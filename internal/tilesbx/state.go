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

// lockState takes a sandbox's state lock (§7 step 2): <state>/lock,
// flocked exclusive. A lock another holder has — the processes of an
// earlier run, still dying — is waited for, at most lockWait; then the
// start is refused. The fd goes to the sandbox as Spec.Lock.
func lockState(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, &Error{Refusal: RefState, State: StateStopping, Msg: "an earlier run of this sandbox still holds its state (it is still ending): try again", RetryAfter: 5 * time.Second}
			}
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
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
		m.mu.Lock()
		if cd, ok := m.defs.get(k, d.Name); ok && cd.UID == d.UID {
			cd.Base = st.Base // mirrors the stamp (§1.3); no version bump: it's the runtime's
			if err := m.defs.put(k, cd); err != nil {
				slog.Warn("tile sandbox: recording its base", "tile", k.Tile, "sandbox", d.Name, "err", err)
			}
		}
		m.mu.Unlock()
		d.Base = st.Base
	}
	return base, nil
}
