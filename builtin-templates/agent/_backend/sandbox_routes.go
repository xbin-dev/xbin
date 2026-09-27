// sandbox_routes.go — the sandboxes routes (API.md §Coding sandboxes): the
// catalog as the caller may see it, and managing sandboxes at the managers.
package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// sandboxRoutes are registered with the route table's (routes.go).
func sandboxRoutes() []routeDef {
	return []routeDef{
		{"GET /sandboxes", needAny, handleSandboxes},
	}
}

// handleSandboxes lists the sandboxes the caller may see across every bound
// manager, and the managers (what they offer, or why they can't be used).
//
//	GET /sandboxes[?fresh=1]
//	→ {sandboxes: [{ref, provider, manager, …the contract's sandbox…,
//	    mine, canUse, canManage, canEdit}], managers: [{provider, title, ok,
//	    error?, caps, egress, images, sizes, limits}]}
func handleSandboxes(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if r.URL.Query().Get("fresh") == "1" {
		invalidateSandboxCatalog()
	}
	cat := sandboxCatalog(r.Context())
	out := []map[string]any{}
	for _, e := range cat.Sandboxes {
		a := sandboxAccess(c, e.Box)
		if !a.seen() {
			continue
		}
		out = append(out, sandboxItem(e, a))
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"sandboxes": out, "managers": cat.Managers})
}

// sandboxItem is one sandbox as the routes show it: the manager's resource
// with the agent's reference and the caller's access.
func sandboxItem(e sbxCatalogEntry, a sbxAccess) map[string]any {
	v := e.Box.view()
	v["ref"], v["provider"], v["manager"] = e.Ref, e.Provider, e.Manager
	v["mine"], v["canUse"], v["canManage"], v["canEdit"] = a.Mine, a.Use, a.Manage, a.Edit
	return v
}
