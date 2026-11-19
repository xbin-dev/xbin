package server

// changeset_access.go — what the server holds an access.set line to,
// beside the broker's description of it (D246; plans/wsagent/
// access-and-status.md §1; the broker's accessline.go):
//
//   - its AGENT never reads the tile's access list: of such a line the
//     agent, its conversation and its page read the arguments, the flags
//     and the result — and a line written from the arguments alone —
//     whoever the person is (accessForAgent). An admin who lets their agent
//     read admin state lets it read grants and bindings, never GET /access;
//   - it is never a line about the person whose agent proposed it, unless
//     that person is a workspace admin — also when an admin describes or
//     applies it for them (Ask an admin): accessSelfRefusal;
//   - a client that doesn't know the kind never applies it (knowsRefusal):
//     an older app ticks any unblocked line of a change set.

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// newerKinds are the line kinds a client must say it knows (approve's
// knows) before a line of one is applied: those added after the eight
// every chrome and app since plans knows.
var newerKinds = []string{delegation.KindAccessSet, delegation.KindBuiltinUpdate}

// accessForAgent is an access.set line as a person's agent reads it.
func accessForAgent(it delegation.ChangeItem) delegation.ChangeItem {
	red := delegation.ChangeItem{N: it.N, ChangeOp: it.ChangeOp, Why: it.Why, Line: delegation.AccessArgsLine(it.Args),
		Admin: it.Admin, NeedsAdmin: it.NeedsAdmin, Needs: it.Needs, Deferred: it.Deferred, Result: it.Result, Of: it.Of}
	if it.Blocked != "" {
		red.Blocked = lineBlockedForAgent
	}
	if it.Power {
		red.Blocked, red.Power = linePowerForAgent, true
	}
	return red
}

// accessSelfOther is why a line an admin describes or applies for another
// person — their request, or their plan — is that person's own access.
const accessSelfOther = "this is the access of the person whose agent proposed it: their own access is changed by a workspace admin on xbin's own pages, never through their agent"

// accessSelfRefusal is why line op (pv, as person describes it) may not be
// applied for the agent a: it is the access of a's person, who is no
// workspace admin ("" it isn't). The broker judges a person's own lines
// (accessline.go); this holds the ones an admin describes or applies for
// someone else — an account the asker named by a name their own
// description couldn't resolve.
func (s *Server) accessSelfRefusal(person auth.Principal, a planAgent, pv delegation.ChangePreview, op delegation.ChangeOp) string {
	proposer := a.p.UserID
	if op.Kind != delegation.KindAccessSet || pv.Account == "" || proposer == "" || proposer == person.UserID {
		return ""
	}
	if users.NormalizeID(pv.Account) != users.NormalizeID(proposer) || s.userIsAdmin(proposer) {
		return ""
	}
	return accessSelfOther
}

// userIsAdmin reports a live workspace admin account.
func (s *Server) userIsAdmin(id string) bool {
	if s.Auth == nil || s.Auth.Users == nil {
		return false
	}
	u, ok := s.Auth.Users.Get(id)
	return ok && !u.Disabled && u.IsAdmin()
}

// knowsRefusal lists the newer kinds among the chosen lines of cs that the
// approving client didn't say it knows (none: it may apply them).
func knowsRefusal(cs delegation.ChangeSet, chosen []int, knows []string) []string {
	var out []string
	for _, n := range chosen {
		if n < 1 || n > len(cs.Items) {
			continue
		}
		k := cs.Items[n-1].Kind
		if slices.Contains(newerKinds, k) && !slices.Contains(knows, k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// accessTileOf is an access.set line's tile ("" for another kind).
func accessTileOf(op delegation.ChangeOp) string {
	if op.Kind != delegation.KindAccessSet {
		return ""
	}
	var a delegation.AccessArgs
	_ = json.Unmarshal(op.Args, &a)
	return strings.Trim(a.Tile, "/")
}

// holdAccessLines holds the access.set and builtin.update items of a
// change set an agent proposes outside a plan (admin_propose) to what its
// conversation may touch, as a plan's lines are (lineAgentRefusal): the
// tile readable, in scope, not "off", its orgs letting the agent in.
// tiles are the tiles each item's preview named.
func (s *Server) holdAccessLines(a planAgent, items []delegation.ChangeItem, tiles [][]string) {
	for i, it := range items {
		t := accessTileOf(it.ChangeOp)
		line := delegation.AccessArgsLine(it.Args)
		if it.Kind == delegation.KindBuiltinUpdate && i < len(tiles) && len(tiles[i]) > 0 {
			t, line = tiles[i][0], delegation.BuiltinArgsLine(it.Args)
		}
		if t == "" {
			continue
		}
		if why := s.lineAgentRefusal(a, []string{t}); why != "" {
			items[i] = delegation.ChangeItem{ChangeOp: it.ChangeOp, Why: it.Why, Line: line, Blocked: why, Admin: true}
		}
	}
}
