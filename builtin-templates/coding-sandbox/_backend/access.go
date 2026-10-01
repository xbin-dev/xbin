// access.go — who is asking and what they may see (docs/sandbox-manager.md
// §Consumers, sharing and people and §Partitioned consumers): the caller — a
// consumer, a partitioned consumer's user partition, a person verified or
// asserted — and a sandbox's home, shares and person rules as each caller
// meets them.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// caller is a request's consumer and person.
type caller struct {
	from string // the consumer tile (X-XBin-From, set by xbind)
	// partID is the user partition of a partitioned consumer the call comes
	// from (X-XBin-Partition-Id, set by xbind), part its "user:<id>" for
	// display. Both are "" for the consumer's non-personal identity: an
	// unpartitioned consumer, or a partitioned one's global instance ("" ≡
	// global, so what a consumer made before it was partitioned stays its
	// global instance's). The consumer identity is (from, partID).
	partID, part string
	user         string // the person: verified (X-XBin-User, or a user partition's) or asserted (Sbx-User)
	verified     bool
	// lookOnly: a person on this tile's own page with less than write access
	// to it (pageReader). They look and never change: contractHandler refuses
	// their changes before routing, and a read never starts a stopped sandbox
	// for them.
	lookOnly bool
	// refused: why the call can't be served as any consumer — partition
	// headers that don't agree, or a person other than its user
	// partition's (partitionCheck). Such a caller is home to nothing, sees
	// nothing and makes nothing: contractHandler answers it 403 before
	// routing, and callerOf fails closed for any route that doesn't — it is
	// never read as the consumer's non-personal identity.
	refused string
}

// lookOnlyKey marks a pageReader's request (its context).
type lookOnlyKey struct{}

// callerOf is who asks. A user partition's call is its person's, verified;
// one whose partition headers, X-XBin-User or Sbx-User don't agree with it
// is a refused caller (contractHandler answered it already; any other route
// finds it matches nothing).
func callerOf(r *http.Request) caller {
	c := caller{from: r.Header.Get("X-XBin-From")}
	if why := partitionCheck(r); why != "" {
		c.verified, c.refused = true, why
		return c
	}
	var person string
	c.partID, c.part, person, _ = partitionOf(r) // no error: partitionCheck passed
	switch u := r.Header.Get("X-XBin-User"); {
	case c.partID != "":
		c.user, c.verified = person, true
	case u != "":
		c.user, c.verified = u, true
	default:
		return caller{from: c.from, user: strings.TrimSpace(r.Header.Get("Sbx-User"))}
	}
	c.lookOnly = r.Context().Value(lookOnlyKey{}) != nil
	return c
}

// key is the caller's consumer identity as one string (clientIds, exec
// prefixes): the consumer's path alone for its non-personal identity — as
// before partitions — and path \x01 partition id for a user partition.
func (c caller) key() string {
	if c.partID == "" {
		return c.from
	}
	return c.from + "\x01" + c.partID
}

// partitionOf reads what xbind says of a partitioned consumer's call:
// X-XBin-Partition ("user:<id>" or "global") and, for a user partition,
// X-XBin-Partition-Id (its stable id: a person deleted and made again gets
// a new one). It answers the user partition's id, its "user:<id>" and its
// person — all "" for no partition and for global — or why the headers
// don't agree. xbind never sends such a call (it strips inbound X-XBin-*);
// one is refused, never read as the consumer's non-personal identity.
func partitionOf(r *http.Request) (id, part, person string, err error) {
	part, id = r.Header.Get("X-XBin-Partition"), r.Header.Get("X-XBin-Partition-Id")
	switch {
	case id == "" && (part == "" || part == "global"):
		return "", "", "", nil
	case !strings.HasPrefix(part, "user:") || len(part) == len("user:"):
		return "", "", "", fmt.Errorf("partition %q isn't one this manager knows (user:<id> or global)", part)
	case id == "":
		return "", "", "", fmt.Errorf("a call from the user partition %s without its partition id", part)
	case len(id) > 128 || strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r >= 0x7f }):
		return "", "", "", errors.New("the partition id is up to 128 printable characters")
	}
	return id, part, part[len("user:"):], nil
}

// partitionCheck is why r can't be served as the partition it comes from
// ("" when it can): headers that don't agree, or a person other than the
// user partition's — a page's X-XBin-User or a backend's Sbx-User.
func partitionCheck(r *http.Request) string {
	id, part, person, err := partitionOf(r)
	switch {
	case err != nil:
		return err.Error()
	case id == "":
		return ""
	case r.Header.Get("X-XBin-User") != "" && r.Header.Get("X-XBin-User") != person:
		return "a call from " + part + "'s partition is " + person + "'s, not " + r.Header.Get("X-XBin-User") + "'s"
	case strings.TrimSpace(r.Header.Get("Sbx-User")) != "" && strings.TrimSpace(r.Header.Get("Sbx-User")) != person:
		return "a call from " + part + "'s partition acts for " + person + ", not " + strings.TrimSpace(r.Header.Get("Sbx-User"))
	}
	return ""
}

// pageReader: r comes from this tile's own page (the frame, so the person
// is verified), and that person has less than write access to the tile.
// docs/auth.md D29: a frame call runs at the tile's full self-role, so a
// mutating endpoint gates on the person's level. Such a person gets a
// read-only view. Other consumers' calls are never gated here: the contract
// trusts consumers.
func (m *Manager) pageReader(r *http.Request) bool {
	c := xbin.Caller(r)
	return m.self != "" && c.From == m.self && !c.UserCanWrite()
}

// mutates: r changes something. That is every method but GET and HEAD, and
// every GET that upgrades: a terminal (it starts a shell or types into
// one), a stdio socket (it writes an exec's stdin, and takes it from the
// socket attached before), a port's socket — and any socket added later,
// until it is shown to only read. The two socket routes count by their
// path too, so the refusal never depends on how complete a handshake is.
func mutates(r *http.Request) bool {
	p := r.URL.Path
	return r.Method != http.MethodGet && r.Method != http.MethodHead || r.Header.Get("Upgrade") != "" ||
		strings.HasSuffix(p, "/tty") || strings.HasSuffix(p, "/stdio")
}

// shared: the sandbox is shared with consumer's partition partID ("" its
// non-personal identity).
func (r *record) shared(consumer, partID string) (share, bool) {
	for _, s := range r.Shares {
		if s.Consumer == consumer && s.PartitionID == partID {
			return s, true
		}
	}
	return share{}, false
}

// home: the caller is the sandbox's home — its consumer, and the same
// partition of it ("" ≡ global).
func (r *record) home(c caller) bool {
	return c.refused == "" && r.Owner.Via == c.from && r.Owner.PartitionID == c.partID
}

// consumerHome: c is a user partition of the sandbox's home consumer, whose
// non-personal identity the sandbox is homed at.
func (r *record) consumerHome(c caller) bool {
	return c.refused == "" && c.partID != "" && r.Owner.Via == c.from && r.Owner.PartitionID == ""
}

// visible: the caller is the sandbox's home, or it was shared with the
// caller's consumer and partition. A user partition also sees what its
// person may use at its consumer's non-personal identity — a sandbox homed
// there, or one shared with the consumer — but only where personOK passes,
// so the rest doesn't exist for it. Never the converse: a sandbox homed in
// a user partition is invisible to its consumer's global instance and to
// every other partition.
func (r *record) visible(c caller) bool {
	if c.refused != "" {
		return false
	}
	if r.home(c) {
		return true
	}
	if _, ok := r.shared(c.from, c.partID); ok {
		return true
	}
	if c.partID == "" {
		return false
	}
	_, viaShare := r.shared(c.from, "")
	return (viaShare || r.consumerHome(c)) && r.personOK(c)
}

// personOK: on a verified call (a page's, or any user partition's) the
// person must be the owner or a member, or the sandbox team — and a share
// the caller sees it through must include them. A backend's call is its
// consumer's to police.
func (r *record) personOK(c caller) bool {
	if c.refused != "" {
		return false
	}
	if !c.verified {
		return true
	}
	if !r.home(c) && !r.consumerHome(c) {
		s, ok := r.shared(c.from, c.partID)
		inShare := ok && s.Users.has(c.user)
		if c.partID != "" { // a user partition also through its consumer's share
			s, ok = r.shared(c.from, "")
			inShare = inShare || ok && s.Users.has(c.user)
		}
		if !inShare {
			return false
		}
	}
	if r.Owner.User == c.user && r.Owner.User != "" || r.Visibility == "team" {
		return true
	}
	return slices.Contains(r.Members, c.user)
}

// canAdmin: who changes visibility, members and shares — the home
// consumer's backend, or its verified owner there (a user partition's
// person).
func (r *record) canAdmin(c caller) bool {
	return r.home(c) && (!c.verified || c.user == r.Owner.User)
}
