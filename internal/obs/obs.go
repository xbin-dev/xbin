// Package obs is the workspace's observability plane — what a workspace
// tells its people about its tiles and what they keep for themselves:
// tile status reports (in-memory, cleared when a backend restarts), per-user
// preferences (the shell's layout, any tile's own per-user state) and the
// backends' logs. The first plane lifted out of the broker (D63): it needs
// nothing of the broker but three answers, so it takes them as fields.
package obs

import (
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
)

// Plane serves /tile-report, /prefs and /logs.
type Plane struct {
	Root         string                    // the workspace root: data/prefs, .xbin/log
	Hub          *events.Hub               // status events out, build-start events in
	IsAdmin      func(auth.Principal) bool // who may report for any tile and read every status
	HasComponent func(path string) bool    // whether a path is a registered component (logs)

	statusMu sync.Mutex
	statuses map[string]statusRec // component → last reported status
}

// Register mounts the plane's routes and starts the status watcher.
func (o *Plane) Register(srv *server.Server) {
	o.statuses = map[string]statusRec{}
	o.registerStatus(srv)
	o.registerPrefs(srv)
	o.registerLogs(srv)
}
