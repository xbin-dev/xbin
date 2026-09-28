package builtins

import (
	"errors"
	"fmt"
	"strings"
)

// retiredTiles are builtin tiles xbind no longer ships, each with what to use
// instead. Importing one fails with that message. A workspace that imported
// one before keeps its copy — an import is a copy, the workspace's own code —
// and nothing here touches it: update checks walk the embedded tiles only, so
// a retired tile is never offered an update or reported as changed.
var retiredTiles = map[string]string{
	"devbox": "the devbox builtin tile was retired on 2026-09-27 — it never worked reliably. " +
		"Coding sandboxes come from sandbox managers over the sandbox-manager contract " +
		"(/docs/sandbox-manager.md): the coding-sandbox template is coming; terminals and SSH " +
		"for people are the sandbox-terminal tile (bx tile import sandbox-terminal). " +
		"A workspace that imported devbox keeps its copy " +
		"(/docs/changes/2026-09-27-devbox-retired.md)",
}

// Retired reports whether name is a builtin tile xbind no longer ships, and
// the message saying so and what replaces it.
func Retired(name string) (string, bool) {
	msg, ok := retiredTiles[name]
	return msg, ok
}

// noSuchTile is the error for a tile name the catalog does not have.
func noSuchTile(name string) error {
	if msg, ok := Retired(name); ok {
		return errors.New(msg)
	}
	return fmt.Errorf("no builtin tile %q", name)
}

// noSuchUnit is the error for a unit id the embed does not have; a retired
// tile's id ("tile:devbox", left in the marker by an earlier import) says so.
func noSuchUnit(id string) error {
	name, _ := strings.CutPrefix(id, "tile:") // `bx builtin update devbox` passes the bare name
	if _, retired := Retired(name); retired {
		return fmt.Errorf("builtin tile %q is retired — xbind no longer ships it, so there is nothing to update to; the workspace's copy is its own", name)
	}
	return fmt.Errorf("no such builtin %q", id)
}
