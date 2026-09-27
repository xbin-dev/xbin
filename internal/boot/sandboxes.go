package boot

// sandboxes.go — GET /api/xbin/sandboxes: every sandbox xbind runs, how the
// host can run them, and what the sandbox layer refused or failed at (D112;
// the admin console's runtime → sandboxes tab). And the at-limit alerts of
// the sessions with a cgroup leaf of their own.
//
// Rows follow the registry's name rule (P17): main's backend rows are what
// they were before tile deployments; another deployment's carry its name,
// under the tile's path, with stats of its own leaf (P13).

import (
	"math"
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
	dep  string // one deployment's ("" = every deployment's; "main" = main's)
}

// sandboxScopeOf: an admin sees everything (?tile= and ?deployment=
// narrow); anyone else, nothing yet — a tile listing its own sandboxes
// (plans/tile-sandboxes.md) is one more case here, without the host's
// health.
func (st *State) sandboxScopeOf(p auth.Principal, q url.Values) (sandboxScope, bool) {
	if st.Broker.IsAdmin(p) {
		return sandboxScope{all: true, tile: strings.Trim(q.Get("tile"), "/"), dep: q.Get("deployment")}, true
	}
	return sandboxScope{}, false
}

// badDeploymentName is the error catalogue's text for a malformed name.
const badDeploymentName = `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`

func (st *State) registerSandboxAPI(srv *server.Server) {
	srv.RegisterAPI("GET /sandboxes", func(w http.ResponseWriter, r *http.Request) {
		sc, ok := st.sandboxScopeOf(auth.PrincipalOf(r), r.URL.Query())
		if !ok {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		if sc.dep != "" && !util.DeploymentNameOK(sc.dep) {
			server.WriteError(w, http.StatusBadRequest, badDeploymentName, "/docs/protocol.md")
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

// sandboxStats is a row's latest sample. Scope "tile": a main backend's —
// its tile's, or, while the tile runs another deployment, its own leaf's —
// shared by its generations (count it once per leaf); "deployment": another
// deployment's backend leaf, the same way; "sandbox": the session's own.
type sandboxStats struct {
	CPU   float64 `json:"cpu"`
	Mem   int64   `json:"mem"`
	Pids  int64   `json:"pids"`
	Scope string  `json:"scope"`
}

// vmView is the VM half of the health: what the probe found, the policy as
// effective and as stored (zero = default), and what running VMs hold.
type vmView struct {
	vm.Health
	Policy vm.Policy           `json:"policy"`
	Stored vm.Policy           `json:"stored"`
	Used   vm.Usage            `json:"used"`
	UsedBy map[string]vm.Usage `json:"usedBy"`
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
	f := sbx.Filter{Tile: sc.tile, Deployment: sc.dep}
	entries := st.Sbx.List(f)
	bySandbox, byTile, cg := st.Run.SandboxStats()
	split, byGen := st.deploymentStats(sc.tile)
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
			if split[e.Tile] {
				row.Stats = byGen[genStatsKey(e)]
			} else if p, ok := byTile[e.Tile]; ok {
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
	if sc.dep != "" {
		out["deployment"] = sc.dep // the echo (12-compat NP-12-2)
	}
	if sc.all {
		out["health"] = map[string]any{
			"isolation": st.isolationHealth(),
			"vm": vmView{Health: st.VM.Health(), Policy: st.VM.Policy(), Stored: st.VM.StoredPolicy(),
				Used: st.VM.Used(), UsedBy: st.VM.UsedBy()},
		}
	}
	return out
}

// deploymentStats samples the backends of the tiles that run a deployment
// beyond main (split; tile "" = every tile): each deployment's generations by
// the leaf they share — main's with scope "tile", the others' "deployment"
// — so a tile's leaves each count once and main's row never repeats another
// deployment's use. Zero-state and main-only tiles keep their tile's series.
func (st *State) deploymentStats(tile string) (split map[string]bool, byGen map[string]*sandboxStats) {
	backends := st.Sbx.List(sbx.Filter{Tile: tile, Kind: sbx.Backend})
	split = map[string]bool{}
	for _, e := range backends {
		if e.Deployment != "" {
			split[e.Tile] = true
		}
	}
	byGen = map[string]*sandboxStats{}
	if len(split) == 0 {
		return split, byGen
	}
	var gens []sbx.Entry
	for _, e := range backends {
		if split[e.Tile] {
			gens = append(gens, e)
		}
	}
	now := time.Now()
	scopes := map[string]string{}
	for _, e := range gens {
		scopes[genStatsKey(e)] = "tile"
		if e.Deployment != "" {
			scopes[genStatsKey(e)] = "deployment"
		}
	}
	for k, u := range st.Run.GenUsage(gens, genStatsKey) {
		byGen[k] = &sandboxStats{CPU: genCPURate(k, u.CPUUsec, now), Mem: u.MemCurrent, Pids: u.PidsCurrent, Scope: scopes[k]}
	}
	return split, byGen
}

// genStatsKey groups a backend generation with the others of its deployment
// that share its leaf.
func genStatsKey(e sbx.Entry) string { return e.Tile + "\x00" + e.Deployment + "\x00" + e.Leaf }

// genCPU keeps each deployment stats group's last CPU reading: its rate is
// taken between two polls (the tab polls every intervalSec), and a poll
// within a second of the last repeats its rate.
var genCPU struct {
	sync.Mutex
	last map[string]cpuReading
}

type cpuReading struct {
	at   time.Time
	usec int64
	pct  float64
}

// genCPURate is group k's CPU, percent of one core, since its last reading.
func genCPURate(k string, usec int64, now time.Time) float64 {
	genCPU.Lock()
	defer genCPU.Unlock()
	if genCPU.last == nil {
		genCPU.last = map[string]cpuReading{}
	}
	for key, r := range genCPU.last {
		if now.Sub(r.at) > time.Minute {
			delete(genCPU.last, key)
		}
	}
	prev, ok := genCPU.last[k]
	dt := now.Sub(prev.at).Seconds()
	if ok && dt < 1 {
		return prev.pct
	}
	pct := 0.0
	if ok && usec >= prev.usec {
		pct = math.Round(float64(usec-prev.usec)/dt/1e4*10) / 10 // µs/s → % of one core
	}
	genCPU.last[k] = cpuReading{at: now, usec: usec, pct: pct}
	return pct
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
