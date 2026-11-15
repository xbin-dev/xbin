package users

// sealed.go — access levels on sealed components (D227): read or none.
// A sealed component has no source and no terminal, so the write and
// terminal levels don't exist on it. Two rules keep that so:
//
//   - nobody has one until it is given to them: an entry reaches a sealed
//     component only by naming it (matchTile) — in a person's own entries,
//     an org's shares or the workspace defaults. "*" and every other
//     pattern were written about tiles, so they give nothing there; an
//     admin reads every one, as they manage it;
//   - at write, an entry that names a sealed path above read is refused
//     wherever entries are stored (sealedLevel: one entry, a user's map,
//     the defaults, a request, an org's entry);
//   - at read, every resolution caps the level there (capSealed: TileLevel
//     of a user and of an Access, Explain) — for admins, owners, exact
//     entries and patterns alike — and the two readers that look at the
//     stored map itself go through the cap too (User.CanTerminal,
//     User.ShownTiles): a file edited by hand gives nothing more.

import (
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/internal/util"
)

// capSealed caps a level on a sealed component's path at read: it has no
// source and no terminal, so write and terminal don't exist there — for
// admins, owners, exact entries and patterns alike. A pattern under xbin/
// ("xbin/*") is capped as the paths it reaches are.
func capSealed(path, l string) string {
	if util.SealedPath(path) && levelRank(l) > levelRank(LevelRead) {
		return LevelRead
	}
	return l
}

// sealedLevel refuses an access entry above read on a sealed component's
// path: the level is read or nothing there (capSealed). An entry that
// names the path is refused wherever it is written — one entry, a user's
// whole map, the defaults; a pattern ("*", "xbin/*") is not an entry for it
// and gives nothing there (matchTile).
func sealedLevel(path, level string) error {
	if !strings.Contains(path, "*") && capSealed(path, level) != level {
		return fmt.Errorf("%s is a system component, managed by xbin: access to it is read or none (the %s level doesn't exist on it)", path, level)
	}
	return nil
}

// sealedLevels is sealedLevel over a map of entries.
func sealedLevels(tiles map[string]string) error {
	for path, level := range tiles {
		if err := sealedLevel(strings.Trim(strings.TrimSpace(path), "/"), level); err != nil {
			return err
		}
	}
	return nil
}

// terminalEntry reports whether a stored entry gives the terminal level
// somewhere: never on a sealed path, whatever the file says.
func terminalEntry(path, level string) bool {
	return levelRank(capSealed(strings.Trim(strings.TrimSpace(path), "/"), level)) >= levelRank(LevelTerminal)
}

// ShownTiles is the user's own entries as they take effect: the stored
// map, with an entry on a sealed path capped at read. What "who am I"
// shows; nil for no entries.
func (u *User) ShownTiles() map[string]string {
	if u == nil || u.Tiles == nil {
		return nil
	}
	out := make(map[string]string, len(u.Tiles))
	for path, level := range u.Tiles {
		out[path] = capSealed(strings.Trim(strings.TrimSpace(path), "/"), level)
	}
	return out
}

// SealedGrants says who was given the sealed component at path: everyone
// (the workspace defaults name it) and how many people and orgs have an
// entry of their own that names it. Admins read it without one.
func (s *Store) SealedGrants(path string) (everyone bool, entries int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	given := func(m map[string]string) bool { l, ok := m[path]; return ok && l != LevelNone && l != "" }
	everyone = given(s.defaultTiles)
	for _, u := range s.byID {
		if given(u.Tiles) {
			entries++
		}
	}
	for _, o := range s.orgs {
		if given(o.Tiles) {
			entries++
		}
	}
	return everyone, entries
}

// SetSealedEveryone gives everyone the sealed component at path (read, in
// the workspace defaults) or takes that entry away; entries of people's
// and orgs' own are left as they are.
func (s *Store) SetSealedEveryone(path string, on bool) error {
	if !util.SealedPath(path) || strings.Contains(path, "*") {
		return fmt.Errorf("%s is not a system component", path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, has := s.defaultTiles[path]
	if has == on {
		return nil
	}
	nm := make(map[string]string, len(s.defaultTiles)+1)
	for k, v := range s.defaultTiles {
		nm[k] = v
	}
	if on {
		nm[path] = LevelRead
	} else {
		delete(nm, path)
	}
	s.defaultTiles = nm
	return s.persistLocked()
}
