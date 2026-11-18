package main

import (
	"context"
	"fmt"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fakeAccess is what the fake xbind knows of D246: accounts (id → display
// name) and whether the person sees them; the tiles the person owns; the
// person a workspace admin; and per tile what GET /delegate/tile adds to the
// map's row. off: an xbind before the two routes.
type fakeAccess struct {
	accounts map[string]string
	sees     bool
	owned    map[string]bool
	wsAdmin  bool
	views    map[string]*wsTileView
	off      bool
}

// TileStanding is GET /delegate/tile: the map's row with what the test put
// in views — the gate is the map's (hidden, blocked, unreadable: refused).
func (f *fakeXbin) TileStanding(_ context.Context, sid, tile string) (wsTileView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note("TileStanding %s", tile)
	if f.access.off {
		return wsTileView{}, errWsUnavailable
	}
	t, err := f.tile(sid, tile, true)
	if err != nil {
		return wsTileView{}, err
	}
	v := wsTileView{}
	if pre := f.access.views[tile]; pre != nil {
		v = *pre
	}
	v.Tile, v.Title, v.Orgs, v.Level, v.Agent = t.Path, t.Title, t.Orgs, t.Level, t.Agent
	if v.Kind == "" {
		v.Kind = "app"
	}
	if v.Backend == nil {
		v.Backend = &wsTileViewBackend{State: t.Status, Words: t.Status}
	}
	if t.Level == "terminal" && !v.Log.Shown && len(v.Log.Lines) == 0 {
		if l := f.logs[tile]; l != "" {
			v.Log.Shown, v.Log.Lines = true, strings.Split(strings.TrimRight(l, "\n"), "\n")
		}
	}
	return v, nil
}

// AccountLookup is POST /delegate/accounts/lookup.
func (f *fakeXbin) AccountLookup(_ context.Context, sid, name string) (wsAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note("AccountLookup")
	if _, err := f.handle(sid); err != nil {
		return wsAccount{}, err
	}
	if f.access.off {
		return wsAccount{}, errWsUnavailable
	}
	out := wsAccount{Name: name}
	if !f.access.sees {
		out.Unseen = true
		return out, nil
	}
	yes, no := true, false
	if dn, ok := f.access.accounts[strings.ToLower(name)]; ok {
		out.Exists, out.ID, out.DisplayName = &yes, strings.ToLower(name), dn
		return out, nil
	}
	var hits []string
	for id, dn := range f.access.accounts {
		if strings.EqualFold(dn, strings.TrimSpace(name)) {
			hits = append(hits, id)
		}
	}
	switch len(hits) {
	case 0:
		out.Exists = &no
	case 1:
		out.Exists, out.ID, out.DisplayName = &yes, hits[0], f.access.accounts[hits[0]]
	default:
		out.Exists, out.Ambiguous = &yes, true
	}
	return out, nil
}

// accessLine is an access.set line as xbind gives it to the agent: its
// arguments' own sentence and the flags — personal on the person's own
// tile, blocked on their own access unless they are a workspace admin.
func (f *fakeXbin) accessLine(item *xbin.ChangeSetItem, args map[string]any) {
	item.Line = fmt.Sprintf("Set the access of “%s” on %s to %s", str(args["user"]), str(args["tile"]), str(args["level"]))
	switch {
	case strings.EqualFold(str(args["user"]), f.state.User) && !f.access.wsAdmin:
		item.Blocked = "xbin can't apply this line as proposed: your person sees why on xbin's review"
	case f.access.owned[str(args["tile"])]:
	default:
		item.Admin = true
	}
}
