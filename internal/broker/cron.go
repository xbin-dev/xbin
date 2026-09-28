package broker

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Cron resource (plans/auth.md §5): an element registers jobs that call its
// OWN endpoints on a schedule — cron can never be aimed at a third element.
// Invocations carry From: xbin/cron and the role the element chose at
// registration (jobs are self-targeted, so the role is not separately vetted
// against the caller's grants — a tile is already admin of itself). Jobs
// persist in data/cron-jobs.json (workspace-wide) and survive restarts.

type cronJob struct {
	Name      string `json:"name"`
	Resource  string `json:"resource"`  // res:<scope>/<name>, type cron
	Schedule  string `json:"schedule"`  // 5-field cron or @every 30s
	Component string `json:"component"` // target = owner of the job
	Path      string `json:"path"`      // endpoint path, e.g. /tick
	Role      string `json:"role"`      // role the invocation carries
}

type cronRunner struct {
	b *Broker

	mu      sync.Mutex
	sched   *cron.Cron
	entries map[string]cron.EntryID // job key → entry
	jobs    map[string]cronJob
	// Scheduled component backups (plans/lifecycle.md), keyed by component. These
	// share the same scheduler but fire a xbind backup, not an element endpoint.
	backups       map[string]backupSchedule
	backupEntries map[string]cron.EntryID
	// Dispatch is how ticks reach elements: installed by main as a call into
	// the proxy (keeps broker ↔ proxy import-cycle-free).
	dispatch func(p auth.Principal, comp, path string) (int, string)
	// The jobs of deployments beyond main, kept in their own files
	// (dormant.go), keyed by depKey; the jobs a run now is delivering; and
	// the lock that serializes rewrites of those files.
	dep     map[string]*depJob
	running map[string]bool
	fileMu  sync.Mutex
}

func newCronRunner(b *Broker) *cronRunner {
	cr := &cronRunner{
		b: b, sched: cron.New(),
		entries: map[string]cron.EntryID{}, jobs: map[string]cronJob{},
		backups: map[string]backupSchedule{}, backupEntries: map[string]cron.EntryID{},
		dep: map[string]*depJob{}, running: map[string]bool{},
	}
	cr.load()
	cr.loadBackups()
	cr.sched.Start()
	return cr
}

// SetDispatch installs the tick→element call path (from main, backed by the
// proxy). Must be called before jobs fire; ticks before that are dropped.
// It then schedules the jobs of every deployment beyond main (dormant.go):
// boot calls it after installing the deployments plane's answers.
func (b *Broker) SetDispatch(fn func(p auth.Principal, comp, path string) (int, string)) {
	b.cron.mu.Lock()
	b.cron.dispatch = fn
	b.cron.mu.Unlock()
	b.cron.loadDeps()
}

func (cr *cronRunner) storePath() string {
	return filepath.Join(cr.b.Reg.Root, "data", "cron-jobs.json")
}

func (cr *cronRunner) load() {
	bts, err := os.ReadFile(cr.storePath())
	if err != nil {
		return
	}
	var jobs []cronJob
	if json.Unmarshal(bts, &jobs) != nil {
		return
	}
	for _, j := range jobs {
		if err := cr.add(j); err != nil {
			slog.Warn("cron: dropping persisted job", "job", j.Name, "err", err)
		}
	}
}

func (cr *cronRunner) persist() {
	cr.mu.Lock()
	jobs := make([]cronJob, 0, len(cr.jobs))
	for _, j := range cr.jobs {
		jobs = append(jobs, j)
	}
	cr.mu.Unlock()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Name < jobs[k].Name })
	bts, _ := json.MarshalIndent(jobs, "", "  ")
	_ = fsutil.WriteFileAtomicIn(cr.storePath(), bts, 0o644)
}

func jobKey(j cronJob) string { return j.Component + "\x00" + j.Name }

func (cr *cronRunner) add(j cronJob) error {
	if j.Name == "" || j.Schedule == "" || j.Component == "" || !strings.HasPrefix(j.Path, "/") {
		return fmt.Errorf("job needs {name, schedule, path:/…}")
	}
	if j.Role == "" {
		j.Role = "writer"
	}
	key := jobKey(j)
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if old, ok := cr.entries[key]; ok {
		cr.sched.Remove(old)
	}
	id, err := cr.sched.AddFunc(j.Schedule, func() { cr.fire(j) })
	if err != nil {
		return fmt.Errorf("bad schedule %q: %w", j.Schedule, err)
	}
	cr.entries[key] = id
	cr.jobs[key] = j
	return nil
}

func (cr *cronRunner) remove(component, name string) bool {
	key := component + "\x00" + name
	cr.mu.Lock()
	defer cr.mu.Unlock()
	id, ok := cr.entries[key]
	if !ok {
		return false
	}
	cr.sched.Remove(id)
	delete(cr.entries, key)
	delete(cr.jobs, key)
	return true
}

// forget drops every job of path (or a path under it) and persists — the
// leftovers a new tile at a removed tile's path must not inherit (D85).
func (cr *cronRunner) forget(path string) int {
	cr.mu.Lock()
	n := 0
	for key, j := range cr.jobs {
		if j.Component == path || strings.HasPrefix(j.Component, path+"/") {
			cr.sched.Remove(cr.entries[key])
			delete(cr.entries, key)
			delete(cr.jobs, key)
			n++
		}
	}
	cr.mu.Unlock()
	if n > 0 {
		cr.persist()
	}
	return n
}

func (cr *cronRunner) fire(j cronJob) {
	// A disabled/offloaded component's jobs don't fire — its backend won't spawn
	// anyway, and offload/disable should pause its schedule without unregistering
	// it (re-enable resumes; plans/lifecycle.md). Component jobs only (j.Component
	// set); xbind-internal jobs are unaffected.
	if j.Component != "" && cr.b.Reg.LifecycleState(j.Component) != registry.StateEnabled {
		return
	}
	// main's jobs fire while main's registrations are active: always without
	// a deployment record; with one, unless main isn't the primary and a
	// tile manager switched its deliveries off (the active set, dormant.go)
	// (D127h).
	if !cr.b.firing(j.Component, "") {
		return
	}
	cr.mu.Lock()
	dispatch := cr.dispatch
	cr.mu.Unlock()
	if dispatch == nil {
		return
	}
	p := auth.Principal{Component: CronPrincipal, Via: "cron", Role: j.Role}
	code, body := dispatch(p, j.Component, j.Path)
	if code >= 400 {
		slog.Warn("cron job failed", "job", j.Name, "component", j.Component,
			"status", code, "body", firstLine(body))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// DispatchViaProxy adapts an http.Handler (the proxy) into the cron dispatch
// function. Lives here so main stays a one-liner.
func DispatchViaProxy(h http.Handler) func(p auth.Principal, comp, path string) (int, string) {
	return func(p auth.Principal, comp, path string) (int, string) {
		req := httptest.NewRequest("POST", "/api/"+comp+path, nil)
		req = req.WithContext(auth.WithPrincipal(context.Background(), p))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
}

// --- API ---------------------------------------------------------------

func (b *Broker) apiCronList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	admin := b.IsAdmin(p)
	// the deployment listed: a tile principal's own, else ?deployment= or main
	dep, code, err := b.listDeployment(r, p)
	if err != nil {
		server.WriteError(w, code, err.Error(), "/docs/resources.md")
		return
	}
	keep := func(comp string) bool { return admin || p.Component == comp }
	var jobs []cronJob
	if dep == util.MainDeployment {
		b.cron.mu.Lock()
		for _, j := range b.cron.jobs {
			if keep(j.Component) {
				jobs = append(jobs, j)
			}
		}
		b.cron.mu.Unlock()
	} else {
		jobs = b.cron.depRows(dep, keep)
	}
	server.WriteJSON(w, http.StatusOK, b.cronList(r, dep, jobs))
}

func (b *Broker) apiCronPut(w http.ResponseWriter, r *http.Request) {
	var j cronJob
	if err := server.DecodeJSON(r, &j); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error(), "/docs/resources.md")
		return
	}
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) {
		j.Component = p.Component // elements schedule only themselves
	} else if j.Component == "" {
		server.WriteError(w, http.StatusBadRequest, "owner-registered jobs need \"component\"")
		return
	}
	// the deployment it registers for: a tile's own, else ?deployment= or main
	dep, code, err := b.regDeployment(r, p, j.Component)
	if err != nil {
		server.WriteError(w, code, err.Error(), "/docs/resources.md")
		return
	}
	rt, res, ok := b.parseRes(j.Resource)
	if !ok || res == nil || res.Type != "cron" {
		server.WriteError(w, http.StatusNotFound, "no such cron resource (declare one in scope.json)", "/docs/resources.md")
		return
	}
	if dep != util.MainDeployment {
		b.putDepCron(w, r, dep, j, rt) // its own file (dormant.go)
		return
	}
	// Unclamped: a job only schedules its own handler, so main while it isn't
	// the primary stores a foreign one under read (NP-09-18); block is
	// depEdge's.
	if err := b.allowResUnclamped(p, rt.String(), "writer"); err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	if err := b.depEdge(j.Component, dep, rt.String()); err != nil { // main while it isn't the primary
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	if err := b.cron.add(j); err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.cron.persist()
	writeRegOK(w, r, !b.firing(j.Component, dep))
}

func (b *Broker) apiCronDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p := auth.PrincipalOf(r)
	comp := r.URL.Query().Get("component")
	if !b.IsAdmin(p) {
		comp = p.Component // unprivileged callers can only delete their own
	}
	dep, code, err := b.regDeployment(r, p, comp)
	if err != nil {
		server.WriteError(w, code, err.Error(), "/docs/resources.md")
		return
	}
	if dep != util.MainDeployment {
		b.deleteDepCron(w, r, comp, dep, name) // its own file (dormant.go)
		return
	}
	if !b.cron.remove(comp, name) {
		server.WriteError(w, http.StatusNotFound, "no such job")
		return
	}
	b.cron.persist()
	writeRegOK(w, r, false)
}

// --- run now (dormant.go keeps the jobs of deployments beyond main) --------

// runNowWait is how long run now waits for the handler's answer.
const runNowWait = 2 * time.Minute

// RunNow delivers deployment dep of tile's cron job once (11-contract §1.9)
// (D127h): as xbin/cron with the job's role, to dep, through the dispatch a
// tick uses, whether its deliveries are on or not. It waits for the answer
// at most two minutes, and one run of a job is in flight at a time. The
// plane judges who may ask (terminal level). Errors are *deployments.Error:
// 404 no such deployment or job, 409 the primary (its jobs fire on
// schedule), a disabled tile or a run in flight, 502 the backend can't
// start, 504 no answer in time.
func (b *Broker) RunNow(ctx context.Context, tile, dep, job string) (deployments.Delivery, error) {
	fail := func(status int, kind, msg string) (deployments.Delivery, error) {
		return deployments.Delivery{}, &deployments.Error{Status: status, Kind: kind, Msg: msg}
	}
	dep = cmp.Or(dep, util.MainDeployment)
	switch {
	case !b.hasDeployment(tile, dep):
		return fail(http.StatusNotFound, "", util.NoDeployment(tile, dep).Error())
	case b.isPrimary(tile, dep):
		return fail(http.StatusConflict, deployments.KindState, fmt.Sprintf("%s is the primary of %s: its jobs fire on schedule", dep, tile))
	}
	if st := b.Reg.LifecycleState(tile); st != registry.StateEnabled {
		return fail(http.StatusConflict, deployments.KindState, fmt.Sprintf("%s is %s: enable it to run its jobs", tile, st))
	}
	cr, key := b.cron, depKey(tile, dep, job)
	cr.mu.Lock()
	j, ok := cr.jobs[jobKey(cronJob{Component: tile, Name: job})]
	if dep != util.MainDeployment {
		var dj *depJob
		if dj, ok = cr.dep[key]; ok {
			j = dj.job
		}
	}
	dispatch, busy := cr.dispatch, cr.running[key]
	if ok && !busy && dispatch != nil {
		cr.running[key] = true
	}
	cr.mu.Unlock()
	switch {
	case !ok:
		return fail(http.StatusNotFound, "", fmt.Sprintf("%s's deployment %s has no cron job %q", tile, dep, job))
	case busy:
		return fail(http.StatusConflict, deployments.KindState, fmt.Sprintf("a run of %s on %s+%s is in flight", job, tile, dep))
	case dispatch == nil:
		return fail(http.StatusBadGateway, "", "cron deliveries aren't wired in this xbind")
	}
	type answer struct {
		code int
		body string
	}
	done, start := make(chan answer, 1), time.Now()
	go func() {
		code, body := dispatch(cronPrincipal(dep, j.Role), tile, j.Path)
		cr.mu.Lock()
		delete(cr.running, key)
		cr.mu.Unlock()
		done <- answer{code, body}
	}()
	timer := time.NewTimer(runNowWait)
	defer timer.Stop()
	select {
	case a := <-done:
		if msg, failed := proxyFailure(a.code, a.body); failed {
			return fail(http.StatusBadGateway, "", fmt.Sprintf("%s+%s's backend can't start: %s", tile, dep, msg))
		}
		if a.code >= 400 {
			slog.Warn("cron run now failed", "job", job, "component", tile, "deployment", dep, "status", a.code, "body", firstLine(a.body))
		}
		return deployments.Delivery{Status: a.code, MS: time.Since(start).Milliseconds()}, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	return fail(http.StatusGatewayTimeout, "", fmt.Sprintf("%s+%s didn't answer the run of %s within %s; it is still in flight", tile, dep, job, runNowWait))
}

// proxyFailure reports whether a 502 is the proxy's own answer, not the
// handler's: the backend couldn't build or start (the proxy's error shape,
// naming its docs page), with the text and the detail's first line.
func proxyFailure(code int, body string) (string, bool) {
	if code != http.StatusBadGateway {
		return "", false
	}
	var e struct {
		Error  string `json:"error"`
		Docs   string `json:"docs"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal([]byte(body), &e) != nil || e.Error == "" || e.Docs != "/docs/protocol.md" {
		return "", false
	}
	if d := firstLine(e.Detail); d != "" {
		return e.Error + ": " + d, true
	}
	return e.Error, true
}
