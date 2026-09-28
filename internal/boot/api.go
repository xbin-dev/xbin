package boot

import (
	"context"
	"errors"
	"net/http"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/util"
)

// registerRuntimeAPI mounts the endpoints that read across the runtime —
// runner, broker, ingress managers and the process itself — which no single
// package owns: /backends, /runtime, /ingress, /tile-status, /term-net.
func (st *State) registerRuntimeAPI(srv *server.Server) {
	run, brk, userStore := st.Run, st.Broker, st.Users
	rd := runtimeReads{run: run, dp: st.Deployments, isAdmin: brk.IsAdmin, disk: brk.TileDiskStatus,
		alerts: func(tile string) any { return brk.TileAlerts(tile) },
		net:    func(tile string) any { return brk.NetLabel(tile) }}
	srv.RegisterAPI("GET /backends", rd.backends)
	// Full runtime visibility for the admin console: host + per-backend process,
	// namespaces, and egress/network activity (plans/isolation.md).
	srv.RegisterAPI("GET /runtime", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		var ms goruntime.MemStats
		goruntime.ReadMemStats(&ms)
		host := st.isolationInfo() // isolate, rootfs, scopeUids, protections (sandboxes.go)
		for k, v := range map[string]any{
			"version": st.Cfg.Version, "pid": os.Getpid(), "uid": os.Geteuid(),
			"kernel": kernelRelease(), "numCPU": goruntime.NumCPU(),
			"goroutines": goruntime.NumGoroutine(), "heapMB": float64(ms.HeapAlloc) / 1e6,
			"uptimeSec": int64(time.Since(st.Started).Seconds()),
		} {
			host[k] = v
		}
		// Live per-tile stats (cpu/mem/io series) with tile owners attached
		// so the console can group by org.
		stats := run.StatsSnapshot()
		if tiles, ok := stats["tiles"].([]runner.TileStats); ok {
			owners := userStore.Owners()
			for i := range tiles {
				tiles[i].Owner = owners[tiles[i].Path]
			}
		}
		bs := run.Inspect()
		// Annotate the runtime picture with each backend's effective network
		// (D54): the ref, mode, source and inert reason the console shows.
		for i := range bs {
			l := brk.NetLabel(bs[i].Path)
			bs[i].NetRef, bs[i].Net, bs[i].NetSource, bs[i].NetNote = l.Ref, l.Effective, l.Source, l.Note
		}
		out := map[string]any{
			"host": host, "backends": bs, "resources": brk.ResourceUsage(),
			"stats": stats,
		}
		if deps := rd.deploymentBackends(auth.PrincipalOf(r), bs); len(deps) > 0 {
			out["deploymentBackends"] = deps
		}
		server.WriteJSON(w, http.StatusOK, out)
	})
	// Ingress overview (plans/ingress.md): exposes + bindings + routes from
	// the broker, live listener/forward status from the managers. Admin-only —
	// the published surface and its failure modes are operator data.
	srv.RegisterAPI("GET /ingress", func(w http.ResponseWriter, r *http.Request) {
		if !brk.IsAdmin(auth.PrincipalOf(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		out := brk.IngressOverview()
		out["streams"] = st.streams.Status()
		out["forwards"] = st.forwards.Status()
		out["httpListener"] = map[string]any{
			"listen": st.Cfg.IngressListen, "tls": st.Cfg.IngressCert != "" && st.Cfg.IngressKey != "",
		}
		server.WriteJSON(w, http.StatusOK, out)
	})
	// Per-tile runtime status — readable from a tile terminal (self) or by an
	// admin for any tile. Read-only. Runtime metrics we already collect, scoped
	// to one component: backend process/cgroup/egress + disk usage/quota + its
	// alerts. `bx status` renders it.
	srv.RegisterAPI("GET /tile-status", rd.tileStatus)
	// GET /term-net?tile= — the scopes a terminal on this tile may take for
	// the caller, so the picker offers only what the server will honour (D54).
	srv.RegisterAPI("GET /term-net", func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		tile := strings.Trim(r.URL.Query().Get("tile"), "/")
		if tile == "" || !p.CanTerminalTile(tile) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no terminal access to this tile"})
			return
		}
		g := brk.TermNetFor(p, tile)
		scopes, def := term.ScopesFor(p, g)
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"tile": tile, "scopes": scopes, "default": def, "label": g.OrgLabel,
			"org": g.OrgOK && g.OwnerScope != term.NetPersonal, "personal": g.OrgOK && g.OwnerScope == term.NetPersonal, // D88
		})
	})
}

// ---- the runtime reads of tiles with deployments (11-contract §8) ----

// runtimeRunner is what the runtime reads ask of the runner (*runner.Runner):
// the rows of each tile's primary, and those of every deployment beyond its
// tile's primary (none while no tile runs one).
type runtimeRunner interface {
	Status() map[string]any
	StatusDeployments() map[string]map[string]any
	Inspect() []runner.Backend
	InspectDeployments() []runner.Backend
}

// runtimeReads are the sources of /backends and /tile-status, and of the
// deployment rows /runtime adds: the runner's rows, the deployments plane
// (nil: every tile is in the zero state) and the broker's answers.
//
// Lists keyed by tile keep one entry per tile, the primary's, because old
// shells and consoles count them or match them by path (12-compat C3); the
// rows of other deployments go in new fields, only to the principals who may
// learn of those deployments (06-security C9). A tile without a deployment
// record answers exactly as before tile deployments (D119c).
type runtimeReads struct {
	run     runtimeRunner
	dp      *deployments.Plane
	isAdmin func(auth.Principal) bool
	disk    func(tile string) (usage, quota int64, blocked bool)
	alerts  func(tile string) any
	net     func(tile string) any
}

// record reports whether a deployment record governs tile.
func (rd runtimeReads) record(tile string) bool { return rd.dp != nil && rd.dp.HasRecord(tile) }

func (rd runtimeReads) primary(tile string) string {
	if rd.dp == nil {
		return util.MainDeployment
	}
	return rd.dp.Primary(tile)
}

func (rd runtimeReads) audience(p auth.Principal, tile string) deployments.Audience {
	if rd.dp == nil {
		return deployments.AudienceNone
	}
	return rd.dp.Audience(p, deployments.Subject{Tile: tile, Primary: rd.primary(tile), Record: rd.record(tile)})
}

// bound is the deployment of tile that p, one of tile's own credentials, is
// bound to (11-contract §7.1): util.ErrNoDeployment for one that no longer
// exists; another error for a session that follows a protected primary,
// which is bound to none.
func (rd runtimeReads) bound(p auth.Principal, tile string) (string, error) {
	if rd.dp == nil {
		return util.MainDeployment, nil
	}
	return rd.dp.Addressed(p, tile)
}

// sees reports whether p may learn of deployment dep of tile (06-security
// C9; 11-contract §0.5). The primary is a fact of the tile's readers: the
// route's own gate decides who reads it. Any other deployment is the write
// audience's — admins and people with write, in their own session, and the
// tile's terminal and agent sessions while their user writes it — and its
// own principals'. The primary's frame and instance principals, minted for
// readers, and other tiles' credentials (an xbin-admin tile's included)
// learn nothing of it.
func (rd runtimeReads) sees(p auth.Principal, tile, dep string) bool {
	if dep == rd.primary(tile) {
		return true
	}
	switch rd.audience(p, tile) {
	case deployments.AudienceWrite:
		return true
	case deployments.AudienceDeployment:
		b, err := rd.bound(p, tile)
		return err == nil && b == dep
	}
	return false
}

// GET /backends: the primary's {state, gen, error?} per tile, admins only.
func (rd runtimeReads) backends(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !rd.isAdmin(p) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	out := rd.run.Status()
	if rd.dp != nil {
		rd.nestDeployments(p, out)
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// nestDeployments gives the row of each tile with a record deployment, its
// primary's name, and for a caller in its write audience deployments: the
// runner's rows of its other deployments, by name (sees). A tile whose
// primary isn't running while another deployment is gets the primary's idle
// row to hold them. The row stays the primary's and one per tile: the old
// shell's footer counts them.
func (rd runtimeReads) nestDeployments(p auth.Principal, out map[string]any) {
	others := rd.run.StatusDeployments()
	tiles := map[string]bool{}
	for tile := range out {
		tiles[tile] = true
	}
	for tile := range others {
		tiles[tile] = true
	}
	for tile := range tiles {
		if !rd.record(tile) {
			continue
		}
		nested := map[string]any{}
		for name, row := range others[tile] {
			if rd.sees(p, tile, name) {
				nested[name] = row
			}
		}
		row, running := out[tile].(map[string]any)
		if !running {
			if len(nested) == 0 {
				continue
			}
			row = map[string]any{"state": "idle", "gen": 0}
		}
		row["deployment"] = rd.primary(tile)
		if len(nested) > 0 || rd.audience(p, tile) == deployments.AudienceWrite {
			row["deployments"] = nested
		}
		out[tile] = row
	}
}

// deploymentBackends is /runtime's deployment part: each row of bs (the
// primaries') of a tile with a record gains deployment, the primary's name;
// the runner's rows of the deployments beyond the primaries that p may see
// come back for deploymentBackends[], never as a second row in backends[],
// which the admin console maps by path.
func (rd runtimeReads) deploymentBackends(p auth.Principal, bs []runner.Backend) []runner.Backend {
	if rd.dp == nil {
		return nil
	}
	for i := range bs {
		if rd.record(bs[i].Path) {
			bs[i].Deployment = rd.primary(bs[i].Path)
		}
	}
	var out []runner.Backend
	for _, b := range rd.run.InspectDeployments() {
		if rd.record(b.Path) && rd.sees(p, b.Path, b.Deployment) {
			out = append(out, b)
		}
	}
	return out
}

// GET /tile-status: one tile's runtime metrics, for admins and the tile's own
// credentials. ?deployment= picks the deployment reported (statusTarget); on
// a tile with a record, or when one was named (the echo), the answer says
// which, and the write audience also gets the deployments summary.
func (rd runtimeReads) tileStatus(w http.ResponseWriter, r *http.Request) {
	p, q := auth.PrincipalOf(r), r.URL.Query()
	comp := strings.Trim(q.Get("component"), "/")
	if rd.dp != nil && util.QueryTileQualified(comp, func(c string) bool { _, ok := rd.dp.Reg.Component(c); return ok }) {
		writeDeployError(w, &dpe{Status: http.StatusBadRequest, Msg: util.QueryRefMsg}) // the deployment rides deployment= (D127j)
		return
	}
	if comp == "" {
		comp = p.Component // default: the caller's own tile (terminal / element principal)
	}
	if comp == "" {
		http.Error(w, "specify ?component= (or call from a tile terminal)", http.StatusBadRequest)
		return
	}
	if !rd.isAdmin(p) && p.Component != comp {
		http.Error(w, "you can only read your own tile's status", http.StatusForbidden)
		return
	}
	named := q.Get("deployment")
	dep, e := rd.statusTarget(p, comp, named)
	if e != nil {
		writeDeployError(w, e)
		return
	}
	usage, quota, blocked := rd.disk(comp)
	out := map[string]any{
		"component": comp,
		"backend":   rd.backendOf(comp, dep), // nil when no backend is running
		"disk":      map[string]any{"usageBytes": usage, "quotaBytes": quota, "blocked": blocked},
		"alerts":    rd.alerts(comp),
		"net":       rd.net(comp), // the effective network + why (D54)
	}
	record := rd.record(comp)
	if record || named != "" {
		out["deployment"] = dep
	}
	if record && rd.audience(p, comp) == deployments.AudienceWrite {
		out["deployments"] = rd.statusSummary(r.Context(), comp)
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// statusTarget is the deployment /tile-status reports on tile (11-contract
// §0.4, §8): the one named, else DR1's and DR2's default — the bound
// deployment of the tile's own credential, the primary for anyone else. The
// tile's frame and instance credentials read their bound deployment only;
// its terminal and agent sessions name any while their user writes the tile,
// as people may; an admin names any; another tile's credential only the
// primary. Naming a deployment the caller may not learn of is refused the
// same whether or not it exists.
func (rd runtimeReads) statusTarget(p auth.Principal, tile, named string) (string, *dpe) {
	if named != "" && !util.DeploymentNameOK(named) {
		return "", &dpe{Status: http.StatusBadRequest, Msg: badDeploymentName}
	}
	if p.Component == tile && (p.Via != "terminal" || named == "") {
		b, err := rd.bound(p, tile)
		switch {
		case errors.Is(err, util.ErrNoDeployment):
			return "", &dpe{Status: http.StatusNotFound, Msg: err.Error()}
		case err != nil:
			return "", &dpe{Status: http.StatusForbidden, Kind: deployments.KindAuthority, Msg: err.Error()}
		case named != "" && named != b:
			return "", &dpe{Status: http.StatusForbidden, Kind: deployments.KindAuthority,
				Msg: "a tile's own credentials act only on their own deployment (" + b + ")"}
		case !rd.sees(p, tile, b): // bound beyond the primary, driven by a user who no longer writes the tile
			return "", &dpe{Status: http.StatusForbidden, Kind: deployments.KindAuthority, Msg: "deployments of " + tile + " need write access"}
		}
		return b, nil
	}
	switch {
	case named == "":
		return rd.primary(tile), nil
	case !rd.sees(p, tile, named):
		return "", &dpe{Status: http.StatusForbidden, Kind: deployments.KindAuthority, Msg: "deployments of " + tile + " need write access"}
	case rd.dp != nil && !rd.dp.HasDeployment(tile, named), rd.dp == nil && named != util.MainDeployment:
		return "", noDeployment(tile, named)
	}
	return named, nil
}

// backendOf is the runner's row of deployment dep of tile: the primary's
// from Inspect, as before tile deployments; another's from
// InspectDeployments. nil while it isn't running.
func (rd runtimeReads) backendOf(tile, dep string) *runner.Backend {
	rows := rd.run.Inspect
	if dep != rd.primary(tile) {
		rows = rd.run.InspectDeployments
	}
	for _, b := range rows() {
		if b.Path == tile && (b.Deployment == "" || b.Deployment == dep) {
			return &b
		}
	}
	return nil
}

// statusSummary is /tile-status' deployments object for the write audience:
// the primary, live reload's target ("" while paused), and each deployment,
// main first, with its state and generation as the runner sees them and the
// checkpoint it is pinned to.
func (rd runtimeReads) statusSummary(ctx context.Context, tile string) map[string]any {
	primary, names := rd.dp.DeploymentsOf(tile)
	live, _ := rd.dp.LiveReload(tile)
	status, others := rd.run.Status(), rd.run.StatusDeployments()
	static := false
	if rd.dp.Reg != nil {
		if c, ok := rd.dp.Reg.Component(tile); ok {
			static = !c.HasBackend()
		}
	}
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		row, _ := others[tile][name].(map[string]any)
		if name == primary {
			row, _ = status[tile].(map[string]any)
		}
		it := map[string]any{"name": name, "state": "idle", "gen": 0}
		if static {
			it["state"] = "static"
		}
		if row != nil {
			it["state"], it["gen"] = row["state"], row["gen"]
		}
		if code, err := rd.dp.CodeFor(tile, name); err == nil && !code.WorkTree {
			it["checkpoint"] = rd.checkpointID(ctx, tile, code.Tree)
		}
		items = append(items, it)
	}
	return map[string]any{"primary": primary, "liveReload": live, "items": items}
}

// checkpointID is tile's checkpoint tree by its id (the store's shortest
// unique prefix); its first seven digits when the store can't say.
func (rd runtimeReads) checkpointID(ctx context.Context, tile, tree string) string {
	if cp, err := rd.dp.Checkpoint(ctx, tile, tree); err == nil && cp.ID != "" {
		return cp.ID
	}
	return "c:" + tree[:min(7, len(tree))]
}
