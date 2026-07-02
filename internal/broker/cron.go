package broker

import (
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

	"github.com/robfig/cron/v3"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/server"
)

// Cron resource (plans/auth.md §5): an element registers jobs that call its
// OWN endpoints on a schedule — cron can never be aimed at a third element.
// Invocations carry From: buxon/cron and the role the element chose at
// registration (bounded by its own declared roles). Jobs persist in
// data/resources/<scope-key>/<res>.cron.json and survive restarts.

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
	// Dispatch is how ticks reach elements: installed by main as a call into
	// the proxy (keeps broker ↔ proxy import-cycle-free).
	dispatch func(p auth.Principal, comp, path string) (int, string)
}

func newCronRunner(b *Broker) *cronRunner {
	cr := &cronRunner{
		b: b, sched: cron.New(),
		entries: map[string]cron.EntryID{}, jobs: map[string]cronJob{},
	}
	cr.load()
	cr.sched.Start()
	return cr
}

// SetDispatch installs the tick→element call path (from main, backed by the
// proxy). Must be called before jobs fire; ticks before that are dropped.
func (b *Broker) SetDispatch(fn func(p auth.Principal, comp, path string) (int, string)) {
	b.cron.mu.Lock()
	b.cron.dispatch = fn
	b.cron.mu.Unlock()
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
	_ = os.MkdirAll(filepath.Dir(cr.storePath()), 0o755)
	_ = os.WriteFile(cr.storePath(), bts, 0o644)
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

func (cr *cronRunner) fire(j cronJob) {
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
	b.cron.mu.Lock()
	jobs := []cronJob{}
	for _, j := range b.cron.jobs {
		if admin || p.Component == j.Component {
			jobs = append(jobs, j)
		}
	}
	b.cron.mu.Unlock()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Name < jobs[k].Name })
	server.WriteJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (b *Broker) apiCronPut(w http.ResponseWriter, r *http.Request) {
	var j cronJob
	if err := decodeJSON(r, &j); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "docs": "/docs/resources.md"})
		return
	}
	p := auth.PrincipalOf(r)
	if !p.Owner {
		j.Component = p.Component // elements schedule only themselves
	} else if j.Component == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "owner-registered jobs need \"component\""})
		return
	}
	rt, res, ok := b.parseRes(j.Resource)
	if !ok || res == nil || res.Type != "cron" {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such cron resource (declare one in scope.json)", "docs": "/docs/resources.md"})
		return
	}
	if err := b.allowRes(p, rt.String(), "writer"); err != nil {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "docs": "/docs/auth.md"})
		return
	}
	if err := b.cron.add(j); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.cron.persist()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiCronDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p := auth.PrincipalOf(r)
	comp := r.URL.Query().Get("component")
	if !b.IsAdmin(p) {
		comp = p.Component // unprivileged callers can only delete their own
	}
	if !b.cron.remove(comp, name) {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	b.cron.persist()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
