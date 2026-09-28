package vm

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// reserve.go — the options a Reserve call takes. Every VM reservation is
// booked to its tile (the owner), whichever deployment or tile-managed
// sandbox it runs for; the options only refine whether it is admitted: a
// non-primary deployment's headroom and primary-first rule (P25), and the
// tile-managed sandboxes' sub-budget (plans/tile-sandbox-runtime.md §6.1).
// They are declared once, here, and every caller adds its options to this
// one type. A reservation without options is admitted and booked exactly as
// before.

// ReserveOption refines one Reserve call.
type ReserveOption func(*reserveOptions)

// reserveOptions is what one call's options ask for. Each field lands with
// the option that sets it and the check in Reserve that honours it.
type reserveOptions struct {
	// nonPrimary: the VM runs for a non-primary deployment, which leaves
	// room for the primary's next guest (primaryMiB and one VM) and never
	// makes room for itself (P25).
	nonPrimary bool
	primaryMiB int
	// stop: the VM runs for the tile's primary, and stop may end the same
	// tile's non-primary guests to admit it (P25).
	stop func(short Usage) bool
	// tile: TileSandbox — also books against the tile sub-budget.
	tile bool
}

// NonPrimary marks the reservation as a non-primary deployment's (P25). It
// is admitted only if the policy still has room afterwards for the tile's
// primary's next guest, for a blue/green swap or a restart: one more VM and
// primaryMiB, that guest's memory. primaryMiB is 0 when the primary runs no
// VM, and then nothing is kept free. It never preempts: a PrimaryFirst given
// with it is ignored, and its refusal is marked sbx.ErrRefused.
func NonPrimary(primaryMiB int) ReserveOption {
	return func(o *reserveOptions) {
		o.nonPrimary = true
		o.primaryMiB = max(primaryMiB, 0)
	}
}

// PrimaryFirst marks the reservation as the tile's primary's (P25). When the
// policy's count or budget can't admit it, Reserve calls stop, holding none
// of its books, with what the limits fall short by. stop, which the runner
// supplies, stops the same tile's non-primary guests if together they hold
// at least short, and returns true once their reservations have been given
// back (the runner's stop waits for the exit that releases them); Reserve
// then tries once more, and refuses if another reservation took that room
// first. stop returns false, having stopped nothing, when the tile's
// non-primary guests can't make that room, and the refusal is then the one
// a reservation without options gets.
func PrimaryFirst(stop func(short Usage) bool) ReserveOption {
	return func(o *reserveOptions) { o.stop = stop }
}

// TileSandbox marks the VM of a sandbox a manager tile runs (D120): besides
// the workspace's VM count and budget it must fit the policy's
// TilesBudgetMiB, and it is counted in UsedTiles until released. Other
// reservations never count against the sub-budget.
func TileSandbox() ReserveOption { return func(o *reserveOptions) { o.tile = true } }

func reserveOpts(opts []ReserveOption) reserveOptions {
	var o reserveOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// shortfall is how far one more VM of memMiB on top of used exceeds p's
// count and budget: zero when it fits.
func shortfall(p Policy, used Usage, memMiB int) Usage {
	return Usage{
		VMs:    max(used.VMs+1-p.MaxVMs, 0),
		MemMiB: max(used.MemMiB+memMiB-p.BudgetMiB, 0),
	}
}

// headroom refuses a non-primary reservation of memMiB, which the policy's
// limits admit, if it would leave less than the primary's next guest free.
func (o reserveOptions) headroom(p Policy, used Usage, memMiB int) error {
	if !o.nonPrimary || o.primaryMiB == 0 {
		return nil
	}
	if used.VMs+2 > p.MaxVMs {
		return sbx.Refuse(fmt.Errorf("the workspace's VM limit (%d running) has no room left for a non-primary deployment: one VM stays free for the tile's primary — stop a deployment or close a VM terminal, or ask an admin to raise it", p.MaxVMs))
	}
	if used.MemMiB+memMiB+o.primaryMiB > p.BudgetMiB {
		return sbx.Refuse(fmt.Errorf("the workspace's VM memory budget (%d MiB) has no room left for a non-primary deployment: %d MiB stays free for the tile's primary — stop a deployment or close a VM terminal, or ask an admin to raise it", p.BudgetMiB, o.primaryMiB))
	}
	return nil
}

// makeRoom asks a primary's stop to free short, and reports whether it did.
// A non-primary reservation never makes room.
func (o reserveOptions) makeRoom(short Usage) bool {
	if o.nonPrimary || o.stop == nil || short == (Usage{}) {
		return false
	}
	return o.stop(short)
}

// UsedTiles is what running tile-sandbox VMs hold: the part of Used booked
// with TileSandbox.
func (m *Manager) UsedTiles() Usage {
	if m == nil {
		return Usage{}
	}
	m.umu.Lock()
	defer m.umu.Unlock()
	return m.usedTiles
}

// TileVMs says whether a manager tile's sandbox may run in a VM now and, if
// not, why: the host can run VMs, an admin turned Tiles on, and where VMs
// run emulated, allowed that for tiles too (TilesEmulated). st is the probe
// (st.Emulated: the VM runs under emulation, which the runtime reports).
// A refusal makes VM mode unavailable; it never falls back to a namespace
// sandbox (D120).
func (m *Manager) TileVMs() (st Status, reason string) {
	st = m.Status()
	if !st.Available {
		return st, "VM sandboxes can't run here: " + st.Reason
	}
	p := m.Policy()
	if !p.Tiles {
		return st, "an admin hasn't enabled VM tile sandboxes (vm policy: tiles)"
	}
	if st.Emulated && !p.TilesEmulated {
		return st, "VMs would run emulated here (no usable KVM), and an admin hasn't allowed that for tile sandboxes (vm policy: tilesEmulated)"
	}
	return st, ""
}
