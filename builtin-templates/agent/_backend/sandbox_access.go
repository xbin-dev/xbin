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
//     it is team. A sandbox another consumer shared with this tile is
//     people's only as far as the share names them (its users: "*" or a
//     list) — no share for this tile, nothing for anyone.
//   - manage (stop, archive, delete): its owner, and the tile's managers —
//     who may stop or delete any sandbox, but never bind someone else's
//     private one.
//   - edit (rename, visibility, members, shares): its owner (the tile's
//     managers for one without an owner).
//
// Anyone who may steer a conversation may use what is bound to it — through
// the conversation, never to bind it elsewhere (sandbox_bind.go).
//
// The class firewall (D116) reaches across a sandbox that conversations
// share: one whose class has internal reach marks the sandbox it works in
// (sbxInternalLabel), and a class that reaches outside with no internal
// reach may not bind or work in a sandbox so marked. The mark spreads within
// a conversation: one that has had a marked sandbox (Config.HeldInternal)
// could carry what it read there into any other it has, so every sandbox it
// has attached is marked then, and every one it binds or works in after.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	xbin "github.com/xbin-dev/xbin/sdk"
)

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
		if b.Shared && homedAtOwnGlobal(b) {
			// a person's partition sees the team's sandboxes (sandbox_partition.go):
			// used by the rules below, changed and deleted only there
			a := sandboxAccess(w, &sbxSandbox{Owner: b.Owner, Visibility: b.Visibility, Members: b.Members})
			a.Manage, a.Edit = false, false
			return a
		}
		if b.Shared && !b.shareAllows(w.user) {
			return sbxAccess{} // shared with this tile, but not for them
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

// sbxShare is one of a sandbox's shares: a consumer, and the people ("*" or
// a list of user ids) it may act for there.
type sbxShare struct {
	Consumer string          `json:"consumer"`
	Users    json.RawMessage `json:"users"`
}

// shareAllows: this tile's share of a sandbox another consumer shared with
// it names user. No share for this tile (or one that can't be read) is no.
func (s *sbxSandbox) shareAllows(user string) bool {
	var shares []sbxShare
	self := xbin.Self()
	if self == "" || user == "" || json.Unmarshal(s.Shares, &shares) != nil {
		return false
	}
	for _, sh := range shares {
		if sh.Consumer != self {
			continue
		}
		var all string
		if json.Unmarshal(sh.Users, &all) == nil {
			return all == "*"
		}
		var list []string
		return json.Unmarshal(sh.Users, &list) == nil && hasStr(list, user)
	}
	return false
}

// --- the firewall across a shared sandbox ------------------------------------------

// sbxInternalLabel marks a sandbox that a conversation with internal reach
// has worked in: it may hold internal data from then on.
const sbxInternalLabel = "xbin.agent/internal"

// taintRefusal says why a conversation of class cl may not work in b (""
// = it may): one that reaches outside (web, or a sandbox with egress) with
// no internal reach, in a sandbox that has held internal data. A confirmed
// mixed class has internal reach, so it may.
func taintRefusal(cl agentClass, b *sbxSandbox) string {
	if b == nil || b.Labels[sbxInternalLabel] == "" || !cl.egress() || cl.has(tsInternal) {
		return ""
	}
	return fmt.Sprintf("this sandbox has held data from an internal-reach conversation, so a conversation of a class that reaches outside (%s) can't work in it — use another sandbox",
		orStr(cl.Name, cl.ID))
}

// markInternal records on b, through conn, that a conversation with
// internal reach works in it: sbxInternalLabel merged into its labels (a
// lost update retried), and checked — the manager must keep it. b takes
// the labels (and version) the manager answered with.
func markInternal(ctx context.Context, conn *sbxConn, id string, b *sbxSandbox) error {
	orig := b
	for attempt := 0; ; attempt++ {
		if b.Labels[sbxInternalLabel] != "" {
			return nil
		}
		labels := map[string]string{}
		for k, v := range b.Labels {
			labels[k] = v
		}
		labels[sbxInternalLabel] = "1" // over a blank one too
		p := sbxPatch{Labels: &labels}
		if v := b.Version; v > 0 {
			p.Version = &v
		}
		nb, err := conn.Patch(ctx, id, p)
		switch {
		case err == nil && nb.Labels[sbxInternalLabel] == "":
			return errors.New("its manager didn't keep the label")
		case err == nil:
			orig.Labels, orig.Version = nb.Labels, nb.Version
			invalidateSandboxCatalog()
			return nil
		case sbxRefusal(err) != "precondition" || attempt >= 2:
			return err
		}
		if b, err = conn.Get(ctx, id); err != nil {
			return err
		}
	}
}

// marked: b holds (or may hold) internal data.
func (b *sbxSandbox) marked() bool { return b != nil && b.Labels[sbxInternalLabel] != "" }

// markAttached marks every sandbox cfg has but ref — a conversation that has
// just had a marked sandbox could move its data into any of them (a copy, or
// a read and a write). Each is asked for through its own binder, best
// effort: one that can't be marked now is marked before the conversation
// next works in it (sandboxUse, on Config.HeldInternal).
func markAttached(ctx context.Context, cfg Config, ref string) {
	for _, a := range bindingsOf(cfg) {
		provider, id, ok := splitSandboxRef(a.Ref)
		if a.Ref == ref || !ok {
			continue
		}
		conn, err := sbxDial(provider, sbxUserOf(binderWho(a.By)))
		if err == nil {
			var box *sbxSandbox
			if box, err = conn.Get(ctx, id); err == nil {
				err = markInternal(ctx, conn, id, box)
			}
		}
		if err != nil && sbxRefusal(err) != "not-found" {
			logf("sandbox %s: marking it as holding internal data: %v", a.Ref, err)
		}
	}
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
