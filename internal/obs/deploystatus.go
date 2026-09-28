package obs

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// Status per deployment (D127h) (11-contract §3.2, §3.3; 08-data §2).
//
// The bare <tile> entry of statuses holds the primary's status, whichever
// deployment is the primary, and is what GET /tile-report lists and the
// status event announces, as today. Another deployment's status is kept
// under <tile>\x00<name>, a key no component path can spell (paths hold no
// NUL), which GET /tile-report never lists; it rides only the deployments
// event, op status, to the tile's write audience and that deployment's own
// principals (the server's filter). So a non-primary report, clear or build
// never touches the primary's status, and never reaches an old shell's
// toasts or title mark. A reassignment of the primary exchanges the two
// entries (settlePrimary).

// depKey is deployment dep of tile's key in statuses; isDepKey tells such a
// key from a tile's.
func depKey(tile, dep string) string { return tile + "\x00" + dep }
func isDepKey(key string) bool       { return strings.IndexByte(key, 0) >= 0 }

// reportDeployment is the deployment of comp a POST /tile-report by p
// reports for (11-contract §0.4): the tile's own principal, its bound
// deployment (DR1); an admin naming the tile, its primary (DR2). The error's
// status is returned beside it.
func (o *Plane) reportDeployment(p auth.Principal, comp string) (string, int, error) {
	if p.Component != comp {
		return o.primary(comp), 0, nil
	}
	dep, err := o.addressed(p, comp)
	switch {
	case errors.Is(err, util.ErrNoDeployment):
		return "", http.StatusNotFound, err
	case err != nil:
		return "", http.StatusForbidden, err
	}
	return dep, 0, nil
}

// docsFor is an error answer's docs page (11-contract §1.14).
func docsFor(code int) string {
	if code == http.StatusForbidden {
		return "/docs/auth.md"
	}
	return "/docs/protocol.md"
}

// setDepStatus stores (or, for ok with no message, clears) non-primary
// deployment dep of tile's status and announces it; a transient report is
// announced only.
func (o *Plane) setDepStatus(tile, dep string, rec statusRec, transient bool) {
	rec.dep = dep
	if !transient {
		o.statusMu.Lock()
		if rec.Level == "ok" && rec.Message == "" {
			delete(o.statuses, depKey(tile, dep))
		} else {
			o.statuses[depKey(tile, dep)] = rec
		}
		o.statusMu.Unlock()
	}
	o.publishDepStatus(tile, dep, rec, transient)
}

// publishDepStatus announces deployment dep of tile's status in the
// deployments event, op status: never the status type (11-contract §3.3).
func (o *Plane) publishDepStatus(tile, dep string, rec statusRec, transient bool) {
	o.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: map[string]any{
		"op": "status", "deployment": dep, "level": rec.Level, "message": rec.Message, "ts": rec.TS,
		"transient": transient}})
}

// clearStatus clears deployment dep of tile's stored status, when it has
// one, at a restart or a swap: the primary's is today's bare entry and
// status event, another's its own entry and op status.
func (o *Plane) clearStatus(tile, dep string) {
	primary := dep == o.primary(tile)
	key := tile
	if !primary {
		key = depKey(tile, dep)
	}
	o.statusMu.Lock()
	_, had := o.statuses[key]
	delete(o.statuses, key)
	o.statusMu.Unlock()
	if !had {
		return
	}
	rec := statusRec{Level: "ok", TS: time.Now().Unix()}
	if primary {
		o.publishStatus(tile, rec, false)
	} else {
		o.publishDepStatus(tile, dep, rec, false)
	}
}

// settlePrimary exchanges tile's statuses after a record change that moved
// its primary (08-data §2): the bare entry, the old primary's, becomes that
// deployment's own, announced in the deployments event; the new primary's
// own becomes the bare entry, announced as today's status event (a clear
// when it had none). Nothing moves while the bare entry is the primary's.
func (o *Plane) settlePrimary(tile string) {
	prim := o.primary(tile)
	o.statusMu.Lock()
	bare, hadBare := o.statuses[tile]
	next, hadNext := o.statuses[depKey(tile, prim)]
	if hadBare && (bare.dep == "" || bare.dep == prim) || !hadBare && !hadNext {
		o.statusMu.Unlock()
		return
	}
	delete(o.statuses, tile)
	if hadBare {
		o.statuses[depKey(tile, bare.dep)] = bare
	}
	if hadNext {
		delete(o.statuses, depKey(tile, prim))
		next.dep = prim
		o.statuses[tile] = next
	}
	o.statusMu.Unlock()
	if hadBare {
		o.publishDepStatus(tile, bare.dep, bare, false)
	}
	if !hadNext {
		next = statusRec{Level: "ok", TS: time.Now().Unix()}
	}
	o.publishStatus(tile, next, false)
}

// pruneStatuses forgets the statuses of tile's deployments that no longer
// exist, after a record change (a removal), so a deployment added again
// under the same name starts without one.
func (o *Plane) pruneStatuses(tile string) {
	prefix := tile + "\x00"
	o.statusMu.Lock()
	var deps []string
	for key := range o.statuses {
		if dep, ok := strings.CutPrefix(key, prefix); ok {
			deps = append(deps, dep)
		}
	}
	o.statusMu.Unlock()
	for _, dep := range deps {
		if !o.exists(tile, dep) {
			o.statusMu.Lock()
			delete(o.statuses, depKey(tile, dep))
			o.statusMu.Unlock()
		}
	}
}
