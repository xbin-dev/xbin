// ws_tools_access.go — a person's access to a tile, as a line of a plan
// or an admin batch (D246; xbin's /docs/protocol/delegation.md §A person's
// access): access.set {tile, user, level}, and account_lookup, which says
// whether an account of a name exists so the line names the right person.
//
// What this agent learns of anyone's access is nothing: xbind writes the
// line with the person's level before and after for its person's review
// alone; the agent reads its own arguments back and, later, whether the
// line was applied. The lookup answers only for a person who already sees
// the workspace's accounts (an admin); for anyone else it says nothing
// either way, and the line carries the name as the person gave it — an
// admin's review resolves it.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// wsAccessLevels are an access.set line's levels ("" removes the person's
// own entry: their access is then what their orgs and the workspace give).
var wsAccessLevels = []string{"none", "read", "write", "terminal", ""}

func wsAccessTools() []coordTool {
	return []coordTool{
		{Spec: toolSpec{Type: "function", Function: funcDef{Name: "account_lookup",
			Description: "Whether a person's account of this name exists — by its exact account id or display name, never a search or a list — so an access.set line (in plan_propose or admin_propose) names the right person. " +
				"It answers only for a person who is a workspace or org admin, who sees the accounts already; for anyone else it says nothing either way: then put the name as your person gave it in the line, and an admin's review resolves it. " +
				"Two accounts with that display name: ask your person which (its account id). Counted and logged as an act.",
			Parameters: obj([]string{"name"}, map[string]any{
				"name": strProp("the person's account id or display name, as your person said it (Dave, dave.r)"),
			})}},
			Run: wsTool(wsAccountLookupTool)},
	}
}

func wsAccountLookupTool(ctx context.Context, ag *Agent, s *wsSession, c coordCall) (string, error) {
	name := strings.TrimSpace(str(c.Args["name"]))
	if name == "" {
		return "", errors.New("name: the account id or display name to look for")
	}
	var a wsAccount
	if err := ag.wsWith(ctx, s, func(sid string) (err error) { a, err = wsX.AccountLookup(ctx, sid, name); return }); err != nil {
		if wsMissing(err) {
			return "xbin can't look accounts up here (an older xbin): put the name as your person gave it in the access.set line — an admin's review resolves it.", nil
		}
		return "", err
	}
	q := wsPlain(clip(name, 100))
	switch {
	case a.Unseen:
		return fmt.Sprintf("xbin says nothing of %q: your person can't see the workspace's accounts. Put the name as they gave it in the access.set line — an admin's review shows them who it is, and a name no account has is a line that isn't applied.", q), nil
	case a.Ambiguous:
		return fmt.Sprintf("More than one account is called %q: ask your person which one (its account id) before you propose the line.", q), nil
	case a.Exists != nil && *a.Exists:
		who := a.ID
		if n := strings.TrimSpace(a.DisplayName); n != "" && !strings.EqualFold(n, a.ID) {
			who = fmt.Sprintf("%s (%q)", a.ID, wsPlain(clip(n, 60)))
		}
		return fmt.Sprintf("An account exists: %s. Name it by its id in the line (user: %q).", who, a.ID), nil
	}
	return fmt.Sprintf("No account has the id or display name %q (exact match only). Ask your person who they mean — its account id, or the name as it is spelled in xbin.", q), nil
}

// wsAccessArgs checks and settles an access.set line's arguments: the tile
// as the session's map names it (a path, or the name people call it), the
// person as given, a level of the list.
func wsAccessArgs(ctx context.Context, ag *Agent, s *wsSession, args map[string]any) (map[string]any, error) {
	t, err := wsFindTile(ctx, ag, s, str(args["tile"]))
	if err != nil {
		return nil, err
	}
	user := strings.TrimSpace(str(args["user"]))
	level := strings.TrimSpace(strings.ToLower(str(args["level"])))
	switch {
	case user == "":
		return nil, errors.New("access.set: user — the person's account id (account_lookup) or the name your person gave")
	case !hasStr(wsAccessLevels, level):
		return nil, fmt.Errorf("access.set: level is one of none, read, write, terminal (or \"\" to remove their own entry), not %q", clip(level, 20))
	}
	if userMode() && strings.EqualFold(user, runUser) {
		if st, err := wsX.State(ctx); err == nil && !st.CanAdminChanges {
			return nil, errors.New("access.set: that is your person's own access — you never change it: a workspace admin does, on xbin's own pages (or they ask from the tile's Request access)")
		}
	}
	return map[string]any{"tile": t.Path, "user": user, "level": level}, nil
}
