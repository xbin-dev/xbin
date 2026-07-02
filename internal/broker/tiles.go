package broker

import (
	"log/slog"
	"net/http"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/builtins"
	"github.com/magik6k/buxon/internal/server"
)

// Builtin tile catalog endpoints (plans/tile-sharing.md). Listing is open to
// any authenticated principal; importing creates components, so it needs the
// same workspace-management capability as POST /create (owner or buxon:writer,
// which admin implies).

func (b *Broker) registerTiles(srv *server.Server) {
	srv.RegisterAPI("GET /builtins", b.apiBuiltinsList)
	srv.RegisterAPI("POST /builtins/import", b.apiBuiltinsImport)
	srv.RegisterAPI("GET /builtins/updates", b.apiBuiltinsUpdates)
	srv.RegisterAPI("POST /builtins/update", b.apiBuiltinsUpdate)
}

func (b *Broker) apiBuiltinsList(w http.ResponseWriter, r *http.Request) {
	if b.tiles == nil {
		server.WriteJSON(w, http.StatusOK, []builtins.Meta{})
		return
	}
	type item struct {
		builtins.Meta
		Installed bool `json:"installed"` // a component already exists at DefaultPath
	}
	out := []item{}
	for _, m := range b.tiles.List() {
		_, installed := b.Reg.Component(m.DefaultPath)
		out = append(out, item{Meta: m, Installed: installed})
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func (b *Broker) apiBuiltinsImport(w http.ResponseWriter, r *http.Request) {
	// Importing a tile creates a component: same capability as /create.
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) {
		role, ok := b.grantedRole(p.Component, "buxon")
		if p.Component == "" || !ok || !roleSatisfies(role, "writer", nil) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{
				"error": "importing tiles needs the workspace-management grant (buxon:writer) — the same as creating components",
				"docs":  "/docs/auth.md",
			})
			return
		}
	}
	if b.tiles == nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "no builtin tiles embedded"})
		return
	}
	var body struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {name, path?}", "docs": "/docs/protocol.md"})
		return
	}
	installed, files, err := b.tiles.Import(b.Reg.Root, body.Name, body.Path)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Record provenance so this tile can be offered updates later
	// (plans/builtin-updates.md).
	if b.updater != nil {
		if err := b.updater.RecordTile(body.Name, installed); err != nil {
			slog.Warn("record tile provenance", "tile", body.Name, "err", err)
		}
	}
	// Make the new component (and its Go module, if any) usable immediately —
	// don't wait for the debounced watcher tick, so opening the tile right
	// after import doesn't race the go.work regeneration.
	_ = b.Reg.Rescan()
	if b.OnStructureChange != nil {
		b.OnStructureChange()
	}
	b.Provision()

	// Surface any cross-scope grants the imported tile now needs, so the UI
	// can point the owner at the grants panel.
	var pending []registryGrantLite
	for _, g := range b.Pending() {
		if hasPrefix(g.From, installed) {
			pending = append(pending, registryGrantLite{From: g.From, Target: g.Target, Role: g.Role})
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"path": installed, "files": files, "pendingGrants": pending,
	})
}

// requireWriter gates workspace-mutating builtin endpoints on the same
// capability as POST /create (owner or buxon:writer, which admin implies).
func (b *Broker) requireWriter(w http.ResponseWriter, r *http.Request) bool {
	p := auth.PrincipalOf(r)
	if b.IsAdmin(p) {
		return true
	}
	role, ok := b.grantedRole(p.Component, "buxon")
	if p.Component != "" && ok && roleSatisfies(role, "writer", nil) {
		return true
	}
	server.WriteJSON(w, http.StatusForbidden, map[string]string{
		"error": "this needs the workspace-management grant (buxon:writer) — the same as creating components",
		"docs":  "/docs/auth.md",
	})
	return false
}

func (b *Broker) apiBuiltinsUpdates(w http.ResponseWriter, r *http.Request) {
	if b.updater == nil {
		server.WriteJSON(w, http.StatusOK, []builtins.UnitUpdate{})
		return
	}
	ups, err := b.updater.Updates()
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ups == nil {
		ups = []*builtins.UnitUpdate{}
	}
	server.WriteJSON(w, http.StatusOK, ups)
}

func (b *Broker) apiBuiltinsUpdate(w http.ResponseWriter, r *http.Request) {
	if !b.requireWriter(w, r) {
		return
	}
	if b.updater == nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "update tracking unavailable"})
		return
	}
	var body struct {
		ID   string `json:"id"`
		Mode string `json:"mode"` // "replace" | "merge" | "pin" | "unpin"
	}
	if err := decodeJSON(r, &body); err != nil || body.ID == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, mode}", "docs": "/docs/protocol.md"})
		return
	}
	var (
		files []string
		err   error
	)
	switch body.Mode {
	case "replace":
		files, err = b.updater.ApplyReplace(body.ID)
	case "merge":
		files, err = b.updater.ApplyMerge(body.ID)
	case "pin":
		err = b.updater.Pin(body.ID, true)
	case "unpin":
		err = b.updater.Pin(body.ID, false)
	default:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be replace|merge|pin|unpin"})
		return
	}
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// A scaffold/tile update can add or change a Go backend — reconcile deps and
	// reprovision so the change is live at once (same as import).
	if body.Mode == "replace" || body.Mode == "merge" {
		_ = b.Reg.Rescan()
		if b.OnStructureChange != nil {
			b.OnStructureChange()
		}
		b.Provision()
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"files": files})
}

type registryGrantLite struct {
	From   string `json:"from"`
	Target string `json:"target"`
	Role   string `json:"role"`
}

func hasPrefix(comp, root string) bool {
	return comp == root || len(comp) > len(root) && comp[:len(root)+1] == root+"/"
}
