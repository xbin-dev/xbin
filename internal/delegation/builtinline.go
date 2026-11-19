package delegation

// builtinline.go — the words of a builtin.update line that nothing of the
// workspace's files is in (D248; plans/wsagent/updates.md §2): what a
// person's agent reads of such a line, its outcome, and the builtin's own
// refusals, which it may read. xbind's own line for the person who reviews
// it says the versions, how many files change and whether the update
// merges cleanly; this one says only what the line's argument says.

import (
	"encoding/json"
	"strings"
)

// KindBuiltinUpdate is a builtin's simple update: {id}. It applies as
// POST /builtins/update {id, mode: "safe"}.
const KindBuiltinUpdate = "builtin.update"

// BuiltinArgs are a builtin.update line's arguments: which builtin, and
// nothing else — the content is the binary's embedded copy.
type BuiltinArgs struct {
	ID string `json:"id"`
}

// BuiltinArgsLine is a builtin.update line from its argument alone.
func BuiltinArgsLine(raw json.RawMessage) string {
	var a BuiltinArgs
	if json.Unmarshal(raw, &a) != nil || strings.TrimSpace(a.ID) == "" {
		return "Update a builtin"
	}
	return "Update the builtin " + strings.TrimSpace(a.ID)
}

// The builtin's own refusals: a line's Blocked, which its agent reads as
// they are — they name the builtin's state in xbin, nothing of a tile.
const (
	BuiltinPinned       = "it is pinned: an admin chose to skip its updates — unpin it first (the Tile Manager's updates tab, or bx builtin unpin)"
	BuiltinUpToDate     = "it has no update: it is what this xbin ships"
	BuiltinNotInstalled = "it isn't installed in this workspace"
	BuiltinOccupied     = "something is already at its path: it is not installed over it — an admin installs it by hand"
)

// BuiltinRefusal reports a line's Blocked that is one of the builtin's own
// refusals — the ones its agent reads as they are.
func BuiltinRefusal(s string) bool {
	switch s {
	case BuiltinPinned, BuiltinUpToDate, BuiltinNotInstalled, BuiltinOccupied:
		return true
	}
	return false
}

// BuiltinOut is what applying a builtin.update line made (ChangeItem.Out):
// Update "updated" | "installed" | "proposed"; Files how many files it
// wrote (or the proposal changes); Proposal the change proposal's number
// on Tile when it was filed as one.
type BuiltinOut struct {
	Update   string `json:"update"`
	Files    int    `json:"files"`
	Tile     string `json:"tile"`
	Proposal int    `json:"proposal,omitempty"`
}
