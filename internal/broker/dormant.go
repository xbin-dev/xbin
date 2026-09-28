package broker

// dormant.go — the cron jobs and bus push subscriptions of a tile's
// deployments beyond main (P13, revised 2026-09-28) (09-fabric §6, §7;
// 11-contract §8, §10.2).
//
// Where they live. A non-main deployment's registrations are kept in its own
// files, data/deployments/<TileKey>/<name>/cron.json and
// bus-subscriptions.json, read and written only through the plane's hooks
// (readDeploymentFile, writeDeploymentFile, removeDeploymentFile), never as
// rows of data/cron-jobs.json or data/bus-subscriptions.json: an older xbind
// would load such rows as main's and fire them (12-compat PO-9). main's stay
// in today's stores whether or not main is the primary, so a component
// backup carries main's rows only. The files are the truth. The broker holds
// them in memory (scheduled jobs, subscription queues), loaded when boot
// installs the dispatch and set again from the file at every change.
//
// The active set. Every deployment's registrations fire, each for its own
// deployment, except those of a non-primary deployment whose deliveries a
// tile manager switched off, which are dormant (registrationsActive; the
// switch is an off switch, default on, never the primary's). It is asked at
// every tick, publish and delivery and never cached, so a switch, a
// reassignment or a removal applies at the next tick or event without the
// broker being told: a dormant job stays scheduled and its tick returns
// early; a publish queues nothing for a dormant subscription and counts the
// event as dormant, not dropped. A delivery reaches the registration's own
// deployment, never the primary: its principal names the owner (Route's
// rule 1), which runs it with its own data. A subscription reads its own
// scope's bus in its own (scope, name) namespace, and another scope's (its
// primary's namespace) through the edge policy, asked again at every
// delivery (busOwnerMayRead): read reads, block refuses. Publishing is
// unchanged: into the publisher's own namespace. Interface instances and
// ingress hosts stay the primary's alone (dormantroutes.go), and a
// non-primary's notifications are still held (P13).
//
// Run now delivers one job of a non-primary deployment once, whatever its
// switch says; the plane judges who may ask (terminal level).
//
// The files stay separate from main's stores whatever the switch says, so an
// older binary, which never reads them, still never fires them as main's.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/robfig/cron/v3"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// The registration files this file keeps (11-contract §10.2), their schema,
// and every file a deployment's directory may hold, which a tile creation at
// the path drops.
const (
	depCronFile   = "cron.json"
	depBusFile    = "bus-subscriptions.json"
	depFileSchema = 1
)

var depRegistrationFiles = [...]string{depCronFile, depBusFile, "iface-instances.json",
	"ingress-hosts.json", "backup-schedule.json", "sandboxes.json"}

// depCronRow and depBusRow are the rows of a deployment's files: today's
// shapes without component, since the file names the tile and deployment.
type depCronRow struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
	Schedule string `json:"schedule"`
	Path     string `json:"path"`
	Role     string `json:"role"`
}

type depBusRow struct {
	Name     string `json:"name"`
	Resource string `json:"resource"`
	Prefix   string `json:"prefix,omitempty"`
	Path     string `json:"path"`
	Role     string `json:"role"`
}

type depCronDoc struct {
	Schema int          `json:"schema"`
	Jobs   []depCronRow `json:"jobs"`
}

type depBusDoc struct {
	Schema        int         `json:"schema"`
	Subscriptions []depBusRow `json:"subscriptions"`
}

func (r depCronRow) job(tile string) cronJob {
	return cronJob{Name: r.Name, Resource: r.Resource, Schedule: r.Schedule, Component: tile, Path: r.Path, Role: r.Role}
}

func (r depBusRow) sub(tile string) busSub {
	return busSub{Name: r.Name, Resource: r.Resource, Prefix: r.Prefix, Component: tile, Path: r.Path, Role: r.Role}
}

func (r depCronRow) check() error {
	if r.Name == "" || r.Schedule == "" || r.Resource == "" || !strings.HasPrefix(r.Path, "/") {
		return errors.New("job needs {name, schedule, path:/…}")
	}
	if _, err := cron.ParseStandard(r.Schedule); err != nil {
		return fmt.Errorf("bad schedule %q: %w", r.Schedule, err)
	}
	return nil
}

func (r depBusRow) check() error {
	if !busSubNameRe.MatchString(r.Name) || r.Resource == "" || !strings.HasPrefix(r.Path, "/") {
		return errors.New("need {name: [A-Za-z0-9._-]{1,64}, resource, path: /…, prefix?, role?}")
	}
	return nil
}

// depKey keys a non-main deployment's registration in memory; depPrefix is
// every key of one deployment. Tile paths hold no NUL.
func depKey(tile, dep, name string) string { return depPrefix(tile, dep) + name }
func depPrefix(tile, dep string) string    { return tile + "\x00" + dep + "\x00" }

// statusErr is a registration failure with the HTTP status it answers.
type statusErr struct {
	code int
	msg  string
}

func (e statusErr) Error() string { return e.msg }

func writeRegErr(w http.ResponseWriter, err error) {
	var se statusErr
	switch {
	case errors.As(err, &se):
		server.WriteError(w, se.code, se.msg, "/docs/resources.md")
	case errors.Is(err, util.ErrNoDeployment):
		server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/resources.md")
	default:
		server.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}

// writeRegOK answers a registration write: today's {"ok":"true"}, plus the
// echo of a ?deployment= the request sent (12-compat NP-12-2) and dormant
// when what it stored doesn't take effect now (11-contract §8).
func writeRegOK(w http.ResponseWriter, r *http.Request, dormant bool) {
	echo := r.URL.Query().Get("deployment")
	if echo == "" && !dormant {
		server.WriteOK(w)
		return
	}
	out := map[string]any{"ok": "true"}
	if echo != "" {
		out["deployment"] = echo
	}
	if dormant {
		out["dormant"] = true
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// ---- which deployment a call acts on, and what it may register ----

// regDeployment is the deployment of tile a cron or bus-subscription call by
// p acts on (11-contract §0.4 DR1, §8): the tile's own principal acts on its
// bound deployment and may name only that one with ?deployment=; anyone
// else (an admin naming the tile) acts on the deployment ?deployment= names,
// or main (the name rule). The error's status is returned beside it.
func (b *Broker) regDeployment(r *http.Request, p auth.Principal, tile string) (string, int, error) {
	q := r.URL.Query().Get("deployment")
	if p.Component != "" && p.Component == tile {
		dep, err := b.addressed(p, tile)
		switch {
		case errors.Is(err, util.ErrNoDeployment):
			return "", http.StatusNotFound, err
		case err != nil:
			return "", http.StatusForbidden, err
		case q != "" && q != dep:
			return "", http.StatusForbidden, fmt.Errorf("%s's deployment %q registers only its own cron jobs and bus subscriptions, not %q's", tile, dep, q)
		}
		return dep, 0, nil
	}
	dep := cmp.Or(q, util.MainDeployment)
	if dep != util.MainDeployment && !b.hasDeployment(tile, dep) {
		return "", http.StatusNotFound, util.NoDeployment(tile, dep)
	}
	return dep, 0, nil
}

// listDeployment is the deployment whose registrations a list by p shows: a
// tile principal's bound one; for admins and people, ?deployment= or main.
func (b *Broker) listDeployment(r *http.Request, p auth.Principal) (string, int, error) {
	if p.Component != "" && !b.IsAdmin(p) {
		return b.regDeployment(r, p, p.Component)
	}
	return cmp.Or(r.URL.Query().Get("deployment"), util.MainDeployment), 0, nil
}

// firing reports whether deployment dep ("" is main) of tile's cron jobs and
// bus subscriptions take effect now: unless its deliveries are switched off.
func (b *Broker) firing(tile, dep string) bool {
	fires, _ := b.registrationsActive(tile, cmp.Or(dep, util.MainDeployment))
	return fires
}

// depEdge is the edge check of a registration on the resource res by
// deployment dep of tile, at registration and, for a bus subscription, at
// every delivery (09-fabric §7; NP-09-18). The primary meets none, and
// neither does an own-scope resource, which is deployment data, not an edge.
// Otherwise the edge's block refuses it (08-data §7), and read lets it
// subscribe, like a read bind: a registration only ever reaches dep itself,
// and a subscription only reads.
func (b *Broker) depEdge(tile, dep, res string) error {
	if b.isPrimary(tile, dep) {
		return nil
	}
	if c, ok := b.Reg.Component(tile); ok && b.sameScope(c, res) {
		return nil
	}
	return b.resolveTarget(tile, dep, res).Deny
}

// depResAllowed is what a non-main deployment registers on rt with: the
// tile's authority at want (a deployment never widens it, P11; an admin
// registering for a tile doesn't lend it theirs), then the edge.
func (b *Broker) depResAllowed(tile, dep string, rt resTarget, want string) error {
	if err := b.allowRes(auth.Principal{Component: tile}, rt.String(), want); err != nil {
		return err
	}
	return b.depEdge(tile, dep, rt.String())
}

// busOwnerMayRead is a delivery's re-check (09-fabric §5.3, §7): the
// subscriber's owner deployment ("" is main) can still read the bus.
func (b *Broker) busOwnerMayRead(tile, owner, res string) error {
	if err := b.allowRes(auth.Principal{Component: tile, Deployment: owner}, res, "reader"); err != nil {
		return err
	}
	return b.depEdge(tile, owner, res)
}

// ---- the files ----

// readDepFile decodes deployment dep of tile's registration file into doc;
// a deployment without one leaves doc empty. A file of another schema (a
// newer xbind's) is refused, never read as this one's.
func (b *Broker) readDepFile(tile, dep, file string, doc any) error {
	data, err := b.readDeploymentFile(tile, dep, file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: deployment %s's %s: %w", tile, dep, file, err)
	}
	if head.Schema != depFileSchema {
		return statusErr{http.StatusConflict, fmt.Sprintf("%s: deployment %s's %s has schema %d, which this xbind doesn't read",
			tile, dep, file, head.Schema)}
	}
	return json.Unmarshal(data, doc)
}

// writeDepFile writes doc as deployment dep of tile's registration file, or
// removes the file when it holds nothing.
func (b *Broker) writeDepFile(tile, dep, file string, doc any, empty bool) error {
	if empty {
		return b.removeDeploymentFile(tile, dep, file)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return b.writeDeploymentFile(tile, dep, file, append(data, '\n'))
}

// beyondMain lists every tile's deployments other than main, for the load.
func (b *Broker) beyondMain() map[string][]string {
	out := map[string][]string{}
	for _, c := range b.Reg.Components() {
		_, names := b.deploymentsOf(c.Path)
		for _, n := range names {
			if n != util.MainDeployment {
				out[c.Path] = append(out[c.Path], n)
			}
		}
	}
	return out
}

func (r depCronRow) key() string { return r.Name }
func (r depBusRow) key() string  { return r.Name }

// upsertRow replaces the row named like row, or appends it; removeRow drops
// the row named n, reporting whether there was one.
func upsertRow[T interface{ key() string }](rows []T, row T) []T {
	if i := slices.IndexFunc(rows, func(r T) bool { return r.key() == row.key() }); i >= 0 {
		rows[i] = row
		return rows
	}
	return append(rows, row)
}

func removeRow[T interface{ key() string }](rows []T, n string) ([]T, bool) {
	i := slices.IndexFunc(rows, func(r T) bool { return r.key() == n })
	if i < 0 {
		return rows, false
	}
	return slices.Delete(rows, i, i+1), true
}

// ---- cron ----

// depJob is one scheduled job of a deployment beyond main.
type depJob struct {
	dep   string
	job   cronJob // Component is the tile
	entry cron.EntryID
}

// cronPrincipal is the identity a tick of deployment dep's job carries: the
// deployment by the name rule (absent means main), so Route sends it there.
func cronPrincipal(dep, role string) auth.Principal {
	if dep == util.MainDeployment {
		dep = ""
	}
	return auth.Principal{Component: CronPrincipal, Via: "cron", Role: role, Deployment: dep}
}

// loadDeps schedules every non-main deployment's jobs from its file. Boot
// installs the dispatch (stepProxy) after the plane's answers (stepBroker),
// so every tile's deployments are known here. A file this xbind can't read
// leaves its deployment's jobs unscheduled.
func (cr *cronRunner) loadDeps() {
	cr.fileMu.Lock()
	defer cr.fileMu.Unlock()
	for tile, deps := range cr.b.beyondMain() {
		for _, dep := range deps {
			var doc depCronDoc
			if err := cr.b.readDepFile(tile, dep, depCronFile, &doc); err != nil {
				slog.Warn("cron: a deployment's jobs aren't loaded", "tile", tile, "deployment", dep, "err", err)
				continue
			}
			cr.setDep(tile, dep, doc.Jobs)
		}
	}
}

// setDep makes rows exactly deployment dep of tile's scheduled jobs. A row
// that doesn't check stays in the file, unscheduled.
func (cr *cronRunner) setDep(tile, dep string, rows []depCronRow) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	prefix := depPrefix(tile, dep)
	for key, dj := range cr.dep {
		if strings.HasPrefix(key, prefix) {
			cr.sched.Remove(dj.entry)
			delete(cr.dep, key)
		}
	}
	for _, row := range rows {
		if row.Role == "" {
			row.Role = "writer"
		}
		if err := row.check(); err != nil {
			slog.Warn("cron: dropping a deployment's job", "tile", tile, "deployment", dep, "job", row.Name, "err", err)
			continue
		}
		dj := &depJob{dep: dep, job: row.job(tile)}
		j := dj.job
		id, err := cr.sched.AddFunc(j.Schedule, func() { cr.fireDep(dep, j) })
		if err != nil {
			continue
		}
		dj.entry = id
		cr.dep[depKey(tile, dep, j.Name)] = dj
	}
}

// rewriteDep changes deployment dep of tile's cron.json and schedules what
// it then holds, one change at a time.
func (cr *cronRunner) rewriteDep(tile, dep string, change func([]depCronRow) ([]depCronRow, error)) error {
	cr.fileMu.Lock()
	defer cr.fileMu.Unlock()
	var doc depCronDoc
	if err := cr.b.readDepFile(tile, dep, depCronFile, &doc); err != nil {
		return err
	}
	rows, err := change(doc.Jobs)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	if err := cr.b.writeDepFile(tile, dep, depCronFile, depCronDoc{Schema: depFileSchema, Jobs: rows}, len(rows) == 0); err != nil {
		return err
	}
	cr.setDep(tile, dep, rows)
	return nil
}

// fireDep is a tick of deployment dep's job: nothing unless the tile is
// enabled and the deployment's registrations fire now (the active set).
func (cr *cronRunner) fireDep(dep string, j cronJob) {
	b := cr.b
	if b.Reg.LifecycleState(j.Component) != registry.StateEnabled || !b.firing(j.Component, dep) {
		return
	}
	cr.mu.Lock()
	dispatch := cr.dispatch
	cr.mu.Unlock()
	if dispatch == nil {
		return
	}
	code, body := dispatch(cronPrincipal(dep, j.Role), j.Component, j.Path)
	if code >= 400 {
		slog.Warn("cron job failed", "job", j.Name, "component", j.Component, "deployment", dep,
			"status", code, "body", firstLine(body))
	}
}

// depRows lists deployment dep's jobs of the tiles keep admits.
func (cr *cronRunner) depRows(dep string, keep func(tile string) bool) []cronJob {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	var out []cronJob
	for _, dj := range cr.dep {
		if dj.dep == dep && keep(dj.job.Component) {
			out = append(out, dj.job)
		}
	}
	return out
}

// cronJobView is a row of GET /cron/jobs: today's row, plus the deployment
// by the name rule and whether it is dormant (11-contract §8), both absent
// for a zero-state tile.
type cronJobView struct {
	cronJob
	Deployment string `json:"deployment,omitempty"`
	Dormant    bool   `json:"dormant,omitempty"`
}

// cronList is GET /cron/jobs's answer for deployment dep's jobs.
func (b *Broker) cronList(r *http.Request, dep string, jobs []cronJob) map[string]any {
	rows := make([]cronJobView, 0, len(jobs))
	for _, j := range jobs {
		v := cronJobView{cronJob: j, Dormant: !b.firing(j.Component, dep)}
		if dep != util.MainDeployment {
			v.Deployment = dep
		}
		rows = append(rows, v)
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	out := map[string]any{"jobs": rows}
	if q := r.URL.Query().Get("deployment"); q != "" {
		out["deployment"] = q
	}
	return out
}

// putDepCron is PUT /cron/jobs for a deployment beyond main: stored in its
// cron.json, active for it unless its deliveries are off.
func (b *Broker) putDepCron(w http.ResponseWriter, r *http.Request, dep string, j cronJob, rt resTarget) {
	tile := j.Component
	if err := b.depResAllowed(tile, dep, rt, "writer"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	row := depCronRow{Name: j.Name, Resource: rt.String(), Schedule: j.Schedule, Path: j.Path, Role: cmp.Or(j.Role, "writer")}
	if err := row.check(); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.cron.rewriteDep(tile, dep, func(rows []depCronRow) ([]depCronRow, error) {
		return upsertRow(rows, row), nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, !b.firing(tile, dep))
}

// deleteDepCron is DELETE /cron/jobs/{name} for a deployment beyond main.
func (b *Broker) deleteDepCron(w http.ResponseWriter, r *http.Request, tile, dep, name string) {
	if err := b.cron.rewriteDep(tile, dep, func(rows []depCronRow) ([]depCronRow, error) {
		rows, ok := removeRow(rows, name)
		if !ok {
			return nil, statusErr{http.StatusNotFound, "no such job"}
		}
		return rows, nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, false)
}

// ---- bus push subscriptions ----

// loadDeps holds every non-main deployment's subscriptions from its file,
// as the cron load does.
func (bs *busSubs) loadDeps() {
	bs.fileMu.Lock()
	defer bs.fileMu.Unlock()
	for tile, deps := range bs.b.beyondMain() {
		for _, dep := range deps {
			var doc depBusDoc
			if err := bs.b.readDepFile(tile, dep, depBusFile, &doc); err != nil {
				slog.Warn("bus subscriptions: a deployment's aren't loaded", "tile", tile, "deployment", dep, "err", err)
				continue
			}
			bs.setDep(tile, dep, doc.Subscriptions)
		}
	}
}

// setDep makes rows exactly deployment dep of tile's subscriptions. One
// that stays keeps its counters and whatever is queued; one that goes stops
// its drain.
func (bs *busSubs) setDep(tile, dep string, rows []depBusRow) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	keep := map[string]bool{}
	for _, row := range rows {
		if row.Role == "" {
			row.Role = "writer"
		}
		if err := row.check(); err != nil {
			slog.Warn("bus subscriptions: dropping a deployment's", "tile", tile, "deployment", dep, "name", row.Name, "err", err)
			continue
		}
		key := depKey(tile, dep, row.Name)
		keep[key] = true
		if st, ok := bs.dep[key]; ok {
			st.sub = row.sub(tile)
			continue
		}
		bs.dep[key] = &busSubState{sub: row.sub(tile), owner: dep}
	}
	prefix := depPrefix(tile, dep)
	for key, st := range bs.dep {
		if strings.HasPrefix(key, prefix) && !keep[key] {
			st.gone, st.queue = true, nil
			delete(bs.dep, key)
		}
	}
}

// rewriteDep changes deployment dep of tile's bus-subscriptions.json and
// holds what it then lists, one change at a time.
func (bs *busSubs) rewriteDep(tile, dep string, change func([]depBusRow) ([]depBusRow, error)) error {
	bs.fileMu.Lock()
	defer bs.fileMu.Unlock()
	var doc depBusDoc
	if err := bs.b.readDepFile(tile, dep, depBusFile, &doc); err != nil {
		return err
	}
	rows, err := change(doc.Subscriptions)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, k int) bool { return rows[i].Name < rows[k].Name })
	if err := bs.b.writeDepFile(tile, dep, depBusFile, depBusDoc{Schema: depFileSchema, Subscriptions: rows}, len(rows) == 0); err != nil {
		return err
	}
	bs.setDep(tile, dep, rows)
	return nil
}

// depViews lists deployment dep's subscriptions of the tiles keep admits,
// with their counters.
func (bs *busSubs) depViews(dep string, keep func(tile string) bool) []busSubView {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	var out []busSubView
	for _, st := range bs.dep {
		if st.owner == dep && keep(st.sub.Component) {
			out = append(out, busSubView{busSub: st.sub, busSubStats: st.stats, Deployment: dep})
		}
	}
	return out
}

// busList is GET /bus/subscriptions's answer for deployment dep's rows.
func (b *Broker) busList(r *http.Request, dep string, rows []busSubView) map[string]any {
	for i := range rows {
		rows[i].Dormant = !b.firing(rows[i].Component, dep)
	}
	sort.Slice(rows, func(i, k int) bool {
		if rows[i].Component != rows[k].Component {
			return rows[i].Component < rows[k].Component
		}
		return rows[i].Name < rows[k].Name
	})
	out := map[string]any{"subscriptions": rows}
	if q := r.URL.Query().Get("deployment"); q != "" {
		out["deployment"] = q
	}
	return out
}

// putDepSub is PUT /bus/subscriptions for a deployment beyond main: stored
// in its bus-subscriptions.json, at most busSubsPerComp per (tile,
// deployment) (NP-09-16), active for it unless its deliveries are off.
func (b *Broker) putDepSub(w http.ResponseWriter, r *http.Request, dep string, s busSub, rt resTarget) {
	tile := s.Component
	if err := b.depResAllowed(tile, dep, rt, "reader"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	row := depBusRow{Name: s.Name, Resource: rt.String(), Prefix: s.Prefix, Path: s.Path, Role: cmp.Or(s.Role, "writer")}
	if err := b.bus.rewriteDep(tile, dep, func(rows []depBusRow) ([]depBusRow, error) {
		if !slices.ContainsFunc(rows, func(r depBusRow) bool { return r.Name == row.Name }) && len(rows) >= busSubsPerComp {
			return nil, statusErr{http.StatusConflict, fmt.Sprintf("%s's deployment %s already has %d bus subscriptions (the limit) — delete one first",
				tile, dep, len(rows))}
		}
		return upsertRow(rows, row), nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, !b.firing(tile, dep))
}

// deleteDepSub is DELETE /bus/subscriptions/{name} for a deployment beyond
// main.
func (b *Broker) deleteDepSub(w http.ResponseWriter, r *http.Request, tile, dep, name string) {
	if err := b.bus.rewriteDep(tile, dep, func(rows []depBusRow) ([]depBusRow, error) {
		rows, ok := removeRow(rows, name)
		if !ok {
			return nil, statusErr{http.StatusNotFound, "no such subscription"}
		}
		return rows, nil
	}); err != nil {
		writeRegErr(w, err)
		return
	}
	writeRegOK(w, r, false)
}

// pruneDepSub drops deployment dep's subscription whose tile is gone.
func (b *Broker) pruneDepSub(tile, dep, name string) {
	err := b.bus.rewriteDep(tile, dep, func(rows []depBusRow) ([]depBusRow, error) {
		rows, _ = removeRow(rows, name)
		return rows, nil
	})
	if err != nil {
		slog.Warn("bus subscription of a gone component not pruned", "component", tile, "deployment", dep, "name", name, "err", err)
		return
	}
	slog.Info("bus subscription pruned: its component is gone", "component", tile, "deployment", dep, "name", name)
}

// ---- what the panel lists, and dropping ----

// DeploymentRegistrations lists deployment dep of tile's cron jobs and bus
// push subscriptions, for the state's Deployment.registrations (11-contract
// §1.1): main's from today's stores, another's from its files, each dormant
// only while its deployment's deliveries are off. Interface instances and
// ingress hosts are their own planes', dormant beyond the primary.
func (b *Broker) DeploymentRegistrations(tile, dep string) []deployments.Registration {
	dep = cmp.Or(dep, util.MainDeployment)
	mine := func(t string) bool { return t == tile }
	var jobs []cronJob
	var subs []busSub
	if dep == util.MainDeployment {
		b.cron.mu.Lock()
		for _, j := range b.cron.jobs {
			if j.Component == tile {
				jobs = append(jobs, j)
			}
		}
		b.cron.mu.Unlock()
		subs = b.bus.forComponent(tile)
	} else {
		jobs = b.cron.depRows(dep, mine)
		for _, v := range b.bus.depViews(dep, mine) {
			subs = append(subs, v.busSub)
		}
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Name < jobs[k].Name })
	sort.Slice(subs, func(i, k int) bool { return subs[i].Name < subs[k].Name })
	dormant := !b.firing(tile, dep)
	out := make([]deployments.Registration, 0, len(jobs)+len(subs))
	for _, j := range jobs {
		out = append(out, deployments.Registration{Kind: deployments.RegCron, Name: j.Name, Schedule: j.Schedule,
			Path: j.Path, Dormant: dormant})
	}
	for _, s := range subs {
		out = append(out, deployments.Registration{Kind: deployments.RegBus, Name: s.Name, Resource: s.Resource,
			Prefix: s.Prefix, Path: s.Path, Dormant: dormant})
	}
	return append(out, b.DeploymentRouteRegistrations(tile, dep)...) // interface instances, ingress hosts (WP-50)
}

// DropDeploymentRegistrations deletes deployment dep of tile's cron jobs
// and bus subscriptions, their files and what the broker holds of them, when
// the deployment is removed (05-model §5; 06-security T6). main's live in
// today's stores and are never dropped here. Call it outside the plane's
// record locks: it writes through the plane's hooks.
func (b *Broker) DropDeploymentRegistrations(tile, dep string) error {
	if cmp.Or(dep, util.MainDeployment) == util.MainDeployment {
		return nil
	}
	b.cron.fileMu.Lock()
	defer b.cron.fileMu.Unlock()
	b.bus.fileMu.Lock()
	defer b.bus.fileMu.Unlock()
	b.cron.setDep(tile, dep, nil)
	b.bus.setDep(tile, dep, nil)
	return errors.Join(b.removeDeploymentFile(tile, dep, depCronFile), b.removeDeploymentFile(tile, dep, depBusFile))
}

// dropDormantAt drops the registrations of path's deployments beyond main,
// and of the paths under it, before a new tile is created there (P29;
// 06-security T6): what the broker holds, and every registration file of
// each deployment directory path keeps (a record an older owner left inert
// was never loaded) or the broker held, so the new tile starts with none
// (D85). It answers how many registrations it dropped from memory.
func (b *Broker) dropDormantAt(path string) int {
	under := func(t string) bool { return t == path || strings.HasPrefix(t, path+"/") }
	b.cron.fileMu.Lock()
	defer b.cron.fileMu.Unlock()
	b.bus.fileMu.Lock()
	defer b.bus.fileMu.Unlock()
	pairs := map[[2]string]bool{}
	for _, dep := range b.deploymentDirs(path) {
		pairs[[2]string{path, dep}] = true
	}
	n := 0
	b.cron.mu.Lock()
	for key, dj := range b.cron.dep {
		if under(dj.job.Component) {
			b.cron.sched.Remove(dj.entry)
			delete(b.cron.dep, key)
			pairs[[2]string{dj.job.Component, dj.dep}] = true
			n++
		}
	}
	b.cron.mu.Unlock()
	b.bus.mu.Lock()
	for key, st := range b.bus.dep {
		if under(st.sub.Component) {
			st.gone, st.queue = true, nil
			delete(b.bus.dep, key)
			pairs[[2]string{st.sub.Component, st.owner}] = true
			n++
		}
	}
	b.bus.mu.Unlock()
	for pair := range pairs {
		for _, f := range depRegistrationFiles {
			if err := b.removeDeploymentFile(pair[0], pair[1], f); err != nil {
				slog.Warn("a removed tile's registration file is still there", "tile", pair[0], "deployment", pair[1], "file", f, "err", err)
			}
		}
	}
	return n
}

// deploymentDirs lists the deployment directories data/deployments keeps
// beside tile's record, whatever the record says.
func (b *Broker) deploymentDirs(tile string) []string {
	dir := filepath.Join(b.Reg.Root, "data", "deployments", util.TileKey(tile))
	entries, err := os.ReadDir(dir) // walk-ok: data/deployments is xbind's own; no tile writes there
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != util.MainDeployment && util.DeploymentNameOK(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}
