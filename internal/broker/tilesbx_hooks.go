package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/xbin-dev/xbin/internal/registry"
)

// The tile-sandbox runtime (internal/tilesbx, D120) as the workspace's own
// lifecycle drives it (plans/tile-sandbox-runtime.md §5, §6.3, §9). The
// sandboxes a manager tile runs hold state the workspace must never drop
// silently, and mounts of resources the workspace may take away:
//
//   - a backup carries the tile's definitions (not their state), and a
//     restore merges them back by uid (backup.go, restore.go);
//   - offload refuses while any of the tile's sandboxes holds state — until
//     archive/thaw (WP-22) carries it; disable, hide and offload stop them;
//   - a removed tile's sandboxes are leftovers of its path (pathLeftovers);
//   - sealing the vault stops every sandbox with a resource mounted before
//     the views unmount, a cap:containers flip stops those of the scope
//     before the remount, and a res: grant change re-checks the tile's
//     running mounts (resenc_wire.go, broker.go's grantRestart);
//   - diskmon counts sandbox bytes for disk pressure only, and tells the
//     runtime when the disk is low (diskmon.go).

// TileSandboxHooks is the runtime, as the broker drives it. Boot installs
// it (SetTileSandboxes); without it there are no tile sandboxes.
type TileSandboxHooks interface {
	// Defs is tile's definitions as a backup carries them (nil: none).
	Defs(tile string) []json.RawMessage
	// RestoreDefs merges a backup's definitions by uid; skipped says what
	// was left out and why.
	RestoreDefs(tile string, defs []json.RawMessage) (skipped []string)
	// StopTile stops tile's running sandboxes, state kept, and returns once
	// they ended.
	StopTile(tile, why string)
	// StopWhere stops, in parallel, the running sandboxes pred picks, state
	// kept, and returns once they ended.
	StopWhere(pred func(TileSandbox) bool, why string)
	// HasState counts tile's sandboxes holding state (an upper, a disk, a
	// snapshot) and their bytes as last measured.
	HasState(tile string) (n int, bytes int64)
	// Leftovers counts the sandbox definitions of the tile at path and
	// under it, and their bytes as last measured.
	Leftovers(path string) (n int, bytes int64)
	// Usage is each tile's sandbox bytes as last measured.
	Usage() map[string]int64
	// OnResourceChange re-checks tile's running mounts (a res: grant
	// changed). It returns at once.
	OnResourceChange(tile string)
	// OnLowDisk is diskmon's low-disk verdict. It returns at once.
	OnLowDisk()
}

// TileSandbox is a running tile sandbox as StopWhere's predicate sees it.
type TileSandbox struct {
	Tile string
	Name string
	Res  []string // its resource mounts (res: targets), as it runs
}

// tileSbxRef holds the hooks for an atomic swap: diskmon's goroutine reads
// them from New on, boot installs them later.
type tileSbxRef struct{ h TileSandboxHooks }

// SetTileSandboxes installs the tile-sandbox runtime's hooks (boot).
func (b *Broker) SetTileSandboxes(h TileSandboxHooks) { b.tileSbx.Store(&tileSbxRef{h}) }

// tileSandboxes is the installed runtime (nil: none).
func (b *Broker) tileSandboxes() TileSandboxHooks {
	if r := b.tileSbx.Load(); r != nil {
		return r.h
	}
	return nil
}

// tileSbxSlot is the Broker's field type (broker.go).
type tileSbxSlot = atomic.Pointer[tileSbxRef]

// stopTileSandboxes stops tile's running sandboxes, state kept.
func (b *Broker) stopTileSandboxes(tile, why string) {
	if h := b.tileSandboxes(); h != nil {
		h.StopTile(tile, why)
	}
}

// stopMountedSandboxes stops every running sandbox with a resource of a
// scope inScope picks mounted, and returns once they ended: their binds
// hold a decrypted view that is about to go (a seal) or change (a
// cap:containers remount).
func (b *Broker) stopMountedSandboxes(inScope func(scope string) bool, why string) {
	h := b.tileSandboxes()
	if h == nil {
		return
	}
	h.StopWhere(func(s TileSandbox) bool {
		for _, res := range s.Res {
			rt, _, _ := b.parseRes(res)
			if inScope(rt.Scope) {
				return true
			}
		}
		return false
	}, why)
}

// sealSandboxes is the seal's share (SealResources): every running
// sandbox with a resource mounted — every filesystem resource is
// encrypted — is synced and stopped before the views unmount. A sandbox's
// bind of a view would otherwise keep its superblock and daemon alive and
// serve plaintext after the seal. Their next start answers 503 until the
// vault is unsealed.
func (b *Broker) sealSandboxes() {
	b.stopMountedSandboxes(func(string) bool { return true }, "the vault was sealed: stopped, state kept — start it again once the vault is unsealed")
}

// sandboxGrantChanged is grantRestart's share: a res: grant approved or
// revoked re-checks the tile's running mounts (a read-write mount whose
// role dropped, or one no longer held, stops its sandbox).
func (b *Broker) sandboxGrantChanged(g registry.Grant) {
	if h := b.tileSandboxes(); h != nil && strings.HasPrefix(g.Target, "res:") {
		h.OnResourceChange(g.From)
	}
}

// stopScopeMounts is the cap:containers flip's share: the scope's
// filesystem resources are about to be remounted in another mode, so the
// running sandboxes that mount one — their binds hold the old view — stop
// first.
func (b *Broker) stopScopeMounts(tile string) {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return
	}
	b.stopMountedSandboxes(func(scope string) bool { return scope == c.Scope },
		"its tile's scope changed how its resources are mounted (cap:containers): stopped, state kept — start it again")
}

// sandboxUsage is each tile's sandbox bytes, for diskmon's disk pressure.
func (b *Broker) sandboxUsage() map[string]int64 {
	if h := b.tileSandboxes(); h != nil {
		return h.Usage()
	}
	return nil
}

// sandboxesLowDisk passes diskmon's low-disk verdict on.
func (b *Broker) sandboxesLowDisk() {
	if h := b.tileSandboxes(); h != nil {
		h.OnLowDisk()
	}
}

// errSandboxState is an offload refused because the tile's sandboxes hold
// state offload can't carry yet (409).
type errSandboxState struct {
	tile  string
	n     int
	bytes int64
}

func (e *errSandboxState) Error() string {
	what, pron := fmt.Sprintf("its %d tile sandboxes hold", e.n), "them"
	if e.n == 1 {
		what, pron = "its tile sandbox holds", "it"
	}
	return fmt.Sprintf("can't offload %s: %s state (%s) that offload can't carry yet — delete %s first (in its manager, or in the admin console's runtime → sandboxes)",
		e.tile, what, humanBytes(e.bytes), pron)
}

// sandboxOffloadCheck refuses an offload while any of tile's sandboxes
// holds state (§9): offload never drops it, and until archive/thaw
// carries it, it can't take it along.
func (b *Broker) sandboxOffloadCheck(tile string) error {
	h := b.tileSandboxes()
	if h == nil {
		return nil
	}
	if n, bytes := h.HasState(tile); n > 0 {
		return &errSandboxState{tile: tile, n: n, bytes: bytes}
	}
	return nil
}

// offloadStatus is the HTTP status an offload failure answers.
func offloadStatus(err error) int {
	var e *errSandboxState
	if errors.As(err, &e) {
		return 409
	}
	return 502
}

// sandboxLeftovers is pathLeftovers' share: a removed tile's sandbox
// definitions and their state, which a tile created at its path would
// inherit.
func (b *Broker) sandboxLeftovers(path string) []string {
	h := b.tileSandboxes()
	if h == nil {
		return nil
	}
	n, bytes := h.Leftovers(path)
	if n == 0 {
		return nil
	}
	s := "es"
	if n == 1 {
		s = ""
	}
	return []string{fmt.Sprintf("%d tile sandbox%s (%s) of a removed tile", n, s, humanBytes(bytes))}
}
