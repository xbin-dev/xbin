package tilesbx

// snapshot.go — snapshots (plans/tile-sandbox-runtime.md §3.9). A snapshot
// is a copy of a sandbox's cur/ — its upper (namespace) or its disk (VM) —
// in <state>/snapshots/<sid>/, with the base and overlay stamps of what it
// copied (layers.Stamp: it pins its base, whatever the sandbox does next)
// and a meta.json. The copy is staged in <state>/tmp/<rand>/, stamped,
// measured and described before one rename puts it in place, so a snapshot
// is listed complete or not at all; a restart empties tmp/ (sweep.go).
//
// Taking one stops the sandbox (its execs end killed), copies, and starts
// it again if it ran — off the request: ?wait bounds how long the call
// waits (202 pending when it didn't finish). Meanwhile the sandbox is busy
// (box.busy): the lifecycle calls, another snapshot, a restore, DELETE and
// every exec or file call answer 409 state with retryAfterMs, and a call
// that auto-starts waits it out (acquire). Ids are s-<n> from Def.SnapSeq,
// never handed out twice within a uid. Nothing a sandbox wrote is read as
// xbind: uppers are copied by a confined cp (confine.CopyTree, xattrs
// exactly), disks by fsutil.CloneSparse.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/layers"
)

const (
	snapMetaFile = "meta.json"
	snapNameMax  = 128
	busyRetry    = 2 * time.Second // a busy sandbox's retryAfterMs
)

// SnapshotInfo is a snapshot as the runtime answers it.
type SnapshotInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"created"`
	Bytes   int64  `json:"bytes"`
	Pending bool   `json:"pending,omitempty"` // still being taken
}

// SnapshotRequest is POST …/snapshots's body.
type SnapshotRequest struct {
	Name     string `json:"name"`
	ClientID string `json:"clientId,omitempty"`
}

// snapMeta is a snapshot's meta.json: written before the snapshot's
// rename, so a snapshot without one is a half-done copy (the boot removes
// it).
type snapMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Created  int64  `json:"created"`
	Mode     string `json:"mode"`
	Base     string `json:"base"`
	Overlay  string `json:"overlay,omitempty"`
	Bytes    int64  `json:"bytes"`
	DiskGiB  int    `json:"diskGiB,omitempty"` // a VM's disk, as large as it was
	ClientID string `json:"clientId,omitempty"`
}

func (s snapMeta) info() SnapshotInfo {
	return SnapshotInfo{ID: s.ID, Name: s.Name, Created: s.Created, Bytes: s.Bytes}
}

// snapSeq is a snapshot id's number (s-<n>).
func snapSeq(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimPrefix(id, "s-"), 10, 64)
	return n
}

// busyLocked refuses a call on a sandbox that can't take it now: one being
// created (a clone's copy runs), or busy with a copy. nil when neither.
// Callers hold m.mu.
func busyLocked(name string, b *box) *Error {
	switch {
	case b.state == StateCreating:
		return &Error{Refusal: RefState, State: StateCreating, RetryAfter: busyRetry, busy: true,
			Msg: fmt.Sprintf("sandbox %q is being created (%s): try again once it is done", name, b.detail)}
	case b.busy != "":
		return &Error{Refusal: RefState, State: b.state, RetryAfter: busyRetry, busy: true,
			Msg: fmt.Sprintf("sandbox %q is %s: try again once it is done", name, b.busy)}
	}
	return nil
}

// isBusy reports a busy refusal.
func isBusy(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.busy
}

// pendingLocked refuses a call on a clone whose copy never finished
// (Def.pending): it failed, or a restart cut it short — only DELETE helps.
func pendingLocked(d *Def, b *box) *Error {
	if d.Pending == "" || b.state == StateCreating {
		return nil
	}
	return &Error{Refusal: RefState, State: StateError, Msg: fmt.Sprintf("sandbox %q is a clone whose copy never finished: %s", d.Name, b.detail)}
}

// snapsLocked is k's sandbox's snapshots, read from disk the first time
// (callers hold m.mu; it is a directory listing and a small file each).
func (m *Manager) snapsLocked(k Key, d *Def, b *box) []snapMeta {
	if !b.snapsLoaded {
		b.snapsLoaded = true
		if dir, err := m.StateDir(k, d); err == nil {
			b.snaps = loadSnaps(dir)
		}
	}
	return append([]snapMeta(nil), b.snaps...)
}

// loadSnaps reads a state dir's snapshots: each snapshots/<sid>/ with a
// readable meta.json of that id (xbind wrote both), by id.
func loadSnaps(dir string) []snapMeta {
	ents, err := os.ReadDir(filepath.Join(dir, "snapshots"))
	if err != nil {
		return nil
	}
	var out []snapMeta
	for _, e := range ents {
		if !snapIDRE.MatchString(e.Name()) || !e.IsDir() {
			continue
		}
		sm, err := readSnapMeta(filepath.Join(dir, "snapshots", e.Name()))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("tile sandbox: a snapshot's meta.json", "dir", dir, "snapshot", e.Name(), "err", err)
			}
			continue
		}
		out = append(out, sm)
	}
	sort.Slice(out, func(i, j int) bool { return snapSeq(out[i].ID) < snapSeq(out[j].ID) })
	return out
}

// readSnapMeta reads dir's meta.json, never through a symlink, bounded.
func readSnapMeta(dir string) (snapMeta, error) {
	var sm snapMeta
	f, err := os.OpenFile(filepath.Join(dir, snapMetaFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return sm, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return sm, fmt.Errorf("%s isn't a regular file", snapMetaFile)
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return sm, err
	}
	if err := json.Unmarshal(b, &sm); err != nil {
		return sm, err
	}
	if sm.ID != filepath.Base(dir) {
		return sm, fmt.Errorf("%s names %q", snapMetaFile, sm.ID)
	}
	return sm, nil
}

// curBytes is what the sandbox's cur/ takes, as last measured: its bytes
// less its snapshots' (at least 0). Callers hold m.mu.
func curBytes(b *box) int64 {
	n := b.diskBytes
	for _, s := range b.snaps {
		n -= s.Bytes
	}
	return max(n, 0)
}

// diskRoom refuses a copy of bytes that would take the tile's sandboxes
// past perTile.diskGiB (429). Callers hold m.mu.
func (m *Manager) diskRoom(tile string, bytes int64) error {
	used, capGiB := m.tileDiskBytesLocked(tile), m.limitsFor(tile).PerTile.DiskGiB
	if used+bytes > int64(capGiB)<<30 {
		return refuse(RefLimit, "the copy (%s) would take the tile's sandboxes to %s, over its %d GiB (sandboxes policy: perTile.diskGiB): delete a sandbox or a snapshot", gib(bytes), gib(used+bytes), capGiB)
	}
	return nil
}

// checkSnapshotRequest validates a snapshot's name and clientId.
func checkSnapshotRequest(req *SnapshotRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > snapNameMax || hasControl(req.Name) {
		return refuse(RefInvalid, "a snapshot's name is at most %d printable characters", snapNameMax)
	}
	return checkClientID(req.ClientID)
}

// Snapshot starts taking a snapshot of k's sandbox. It answers at once:
// the snapshot (its id handed out, pending) and the job taking it — or,
// for a clientId repeat (repeat), the snapshot that has it (with its job
// while it is still being taken) — or a refusal.
func (m *Manager) Snapshot(k Key, name string, req SnapshotRequest) (sn SnapshotInfo, job *copyJob, repeat bool, err error) {
	if err := checkSnapshotRequest(&req); err != nil {
		return SnapshotInfo{}, nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok {
		return SnapshotInfo{}, nil, false, refuse(RefNotFound, "no sandbox %q", name)
	}
	b := m.boxLocked(k, name)
	snaps := m.snapsLocked(k, d, b)
	if req.ClientID != "" { // a repeat answers the snapshot it made
		same := func(s *snapMeta) (bool, error) {
			if s.ClientID != req.ClientID {
				return false, nil
			}
			if s.Name != req.Name {
				return false, refuse(RefExists, "clientId %q was used for another snapshot (%s)", req.ClientID, s.ID)
			}
			return true, nil
		}
		if p := b.pendingSnap; p != nil {
			if ok, err := same(p); ok || err != nil {
				in := p.info()
				in.Pending = true
				return in, b.copying, err == nil, err
			}
		}
		for i := range snaps {
			if ok, err := same(&snaps[i]); ok || err != nil {
				return snaps[i].info(), nil, err == nil, err
			}
		}
	}
	if e := busyLocked(name, b); e != nil {
		return SnapshotInfo{}, nil, false, e
	}
	if e := pendingLocked(d, b); e != nil {
		return SnapshotInfo{}, nil, false, e
	}
	if b.state == StateError {
		return SnapshotInfo{}, nil, false, &Error{Refusal: RefState, State: StateError, Msg: fmt.Sprintf("sandbox %q is in error: %s", name, b.detail)}
	}
	cur, err := m.CurDir(k, d)
	if err != nil {
		return SnapshotInfo{}, nil, false, err
	}
	if _, err := os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
		return SnapshotInfo{}, nil, false, &Error{Refusal: RefState, State: b.state, Msg: fmt.Sprintf("sandbox %q has no state yet: it never ran (start it once first)", name)}
	}
	booked := curBytes(b)
	if err := m.diskRoom(k.Tile, booked); err != nil {
		return SnapshotInfo{}, nil, false, err
	}
	d.SnapSeq++ // handed out now: never again within this uid, whatever becomes of the copy
	if err := m.defs.put(k, d); err != nil {
		return SnapshotInfo{}, nil, false, err
	}
	sm := &snapMeta{ID: fmt.Sprintf("s-%d", d.SnapSeq), Name: req.Name, Created: m.now().UnixMilli(), Mode: d.Mode, ClientID: req.ClientID}
	job = newJob()
	b.busy, b.pendingSnap, b.copying, b.copyBytes = "busy: taking snapshot "+sm.ID, sm, job, booked
	go m.takeSnapshot(m.copies.get(), k, d, b, *sm, job)
	in := sm.info()
	in.Pending = true
	return in, job, false, nil
}

// endCopy ends a snapshot's or a restore's busy spell (in its flight).
func (m *Manager) endCopy(b *box, job *copyJob, err error) {
	m.mu.Lock()
	if b.copying == job {
		b.busy, b.pendingSnap, b.copying, b.copyBytes = "", nil, nil, 0
	}
	m.mu.Unlock()
	job.err = err
	close(job.done)
}

// takeSnapshot is a snapshot's job, in the sandbox's flight: stop it if it
// runs, copy its cur/, start it again if it ran. ctx is the copies' as the
// snapshot was asked (StopAll cancels it).
func (m *Manager) takeSnapshot(ctx context.Context, k Key, d *Def, b *box, sm snapMeta, job *copyJob) {
	var err error
	b.flight.Lock()
	defer b.flight.Unlock()
	defer func() { m.endCopy(b, job, err) }() // before the flight is let go
	restart := false
	if restart, err = m.stopForCopy(k, d, b); err != nil {
		return
	}
	if sm, err = m.snapshotState(ctx, k, d, sm); err == nil {
		m.mu.Lock()
		b.snaps = append(b.snaps, sm)
		b.diskBytes += sm.Bytes // until it is measured again
		m.mu.Unlock()
		slog.Info("tile sandbox: snapshot taken", "tile", k.Tile, "sandbox", d.Name, "snapshot", sm.ID, "bytes", sm.Bytes)
	} else {
		slog.Warn("tile sandbox: a snapshot failed", "tile", k.Tile, "sandbox", d.Name, "snapshot", sm.ID, "err", err)
	}
	m.measureSoon(k, d)
	m.restartAfterCopy(ctx, k, d, b, restart)
}

// restartAfterCopy starts k's sandbox again after a copy (in its flight)
// when it ran before — unless xbind is shutting down (StopAll cancelled
// the copy's ctx): its stop found this sandbox stopped for the copy and
// doesn't wait for a start after it.
func (m *Manager) restartAfterCopy(ctx context.Context, k Key, d *Def, b *box, restart bool) {
	if !restart || ctx.Err() != nil {
		return
	}
	if err := m.startLocked(k, d.Name, b); err != nil {
		slog.Info("tile sandbox: starting again after a copy", "tile", k.Tile, "sandbox", d.Name, "err", err)
	}
}

// stopForCopy stops k's sandbox for a copy (in its flight) and says
// whether it ran — to be started again after. The sandbox must still be
// the one the copy was asked of.
func (m *Manager) stopForCopy(k Key, d *Def, b *box) (restart bool, err error) {
	m.mu.Lock()
	cd, ok := m.defs.get(k, d.Name)
	if !ok || m.live[k][d.Name] != b || cd.UID != d.UID {
		m.mu.Unlock()
		return false, refuse(RefNotFound, "no sandbox %q", d.Name)
	}
	up := b.run != nil
	restart = up && b.state == StateRunning // not one a stop that timed out left stopping
	m.mu.Unlock()
	if up {
		if err := m.stopLocked(b, ""); err != nil {
			return false, err
		}
	}
	return restart, nil
}

// snapshotState copies d's cur/ into a staged snapshot and renames it into
// place, under the sandbox's lock (an orphan's processes gone first): sm
// as written, its stamps and size filled in.
func (m *Manager) snapshotState(ctx context.Context, k Key, d *Def, sm snapMeta) (snapMeta, error) {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return sm, err
	}
	lock, err := m.lockRun(k, d, dir)
	if err != nil {
		return sm, err
	}
	defer lock.Close()
	cur := filepath.Join(dir, layers.CurDir)
	st, err := layers.Read(cur)
	if err != nil {
		return sm, err
	}
	tmp, err := m.stage(dir)
	if err != nil {
		return sm, err
	}
	done := false
	defer func() {
		if !done {
			m.discard(k, d, tmp)
		}
	}()
	if err := m.copyLayer(ctx, d.Mode, cur, tmp, st); err != nil {
		return sm, err
	}
	sm.Base, sm.Overlay = st.Base, st.Overlay
	if sm.Bytes, err = m.stagedBytes(ctx, d.Mode, tmp); err != nil {
		return sm, err
	}
	if d.Mode == ModeVM {
		sm.DiskGiB = diskGiBOf(filepath.Join(tmp, "vm", "disk.img"))
	}
	b, err := json.Marshal(sm)
	if err != nil {
		return sm, err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(tmp, snapMetaFile), b, 0o600); err != nil {
		return sm, err
	}
	snaps := filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(snaps, 0o700); err != nil {
		return sm, err
	}
	to := filepath.Join(snaps, sm.ID)
	if _, err := os.Lstat(to); err == nil {
		return sm, fmt.Errorf("snapshot %s exists", sm.ID)
	}
	if err := os.Rename(tmp, to); err != nil {
		return sm, err
	}
	done = true
	return sm, nil
}

// Snapshots is k's sandbox's snapshots, by id, the one being taken last
// (pending).
func (m *Manager) Snapshots(k Key, name string) ([]SnapshotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok {
		return nil, refuse(RefNotFound, "no sandbox %q", name)
	}
	b := m.boxLocked(k, name)
	if b.state == StateCreating {
		return nil, busyLocked(name, b)
	}
	out := []SnapshotInfo{}
	for _, s := range m.snapsLocked(k, d, b) {
		out = append(out, s.info())
	}
	if p := b.pendingSnap; p != nil {
		in := p.info()
		in.Pending = true
		out = append(out, in)
	}
	return out, nil
}

// snapshotInfo is k's sandbox's snapshot sid as it stands (pending while
// it is taken).
func (m *Manager) snapshotInfo(k Key, name, sid string) (SnapshotInfo, bool) {
	list, err := m.Snapshots(k, name)
	if err != nil {
		return SnapshotInfo{}, false
	}
	for _, s := range list {
		if s.ID == sid {
			return s, true
		}
	}
	return SnapshotInfo{}, false
}

// DeleteSnapshot puts k's sandbox's snapshot sid aside for the confined
// remover: one rename, refused while a copy of the sandbox runs or a clone
// copies the snapshot.
func (m *Manager) DeleteSnapshot(k Key, name, sid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok {
		return refuse(RefNotFound, "no sandbox %q", name)
	}
	b := m.boxLocked(k, name)
	if e := busyLocked(name, b); e != nil {
		return e
	}
	i := -1
	snaps := m.snapsLocked(k, d, b)
	for j, s := range snaps {
		if s.ID == sid {
			i = j
		}
	}
	if i < 0 {
		return refuse(RefNotFound, "sandbox %q has no snapshot %s", name, sid)
	}
	if b.readers[sid] > 0 {
		return &Error{Refusal: RefState, State: b.state, RetryAfter: busyRetry, Msg: fmt.Sprintf("snapshot %s of %q is being cloned: try again once the clone is made", sid, name)}
	}
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err
	}
	trash, err := m.TrashDir(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	to := filepath.Join(trash, d.UID+"."+randSuffix())
	if err := os.Rename(filepath.Join(dir, "snapshots", sid), to); err != nil {
		return err
	}
	b.snaps = append(snaps[:i:i], snaps[i+1:]...)
	b.diskBytes = max(b.diskBytes-snaps[i].Bytes, 0) // until it is measured again
	m.trash.putSized(to, snaps[i].Bytes)
	return nil
}
