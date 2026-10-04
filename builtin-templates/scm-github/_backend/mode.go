// mode.go — which instance this is (legacy, global or a person's
// partition) and who is calling (docs/scm.md §Who is asking; API.md §Modes):
// the caller classes, the consumer key, and the guards every route sits
// behind. Everything here reads the headers xbind verified, never a body.
package main

import (
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Modes, decided once at start from XBIN_PARTITION.
const (
	modeLegacy = "legacy" // unpartitioned: the App, bot tokens only
	modeGlobal = "global" // the App, setup, policy, the identity directory
	modeUser   = "user"   // one person's sign-in
)

func modeOf(partition string) string {
	switch {
	case partition == "global":
		return modeGlobal
	case strings.HasPrefix(partition, "user:"):
		return modeUser
	}
	return modeLegacy
}

// Caller classes.
type class int

const (
	clsNone           class = iota
	clsSelf                 // the tile itself at global or legacy (its own backend, cron); never a partition
	clsOwner                // the owner token
	clsRelay                // global only: a person's own partition calling its global — its backend, frame or terminal
	clsPersonPage           // a person in the tile's own frame (legacy, or their own partition)
	clsTile                 // another tile, not acting for a person (no partition, or global)
	clsPersonConsumer       // another tile's partition of a person
	clsIngress              // public traffic through a published endpoint
)

func (c class) String() string {
	return [...]string{"none", "self", "owner", "relay", "person-page", "tile", "person-consumer", "ingress"}[c]
}

// who is a request's caller as this instance sees it.
type who struct {
	cls    class
	c      xbin.CallerInfo
	person string // the xbin person (relay, person-page, person-consumer)
	pid    string // their partition id (X-XBin-Partition-Id)
}

// consumerKey is the per-consumer key of a provider's state
// (docs/partitions.md §Providers): From, Deployment and the partition id —
// so a tile's global instance and an unpartitioned tile are one consumer,
// and each person's partition of it another.
func (w who) consumerKey() string {
	return w.c.From + "|" + w.c.Deployment + "|" + w.c.PartitionID
}

// classify decides the caller's class from the verified headers.
func (s *srv) classify(r *http.Request) who {
	c := xbin.Caller(r)
	w := who{c: c}
	userPart, isUserPart := strings.CutPrefix(c.Partition, "user:")
	switch {
	case c.Ingress():
		w.cls = clsIngress
	case c.Owner:
		if isUserPart { // never the owner while acting for a partition
			return w
		}
		w.cls = clsOwner
	case c.From == "xbin/cron":
		w.cls = clsSelf
	case c.From == s.self && c.From != "":
		switch {
		case isUserPart:
			// A person's partition: at global it is a relay call (role
			// reader or writer — anything else is no one); in their own
			// partition it is their page.
			if c.User == "" || c.ViewedBy != "" || userPart == "" {
				return w
			}
			if s.mode == modeGlobal && (c.Role == "reader" || c.Role == "writer") {
				w.cls, w.person, w.pid = clsRelay, c.User, c.PartitionID
			} else if s.mode == modeUser && userPart == s.user && c.User == s.user {
				w.cls, w.person, w.pid = clsPersonPage, c.User, c.PartitionID
			}
		case s.mode == modeUser:
			// A person's partition only ever hears its own person.
		case c.User != "":
			if s.mode == modeLegacy && c.ViewedBy == "" {
				w.cls, w.person = clsPersonPage, c.User
			} else if s.mode == modeGlobal && c.ViewedBy == "" {
				// A person at global through a path that names no partition
				// (an admin's direct call): a page caller, not the tile.
				w.cls, w.person = clsPersonPage, c.User
			}
		default:
			w.cls = clsSelf
		}
	case c.From != "" && !strings.HasPrefix(c.From, "xbin/"):
		switch {
		case isUserPart:
			if userPart == "" {
				return w
			}
			if s.mode == modeUser && userPart != s.user {
				return w // another person's partition never reaches this one
			}
			if s.mode == modeGlobal {
				return w // a person's partition of a consumer reaches the person's partition, never global
			}
			if c.ViewedBy != "" {
				return w // view-as is never a person here (docs/scm.md §Who is asking)
			}
			w.cls, w.person, w.pid = clsPersonConsumer, userPart, c.PartitionID
		case s.mode == modeUser:
			// Only a person's own partition of a consumer reaches theirs.
		default:
			w.cls = clsTile
		}
	}
	return w
}

// manager says whether the caller may set the tile up: the owner token; the
// tile itself at global or legacy; or a person there whose level is write or
// terminal and who isn't viewing as someone. A person's role at global is
// clamped to writer (docs/partitions.md §The global instance and people's
// partitions), so the admin role can't say this. Never in a person's
// partition: setup lives at global.
func (s *srv) manager(w who) bool {
	if s.mode == modeUser {
		return false
	}
	switch w.cls {
	case clsOwner, clsSelf:
		return w.c.From != "xbin/cron"
	case clsRelay, clsPersonPage:
		return w.c.ViewedBy == "" && (w.c.UserLevel == "write" || w.c.UserLevel == "terminal")
	}
	return false
}

// contractRole: the scm routes need the consumer role (a binding's) or admin
// (the tile's own frame).
func contractRole(c xbin.CallerInfo) bool {
	return xbin.RoleSatisfies(c.Role, "consumer") || xbin.RoleSatisfies(c.Role, "admin")
}

// guard wraps a handler with a check of the caller; the handler gets who.
type handler func(w http.ResponseWriter, r *http.Request, c who)

// scmGuard admits /scm/* callers: a tile or a person's consumer (pageOK: a
// person's page too, for hello and sign-in), holding the consumer role.
func (s *srv) scmGuard(pageOK bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.classify(r)
		ok := c.cls == clsTile || c.cls == clsPersonConsumer || (pageOK && c.cls == clsPersonPage)
		if !ok || !contractRole(c.c) {
			fail(w, refuse(refNotAllowed, "the scm routes need the consumer role: bind this tile's scm provide (service scm); a call that acts for a person reaches that person's partition (docs/scm.md)"))
			return
		}
		h(w, r, c)
	}
}

// relayGuard admits global's /partition/* routes: a person's own partition
// only (its backend, frame or terminal — all the person).
func (s *srv) relayGuard(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.mode != modeGlobal {
			fail(w, refuse(refNotFound, "only a partitioned copy's global instance relays for people's partitions"))
			return
		}
		c := s.classify(r)
		if c.cls != clsRelay {
			fail(w, refuse(refNotAllowed, "only a person's own partition of this tile calls its relay"))
			return
		}
		h(w, r, c)
	}
}

// managerGuard admits setup and policy: managers at global or legacy.
func (s *srv) managerGuard(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.mode == modeUser {
			fail(w, refuse(refNotFound, "setup and policy live at this tile's global instance (open it with ?xbin-partition=global)"))
			return
		}
		c := s.classify(r)
		if !s.manager(c) {
			fail(w, refuse(refNotAllowed, "only the tile's managers (its owner, people with write access) set it up"))
			return
		}
		h(w, r, c)
	}
}

// pageGuard admits the page's own reads: anyone the frame carries (a
// person's page, a relay call from their partition, a manager).
func (s *srv) pageGuard(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.classify(r)
		if c.cls != clsPersonPage && c.cls != clsRelay && !s.manager(c) {
			fail(w, refuse(refNotAllowed, "the page's routes are for people using this tile"))
			return
		}
		h(w, r, c)
	}
}
