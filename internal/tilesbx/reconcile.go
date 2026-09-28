package tilesbx

// reconcile.go — running sandboxes follow their tile's reach, not only the
// next start (plans/tile-sandbox-runtime.md §5, §7). A sandbox kept up by
// a non-tty exec may never restart, so what its start checked is checked
// again whenever it may have changed:
//
//   - On every registry rescan and every users event (Reconcile) each tile
//     with a running sandbox is walked: a tile that vanished, was disabled
//     or no longer holds cap:sandboxes has its sandboxes stopped, state
//     kept (this also catches a hand edit of xbin.json that dropped the
//     grant row, which fires no hook); the others have their mounts and
//     their egress resolved again.
//   - A res: grant change calls OnResourceChange for its tile.
//
// A mount the tile no longer holds, or a read-write one whose role dropped
// to reader, syncs and stops the sandbox, state kept, with stateDetail
// naming the mount — the rule §4 applies when egress narrows. A role that
// widened waits for the next start.

import (
	"fmt"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// runMount is one res mount of a run, as it was bound.
type runMount struct {
	Res string
	At  string
	RW  bool // bound read-write: the tile held it as a writer then
}

// resMounts are d's res mounts as binds made them (binds answers one bind
// per mount, in order).
func resMounts(d *Def, binds []sandbox.Bind) []runMount {
	var out []runMount
	for i, mt := range d.Mounts {
		if mt.Res == "" || i >= len(binds) {
			continue
		}
		out = append(out, runMount{Res: mt.Res, At: mt.At, RW: !binds[i].RO})
	}
	return out
}

// reconcileState coalesces the reconciles: one runs at a time, and a call
// that arrives during it runs it once more after.
type reconcileState struct {
	mu      sync.Mutex
	running bool
	again   bool
	idle    chan struct{} // closed when a reconcile round ends with nothing more asked (tests)
}

// Reconcile re-checks every running sandbox against its tile now (the
// registry rescanned, a users event): see the file comment. It returns at
// once; the stops it decides run in the background.
func (m *Manager) Reconcile() {
	m.recon.mu.Lock()
	defer m.recon.mu.Unlock()
	if m.recon.running {
		m.recon.again = true
		return
	}
	m.recon.running = true
	m.recon.idle = make(chan struct{})
	go func() {
		for {
			for _, tile := range m.activeTiles() {
				m.reconcileTile(tile)
			}
			m.recon.mu.Lock()
			if !m.recon.again {
				m.recon.running = false
				close(m.recon.idle)
				m.recon.mu.Unlock()
				return
			}
			m.recon.again = false
			m.recon.mu.Unlock()
		}
	}()
}

// OnUsersChange is the hub's users events (a policy row, a permission,
// org or personal set, an owner transfer, a user's role changed): a
// ceiling may have moved — a D20 policy-row edit fires no
// OnSandboxNetChange (§4) and no OnCapChange for a grant it narrows — and
// a user's noTerminal may have taken effect (an admin demoted). It
// reconciles, and cuts the terminals noTerminal now forbids (D88). It
// returns at once.
func (m *Manager) OnUsersChange() {
	m.Reconcile()
	m.cutNoTerminal()
}

// waitReconcile waits until no reconcile runs (tests).
func (m *Manager) waitReconcile() {
	m.recon.mu.Lock()
	idle, running := m.recon.idle, m.recon.running
	m.recon.mu.Unlock()
	if running {
		<-idle
	}
}

// reconcileTile is one tile's share of a reconcile.
func (m *Manager) reconcileTile(tile string) {
	if why, _ := m.tileReach(tile); why != "" {
		go m.StopTile(tile, why)
		return
	}
	m.reconcileMounts(tile)
	m.reconcileEgress(tile)
}

// tileReach is whether tile may run sandboxes now: "" and nil when it may;
// else why its sandboxes stop (a reconcile's stateDetail) and the refusal
// a start answers — the tile removed, disabled (hidden, offloaded) or no
// longer holding cap:sandboxes. Without Deps.Caps it fails closed.
func (m *Manager) tileReach(tile string) (why string, refusal *Error) {
	switch t := m.deps.Tiles; {
	case t != nil && !t.Exists(tile):
		return "its tile was removed: stopped, state kept (a workspace admin can delete it)",
			refuse(RefNotAllowed, "the tile %s was removed: its sandboxes don't start", tile)
	case t != nil && !t.Enabled(tile):
		return "its tile is disabled: stopped, state kept",
			&Error{Refusal: RefUnavailable, Msg: fmt.Sprintf("the tile %s is disabled: its sandboxes don't start", tile), RetryAfter: time.Minute}
	case m.deps.Caps == nil || !m.deps.Caps.SandboxesFor(tile):
		return "its tile no longer holds cap:sandboxes: stopped, state kept",
			refuse(RefNotAllowed, "the tile %s doesn't hold cap:sandboxes", tile)
	}
	return "", nil
}

// OnResourceChange is a res: grant of tile approved or revoked (the
// broker's grantRestart): its running sandboxes' mounts are resolved
// again. It returns at once.
func (m *Manager) OnResourceChange(tile string) { go m.reconcileMounts(tile) }

// reconcileMounts stops each running sandbox of tile a mount of which the
// tile no longer holds as it was bound.
func (m *Manager) reconcileMounts(tile string) {
	for _, r := range m.running(func(k Key, _ *Def) bool { return k.Tile == tile }) {
		if why := m.mountsNarrowed(r); why != "" {
			go func(r *run) { _ = m.Stop(r.k, r.def.Name, why) }(r)
		}
	}
}

// mountsNarrowed is why r's mounts no longer stand ("" = they do): one the
// tile no longer holds, one no longer a filesystem, or a read-write one
// the tile now holds only as a reader. A view that is merely down isn't
// narrowed (the vault's seal stops sandboxes itself).
func (m *Manager) mountsNarrowed(r *run) string {
	for _, mt := range r.mounts {
		ms, err := m.ResourceMount(r.k, mt.Res)
		switch {
		case err != nil:
			return fmt.Sprintf("its mount at %s (%s) is no longer held: stopped, state kept — %v", mt.At, mt.Res, err)
		case ms.Kind != "filesystem":
			return fmt.Sprintf("its mount at %s (%s) is no longer a filesystem resource: stopped, state kept", mt.At, mt.Res)
		case mt.RW && ms.Role != "writer" && ms.Role != "admin":
			return fmt.Sprintf("its read-write mount at %s (%s): the tile now holds it only as a %s: stopped, state kept — start it again to mount it read-only", mt.At, mt.Res, ms.Role)
		}
	}
	return ""
}
