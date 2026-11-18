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
//   - WARNED, so it starts unticked: a raise to terminal, a system
//     component, the person's own level. One's own level is BLOCKED for
//     anyone but a workspace admin, and an admin's line for them;
//   - the account is resolved only for a person who already sees the
//     workspace's accounts (AccountsVisible): exact id, else exact display
//     name. For anyone else the name is used as an id, as typed, and
//     nothing is said of whether it is one before the by-hand route says
//     so at Apply;
//   - refused as no change at all: the workspace agent itself, a tile with
//     xbin/cap:/gpu:/code power at any level above none, a tile an earlier
//     line of the plan creates (a person's access is set on a tile that
//     exists: it never waits).

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
	accessWarnTerminal = "TERMINAL: %s can run code in %s and change it — and with it everything the tile can reach"
	accessWarnSystem   = "SYSTEM COMPONENT: %s is part of xbin itself; access to it changes what %s can do in xbin"
	accessWarnSelf     = "YOUR OWN ACCESS: this changes what you yourself can do on %s"
	accessSelfBlocked  = "your own access is changed by a workspace admin on xbin's own pages (or asked for from the tile's Request access) — never through your agent"
)

// AccountsVisible reports whether p already sees the workspace's accounts
// in xbin: a workspace admin (GET /users) or an org admin (GET
// /users-directory: every account's id and name, to add members). Nobody
// else sees an account list.
func (b *Broker) AccountsVisible(p auth.Principal) bool {
	if b.Users == nil || p.Agent != "" || p.Component != "" || p.Impersonator != "" {
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
		if strings.EqualFold(strings.TrimSpace(u.Name), name) {
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

// effectiveWords is a person's effective level on a tile in a line's words.
func effectiveWords(level string) string {
	if level == "" {
		return "no access"
	}
	return level
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
	raise := a.Level != "" && a.Level != users.LevelNone
	if power := b.powerOf(a.Tile); len(power) > 0 && raise {
		return delegation.ChangePreview{}, fmt.Errorf("%s holds %s: who may use a tile with that power is decided on its Access page, by an admin — never by a change set", a.Tile, strings.Join(power, ", "))
	}
	typed := strings.TrimSpace(a.User)
	m := b.ResolveAccount(p, typed) // nothing is looked up for a person who sees no accounts
	id := typed
	if m.State == "one" {
		id = m.ID
	}
	pv := delegation.ChangePreview{Account: m.ID, Tiles: []string{a.Tile}, Line: delegation.AccessArgsLine(op.Args), Admin: true,
		Call: delegation.AdminCall{Method: http.MethodPut, Path: "/access", Body: mustJSON(map[string]string{"tile": a.Tile, "kind": "user", "id": id, "level": a.Level})}}
	// one's own level, by one's id or one's own display name — which says
	// nothing of anyone else's account: a person's own agent never changes
	// it, unless they are a workspace admin (a warned admin's line, below)
	self := p.UserID != "" && (strings.EqualFold(id, p.UserID) || p.User != nil && strings.TrimSpace(p.User.Name) != "" && strings.EqualFold(strings.TrimSpace(p.User.Name), typed))
	adminP := p.IsAdmin()
	if self && !adminP {
		pv.Admin, pv.Blocked = false, accessSelfBlocked // never personal, never asked of an admin
		return pv, nil
	}
	// who manages the tile's access comes next: a person who may not change
	// it reads the line from its arguments, and nothing of the tile's access
	// or of the account (the handler's 403)
	if why := b.accessCheck(p, a.Tile, id); why != nil {
		pv.Blocked, pv.Forbidden = why.msg, why.status == http.StatusForbidden
		return pv, nil
	}
	if _, ok := b.Reg.Component(a.Tile); !ok {
		pv.Blocked = "no such tile: " + a.Tile // a share of nothing: the handler would take it, a change set doesn't
		return pv, nil
	}
	// personal: the tile's user-owner, about someone else — what the by-hand
	// rule lets them do with no admin right
	pv.Admin = self || b.approverSelf(p, a.Tile) == ""
	who := delegation.QuoteName(typed) + " (an account id, as typed — xbin checks it when this is applied)"
	switch m.State {
	case "one":
		who = m.ID
		if n := strings.TrimSpace(m.Name); n != "" && !strings.EqualFold(n, m.ID) {
			who = delegation.QuoteName(n) + " (" + m.ID + ")"
		}
	case "none":
		pv.Blocked = "no account is named " + delegation.QuoteName(typed) + ": name the person by their account id"
	case "several":
		pv.Blocked = "more than one account is named " + delegation.QuoteName(typed) + ": name the person by their account id"
	}
	if err := sealedEntryOK(a.Tile, a.Level); err != "" && pv.Blocked == "" {
		pv.Blocked = err
	}
	afterEntry := delegation.AccessLevelWords(a.Level)
	switch m.State {
	case "one":
		acc, _ := b.Users.Access(m.ID)
		before, after := acc.TileLevel(a.Tile), acc.WithEntry(a.Tile, a.Level).TileLevel(a.Tile)
		pv.Before, pv.After = mustJSON(map[string]string{"level": before}), mustJSON(map[string]string{"level": after, "entry": a.Level})
		pv.BeforeText, pv.AfterText = effectiveWords(before), effectiveWords(after)
		if a.Level == users.LevelNone {
			pv.AfterText = "no access (excluded)"
		}
		pv.Line = fmt.Sprintf("%s: %s → %s on %s", who, pv.BeforeText, pv.AfterText, a.Tile)
		if u, ok := b.Users.Get(m.ID); ok {
			if u.IsAdmin() {
				pv.Effects = append(pv.Effects, m.ID+" is a workspace admin: they reach every tile whatever this entry says")
			}
			pv.Inverse = &delegation.ChangeOp{Kind: ChangeAccessSet, Args: mustJSON(delegation.AccessArgs{Tile: a.Tile, User: m.ID, Level: u.Tiles[a.Tile]})}
		}
	default: // nothing resolved: nothing of the account is read for this line
		pv.After = mustJSON(map[string]string{"entry": a.Level})
		pv.AfterText = afterEntry + " (their own entry on this tile)"
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
	whoShort := id
	if a.Level == users.LevelTerminal {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnTerminal, whoShort, a.Tile))
	}
	if util.SealedPath(a.Tile) {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnSystem, a.Tile, whoShort))
	}
	if self {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(accessWarnSelf, a.Tile))
	}
	return pv, nil
}

// sealedEntryOK is the users store's rule for an entry on a system
// component, as a line's refusal ("" it holds): read or none there.
func sealedEntryOK(tile, level string) string {
	if util.SealedPath(tile) && (level == users.LevelWrite || level == users.LevelTerminal) {
		return tile + " is a system component, managed by xbin: access to it is read or none (the " + level + " level doesn't exist on it)"
	}
	return ""
}
