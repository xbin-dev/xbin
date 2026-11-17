package broker

import (
	"sort"

	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/users"
)

// signinothers.go — where a tile's code comes from, for one person's saved
// coding-agent sign-in (D230; internal/term/signin.go has the rule,
// internal/signins/trust.go the two conditions). Code in a tile runs with
// the sign-in while the person's coding agent works there — project hooks,
// scripts, a Makefile — so a tile whose source is not only the person's
// own asks for their trust first. Two things make it so, answered apart:
//
//  1. signinNotOwn: the tile is owned by someone else — another person, an
//     org, or the workspace. A tile belongs to whoever created it until it
//     is handed on, so this is "created by a different user";
//  2. signinEditors: someone else who is not a workspace admin holds write
//     level or more on it, by any source — an entry of their own, a
//     pattern, an org's share, a permission set. Write level is the
//     threshold, not terminal: the tile's directory is its code, and a
//     partitioned tile shares that directory across every partition — a
//     partition keeps people's data apart, never the code.
//
// Workspace admins are not counted as editors: they can change any tile
// and the workspace itself, and are in the trust base. An admin who OWNS
// the tile is its owner like anyone else (condition 1).

// signinNotOwn is condition 1: owner — a tile's owner ref — is not user.
func signinNotOwn(owner, user string) bool {
	return owner != users.OwnerKindUser+":"+user
}

// signinEditors is condition 2: the other enabled people, not admins, who
// hold write level or more on the tile at path. Sorted.
func (b *Broker) signinEditors(user, path string) []string {
	var others []string
	for _, id := range b.codeWritersNow(path) { // enabled, not an admin, write level or more
		if id != user {
			others = append(others, id)
		}
	}
	sort.Strings(others)
	return others
}

// SigninSource answers, for user, the registered tile path is or lies in
// and where its code comes from. path may be a directory inside the tile:
// it is resolved first, so a person's access to the tile is never missed
// by asking about the directory as written. ok is false when the path lies
// in no registered tile (nothing is known: the caller refuses).
func (b *Broker) SigninSource(user, path string) (signins.Source, bool) {
	c, _, found := b.Reg.Resolve(path)
	if !found || c == nil || c.Sealed {
		return signins.Source{}, false
	}
	owner := ""
	if b.Users != nil {
		owner = b.Users.Owner(c.Path)
	}
	src := b.signinSourceOf(user, c.Path, owner)
	src.ID, src.Name = b.TileIdentity(c.Path), c.Manifest.Title
	return src, true
}

// SigninSourcePlanned is SigninSource for a tile a plan is about to create
// at path for owner: it has no identity yet, so it can be shown as the
// person's own — or not — and never trusted ahead of time.
func (b *Broker) SigninSourcePlanned(user, path, owner string) signins.Source {
	return b.signinSourceOf(user, path, owner)
}

func (b *Broker) signinSourceOf(user, tile, owner string) signins.Source {
	src := signins.Source{Tile: tile, Owner: owner, OwnerKind: "workspace"}
	if kind, id, err := users.ParseOwner(owner); err == nil && b.Users != nil {
		switch kind {
		case users.OwnerKindUser:
			src.OwnerKind, src.OwnerName = "person", id
			if id == user {
				src.OwnerKind = "you"
			} else if u, ok := b.Users.Get(id); ok && u.Name != "" {
				src.OwnerName = u.Name
			}
		case users.OwnerKindOrg:
			src.OwnerKind, src.OwnerName = "org", id
			if o, ok := b.Users.Org(id); ok && o.Name != "" {
				src.OwnerName = o.Name
			}
		}
	}
	src.NotOwn = signinNotOwn(owner, user)    // condition 1
	src.Editors = b.signinEditors(user, tile) // condition 2
	return src
}
