package tilesbx

// restore.go — putting a snapshot back (plans/tile-sandbox-runtime.md
// §3.9). A restore brings a sandbox's state back as it was, base included:
// the snapshot is copied into a staged cur (tmp/<rand>/, the snapshot's
// stamps on it) and swapped with the live cur/ in one
// renameat2(RENAME_EXCHANGE) — its upper and its work dir together, or its
// disk — so a crash leaves the old state or the new one, never a mix; the
// old cur/ goes to .trash for the confined remover, and Def.base follows
// the snapshot's base. Like a reset it stops the sandbox first (its execs
// end killed) and starts it again if it ran; it repairs `error`. Only a
// mode mismatch, another overlay flavour (namespace) or a base no longer
// installed is refused, as `invalid`.

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/layers"
)

// checkRestorable refuses state (its stamps st) a sandbox of mode can't be
// put on — a restore's snapshot, a clone's source: another overlay flavour
// (namespace), a base no longer installed. what names it.
func (m *Manager) checkRestorable(mode string, st layers.Stamps, what string) error {
	if m.rootfs == "" {
		return refuse(RefUnavailable, "tile sandboxes need a base rootfs (xbind --isolate --rootfs)")
	}
	if mode == ModeNamespace && st.Overlay != "" && st.Overlay != overlayFlavour() {
		return refuse(RefInvalid, "%s was written by the %s overlay, and this host mounts %s: its upper can't be read here", what, st.Overlay, overlayFlavour())
	}
	if st.Base != "" {
		if _, ok := layers.ResolveBase(m.rootfs, st.Base); !ok {
			return refuse(RefInvalid, "%s is pinned to the base image %q, which is no longer installed", what, st.Base)
		}
	}
	return nil
}

// Restore starts putting k's sandbox back to its snapshot sid: it answers
// the job doing it, or a refusal.
func (m *Manager) Restore(k Key, name, sid string) (*copyJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok {
		return nil, refuse(RefNotFound, "no sandbox %q", name)
	}
	b := m.boxLocked(k, name)
	if e := busyLocked(name, b); e != nil {
		return nil, e
	}
	if e := pendingLocked(d, b); e != nil {
		return nil, e
	}
	var sm *snapMeta
	for _, s := range m.snapsLocked(k, d, b) {
		if s.ID == sid {
			sm = &s
		}
	}
	if sm == nil {
		return nil, refuse(RefNotFound, "sandbox %q has no snapshot %s", name, sid)
	}
	if sm.Mode != d.Mode {
		return nil, refuse(RefInvalid, "snapshot %s is of a %s sandbox; %q runs in %s mode", sid, sm.Mode, name, d.Mode)
	}
	if err := m.checkRestorable(d.Mode, layers.Stamps{Base: sm.Base, Overlay: sm.Overlay}, "snapshot "+sid); err != nil {
		return nil, err
	}
	job := newJob()
	b.busy, b.copying = "busy: restoring snapshot "+sid, job
	go m.restoreSnapshot(k, d, b, *sm, job)
	return job, nil
}

// restoreSnapshot is a restore's job, in the sandbox's flight.
func (m *Manager) restoreSnapshot(k Key, d *Def, b *box, sm snapMeta, job *copyJob) {
	var err error
	b.flight.Lock()
	defer b.flight.Unlock()
	defer func() { m.endCopy(b, job, err) }() // before the flight is let go
	restart := false
	if restart, err = m.stopForCopy(k, d, b); err != nil {
		return
	}
	err = m.restoreState(k, d, sm)
	m.mu.Lock()
	if err == nil && m.live[k][d.Name] == b && b.run == nil {
		b.state, b.detail = StateStopped, "" // repaired, if it was in error
	}
	m.mu.Unlock()
	if err != nil {
		slog.Warn("tile sandbox: a restore failed; its state is as it was", "tile", k.Tile, "sandbox", d.Name, "snapshot", sm.ID, "err", err)
	} else {
		slog.Info("tile sandbox: snapshot restored", "tile", k.Tile, "sandbox", d.Name, "snapshot", sm.ID)
		m.measureSoon(k, d) // its old cur/ no longer counts
	}
	if restart {
		if serr := m.startLocked(k, d.Name, b); serr != nil {
			slog.Info("tile sandbox: starting again after a copy", "tile", k.Tile, "sandbox", d.Name, "err", serr)
		}
	}
}

// restoreState stages snapshot sm as a new cur and swaps it in, under the
// sandbox's lock; the old cur/ goes to .trash.
func (m *Manager) restoreState(k Key, d *Def, sm snapMeta) error {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err
	}
	lock, err := m.lockRun(k, d, dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	tmp, err := m.stage(dir)
	if err != nil {
		return err
	}
	swapped := false
	defer func() {
		if !swapped {
			m.discard(k, d, tmp)
		}
	}()
	if err := m.copyLayer(m.copies.get(), d.Mode, filepath.Join(dir, "snapshots", sm.ID), tmp, layers.Stamps{Base: sm.Base, Overlay: sm.Overlay}); err != nil {
		return err
	}
	cur := filepath.Join(dir, layers.CurDir)
	switch _, err := os.Lstat(cur); {
	case errors.Is(err, fs.ErrNotExist): // never ran, reset, or its state went missing
		if err := os.Rename(tmp, cur); err != nil {
			return err
		}
		swapped = true
	case err != nil:
		return err
	default:
		if err := m.exchange(tmp, cur); err != nil {
			return fmt.Errorf("swapping its state in: %w", err)
		}
		swapped = true
		m.discard(k, d, tmp) // the old cur/, now at tmp
	}
	// Def.base follows the stamp now in cur/ (were this to fail, the next
	// start's pin records it)
	return m.recordBase(k, d, sm.Base)
}
