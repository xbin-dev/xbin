package broker

// partitionadmin.go — what the admin console's runtime → partitions view
// reads beyond the listing's rows (plans/partitions/06 §12.3; PD-46, PD-07):
//
//   - GET /partitions without a tile, from the admin tile's frame driven by
//     an admin's login (AdminFrameDriver, the person an admin), answers the
//     admins' overview — totals, trust warnings, global binds,
//     reviewed-only, caps hit, untracked files, orphans, isolation — as the
//     tile listing does for that frame: the admin console reads what an
//     admin's own session reads. Its driver's own rows, notices and held
//     credentials stay out: the console shows the workspace, not the
//     person. Driven by anyone else, the frame is tile code here.
//   - GET /partitions?tile=, for admins: the tile's mode history (the last
//     partitionHistoryShown entries, newest first: requests, switches,
//     keeps, withdrawals, and the backup ops — keys erased, a person's
//     partition restored), the last switch that deleted data (lastWipe),
//     and the global instance's inbox counts (globalMail: counts only,
//     never an item — 04 §3, owner answer I6).
//   - GET /partitions?tile=, for the tile's managers who aren't admins, in
//     their own session: who shares their partition's log now (logShares,
//     06 §5) — the logs panel's switcher offers each; admins read it on
//     every row.
//
// Nothing here is a person's data: a history entry names who decided, when,
// the modes, the counts a switch wiped and a partition id (admins see
// partition ids on every row); the inbox counts are metadata; a log share
// is its person's own choice to show their log to admins and managers.

import (
	"cmp"
	"slices"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// partitionHistoryShown is how many of a tile's mode history entries the
// admins' listing carries (the record keeps modeHistoryMax).
const partitionHistoryShown = 50

// consoleAdminDriver is the admin behind p when p is the admin tile's frame
// under that admin's login (AdminFrameDriver, and the person — or the root
// token — is an admin): the tile's grant opens the console, the person's
// role decides what it reads (PD-46). A frame driven by a person who isn't
// an admin gets false: the listing answers it the tile-level fields, like
// any tile code.
func (b *Broker) consoleAdminDriver(p auth.Principal) (auth.Principal, bool) {
	if p.Component == "" {
		return auth.Principal{}, false
	}
	d, ok := b.AdminFrameDriver(p)
	if !ok || !b.IsAdmin(d) {
		return auth.Principal{}, false
	}
	return d, true
}

// adminConsoleView reports p as the admin console driven by an admin
// (consoleAdminDriver): the listing answers it an admin's view.
func (b *Broker) adminConsoleView(p auth.Principal) bool {
	_, ok := b.consoleAdminDriver(p)
	return ok
}

// partitionsAdmin is IsAdmin for the partitions routes that judge the
// credential itself (the limits, the personal binds): the admin tile's
// frame under a person's login is an admin only when its driver is one —
// as the routes that judge the person (partitionActor, modeDecider,
// partitionBackupActor) already answer it. Every other credential is
// judged as itself.
func (b *Broker) partitionsAdmin(p auth.Principal) bool {
	if d, ok := b.AdminFrameDriver(p); ok {
		return b.IsAdmin(d)
	}
	return b.IsAdmin(p)
}

// partitionAdminExtras adds the admins' fields of tile's listing to out:
// history, lastWipe and globalMail, each only when there is something.
// mail is the listing's mailCountsOf(tile): the primary's store, already
// read for the people's rows, isn't read (or locked) again.
func (b *Broker) partitionAdminExtras(tile string, out map[string]any, mail func(dep, pkey string) *MailCount) {
	if rec := b.modeRecordOf(tile); rec != nil {
		if n := len(rec.History); n > 0 {
			h := slices.Clone(rec.History[max(0, n-partitionHistoryShown):])
			slices.Reverse(h)
			out["history"] = h
		}
		if rec.LastWipe != nil {
			out["lastWipe"] = rec.LastWipe
		}
	}
	if c := mail(b.primaryOf(tile), mailGlobalBox); c != nil {
		out["globalMail"] = *c
	}
}

// logShareRow is one person who shares their partition's log of a tile now.
type logShareRow struct {
	User  string    `json:"user"`
	Until time.Time `json:"until"`
}

// partitionManagerExtras adds, for person managing tile in their own
// session (not an admin: admins read every row), logShares — who shares
// their partition's log now, their own left out — so the logs panel
// offers what sharedLog lets a manager read (06 §5). Absent when nobody
// does.
func (b *Broker) partitionManagerExtras(p auth.Principal, tile, person string, out map[string]any) {
	if person == "" || p.Component != "" || !b.mayManageTile(p, tile) {
		return
	}
	primary := b.primaryOf(tile)
	var rows []logShareRow
	_ = b.eachPartitionRecord(tile, func(d partitionDirOf, rec partitionRecord) {
		if d.dep != primary || rec.User == person || rec.State == partStateOrphaned || b.storedPartitionUID(rec.User) != rec.UID {
			return // a log is shared from the primary's partition; an orphan's never
		}
		if s := b.logShareOf(tile, d.pkey); s != nil {
			rows = append(rows, logShareRow{User: rec.User, Until: s.Until})
		}
	})
	if len(rows) > 0 {
		slices.SortFunc(rows, func(x, y logShareRow) int { return cmp.Compare(x.User, y.User) })
		out["logShares"] = rows
	}
}
