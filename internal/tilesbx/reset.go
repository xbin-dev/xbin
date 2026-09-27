package tilesbx

// reset.go — reset and rebase (plans/tile-sandbox-runtime.md §3.4). Both
// run in the sandbox's flight, with no run holding its state: a running
// sandbox is stopped first (its execs end killed), its state is reset or
// re-pinned under its lock, and it is started again, so it is running
// afterwards; a stopped one stays stopped. Both keep its snapshots (each
// pins its own base) and both repair `error`.
//
//   - Reset puts cur/ aside — renamed to .trash/<uid>.<rand>, removed later
//     by the confined remover (trash.go) — and clears Def.base, so the next
//     start pins the current base into a fresh cur/. Nothing of the old
//     state is read or walked as xbind. It is idempotent.
//   - Rebase keeps cur/ and re-stamps its base with the current one (which
//     may break what a package manager installed); Def.base follows. An
//     upper written by another overlay flavour can only be reset.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/layers"
)

// Reset stops k's sandbox if it runs, puts its state aside for the
// confined remover, clears its base pin, and starts it again if it ran.
func (m *Manager) Reset(k Key, name string) error { return m.restage(k, name, false) }

// Rebase stops k's sandbox if it runs, pins its kept state to the current
// base, and starts it again if it ran.
func (m *Manager) Rebase(k Key, name string) error { return m.restage(k, name, true) }

// restage is Reset (rebase false) and Rebase, in the sandbox's flight.
func (m *Manager) restage(k Key, name string, rebase bool) error {
	b, err := m.boxFor(k, name)
	if err != nil {
		return err
	}
	b.flight.Lock()
	defer b.flight.Unlock()
	m.mu.Lock()
	d, ok := m.defs.get(k, name)
	switch {
	case !ok || m.live[k][name] != b:
		m.mu.Unlock()
		return refuse(RefNotFound, "no sandbox %q", name)
	case b.state == StateCreating:
		st, detail := b.state, b.detail
		m.mu.Unlock()
		return &Error{Refusal: RefState, State: st, Msg: "sandbox " + name + " is " + st + ": " + detail}
	}
	up := b.run != nil
	restart := up && b.state == StateRunning // not one a stop that timed out left stopping
	m.mu.Unlock()
	if up {
		if err := m.stopLocked(b, ""); err != nil {
			return err
		}
	}
	err = m.restageState(k, d, rebase)
	m.mu.Lock()
	if m.live[k][name] == b && b.run == nil {
		var e *Error
		switch {
		case err == nil:
			b.state, b.detail = StateStopped, "" // repaired, if it was in error
		case errors.As(err, &e) && e.State == StateError:
			b.state, b.detail = StateError, e.Msg
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if !rebase {
		m.measureSoon(k, d) // its old cur/ no longer counts against the tile's disk
	}
	if restart {
		return m.startLocked(k, name, b)
	}
	return nil
}

// restageState resets or re-pins d's state on disk, under its lock: an
// earlier run's processes (an orphan's) must be gone first.
func (m *Manager) restageState(k Key, d *Def, rebase bool) error {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) { // it never started
		if !rebase && d.Base != "" {
			return m.recordBase(k, d, "")
		}
		if rebase && d.Base != "" {
			return &Error{Refusal: RefState, State: StateError, Msg: "its state is missing — reset it to start from a fresh base"}
		}
		return nil
	}
	lock, err := m.lockRun(k, d, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	cur := filepath.Join(dir, "cur")
	if !rebase {
		// The pin is cleared first: a rename that then fails leaves cur/
		// with its stamps, which the next start keeps pinned as before.
		if d.Base != "" {
			if err := m.recordBase(k, d, ""); err != nil {
				return err
			}
		}
		to, err := m.trashCur(k, d, cur)
		if err != nil {
			return err
		}
		if to != "" {
			m.trash.put(to)
		}
		return nil
	}
	fi, err := os.Lstat(cur)
	switch {
	case errors.Is(err, fs.ErrNotExist) && d.Base != "":
		return &Error{Refusal: RefState, State: StateError, Msg: "its state is missing — reset it to start from a fresh base"}
	case errors.Is(err, fs.ErrNotExist):
		return nil // nothing pinned yet: the next start pins the current base
	case err != nil:
		return err
	case !fi.IsDir():
		return &Error{Refusal: RefState, State: StateError, Msg: "its state dir isn't a directory — reset it"}
	case m.rootfs == "":
		return refuse(RefUnavailable, "tile sandboxes need a base rootfs (xbind --isolate --rootfs)")
	}
	v := layers.BaseVersion(m.rootfs)
	if err := layers.Stamp(cur, layers.Stamps{Base: v}); err != nil {
		return err
	}
	return m.recordBase(k, d, v) // were it to fail, the next start's pin records the stamp
}

// trashCur renames a sandbox's cur/ to its key's .trash/<uid>.<rand>: one
// rename of an xbind-created dir, nothing inside it read. "" when there is
// no cur/.
func (m *Manager) trashCur(k Key, d *Def, cur string) (string, error) {
	if _, err := os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	trash, err := m.TrashDir(k)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	to := filepath.Join(trash, d.UID+"."+randSuffix())
	if err := os.Rename(cur, to); err != nil {
		return "", err
	}
	return to, nil
}

// recordBase sets d's Def.base to v (it mirrors cur/'s stamp, §1.3; no
// version bump: it's the runtime's), in the store and in d.
func (m *Manager) recordBase(k Key, d *Def, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cd, ok := m.defs.get(k, d.Name)
	if !ok || cd.UID != d.UID {
		return refuse(RefNotFound, "no sandbox %q", d.Name)
	}
	if cd.Base != v {
		cd.Base = v
		if err := m.defs.put(k, cd); err != nil {
			return err
		}
	}
	d.Base = v
	return nil
}
