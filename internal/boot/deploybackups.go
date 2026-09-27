package boot

// deploybackups.go — GET /deployments/backups (11-contract §1.8; 08-data
// §11): a deployment's archived versions, an admin's read, through the
// plane's DataBackups hook (the broker's). Without the hook it answers 501,
// as a reserved route.

import (
	"cmp"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// getBackups lists the versions of one deployment's archive: ?deployment=,
// else the ref's qualifier, else the primary. The deployment need not exist
// any more: removing a deployment keeps its archives. A tile without a
// record has only main's, the tile's own backups (GET /backups).
func (a *deploymentsAPI) getBackups(w http.ResponseWriter, r *http.Request) {
	if a.dp.DataBackups == nil {
		reservedRoute(w, r)
		return
	}
	pr, q := auth.PrincipalOf(r), r.URL.Query()
	t, e := a.resolve(q.Get("tile"))
	dep := q.Get("deployment")
	switch {
	case e != nil:
	case t.qualified && dep != "" && dep != t.dep:
		e = &dpe{Status: http.StatusBadRequest, Msg: "the tile ref names " + t.dep + ", deployment names " + dep}
	case dep != "" && !util.DeploymentNameOK(dep):
		e = &dpe{Status: http.StatusBadRequest, Msg: "deployment names are lowercase letters, digits and \"-\", start with a letter, at most 24 characters"}
	default:
		if t.qualified {
			dep = t.dep
		}
		e = a.readGate(pr, t, deployments.OpBackups)
	}
	if e != nil {
		writeDeployError(w, e)
		return
	}
	res, err := a.dp.DataBackups(t.c.Path, cmp.Or(dep, t.primary()))
	if err != nil {
		writeDeployErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, res)
}
