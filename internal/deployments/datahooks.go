package deployments

// datahooks.go — the broker's data acts the plane's operations call
// (08-data §6, §8–§10), installed by boot's stepBroker. Plane embeds them;
// each is nil until installed, and a caller treats nil as "no data plane":
// nothing joined, nothing dropped, no data state, no vault to copy.

import "github.com/xbin-dev/xbin/internal/auth"

// DataHooks are the broker's answers about a deployment's (scope, name)
// data namespace and its vault.
type DataHooks struct {
	// ResetData empties the namespace deployment dep of tile claims (08-data
	// §9.1; 11-contract §1.8) for the reset op, which judged tile itself:
	// authorize judges every other claimant of the scope (P28), and stop
	// stops each claimant's dep before the wipe. vault also empties dep's
	// vault; a dry run changes nothing. It answers the claimants. Errors are
	// *Error (403 naming the claimant that blocks, 409 while an act runs or
	// while a claimant serves the namespace as its primary).
	ResetData func(tile, dep, by string, vault, dryRun bool, authorize func(tile string) error,
		stop func(tile, dep string)) ([]string, error)
	// DropData deletes the namespace dep of tile claims when tile is its last
	// claimant, never main's (08-data §9.2 step 3, §6.4): the remove op
	// calls it once the record no longer lists dep, outside index.dmu. It
	// answers whether the namespace went (or, dry, would go).
	DropData func(tile, dep string, dryRun bool) (bool, error)
	// DataOf is Deployment.data (11-contract §1.1) of dep of tile, from the
	// namespace's ns.json and its holds; nil for an unknown tile.
	DataOf func(tile, dep string) *DataState
	// JoinData answers the existing namespace a new deployment dep of tile
	// joins, nil when none (08-data §6.2; 11-contract §1.5): a 409 while an
	// act holds it. manager false refuses joining seeded, restored or
	// partial data with a 403; the plane passes true and judges the actor
	// itself (joinGate).
	JoinData func(tile, dep string, manager bool) (*Joins, error)
	// VaultCopy copies the named vault keys, or all, from the primary into a
	// non-primary deployment, never the other way (08-data §10): the
	// vault-copy op's run passes the grant's principal and the decoded
	// request, and the broker judges the manager gate again itself. Errors
	// are *Error; the audit names keys, never values.
	VaultCopy func(p auth.Principal, req VaultCopyRequest) (VaultCopyAnswer, error)
	// VaultPlaceholders lists deployment dep of tile's placeholders, the
	// primary's key names dep has no value for (none for the primary): a
	// reassignment's dry run shows its target's as Impact.placeholders.
	VaultPlaceholders func(tile, dep string) ([]string, error)

	// The per-deployment backups (08-data §11; 11-contract §1.8), each
	// judged by the plane first. BackupData archives dep of tile's data now
	// under its own key (main: the tile's main archive); a dry run writes
	// nothing. DataBackups lists dep's archived versions (dep need not
	// exist: a removed deployment keeps its archives). RestoreData restores
	// req's archive into req.Into by replace, judging every other claimant
	// with authorize at the reset level and stopping each with stop; it
	// answers those claimants too (a dry run's stops). SetBackupSchedule
	// sets or, with schedule "", removes dep's own schedule;
	// BackupScheduleOf is Deployment.backup, nil without one. Errors are
	// *Error.
	BackupData        func(tile, dep string, dryRun bool) (BackupAnswer, error)
	DataBackups       func(tile, dep string) (any, error)
	RestoreData       func(tile, by string, req RestoreRequest, authorize func(tile string) error, stop func(tile, dep string)) (RestoreAnswer, []string, error)
	SetBackupSchedule func(tile, dep string, schedule *string, retention *int, dryRun bool) error
	BackupScheduleOf  func(tile, dep string) *BackupSchedule

	// WouldNotify is Deployment.wouldNotify: the push plane's held
	// notifications of a non-primary deployment, oldest first (09-fabric
	// §6), installed with POST /notify; nil: none held.
	WouldNotify func(tile, dep string) []WouldNotify
}
