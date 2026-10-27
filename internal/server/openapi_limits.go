package server

// openapi_limits.go — the tile resource limits routes (D203,
// docs/protocol.md §Tile limits). endpoints() appends them.

const limitsTag = "Limits"

func limitsEndpoints() []ep {
	const person = "a person's own session, app or device, the root token, the shell's frame (its driver), or the admin console driven by a workspace admin; never a tile's backend, frame, terminal or agent"
	kind := str("memMiB | diskGiB | vmMemMiB | vmVCPUs | sbxMemMiB | sbxVCPUs | sbxDiskGiB (or the bx alias: memory, disk, vm-memory, …)")
	target := []oapi{queryParam("tile", "the tile (diskGiB: one that roots its scope)", false),
		queryParam("scope", "diskGiB: the scope (its path or data key; workspace = the workspace scope)", false), queryParam("kind", "the kind", true)}
	return []ep{
		{"GET", "/limits", limitsTag, "Tiles' resource limits", "any caller; what it lists depends on who asks",
			"Every value an integer in its kind's unit (MiB, GiB, vCPUs). No query: {tiles: [Row], defaults, enforced, orgCeilings, canSetCeilings} — rows for every tile a workspace admin sees, an org admin's org's, a person's own or managed tiles, a tile credential's own. " +
				"Row: {tile, scope, scopeKey, rootsScope, org?, limits: {<kind>: {effective, default, max, enforced, ask?, covered?, pending?, set?, approved?, dormant?, usage?, applied?, appliesAt?, hit?, note?, approvers, canDecide?, canRevert?, choices?}}} — only kinds that apply to the tile, or that its manifest asks (note: why the ask is ignored). " +
				"tile=: {tile: Row, capacity?: {<kind>: {kind, max, total, free, reserve, promised}}} (capacity and choices: approvers only; readers and the tile's own credentials read the row). " +
				"pending=1: {pending: [{tile, scope?, kind, unit, why: ask|cap, ask?, hit?, effective, approvable, mine, approvers}]} — what the viewer may approve or manages. A shell frame reads as its driver.",
			[]oapi{queryParam("tile", "one tile's row", false), queryParam("pending", "1: what waits for the caller", false)}, nil,
			"{tiles, defaults, enforced, orgCeilings, canSetCeilings} | {tile, capacity?} | {pending}"},
		{"POST", "/limits", limitsTag, "Approve, raise or set a tile's limit", person + ": a workspace admin, or an admin of the owning org up to the org's ceiling",
			"source: manifest-ask (approve the manifest's ask, up to what it asks: applies while it asks), raise or admin-set (holds whatever the manifest says). value above the default (400 otherwise: DELETE goes back to it) and at most the maximum (400). " +
				"403: not an approver, above the org's ceiling, force by a non-workspace-admin. 409: doesn't fit the workspace (why), unless force by a workspace admin — fit is checked under the store's lock. 500: data/tile-limits.json can't be read (never overwritten). " +
				"The decision clears the kind's hit; memory applies live (a lower value at the next start), disk at once, VM sizes at the next start (restart: now). Audited; publishes a limits event.",
			nil, jsonBody("the decision", oapi{"tile": str("the tile"), "scope": str("diskGiB: the scope, instead of its root tile"), "kind": kind, "value": integer(),
				"source": str("manifest-ask | raise | admin-set"), "restart": boolean(), "force": boolean()}, "kind", "value", "source"), "{tile: Row}"},
		{"DELETE", "/limits", limitsTag, "Put a tile's limit back to its default", person + ": the tile's managers (its user-owner, the owning org's admins) or a workspace admin",
			"Both decisions on the kind go; a manifest ask still standing is pending again. Audited.", target, nil, "{tile: Row}"},
		{"POST", "/limits/dismiss", limitsTag, "Dismiss a tile's cap hit for everyone", person + ": an approver of the kind",
			"The sticky at-cap alert (an OOM kill, a full quota, a sandbox refused) goes for everyone; the next hit shows again. An alert's dismiss names this route. 204.", target, nil, "204"},
		{"PUT", "/orgs/{org}/limits", "Orgs", "Set an org's per-tile resource ceilings", "xbin:users",
			"{memMiB?: n|null, …}: the most an admin of the org may approve for one tile it owns, per kind (null or 0 removes a kind; a kind not named keeps its ceiling). A kind without one: the org's admins can't raise it. GET /orgs carries limits.",
			[]oapi{pathParam("org", "org id")}, jsonBody("the ceilings", oapi{"memMiB": integer(), "diskGiB": integer(), "vmMemMiB": integer(), "vmVCPUs": integer(),
				"sbxMemMiB": integer(), "sbxVCPUs": integer(), "sbxDiskGiB": integer()}), "{limits}"},
	}
}
