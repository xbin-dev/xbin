package broker

// accessline.go — access.set {tile, user, level}: a person's own level on
// one tile, as a line of a change set or a plan (D246;
// plans/wsagent/access-and-status.md §1; docs/protocol/delegation.md
// §Change sets).
//
// It applies as PUT /access {tile, kind: "user", id, level} — the call the
// tile's access dialog makes — through the handler (accessCheck: the tile's
// owner, the owning org's admins, a workspace admin) and the users store
// (SetUserTile: the level, a system component's read-or-none, the
// account). Nothing here writes; this file describes the line as the person
// would apply it now:
//
//   - PERSONAL exactly when the person is the tile's user-owner
//     (approverSelf): the branch of the by-hand rule that needs no admin
//     right. Every other way to change a tile's access by hand is an
//     admin's right (a workspace admin, the owning org's admins), so the
//     line is an admin's (TestAccessLinePersonalIsByHand);
//   - judged on the person's EFFECTIVE level before and after — their own
//     entry, their orgs, the workspace defaults, as the Access page explains
//     it (Access.TileLevel, Access.Explain): removing an exclusion can raise
//     it. For an account nothing was resolved for, removing its entry counts
//     as a raise;
//   - WARNED, so it starts unticked: write or terminal reached — a tile's
//     writers change its code and every nested tile's, which runs with all
//     those tiles reach (accessreach.go) — a removal whose result xbin can't
//     show, a system component, the person's own level. One's own level is
//     BLOCKED for anyone but a workspace admin, and an admin's line for them;
//   - the account is resolved only for a person who already sees the
//     workspace's accounts (AccountsVisible): exact id, else exact display
//     name. For anyone else the name is used as an id, as typed, and
//     nothing is said of whether it is one before the by-hand route says
//     so at Apply;
//   - refused as no change at all: the workspace agent itself, a raise on a
//     tile whose subtree — or a tile it reaches through grants and bindings
//     — holds power (accessreach.go), asked only of a person who manages the
//     tile's access (anyone else reads the 403 line), a tile an earlier line
//     of the plan creates (a person's access is set on a tile that exists:
//     it never waits).

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// ChangeAccessSet is a person's own level on one tile.
const ChangeAccessSet = delegation.KindAccessSet

// The warnings an access line may carry: each starts it unticked.
const (
	accessWarnTerminal = "TERMINAL: %s can run code in %s and change it — and every tile inside it — and with it everything those tiles can reach"
	accessWarnWrite    = "WRITE: %s can change %s's code — and every tile inside it; that code runs with everything those tiles can reach"
	accessWarnUnknown  = "ENTRY REMOVED: what %s has on %s afterwards is what their orgs and the workspace defaults give — xbin can't show it here, and it may be more than now (up to terminal)"
	accessWarnSystem   = "SYSTEM COMPONENT: %s is part of xbin itself; access to it changes what %s can do in xbin"
	accessWarnSelf     = "YOUR OWN ACCESS: this changes what you yourself can do on %s"
	accessSelfBlocked  = "your own access is changed by a workspace admin on xbin's own pages (or asked for from the tile's Request access) — never through your agent"
)

// AccountsVisible reports whether p already sees the workspace's accounts
// in xbin: a workspace admin (GET /users) or an org admin (GET
// /users-directory: every account's id and name, to add members). Nobody
// else sees an account list.
func (b *Broker) AccountsVisible(p auth.Principal) bool {
	if b.Users == nil || p.Agent != "" || p.Component != "" || p.ViewAs() || p.Preview() {
		return false
	}
	if p.Owner || b.canManageUsers(p) {
		return true
	}
	return p.User != nil && !p.User.Disabled && p.Access != nil && len(p.Access.AdminOrgs()) > 0
}

// ResolveAccount resolves name for viewer: the account whose id it is (as
// ids are normalised), else the accounts whose display name equals it
// ignoring case and the space around it — exact, never a prefix. "unseen"
// for a viewer who sees no accounts: nothing is looked up for them.
func (b *Broker) ResolveAccount(viewer auth.Principal, name string) delegation.AccountMatch {
	if !b.AccountsVisible(viewer) {
		return delegation.AccountMatch{State: "unseen"}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return delegation.AccountMatch{State: "none"}
	}
	if u, ok := b.Users.Get(name); ok {
		return delegation.AccountMatch{ID: u.ID, Name: u.Name, State: "one"}
	}
	var hits []users.User
	for _, u := range b.Users.List() {
		if nameKey(u.Name) == nameKey(name) {
			hits = append(hits, u)
		}
	}
	switch len(hits) {
	case 0:
		return delegation.AccountMatch{State: "none"}
	case 1:
		return delegation.AccountMatch{ID: hits[0].ID, Name: hits[0].Name, State: "one"}
	}
	return delegation.AccountMatch{State: "several"}
}

// accountUserText: an access line's user is a name — an id or a display
// name — and nothing that reads as more: at most 100 characters, no
// control or format character.
func accountUserText(s string) error {
	t := strings.TrimSpace(s)
	if t == "" || len([]rune(t)) > 100 {
		return errors.New("access.set names the person by their account id or display name (at most 100 characters)")
	}
	for _, r := range t {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("user is not a name: it holds a control or formatting character")
		}
	}
	return nil
}

func accessLevelOK(l string) bool {
	switch l {
	case "", users.LevelNone, users.LevelRead, users.LevelWrite, users.LevelTerminal:
		return true
	}
	return false
}

// levelWords is a person's effective level on a tile and where it comes
// from, as the access matrix explains it (Access.TileLevel and
// Access.Explain — the by-hand Access page's words): "none (excluded)",
// "terminal through org eng", "no access".
func levelWords(level string, ex []users.Contribution) string {
	if len(ex) > 0 && ex[0].Source == "exact" && ex[0].Level == users.LevelNone {
		return "none (excluded)"
	}
	if level == "" || len(ex) == 0 {
		return "no access"
	}
	src, capped := ex[0].Source, ""
	for _, c := range []struct{ suffix, words string }{{" (no-terminal)", ", capped: their account opens no terminals"}, {" (system component)", ", capped on a system component"}} {
		if s, ok := strings.CutSuffix(src, c.suffix); ok {
			src, capped = s, c.words+capped
		}
	}
	kind, rest, _ := strings.Cut(src, ":")
	org, pat, _ := strings.Cut(rest, ":")
	var from string
	switch kind {
	case "admin":
		from = "as a workspace admin"
	case "owner":
		from = "as the tile's owner"
	case "org-admin":
		from = "as an admin of org " + org
	case "exact":
		from = "by their own entry"
	case "org-member":
		from = "through org " + org
	case "org-share":
		from = "through org " + org + "'s share of " + pat
	case "direct":
		from = "through their entry for " + rest
	case "default":
		from = "through the workspace default for " + rest
	default:
		from = "through " + src
	}
	return level + " " + from + capped
}

// previewAccess describes access.set (the file comment).
func (b *Broker) previewAccess(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile) (delegation.ChangePreview, error) {
	var a delegation.AccessArgs
	if err := strictArgs(op.Args, &a); err != nil {
		return delegation.ChangePreview{}, err
	}
	a.Tile = strings.Trim(a.Tile, "/")
	if err := lineNames("tile", a.Tile, "level", a.Level); err != nil {
		return delegation.ChangePreview{}, err
	}
	if err := accountUserText(a.User); err != nil {
		return delegation.ChangePreview{}, err
	}
	switch {
	case a.Tile == "":
		return delegation.ChangePreview{}, errors.New("access.set names a tile, a person and a level")
	case !accessLevelOK(a.Level):
		return delegation.ChangePreview{}, fmt.Errorf("unknown level %q (want none, read, write or terminal — or \"\" to remove the person's own entry)", a.Level)
	}
	if _, ok := plannedTile(planned, a.Tile); ok {
		return delegation.ChangePreview{}, fmt.Errorf("a person's access is set on a tile that exists: propose it once %s is made", a.Tile)
	}
	if err := notOnAgent("access to", a.Tile); err != nil {
		return delegation.ChangePreview{}, err
	}
	typed := strings.TrimSpace(a.User)
	m := b.ResolveAccount(p, typed) // nothing is looked up for a person who sees no accounts
	id := typed
	if m.State == "one" {
		id = m.ID
	}
	pv := delegation.ChangePreview{Account: m.ID, Tiles: []string{a.Tile}, Line: delegation.AccessArgsLine(op.Args), Admin: true,
		Call: delegation.AdminCall{Method: http.MethodPut, Path: "/access", Body: mustJSON(map[string]string{"tile": a.Tile, "kind": "user", "id": id, "level": a.Level})}}
	// one's own level, by one's id (as the store keys ids) or one's own
	// display name — which says nothing of anyone else's account: a person's
	// own agent never changes it, unless they are a workspace admin (a
	// warned admin's line, below)
	self := p.UserID != "" && (users.NormalizeID(id) == users.NormalizeID(p.UserID) || p.User != nil && nameKey(p.User.Name) != "" && nameKey(p.User.Name) == nameKey(typed))
	adminP := p.IsAdmin()
	if self && !adminP {
		pv.Admin, pv.Blocked = false, accessSelfBlocked // never personal, never asked of an admin
		return pv, nil
	}
	// who manages the tile's access comes next: a person who may not change
	// it reads the line from its arguments, and nothing of the tile's access,
	// its power or the account (the handler's 403)
	if why := b.accessCheck(p, a.Tile, id); why != nil {
		pv.Blocked, pv.Forbidden = why.msg, why.status == http.StatusForbidden
		return pv, nil
	}
	if _, ok := b.Reg.Component(a.Tile); !ok {
		pv.Blocked = "no such tile: " + a.Tile // a share of nothing: the handler would take it, a change set doesn't
		return pv, nil
	}
	// before and after, as the Access page says them: the person's
	// EFFECTIVE level — their own entry, their orgs, the workspace defaults
	// — before the line and with its entry in place. An account nothing was
	// resolved for has neither: removing its entry may give it anything its
	// orgs give, so that counts as a raise
	before, after, beforeText, afterText := "", "", "", ""
	known := false
	if m.State == "one" {
		if acc, ok := b.Users.Access(m.ID); ok {
			known = true
			next := acc.WithEntry(a.Tile, a.Level)
			before, after = acc.TileLevel(a.Tile), next.TileLevel(a.Tile)
			beforeText, afterText = levelWords(before, acc.Explain(a.Tile)), levelWords(after, next.Explain(a.Tile))
		}
	}
	raise := a.Level != users.LevelNone
	if known {
		raise = users.LevelRank(after) > users.LevelRank(before)
	}
	reach := b.accessReachOf(a.Tile)
	if raise && reach.holdsPower() {
		return delegation.ChangePreview{}, b.accessPowerRefusal(p, a.Tile, reach)
	}
	// personal: the tile's user-owner, about someone else — what the by-hand
	// rule lets them do with no admin right
	pv.Admin = self || b.approverSelf(p, a.Tile) == ""
	who := delegation.QuoteName(typed) + " (an account id, as typed — xbin checks it when this is applied)"
	switch m.State {
	case "one":
		who = delegation.AccountWords(m.ID, m.Name)
	case "none":
		pv.Blocked = "no account is named " + delegation.QuoteName(typed) + ": name the person by their account id"
	case "several":
		pv.Blocked = "more than one account is named " + delegation.QuoteName(typed) + ": name the person by their account id"
	}
	if err := sealedEntryOK(a.Tile, a.Level); err != "" && pv.Blocked == "" {
		pv.Blocked = err
	}
	if known {
		pv.Before, pv.After = mustJSON(map[string]string{"level": before}), mustJSON(map[string]string{"level": after, "entry": a.Level})
		pv.BeforeText, pv.AfterText = beforeText, afterText
		pv.Line = fmt.Sprintf("%s: %s → %s on %s", who, beforeText, afterText, a.Tile)
		if u, ok := b.Users.Get(m.ID); ok {
			if u.IsAdmin() {
				pv.Effects = append(pv.Effects, m.ID+" is a workspace admin: they reach every tile whatever this entry says")
			}
			pv.Inverse = &delegation.ChangeOp{Kind: ChangeAccessSet, Args: mustJSON(delegation.AccessArgs{Tile: a.Tile, User: m.ID, Level: u.Tiles[a.Tile]})}
		}
	} else { // nothing resolved: nothing of the account is read for this line
		pv.After = mustJSON(map[string]string{"entry": a.Level})
		pv.AfterText = delegation.AccessLevelWords(a.Level) + " (their own entry on this tile)"
		if a.Level == "" {
			pv.AfterText = "their own entry removed"
		}
		pv.Line = fmt.Sprintf("%s: %s on %s", who, pv.AfterText, a.Tile)
	}
	switch a.Level {
	case "":
		pv.Effects = append(pv.Effects, "their own entry on "+a.Tile+" goes: their access there is what their orgs and the workspace defaults give")
	case users.LevelNone:
		pv.Effects = append(pv.Effects, "they are excluded from "+a.Tile+", whatever their orgs and the workspace defaults give")
	default:
		pv.Effects = append(pv.Effects, fmt.Sprintf("they can use %s at %s — their own entry, which overrides what their orgs and the workspace defaults give", a.Tile, a.Level))
	}
	whoShort := m.ID // an id xbind resolved; a name as typed only quoted (it is the agent's text)
	if whoShort == "" {
		whoShort = delegation.QuoteName(typed)
	}
	// what the level brings, and its warnings, from the level AFTER: a
	// write or terminal reached by the line — raised to, or set as their own
	// entry — starts the line unticked; an unresolved entry's removal may
	// reach either
	reaches := func(l string) bool {
		if !known {
			return a.Level == l || a.Level == "" && l == users.LevelTerminal
		}
		return users.LevelRank(after) >= users.LevelRank(l) && (raise || before == after && users.LevelRank(a.Level) >= users.LevelRank(l))
	}
	switch {
	case reaches(users.LevelTerminal):
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnTerminal, whoShort, a.Tile))
	case reaches(users.LevelWrite):
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnWrite, whoShort, a.Tile))
	}
	if !known && a.Level == "" {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnUnknown, whoShort, a.Tile))
	}
	if reaches(users.LevelWrite) {
		pv.Effects = append(pv.Effects, reach.effects(p, a.Tile)...)
	}
	if util.SealedPath(a.Tile) {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnSystem, a.Tile, whoShort))
	}
	if self {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnSelf, a.Tile))
	}
	return pv, nil
}

// nameKey is a display name as names are compared: case and the space
// around it aside, as ids are (users.NormalizeID).
func nameKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// sealedEntryOK is the users store's rule for an entry on a system
// component, as a line's refusal ("" it holds): read or none there.
func sealedEntryOK(tile, level string) string {
	if util.SealedPath(tile) && (level == users.LevelWrite || level == users.LevelTerminal) {
		return tile + " is a system component, managed by xbin: access to it is read or none (the " + level + " level doesn't exist on it)"
	}
	return ""
}
