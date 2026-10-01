package term

// basemove.go — the moving parts of base auto-update (D175; the decision is
// claimLayer's, base.go).
//
//   - The current base's version is read once (currentBase): the installer
//     stops xbind before it swaps the rootfs, so it can't change under a
//     running xbind, and a read that fails is an error, never "v0".
//   - A move puts the old layer aside — one rename into .xbin/term-moved/,
//     which no layer scan (layers.List: .xbin/term, .xbin/sbx), the VM disk
//     scan or a backup reads — so the session starts at once, however big
//     the layer (a VM terminal's disk is GBs). One worker removes what is
//     put aside, confined (removeLayer: the sandbox's files belong to other
//     sub-uids), one entry at a time; the boot queues whatever a restart
//     left (sweepMoved, from CheckBaseImages).
//   - A move an agent session's start made is told to the tile's next shell,
//     once (noteMove, takeMoveNote): an alwaysOn agent is often the first
//     to start on a tile after an upgrade, and the shell user's packages
//     went with it.

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/util"
)

// EvNotice is an agent session event xbin itself logs, not the agent: a line
// the session's clients show muted ({text}; docs/protocol.md). Today one: the
// session's start moved the tile's layer to a new base (D175).
const EvNotice = "notice"

// noticeData is EvNotice's payload.
type noticeData struct {
	Text string `json:"text"`
}

// baseState is the Manager's share of base auto-update (zero value ready).
type baseState struct {
	mu      sync.Mutex
	cur     string            // the current base's version, once read ("" = not yet)
	curOf   string            // the rootfs it was read from
	notes   map[string]string // terminal key → the line its next shell says (an agent's move)
	queue   []string          // put-aside layers waiting for the remover
	working bool              // the remover runs
	idle    chan struct{}     // closed when the remover finds the queue empty
}

// currentBase is the current rootfs's base version, read once. A version
// file that can't be read is an error and is read again next time:
// nothing is moved or stamped on a guess (an unreadable file read as "v0"
// once had a layer on the current base discarded as outdated).
func (m *Manager) currentBase() (string, error) {
	b := &m.base
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur != "" && b.curOf == m.Rootfs {
		return b.cur, nil
	}
	v, err := layers.ReadBaseVersion(m.Rootfs)
	if err != nil {
		return "", fmt.Errorf("the base image's version (%s) can't be read: %w", filepath.Join(m.Rootfs, layers.VersionFile), err)
	}
	b.cur, b.curOf = v, m.Rootfs
	return v, nil
}

// movedRoot is where layers moved off their base wait for the remover.
func (m *Manager) movedRoot() string { return filepath.Join(m.Root, ".xbin", "term-moved") }

// moveLayer moves a held layer to the current base cur: the old layer is
// put aside for the remover (or, where it can't be renamed, removed in
// place, confined — the start then waits for it), and a fresh layer dir is
// stamped cur. An error fails the start with nothing mounted; a fresh dir
// that couldn't be stamped is removed again, so the next start makes a new
// layer on the current base.
func (m *Manager) moveLayer(dir, cur string) error {
	if err := m.putAside(dir); err != nil {
		slog.Warn("terminal layer: can't put the old layer aside; removing it in place", "layer", dir, "err", err)
		if err := m.removeLayer(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := m.stamp(dir, layers.Stamps{Base: cur}); err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("stamp the new layer: %w", err)
	}
	return nil
}

// stamp is layers.Stamp (tests make it fail).
func (m *Manager) stamp(dir string, s layers.Stamps) error {
	if m.stampHook != nil {
		return m.stampHook(dir, s)
	}
	return layers.Stamp(dir, s)
}

// putAside renames a layer into .xbin/term-moved/<key>.<rand> and queues it
// for the remover.
func (m *Manager) putAside(dir string) error {
	root := m.movedRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	to := filepath.Join(root, filepath.Base(dir)+"."+util.RandomToken(4))
	if err := os.Rename(dir, to); err != nil {
		return err
	}
	m.queueMoved(to)
	return nil
}

// sweepMoved queues what .xbin/term-moved holds (the boot: a restart cut
// the remover short).
func (m *Manager) sweepMoved() {
	ents, err := os.ReadDir(m.movedRoot())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("terminal layers moved off their base: can't list what is left to remove", "err", err)
		}
		return
	}
	var dirs []string
	for _, e := range ents {
		dirs = append(dirs, filepath.Join(m.movedRoot(), e.Name()))
	}
	m.queueMoved(dirs...)
}

// queueMoved hands dirs to the remover, starting it when it isn't running.
func (m *Manager) queueMoved(dirs ...string) {
	if len(dirs) == 0 {
		return
	}
	b := &m.base
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = append(b.queue, dirs...)
	if !b.working {
		b.working = true
		b.idle = make(chan struct{})
		go m.removeMoved()
	}
}

// removeMoved is the remover: one entry at a time, confined, until the
// queue is empty. One it can't remove stays for the next boot's sweep.
func (m *Manager) removeMoved() {
	b := &m.base
	for {
		b.mu.Lock()
		if len(b.queue) == 0 {
			b.working = false
			close(b.idle)
			b.mu.Unlock()
			return
		}
		dir := b.queue[0]
		b.queue = b.queue[1:]
		b.mu.Unlock()
		if err := m.removeLayer(dir); err != nil {
			slog.Warn("terminal layer moved off its base: removing the old layer failed (the next boot tries again)", "dir", dir, "err", err)
		}
	}
}

// waitMoved returns once the remover has nothing left (tests, shutdown).
func (m *Manager) waitMoved() {
	b := &m.base
	b.mu.Lock()
	idle, working := b.idle, b.working
	b.mu.Unlock()
	if working {
		<-idle
	}
}

// sayBaseMoved is an agent session saying its start moved the tile's layer
// (key) to the current base: a notice in its log — the Agent tab shows it,
// muted — and its host's text log; and the tile's next shell says it too
// (noteMove), since the packages that went may have been a shell user's.
func (s *Session) sayBaseMoved(m *Manager, key string) {
	s.logEvent(m, agent.New(EvNotice, noticeData{Text: baseMovedNote}))
	s.agent.logf(baseMovedNote)
	m.noteMove(key)
}

// noteMove records that an agent session's start moved key's layer, for the
// tile's next shell (takeMoveNote).
func (m *Manager) noteMove(key string) {
	b := &m.base
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.notes == nil {
		b.notes = map[string]string{}
	}
	b.notes[key] = baseMovedByAgentLine
}

// takeMoveNote is the line a shell starting on key says first: its own
// move's (moved), else an agent's move not yet told (once), else "".
func (m *Manager) takeMoveNote(key string, moved bool) string {
	b := &m.base
	b.mu.Lock()
	defer b.mu.Unlock()
	note := b.notes[key]
	delete(b.notes, key)
	if moved {
		return baseMovedLine
	}
	return note
}
