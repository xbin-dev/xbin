package broker

import (
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
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
	Kind  string `json:"kind"`           // "disk-low" | "quota" | "blocking" | "oom" | "pids"
	Tile  string `json:"tile,omitempty"` // scope/component, when tile-specific
	// Deployment names a data namespace's deployment when the alert is
	// about one beyond the primary's main: never System, admins only
	// (08-data §12).
	Deployment string `json:"deployment,omitempty"`
	Message    string `json:"message"`
	System     bool   `json:"system"` // workspace-wide (shown to every user), vs tile-scoped
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
	return &diskMon{root: root, quota: quota, scopeUsage: scopeUsage, free: diskFree, blocked: map[string]string{}, stop: make(chan struct{})}
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
		case <-d.stop:
			return
		}
	}
}

func (d *diskMon) close() { d.stopOnce.Do(func() { close(d.stop) }) }

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
		if bk.nonPrimary {
			firsts = append(firsts, a) // the alert names non-primary namespaces first (08-data §12)
		} else {
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
	if admin {
		if a, bad := b.policiesAlert(); bad { // an unreadable data/workspace-policies.json (PD-55)
			out = append(out, a)
		}
		out = append(out, b.backupKeyAlerts()...) // keys no export holds yet: admins only (backupkeys_status.go)
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

// ---- tile deployments: namespace buckets, limits, the per-tile quota ----

const (
	// deployStoreQuota bounds one tile's deployment state: its checkpoint
	// store, view repository, materialized trees (the protected primary's
	// build products included) and per-checkpoint build artifacts
	// (07-runtime §2.5, NP-07-7).
	deployStoreQuota = 10 << 30
	deployStoreWarn  = 0.9 // the alert's threshold
	maxWalkDepth     = 1024
)

// nsBucket is the rule of one data namespace whose rule isn't today's: one
// beyond main, or main's when a claimant lowered its limit or it isn't the
// scope's primary.
type nsBucket struct {
	limit      int64  // bytes: the lowest limit its claimants set, 0 for none (the scope's quota)
	nonPrimary bool   // its deployment isn't P(S): write-blocked first on low disk (D127q)
	label      string // how messages name it
	tile       string // the alert's tile: beyond main its quota key, which no component path equals; "" for a non-primary main
	alertDep   string // the alert's deployment: set for every namespace but the primary's main
}

// deployFacts is what a scan measures beyond today's: usage of every
// namespace beyond main (quota key → bytes), the rules that aren't today's,
// and each tile's deployment state (tile → bytes; tiles without any are
// absent).
type deployFacts struct {
	usage   map[string]int64
	buckets map[string]nsBucket
	stores  map[string]int64
}

// SetDeploymentQuota installs the deployments plane's records (its Lookup)
// as the source of per-deployment disk limits (D127n), and with them turns on
// the monitor's tile-deployment rules, from the next scan: the namespaces
// beyond main, non-primary namespaces blocked first on low disk, and the
// per-tile quota (08-data §12; 07-runtime §2.5). Boot calls it once the
// plane's answers are installed; until then a scan is today's.
func (b *Broker) SetDeploymentQuota(records func(tile string) deployments.Found) {
	if b.disk == nil || records == nil {
		return
	}
	b.disk.mu.Lock()
	b.disk.deploy = func(d *diskMon, seen map[string]bool) deployFacts { return b.deployFacts(d, records, seen) }
	b.disk.mu.Unlock()
}

// deployFacts measures the tile-deployment half of a scan.
func (b *Broker) deployFacts(d *diskMon, records func(string) deployments.Found, seen map[string]bool) deployFacts {
	fx := deployFacts{usage: map[string]int64{}, buckets: map[string]nsBucket{}, stores: map[string]int64{}}
	limits := map[nsID]int64{} // the lowest limit each namespace's claimants set
	for _, c := range b.Reg.Components() {
		if u := b.storeUsage(d, c.Path, seen); u > 0 {
			fx.stores[c.Path] = u
		}
		rec := records(c.Path).Record
		if rec == nil || c.Scope == "" { // the workspace's namespace is no tile's to limit
			continue
		}
		for dep, dr := range rec.Deployments {
			if v := diskLimitBytes(dr); v > 0 {
				if id := nsOf(c.Scope, dep); limits[id] == 0 || v < limits[id] {
					limits[id] = v
				}
			}
		}
	}
	b.eachNamespace(func(id nsID) { // a namespace is measured as its root, its kv file included
		if k, err := id.keys(); err == nil {
			fx.usage[k.Quota], _ = treeUsage(filepath.Join(b.Reg.Root, filepath.FromSlash(k.Enc)))
			fx.buckets[k.Quota] = b.bucketOf(id, limits[id])
		}
	})
	for scope := range b.scopesAndWorkspace() {
		id := nsOf(scope, util.MainDeployment)
		if bk := b.bucketOf(id, limits[id]); bk.limit > 0 || bk.nonPrimary {
			k, _ := scopeKeys(scope, util.MainDeployment)
			fx.buckets[k.Quota] = bk
		}
	}
	return fx
}

// scopesAndWorkspace is every scope path, the workspace's ("") included.
func (b *Broker) scopesAndWorkspace() map[string]bool {
	out := map[string]bool{"": true}
	for s := range b.Reg.Scopes() {
		out[s] = true
	}
	return out
}

// diskLimitBytes is a deployment's diskGiB override in bytes; 0 for none or a
// value that isn't a positive number of GiB.
func diskLimitBytes(dr *deployments.DeploymentRecord) int64 {
	if dr == nil {
		return 0
	}
	if v := dr.Limits[deployments.LimitDiskGiB]; v > 0 && v <= 1<<33 {
		return v << 30
	}
	return 0
}

// bucketOf is namespace id's rule: limit, the lowest its claimants set, and
// its role, from P(S) (08-data §12 item 3: main counts as non-primary while
// P(S) isn't main).
func (b *Broker) bucketOf(id nsID, limit int64) nsBucket {
	bk := nsBucket{limit: limit, nonPrimary: id.dep != b.scopePrimary(id.scope)}
	named := fmt.Sprintf("%s's %q data", cmp.Or(id.scope, "the workspace"), id.dep)
	switch k, _ := scopeKeys(id.scope, id.dep); {
	case !id.main():
		bk.label, bk.tile, bk.alertDep = named, k.Quota, id.dep
	case bk.nonPrimary: // no tile field: /tile-status matches it to a component path
		bk.label, bk.alertDep = named, id.dep
	default: // the primary's main with a lowered limit: today's alert
		bk.label, bk.tile = k.Quota, k.Quota
	}
	return bk
}

// namespaceQuota is the quota of the namespace whose quota key is quotaKey:
// the lowest limit its claimants set (at the last scan), never above the
// scope's quota. Seed preflight's target limit (08-data §12 item 4).
func (d *diskMon) namespaceQuota(quotaKey string) int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if bk, ok := d.buckets[quotaKey]; ok && bk.limit > 0 && bk.limit < d.quota {
		return bk.limit
	}
	return d.quota
}

// DeploymentDiskStatus is TileDiskStatus for deployment dep of tile ("" is
// main): the footprint of the data namespace it reaches, (scope, dep), that
// namespace's quota, and whether its writes are blocked (08-data §12). A
// tile in the workspace scope reaches the workspace's, which is never split.
// For /tile-status?deployment= and the deployment's effective limits.
func (b *Broker) DeploymentDiskStatus(tile, dep string) (usage, quota int64, blocked bool) {
	d := b.disk
	if d == nil {
		return 0, 0, false
	}
	scope := ""
	if c, ok := b.Reg.Component(tile); ok {
		scope = c.Scope
	}
	if scope == "" {
		dep = util.MainDeployment
	}
	k, err := scopeKeys(scope, dep)
	if err != nil {
		return 0, 0, false
	}
	quota = d.namespaceQuota(k.Quota)
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, blocked = d.blocked[k.Quota]
	return d.usage[k.Quota], quota, blocked
}

// DiskQuota is the per-scope disk quota in bytes (XBIN_LIMIT_DISK, or the
// default): a tile's disk ceiling, which a deployment's diskGiB never
// exceeds (D127n), for the plane's limits op (GovHooks.DiskCeiling).
func (b *Broker) DiskQuota() int64 {
	if b.disk == nil {
		return defaultQuotaBytes
	}
	return b.disk.quota
}

// DiskLimitCheck judges a tile manager's diskGiB for deployment dep of tile,
// for POST /deployments/limits (11-contract §1.7, §1.14) (D127n): a positive
// number of GiB, at most the tile's ceiling (the scope quota,
// XBIN_LIMIT_DISK), set on the tile that roots its scope, since the (scope,
// name) namespace is the scope's.
func (b *Broker) DiskLimitCheck(tile, dep string, gib int64) error {
	ceiling := int64(defaultQuotaBytes)
	if b.disk != nil {
		ceiling = b.disk.quota
	}
	c, ok := b.Reg.Component(tile)
	switch {
	case !ok:
		return nsErr(http.StatusNotFound, "", "no such tile: "+tile)
	case gib <= 0:
		return nsErr(http.StatusBadRequest, "", "diskGiB takes a positive integer")
	case gib > ceiling>>30:
		return nsErr(http.StatusBadRequest, "", fmt.Sprintf("diskGiB can't exceed the tile's ceiling (%d)", ceiling>>30))
	case c.Scope != tile:
		root, detail := c.Scope, ""
		if _, isTile := b.Reg.Component(c.Scope); !isTile || c.Scope == "" {
			root, detail = cmp.Or(c.Scope, "the workspace"), " — no tile roots it, so it stays at the tile's ceiling"
		}
		return nsErr(http.StatusConflict, deployments.KindPolicy, fmt.Sprintf("the quota of %s's %q data is set on %s%s",
			cmp.Or(c.Scope, "the workspace"), cmp.Or(dep, util.MainDeployment), root, detail))
	}
	return nil
}

// DeployStoreRoom answers whether tile's deployment state has room under
// its per-tile quota, for a new capture or materialization (07-runtime
// §2.5): nil below the quota at the last scan; at it, the state is measured
// again (a GC may have freed some), and still full is a 507 with the
// reason. Running deployments keep serving either way. nil until
// SetDeploymentQuota.
func (b *Broker) DeployStoreRoom(tile string) error {
	d := b.disk
	if d == nil {
		return nil
	}
	d.mu.RLock()
	on, u := d.deploy != nil, d.stores[tile]
	d.mu.RUnlock()
	if !on || u < deployStoreQuota {
		return nil
	}
	u = b.storeUsage(d, tile, nil)
	d.mu.Lock()
	if d.stores != nil {
		d.stores[tile] = u
	}
	d.mu.Unlock()
	if u < deployStoreQuota {
		return nil
	}
	return nsErr(http.StatusInsufficientStorage, deployments.KindState, fmt.Sprintf(
		"%s's checkpoints, materialized trees and build artifacts use %s of their %s quota: remove a deployment or purge its history to free space",
		tile, humanBytes(u), humanBytes(deployStoreQuota)))
}

// storeAlerts are the per-tile quota's alerts: a warning from 90%, critical
// when full. Tile-scoped, like a scope's quota alert.
func storeAlerts(stores map[string]int64) []Alert {
	var out []Alert
	for _, tile := range slices.Sorted(maps.Keys(stores)) {
		switch u := stores[tile]; {
		case u >= deployStoreQuota:
			out = append(out, Alert{Level: "crit", Kind: "quota", Tile: tile, Message: fmt.Sprintf(
				"%s's checkpoints, materialized trees and build artifacts fill their %s quota (%s): new captures and materializations are refused until GC or a purge frees space; running deployments keep serving",
				tile, humanBytes(deployStoreQuota), humanBytes(u))})
		case float64(u) >= deployStoreWarn*deployStoreQuota:
			out = append(out, Alert{Level: "warn", Kind: "quota", Tile: tile, Message: fmt.Sprintf(
				"%s's checkpoints, materialized trees and build artifacts use %s of their %s quota", tile, humanBytes(u), humanBytes(deployStoreQuota))})
		}
	}
	return out
}

// storeUsage is tile's deployment state on disk (07-runtime §2.5): its
// checkpoint store and view repository; under .xbin/deploy/<TileKey>/ its
// materialized trees, in-progress ones and the protected primary's build
// products, but not d/ (its deployments' logs and sandbox state); and its
// per-checkpoint build artifacts, .xbin/build/<CompKey>/c/ (the runner's
// buildcheckpoint.go). 0 for a tile in the zero state, which has none.
func (b *Broker) storeUsage(d *diskMon, tile string, seen map[string]bool) int64 {
	cs := checkpoint.Store{Root: b.Reg.Root}
	arts := filepath.Join(b.Reg.Root, ".xbin", "build", util.CompKey(tile), "c")
	store, _ := treeUsage(cs.Dir(tile))
	view, _ := treeUsage(cs.ViewDir(tile))
	return store + view + d.treesUsage(cs.TreesDir(tile), "d", seen) + d.treesUsage(arts, "", seen)
}

// treeSize is a content-addressed tree's measured size, valid while its
// directory keeps the inode it was measured at.
type treeSize struct {
	ino  uint64
	size int64
}

// treesUsage is treeUsage of a directory of content-addressed trees: a child
// named by a full tree id is complete and never changes once renamed into
// place, so its size is kept by path and inode and walked again only when it
// is replaced. skip names a child that isn't counted; seen collects the
// trees still present.
func (d *diskMon) treesUsage(dir, skip string, seen map[string]bool) (n int64) {
	fd, err := openDirNoFollow(unix.AT_FDCWD, dir)
	if err != nil {
		return 0
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	names, _ := f.Readdirnames(-1)
	for _, name := range names {
		var st unix.Stat_t
		if name == skip || unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			n += st.Size
			continue
		}
		p, memo := filepath.Join(dir, name), isTreeID(name)
		if memo {
			if seen != nil {
				seen[p] = true
			}
			if v, ok := d.trees.Load(p); ok && v.(treeSize).ino == uint64(st.Ino) {
				n += v.(treeSize).size
				continue
			}
		}
		cfd, err := openDirNoFollow(fd, name)
		if err != nil {
			continue
		}
		size, _ := usageAt(cfd, p, 1)
		if memo {
			d.trees.Store(p, treeSize{ino: uint64(st.Ino), size: size})
		}
		n += size
	}
	return n
}

// isTreeID reports whether name is a full git object id (SHA-1 or SHA-256
// hex), the name of a materialized tree or an artifact directory.
func isTreeID(name string) bool {
	if len(name) != 40 && len(name) != 64 {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// treeUsage is the bytes of every entry under dir, and how many there are
// besides directories, never following a symlink (06-security C5;
// 07-runtime §2.5): each directory is opened relative to its parent with
// O_NOFOLLOW|O_DIRECTORY, so one swapped for a link or a FIFO mid-walk fails
// to open instead of being followed or blocking, and entries are sized with
// fstatat(AT_SYMLINK_NOFOLLOW) (a link's own size, as dirUsage counts it). A
// missing dir is 0; entries deeper than maxWalkDepth aren't counted.
func treeUsage(dir string) (size int64, files int) {
	fd, err := openDirNoFollow(unix.AT_FDCWD, dir)
	if err != nil {
		return 0, 0
	}
	return usageAt(fd, dir, 0)
}

func openDirNoFollow(at int, name string) (int, error) {
	return unix.Openat(at, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
}

// usageAt sums the directory open at fd (named name, depth levels down),
// and closes it.
func usageAt(fd int, name string, depth int) (size int64, files int) {
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	for {
		names, err := f.Readdirnames(256)
		for _, e := range names {
			var st unix.Stat_t
			if unix.Fstatat(fd, e, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			if st.Mode&unix.S_IFMT != unix.S_IFDIR {
				size, files = size+st.Size, files+1
				continue
			}
			if depth < maxWalkDepth {
				if cfd, err := openDirNoFollow(fd, e); err == nil {
					s, n := usageAt(cfd, name+"/"+e, depth+1)
					size, files = size+s, files+n
				}
			}
		}
		if err != nil || len(names) == 0 {
			return size, files
		}
	}
}
