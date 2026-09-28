package obs

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// Logs per deployment (11-contract §8; 08-data §2; 07-runtime §9).
//
// main keeps today's .xbin/log/<CompKey>.log whether or not it is the
// primary (the name rule); any other deployment's backend log is
// .xbin/deploy/<TileKey>/d/<name>/backend.log, which the runner writes.
//
// Which one a request reads (11-contract §0.4 DR1, DR2): a tile's frame and
// instance principals read their bound deployment's, and naming another is
// refused, so the primary's frame token never reads a non-primary log; the
// tile's terminal and agent tokens, humans with terminal level and admins
// read any deployment's they name, and without a name their session's
// target, or the primary. A non-primary answer carries X-XBin-Deployment,
// and so does every answer to a request that named a deployment: the echo
// an older xbind, which ignores the parameter, never sends (12-compat
// NP-12-2).

// deploymentHeader is the echo, a response header (the proxy's name for it).
const deploymentHeader = "X-XBin-Deployment"

// errBadDeploymentName is 11-contract §1.14's text.
var errBadDeploymentName = errors.New(`deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`)

// logsDeployment is the deployment of comp whose log a request by p reads,
// named being the request's ?deployment= ("" for none); the error's status
// is returned beside it. canReadLogs has admitted p.
func (o *Plane) logsDeployment(p auth.Principal, comp, named string) (string, int, error) {
	if named != "" && !util.DeploymentNameOK(named) {
		return "", http.StatusBadRequest, errBadDeploymentName
	}
	own := p.Component == comp
	switch {
	case own && p.Via != "terminal": // a frame or instance principal: its bound deployment's only
		dep, err := o.addressed(p, comp)
		if err != nil {
			return "", refusalStatus(err), err
		}
		if named != "" && named != dep {
			return "", http.StatusForbidden, fmt.Errorf("a tile's own credentials act only on their own deployment (%s) — its frames and backend read only that deployment's log", dep)
		}
		return dep, 0, nil
	case named != "":
		if !o.exists(comp, named) {
			return "", http.StatusNotFound, util.NoDeployment(comp, named)
		}
		return named, 0, nil
	case own: // a terminal or agent session: its target, or the primary
		dep, err := o.addressed(p, comp)
		if errors.Is(err, util.ErrNoDeployment) {
			return "", http.StatusNotFound, err
		}
		if err == nil {
			return dep, 0, nil
		}
	}
	return o.primary(comp), 0, nil
}

// refusalStatus is the status of an addressed error: 404 for a deployment
// that no longer exists, 403 for a refusal.
func refusalStatus(err error) int {
	if errors.Is(err, util.ErrNoDeployment) {
		return http.StatusNotFound
	}
	return http.StatusForbidden
}

// exists reports whether tile has a deployment called name, as the plane's
// Addressed answers for an instance principal bound to it: that deployment
// when it exists, util.ErrNoDeployment otherwise.
func (o *Plane) exists(tile, name string) bool {
	dep, err := o.addressed(auth.Principal{Component: tile, Via: "instance", Deployment: name}, tile)
	return err == nil && dep == name
}

// logOpener opens deployment dep of comp's backend log: main's where it has
// always been; another's beneath the tile's deploy state, never through a
// symlink (the directory sits beside materialized checkpoint trees).
func (o *Plane) logOpener(comp, dep string) func() (*os.File, error) {
	if dep == util.MainDeployment {
		path := filepath.Join(o.Root, ".xbin", "log", util.CompKey(comp)+".log")
		return func() (*os.File, error) { return os.Open(path) }
	}
	root := filepath.Join(o.Root, ".xbin", "deploy")
	sub := util.TileKey(comp) + "/d/" + dep
	return func() (*os.File, error) { return fsutil.OpenIn(root, sub, "backend.log") }
}
