package deployments

// datahooks.go — the broker's data acts the plane's operations call
// (08-data §6, §8, §9), installed by boot's stepBroker. Plane embeds them;
// each is nil until installed, and a caller treats nil as "no data plane":
// nothing joined, nothing dropped, no data state.

// DataHooks are the broker's answers about a deployment's (scope, name)
// data namespace.
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
}
