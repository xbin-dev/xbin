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
//     orphans. Never contents, vault key names, log lines or mail.
//
// A tile's credentials (its frames, backend, terminals) get the tile-level
// fields and features only: tile code never reads people's metadata.

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
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
	Instance    *PartitionInstance  `json:"instance,omitempty"`
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
}

// partitionPeople walks tile's people's partitions: each record (with its
// partition directory), live or orphaned.
func (b *Broker) partitionPeople(tile string) []partitionRow {
	running := map[string]PartitionInstance{}
	for _, in := range b.partitionInstances() {
		if in.Tile == tile {
			running[in.Partition] = in
		}
	}
	var out []partitionRow
	_ = b.eachPartitionRecord(tile, func(d partitionDirOf, rec partitionRecord) {
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
			row.Running, row.Instance = true, &in
		}
		regs := b.PartitionRegistrations(tile, d.dep, d.pkey)
		row.Regs = &regs
		row.Bytes = b.partitionBytes(tile, d.pkey)
		out = append(out, row)
	})
	slices.SortFunc(out, func(x, y partitionRow) int { return cmp.Or(cmp.Compare(x.User, y.User), cmp.Compare(x.ID, y.ID)) })
	return out
}

// partitionBytes is what person pkey's namespaces of tile's scope hold (0
// for a tile that doesn't root its scope: the root's rows count them).
func (b *Broker) partitionBytes(tile, pkey string) int64 {
	if c, ok := b.Reg.Component(tile); ok && c.Scope != "" && c.Scope != tile {
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

// partitionTotals are a tile's totals, for its writers and managers.
func partitionTotals(rows []partitionRow) map[string]int64 {
	t := map[string]int64{"people": 0, "running": 0, "bytes": 0, "cron": 0, "bus": 0}
	for _, r := range rows {
		if r.State == partStateOrphaned {
			continue
		}
		t["people"]++
		if r.Running {
			t["running"]++
		}
		t["bytes"] += r.Bytes
		if r.Regs != nil {
			t["cron"] += int64(r.Regs.CronJobs)
			t["bus"] += int64(r.Regs.BusSubscriptions)
		}
	}
	return t
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
	out := map[string]any{"features": partitionFeatures,
		"policies": map[string]any{"partitionConsent": pol.PartitionConsent, "credentialResetConfirm": pol.CredentialResetConfirm}}
	w.Header().Set("Cache-Control", "no-store")
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	if tile == "" {
		b.partitionsOverview(p, out)
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
	perTile, _ := b.PartitionCaps(tile)
	defTile, _ := b.partitionCapDefaults()
	out["limits"] = map[string]any{"maxRunning": setOr(perTile, defTile), "partitionBytes": b.PartitionBytes(tile)}
	rows := b.partitionPeople(tile)
	var mine []partitionRow
	visible := []partitionRow{}
	for _, row := range rows {
		switch {
		case person != "" && row.User == person && row.State != partStateOrphaned:
			row.LogShare = b.logShareOf(tile, row.ID)
			row.Ledger = b.personLedger(tile, person)
			mine = append(mine, row)
			visible = append(visible, row)
		case admin:
			visible = append(visible, adminRow(b, tile, row))
		}
	}
	out["partitions"] = visible
	if admin || person != "" && (b.mayManageTile(p, tile) || p.CanWriteTile(tile)) {
		out["totals"] = partitionTotals(rows)
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
func (b *Broker) partitionsOverview(p auth.Principal, out map[string]any) {
	admin := b.IsAdmin(p)
	person := ""
	if p.Component == "" && p.Impersonator == "" {
		person = p.UserID
	}
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
		if p.Component != "" {
			tiles = append(tiles, row)
			continue
		}
		rows := b.partitionPeople(c.Path)
		for _, r := range rows {
			if person != "" && r.User == person && r.State != partStateOrphaned {
				row["mine"] = map[string]any{"partition": r.Partition, "state": r.State, "running": r.Running, "bytes": r.Bytes}
			}
		}
		if admin {
			row["totals"] = partitionTotals(rows)
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
		}
		tiles = append(tiles, row)
	}
	out["tiles"] = tiles
	if admin && p.Component == "" {
		out["isolated"] = partitionIsolated()
		out["orphans"] = b.partitionOrphans("")
		out["now"] = time.Now().UTC()
	}
	if person != "" {
		out["credentials"] = b.heldOf(person)
		out["notices"] = b.noticesOf(person, "")
	}
}
