package main

import (
	"context"
	"encoding/json"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fakeUpdates is what the fake xbind knows of D248: what GET
// /delegate/updates answers (rows already held to the conversation, as
// xbind's are), and what applying a builtin.update line makes, by id.
// off: an xbind before the read.
type fakeUpdates struct {
	u        wsUpdates
	outcomes map[string]wsBuiltinOut
	off      bool
}

// Updates is GET /delegate/updates[?tile=]: the rows the test put there,
// narrowed to the tile when one is named — the gate is the map's.
func (f *fakeXbin) Updates(_ context.Context, sid, tile string) (wsUpdates, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note("Updates %s", tile)
	if f.updates.off {
		return wsUpdates{}, errWsUnavailable
	}
	if _, err := f.handle(sid); err != nil {
		return wsUpdates{}, err
	}
	if tile == "" {
		return f.updates.u, nil
	}
	if _, err := f.tile(sid, tile, true); err != nil {
		return wsUpdates{}, err
	}
	out := wsUpdates{}
	for _, r := range f.updates.u.Builtins {
		if r.Tile == tile {
			out.Builtins = append(out.Builtins, r)
		}
	}
	for _, r := range f.updates.u.Instances {
		if r.Tile == tile {
			out.Instances = append(out.Instances, r)
		}
	}
	return out, nil
}

// builtinLine is a builtin.update line as xbind gives it to the agent: the
// argument's own sentence, always an admin's; blocked for an id xbind lists
// no update of.
func (f *fakeXbin) builtinLine(item *xbin.ChangeSetItem, args map[string]any) {
	id := str(args["id"])
	item.Line, item.Admin = "Update the builtin "+id, true
	for _, r := range f.updates.u.Builtins {
		if r.ID == id {
			return
		}
	}
	item.Blocked = "it has no update: it is what this xbin ships"
}

// builtinOut is what applying id's line made.
func (f *fakeXbin) builtinOut(id string) json.RawMessage {
	o, ok := f.updates.outcomes[id]
	if !ok {
		o = wsBuiltinOut{Update: "updated", Files: 1}
	}
	raw, _ := json.Marshal(o)
	return raw
}
