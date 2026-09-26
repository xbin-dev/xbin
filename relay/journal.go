package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// The relay's state on disk: a snapshot (<state>) and an append-only
// journal (<state>.log) of the changes since. Every change appends one small
// record and fsyncs it, outside the store's lock; when the journal outgrows
// the snapshot, a background compaction writes a fresh snapshot from a copy
// of the state and starts a new journal with what arrived meanwhile. Opening
// replays the journal onto the snapshot (records at or below the
// snapshot's seq are already in it; a torn last line is ignored).

// record is one journal line: an entry's new value, or (value nil) its
// deletion.
type record struct {
	Seq uint64     `json:"s"`
	H   string     `json:"h,omitempty"`
	HV  *Handle    `json:"hv,omitempty"`
	W   string     `json:"w,omitempty"`
	WV  *Workspace `json:"wv,omitempty"`
}

// minCompact is the journal size below which it is never compacted (a
// variable for tests).
var minCompact int64 = 4 << 20

var errClosed = errors.New("relay state is closed")

type journal struct {
	path string // the snapshot; the journal is path + ".log"

	snapshot func() *state // a deep copy of the live state (store.snapshot)
	written  func(*state)  // that copy is on disk now (store.written)

	mu         sync.Mutex
	f          *os.File
	size       int64
	compactAt  int64
	capture    bool     // a compaction is running: keep what is appended
	tail       [][]byte // … here
	compacting bool
	closed     bool
	wg         sync.WaitGroup
	onErr      func(error) // a background compaction failed (logged)
}

func (j *journal) logPath() string { return j.path + ".log" }

func applyRecord(st *state, r record) {
	switch {
	case r.H != "" && r.HV != nil:
		st.Handles[r.H] = r.HV
	case r.H != "":
		delete(st.Handles, r.H)
	case r.W != "" && r.WV != nil:
		st.Workspaces[r.W] = r.WV
	case r.W != "":
		delete(st.Workspaces, r.W)
	}
}

// openJournal loads the snapshot, replays the journal onto it, folds the
// result into a new snapshot when the journal held anything, and opens an
// empty journal.
func openJournal(path string) (*journal, state, error) {
	st := state{Version: 1}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &st); err != nil {
			return nil, st, fmt.Errorf("relay state %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return nil, st, err
	}
	if st.Handles == nil {
		st.Handles = map[string]*Handle{}
	}
	if st.Workspaces == nil {
		st.Workspaces = map[string]*Workspace{}
	}
	j := &journal{path: path}
	recs, err := readRecords(j.logPath())
	if err != nil {
		return nil, st, err
	}
	sort.Slice(recs, func(a, b int) bool { return recs[a].Seq < recs[b].Seq })
	for _, r := range recs {
		if r.Seq > st.Seq {
			applyRecord(&st, r)
			st.Seq = r.Seq
		}
	}
	size := int64(len(b))
	if len(recs) > 0 || b == nil { // replayed something, or no snapshot yet
		if size, err = writeSnapshot(path, &st); err != nil {
			return nil, st, err
		}
	}
	f, err := os.OpenFile(j.logPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return nil, st, err
	}
	j.f, j.compactAt = f, max(minCompact, size)
	return j, st, nil
}

func readRecords(path string) ([]record, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Seq > 0 {
			out = append(out, r) // anything else is a torn write
		}
	}
	return out, nil
}

// writeSnapshot writes st atomically (temp file, fsync, rename, fsync the
// directory), mode 0600. Returns its size.
func writeSnapshot(path string, st *state) (int64, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return 0, err
	}
	return int64(len(b)), writeAtomic(path, b)
}

func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// append writes records and fsyncs them; a journal grown past its
// threshold starts a background compaction.
func (j *journal) append(recs []record) error {
	var buf bytes.Buffer
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return errClosed
	}
	if _, err := j.f.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	j.size += int64(buf.Len())
	if j.capture {
		j.tail = append(j.tail, buf.Bytes())
	}
	if j.size > j.compactAt && !j.compacting && !j.closed {
		j.compacting = true
		j.wg.Add(1)
		go func() {
			defer j.wg.Done()
			if err := j.compact(); err != nil && j.onErr != nil {
				j.onErr(err)
			}
		}()
	}
	return nil
}

// compact writes a snapshot of the live state and replaces the journal
// with the records appended while it was written. The caller set
// j.compacting.
func (j *journal) compact() error {
	j.mu.Lock()
	j.capture, j.tail = true, nil
	j.mu.Unlock()
	snap := j.snapshot()
	size, err := writeSnapshot(j.path, snap)
	if err == nil {
		j.written(snap)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	tail := j.tail
	j.capture, j.tail, j.compacting = false, nil, false
	if err != nil {
		return err
	}
	if j.f == nil {
		return errClosed
	}
	var buf bytes.Buffer
	for _, t := range tail {
		buf.Write(t)
	}
	if err := writeAtomic(j.logPath(), buf.Bytes()); err != nil {
		return err
	}
	f, err := os.OpenFile(j.logPath(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	j.f.Close()
	j.f, j.size, j.compactAt = f, int64(buf.Len()), max(minCompact, size)
	return nil
}

// close waits for a running compaction, compacts once more (so usage
// timestamps survive) and closes the journal.
func (j *journal) close() error {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.closed = true
	j.mu.Unlock()
	j.wg.Wait()
	j.mu.Lock()
	j.compacting = true
	j.mu.Unlock()
	err := j.compact()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f != nil {
		j.f.Close()
		j.f = nil
	}
	return err
}
