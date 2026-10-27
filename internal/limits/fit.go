package limits

import "fmt"

// Pcts are the raises offered when a tile meets a cap; DefaultPct is the
// one pre-selected (the owner's call, D203).
var Pcts = []int{25, 50, 100}

const DefaultPct = 50

// ReserveFraction of host memory and of the data partition is never
// granted: the workspace itself, and the slack the defaults rely on.
const ReserveFraction = 0.10

// Approver hints, in an Action and a row's "approvers".
const (
	ApproverAdmin = "workspace-admin"
	ApproverOrg   = "org:" // + the owning org's id
)

// Choice is one raise an approver is offered.
type Choice struct {
	Pct        int    `json:"pct"`
	Value      int64  `json:"value"`
	Fits       bool   `json:"fits"`
	Why        string `json:"why,omitempty"` // why it doesn't fit, or why this viewer can't approve it
	Approvable bool   `json:"approvable"`    // by the viewer the row or alert was decorated for
	Default    bool   `json:"default,omitempty"`
}

// Step is current raised by pct, rounded up to k's step and at least one
// step above current.
func Step(k Kind, current int64, pct int) int64 {
	s := max(metas[k].Step, 1)
	v := (current*int64(100+pct) + 99) / 100
	v = (v + s - 1) / s * s
	return max(v, current+s)
}

// Capacity is the workspace's room for one kind, in the kind's unit.
type Capacity struct {
	Kind     Kind  `json:"kind"`
	Max      int64 `json:"max"`      // no tile goes past it (0 = none)
	Total    int64 `json:"total"`    // host memory, the data partition, the CPUs (0 = unknown)
	Free     int64 `json:"free"`     // MemAvailable, free disk; unused for vCPUs
	Reserve  int64 `json:"reserve"`  // ReserveFraction of Total
	Promised int64 `json:"promised"` // the other tiles' granted extra they don't use (Promise)
}

// WithReserve fills Reserve from Total.
func (c Capacity) WithReserve() Capacity {
	c.Reserve = int64(float64(c.Total) * ReserveFraction)
	return c
}

// Promise is what a raised tile holds but doesn't use: its extra above the
// default, less what it already uses of it. A tile at its default promises
// nothing (the default is a fair share, not a reservation); a raise is a
// promise — the owner's "only the granted extra" rule (D203).
func Promise(eff, usage, def int64) int64 { return max(0, eff-max(usage, def)) }

// Room is how much more may be granted: the free room past the reserve and
// the other tiles' promises (negative when they already overlap).
func (c Capacity) Room() int64 { return c.Free - c.Reserve - c.Promised }

// Fit reports whether raising a limit from current to v fits the
// workspace, and why not. vCPUs are shared by weight, never reserved, so
// they only meet the maximum; with Total unknown nothing is refused but
// the maximum (the approver's call).
func (c Capacity) Fit(current, v int64) (bool, string) {
	if c.Max > 0 && v > c.Max {
		return false, "above the workspace's maximum of " + Format(c.Kind, c.Max)
	}
	need := v - current
	if need <= 0 || c.Kind.CPU() || c.Total == 0 || need <= c.Room() {
		return true, ""
	}
	why := fmt.Sprintf("needs +%s; %s free after the 10%% reserve", Format(c.Kind, need), Format(c.Kind, max(0, c.Free-c.Reserve)))
	if c.Promised > 0 {
		why += fmt.Sprintf(" and %s granted to other tiles but unused", Format(c.Kind, c.Promised))
	}
	return false, why
}

// Choices are the raises offered for a limit at current: +25, +50 (the
// default) and +100 %, each with whether it fits c. Approvable is the
// caller's to fill in — it depends on who is looking.
func Choices(k Kind, current int64, c Capacity) []Choice {
	out := make([]Choice, 0, len(Pcts))
	for _, p := range Pcts {
		v := Step(k, current, p)
		fits, why := c.Fit(current, v)
		out = append(out, Choice{Pct: p, Value: v, Fits: fits, Why: why, Default: p == DefaultPct})
	}
	return out
}

// ActionRaise is Action.Action's one value today.
const ActionRaise = "raise-limit"

// Action is what an alert about a cap offers (broker.Alert.Action): the
// tile (and, for disk, the scope), the kind, the limit it met and —
// decorated per viewer — the choices and who may approve them. Old shells
// ignore it and show the alert's message, which names /xbin/limits and
// bx limits.
type Action struct {
	Action    string   `json:"action"`
	Tile      string   `json:"tile,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Kind      Kind     `json:"kind"`
	Unit      string   `json:"unit"`
	Current   int64    `json:"current"`
	Approvers []string `json:"approvers,omitempty"`
	Choices   []Choice `json:"choices,omitempty"`
}
