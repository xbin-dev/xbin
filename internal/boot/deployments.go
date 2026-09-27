package boot

// deployments.go — the tile-deployments API: pausing live reload, tile
// deployments and promotion (docs/protocol.md, "Tile deployments"). Every
// route of that contract is mounted here, once, so feature work never adds a
// route literal: TestRouteInventory ties each one to its openapi.go and
// protocol.md rows. A route whose operation this xbind doesn't build yet is
// reserved: it answers 501 through server.WriteError (NP-14-4), and its
// openapi.go row is marked x-xbin-reserved.

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/server"
)

// registerDeploymentsAPI mounts the /deployments family and the checkpoint
// remote over dp, the deployments plane. Nothing here reads or writes a
// tile's files: a tile with no deployment record stays in the zero state (P5).
func registerDeploymentsAPI(srv *server.Server, dp *deployments.Plane) {
	// Reading: the state (the caller's view), the deploy log, the diff.
	srv.RegisterAPI("GET /deployments", reservedRoute)
	srv.RegisterAPI("GET /deployments/log", reservedRoute)
	srv.RegisterAPI("GET /deployments/diff", reservedRoute)

	// Live reload.
	srv.RegisterAPI("POST /deployments/live-reload/pause", reservedRoute)
	srv.RegisterAPI("POST /deployments/live-reload/resume", reservedRoute)
	srv.RegisterAPI("POST /deployments/live-reload/now", reservedRoute)
	srv.RegisterAPI("POST /deployments/live-reload/attach", reservedRoute)

	// Adding and removing deployments; moving code (restart rides deploy).
	srv.RegisterAPI("POST /deployments/add", reservedRoute)
	srv.RegisterAPI("POST /deployments/remove", reservedRoute)
	srv.RegisterAPI("POST /deployments/deploy", reservedRoute)
	srv.RegisterAPI("POST /deployments/promote", reservedRoute)
	srv.RegisterAPI("POST /deployments/rollback", reservedRoute)

	// Governance: tile managers, in a person's own session.
	srv.RegisterAPI("POST /deployments/primary", reservedRoute)
	srv.RegisterAPI("POST /deployments/protect", reservedRoute)
	srv.RegisterAPI("POST /deployments/edge", reservedRoute)
	srv.RegisterAPI("POST /deployments/deliveries", reservedRoute)
	srv.RegisterAPI("POST /deployments/always-on", reservedRoute)
	srv.RegisterAPI("POST /deployments/limits", reservedRoute)

	// Data: seed, reset, vault copy, and per-deployment backups beside
	// today's (whose bodies stay as they are).
	srv.RegisterAPI("POST /deployments/seed", reservedRoute)
	srv.RegisterAPI("POST /deployments/reset", reservedRoute)
	srv.RegisterAPI("POST /deployments/vault-copy", reservedRoute)
	srv.RegisterAPI("POST /deployments/backup", reservedRoute)
	srv.RegisterAPI("GET /deployments/backups", reservedRoute)
	srv.RegisterAPI("POST /deployments/restore", reservedRoute)
	srv.RegisterAPI("POST /deployments/backup-schedule", reservedRoute)

	// Run a deployment's cron job now.
	srv.RegisterAPI("POST /deployments/run-now", reservedRoute)

	// The checkpoint remote: read-only dumb HTTP git over the tile's view
	// repository (<tile>.git/<git path>).
	srv.RegisterAPI("GET /checkpoints/{rest...}", reservedRoute)
}

// reservedRoute answers a tile-deployments route whose operation isn't built
// in this xbind yet: 501 in the {"error", "docs"} shape of every API error.
// A client tells it from an older xbind, whose mux answers a plain-text 404
// or 405.
func reservedRoute(w http.ResponseWriter, r *http.Request) {
	server.WriteError(w, http.StatusNotImplemented,
		"reserved for tile deployments, not built in this xbind yet — "+r.Pattern, "/docs/protocol.md")
}
