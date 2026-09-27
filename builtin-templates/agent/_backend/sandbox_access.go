// sandbox_access.go — who may do what with a sandbox (D115, on D83's people).
//
// The manager keeps a sandbox's people — its owner (the person this agent
// asserted in Sbx-User when it created it), a private|team visibility and
// members — and trusts this backend, whose calls carry no verified person, to
// enforce them for the people it acts for. The rules, mirroring
// conversations:
//
//   - use (bind it, run the agent's tools in it, start it): its owner, a
//     member, or anyone when it is team. A sandbox of the tile itself (no
//     owner) is the system's and other components', and people's only when
//     it is team.
//   - manage (stop, archive, delete): its owner, and the tile's managers —
//     who may stop or delete any sandbox, but never bind someone else's
//     private one.
//   - edit (rename, visibility, members, shares): its owner (the tile's
//     managers for one without an owner).
//
// Anyone who may steer a conversation may use what is bound to it — through
// the conversation, never to bind it elsewhere (sandbox_bind.go).
package main

// sbxAccess is what one caller may do with one sandbox.
type sbxAccess struct {
	Mine   bool // the caller owns it
	Use    bool // may bind it and work in it
	Manage bool // may stop, archive or delete it
	Edit   bool // may change its name, visibility, members and shares
}

// seen: the caller may see the sandbox in a list.
func (a sbxAccess) seen() bool { return a.Use || a.Manage || a.Edit }

// sandboxAccess resolves w's access to b from the resource the manager
// returned (its owner, visibility and members).
func sandboxAccess(w who, b *sbxSandbox) sbxAccess {
	if b == nil {
		return sbxAccess{}
	}
	team := b.Visibility == visTeam
	unowned := b.Owner.User == ""
	switch w.kind {
	case whoSystem:
		return sbxAccess{Use: true, Manage: true, Edit: true}
	case whoElement:
		return sbxAccess{Use: unowned || team, Manage: true, Edit: unowned}
	case whoUser:
		if w.viewedBy != "" {
			return sbxAccess{} // an admin viewing as someone acts for nobody
		}
		mine := !unowned && b.Owner.User == w.user
		member := false
		for _, m := range b.Members {
			if m == w.user {
				member = true
				break
			}
		}
		return sbxAccess{Mine: mine, Use: mine || member || team, Manage: mine || w.manager(),
			Edit: mine || (unowned && w.manager())}
	}
	return sbxAccess{}
}

// binderWho is the caller a binding's By names (who.tag()), for re-checking
// what they may still use: a person, a component, or the tile itself.
func binderWho(by string) who {
	switch {
	case by == "":
		return who{kind: whoSystem}
	case len(by) > 3 && by[:3] == "el:":
		return who{kind: whoElement, el: by[3:]}
	}
	return who{kind: whoUser, user: by, level: "read"}
}

// sbxUserOf is the person a caller's calls to a manager assert (Sbx-User):
// a signed-in person, never a component or the tile.
func sbxUserOf(w who) string {
	if w.kind == whoUser && w.viewedBy == "" {
		return w.user
	}
	return ""
}
