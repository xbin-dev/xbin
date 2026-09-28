// Package obs is the workspace's observability plane — what a workspace
// tells its people about its tiles and what they keep for themselves:
// tile status reports (in-memory, cleared when a backend restarts), per-user
// preferences (the shell's layout, any tile's own per-user state) and the
// backends' logs. The first plane lifted out of the broker (D63): it needs
// nothing of the broker but a few answers, so it takes them as fields.
package obs

import (
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Plane serves /tile-report, /prefs and /logs.
type Plane struct {
	Root         string                    // the workspace root: data/prefs, .xbin/log
	Hub          *events.Hub               // status events out, build-start events in
	IsAdmin      func(auth.Principal) bool // who may report for any tile and read every status
	HasComponent func(path string) bool    // whether a path is a registered component (logs)

	// The deployment input (P13): Primary names a tile's primary deployment;
	// Addressed answers which deployment of a tile a request by p reaches
	// (11-contract §0.4 DR1: the tile's own principals their bound
	// deployment, anyone else the primary), util.ErrNoDeployment for one
	// that no longer exists and any other error a refusal. The deployments
	// plane's answers, through the broker; nil: main, the only deployment of
	// a tile without a record.
	Primary   func(tile string) string
	Addressed func(p auth.Principal, tile string) (string, error)

	statusMu sync.Mutex
	statuses map[string]statusRec // component → last reported status

	prefsMu    sync.Mutex
	prefsLocks map[string]*sync.Mutex // bucket file → its read-modify-write lock
}

// Register mounts the plane's routes and starts the status watcher.
func (o *Plane) Register(srv *server.Server) {
	o.statuses = map[string]statusRec{}
	o.registerStatus(srv)
	o.registerPrefs(srv)
	o.registerLogs(srv)
}

// primary names tile's primary deployment.
func (o *Plane) primary(tile string) string {
	if o.Primary != nil {
		return o.Primary(tile)
	}
	return util.MainDeployment
}

// addressed is the deployment of tile a request by p reaches.
func (o *Plane) addressed(p auth.Principal, tile string) (string, error) {
	if o.Addressed != nil {
		return o.Addressed(p, tile)
	}
	if p.Component == tile && p.Deployment != "" && p.Deployment != util.MainDeployment {
		return "", util.NoDeployment(tile, p.Deployment)
	}
	return util.MainDeployment, nil
}
