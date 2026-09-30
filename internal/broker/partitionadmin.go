package broker

// partitionadmin.go — what the admin console's runtime → partitions view
// reads beyond the listing's rows (plans/partitions/06 §12.3; PD-46, PD-07):
//
//   - GET /partitions without a tile, from the admin tile's frame driven by
//     a person's login (AdminFrameDriver), answers the admins' overview —
//     totals, trust warnings, global binds, reviewed-only, caps hit,
//     untracked files, orphans, isolation — as the tile listing already did
//     for that frame (F7b): the admin console reads what an admin's own
//     session reads. Its driver's own rows, notices and held credentials
//     stay out: the console shows the workspace, not the person.
//   - GET /partitions?tile=, for admins: the tile's mode history (the last
//     partitionHistoryShown entries, newest first: requests, switches,
//     keeps, withdrawals, and the backup ops — keys erased, a person's
//     partition restored), the last switch that deleted data (lastWipe),
//     and the global instance's inbox counts (globalMail: counts only,
//     never an item — 04 §3, owner answer I6).
//
// Nothing here is a person's data: a history entry names who decided, when,
// the modes, the counts a switch wiped and a partition id (admins see
// partition ids on every row); the inbox counts are metadata.

import (
	"slices"

	"github.com/xbin-dev/xbin/internal/auth"
)

// partitionHistoryShown is how many of a tile's mode history entries the
// admins' listing carries (the record keeps modeHistoryMax).
const partitionHistoryShown = 50

// adminConsoleView reports p as the admin tile's frame under a person's
// login (AdminFrameDriver): the listing answers it an admin's view.
func (b *Broker) adminConsoleView(p auth.Principal) bool {
	if p.Component == "" {
		return false
	}
	_, ok := b.AdminFrameDriver(p)
	return ok
}

// partitionAdminExtras adds the admins' fields of tile's listing to out:
// history, lastWipe and globalMail, each only when there is something.
func (b *Broker) partitionAdminExtras(tile string, out map[string]any) {
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
	if counts, err := b.PartitionMailCounts(tile, ""); err == nil {
		if c, ok := counts[mailGlobalBox]; ok {
			out["globalMail"] = c
		}
	}
}
