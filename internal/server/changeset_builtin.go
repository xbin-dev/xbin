package server

// changeset_builtin.go — what the server holds a builtin.update line to,
// beside the broker's description of it (D248; plans/wsagent/updates.md
// §2; the broker's builtinline.go):
//
//   - it is an admin's line for a workspace admin and "needs an admin" for
//     everyone else — an org admin too (describePlan): its by-hand route
//     takes xbin:writer, which no org right gives;
//   - its AGENT reads applied / proposed / failed and nothing of the files:
//     of such a line the agent, its conversation and its page read the
//     argument, the flags, the result and its out — and a line written from
//     the argument alone — whoever the person is; a refusal only where it
//     is one of the builtin's own (pinned, no update);
//   - in an admin_propose set it is held to what the conversation may
//     touch, as a plan's line is (holdAccessLines);
//   - applied, the route's answer is the line's out: {update, files, tile,
//     proposal?}.

import (
	"encoding/json"
	"strings"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// builtinForAgent is a builtin.update line as a person's agent reads it.
func builtinForAgent(it delegation.ChangeItem) delegation.ChangeItem {
	red := delegation.ChangeItem{N: it.N, ChangeOp: it.ChangeOp, Why: it.Why, Line: delegation.BuiltinArgsLine(it.Args),
		Admin: it.Admin, NeedsAdmin: it.NeedsAdmin, Needs: it.Needs, Result: it.Result, Out: it.Out, Of: it.Of}
	switch {
	case it.Blocked == "":
	case delegation.BuiltinRefusal(it.Blocked):
		red.Blocked = it.Blocked // the builtin's own state in xbin: pinned, no update
	default:
		red.Blocked = lineBlockedForAgent
	}
	if it.Result == delegation.ItemFailed {
		red.Error = builtinErrorForAgent(it.Error)
	}
	return red
}

// builtinErrorForAgent is why a builtin.update line failed, as its agent
// reads it: the builtin's own refusals as they are, anything else one
// sentence — the person reads the whole of it on xbin's review.
func builtinErrorForAgent(msg string) string {
	for _, own := range []string{delegation.BuiltinPinned, delegation.BuiltinUpToDate, delegation.BuiltinNotInstalled, delegation.BuiltinOccupied} {
		if msg == own {
			return msg
		}
	}
	switch {
	case strings.HasPrefix(msg, "pinned:"):
		return delegation.BuiltinPinned
	case strings.Contains(msg, "has no update"):
		return delegation.BuiltinUpToDate
	case strings.Contains(msg, "xbin:writer"):
		return "it takes a workspace admin's rights, and the person who applied it has none now"
	}
	return "xbin couldn't apply it: your person sees why on xbin's review"
}

// builtinTileOf is a builtin.update line's tile as its preview named it
// ("" for another kind).
func builtinTileOf(op delegation.ChangeOp, pv delegation.ChangePreview) string {
	if op.Kind != delegation.KindBuiltinUpdate || len(pv.Tiles) == 0 {
		return ""
	}
	return pv.Tiles[0]
}

// builtinOut is what the by-hand route answered for a builtin.update line,
// as the line's out.
func builtinOut(tile string, answer json.RawMessage) json.RawMessage {
	var a struct {
		Applied string   `json:"applied"`
		Files   []string `json:"files"`
		PR      *struct {
			Number int `json:"number"`
		} `json:"pr"`
		Conflicts []string `json:"conflicts"`
	}
	_ = json.Unmarshal(answer, &a)
	out := delegation.BuiltinOut{Update: a.Applied, Files: len(a.Files), Tile: tile}
	if a.PR != nil {
		out.Proposal = a.PR.Number
	}
	raw, _ := json.Marshal(out)
	return raw
}

// needsWorkspaceAdmin: line op is applied with a workspace admin's right
// and nothing less — an org admin's rights don't reach it, so for anyone
// but a workspace admin it is "needs an admin", never a blocked line they
// could do nothing with.
func needsWorkspaceAdmin(op delegation.ChangeOp) bool {
	return op.Kind == delegation.KindBuiltinUpdate
}
