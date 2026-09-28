package boot

// deployreads.go — the tile-deployments API's reads beyond the record: the
// state's facts from the other planes (11-contract §1.1), the deploy log
// (§1.10), the diff (§1.11) and the checkpoint remote (§1.12). Each judges
// the caller with the plane's one authorize function and answers what its
// source says (deployReads, deployments.go); without its source a fact is
// left out and a route answers 501, as a reserved one.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// planeReads are the reads' sources in xbind: the plane's runner status,
// checkpoint store (WP-11, WP-12), deploy queue and log (WP-14b) and drift
// count (WP-20).
func planeReads(dp *deployments.Plane) deployReads {
	return deployReads{
		status:     runnerStatus(dp),
		checkpoint: dp.Checkpoint,
		queue: func(tile, dep string) (any, []any) {
			df := dp.Deploys(context.Background(), tile, dep)
			var deploying any
			if df.Deploying != nil {
				deploying = *df.Deploying
			}
			queued := make([]any, 0, len(df.Queued))
			for _, b := range df.Queued {
				queued = append(queued, b)
			}
			return deploying, queued
		},
		lastDeploy: func(ctx context.Context, tile, dep string) (any, error) {
			if last := dp.Deploys(ctx, tile, dep).LastDeploy; last != nil {
				return *last, nil
			}
			return nil, nil
		},
		workTree: func(tile string) (int, bool) {
			d, ok := dp.WorkTreeDrift(tile)
			return d.Changed, ok
		},
		log: func(ctx context.Context, tile, dep string, limit int, before int64) ([]any, bool, error) {
			es, more := dp.Log(ctx, tile, dep, limit, before)
			out := make([]any, len(es))
			for i, e := range es {
				out[i] = e
			}
			return out, more, nil
		},
		entry: func(ctx context.Context, tile string, id int64, wait time.Duration) (any, error) {
			e, err := dp.Entry(ctx, tile, id, wait)
			if errors.Is(err, deployments.ErrNoAttempt) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return e, nil
		},
		diff: func(w http.ResponseWriter, r *http.Request, q diffQuery) error { return writeDiff(w, r, dp, q) },
		fetch: func(w http.ResponseWriter, r *http.Request, tile, gitPath string) {
			if err := dp.ServeFetch(w, r, tile, gitPath); err != nil { // ErrNotFetchable: nothing written yet
				writeDeployErr(w, err)
			}
		},
		factReads: planeFacts(dp),
	}
}

// ---- the state's facts from the other planes (11-contract §1.1) ----

// factReads are the sources of what a tile with a record shows beyond its
// record: on each deployment, its data, vault, limits, alwaysOn declaration,
// backup schedule, registrations and held notifications; on the state, the
// admission caps and the edges. A nil source leaves its fact out. The plane
// answers data, limits, the declaration and caps (planeFacts); the broker
// the vault, registrations, edges and the tile's disk quota (from).
type factReads struct {
	data          func(tile, dep string) *deployments.DataState
	vault         func(tile, dep string) (deployments.VaultSummary, error)
	limits        func(tile, dep string) cgroup.Limits // effective, the tile's caps lowered (P22)
	diskQuota     func(tile string) int64              // the tile's disk quota in bytes; 0: none
	declared      func(c *registry.Component, dep string) (alwaysOn, known bool)
	registrations func(tile, dep string) []deployments.Registration
	wouldNotify   func(tile, dep string) []deployments.WouldNotify   // held notifications, newest last (P13)
	backup        func(tile, dep string) *deployments.BackupSchedule // nil: no schedule
	caps          func(tile string) deployments.Caps
	edges         func(tile string) []deployments.Edge
}

// brokerFacts are the broker's answers the state's facts need
// (*broker.Broker): a deployment's vault summary and registrations, the
// tile's edges with their policy, and its disk quota.
type brokerFacts interface {
	DeploymentVault(tile, dep string) (deployments.VaultSummary, error)
	DeploymentRegistrations(tile, dep string) []deployments.Registration
	EdgesOf(tile string) []deployments.Edge
	TileDiskStatus(tile string) (usage, quota int64, blocked bool)
}

var _ brokerFacts = (*broker.Broker)(nil)

// planeFacts are the facts the plane answers. Deployment.data is the
// broker's data hook, which boot installs on the plane after the API's
// sources are built, so it is read per call.
func planeFacts(dp *deployments.Plane) factReads {
	return factReads{
		data: func(tile, dep string) *deployments.DataState {
			if dp.DataOf == nil {
				return nil
			}
			return dp.DataOf(tile, dep)
		},
		limits:   dp.LimitsFor,
		declared: func(c *registry.Component, dep string) (bool, bool) { return declaredAlwaysOn(dp, c, dep) },
		caps:     dp.CapsOf,
		// the broker's schedule and the push plane's held notifications,
		// installed on the plane after the API's sources: read per call
		backup: func(tile, dep string) *deployments.BackupSchedule {
			if dp.BackupScheduleOf == nil {
				return nil
			}
			return dp.BackupScheduleOf(tile, dep)
		},
		wouldNotify: func(tile, dep string) []deployments.WouldNotify {
			if dp.WouldNotify == nil {
				return nil
			}
			return dp.WouldNotify(tile, dep)
		},
	}
}

// from takes the broker's facts from b.
func (r *factReads) from(b brokerFacts) {
	r.vault, r.registrations, r.edges = b.DeploymentVault, b.DeploymentRegistrations, b.EdgesOf
	r.diskQuota = func(tile string) int64 {
		_, quota, _ := b.TileDiskStatus(tile)
		return quota
	}
}

// declaredAlwaysOn answers whether deployment dep of c's own code says
// "alwaysOn" (07-runtime §11): the primary's is the registry's component,
// composed from the primary's code; another's is its work tree's manifest
// while it follows the work tree, its checkpoint's while pinned, read from
// the checkpoint's materialized tree when one is there. A read never
// extracts a checkpoint (P5; reading the state is pure): known is false
// then.
func declaredAlwaysOn(dp *deployments.Plane, c *registry.Component, dep string) (alwaysOn, known bool) {
	if dep == dp.Primary(c.Path) {
		return c.Manifest.AlwaysOn, true
	}
	code, err := dp.CodeFor(c.Path, dep)
	switch {
	case err != nil:
		return false, false
	case code.WorkTree:
		return c.WorkTreeManifest().AlwaysOn, true
	case dp.Reg == nil || dp.Root == "":
		return false, false
	}
	root := filepath.Join((&checkpoint.Store{Root: dp.Root}).TreesDir(c.Path), code.Tree)
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() { // xbind's own .xbin/deploy: no tile writes there
		return false, false
	}
	v, err := dp.Reg.View(c, registry.ViewCode{Deployment: dep, Tree: code.Tree, Root: root})
	if err != nil {
		return false, false
	}
	return v.Manifest.AlwaysOn, true
}

// addFacts adds the facts of 11-contract §1.1 beyond the record to the
// state of t, a tile with an active record, as the caller's view keeps them
// (§1.3): the full view gets them on every deployment, and caps and edges;
// the deployment view of d's own principals only on the primary and d. The
// reader view gets none, so they are never computed for it.
func (a *deploymentsAPI) addFacts(out map[string]any, t tileRef, rows []map[string]any, full bool, d string) {
	rec, tile, r := t.found.Record, t.c.Path, a.reads.factReads
	for _, row := range rows {
		name, _ := row["name"].(string)
		dr := rec.Deployments[name]
		if dr == nil || !full && name != rec.Primary && name != d {
			continue
		}
		a.deploymentFacts(row, t, name, dr)
	}
	if !full {
		return
	}
	if r.caps != nil {
		out["caps"] = r.caps(tile)
	}
	if r.edges != nil {
		out["edges"] = nonNil(r.edges(tile))
	}
}

// deploymentFacts adds Deployment's facts beyond the record to row, the
// deployment name of t (dr, its record entry). deliveries is always on for
// the primary, and alwaysOn is its code's value: neither is a switch there.
func (a *deploymentsAPI) deploymentFacts(row map[string]any, t tileRef, name string, dr *deployments.DeploymentRecord) {
	tile, r := t.c.Path, a.reads.factReads
	primary := name == t.found.Record.Primary
	if r.data != nil {
		if d := r.data(tile, name); d != nil {
			row["data"] = d
		}
	}
	if r.vault != nil {
		if v, err := r.vault(tile, name); err == nil {
			row["vault"] = v
		}
	}
	if r.limits != nil {
		var quota int64
		if r.diskQuota != nil {
			quota = r.diskQuota(tile)
		}
		row["limits"] = limitsView(r.limits(tile, name), quota, dr.Limits)
	}
	row["deliveries"] = primary || dr.DeliveriesOn()
	declared, known := false, false
	if r.declared != nil {
		declared, known = r.declared(t.c, name)
	}
	if known {
		row["alwaysOnDeclared"] = declared
	}
	row["alwaysOn"] = dr.AlwaysOn
	if primary {
		row["alwaysOn"] = t.c.Manifest.AlwaysOn
	}
	if r.backup != nil && !primary {
		if b := r.backup(tile, name); b != nil {
			row["backup"] = b
		}
	}
	if r.registrations != nil {
		row["registrations"] = nonNil(r.registrations(tile, name))
	}
	if r.wouldNotify != nil && !primary {
		row["wouldNotify"] = nonNil(r.wouldNotify(tile, name))
	}
}

// limitsView is Deployment.limits (P22): the effective memory and pids caps
// (0: none), the disk quota — the tile's, lowered by the deployment's
// override — and the limits a tile manager lowered for it.
func limitsView(l cgroup.Limits, quota int64, set map[string]int64) deployments.LimitsView {
	v := deployments.LimitsView{MemMiB: max(l.MemMax, 0) >> 20, Pids: max(l.PidsMax, 0), DiskGiB: max(quota, 0) >> 30}
	if g := set[deployments.LimitDiskGiB]; g > 0 && (v.DiskGiB == 0 || g < v.DiskGiB) {
		v.DiskGiB = g
	}
	for _, k := range []string{deployments.LimitMemMiB, deployments.LimitPids, deployments.LimitDiskGiB} {
		if set[k] > 0 {
			v.Overrides = append(v.Overrides, k)
		}
	}
	return v
}

// nonNil is s, or an empty list for nil: a fact that is a list is [], never
// null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// writeDiff answers a judged diff from the plane (11-contract §1.11): the
// patch as text/x-diff, or with stat the per-file summary as JSON, with the
// resolved checkpoints in X-XBin-Checkpoint-From and -To. Nothing is
// written before the diff ran.
func writeDiff(w http.ResponseWriter, r *http.Request, dp *deployments.Plane, q diffQuery) error {
	spec := func(s diffSide) deployments.DiffSpec {
		return deployments.DiffSpec{WorkTree: s.WorkTree, ID: s.ID, Tree: s.Tree}
	}
	res, err := dp.Diff(r.Context(), q.By, q.Tile, spec(q.From), spec(q.To), q.Path, q.Stat)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("X-XBin-Checkpoint-From", res.From.ID)
	h.Set("X-XBin-Checkpoint-To", res.To.ID)
	if q.Stat {
		files := res.Files
		if files == nil {
			files = []checkpoint.DiffFile{}
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"from": res.From.ID, "to": res.To.ID, "files": files, "truncated": res.Truncated})
		return nil
	}
	h.Set("Content-Type", "text/x-diff; charset=utf-8")
	if res.Truncated {
		h.Set("X-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Patch)
	return nil
}

// ---- GET /deployments/log ----

func (a *deploymentsAPI) getLog(w http.ResponseWriter, r *http.Request) {
	if a.reads.log == nil || a.reads.entry == nil {
		reservedRoute(w, r)
		return
	}
	pr, q := auth.PrincipalOf(r), r.URL.Query()
	t, e := a.resolveQuery(q.Get("tile"))
	dep := q.Get("deployment")
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
	t, e := a.resolveQuery(q.Get("tile"))
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
