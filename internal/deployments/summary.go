package deployments

// summary.go — the primary summary /components gives a tile with a
// deployment record (11-contract §8): the server asks it through the
// broker's policy (server.PrimarySummaryPolicy).

// PrimarySummary answers /components' deployments summary for tile: its
// primary's name, whether the primary is pinned (doesn't follow the work
// tree) and whether it is protected; ok only while an active record governs
// the tile. A tile in the zero state, or one its record holds, gains nothing
// (its entry is today's). The same for every caller who sees the row: no
// non-primary name, no count (D127l). An in-memory lookup.
func (p *Plane) PrimarySummary(tile string) (primary string, pinned, protected, ok bool) {
	rec, _ := p.record(tile)
	if rec == nil {
		return "", false, false, false
	}
	d := rec.Deployments[rec.Primary]
	return rec.Primary, d != nil && d.Checkpoint != nil, rec.ProtectedPrimary, true
}
