package fsutil

import (
	"errors"
	"os"
)

// ErrEscapes is returned by OpenBeneath when rel resolves outside dir.
var ErrEscapes = errors.New("path escapes its directory")

// OpenBeneath opens dir/rel for reading such that no step of rel — and no
// symlink met on the way, whatever its target — resolves outside dir: an
// in-tree symlink (a tile's own assets/ → dist/) still works, one that
// leaves the tree (→ /etc, → ../../.xbin/secret, → another tile) fails
// with ErrEscapes (or the platform's equivalent error). dir itself may be
// reached through symlinks; it is the trust boundary, not a path under
// suspicion. On Linux it is one openat2(RESOLVE_BENEATH) — no check-then-
// open race; elsewhere a resolve-and-compare fallback.
//
// Files written by sandboxes (tiles, homes) are untrusted: daemon code that
// reads them on a caller's behalf opens them through here, never os.Open.
func OpenBeneath(dir, rel string) (*os.File, error) { return openBeneath(dir, rel) }
