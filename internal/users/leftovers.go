package users

import "sort"

// Leftover detection for ownership-based tile creation (D82): the broker's
// newTilePathOK refuses a non-admin create at a path the identity store
// still carries state for; this is the store's half of that check.

// PathLeftovers lists identity-store state still keyed by a path that a new
// tile owned by ownerRef would silently inherit (D82): another owner's entry,
// other users' exact per-tile entries, org shares, and an exact
// default-visibility entry. Paths are durable keys
// and nothing prunes them when a tile's directory disappears, so a non-admin
// creating there would take over access someone set up for the old tile.
// Explicit `none` exclusions, admins' entries (they see everything anyway),
// the creator's own entries, and the owning org's own share are not
// leftovers. Sorted, human-readable; empty = clean.
func (s *Store) PathLeftovers(path, ownerRef string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	if o := s.owners[path]; o != "" && o != ownerRef {
		out = append(out, "owner entry "+o)
	}
	kind, id, _ := ParseOwner(ownerRef)
	for uid, u := range s.byID {
		if l, ok := u.Tiles[path]; ok && l != LevelNone && !u.IsAdmin() && !(kind == OwnerKindUser && uid == id) {
			out = append(out, "user "+uid+" has "+l)
		}
	}
	for oid, o := range s.orgs {
		if l, ok := o.Tiles[path]; ok && l != LevelNone && !(kind == OwnerKindOrg && oid == id) {
			out = append(out, "shared to org "+oid+" ("+l+")")
		}
	}
	// An exact default-visibility entry puts whatever sits at the path in
	// front of every user (the default screen, the sidebar) — a spoofing
	// surface, not just a share.
	if l, ok := s.defaultTiles[path]; ok && l != LevelNone {
		out = append(out, "visible to every user by default ("+l+")")
	}
	sort.Strings(out)
	return out
}
