// access.go — who is calling, and what that caller may do with a run (D83).
//
// Conversations are per user: the human behind a request is the one xbind
// attributes (X-XBin-User — the tile's own frame and terminals carry it, D29),
// and a run belongs to whoever started it. Sharing adds viewers and
// participants, or opens a run to the whole team. Privacy holds between people
// who USE the agent; anyone who can change or read the tile itself (write or
// terminal access, the owner token) can read every run anyway.
package main

import (
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Visibility and roles as stored.
const (
	visPrivate      = "private"
	visTeam         = "team"
	roleViewer      = "viewer"
	roleParticipant = "participant"
)

type whoKind int

const (
	whoNone    whoKind = iota
	whoSystem          // the owner token, or the tile calling itself: everything
	whoCron            // xbin/cron: the fire and tick routes
	whoUser            // a signed-in human (through the tile's frame or terminal)
	whoElement         // another component, through a grant or binding
)

// who is a request's caller as far as runs are concerned.
type who struct {
	kind  whoKind
	user  string // whoUser: the user id (the login name)
	level string // whoUser: read | write | terminal on this tile
	el    string // whoElement: the component path
	// viewedBy is set when an admin looks at the workspace AS this user
	// (view-as, D64): they get the user's shared runs, never private ones.
	viewedBy string
}

// principal reads the verified caller xbind attached to r.
func principal(r *http.Request) who {
	c := xbin.Caller(r)
	self := xbin.Self()
	switch {
	case c.Owner:
		return who{kind: whoSystem}
	case c.From == "xbin/cron":
		return who{kind: whoCron}
	case c.User != "" && (c.From == self || strings.HasPrefix(c.From, "user:")):
		lvl := c.UserLevel
		if lvl == "" {
			lvl = "read"
		}
		return who{kind: whoUser, user: c.User, level: lvl, viewedBy: r.Header.Get("X-XBin-Viewed-By")}
	case c.From != "" && c.From == self:
		return who{kind: whoSystem}
	case c.From != "":
		return who{kind: whoElement, el: c.From}
	}
	return who{kind: whoNone}
}

// manager: may change what the whole tile does (config, model tiers, shared
// skills, the halt) and oversee every automation. A user needs write access
// to the tile — which already lets them change its code.
func (w who) manager() bool {
	switch w.kind {
	case whoSystem, whoElement:
		return true
	case whoUser:
		return w.level == "write" || w.level == "terminal"
	}
	return false
}

// tag is how the caller is recorded as a run's owner.
func (w who) tag() string {
	switch w.kind {
	case whoUser:
		return w.user
	case whoElement:
		return "el:" + w.el
	}
	return ""
}

// stamp is the ownership of a run this caller starts from the UI or the API:
// a person's run is theirs and private; a system or element run keeps the
// legacy shape (team-visible), so scripts see what they always saw.
func (w who) stamp(origin string) runStamp {
	switch w.kind {
	case whoUser:
		return runStamp{Owner: w.user, Visibility: visPrivate, TeamRole: roleViewer, Origin: origin}
	case whoElement:
		return runStamp{Owner: w.tag(), Visibility: visPrivate, TeamRole: roleViewer, Origin: "api"}
	}
	if origin == "chat" {
		origin = "api"
	}
	return runStamp{Visibility: visTeam, TeamRole: roleParticipant, Origin: origin}
}
