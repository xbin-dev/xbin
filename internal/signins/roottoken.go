package signins

// roottoken.go — THE ROOT TOKEN'S TRUST ANSWERS: one seam (D245, the
// review's item 2; plans/wsagent/home-on-untrusted-tiles.md §2).
//
// Trust answers are an account's. The root token is not one — but people
// live on it today, and on a tile that isn't the workspace's its sessions
// mount the tile's copy of its home, confined: without an answer of its
// own there would be no way back to the real home. So the token keeps
// trust answers like a person does, under the key its home has
// (homes/owner; "owner" is a reserved account id): asked by the same
// dialog, bound to the same basis, voided the same way, listed, revocable.
// It never has a saved SIGN-IN: those stay a person's.
//
// A later change turns the root token into a credential that only
// onboards. Then this goes: delete this file, and what names
// RootTokenUser / RootTokenSource —
//
//   - internal/boot/signins.go        the source is wrapped once
//   - internal/term/home.go           homeUser (whose answers a session's home is judged by)
//   - internal/server/signinsroot.go  the token's calls to the trust and home routes

// RootTokenUser is the key the root token's trust answers are kept under.
const RootTokenUser = "owner"

// RootTokenSource wraps a tile-source function so that it answers for the
// root token too (user RootTokenUser): the workspace's own admin with no
// account — a workspace-owned tile is its own, anyone else's is not.
func RootTokenSource(source func(user, tile string) (Source, bool)) func(user, tile string) (Source, bool) {
	return func(user, tile string) (Source, bool) {
		if user != RootTokenUser {
			return source(user, tile)
		}
		src, ok := source("", tile)
		if ok {
			src.Admin, src.NotOwn = true, src.Owner != ""
		}
		return src, ok
	}
}
