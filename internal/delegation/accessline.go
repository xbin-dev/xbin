package delegation

// accessline.go — the words of an access.set line that nothing of the
// workspace's state is in (D246; plans/wsagent/access-and-status.md §1):
// what a person's agent reads of such a line, and what a person who may not
// change the tile's access reads of it. xbind's own line for the person who
// reviews it says the account it resolved and the level before and after;
// this one says only what the line's arguments say.

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KindAccessSet is a person's own level on one tile: {tile, user, level}.
const KindAccessSet = "access.set"

// AccountMatch is how a name resolves for a person who may see the
// workspace's accounts (the broker's ResolveAccount; the lookup and an
// access.set line both ask it).
type AccountMatch struct {
	ID, Name string
	// State: "one" (ID, Name), "none", "several", or "unseen" — the person
	// sees no accounts, and nothing was looked up.
	State string
}

// AccessArgs are an access.set line's arguments.
type AccessArgs struct {
	Tile  string `json:"tile"`
	User  string `json:"user"`
	Level string `json:"level"`
}

// QuoteName is a name a person or an agent wrote — an account's display
// name, a name as typed — as xbind may put it in a sentence: control and
// format characters (bidi overrides, zero-width ones) dropped, white space
// one space, quotes made plain, at most 60 characters, between “ and ”.
// It can't close its quotes early or make the sentence read otherwise.
func QuoteName(s string) string {
	var b strings.Builder
	space, n := false, 0
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r) && !unicode.IsSpace(r), unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r):
			space = true
			continue
		case strings.ContainsRune("\"“”„‟«»", r):
			r = '\''
		}
		if n >= 60 {
			b.WriteString("…")
			break
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
			n++
		}
		space = false
		b.WriteRune(r)
		n++
	}
	return "“" + b.String() + "”"
}

// AccessLevelWords is a level as an access.set line says it.
func AccessLevelWords(level string) string {
	switch level {
	case "":
		return "no access"
	case "none":
		return "none (excluded)"
	}
	return level
}

// AccessArgsLine is an access.set line from its arguments alone: no account
// resolved, no level read.
func AccessArgsLine(raw json.RawMessage) string {
	var a AccessArgs
	if json.Unmarshal(raw, &a) != nil {
		return "Set a person's access to a tile"
	}
	tile := strings.Trim(a.Tile, "/")
	if a.Level == "" {
		return "Remove the own access entry of " + QuoteName(a.User) + " on " + tile
	}
	return "Set the access of " + QuoteName(a.User) + " on " + tile + " to " + AccessLevelWords(a.Level)
}
