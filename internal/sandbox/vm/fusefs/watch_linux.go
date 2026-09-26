//go:build linux

package fusefs

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// watcher turns host-side changes into FUSE invalidations, so the guest can
// cache for an hour and still see the browser editor's save at once. Every
// directory the guest has looked into carries an inotify watch; an event
// drops the guest's entry for the name (a negative one included), its view
// of the directory (attributes, cached listing) or the file's attributes and
// pages. Changes the guest made itself are skipped — its kernel already
// knows — and a queue overflow invalidates everything.
type watcher struct {
	fs *FS
	fd int

	mu     sync.Mutex
	wds    map[int32][]uint64 // watch → the directory nodes it serves
	byNode map[uint64]int32
	own    map[ownKey]time.Time // (dir, name) the guest just changed

	events chan []event
	done   chan struct{}
}

type ownKey struct {
	dir  uint64
	name string
}

type event struct {
	wd   int32
	mask uint32
	name string
}

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO |
	unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_CLOSE_WRITE | unix.IN_EXCL_UNLINK | unix.IN_ONLYDIR

// ownFor is how long a guest-made change masks the host event it causes.
const ownFor = 2 * time.Second

func newWatcher(fs *FS) (*watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &watcher{fs: fs, fd: fd, wds: map[int32][]uint64{}, byNode: map[uint64]int32{},
		own: map[ownKey]time.Time{}, events: make(chan []event, 64), done: make(chan struct{})}, nil
}

// add watches directory node n (through its magic link: the directory
// itself, never a name the guest chose).
func (w *watcher) add(n *node) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.byNode[n.id]; ok {
		return
	}
	wd, err := unix.InotifyAddWatch(w.fd, procPath(n.fd), watchMask)
	if err != nil {
		return // ENOSPC (watch budget): that directory just goes unwatched
	}
	w.byNode[n.id] = int32(wd)
	w.wds[int32(wd)] = append(w.wds[int32(wd)], n.id)
}

func (w *watcher) remove(n *node) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wd, ok := w.byNode[n.id]
	if !ok {
		return
	}
	delete(w.byNode, n.id)
	ids := w.wds[wd][:0]
	for _, id := range w.wds[wd] {
		if id != n.id {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		delete(w.wds, wd)
		_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
	} else {
		w.wds[wd] = ids
	}
}

// ours notes a change the guest is making to dir/name.
func (w *watcher) ours(dir uint64, name string) {
	w.mu.Lock()
	w.own[ownKey{dir, name}] = time.Now()
	w.mu.Unlock()
}

func (w *watcher) close() {
	select {
	case <-w.done:
	default:
		close(w.done)
		unix.Close(w.fd)
	}
}

// run reads events and applies them in batches (a burst of writes to one
// file becomes one invalidation).
func (w *watcher) run() {
	go w.read()
	for {
		var batch []event
		select {
		case <-w.done:
			return
		case b := <-w.events:
			batch = append(batch, b...)
		}
		deadline := time.After(20 * time.Millisecond)
	collect:
		for {
			select {
			case b := <-w.events:
				batch = append(batch, b...)
			case <-deadline:
				break collect
			case <-w.done:
				return
			}
		}
		w.apply(batch)
	}
}

func (w *watcher) read() {
	buf := make([]byte, 64<<10)
	for {
		n, err := unix.Read(w.fd, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return
		}
		var evs []event
		for b := buf[:n]; len(b) >= unix.SizeofInotifyEvent; {
			raw := (*unix.InotifyEvent)(unsafe.Pointer(&b[0]))
			nameLen := int(raw.Len)
			name := cstring(b[unix.SizeofInotifyEvent : unix.SizeofInotifyEvent+nameLen])
			evs = append(evs, event{wd: raw.Wd, mask: raw.Mask, name: name})
			b = b[unix.SizeofInotifyEvent+nameLen:]
		}
		select {
		case w.events <- evs:
		case <-w.done:
			return
		}
	}
}

func (w *watcher) apply(batch []event) {
	srv := w.fs.srv
	if srv == nil {
		return
	}
	type inval struct {
		entries map[ownKey]bool
		inodes  map[uint64]bool
	}
	iv := inval{entries: map[ownKey]bool{}, inodes: map[uint64]bool{}}
	now := time.Now()
	w.mu.Lock()
	for k, t := range w.own {
		if now.Sub(t) > ownFor {
			delete(w.own, k)
		}
	}
	for _, ev := range batch {
		if ev.mask&unix.IN_Q_OVERFLOW != 0 {
			w.mu.Unlock()
			w.invalidateAll()
			return
		}
		if ev.mask&unix.IN_IGNORED != 0 {
			for _, id := range w.wds[ev.wd] {
				delete(w.byNode, id)
			}
			delete(w.wds, ev.wd)
			continue
		}
		for _, dir := range w.wds[ev.wd] {
			if ev.name == "" {
				iv.inodes[dir] = true
				continue
			}
			if _, own := w.own[ownKey{dir, ev.name}]; own {
				continue
			}
			if ev.mask&(unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO) != 0 {
				iv.entries[ownKey{dir, ev.name}] = true
				iv.inodes[dir] = true
			}
			if ev.mask&(unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
				if child := w.fs.childOf(dir, ev.name); child != 0 {
					iv.inodes[child] = true
				}
			}
		}
	}
	w.mu.Unlock()
	for e := range iv.entries {
		_ = srv.EntryNotify(e.dir, e.name)
	}
	for id := range iv.inodes {
		if w.fs.writing(id) {
			continue // the guest's own write: its cache is the truth
		}
		_ = srv.InodeNotify(id, 0, 0)
	}
}

// invalidateAll drops everything the guest caches (events were lost).
func (w *watcher) invalidateAll() {
	w.fs.mu.Lock()
	type ent struct {
		id, parent uint64
		name       string
	}
	var all []ent
	for _, n := range w.fs.nodes {
		all = append(all, ent{n.id, n.parent, n.name})
	}
	w.fs.mu.Unlock()
	for _, e := range all {
		_ = w.fs.srv.InodeNotify(e.id, 0, 0)
		if e.parent != 0 {
			_ = w.fs.srv.EntryNotify(e.parent, e.name)
		}
	}
}

// childOf finds the node for dir/name, if the guest has one.
func (fs *FS) childOf(dir uint64, name string) uint64 {
	d := fs.node(dir)
	if d == nil || !validName(name) {
		return 0
	}
	var st unix.Statx_t
	if unix.Statx(d.fd, name, unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_DONT_SYNC, unix.STATX_INO|unix.STATX_MNT_ID, &st) != nil {
		return 0
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.byKey[keyOf(&st)]
}

// writing reports whether the guest itself wrote node id just now.
func (fs *FS) writing(id uint64) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n := fs.nodes[id]
	return n != nil && time.Since(n.lastWrite) < ownFor
}
