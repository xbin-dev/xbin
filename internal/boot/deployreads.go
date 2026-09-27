package boot

// deployreads.go — the tile-deployments API's reads beyond the state: the
// deploy log (11-contract §1.10), the diff (§1.11) and the checkpoint remote
// (§1.12). Each judges the caller with the plane's one authorize function
// and answers what its source says (deployReads, deployments.go); without
// its source it answers 501, as a reserved route.

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- GET /deployments/log ----

func (a *deploymentsAPI) getLog(w http.ResponseWriter, r *http.Request) {
	if a.reads.log == nil || a.reads.entry == nil {
		reservedRoute(w, r)
		return
	}
	pr, q := auth.PrincipalOf(r), r.URL.Query()
	t, e := a.resolve(q.Get("tile"))
	dep := q.Get("deployment")
	if e == nil && t.qualified {
		if dep != "" && dep != t.dep {
			e = &dpe{Status: http.StatusBadRequest, Msg: "the tile ref names " + t.dep + ", deployment names " + dep}
		}
		dep = t.dep
	}
	if e == nil {
		e = a.readGate(pr, t, deployments.OpLog)
	}
	if e == nil && !t.active() {
		e = noRecord(t.c.Path, http.StatusConflict)
	}
	own := "" // a non-primary deployment's own principals read its entries only
	if e == nil && a.dp.Audience(pr, t.subject("")) == deployments.AudienceDeployment {
		if own = bound(pr); dep == "" {
			dep = own
		} else if dep != own {
			e = &dpe{Status: http.StatusForbidden, Kind: deployments.KindAuthority,
				Msg: "a tile's own credentials act only on their own deployment (" + own + ")"}
		}
	}
	if e == nil && dep != "" && !a.dp.HasDeployment(t.c.Path, dep) {
		e = noDeployment(t.c.Path, dep)
	}
	id, idErr := queryInt(q.Get("id"), 0)
	wait, waitErr := queryInt(q.Get("wait"), 0)
	limit, limitErr := queryInt(q.Get("limit"), 50)
	before, beforeErr := queryInt(q.Get("before"), 0)
	if e == nil && (errors.Join(idErr, waitErr, limitErr, beforeErr) != nil || id < 0 || wait < 0 || limit < 1 || before < 0) {
		e = &dpe{Status: http.StatusBadRequest, Msg: "id and before are deploy ids; wait, seconds (at most 25); limit, 1 to 200"}
	}
	if e != nil {
		writeDeployError(w, e)
		return
	}
	if id > 0 {
		ent, err := a.reads.entry(r.Context(), t.c.Path, int64(id), time.Duration(min(wait, 25))*time.Second)
		m := asMap(ent)
		switch {
		case err != nil:
			writeDeployErr(w, err)
		case ent == nil || m == nil || (own != "" && m["deployment"] != own):
			writeDeployError(w, &dpe{Status: http.StatusNotFound, Msg: fmt.Sprintf("%s has no deploy %d", t.c.Path, id)})
		default:
			server.WriteJSON(w, http.StatusOK, map[string]any{"entry": m})
		}
		return
	}
	entries, more, err := a.reads.log(r.Context(), t.c.Path, dep, min(limit, 200), int64(before))
	if err != nil {
		writeDeployErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"tile": t.c.Path, "entries": asMaps(entries), "more": more})
}

func queryInt(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	return strconv.Atoi(s)
}

// ---- GET /deployments/diff ----

// diffQuery is a judged diff request (11-contract §1.11).
type diffQuery struct {
	Tile     string
	From, To diffSide
	Path     string // one tile-relative file, or ""
	Stat     bool
	By       auth.Principal // who asks: a work-tree side's capture is theirs
}

// diffSide is one spec, a deployment's resolved from the record: its
// checkpoint, or the work tree while it follows it.
type diffSide struct {
	Spec       string // as sent, or the default
	WorkTree   bool   // the work tree: a capture
	ID         string // a checkpoint id the client sent (c:…)
	Deployment string // the deployment a deployment: spec named
	Tree       string // that deployment's checkpoint, a full tree id
}

func (a *deploymentsAPI) getDiff(w http.ResponseWriter, r *http.Request) {
	if a.reads.diff == nil {
		reservedRoute(w, r)
		return
	}
	pr, q := auth.PrincipalOf(r), r.URL.Query()
	t, e := a.resolve(q.Get("tile"))
	var from, to diffSide
	if e == nil {
		from, e = t.diffSide(q.Get("from"), "deployment:"+t.primary())
	}
	if e == nil {
		to, e = t.diffSide(q.Get("to"), "work-tree")
	}
	p, stat := q.Get("path"), q.Get("stat")
	if e == nil && ((p != "" && !cleanRel(p)) || (stat != "" && stat != "0" && stat != "1")) {
		e = &dpe{Status: http.StatusBadRequest, Msg: "path is one clean tile-relative file; stat is 1 or absent"}
	}
	if e == nil { // a work-tree side captures: terminal level (06-security T10)
		o := deployments.OpDiff
		if from.WorkTree || to.WorkTree {
			o = deployments.OpDiffWorkTree
		}
		e = a.readGate(pr, t, o)
	}
	if e == nil && !t.active() { // nothing to diff, nothing captured, no store (P5)
		e = &dpe{Status: http.StatusConflict, Kind: deployments.KindState,
			Msg: t.c.Path + " has no deployments: its work tree is what runs, so there is nothing to diff"}
	}
	for _, s := range []diffSide{from, to} {
		if e == nil && s.Deployment != "" && !a.dp.HasDeployment(t.c.Path, s.Deployment) {
			e = noDeployment(t.c.Path, s.Deployment)
		}
	}
	if e != nil {
		writeDeployError(w, e)
		return
	}
	if err := a.reads.diff(w, r, diffQuery{Tile: t.c.Path, From: from, To: to, Path: p, Stat: stat == "1", By: pr}); err != nil {
		writeDeployErr(w, err)
	}
}

// diffSide parses one spec: c:<id> | deployment:<name> | work-tree.
func (t tileRef) diffSide(spec, def string) (diffSide, *dpe) {
	if spec == "" {
		spec = def
	}
	d := diffSide{Spec: spec}
	name, isDep := strings.CutPrefix(spec, "deployment:")
	_, idErr := checkpoint.ParseID(spec)
	switch {
	case spec == "work-tree":
		d.WorkTree = true
	case idErr == nil:
		d.ID = spec
	case isDep && util.DeploymentNameOK(name):
		d.Deployment = name
		if r := t.found.Record; r != nil && r.Deployments[name] != nil { // a held record has none
			d.WorkTree = r.Deployments[name].Checkpoint == nil
			if cp := r.Deployments[name].Checkpoint; cp != nil {
				d.Tree = *cp
			}
		}
	default:
		return d, &dpe{Status: http.StatusBadRequest, Msg: "bad spec " + strconv.Quote(spec) + ": c:<id> | deployment:<name> | work-tree"}
	}
	return d, nil
}

// ---- GET /checkpoints/{rest...} ----

// getFetch serves the checkpoint remote (11-contract §1.12), judged on
// every request, to the tile's own terminal and agent sessions while their
// user holds write, and to people with write at their current level; never
// to frame or instance tokens, code: grants or other tiles.
func (a *deploymentsAPI) getFetch(w http.ResponseWriter, r *http.Request) {
	if a.reads.fetch == nil {
		reservedRoute(w, r)
		return
	}
	pr := auth.PrincipalOf(r)
	t, gitPath, e := a.splitRemote(r.PathValue("rest")) // decoded: the remote escapes each segment
	if e == nil {
		e = a.readGate(pr, t, deployments.OpFetch)
	}
	if e == nil && !t.active() {
		e = noRecord(t.c.Path, http.StatusNotFound)
	}
	if e != nil {
		writeDeployError(w, e)
		return
	}
	a.reads.fetch(w, r, t.c.Path, gitPath)
}

// splitRemote splits "<tile>.git/<git path>" at the last segment ending in
// .git whose prefix is a registered tile: a tile path may hold
// .git-suffixed segments, a git path never does.
func (a *deploymentsAPI) splitRemote(rest string) (tileRef, string, *dpe) {
	segs := strings.Split(rest, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		name, ok := strings.CutSuffix(segs[i], ".git")
		tile := strings.Join(append(slices.Clone(segs[:i]), name), "/")
		if !ok || name == "" || !cleanRel(tile) {
			continue
		}
		if c, ok := a.dp.Reg.Component(tile); ok {
			return a.ref(c, "", false), strings.Join(segs[i+1:], "/"), nil
		}
	}
	return tileRef{}, "", noTile(strings.TrimSuffix(strings.SplitN(rest, ".git/", 2)[0], ".git"))
}
