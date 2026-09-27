package boot

import (
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// sessionLimitAlerts is the at-limit alerts of the sessions with a cgroup
// leaf of their own (a VM's, a restricted user's; D112) — delta-tracked per
// leaf like the tiles', and forgotten once the session is gone.
func sessionLimitAlerts(cg *cgroup.Manager, reg *sbx.Registry, lastMem, lastPids map[string]int64) []broker.Alert {
	var out []broker.Alert
	seen := map[string]bool{}
	for _, e := range reg.List(sbx.Filter{}) {
		if e.Kind == sbx.Backend || e.Leaf == "" {
			continue // a backend is its tile's (the caller's loop)
		}
		seen[e.Leaf] = true
		mem, pids, ok := cg.AtLimit(e.Leaf)
		if !ok {
			continue
		}
		if mem > lastMem[e.Leaf] {
			out = append(out, broker.Alert{Level: "warn", Kind: "oom", Tile: e.Tile,
				Message: sessionWhat(e) + " hit its memory limit (was OOM-killed)"})
		}
		if pids > lastPids[e.Leaf] {
			out = append(out, broker.Alert{Level: "warn", Kind: "pids", Tile: e.Tile,
				Message: sessionWhat(e) + " hit its process (pids) limit — a runaway fork/spawn?"})
		}
		lastMem[e.Leaf], lastPids[e.Leaf] = mem, pids
	}
	for k := range lastMem {
		if !seen[k] {
			delete(lastMem, k)
			delete(lastPids, k)
		}
	}
	return out
}

// sessionWhat names a session for an alert: "a VM terminal of alice on apps/x".
func sessionWhat(e sbx.Entry) string {
	kind := "terminal"
	if e.Kind == sbx.Agent {
		kind = "agent session"
	}
	if e.Mode == sbx.VM {
		kind = "VM " + kind
	}
	s := "a " + kind
	if e.User != "" {
		s += " of " + e.User
	}
	return s + " on " + e.Tile
}
