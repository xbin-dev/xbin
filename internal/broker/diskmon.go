package broker

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/util"
)

// Disk containment (plans/isolation.md): a clumsy tile can't fill the shared
// data partition and break the workspace. A background scan measures each
// scope's resource footprint and the partition's free space, then:
//
//   - caps each scope at quotaBytes (default 50 GiB): over → its API resource
//     writes (kv/blob) are refused with 507;
//   - when the partition drops below reservePct free, refuses writes for the
//     biggest offenders (scopes over a fair share) to hold the reserve;
//   - raises alerts (per-tile over-quota, low-disk, active blocking) that the
//     admin tile and the shell surface prominently.
//
// Directly-mounted resources (sqlite/filesystem) can't be write-blocked at the
// API — they count toward a scope's usage and raise alerts, but stopping them
// is the admin's call (docs/isolation.md §disk).

const (
	defaultQuotaBytes = 50 << 30 // 50 GiB per scope
	reserveFraction   = 0.10     // hold at least 10% of the data partition free
	fairShareMin      = 5 << 30  // under pressure, spare scopes below ~5 GiB
)

// Alert is a workspace health notice for the admin tile / shell banner.
type Alert struct {
	Level   string `json:"level"`          // "warn" | "crit"
	Kind    string `json:"kind"`           // "disk-low" | "quota" | "blocking" | "oom" | "pids"
	Tile    string `json:"tile,omitempty"` // scope/component, when tile-specific
	Message string `json:"message"`
	System  bool   `json:"system"` // workspace-wide (shown to every user), vs tile-scoped
}

type diskMon struct {
	root       string
	quota      int64
	scopeUsage func() map[string]int64 // scopeKey → bytes (injected: uses resource dirs)
	extra      func() []Alert          // extra alerts (cgroup at-limit), optional

	mu       sync.RWMutex
	blocked  map[string]string // scopeKey → reason (over quota / low-disk offender)
	usage    map[string]int64  // scopeKey → bytes (last scan)
	alerts   []Alert
	freeB    int64
	totalB   int64
	lastScan time.Time
}

// envQuota reads XBIN_LIMIT_DISK (bytes, optional K/M/G/T suffix) for the
// per-scope disk cap, defaulting to 50 GiB.
func envQuota() int64 {
	v := strings.TrimSpace(os.Getenv("XBIN_LIMIT_DISK"))
	if v == "" {
		return defaultQuotaBytes
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1<<10, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1<<20, v[:len(v)-1]
	case 'g', 'G':
		mult, v = 1<<30, v[:len(v)-1]
	case 't', 'T':
		mult, v = 1<<40, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return defaultQuotaBytes
	}
	return n * mult
}

func newDiskMon(root string, quota int64, scopeUsage func() map[string]int64) *diskMon {
	if quota <= 0 {
		quota = defaultQuotaBytes
	}
	return &diskMon{root: root, quota: quota, scopeUsage: scopeUsage, blocked: map[string]string{}}
}

// run scans every interval until ctx-less stop isn't needed (daemon lifetime).
func (d *diskMon) run() {
	d.scan()
	for range time.Tick(45 * time.Second) {
		d.scan()
	}
}

func (d *diskMon) scan() {
	usage := d.scopeUsage()
	free, total := diskFree(d.root)

	blocked := map[string]string{}
	var alerts []Alert
	lowDisk := total > 0 && float64(free) < reserveFraction*float64(total)

	// Fair share for the pressure heuristic: an equal cut of the used space
	// among scopes that actually store anything.
	var used int64
	n := 0
	for _, u := range usage {
		if u > 0 {
			used += u
			n++
		}
	}
	fairShare := int64(fairShareMin)
	if n > 0 && used/int64(n) > fairShare {
		fairShare = used / int64(n)
	}

	for scope, u := range usage {
		switch {
		case u >= d.quota:
			blocked[scope] = fmt.Sprintf("over its %s quota", humanBytes(d.quota))
			alerts = append(alerts, Alert{Level: "crit", Kind: "quota", Tile: scope, System: false,
				Message: fmt.Sprintf("%s is over its %s disk quota (%s) — resource writes are blocked", scope, humanBytes(d.quota), humanBytes(u))})
		case lowDisk && u > fairShare:
			blocked[scope] = "workspace disk low — biggest users are write-blocked"
			alerts = append(alerts, Alert{Level: "warn", Kind: "quota", Tile: scope, System: false,
				Message: fmt.Sprintf("%s writes paused: workspace disk is low and %s is a top user (%s)", scope, scope, humanBytes(u))})
		}
	}
	if lowDisk {
		alerts = append([]Alert{{Level: "crit", Kind: "disk-low", System: true,
			Message: fmt.Sprintf("Workspace disk low: %s free of %s (< %d%%). Free space or the workspace may stop accepting data.",
				humanBytes(free), humanBytes(total), int(reserveFraction*100))}}, alerts...)
	}
	if len(blocked) > 0 && !lowDisk {
		alerts = append([]Alert{{Level: "warn", Kind: "blocking", System: true,
			Message: fmt.Sprintf("%d tile(s) are over quota and write-blocked — see the admin console.", len(blocked))}}, alerts...)
	}
	if d.extra != nil {
		alerts = append(alerts, d.extra()...)
	}

	d.mu.Lock()
	d.blocked, d.usage, d.alerts = blocked, usage, alerts
	d.freeB, d.totalB, d.lastScan = free, total, time.Now()
	d.mu.Unlock()
}

// Blocked reports whether a scope's resource writes are currently refused.
func (d *diskMon) Blocked(scopeKey string) (string, bool) {
	if d == nil {
		return "", false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	r, ok := d.blocked[scopeKey]
	return r, ok
}

// Status reports a scope's disk footprint, the quota, and whether writes are
// currently blocked — for the per-tile status API.
func (d *diskMon) Status(scopeKey string) (usage, quota int64, blocked bool) {
	if d == nil {
		return 0, 0, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, blocked = d.blocked[scopeKey]
	return d.usage[scopeKey], d.quota, blocked
}

// Alerts returns the active alerts (a copy).
func (d *diskMon) Alerts() []Alert {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]Alert(nil), d.alerts...)
}

func diskFree(path string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// apiAlerts returns the alerts this caller should see: workspace-wide (system)
// alerts to everyone, tile-specific alerts to admins and that tile's users.
// Powers the shell banner and the admin console.
func (b *Broker) apiAlerts(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	out := []Alert{}
	for _, a := range b.DiskAlerts() {
		if a.System || admin || (a.Tile != "" && p.CanUseTile(a.Tile)) {
			out = append(out, a)
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"alerts": out})
}

// quotaOK gates a resource *write* on the scope's disk state: over quota or a
// low-disk offender → 507 with the reason. Reads and non-writer access pass.
func (b *Broker) quotaOK(w http.ResponseWriter, scope, want string) bool {
	if want != "writer" {
		return true
	}
	if reason, blocked := b.disk.Blocked(util.ScopeKey(scope)); blocked {
		server.WriteJSON(w, http.StatusInsufficientStorage, map[string]string{
			"error": "disk write blocked: " + reason, "docs": "/docs/isolation.md",
		})
		return false
	}
	return true
}
