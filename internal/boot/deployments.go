package boot

// deployments.go — the tile-deployments API: pausing live reload, tile
// deployments and promotion (docs/protocol.md, "Tile deployments"). Every
// route of that contract is mounted here, once, as a RegisterAPI literal, so
// feature work never adds a route: TestRouteInventory ties each one to its
// openapi.go and protocol.md rows.
//
// The handlers are thin. A POST decodes its body into the request type its
// op registers in internal/deployments, whose registry judges and runs it
// (NP-14-3); an op this xbind doesn't build answers 501, as a reserved route
// (NP-14-4). The reads judge the caller with the plane's one authorize
// function over the record its index holds; what they need beyond the
// record comes through deployReads, and a read without its source answers
// 501 too. Nothing here writes: reading a zero-state tile creates nothing (P5).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

type (
	op  = deployments.Op
	dpe = deployments.Error
)

// registerDeploymentsAPI mounts the /deployments family and the checkpoint
// remote over dp, the deployments plane. facts, the broker in xbind, answers
// the state's facts the plane doesn't hold (brokerFacts); without it the
// state leaves them out.
func registerDeploymentsAPI(srv *server.Server, dp *deployments.Plane, facts ...brokerFacts) {
	owner := func(tile string) string {
		if srv.Pol != nil { // the broker's, installed at boot: read per request
			return srv.Pol.OwnerOf(tile)
		}
		return ""
	}
	reads := planeReads(dp)
	for _, f := range facts {
		reads.from(f)
	}
	mountDeploymentsAPI(srv, &deploymentsAPI{dp: dp, owner: owner, origin: srv.DeploymentOrigin,
		ops:   opRegistry{deployments.Registered, deployments.NewRequest, dp.Do},
		reads: reads})
}

// apiMounter is where the routes go: the server, or a test's mux.
type apiMounter interface {
	RegisterAPI(string, http.HandlerFunc)
}

// mountDeploymentsAPI registers a's handlers on m.
func mountDeploymentsAPI(m apiMounter, a *deploymentsAPI) {
	// Reading: the state (the caller's view), the deploy log, the diff.
	m.RegisterAPI("GET /deployments", a.getState)
	m.RegisterAPI("GET /deployments/log", a.getLog)
	m.RegisterAPI("GET /deployments/diff", a.getDiff)
	// Live reload.
	m.RegisterAPI("POST /deployments/live-reload/pause", a.post(deployments.OpPause))
	m.RegisterAPI("POST /deployments/live-reload/resume", a.post(deployments.OpResume))
	m.RegisterAPI("POST /deployments/live-reload/now", a.post(deployments.OpReloadNow))
	m.RegisterAPI("POST /deployments/live-reload/attach", a.post(deployments.OpAttach))
	// Adding and removing deployments; moving code.
	m.RegisterAPI("POST /deployments/add", a.post(deployments.OpAdd))
	m.RegisterAPI("POST /deployments/remove", a.post(deployments.OpRemove))
	m.RegisterAPI("POST /deployments/deploy", a.post(deployments.OpDeploy)) // restart rides deploy
	m.RegisterAPI("POST /deployments/promote", a.post(deployments.OpPromote))
	m.RegisterAPI("POST /deployments/rollback", a.post(deployments.OpRollback))
	// Governance: tile managers, in a person's own session.
	m.RegisterAPI("POST /deployments/primary", a.post(deployments.OpPrimary))
	m.RegisterAPI("POST /deployments/protect", a.post(deployments.OpProtect))
	m.RegisterAPI("POST /deployments/edge", a.post(deployments.OpEdge))
	m.RegisterAPI("POST /deployments/deliveries", a.post(deployments.OpDeliveries))
	m.RegisterAPI("POST /deployments/always-on", a.post(deployments.OpAlwaysOn))
	m.RegisterAPI("POST /deployments/limits", a.post(deployments.OpLimits))
	// Data: seed, reset, vault copy; per-deployment backups beside today's,
	// whose bodies stay as they are.
	m.RegisterAPI("POST /deployments/seed", a.post(deployments.OpSeed))
	m.RegisterAPI("POST /deployments/reset", a.post(deployments.OpReset))
	m.RegisterAPI("POST /deployments/vault-copy", a.post(deployments.OpVaultCopy))
	m.RegisterAPI("POST /deployments/backup", a.post(deployments.OpBackup))
	m.RegisterAPI("GET /deployments/backups", a.getBackups) // deploybackups.go
	m.RegisterAPI("POST /deployments/restore", a.post(deployments.OpRestore))
	m.RegisterAPI("POST /deployments/backup-schedule", a.post(deployments.OpBackupSchedule))
	// Run a deployment's cron job now.
	m.RegisterAPI("POST /deployments/run-now", a.post(deployments.OpRunNow))
	// The checkpoint remote: read-only dumb HTTP git over the tile's view
	// repository (<tile>.git/<git path>).
	m.RegisterAPI("GET /checkpoints/{rest...}", a.getFetch)
}

// reservedRoute answers a route this xbind doesn't build yet: 501 in the
// shape of every API error, unlike an older xbind's plain-text 404 or 405.
func reservedRoute(w http.ResponseWriter, r *http.Request) {
	server.WriteError(w, http.StatusNotImplemented,
		"reserved for tile deployments, not built in this xbind yet — "+r.Pattern, "/docs/protocol.md")
}

// deploymentsAPI is what the handlers ask of xbind.
type deploymentsAPI struct {
	dp     *deployments.Plane
	owner  func(tile string) string      // the tile's owner ref, as /components reports it
	origin func(tile, dep string) string // a deployment's own origin in origins mode; "" otherwise
	ops    opRegistry
	reads  deployReads
}

// opRegistry is the plane's operation registry; a test substitutes its own.
type opRegistry struct {
	registered func(op) bool
	newRequest func(op) (any, bool)
	do         func(context.Context, auth.Principal, op, any) (any, error)
}

// deployReads are the reads' sources beyond the record. A nil one is a
// source this xbind doesn't have: the state leaves out what it would say;
// the log, the diff and the checkpoint remote answer 501.
type deployReads struct {
	status     func(c *registry.Component, dep string) depStatus                           // the runner's view of dep
	checkpoint func(ctx context.Context, tile, tree string) (checkpoint.Checkpoint, error) // by full tree id
	queue      func(tile, dep string) (deploying any, queued []any)                        // DeployEntry values
	lastDeploy func(ctx context.Context, tile, dep string) (any, error)                    // nil: none yet
	workTree   func(tile string) (changed int, ok bool)                                    // moved since the paused target's checkpoint
	// log lists DeployEntry values newest first: dep's ("" = all), at most
	// limit, ids below before (0 = no bound). entry is one attempt, held up
	// to wait until it finishes; nil for an unknown id.
	log   func(ctx context.Context, tile, dep string, limit int, before int64) (entries []any, more bool, err error)
	entry func(ctx context.Context, tile string, id int64, wait time.Duration) (any, error)
	// diff answers a judged request (patch or stat, X-XBin-Checkpoint-*),
	// writing nothing before it can't fail; fetch serves one file of the
	// view repository, 404 outside the dumb-HTTP allow-list.
	diff  func(w http.ResponseWriter, r *http.Request, q diffQuery) error
	fetch func(w http.ResponseWriter, r *http.Request, tile, gitPath string)

	factReads // the state's facts beyond the record (deployreads.go)
}

// depStatus is a deployment's generation as the runner sees it.
type depStatus struct {
	State string // idle | building | healthy | failed | crash-looping | static
	Gen   int
	Error string
}

// beyondPrimaries is the runner's status of every deployment that isn't its
// tile's primary: tile → deployment → {state, gen, error?}.
type beyondPrimaries interface {
	StatusDeployments() map[string]map[string]any
}

// runnerStatus reads the runner xbind hands the plane: the primary's
// generation from its rows keyed by tile path, any other deployment's from
// its rows beyond the primaries; a deployment it has no row for, or can't
// say about, is idle, and a tile whose code has no backend is static.
func runnerStatus(dp *deployments.Plane) func(*registry.Component, string) depStatus {
	return func(c *registry.Component, dep string) depStatus {
		if !c.HasBackend() {
			return depStatus{State: "static"}
		}
		st, primary := depStatus{State: "idle"}, dep == dp.Primary(c.Path)
		var e map[string]any
		if src, ok := dp.Run.(interface{ Status() map[string]any }); ok && primary {
			e, _ = src.Status()[c.Path].(map[string]any)
		}
		if src, ok := dp.Run.(beyondPrimaries); ok && !primary {
			e, _ = src.StatusDeployments()[c.Path][dep].(map[string]any)
		}
		if e != nil {
			st.Gen, _ = e["gen"].(int)
			st.Error, _ = e["error"].(string)
			if s, _ := e["state"].(string); s != "" {
				st.State = s
			}
		}
		return st
	}
}

// ---- resolving a tile ref and judging a read ----

// tileRef is a resolved tile ref (11-contract §0.3): the tile, what its
// record means for it, and the deployment the ref names.
type tileRef struct {
	c         *registry.Component
	found     deployments.Found
	dep       string // the qualifier, else the primary
	qualified bool
}

// resolve maps a tile ref to its tile (11-contract §2.2): a component at the
// whole ref wins, as does anything on disk there; only then does
// "<tile>+<name>" name a deployment, of a tile with a record (P5).
func (a *deploymentsAPI) resolve(ref string) (tileRef, *dpe) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return tileRef{}, &dpe{Status: http.StatusBadRequest, Msg: "need ?tile= (a tile ref: apps/crm, or apps/crm+dev)"}
	}
	if !cleanRel(ref) {
		return tileRef{}, noTile(ref)
	}
	if c, ok := a.dp.Reg.Component(ref); ok {
		return a.ref(c, "", false), nil
	}
	if j := strings.LastIndexByte(ref, '+'); j > 0 && ref[j-1] != '/' && !strings.Contains(ref[j:], "/") {
		_, onDisk := os.Lstat(filepath.Join(a.dp.Reg.Root, filepath.FromSlash(ref))) // follows nothing
		c, ok := a.dp.Reg.Component(ref[:j])
		if ok && util.DeploymentNameOK(ref[j+1:]) && a.dp.HasRecord(c.Path) && onDisk != nil {
			return a.ref(c, ref[j+1:], true), nil
		}
	}
	return tileRef{}, noTile(ref)
}

func (a *deploymentsAPI) ref(c *registry.Component, dep string, qualified bool) tileRef {
	t := tileRef{c: c, found: a.dp.Lookup(c.Path), dep: dep, qualified: qualified}
	if !qualified {
		t.dep = t.primary()
	}
	return t
}

func (t tileRef) primary() string {
	if r := t.found.Record; r != nil && r.Primary != "" {
		return r.Primary
	}
	return util.MainDeployment
}

func (t tileRef) active() bool { return t.found.State == deployments.RecordActive }

// subject is what an act on t is judged against, as the record says now.
func (t tileRef) subject(dep string) deployments.Subject {
	s := deployments.Subject{Tile: t.c.Path, Deployment: dep, Record: t.active()}
	if r := t.found.Record; r != nil {
		s.Primary, s.Protected, s.Seq = r.Primary, r.ProtectedPrimary, uint64(max(r.Seq, 0))
	}
	return s
}

// reaches reports whether pr is in the audience of t's deployment dep: the
// primary's is the tile's; another's is the write audience and its own
// principals (11-contract §0.5).
func (a *deploymentsAPI) reaches(pr auth.Principal, t tileRef, dep string) bool {
	au := a.dp.Audience(pr, t.subject(""))
	return dep == t.primary() || au == deployments.AudienceWrite || (au == deployments.AudienceDeployment && bound(pr) == dep)
}

// readGate judges a read of t: pr's authority for o; for a qualified ref
// naming a non-primary deployment, its audience — the same 403 for anyone
// else whether or not it exists (11-contract §1.3); a record holding the tile.
func (a *deploymentsAPI) readGate(pr auth.Principal, t tileRef, o op) *dpe {
	switch _, err := a.dp.Authorize(pr, o, t.subject("")); {
	case err != nil:
		return asDeployError(err)
	case t.qualified && !a.reaches(pr, t, t.dep):
		return &dpe{Status: http.StatusForbidden, Kind: deployments.KindAuthority, Msg: "deployment URLs need write access on " + t.c.Path}
	case t.qualified && !a.dp.HasDeployment(t.c.Path, t.dep):
		return noDeployment(t.c.Path, t.dep)
	case t.found.State == deployments.RecordHeld:
		return &dpe{Status: http.StatusConflict, Kind: deployments.KindState, Msg: t.found.Err.Error()}
	}
	return nil
}

// bound is the deployment a tile credential is bound to (11-contract §7.1):
// a frame or instance token's, main without a claim; a terminal or agent
// token's target, "" while it follows the primary.
func bound(pr auth.Principal) string {
	if pr.Deployment == "" && pr.Via != "terminal" {
		return util.MainDeployment
	}
	return pr.Deployment
}

// ---- GET /deployments ----

func (a *deploymentsAPI) getState(w http.ResponseWriter, r *http.Request) {
	pr := auth.PrincipalOf(r)
	t, e := a.resolve(r.URL.Query().Get("tile"))
	if e == nil {
		e = a.readGate(pr, t, deployments.OpState)
	}
	if e != nil {
		writeDeployError(w, e)
		return
	}
	server.WriteJSON(w, http.StatusOK, a.state(r.Context(), pr, t))
}

// state is the State of 11-contract §1.1 in pr's view (§1.3): full for the
// write audience, deployment for a non-primary deployment's own principals,
// reader for the rest — depending only on the audience, never on what a
// reader can't see. The zero state (synthesized, nothing written) names
// only main, so it is the same for every view.
func (a *deploymentsAPI) state(ctx context.Context, pr auth.Principal, t tileRef) map[string]any {
	rec, tile, s := t.found.Record, t.c.Path, t.subject("")
	au := a.dp.Audience(pr, s)
	view := map[deployments.Audience]string{deployments.AudienceWrite: "full",
		deployments.AudienceDeployment: "deployment", deployments.AudienceReader: "reader"}[au]
	out := map[string]any{
		"tile": tile, "record": t.active(), "schema": rec.Schema, "seq": rec.Seq,
		"features": a.features(), "owner": a.owner(tile), "view": view,
		"primary": rec.Primary, "liveReload": rec.LiveReload, "lastLiveReload": rec.LastLiveReload,
		"protectedPrimary": rec.ProtectedPrimary, "caller": a.caller(pr, t, s),
		"allowed": map[string]deployments.Can{
			"pause": a.allowed(deployments.OpPause, s), "deployments": a.allowed(deployments.OpAdd, s)},
	}
	if t.qualified {
		out["selected"] = t.dep
	}
	if rec.LiveReloadSince != nil {
		out["liveReloadSince"] = rec.LiveReloadSince
	}
	rows := []map[string]any{}
	for _, name := range deploymentOrder(rec) {
		rows = append(rows, a.row(ctx, pr, t, name))
	}
	out["deployments"] = rows
	if t.active() && rec.LiveReload == "" { // paused: how far the work tree moved since
		wt := map[string]any{}
		for _, row := range rows {
			if cp, ok := row["checkpoint"].(map[string]any); ok && row["name"] == rec.LastLiveReload {
				wt["since"] = cp["id"]
			}
		}
		if a.reads.workTree != nil {
			if n, ok := a.reads.workTree(tile); ok {
				wt["changed"] = n
			}
		}
		out["workTree"] = wt
	}
	if t.active() && (au == deployments.AudienceWrite || au == deployments.AudienceDeployment) {
		a.addFacts(out, t, rows, au == deployments.AudienceWrite, bound(pr))
	}
	switch {
	case !t.active():
		return out
	case au == deployments.AudienceReader:
		return readerView(out, rec)
	case au == deployments.AudienceDeployment:
		return deploymentView(out, rec, bound(pr))
	}
	return out
}

// deploymentOrder is main first, then by creation.
func deploymentOrder(rec *deployments.Record) []string {
	names := make([]string, 0, len(rec.Deployments))
	for n := range rec.Deployments {
		names = append(names, n)
	}
	slices.SortFunc(names, func(x, y string) int {
		if x == util.MainDeployment || y == util.MainDeployment {
			return map[bool]int{true: -1, false: 1}[x == util.MainDeployment]
		}
		if c := strings.Compare(rec.Deployments[x].Created, rec.Deployments[y].Created); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	})
	return names
}

// row is one Deployment of 11-contract §1.1, in the full view.
func (a *deploymentsAPI) row(ctx context.Context, pr auth.Principal, t tileRef, name string) map[string]any {
	rec, tile := t.found.Record, t.c.Path
	d, primary := rec.Deployments[name], name == rec.Primary
	row := map[string]any{"name": name, "primary": primary, "liveReload": d.Checkpoint == nil, "checkpoint": nil}
	status := map[string]any{"serving": "work-tree"}
	if d.Checkpoint != nil {
		cp := a.checkpointOf(ctx, tile, *d.Checkpoint)
		row["checkpoint"], status["serving"] = cp, cp["id"]
	}
	st := depStatus{State: "idle"}
	if a.reads.status != nil {
		st = a.reads.status(t.c, name)
	}
	status["state"], status["gen"] = st.State, st.Gen
	if st.Error != "" { // at most 500 bytes: the full text is the deployment's log
		status["error"] = strings.ToValidUTF8(st.Error[:min(len(st.Error), 500)], "")
	}
	if a.reads.queue != nil {
		if deploying, queued := a.reads.queue(tile, name); deploying != nil || len(queued) > 0 {
			if deploying != nil {
				status["deploying"] = asMap(deploying)
			}
			if len(queued) > 0 {
				status["queued"] = asMaps(queued)
			}
		}
	}
	row["status"] = status
	ref := tile
	if !primary {
		ref += "+" + name
	}
	row["url"] = "/c/" + ref + "/"
	if t.c.HasBackend() {
		row["api"] = "/api/" + ref + "/"
	}
	if a.origin != nil { // origins mode: each deployment's own origin (11-contract §2.6)
		if o := a.origin(tile, name); o != "" {
			row["origin"] = o
		}
	}
	if a.reads.lastDeploy != nil {
		if e, err := a.reads.lastDeploy(ctx, tile, name); err == nil && e != nil {
			row["lastDeploy"] = asMap(e)
		}
	}
	if d.Created != "" {
		row["created"], row["by"] = d.Created, d.By
	}
	can := map[string]deployments.Can{"open": {OK: true}}
	if !a.reaches(pr, t, name) {
		can["open"] = deployments.Can{Why: "deployment URLs need write access on " + tile, Kind: deployments.KindAuthority}
	}
	for _, c := range deploymentCans {
		can[c.key] = a.can(pr, c.op, t.subject(name))
	}
	row["can"] = can
	return row
}

// deploymentCans are Deployment.can's acts, judged on the row's deployment.
var deploymentCans = []struct {
	key string
	op  op
}{
	{"deploy", deployments.OpDeploy}, {"restart", deployments.OpRestart}, {"promoteTo", deployments.OpPromote},
	{"rollback", deployments.OpRollback}, {"attach", deployments.OpAttach}, {"remove", deployments.OpRemove},
	{"reset", deployments.OpReset}, {"seed", deployments.OpSeed}, {"vaultCopy", deployments.OpVaultCopy},
	{"deliveries", deployments.OpDeliveries}, {"alwaysOn", deployments.OpAlwaysOn}, {"limits", deployments.OpLimits},
	{"backup", deployments.OpBackup}, {"runNow", deployments.OpRunNow}, {"primary", deployments.OpPrimary},
}

// caller is the Caller of 11-contract §1.1: pr's level and tile-level acts.
func (a *deploymentsAPI) caller(pr auth.Principal, t tileRef, s deployments.Subject) map[string]any {
	tile, rec := t.c.Path, t.found.Record
	level := "read"
	switch {
	case pr.Component == tile && pr.Via != "terminal":
		level = "tile" // a frame or instance token: no operation rights
	case pr.Component == "" && (a.dp.IsAdmin != nil && a.dp.IsAdmin(pr) || a.dp.IsAdmin == nil && pr.IsAdmin()):
		level = "admin"
	case pr.CanTerminalTileVia(tile):
		level = "terminal"
	case a.dp.Audience(pr, s) == deployments.AudienceWrite:
		level = "write"
	}
	c := map[string]any{"level": level, "manager": a.dp.Manager(pr, tile),
		"humanSession": pr.Component == "", "readOnly": pr.ReadOnly()}
	if pr.Component == tile {
		c["bound"] = bound(pr)
	}
	protect := deployments.OpProtect
	if rec.ProtectedPrimary {
		protect = deployments.OpUnprotect
	}
	on := func(dep string) deployments.Subject { s.Deployment = dep; return s }
	c["can"] = map[string]deployments.Can{
		"pause":     a.can(pr, deployments.OpPause, on(rec.LiveReload)),
		"resume":    a.can(pr, deployments.OpResume, on(rec.LastLiveReload)),
		"reloadNow": a.can(pr, deployments.OpReloadNow, on(rec.LastLiveReload)),
		"add":       a.can(pr, deployments.OpAdd, on("")),
		"edges":     a.can(pr, deployments.OpEdge, on("")),
		"protect":   a.can(pr, protect, on("")),
	}
	return c
}

// notBuilt is an act whose route answers 501 here: this xbind can't.
var notBuilt = deployments.Can{Why: "not built in this xbind yet", Kind: deployments.KindPolicy}

// can is the plane's answer for pr, then what the tile itself may do
// (Policy: P18's isolation refusal among them, as the request would be
// judged); an act it allows that this xbind doesn't build is notBuilt.
// allowed is the tile's, whoever asks.
func (a *deploymentsAPI) can(pr auth.Principal, o op, s deployments.Subject) deployments.Can {
	c := a.dp.Can(pr, o, s)
	if c.OK {
		c = a.dp.Policy(o, s)
	}
	return a.orNotBuilt(o, c)
}

func (a *deploymentsAPI) allowed(o op, s deployments.Subject) deployments.Can {
	return a.orNotBuilt(o, a.dp.Policy(o, s))
}

func (a *deploymentsAPI) orNotBuilt(o op, c deployments.Can) deployments.Can {
	switch o { // a refinement rides its route's op
	case deployments.OpRestart:
		o = deployments.OpDeploy
	case deployments.OpUnprotect:
		o = deployments.OpProtect
	}
	if c.OK && !a.ops.registered(o) {
		return notBuilt
	}
	return c
}

// features is what this xbind speaks (NP-14-4): live-reload/1 once pausing
// is built, deployments/1 once adding is; none while the ship-dark switch
// is off (NP-14-5).
func (a *deploymentsAPI) features() []string {
	out := []string{}
	if !a.dp.OptInClosed && a.ops.registered(deployments.OpPause) {
		out = append(out, "live-reload/1")
	}
	if !a.dp.OptInClosed && a.ops.registered(deployments.OpAdd) {
		out = append(out, "deployments/1")
	}
	return out
}

// checkpointOf is the Checkpoint of tile's tree; without the store's
// answer, the id and the hash alone.
func (a *deploymentsAPI) checkpointOf(ctx context.Context, tile, tree string) map[string]any {
	if a.reads.checkpoint != nil {
		if cp, err := a.reads.checkpoint(ctx, tile, tree); err == nil {
			m := map[string]any{"id": cp.ID, "hash": cp.Hash, "feed": cp.Feed, "by": cp.By}
			if !cp.At.IsZero() {
				m["at"] = cp.At.UTC().Format(time.RFC3339)
			}
			return m
		}
	}
	return map[string]any{"id": "c:" + tree[:min(7, len(tree))], "hash": tree}
}

// readerView is 11-contract §1.3's reader view: the primary's facts only —
// no other deployment's name, nothing that counts them.
func readerView(full map[string]any, rec *deployments.Record) map[string]any {
	out := pick(full, "tile", "record", "schema", "features", "owner", "primary", "protectedPrimary")
	out["view"], out["liveReload"], out["deployments"] = "reader", "", []map[string]any{}
	if rec.LiveReload == rec.Primary {
		out["liveReload"] = rec.Primary
	}
	if full["selected"] == rec.Primary {
		out["selected"] = rec.Primary // the alias
	}
	for _, row := range full["deployments"].([]map[string]any) {
		if row["name"] != rec.Primary {
			continue
		}
		p := pick(row, "name", "primary", "liveReload", "checkpoint", "url", "api", "origin")
		st := row["status"].(map[string]any)
		p["status"] = pick(st, "state", "gen", "serving")
		if d, ok := st["deploying"].(map[string]any); ok {
			p["status"].(map[string]any)["deploying"] = pick(d, "result", "phase")
		}
		if l, ok := row["lastDeploy"].(map[string]any); ok {
			p["lastDeploy"] = pick(l, "at", "by", "result")
		}
		out["deployments"] = []map[string]any{p}
	}
	caller := pick(full["caller"].(map[string]any), "level", "manager", "humanSession", "readOnly", "bound")
	can := map[string]deployments.Can{}
	for k := range full["caller"].(map[string]any)["can"].(map[string]deployments.Can) {
		can[k] = deployments.Can{Why: "deployments of " + rec.Tile + " need write access", Kind: deployments.KindAuthority}
	}
	caller["can"], out["caller"] = can, caller
	return out
}

// deploymentView is 11-contract §1.3's deployment view for d's own
// principals: the full view restricted to the primary and d.
func deploymentView(full map[string]any, rec *deployments.Record, d string) map[string]any {
	out := pick(full, "tile", "selected", "record", "schema", "seq", "features", "owner", "primary",
		"liveReloadSince", "protectedPrimary", "caller")
	out["view"] = "deployment"
	for _, k := range []string{"liveReload", "lastLiveReload"} {
		if out[k] = ""; full[k] == rec.Primary || full[k] == d {
			out[k] = full[k]
		}
	}
	rows := []map[string]any{}
	for _, row := range full["deployments"].([]map[string]any) {
		if row["name"] == rec.Primary || row["name"] == d {
			rows = append(rows, row)
		}
	}
	out["deployments"] = rows
	return out
}

// ---- POST /deployments/<op> ----

// post is the generic operation handler (NP-14-3): the body, decoded
// strictly into o's request type, goes to the registry, which judges the
// caller and runs it. The answer is the operation's, with the caller's
// state after it (11-contract §1.2) unless the operation answers one.
func (a *deploymentsAPI) post(o op) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := a.ops.newRequest(o)
		if !ok || !a.ops.registered(o) {
			reservedRoute(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10)) // a few fields
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			err = server.DecodeJSON(r, req)
		}
		if err != nil {
			writeDeployError(w, &dpe{Status: http.StatusBadRequest, Msg: "bad request body: " + err.Error()})
			return
		}
		pr := auth.PrincipalOf(r)
		res, err := a.ops.do(r.Context(), pr, o, req)
		if err != nil {
			writeDeployErr(w, err)
			return
		}
		server.WriteJSON(w, http.StatusOK, a.withState(r.Context(), pr, body, res))
	}
}

// withState adds the caller's state as it stands now to an answer that is a
// JSON object without one; anything else stays as the operation made it.
func (a *deploymentsAPI) withState(ctx context.Context, pr auth.Principal, body []byte, res any) any {
	raw, err := json.Marshal(res)
	var obj map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &obj) != nil || obj["state"] != nil {
		return res
	}
	var b struct {
		Tile string `json:"tile"`
	}
	_ = json.Unmarshal(body, &b)
	t, e := a.resolve(b.Tile)
	if e != nil || t.found.State == deployments.RecordHeld {
		return res
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	if obj["state"], err = json.Marshal(a.state(ctx, pr, t)); err != nil {
		return res
	}
	return obj
}

// ---- answers ----

// cleanRel reports a clean relative slash path: no empty, "." or ".."
// segment, no leading slash, no NUL.
func cleanRel(p string) bool {
	return p != "" && path.Clean(p) == p && !path.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") &&
		!strings.ContainsRune(p, 0)
}

func noTile(ref string) *dpe { return &dpe{Status: http.StatusNotFound, Msg: "no such tile: " + ref} }

func noDeployment(tile, dep string) *dpe {
	return &dpe{Status: http.StatusNotFound, Msg: util.NoDeployment(tile, dep).Error()}
}

func noRecord(tile string, status int) *dpe {
	return &dpe{Status: status, Kind: deployments.KindState,
		Msg: tile + " has no deployments yet: pause live reload or add a deployment first"}
}

func writeDeployError(w http.ResponseWriter, e *dpe) { server.WriteError(w, e.Status, e.Msg, e.Docs()) }

// writeDeployErr answers an error of the plane (its *Error as it says) or
// of a read's source (the store's conditions by status; a capture refused
// for its rate with Retry-After); anything else is a 500.
func writeDeployErr(w http.ResponseWriter, err error) {
	var rl *checkpoint.RateLimited
	if errors.As(err, &rl) {
		w.Header().Set("Retry-After", strconv.Itoa(int((rl.RetryAfter+time.Second-1)/time.Second)))
	}
	writeDeployError(w, asDeployError(err))
}

func asDeployError(err error) *dpe {
	var e *dpe
	if errors.As(err, &e) {
		return e
	}
	for _, c := range []struct {
		is     error
		status int
	}{
		{checkpoint.ErrBadID, http.StatusBadRequest}, {checkpoint.ErrUnknownCheckpoint, http.StatusNotFound},
		{util.ErrNoDeployment, http.StatusNotFound}, {checkpoint.ErrAmbiguousID, http.StatusConflict},
		{checkpoint.ErrRefused, http.StatusConflict}, {deployments.ErrStaleSeq, http.StatusConflict},
		{deployments.ErrRecordHeld, http.StatusConflict}, {checkpoint.ErrRateLimited, http.StatusTooManyRequests},
		{context.DeadlineExceeded, http.StatusGatewayTimeout}, {deployments.ErrNoAttempt, http.StatusNotFound},
		{checkpoint.ErrBadDiffPath, http.StatusBadRequest}, {checkpoint.ErrNothingToDiff, http.StatusConflict},
		{checkpoint.ErrDiffBusy, http.StatusTooManyRequests}, {checkpoint.ErrDiffTimeout, http.StatusGatewayTimeout},
		{checkpoint.ErrNotFetchable, http.StatusNotFound},
	} {
		if errors.Is(err, c.is) {
			return &dpe{Status: c.status, Msg: err.Error()}
		}
	}
	return &dpe{Status: http.StatusInternalServerError, Msg: err.Error()}
}

// asMap is v's JSON object form (a DeployEntry, whatever type holds it);
// nil for anything else. asMaps maps a list, dropping what isn't one.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	var m map[string]any
	if raw, err := json.Marshal(v); err != nil || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func asMaps(vs []any) []map[string]any {
	out := []map[string]any{}
	for _, v := range vs {
		if m := asMap(v); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// pick copies m's keys that are present.
func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}
