package broker

import "sort"

// signinothers.go — who else could change what a person's coding agent
// runs on a tile (D230; internal/term/signin.go has the rule): the other
// enabled people who are not workspace admins and hold write level or more
// on the tile, by any source — ownership, an entry of their own, a pattern,
// an org's share, a permission set. Write level is the threshold, not
// terminal: the tile's directory is its code (project hooks, scripts, a
// Makefile), and a partitioned tile shares that directory across every
// partition — a partition keeps people's data apart, never the code.
//
// path may be the tile or a directory inside it: it is resolved to the
// registered tile first, so a person's access to the tile is never missed
// by asking about the directory as written. ok is false when the path lies
// in no registered tile (nothing is known: the caller refuses).

// SigninOthers answers the tile path lies in and those people, sorted.
func (b *Broker) SigninOthers(user, path string) (tile string, others []string, ok bool) {
	c, _, found := b.Reg.Resolve(path)
	if !found || c == nil || c.Sealed {
		return "", nil, false
	}
	for _, id := range b.codeWritersNow(c.Path) { // enabled, not an admin, write level or more
		if id != user {
			others = append(others, id)
		}
	}
	sort.Strings(others)
	return c.Path, others, true
}
