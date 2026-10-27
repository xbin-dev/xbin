package broker

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/server"
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
//
// Tile deployments (08-data §12; 07-runtime §2.5) add, once the plane's
// records are installed (SetDeploymentQuota): every data namespace beyond
// main as a quota bucket of its own, at the lowest limit its claimants set,
// never above the scope's quota (D127n); low disk write-blocking non-primary
// namespaces first (D127q); and a per-tile quota on each tile's deployment
// state. main's figures and blocks stay today's.

const (
	defaultQuotaBytes = 50 << 30 // 50 GiB per scope
	reserveFraction   = 0.10     // hold at least 10% of the data partition free
	fairShareMin      = 5 << 30  // under pressure, spare scopes below ~5 GiB
)

// Alert is a workspace health notice for the admin tile / shell banner.
type Alert struct {
	Level string `json:"level"`          // "warn" | "crit"
	Kind  string `json:"kind"`           // "disk-low" | "quota" | "blocking" | "oom" | "pids" | "go-build-versions"
	Tile  string `json:"tile,omitempty"` // scope/component, when tile-specific
	// Deployment names a data namespace's deployment when the alert is
	// about one beyond the primary's main: never System, admins only
	// (08-data §12).
	Deployment string `json:"deployment,omitempty"`
	Message    string `json:"message"`
	System     bool   `json:"system"` // workspace-wide (shown to every user), vs tile-scoped
	// Dismiss is the /api/xbin route a POST to which dismisses the alert,
	// for one that can be ("" = it goes away only when its cause does).
	Dismiss string `json:"dismiss,omitempty"`
	// Action offers raising the cap the alert is about (D203): set by the
	// alert's source, its choices and approvers decorated per viewer.
	Action *limits.Action `json:"action,omitempty"`
}

type diskMon struct {
	root       string
	quota      int64
	scopeUsage func() map[string]int64 // scopeKey → bytes (injected: uses resource dirs)
	extra      func() []Alert          // extra alerts (cgroup at-limit), optional
	// sbxUsage is each tile's tile-sandbox bytes (plans/tile-sandbox-runtime.md
	// §9): they count for disk pressure — the fair share — only, never for a
	// scope's quota or its write blocking (a manager's kv writes must not be
	// blocked by its sandboxes). onLow is told every low-disk verdict: the
	// runtime stops the running namespace sandboxes above the fair share.
	sbxUsage func() map[string]int64
	onLow    func()
	free     func(path string) (free, total int64) // the partition's space: diskFree (tests stand in)
	stop     chan struct{}                         // closed by close(): the scan loop ends
	kicks    chan struct{}                         // kick(): one rescan now (a raised quota lifts its block)
	stopOnce sync.Once

	// Tile deployments: SetDeploymentQuota's measure (nil: a scan is
	// today's), and the content-addressed trees it measured, path → treeSize.
	deploy func(d *diskMon, seen map[string]bool) deployFacts
	trees  sync.Map

	mu        sync.RWMutex
	blocked   map[string]string   // scopeKey → reason (over quota / low-disk offender)
	usage     map[string]int64    // scopeKey → bytes (last scan)
	buckets   map[string]nsBucket // quota key → the rule of a namespace whose rule isn't today's (last scan)
	stores    map[string]int64    // tile → its deployment state's bytes (last scan)
	alerts    []Alert
	freeB     int64
	totalB    int64
	low       bool  // the last scan's verdict: the partition is below its reserve
	fairShare int64 // the last scan's fair share (tile sandboxes' bytes included)
	lastScan  time.Time
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
	return &diskMon{root: root, quota: quota, scopeUsage: scopeUsage, free: diskFree, blocked: map[string]string{}, stop: make(chan struct{}), kicks: make(chan struct{}, 1)}
}

// run scans every interval until stop() (the daemon's shutdown).
func (d *diskMon) run() {
	d.scan()
	t := time.NewTicker(45 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.scan()
		case <-d.kicks:
			d.scan()
		case <-d.stop:
			return
		}
	}
}

func (d *diskMon) close() { d.stopOnce.Do(func() { close(d.stop) }) }

// kick asks the scan loop for one more scan now, without waiting: a kick
// while one is queued is the same scan.
func (d *diskMon) kick() {
	if d == nil {
		return
	}
	select {
	case d.kicks <- struct{}{}:
	default:
	}
}

func (d *diskMon) scan() {
	usage := maps.Clone(d.scopeUsage())
	if usage == nil {
		usage = map[string]int64{}
	}
	var sbx map[string]int64
	if d.sbxUsage != nil {
		sbx = d.sbxUsage()
	}
	free, total := d.free(d.root)
	d.mu.RLock()
	deploy := d.deploy
	d.mu.RUnlock()
	var fx deployFacts
	if deploy != nil {
		seen := map[string]bool{}
		fx = deploy(d, seen)
		maps.Copy(usage, fx.usage) // keys beyond main start with ".": never a scope key
		d.trees.Range(func(p, _ any) bool {
			if !seen[p.(string)] {
				d.trees.Delete(p)
			}
			return true
		})
	}
	bucket := func(key string) nsBucket {
		if bk, ok := fx.buckets[key]; ok {
			return bk
		}
		return nsBucket{tile: key, label: key} // today's rule
	}

	blocked := map[string]string{}
	var alerts, firsts []Alert
	lowDisk := lowAt(free, total)

	// Fair share for the pressure heuristic: an equal cut of the used space
	// among primary namespaces that actually store anything — and tiles whose
	// sandboxes do, which count here and nowhere else; non-primary usage
	// never moves a primary's share (D127q).
	var used int64
	n := 0
	for key, u := range usage {
		if u > 0 && !bucket(key).nonPrimary {
			used += u
			n++
		}
	}
	for _, u := range sbx {
		if u > 0 {
			used += u
			n++
		}
	}
	fairShare := int64(fairShareMin)
	if n > 0 && used/int64(n) > fairShare {
		fairShare = used / int64(n)
	}

	primaryBlocked := 0
	for _, scope := range slices.Sorted(maps.Keys(usage)) {
		u, bk := usage[scope], bucket(scope)
		quota := d.quota
		if bk.limit > 0 && bk.limit < quota {
			quota = bk.limit
		}
		var a Alert
		switch {
		case u >= quota:
			blocked[scope] = fmt.Sprintf("over its %s quota", humanBytes(quota))
			a = Alert{Level: "crit", Kind: "quota",
				Message: fmt.Sprintf("%s is over its %s disk quota (%s) — resource writes are blocked", bk.label, humanBytes(quota), humanBytes(u))}
		case lowDisk && bk.nonPrimary && u > 0:
			blocked[scope] = "workspace disk low — non-primary deployments' data is write-blocked first"
			a = Alert{Level: "warn", Kind: "quota",
				Message: fmt.Sprintf("%s writes paused: workspace disk is low and non-primary deployments' data is write-blocked first (%s)", bk.label, humanBytes(u))}
		case lowDisk && !bk.nonPrimary && u > fairShare:
			blocked[scope] = "workspace disk low — biggest users are write-blocked"
			a = Alert{Level: "warn", Kind: "quota",
				Message: fmt.Sprintf("%s writes paused: workspace disk is low and %s is a top user (%s)", bk.label, bk.label, humanBytes(u))}
		default:
			continue
		}
		a.Tile, a.Deployment = bk.tile, bk.alertDep
		switch {
		case bk.nonPrimary:
			firsts = append(firsts, a) // the alert names non-primary namespaces first (08-data §12)
		case bk.partition: // one person's: admins' alert only, never in the count every tile shows (PD-46)
			alerts = append(alerts, a)
		default:
			primaryBlocked++
			alerts = append(alerts, a)
		}
	}
	alerts = append(append(firsts, alerts...), storeAlerts(fx.stores)...)
	if lowDisk {
		alerts = append([]Alert{{Level: "crit", Kind: "disk-low", System: true,
			Message: fmt.Sprintf("Workspace disk low: %s free of %s (< %d%%). Free space or the workspace may stop accepting data.",
				humanBytes(free), humanBytes(total), int(reserveFraction*100))}}, alerts...)
	}
	// Every tile's /tile-status shows this count, so it counts primary
	// namespaces only: no tile-visible figure reveals a non-primary one.
	if primaryBlocked > 0 && !lowDisk {
		alerts = append([]Alert{{Level: "warn", Kind: "blocking", System: true,
			Message: fmt.Sprintf("%d tile(s) are over quota and write-blocked — see the admin console.", primaryBlocked)}}, alerts...)
	}
	if d.extra != nil {
		alerts = append(alerts, d.extra()...)
	}

	d.mu.Lock()
	d.blocked, d.usage, d.alerts = blocked, usage, alerts
	d.buckets, d.stores = fx.buckets, fx.stores
	d.freeB, d.totalB, d.low, d.fairShare, d.lastScan = free, total, lowDisk, fairShare, time.Now()
	d.mu.Unlock()
	if lowDisk && d.onLow != nil {
		d.onLow()
	}
}

// lowAt is the low-disk rule: the partition below reserveFraction free.
func lowAt(free, total int64) bool {
	return total > 0 && float64(free) < reserveFraction*float64(total)
}

// FairShare is the last scan's fair share (at least fairShareMin): under
// low disk, a tile whose sandboxes hold more has them stopped.
func (d *diskMon) FairShare() int64 {
	if d == nil {
		return fairShareMin
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return max(d.fairShare, fairShareMin)
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

// Low reports the last scan's verdict: the data partition is below its
// reserve (reserveFraction free).
func (d *diskMon) Low() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.low
}

// DiskLow is the disk monitor's last verdict on the workspace partition:
// below its reserve. Tile sandboxes don't start meanwhile
// (plans/tile-sandbox-runtime.md §6.3).
func (b *Broker) DiskLow() bool { return b.disk.Low() }

// DiskLowAt is the same verdict over a statfs taken now (free and total
// bytes): the tile-sandbox runtime's own 5 s watch.
func (b *Broker) DiskLowAt(free, total int64) bool { return lowAt(free, total) }

// DiskFairShare is the last scan's fair share, tile sandboxes' bytes
// included.
func (b *Broker) DiskFairShare() int64 { return b.disk.FairShare() }

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
// An alert naming a deployment (a data namespace beyond the primary's main)
// reaches admins only (08-data §12). Powers the shell banner and the admin
// console.
func (b *Broker) apiAlerts(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	out := []Alert{}
	for _, a := range b.DiskAlerts() {
		switch {
		case a.Deployment != "" && !admin:
		case a.System || admin || (a.Tile != "" && p.CanReadTile(a.Tile)):
			out = append(out, a)
		}
	}
	out = append(out, b.partitionAlerts(p, admin)...)      // switch requests: admins and the tile's readers (partitionswitch.go)
	out = append(out, b.partitionTrustAlerts(p, admin)...) // who can change a partitioned tile's code (partitiontrust.go)
	if admin {
		if a, bad := b.settingsAlert(); bad { // a workspace setting that can't be read (D180; policies.go)
			out = append(out, a)
		}
		out = append(out, b.backupKeyAlerts()...) // keys no export holds yet: admins only (backupkeys_status.go)
		out = append(out, b.consentAlerts()...)   // consent files this xbind can't read (partitionconsent.go)
		if b.AdminAlerts != nil {
			out = append(out, b.AdminAlerts()...) // the Go build versions alert (D166)
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"alerts": out})
}

// quotaOK gates a resource *write* on its namespace's disk state (quotaKey:
// resKeys.quotaKey, the scope's data key in main): over quota or a low-disk
// offender → 507 with the reason. Reads and non-writer access pass.
func (b *Broker) quotaOK(w http.ResponseWriter, quotaKey, want string) bool {
	if want != "writer" {
		return true
	}
	if reason, blocked := b.disk.Blocked(quotaKey); blocked {
		server.WriteError(w, http.StatusInsufficientStorage, "disk write blocked: "+reason, "/docs/isolation.md")
		return false
	}
	return true
}
