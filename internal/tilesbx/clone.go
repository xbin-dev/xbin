package tilesbx

// clone.go — a clone is POST /sandboxes with from: {sandbox, snapshot?}
// (plans/tile-sandbox-runtime.md §3.9): a new sandbox of the same key and
// mode whose state starts as a copy of its source's — of a snapshot,
// whatever the source does meanwhile, or of its cur/, only while the
// source is stopped (a live upper or disk would copy torn, and an implicit
// snapshot would stop the source's work from inside someone else's
// create). It takes the base its source's state is pinned to.
//
// The definition is stored at once, counted against perTile.max under the
// definitions mutex, with Def.pending "clone"; the sandbox is `creating`
// while its copy runs off the request (staged in its own tmp/, renamed to
// its cur/ whole) and every call but GET, the list and DELETE answers 409.
// It ends `stopped` (`running` with start), or `error` when the copy
// failed — and after a restart that cut it short (the boot, sweep.go):
// only a DELETE helps then. The source is held meanwhile: a snapshot being
// copied isn't deleted, and a source copied from its cur/ is busy.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/layers"
)

// cloneSource is what a clone copies.
type cloneSource struct {
	def     *Def
	b       *box
	sid     string        // a snapshot's id; "" = its cur/
	dir     string        // what is copied ("" = nothing: the source never ran)
	st      layers.Stamps // its stamps: the clone's base and flavour
	bytes   int64         // what it takes on disk, as last measured
	diskGiB int           // a VM disk's size
}

// cloneSource finds and checks a clone's source for d (validated, its mode
// the request's). Callers hold m.mu.
func (m *Manager) cloneSource(k Key, from *From, d *Def) (*cloneSource, error) {
	if err := validName(from.Sandbox); err != nil {
		return nil, refuse(RefInvalid, "from.sandbox: %v", err)
	}
	if from.Snapshot != "" && !snapIDRE.MatchString(from.Snapshot) {
		return nil, refuse(RefInvalid, "from.snapshot %q must match %s", from.Snapshot, snapIDRE)
	}
	sd, ok := m.defs.get(k, from.Sandbox)
	if !ok {
		return nil, refuse(RefNotFound, "no sandbox %q to clone", from.Sandbox)
	}
	if sd.Mode != d.Mode {
		return nil, refuse(RefInvalid, "a clone runs in its source's mode: %q is a %s sandbox", sd.Name, sd.Mode)
	}
	sb := m.boxLocked(k, sd.Name)
	if sd.Pending != "" {
		return nil, &Error{Refusal: RefState, State: sb.state, RetryAfter: busyRetry, Msg: fmt.Sprintf("sandbox %q is a clone still being made (or one that failed): it can't be cloned", sd.Name)}
	}
	dir, err := m.StateDir(k, sd)
	if err != nil {
		return nil, err
	}
	src := &cloneSource{def: sd, b: sb, sid: from.Snapshot}
	if from.Snapshot != "" {
		var sm *snapMeta
		for _, s := range m.snapsLocked(k, sd, sb) {
			if s.ID == from.Snapshot {
				sm = &s
			}
		}
		switch {
		case sm == nil && sb.pendingSnap != nil && sb.pendingSnap.ID == from.Snapshot:
			return nil, &Error{Refusal: RefState, State: sb.state, RetryAfter: busyRetry, busy: true, Msg: fmt.Sprintf("snapshot %s of %q is still being taken: try again once it is done", from.Snapshot, sd.Name)}
		case sm == nil:
			return nil, refuse(RefNotFound, "sandbox %q has no snapshot %s", sd.Name, from.Snapshot)
		case sm.Mode != d.Mode:
			return nil, refuse(RefInvalid, "snapshot %s is of a %s sandbox", sm.ID, sm.Mode)
		}
		src.dir = filepath.Join(dir, "snapshots", sm.ID)
		src.st, src.bytes, src.diskGiB = layers.Stamps{Base: sm.Base, Overlay: sm.Overlay}, sm.Bytes, sm.DiskGiB
	} else {
		if sb.busy != "" || sb.run != nil || sb.state != StateStopped {
			st := sb.state
			if sb.busy != "" {
				st = sb.busy
			}
			return nil, &Error{Refusal: RefState, State: sb.state, Msg: fmt.Sprintf("sandbox %q is %s: a clone copies a stopped sandbox — stop it, or clone a snapshot of it", sd.Name, st)}
		}
		cur := filepath.Join(dir, layers.CurDir)
		if _, err := os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
			if sd.Base != "" {
				return nil, &Error{Refusal: RefState, State: StateError, Msg: fmt.Sprintf("sandbox %q's state is missing: it can't be cloned", sd.Name)}
			}
			return src, nil // it never ran: the clone starts from nothing, as it would
		}
		if src.st, err = layers.Read(cur); err != nil {
			return nil, err
		}
		src.dir, src.bytes, src.diskGiB = cur, curBytes(sb), sd.DiskGiB
	}
	what := fmt.Sprintf("sandbox %q's state", sd.Name)
	if src.sid != "" {
		what = fmt.Sprintf("snapshot %s of %q", src.sid, sd.Name)
	}
	if err := m.checkRestorable(d.Mode, src.st, what); err != nil {
		return nil, err
	}
	return src, nil
}

// beginClone makes the new sandbox d `creating`, holds its source, and
// starts the copy (start: the sandbox is started once it is made).
// Callers hold m.mu; d is stored.
func (m *Manager) beginClone(k Key, d *Def, src *cloneSource, start bool) {
	b := m.boxLocked(k, d.Name)
	b.state, b.snapsLoaded, b.copyBytes = StateCreating, true, src.bytes // booked until the copy ends
	b.detail = fmt.Sprintf("copying the state of %q", src.def.Name)
	if src.sid != "" {
		b.detail = fmt.Sprintf("copying snapshot %s of %q", src.sid, src.def.Name)
		if src.b.readers == nil {
			src.b.readers = map[string]int{}
		}
		src.b.readers[src.sid]++
	} else {
		src.b.busy = fmt.Sprintf("busy: being cloned into %q", d.Name)
	}
	ctx, cancel := context.WithCancel(m.copies.get())
	job := newJob()
	b.clone, b.cloneCancel = job, cancel
	go m.cloneCopy(ctx, k, d.clone(), b, src, job, start)
}

// cloneCopy is a clone's job: copy, then make the sandbox stopped (and
// start it, with start) — or error.
func (m *Manager) cloneCopy(ctx context.Context, k Key, d *Def, b *box, src *cloneSource, job *copyJob, start bool) {
	var err error
	defer func() {
		m.mu.Lock()
		if src.sid != "" {
			if src.b.readers[src.sid]--; src.b.readers[src.sid] <= 0 {
				delete(src.b.readers, src.sid)
			}
		}
		if b.clone == job {
			b.clone, b.cloneCancel = nil, nil
		}
		m.mu.Unlock()
		job.err = err
		close(job.done)
	}()
	if src.sid == "" && src.dir != "" { // its cur/: the source stays stopped, in its flight, under its lock
		src.b.flight.Lock()
		unlock := src.b.flight.Unlock
		err = m.fromStopped(k, src)
		if err == nil {
			var lock *os.File
			if lock, err = m.lockRun(k, src.def, filepath.Dir(src.dir)); err == nil {
				unlock = func() { lock.Close(); src.b.flight.Unlock() }
			}
		}
		if err == nil {
			err = m.cloneState(ctx, k, d, src)
		}
		m.mu.Lock()
		src.b.busy = "" // (before its flight is let go: a start waiting for it sees it stopped)
		m.mu.Unlock()
		unlock()
	} else {
		err = m.cloneState(ctx, k, d, src)
	}
	m.mu.Lock()
	cd, ok := m.defs.get(k, d.Name)
	if !ok || m.live[k][d.Name] != b || cd.UID != d.UID {
		m.mu.Unlock()
		return // deleted meanwhile: its state went to .trash
	}
	b.copyBytes = 0
	if err == nil {
		cd.Pending = ""
		err = m.defs.put(k, cd)
	}
	if err != nil {
		b.state, b.detail = StateError, "the clone failed: "+err.Error()+" — delete it and clone again"
		m.mu.Unlock()
		slog.Warn("tile sandbox: a clone failed", "tile", k.Tile, "sandbox", d.Name, "from", src.def.Name, "snapshot", src.sid, "err", err)
		return
	}
	b.state, b.detail, b.diskBytes = StateStopped, "", src.bytes // until it is measured
	m.mu.Unlock()
	slog.Info("tile sandbox: cloned", "tile", k.Tile, "sandbox", d.Name, "from", src.def.Name, "snapshot", src.sid)
	m.measureSoon(k, d)
	if start {
		if serr := m.Start(k, d.Name); serr != nil {
			m.mu.Lock()
			if b.run == nil && b.detail == "" {
				b.detail = serr.Error()
			}
			m.mu.Unlock()
		}
	}
}

// fromStopped re-checks, in the source's flight, that the source a clone
// copies the cur/ of is still the one asked for and still stopped (a start
// that waited for its flight before the clone held it may have run).
func (m *Manager) fromStopped(k Key, src *cloneSource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sd, ok := m.defs.get(k, src.def.Name)
	switch {
	case !ok || sd.UID != src.def.UID || m.live[k][sd.Name] != src.b:
		return fmt.Errorf("sandbox %q was deleted before its state was copied", src.def.Name)
	case src.b.run != nil:
		return fmt.Errorf("sandbox %q started before its state was copied: stop it, or clone a snapshot of it", src.def.Name)
	}
	return nil
}

// cloneState copies src into d's new state dir: staged in its tmp/, then
// renamed to its cur/.
func (m *Manager) cloneState(ctx context.Context, k Key, d *Def, src *cloneSource) error {
	if src.dir == "" {
		return nil
	}
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := m.stage(dir)
	if err != nil {
		return err
	}
	if err := m.copyLayer(ctx, d.Mode, src.dir, tmp, src.st); err != nil {
		m.discard(k, d, tmp)
		return err
	}
	cur := filepath.Join(dir, layers.CurDir)
	if _, err := os.Lstat(cur); err == nil {
		m.discard(k, d, tmp)
		return fmt.Errorf("%s exists", cur)
	}
	if err := os.Rename(tmp, cur); err != nil {
		m.discard(k, d, tmp)
		return err
	}
	return nil
}

// deleteBlockedLocked refuses a DELETE while a copy reads the sandbox's
// state — its own snapshot or restore, a clone of its cur/ (busy), or a
// clone of one of its snapshots (readers): the confined remover would pull
// it from under the copy. A clone of its own that is copying ends first
// (endClone). Callers hold m.mu.
func deleteBlockedLocked(name string, b *box) *Error {
	if b.state == StateCreating {
		return nil
	}
	if e := busyLocked(name, b); e != nil {
		return e
	}
	if len(b.readers) > 0 {
		return &Error{Refusal: RefState, State: b.state, RetryAfter: busyRetry, busy: true,
			Msg: fmt.Sprintf("sandbox %q is being cloned (a snapshot of it is being copied): try again once the clone is made", name)}
	}
	return nil
}

// endClone ends k's sandbox's clone copy, if it is creating, and waits for
// it (at most endWait): a DELETE then puts away what it made.
func (m *Manager) endClone(k Key, name string) error {
	m.mu.Lock()
	var job *copyJob
	if b := m.live[k][name]; b != nil && b.clone != nil {
		job = b.clone
		b.cloneCancel()
	}
	m.mu.Unlock()
	if job == nil {
		return nil
	}
	if !waitJob(job, m.endWait) {
		return &Error{Refusal: RefState, State: StateCreating, RetryAfter: 5 * busyRetry, Msg: fmt.Sprintf("sandbox %q's copy is still ending: try again", name)}
	}
	return nil
}
