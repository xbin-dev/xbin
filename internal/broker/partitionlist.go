package broker

// partitionlist.go — GET /api/xbin/partitions (plans/partitions/06 §6;
// PD-46, S19): what xbind says about partitioned tiles, per audience.
//
//	GET /api/xbin/partitions?tile=<t>
//	  → {features, tile, state, spec, request, policies, limits, totals?,
//	     partitions: [rows], binds?, orphans?}
//	GET /api/xbin/partitions
//	  → {features, policies, isolated, tiles: [{tile, state, spec, request, …}]}
//
// features names what this xbind serves (a client reads it rather than
// guess from a version; a 404 means an xbind without partitions).
//
// Who sees what of a tile (PD-46):
//   - anyone who can read it: its state, spec and request;
//   - the person: their own row — state, running, lastStarted, bytes,
//     registration counts, missed ticks, dormant drops, their ledger totals,
//     consents (policy on), personal binds on the tile, their log share —
//     and the trust panel (who can change the code that runs on their data);
//   - the tile's writers and managers: totals only;
//   - admins: every person's metadata row, the personal-bind rows and the
//     orphans. Never contents, vault key names, log lines or mail (a row's
//     mail counts are metadata: 04 §3).
//
// A tile's credentials (its frames, backend, terminals) get the tile-level
// fields and features only: tile code never reads people's metadata. The
// admin tile's frame under a person's login (AdminFrameDriver) is the
// exception: the admin console reads an admin's view (partitionadmin.go).

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// The features this xbind's partitions API serves (06 §6).
var partitionFeatures = []string{"partitions/1", "mode-switch/1", "consents/1", "personal-binds/1", GlobalAddressFeature,
	"partition-ops/1", "log-share/1", "credential-confirm/1"}

// registerPartitionFeature adds a feature word another plane serves (F6's
// partition-mail/1): call it from the plane's init.
func registerPartitionFeature(f string) {
	if !slices.Contains(partitionFeatures, f) {
		partitionFeatures = append(partitionFeatures, f)
	}
}

// partitionRow is one person's partition of a tile on the wire.
type partitionRow struct {
	User        string              `json:"user"`
	Partition   string              `json:"partition"`   // user:<id>
	ID          string              `json:"partitionId"` // the pkey
	State       string              `json:"state"`       // active | dormant | orphaned
	Why         string              `json:"why,omitempty"`
	Running     bool                `json:"running"`
	Instance    *instanceView       `json:"instance,omitempty"`
	LastStarted string              `json:"lastStarted,omitempty"`
	Created     string              `json:"created,omitempty"`
	LastExit    string              `json:"lastExit,omitempty"`
	Restarts    int                 `json:"restarts,omitempty"`
	CrashLoop   bool                `json:"crashLoop,omitempty"`
	Bytes       int64               `json:"bytes"`
	Regs        *PartitionRegCounts `json:"registrations,omitempty"`
	Orphaned    string              `json:"orphaned,omitempty"`
	// the person's own row only
	LogShare *logShareView `json:"logShare,omitempty"`
	Ledger   []ledgerRow   `json:"ledger,omitempty"`
	// the person's own row and admins': their inbox's counts, never a
	// content (partition mail, 04 §3; partitionmail_inbox.go)
	Mail *MailCount `json:"mail,omitempty"`
}

// mailCountsOf answers the counts of tile's inbox pkey on deployment dep,
// reading each deployment's mail store once per listing; nil for an inbox
// that holds nothing (or a store that can't be read: the counts are
// metadata, never a reason to fail the listing).
func (b *Broker) mailCountsOf(tile string) func(dep, pkey string) *MailCount {
	byDep := map[string]map[string]MailCount{}
	return func(dep, pkey string) *MailCount {
		counts, ok := byDep[dep]
		if !ok {
			counts, _ = b.PartitionMailCounts(tile, dep)
			byDep[dep] = counts
		}
		if c, ok := counts[pkey]; ok {
			return &c
		}
		return nil
	}
}

// runningOf is the runner's instances of tile's people, by partition key.
func (b *Broker) runningOf(tile string) map[string]PartitionInstance {
	running := map[string]PartitionInstance{}
	for _, in := range b.partitionInstances() {
		if in.Tile == tile {
			running[in.Partition] = in
		}
	}
	return running
}

// partitionRowOf is record rec's row (its directory d): its state as the
// users store says it now, whether it runs, its registration counts and
// bytes. own: the person's own row (the instance's error text).
func (b *Broker) partitionRowOf(tile string, d partitionDirOf, rec partitionRecord, running map[string]PartitionInstance, own bool) partitionRow {
	row := partitionRow{User: rec.User, Partition: "user:" + rec.User, ID: d.pkey, State: rec.State,
		LastStarted: rec.LastStarted, Created: rec.Created, LastExit: rec.LastExit, Restarts: rec.Restarts, CrashLoop: rec.CrashLoop}
	if rec.State == partStateOrphaned {
		row.Why, row.Orphaned = rec.Reason, rec.Orphaned
	} else if b.storedPartitionUID(rec.User) != rec.UID {
		row.State, row.Why = partStateOrphaned, orphanUserDeleted // not recorded yet: the sweep will
	} else if err := b.personLive(rec.User, tile); err != nil {
		row.State, row.Why = "dormant", err.Error()
	}
	if in, ok := running[row.Partition]; ok && row.State != partStateOrphaned {
		row.Running, row.Instance = true, in.view(own)
	}
	regs := b.PartitionRegistrations(tile, d.dep, d.pkey)
	row.Regs = &regs
	row.Bytes = b.partitionBytesCached(tile, d.pkey)
	return row
}

// partitionPeople walks tile's people's partitions: each record (with its
// partition directory), live or orphaned — the admins' rows.
func (b *Broker) partitionPeople(tile string) []partitionRow {
	running := b.runningOf(tile)
	mail := b.mailCountsOf(tile)
	var out []partitionRow
	_ = b.eachPartitionRecord(tile, func(d partitionDirOf, rec partitionRecord) {
		row := b.partitionRowOf(tile, d, rec, running, false)
		row.Mail = mail(d.dep, d.pkey)
		out = append(out, row)
	})
	slices.SortFunc(out, func(x, y partitionRow) int { return cmp.Or(cmp.Compare(x.User, y.User), cmp.Compare(x.ID, y.ID)) })
	return out
}

// ownPartitionRows are person's own live rows of tile: only the
// directories of their current partition id are read.
func (b *Broker) ownPartitionRows(tile, person string) []partitionRow {
	uid := b.storedPartitionUID(person)
	if uid == "" {
		return nil
	}
	pkey := util.PartitionKey(person, uid)
	var out []partitionRow
	var running map[string]PartitionInstance
	_ = b.eachPartitionDir(tile, func(d partitionDirOf) {
		if d.pkey != pkey {
			return
		}
		rec, ok, err := readPartitionRecordAt(d.dir)
		if err != nil || !ok || rec.User != person || util.TileKey(rec.Tile) != d.tileKey || rec.Dep != d.dep || rec.State == partStateOrphaned {
			return
		}
		if running == nil {
			running = b.runningOf(tile)
		}
		if row := b.partitionRowOf(tile, d, rec, running, true); row.State != partStateOrphaned {
			row.Mail = b.mailCountsOf(tile)(d.dep, d.pkey)
			out = append(out, row)
		}
	})
	return out
}

// partitionBytes is what person pkey's namespaces of tile's scope hold (0
// for a tile that doesn't root its scope: it uses no scope resources).
func (b *Broker) partitionBytes(tile, pkey string) int64 {
	if !b.rootsOwnScope(tile) {
		return 0
	}
	var n int64
	_ = b.eachPartitionNamespace(tile, func(id nsID) {
		if id.pkey != pkey {
			return
		}
		if dir, err := b.nsDir(id); err == nil {
			s, _ := treeUsage(dir)
			n += s
		}
	})
	return n
}

// partitionBytesTTL is how long a partition's measured bytes are reused:
// the listing and tile-status are polled, and a measurement walks the
// person's whole namespace tree.
const partitionBytesTTL = time.Minute

type bytesAt struct {
	n  int64
	at time.Time
}

var partitionBytesSeen sync.Map // root \x00 tile \x00 pkey → bytesAt

func (b *Broker) bytesKey(tile, pkey string) string {
	return b.Reg.Root + "\x00" + tile + "\x00" + pkey
}

// partitionBytesCached is partitionBytes, measured at most once per
// partitionBytesTTL.
func (b *Broker) partitionBytesCached(tile, pkey string) int64 {
	k := b.bytesKey(tile, pkey)
	if v, ok := partitionBytesSeen.Load(k); ok && time.Since(v.(bytesAt).at) < partitionBytesTTL {
		return v.(bytesAt).n
	}
	n := b.partitionBytes(tile, pkey)
	partitionBytesSeen.Store(k, bytesAt{n, time.Now()})
	return n
}

// forgetPartitionBytes drops pkey's measurement (its data went).
func (b *Broker) forgetPartitionBytes(tile, pkey string) {
	partitionBytesSeen.Delete(b.bytesKey(tile, pkey))
}

// partitionTotals are a tile's totals, for its writers, managers and
// admins: from the records, the runner's rows, the in-memory registrations
// and the measured bytes — never a vault opened or a tree walked per call.
func (b *Broker) partitionTotals(tile string) map[string]int64 {
	t := map[string]int64{"people": 0, "running": 0, "bytes": 0, "cron": 0, "bus": 0}
	running := b.runningOf(tile)
	_ = b.eachPartitionRecord(tile, func(d partitionDirOf, rec partitionRecord) {
		if rec.State == partStateOrphaned || b.storedPartitionUID(rec.User) != rec.UID {
			return
		}
		t["people"]++
		if _, ok := running["user:"+rec.User]; ok {
			t["running"]++
		}
		t["bytes"] += b.partitionBytesCached(tile, d.pkey)
		cron, bus := b.partitionRegsLive(tile, d.dep, d.pkey)
		t["cron"] += int64(cron)
		t["bus"] += int64(bus)
	})
	return t
}

// partitionRegsLive counts partition pkey's cron jobs and bus
// subscriptions the broker holds (PartitionRegistrations' cheap part).
func (b *Broker) partitionRegsLive(tile, dep, pkey string) (cron, bus int) {
	prefix := partPrefix(tile, dep, pkey)
	b.cron.mu.Lock()
	for key := range b.cron.part {
		if strings.HasPrefix(key, prefix) {
			cron++
		}
	}
	b.cron.mu.Unlock()
	b.bus.mu.Lock()
	for key := range b.bus.part {
		if strings.HasPrefix(key, prefix) {
			bus++
		}
	}
	b.bus.mu.Unlock()
	return cron, bus
}

// adminRow is a row as admins see it: metadata and whether its log is
// shared with them, never the person's ledger.
func adminRow(b *Broker, tile string, r partitionRow) partitionRow {
	r.Ledger, r.LogShare = nil, b.logShareOf(tile, r.ID)
	return r
}

// modeRequestView is a tile's open or declined request on the wire.
func modeRequestView(req *registry.PartitionRequest) map[string]any {
	if req == nil {
		return nil
	}
	return map[string]any{"spec": registry.SpecOf(req.Spec), "since": req.Since.UTC(), "declined": req.Declined}
}

func (b *Broker) apiPartitionsList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	pol := b.Policies()
	out := map[string]any{"features": partitionFeatures}
	if b.canReadPolicies(p) { // the workspace policies are for people and admins, not tile code (F16)
		out["policies"] = map[string]any{"partitionConsent": pol.PartitionConsent, "credentialResetConfirm": pol.CredentialResetConfirm}
	}
	w.Header().Set("Cache-Control", "no-store")
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	if tile == "" {
		b.partitionsOverview(p, out, r.URL.Query().Get("untracked") == "1")
		server.WriteJSON(w, http.StatusOK, out)
		return
	}
	c, ok := b.Reg.Component(tile)
	admin := b.IsAdmin(p)
	if !ok || !admin && !p.CanReadTile(tile) && p.Component != tile {
		server.WriteError(w, http.StatusNotFound, "no such tile: "+tile, "/docs/protocol.md")
		return
	}
	st, rec, req := c.PartitionState()
	out["tile"], out["state"], out["spec"], out["request"] = tile, st.String(), rec, modeRequestView(req)
	if c.PartitionErr != "" {
		out["error"] = c.PartitionErr
	}
	person := ""
	if p.Component == "" && p.Impersonator == "" {
		person = p.UserID
	} else if d, ok := b.AdminFrameDriver(p); ok {
		person, admin = d.UserID, true // the admin tile, driven by its person: an admin's view
	} else {
		admin = false // tile code: the tile-level fields only
	}
	if p.Component != "" && !admin {
		server.WriteJSON(w, http.StatusOK, out)
		return
	}
	out["reviewedOnly"] = map[string]any{"on": false} // the admin switch (partitionreviewed.go)
	if v := b.reviewedOnlyView(tile); v != nil {
		out["reviewedOnly"] = v
	}
	perTile, _ := b.PartitionCaps(tile)
	defTile, _ := b.partitionCapDefaults()
	out["limits"] = map[string]any{"maxRunning": setOr(perTile, defTile), "partitionBytes": b.PartitionBytes(tile)}
	// only the rows the caller sees are built: their own (their directory
	// alone is read), or every person's for an admin
	visible := []partitionRow{}
	var mine []partitionRow
	if person != "" {
		for _, row := range b.ownPartitionRows(tile, person) {
			row.LogShare = b.logShareOf(tile, row.ID)
			row.Ledger = b.personLedger(tile, person)
			mine = append(mine, row)
		}
	}
	if admin {
		for _, row := range b.partitionPeople(tile) {
			if i := slices.IndexFunc(mine, func(m partitionRow) bool { return m.ID == row.ID }); i >= 0 {
				visible = append(visible, mine[i])
			} else {
				visible = append(visible, adminRow(b, tile, row))
			}
		}
	} else {
		visible = append(visible, mine...)
	}
	out["partitions"] = visible
	if admin || person != "" && (b.mayManageTile(p, tile) || p.CanWriteTile(tile)) {
		out["totals"] = b.partitionTotals(tile)
	}
	if len(mine) > 0 || person != "" && p.CanReadTile(tile) {
		out["trust"] = b.partitionTrust(tile)
	}
	if person != "" && pol.PartitionConsent {
		out["consents"] = b.consentsView(person)["consents"]
	}
	if binds := b.tileBindRows(tile, person, admin); len(binds) > 0 {
		out["binds"] = binds
	}
	if admin {
		out["orphans"] = b.partitionOrphans(tile)
		b.partitionAdminExtras(tile, out) // history, lastWipe, globalMail (partitionadmin.go)
	}
	if person != "" {
		out["notices"] = b.noticesOf(person, tile)
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// partitionCapDefaults is the runner's default caps (0 without one).
func (b *Broker) partitionCapDefaults() (perTile, workspace int) {
	b.plim.mu.Lock()
	f := b.plim.defaults
	b.plim.mu.Unlock()
	if f == nil {
		return 0, 0
	}
	return f()
}

// tileBindRows are the personal binds whose requester is tile: the
// person's own, or every live one for an admin.
func (b *Broker) tileBindRows(tile, person string, admin bool) []personalBindRow {
	var rows []personalBindRow
	_ = b.eachPersonalBinds(func(f *personalBindsFile) {
		if !b.personalBindsLive(f) || !admin && f.User != person {
			return
		}
		for _, pb := range f.Binds {
			if pb.Requester == tile {
				rows = append(rows, b.bindRow(f.User, pb))
			}
		}
	})
	return rows
}

// personLedger is person's ledger totals of their partition of tile over
// 30 days, per kind and target (their own: 06 §6.1).
func (b *Broker) personLedger(tile, person string) []ledgerRow {
	since := ledgerSince(30)
	sum := map[[2]string]int64{}
	for _, d := range b.ledgerDocs() {
		if d.Tile != tile || d.User != person || !b.ledgerLive(d) {
			continue
		}
		for day, kinds := range d.Days {
			if day < since {
				continue
			}
			for kind, targets := range kinds {
				for target, n := range targets {
					sum[[2]string{kind, target}] += n
				}
			}
		}
	}
	var out []ledgerRow
	for k, n := range sum {
		out = append(out, ledgerRow{Tile: tile, Kind: k[0], Target: k[1], Count: n})
	}
	sortLedger(out)
	return out
}

// partitionsOverview is the listing without a tile: every partitioned (or
// paused, or asking) tile the caller can read, with the caller's own
// partition's state on each; admins also get people and running counts,
// orphans, and whether xbind isolates (people's partitions need it).
func (b *Broker) partitionsOverview(p auth.Principal, out map[string]any, untracked bool) {
	admin := b.IsAdmin(p)
	person := ""
	if p.Component == "" && p.Impersonator == "" {
		person = p.UserID
	}
	tileCode := p.Component != "" && !b.adminConsoleView(p) // the admin tile driven by its person reads as an admin (partitionadmin.go)
	tiles := []map[string]any{}
	for _, c := range b.Reg.Components() {
		if !c.PartitionShown() || !admin && !p.CanReadTile(c.Path) {
			continue
		}
		st, rec, req := c.PartitionState()
		row := map[string]any{"tile": c.Path, "state": st.String(), "spec": rec, "request": modeRequestView(req)}
		if c.PartitionErr != "" {
			row["error"] = c.PartitionErr
		}
		if tileCode {
			tiles = append(tiles, row)
			continue
		}
		if person != "" {
			for _, r := range b.ownPartitionRows(c.Path, person) {
				row["mine"] = map[string]any{"partition": r.Partition, "state": r.State, "running": r.Running, "bytes": r.Bytes}
			}
		}
		if admin {
			row["totals"] = b.partitionTotals(c.Path)
			if w := b.trustWarnings(c.Path); len(w) > 0 {
				row["trust"] = w
			}
			if gb := b.globalBindsOn(c.Path); len(gb) > 0 {
				row["globalBinds"] = gb
			}
			if spec, on := c.Partitioned(); on && !spec.Global {
				if rs := b.requestersWithoutGlobal(c.Path); len(rs) > 0 {
					row["boundWithoutGlobal"] = rs
				}
			}
			if m := b.managersLackingPartitions(c); len(m) > 0 {
				row["managersLacking"] = m
			}
			if _, on := c.Partitioned(); on && untracked { // ?untracked=1 (bx doctor): a confined git per tile
				if files, err := b.untrackedInTile(c.Path); err != nil {
					row["untrackedError"] = hostless(err).Error()
				} else if len(files) > 0 {
					row["untracked"] = firstFiles(files, maxUntrackedListed)
					row["untrackedCount"] = len(files)
				}
			}
			if ro := b.reviewedOnlyView(c.Path); ro != nil {
				row["reviewedOnly"] = ro
			}
			if h, ok := b.recentCapHit(c.Path); ok { // bx doctor's "caps hit recently" (06 §7)
				row["capsHit"] = h
			}
		}
		tiles = append(tiles, row)
	}
	out["tiles"] = tiles
	if admin && !tileCode {
		out["isolated"] = partitionIsolated()
		out["orphans"] = b.partitionOrphans("")
		out["now"] = time.Now().UTC()
	}
	if person != "" {
		out["credentials"] = b.heldOf(person)
		out["notices"] = b.noticesOf(person, "")
	}
}
