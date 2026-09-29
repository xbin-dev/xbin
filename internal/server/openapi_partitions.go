package server

// openapi_partitions.go — the routes of partitioned tiles
// (plans/partitions 06 §6, docs/partitions.md). endpoints() appends them.

func partitionEndpoints() []ep {
	return []ep{
		{"POST", "/partitions/limits", "Partitions", "Set the limits of people's partitions", "admin; a tile manager may lower their own tile's",
			"{tile?, maxRunning?, partitionBytes?}. Without tile: the workspace's cap on people's partition instances running at once (maxRunning; admin only). With tile: its per-tile running cap and each person's partition's byte ceiling — an admin's values, or a tile manager's lower ones (with their own session, app or device; never above the admin's value or the default: 403). 0 clears a value. Defaults: the caps derive from host memory, the ceiling is the tile's per-namespace one. Kept in data/partition-limits.json; takes effect at the next start. The tile's own credentials: 403; an unknown tile: 404. Audited.",
			nil, jsonBody("the limits", oapi{"tile": str("tile path; absent = the workspace"), "maxRunning": integer(), "partitionBytes": integer()}), "{tile?, limits?: {maxRunning, partitionBytes}, workspace: {maxRunning}, defaults: {maxRunning, workspaceMaxRunning}}"},
	}
}
