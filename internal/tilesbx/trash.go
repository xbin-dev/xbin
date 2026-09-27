package tilesbx

// trash.go — nothing of a sandbox's state is deleted inline
// (plans/tile-sandbox-runtime.md §1.3, §8.3). It is sandbox-written: an
// upper holds files of other sub-uids, modes that lock xbind out and links
// planted anywhere. A delete renames the state dir into its key's .trash
// (one rename of an xbind-created dir), and one worker empties .trash with
// a confined remove (confine.RemoveAll: a throwaway sandbox with the file
// capabilities), one entry at a time. At boot whatever .trash holds is
// queued again, and every staging dir (tmp/) goes there too: nothing staged
// survives a restart (sweep.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// trashTimeout bounds one entry's removal.
const trashTimeout = 2 * time.Hour

// trashQueue is the remover's queue: one worker, started when something is
// queued and gone when the queue is empty.
type trashQueue struct {
	remove func(ctx context.Context, dir string) error // confine.RemoveAll

	mu      sync.Mutex
	queue   []string
	working bool
	idle    chan struct{} // closed when the worker finds the queue empty (tests)
}

// put queues dirs for removal.
func (q *trashQueue) put(dirs ...string) {
	if len(dirs) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queue = append(q.queue, dirs...)
	if !q.working {
		q.working = true
		q.idle = make(chan struct{})
		go q.work()
	}
}

func (q *trashQueue) work() {
	for {
		q.mu.Lock()
		if len(q.queue) == 0 {
			q.working = false
			close(q.idle)
			q.mu.Unlock()
			return
		}
		dir := q.queue[0]
		q.queue = q.queue[1:]
		q.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), trashTimeout)
		if err := q.remove(ctx, dir); err != nil {
			// left in .trash: the next boot queues it again
			slog.Warn("tile sandboxes: removing old state", "dir", dir, "err", err)
		}
		cancel()
	}
}

// wait waits until the queue is empty (tests).
func (q *trashQueue) wait() {
	q.mu.Lock()
	idle, working := q.idle, q.working
	q.mu.Unlock()
	if working {
		<-idle
	}
}

// randSuffix is 8 random hex characters.
func randSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// trashState puts a stopped sandbox's state dir aside: renamed to
// .trash/<uid> (.trash/<uid>.<rand> if that is taken). It returns where it
// went ("" when the sandbox has no state) and how to put it back, for a
// delete that fails after it; the caller queues it for removal once the
// delete stands.
func (m *Manager) trashState(k Key, d *Def) (to string, undo func() error, err error) {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", func() error { return nil }, nil
	}
	trash, err := m.TrashDir(k)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", nil, err
	}
	to = filepath.Join(trash, d.UID)
	if _, err := os.Lstat(to); err == nil {
		to += "." + randSuffix()
	}
	if err := os.Rename(dir, to); err != nil {
		return "", nil, err
	}
	return to, func() error { return os.Rename(to, dir) }, nil
}
