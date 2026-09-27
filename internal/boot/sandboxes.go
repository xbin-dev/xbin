package boot

// sandboxes.go — GET /api/xbin/sandboxes: every sandbox xbind runs, how the
// host can run them, and what the sandbox layer refused or failed at (D112;
// the admin console's runtime → sandboxes tab). And the at-limit alerts of
// the sessions with a cgroup leaf of their own.

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vm"
)

// sandboxScope is what a caller of GET /sandboxes may see.
type sandboxScope struct {
	all  bool   // the host's health too
	tile string // one tile's ("" = every tile)
}

// sandboxScopeOf: an admin sees everything (?tile= narrows); anyone else,
// nothing yet — a tile listing its own sandboxes (plans/tile-sandboxes.md)
// is one more case here, without the host's health.
func (st *State) sandboxScopeOf(p auth.Principal, q url.Values) (sandboxScope, bool) {
	if st.Broker.IsAdmin(p) {
		return sandboxScope{all: true, tile: strings.Trim(q.Get("tile"), "/")}, true
	}
	return sandboxScope{}, false
}

func (st *State) registerSandboxAPI(srv *server.Server) {
	srv.RegisterAPI("GET /sandboxes", func(w http.ResponseWriter, r *http.Request) {
		sc, ok := st.sandboxScopeOf(auth.PrincipalOf(r), r.URL.Query())
		if !ok {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		server.WriteJSON(w, http.StatusOK, st.sandboxesView(sc))
	})
}

// sandboxRow is a registry entry as the admin sees it.
type sandboxRow struct {
	sbx.Entry
	Owner     string        `json:"owner,omitempty"`  // the tile's owner (users store)
	Name      string        `json:"name,omitempty"`   // a session's tab name
	Status    string        `json:"status,omitempty"` // an agent session's
	UptimeSec int64         `json:"uptimeSec"`
	Stats     *sandboxStats `json:"stats,omitempty"`
}

// sandboxStats is a row's latest sample. Scope "tile": a backend's tile leaf,
// shared by its generations (count it once); "sandbox": the session's own.
type sandboxStats struct {
	CPU   float64 `json:"cpu"`
	Mem   int64   `json:"mem"`
	Pids  int64   `json:"pids"`
	Scope string  `json:"scope"`
}

// vmView is the VM half of the health: what the probe found, the policy as
// effective and as stored (zero = default), and what running VMs hold — in
// all, the part tile sandboxes hold (their sub-budget), and per tile.
type vmView struct {
	vm.Health
	Policy    vm.Policy           `json:"policy"`
	Stored    vm.Policy           `json:"stored"`
	Used      vm.Usage            `json:"used"`
	UsedTiles vm.Usage            `json:"usedTiles"`
	UsedBy    map[string]vm.Usage `json:"usedBy"`
}

// sandboxDisk is a VM terminal disk on the host.
type sandboxDisk struct {
	vm.Disk
	Tile  string `json:"tile,omitempty"` // "" = no tile has that key now
	InUse bool   `json:"inUse"`
}

// disks are listed at most every 15 s (a glob and a stat per tile).
var sandboxDisks struct {
	sync.Mutex
	at   time.Time
	list []vm.Disk
}

func (st *State) listDisks() []vm.Disk {
	sandboxDisks.Lock()
	defer sandboxDisks.Unlock()
	if sandboxDisks.list == nil || time.Since(sandboxDisks.at) > 15*time.Second {
		sandboxDisks.list, sandboxDisks.at = vm.ListDisks(st.WS), time.Now()
	}
	return sandboxDisks.list
}

func (st *State) sandboxesView(sc sandboxScope) map[string]any {
	f := sbx.Filter{Tile: sc.tile}
	entries := st.Sbx.List(f)
	bySandbox, byTile, cg := st.Run.SandboxStats()
	owners := st.Users.Owners()
	rows := make([]sandboxRow, 0, len(entries))
	disksInUse := map[string]bool{}
	for _, e := range entries {
		row := sandboxRow{Entry: e, Owner: owners[e.Tile], UptimeSec: int64(time.Since(e.Started).Seconds())}
		if e.Disk != "" {
			disksInUse[e.Disk] = true
		}
		switch e.Kind {
		case sbx.Backend:
			row.Net = st.Broker.NetLabel(e.Tile).Effective
			if p, ok := byTile[e.Tile]; ok {
				row.Stats = &sandboxStats{CPU: p.CPU, Mem: p.Mem, Pids: p.Pids, Scope: "tile"}
			}
		default:
			if info, ok := st.Term.Info(e.ID); ok {
				row.Name, row.Status = info.Name, info.Status
			}
			if p, ok := bySandbox[e.ID]; ok {
				row.Stats = &sandboxStats{CPU: p.CPU, Mem: p.Mem, Pids: p.Pids, Scope: "sandbox"}
			}
		}
		rows = append(rows, row)
	}
	tiles := map[string]string{} // layer key → tile
	for _, c := range st.Reg.Components() {
		tiles[util.CompKey(c.Path)] = c.Path
	}
	disks := []sandboxDisk{}
	for _, d := range st.listDisks() {
		t := tiles[d.Key]
		if sc.tile != "" && t != sc.tile {
			continue
		}
		disks = append(disks, sandboxDisk{Disk: d, Tile: t, InUse: disksInUse[d.Path]})
	}
	out := map[string]any{
		"sandboxes": rows, "disks": disks, "failures": st.Sbx.Failures(f),
		"failureCounts": st.Sbx.FailureCounts(), "cgroup": cg, "intervalSec": 2,
	}
	if sc.all {
		out["health"] = map[string]any{
			"isolation": st.isolationHealth(),
			"vm": vmView{Health: st.VM.Health(), Policy: st.VM.Policy(), Stored: st.VM.StoredPolicy(),
				Used: st.VM.Used(), UsedTiles: st.VM.UsedTiles(), UsedBy: st.VM.UsedBy()},
		}
	}
	return out
}

// isolationHealth is how isolated this workspace's sandboxes are: tier 3
// (--isolate: every backend and terminal sandboxed), 2 (per-scope uids),
// 1 (none), and the pieces behind it.
func (st *State) isolationHealth() map[string]any {
	iso := st.isolationInfo()
	tier := 1
	switch {
	case st.Run.Isolate:
		tier = 3
	case iso["scopeUids"] == true:
		tier = 2
	}
	iso["tier"] = tier
	iso["cgroup"] = st.Run.Cgroup.Enabled()
	if st.Run.Isolate {
		iso["uidRange"], iso["uidRangeNote"] = st.uidRange, st.uidRangeNote
	}
	return iso
}

// isolationInfo is the part of /runtime's host that says how sandboxes are
// built here.
func (st *State) isolationInfo() map[string]any {
	return map[string]any{
		"isolate": st.Run.Isolate, "rootfs": st.Run.Rootfs, "scopeUids": st.Cfg.ScopeUIDs && st.priv.Euid() == 0,
		"protections": sandbox.DetectProtections(), // terminal mount/read guard availability
	}
}

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
