package vm

// reserve.go — the options a Reserve call takes. Every VM reservation is
// booked to its tile (the owner), whichever deployment or tile-managed
// sandbox it runs for; the options only refine whether it is admitted: a
// non-primary deployment's headroom and primary-first rule (P25), and the
// tile-managed sandboxes' sub-budget (plans/tile-sandboxes.md). They are
// declared once, here, and every caller adds its options to this one type.

// ReserveOption refines one Reserve call. None is honoured yet: a
// reservation with options is admitted and booked exactly as one without.
type ReserveOption func(*reserveOptions)

// reserveOptions is what one call's options ask for. Each field lands with
// the option that sets it and the check in Reserve that honours it.
type reserveOptions struct{}
