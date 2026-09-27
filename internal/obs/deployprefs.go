package obs

import (
	"errors"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Prefs per deployment (05-model §9; 08-data §2, §3.3). A tile's element
// principal keeps prefs per (user, tile, deployment) by the name rule: main
// keeps today's data/prefs/<CompKey(user)>/<CompKey(tile)>.json whether or
// not it is the primary, and any other deployment has
// data/prefs/<CompKey(user)>/.deployments/<TileKey>/<name>.json, a dot level
// no CompKey can spell and older binaries never compute. The deployment is
// the one the principal acts in (11-contract §0.4 DR1): a frame or instance
// principal's bound one, a terminal or agent session's target; so prefs
// don't follow a reassignment of the primary. The shell's and every
// person's own bucket (no tile) are unchanged.

// prefsDeploymentFile is p's bucket, relative to its user's prefs directory,
// when p acts in a tile deployment beyond main; "" for main's and for
// people. An error is the deployment's: gone (util.ErrNoDeployment) or
// refused (prefsRefusal).
func (o *Plane) prefsDeploymentFile(p auth.Principal) (string, error) {
	if p.Component == "" {
		return "", nil
	}
	dep, err := o.addressed(p, p.Component)
	switch {
	case errors.Is(err, util.ErrNoDeployment):
		return "", err
	case err != nil:
		return "", prefsRefusal{err}
	case dep == util.MainDeployment:
		return "", nil
	}
	return ".deployments/" + util.TileKey(p.Component) + "/" + dep + ".json", nil
}

// prefsRefusal is a refused deployment (a session following a protected
// primary): 403.
type prefsRefusal struct{ error }

func (e prefsRefusal) Unwrap() error { return e.error }

// writePrefsErr answers a prefs failure: the deployment's gone (404) or
// refused (403), or the store's (500, as always).
func writePrefsErr(w http.ResponseWriter, err error) {
	var refused prefsRefusal
	switch {
	case errors.Is(err, util.ErrNoDeployment):
		server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/protocol.md")
	case errors.As(err, &refused):
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
	default:
		server.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}
