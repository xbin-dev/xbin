package vm

// reserve.go — the options a Reserve call takes. Every VM reservation is
// booked to its tile (the owner), whichever deployment or tile-managed
// sandbox it runs for; the options only refine whether it is admitted: a
// non-primary deployment's headroom and primary-first rule (P25), and the
// tile-managed sandboxes' sub-budget (plans/tile-sandbox-runtime.md §6.1).
// They are declared once, here, and every caller adds its options to this
// one type.

// ReserveOption refines one Reserve call.
type ReserveOption func(*reserveOptions)

// reserveOptions is what one call's options ask for. Each field lands with
// the option that sets it and the check in Reserve that honours it.
type reserveOptions struct {
	tile bool // TileSandbox: also books against the tile sub-budget
}

// TileSandbox marks the VM of a sandbox a manager tile runs (D120): besides
// the workspace's VM count and budget it must fit the policy's
// TilesBudgetMiB, and it is counted in UsedTiles until released. Other
// reservations never count against the sub-budget.
func TileSandbox() ReserveOption { return func(o *reserveOptions) { o.tile = true } }

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
