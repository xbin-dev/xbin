// sandboxes.go — the contract's sandboxes: list, create (a plain base, an
// image's clone, or a clone of a sandbox), get, PATCH, DELETE, start and
// stop; and what makes a sandbox usable — its first start, where its
// workdir and home are made (ready).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// contractHandler is the contract's routes, /sbx/…, answering as the
// consumer each request says it comes from (X-XBin-From — main.go admits
// only the consumer role and the tile itself).
func (m *Manager) contractHandler() http.Handler {
	x := http.NewServeMux()
	x.HandleFunc("GET /sbx/hello", m.hello)
	x.HandleFunc("GET /sbx/sandboxes", m.list)
	x.HandleFunc("POST /sbx/sandboxes", m.create)
	x.HandleFunc("GET /sbx/sandboxes/{id}", m.get)
	x.HandleFunc("PATCH /sbx/sandboxes/{id}", m.patch)
	x.HandleFunc("DELETE /sbx/sandboxes/{id}", m.del)
	x.HandleFunc("POST /sbx/sandboxes/{id}/{action}", m.action)
	m.commandRoutes(x)
	m.fileRoutes(x)
	x.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { // errors are JSON, even for a route that isn't here
		fail(w, http.StatusNotFound, "not-found", "no route "+r.Method+" "+r.URL.Path)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-XBin-From") == "" && r.URL.Path != "/sbx/hello" {
			fail(w, http.StatusForbidden, "not-allowed", "no consumer: calls come from a tile (X-XBin-From, set by xbind)")
			return
		}
		x.ServeHTTP(w, r)
	})
}

// find is {id} for the caller: its record (a copy), or the refusal answered.
func (m *Manager) find(w http.ResponseWriter, r *http.Request) (record, caller, bool) {
	c := callerOf(r)
	m.mu.Lock()
	rec := m.recs[r.PathValue("id")]
	if rec == nil || !rec.visible(c) {
		m.mu.Unlock()
		fail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return record{}, c, false
	}
	if !rec.personOK(c) {
		m.mu.Unlock()
		fail(w, http.StatusForbidden, "not-allowed", c.user+" may not use this sandbox")
		return record{}, c, false
	}
	cp := *rec
	m.mu.Unlock()
	return cp, c, true
}

// update changes a record and writes it through (m.mu held).
func (m *Manager) update(id string, f func(*record)) (record, error) {
	rec := m.recs[id]
	if rec == nil {
		return record{}, errf(http.StatusNotFound, "not-found", "no such sandbox")
	}
	next := *rec
	f(&next)
	if err := m.st.putRecord(&next); err != nil {
		return *rec, err
	}
	*rec = next
	return next, nil
}

// lock takes a sandbox's (or any key's) lifecycle lock.
func (m *Manager) lock(key string) (unlock func()) {
	m.mu.Lock()
	l := m.locks[key]
	if l == nil {
		l = &opLock{}
		m.locks[key] = l
	}
	l.n++
	m.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		m.mu.Lock()
		if l.n--; l.n == 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}

// info is the substrate's view of rec: nil when it has none (yet, or any
// more); an error only when the substrate can't say.
func (m *Manager) info(ctx context.Context, rec record) (*xbin.SandboxInfo, error) {
	if rec.Overlay == "creating" && rec.Plan != nil {
		return nil, nil // the substrate may not have it yet
	}
	in, err := m.backend().Get(ctx, rec.Runtime)
	if errors.Is(err, xbin.ErrSandboxNotFound) {
		return nil, nil
	}
	return in, err
}

// --- list, get ------------------------------------------------------------------------

func (m *Manager) list(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	o, err := m.offer(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return
	}
	infos, err := m.backend().List(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return
	}
	byName := map[string]*xbin.SandboxInfo{}
	for i := range infos {
		byName[infos[i].Name] = &infos[i]
	}
	m.mu.Lock()
	var recs []record
	for _, rec := range m.recs {
		if rec.visible(c) && rec.personOK(c) {
			recs = append(recs, *rec)
		}
	}
	m.mu.Unlock()
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Created < recs[j].Created || recs[i].Created == recs[j].Created && recs[i].ID < recs[j].ID
	})
	out := []sandboxView{}
	for _, rec := range recs {
		out = append(out, m.view(rec, byName[rec.Runtime], c, o.caps))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sandboxes": out})
}

func (m *Manager) get(w http.ResponseWriter, r *http.Request) {
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	m.answer(w, r, http.StatusOK, rec.ID, c, nil)
}

// answer writes sandbox id as c sees it now (in: the substrate's info when
// the caller just got it).
func (m *Manager) answer(w http.ResponseWriter, r *http.Request, status int, id string, c caller, in *xbin.SandboxInfo) {
	o, err := m.offer(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return
	}
	m.mu.Lock()
	p := m.recs[id]
	var rec record
	if p != nil {
		rec = *p
	}
	m.mu.Unlock()
	if p == nil {
		fail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return
	}
	if in == nil {
		if in, err = m.info(r.Context(), rec); err != nil {
			writeErr(w, err, &rec)
			return
		}
	}
	writeJSON(w, status, m.view(rec, in, c, o.caps))
}

// --- create ------------------------------------------------------------------------------

type fromReq struct {
	Sandbox  string `json:"sandbox"`
	Snapshot string `json:"snapshot"`
}

type createReq struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Size       string            `json:"size"`
	Egress     string            `json:"egress"`
	Visibility string            `json:"visibility"`
	Members    []string          `json:"members"`
	Labels     map[string]string `json:"labels"`
	ClientID   string            `json:"clientId"`
	Start      *bool             `json:"start"`
	From       *fromReq          `json:"from"`
}

// checkName trims and checks a sandbox's name.
func checkName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= 64
}

func labelsTooLarge(l map[string]string) bool {
	b, _ := json.Marshal(l)
	return len(b) > 1024
}

// createJob is a creation under way.
type createJob struct {
	done   chan struct{}
	err    error
	cancel context.CancelFunc
}

func (m *Manager) create(w http.ResponseWriter, r *http.Request) {
	var q createReq
	if err := decode(r, 64<<10, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	c := callerOf(r)
	o, err := m.offer(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return
	}
	name, okName := checkName(q.Name)
	q.Name = name
	im, okImage := o.image(q.Image)
	sz, okSize := o.size(q.Size)
	switch {
	case !okName:
		fail(w, http.StatusBadRequest, "invalid", "name is 1–64 characters")
		return
	case q.Image != "" && !okImage:
		fail(w, http.StatusBadRequest, "invalid", "no image "+quote(q.Image)+" here (hello lists them)")
		return
	case q.Size != "" && !okSize:
		fail(w, http.StatusBadRequest, "invalid", "no size "+quote(q.Size)+" here (hello lists them)")
		return
	case q.Egress != "" && !contains(o.egress, q.Egress):
		fail(w, http.StatusBadRequest, "invalid", "egress "+quote(q.Egress)+" isn't offered here: "+strings.Join(o.egress, ", "))
		return
	case q.Visibility != "" && q.Visibility != "private" && q.Visibility != "team":
		fail(w, http.StatusBadRequest, "invalid", "visibility is private or team")
		return
	case labelsTooLarge(q.Labels):
		fail(w, http.StatusRequestEntityTooLarge, "too-large", "labels are 1 KiB at most")
		return
	case q.From != nil && !contains(o.caps, "clone"):
		fail(w, http.StatusNotImplemented, "unsupported", "this manager makes no clones")
		return
	case len(q.ClientID) > 128:
		fail(w, http.StatusBadRequest, "invalid", "clientId is 128 characters at most")
		return
	}
	for _, u := range q.Members {
		if strings.TrimSpace(u) == "" {
			fail(w, http.StatusBadRequest, "invalid", "a member is a user id")
			return
		}
	}
	if q.ClientID != "" { // one create per clientId at a time
		defer m.lock("create\x00" + c.from + "\x00" + q.ClientID)()
	}
	ikey, h := "create\x00"+c.from+"\x00"+q.ClientID, hashOf(q)
	if q.ClientID != "" {
		target, prev, found, err := m.st.idem(ikey)
		if err != nil {
			writeErr(w, err, nil)
			return
		}
		if found && prev != h {
			fail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different sandbox")
			return
		}
		m.mu.Lock()
		existing := m.recs[target]
		m.mu.Unlock()
		if found && existing != nil {
			m.answer(w, r, http.StatusOK, target, c, nil)
			return
		}
	}
	mode := m.chooseModeOr(o.rt, "")
	if mode == "" {
		writeErr(w, errf(http.StatusServiceUnavailable, "unavailable", "the substrate runs no sandboxes now: %s", unavailableWhy(o.rt)), nil)
		return
	}
	start := q.Start == nil || *q.Start
	plan := &createPlan{Start: start, AutoStopMin: m.config().AutoStopMin}
	image, size := im.ID, sz
	if q.From != nil { // a clone: of a sandbox the caller may use, now or at a snapshot
		m.mu.Lock()
		src := m.recs[q.From.Sandbox]
		var s record
		if src != nil {
			s = *src
		}
		m.mu.Unlock()
		if src == nil || !s.visible(c) || !s.personOK(c) || s.Overlay != "" {
			fail(w, http.StatusNotFound, "not-found", "no such sandbox to clone")
			return
		}
		if q.From.Snapshot != "" {
			snaps, err := m.backend().Sandbox(s.Runtime).Snapshots(r.Context())
			if err != nil {
				writeErr(w, err, &s)
				return
			}
			if !hasSnapshot(snaps, q.From.Snapshot) {
				fail(w, http.StatusNotFound, "not-found", "no such snapshot")
				return
			}
		}
		plan.FromRuntime, plan.FromSnap = s.Runtime, q.From.Snapshot
		image = s.Image // a clone is of its source's image, whatever the body says
		if q.Size == "" {
			if x, ok := m.config().size(s.Size); ok {
				size = x
			}
		}
	} else if im.Setup != "" {
		plan.Build = !m.imageReady(im, mode)
	}
	plan.Size = sizeSpec{size.MemMiB, size.VCPUs, size.DiskGiB}
	lay := m.layout(o.rt)
	rec := &record{ID: "sb-" + randHex(5), Runtime: "s" + randHex(6), Name: q.Name, Image: image, Size: size.ID,
		Egress: orStr(q.Egress, "none"), Owner: owner{User: c.user, Via: c.from, Asserted: !c.verified && c.user != ""},
		Visibility: orStr(q.Visibility, "private"), Members: q.Members, Labels: q.Labels,
		Workdir: lay.Workdir, Home: lay.Home, User: lay.User, UID: lay.UID, GID: lay.GID, Shell: lay.Shell,
		Created: now(), Version: 1, Overlay: "creating", Plan: plan}
	if rec.Members == nil {
		rec.Members = []string{}
	}
	if plan.Build {
		rec.Detail = "building the image " + im.ID + " (its first use)"
	}
	var running map[string]bool
	if start {
		if running, err = m.runningSet(r.Context()); err != nil {
			writeErr(w, err, nil)
			return
		}
	}
	m.mu.Lock()
	if err := m.quotaCheck(o.rt, c.from, c.user, plan.Size, quotaNeed{count: true, disk: true, run: start}, running, ""); err != nil {
		m.mu.Unlock()
		writeErr(w, err, nil)
		return
	}
	if err := m.st.putRecord(rec); err != nil {
		m.mu.Unlock()
		writeErr(w, err, nil)
		return
	}
	m.recs[rec.ID] = rec
	m.starting[rec.ID] = start
	id, made := rec.ID, *rec
	job := m.launchCreate(id)
	m.mu.Unlock()
	if q.ClientID != "" {
		if err := m.st.putIdem(ikey, id, h); err != nil {
			m.logf("clientId %s: %v", q.ClientID, err)
		}
	}
	// Wait for the creation — all of it, unless an image builds first: then
	// at most ?wait (the answer says "creating", and the consumer polls).
	var timeout <-chan time.Time
	if plan.Build {
		t := time.NewTimer(m.waitParam(r))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-job.done:
		if job.err != nil && !plan.Build {
			writeErr(w, job.err, &made)
			return
		}
	case <-timeout:
	case <-r.Context().Done():
		return // the creation goes on; a repeat with the clientId finds it
	}
	m.answer(w, r, http.StatusCreated, id, c, nil)
}

func hasSnapshot(snaps []xbin.Snapshot, id string) bool {
	for _, s := range snaps {
		if s.ID == id {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// waitParam is ?wait=<seconds>, at most limits.waitMaxSec (0 when absent).
func (m *Manager) waitParam(r *http.Request) time.Duration {
	s := r.URL.Query().Get("wait")
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return min(time.Duration(f*float64(time.Second)), m.waitMax(r.Context()))
}

// layout is where people work in a new sandbox: the configured one, or root
// at /root on a substrate that runs everything as root.
func (m *Manager) layout(rt *xbin.SandboxRuntime) Layout {
	l := m.config().Layout
	if rt != nil && rt.Users == "root" {
		l.User, l.UID, l.GID, l.Home = "root", 0, 0, "/root"
	}
	return l
}

// chooseModeOr is the mode a new sandbox runs in: the configured one, else
// a VM where the substrate offers one, else a namespace ("" when neither).
func (m *Manager) chooseModeOr(rt *xbin.SandboxRuntime, def string) string {
	if want := m.config().Mode; want != "" {
		return want
	}
	has := func(mode string) bool {
		for _, x := range rt.Modes {
			if x.Mode == mode {
				return true
			}
		}
		return false
	}
	switch {
	case has("vm"):
		return "vm"
	case has("namespace"):
		return "namespace"
	}
	return def
}

// defaults is what every command in rec's sandbox gets.
func defaultsOf(rec record) *xbin.SandboxDefaults {
	uid, gid := rec.UID, rec.GID
	return &xbin.SandboxDefaults{Cwd: rec.Workdir, UID: &uid, GID: &gid, Shell: rec.Shell,
		Env: map[string]string{"HOME": rec.Home, "USER": rec.User, "LOGNAME": rec.User,
			"IN_SANDBOX": "1", "SANDBOX_ID": rec.ID, "SANDBOX_NAME": rec.Name}}
}

// launchCreate starts making sandbox id in the background (m.mu held).
func (m *Manager) launchCreate(id string) *createJob {
	ctx, cancel := context.WithCancel(m.ctx)
	job := &createJob{done: make(chan struct{}), cancel: cancel}
	m.jobsSet(id, job)
	m.goBG(func(context.Context) {
		defer cancel()
		job.err = m.makeSandbox(ctx, id)
		m.mu.Lock()
		m.jobsDel(id, job)
		m.mu.Unlock()
		close(job.done)
	})
	return job
}

func (m *Manager) jobsSet(id string, j *createJob) { m.creates[id] = j }

func (m *Manager) jobsDel(id string, j *createJob) {
	if m.creates[id] == j {
		delete(m.creates, id)
	}
}

// makeSandbox makes sandbox id by its plan: an image's clone (building the
// image first), a clone of a sandbox, or the plain base; then starts and
// prepares it. A plain or cloned sandbox that fails is forgotten (the
// create answers the error); one waiting on an image build stays, in
// state error, since its create has answered already.
func (m *Manager) makeSandbox(ctx context.Context, id string) (err error) {
	m.mu.Lock()
	p := m.recs[id]
	if p == nil || p.Plan == nil {
		m.mu.Unlock()
		return nil
	}
	rec := *p
	plan := *rec.Plan
	cfg := m.cfg
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		m.mu.Unlock()
		if err == nil {
			return
		}
		if ctx.Err() != nil && m.ctx.Err() != nil {
			return // the manager is stopping: the next one resumes it
		}
		m.logf("sandbox %s (%s): %v", id, rec.Runtime, err)
		dctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = m.backend().Delete(dctx, rec.Runtime) // what was made of it
		m.mu.Lock()
		defer m.mu.Unlock()
		if cur := m.recs[id]; cur == nil || cur.Overlay != "creating" {
			return // deleted meanwhile
		}
		if plan.Build {
			_, _ = m.update(id, func(r *record) { r.Overlay, r.Detail, r.Plan = "error", errText(err), nil })
			return
		}
		m.forget(id)
	}()
	rt, err := m.runtime(ctx)
	if err != nil {
		return err
	}
	be := m.backend()
	mode := m.chooseModeOr(rt, "")
	if mode == "" {
		return errf(http.StatusServiceUnavailable, "unavailable", "the substrate runs no sandboxes now: %s", unavailableWhy(rt))
	}
	spec := xbin.SandboxSpec{Name: rec.Runtime, Mode: mode, MemMiB: plan.Size.MemMiB, VCPUs: plan.Size.VCPUs, DiskGiB: plan.Size.DiskGiB,
		Net: &xbin.SandboxNet{Egress: egressClass(rec.Egress)}, Defaults: defaultsOf(rec),
		Labels: map[string]string{"coding-sandbox/id": rec.ID}, For: rec.Owner.Via, ForUser: rec.Owner.User,
		IdleStopMin: plan.AutoStopMin, ClientID: rec.Runtime}
	switch {
	case plan.FromRuntime != "":
		spec.From = &xbin.SandboxFrom{Sandbox: plan.FromRuntime, Snapshot: plan.FromSnap}
	default:
		if im, ok := cfg.image(rec.Image); ok && im.Setup != "" {
			built, err := m.imageFor(ctx, im, mode)
			if err != nil {
				return errf(http.StatusServiceUnavailable, "unavailable", "the image %s didn't build: %s", im.ID, errText(err))
			}
			spec.From = &xbin.SandboxFrom{Sandbox: built.Runtime, Snapshot: built.Snapshot}
		}
	}
	info, err := be.Create(ctx, spec)
	if err != nil {
		return err
	}
	lay := info.Defaults // where the substrate put them
	m.mu.Lock()
	_, err = m.update(id, func(r *record) {
		if lay.Cwd != "" {
			r.Workdir = lay.Cwd
		}
		if h := lay.Env["HOME"]; h != "" {
			r.Home = h
		}
		if lay.Shell != "" {
			r.Shell = lay.Shell
		}
		r.Prepared = spec.From != nil && plan.FromRuntime == "" // an image's clone was prepared when it was built
	})
	rec = *m.recs[id]
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if plan.Start {
		if _, err := be.Start(ctx, rec.Runtime, m.waitMax(ctx)); err != nil {
			return err
		}
		if err := m.prepare(ctx, rec); err != nil {
			return err
		}
		rec.Prepared = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.recs[id]; cur == nil || cur.Overlay != "creating" {
		return errors.New("deleted while it was made")
	}
	_, err = m.update(id, func(r *record) { r.Overlay, r.Detail, r.Plan, r.Prepared = "", "", nil, rec.Prepared })
	if plan.Start {
		m.live[id] = time.Now()
	}
	return err
}

func unavailableWhy(rt *xbin.SandboxRuntime) string {
	var out []string
	for _, u := range rt.Unavailable {
		out = append(out, u.Mode+": "+u.Reason)
	}
	return joinOr(out, "no modes")
}

func errText(err error) string {
	var se *xbin.SandboxError
	if errors.As(err, &se) {
		return se.Message
	}
	return err.Error()
}

// forget drops a record and what names it (m.mu held).
func (m *Manager) forget(id string) {
	if err := m.st.deleteRecord(id); err != nil {
		m.logf("forget %s: %v", id, err)
	}
	delete(m.recs, id)
	delete(m.live, id)
	delete(m.starting, id)
	for k := range m.execIdem {
		if strings.Contains(k, "\x00"+id+"\x00") {
			delete(m.execIdem, k)
		}
	}
}

// prepare makes the workdir and home (as the sandbox's user: the runtime
// owns what it creates to defaults.uid).
func (m *Manager) prepare(ctx context.Context, rec record) error {
	box := m.backend().Sandbox(rec.Runtime)
	for _, p := range []string{rec.Workdir, rec.Home} {
		if err := box.Mkdir(ctx, p, true); err != nil {
			return err
		}
	}
	return nil
}

// ready makes rec usable for a command or a file operation: started (within
// the quotas) and prepared. A sandbox seen running within LiveTTL is taken
// to be; one the substrate stopped since starts on the operation itself.
func (m *Manager) ready(ctx context.Context, rec *record) error {
	if rec.Overlay != "" {
		return &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: rec.Overlay,
			Message: "the sandbox is " + rec.Overlay + orStr(prefixed(": ", rec.Detail), "")}
	}
	ttl := m.LiveTTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	m.mu.Lock()
	fresh := rec.Prepared && time.Since(m.live[rec.ID]) < ttl
	m.mu.Unlock()
	if fresh {
		return nil
	}
	in, err := m.backend().Get(ctx, rec.Runtime)
	if err != nil {
		return err
	}
	switch in.State {
	case "running":
	case "stopped", "starting":
		if _, err := m.startBox(ctx, *rec, 0, false); err != nil {
			return err
		}
	default:
		return &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: in.State, Message: "the sandbox is " + in.State}
	}
	if !rec.Prepared {
		if err := m.prepare(ctx, *rec); err != nil {
			return err
		}
		m.mu.Lock()
		if _, err := m.update(rec.ID, func(r *record) { r.Prepared = true }); err == nil {
			rec.Prepared = true
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.live[rec.ID] = time.Now()
	m.mu.Unlock()
	return nil
}

func prefixed(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// startBox starts rec within the quotas (its lifecycle lock taken unless
// locked): wait is how long to wait for it (0 on a command's auto-start:
// limits.waitMaxSec).
func (m *Manager) startBox(ctx context.Context, rec record, wait time.Duration, locked bool) (*xbin.SandboxInfo, error) {
	if !locked {
		defer m.lock(rec.ID)()
	}
	o, err := m.offer(ctx)
	if err != nil {
		return nil, err
	}
	running, err := m.runningSet(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if running[rec.Runtime] { // up already (another request started it)
		m.mu.Unlock()
		return m.backend().Get(ctx, rec.Runtime)
	}
	if err := m.quotaCheck(o.rt, rec.Owner.Via, rec.Owner.User, m.sizeOf(&rec), quotaNeed{run: true}, running, rec.ID); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.starting[rec.ID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, rec.ID)
		m.mu.Unlock()
	}()
	if wait <= 0 {
		wait = m.waitMax(ctx)
	}
	in, err := m.backend().Start(ctx, rec.Runtime, wait)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	_, _ = m.update(rec.ID, func(r *record) { r.Version++ })
	if in.State == "running" {
		m.live[rec.ID] = time.Now()
	}
	m.mu.Unlock()
	return in, nil
}

// --- PATCH -------------------------------------------------------------------------------

type patchReq struct {
	Name        *string            `json:"name"`
	Visibility  *string            `json:"visibility"`
	Members     *[]string          `json:"members"`
	Shares      *[]share           `json:"shares"`
	Labels      *map[string]string `json:"labels"`
	Egress      *string            `json:"egress"`
	Size        *string            `json:"size"`
	AutoStopMin *int               `json:"autoStopMin"`
	Version     *int               `json:"version"`
}

func (m *Manager) patch(w http.ResponseWriter, r *http.Request) {
	var q patchReq
	if err := decode(r, 64<<10, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	if (q.Visibility != nil || q.Members != nil || q.Shares != nil) && !rec.canAdmin(c) {
		fail(w, http.StatusForbidden, "not-allowed", "only its home consumer or its owner changes who may use it")
		return
	}
	in, err := m.patchSandbox(r.Context(), rec.ID, q)
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	m.answer(w, r, http.StatusOK, rec.ID, c, in)
}

// patchSandbox applies q to sandbox id — every field checked before any
// applies, so a refused PATCH changes nothing. Who may is the caller's
// check (the contract's, or the operators').
func (m *Manager) patchSandbox(ctx context.Context, id string, q patchReq) (*xbin.SandboxInfo, error) {
	defer m.lock(id)()
	o, err := m.offer(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	p := m.recs[id]
	if p == nil {
		m.mu.Unlock()
		return nil, errf(http.StatusNotFound, "not-found", "no such sandbox")
	}
	rec := *p
	m.mu.Unlock()
	if q.Version != nil && *q.Version != rec.Version {
		return nil, errf(http.StatusPreconditionFailed, "precondition", "the sandbox changed (version %d, not %d)", rec.Version, *q.Version)
	}
	var name string
	if q.Name != nil {
		var okName bool
		if name, okName = checkName(*q.Name); !okName {
			return nil, errf(http.StatusBadRequest, "invalid", "name is 1–64 characters")
		}
	}
	var size Size
	switch {
	case q.Visibility != nil && *q.Visibility != "private" && *q.Visibility != "team":
		return nil, errf(http.StatusBadRequest, "invalid", "visibility is private or team")
	case q.Egress != nil && !contains(o.egress, *q.Egress):
		return nil, errf(http.StatusBadRequest, "invalid", "egress %s isn't offered here: %s", quote(*q.Egress), strings.Join(o.egress, ", "))
	case q.AutoStopMin != nil && (*q.AutoStopMin < 0 || *q.AutoStopMin > 1440):
		return nil, errf(http.StatusBadRequest, "invalid", "autoStopMin is 0–1440")
	case q.Labels != nil && labelsTooLarge(*q.Labels):
		return nil, errf(http.StatusRequestEntityTooLarge, "too-large", "labels are 1 KiB at most")
	}
	if q.Size != nil {
		var okSize bool
		if size, okSize = o.size(*q.Size); !okSize || *q.Size == "" {
			return nil, errf(http.StatusBadRequest, "invalid", "no size %s here (hello lists them)", quote(*q.Size))
		}
	}
	if q.Shares != nil {
		for _, s := range *q.Shares {
			if s.Consumer == "" {
				return nil, errf(http.StatusBadRequest, "invalid", "a share names its consumer")
			}
		}
	}
	if q.Members != nil {
		for _, u := range *q.Members {
			if strings.TrimSpace(u) == "" {
				return nil, errf(http.StatusBadRequest, "invalid", "a member is a user id")
			}
		}
	}
	var bp xbin.SandboxPatch
	touch := false
	if q.Egress != nil {
		bp.Net, touch = &xbin.SandboxNet{Egress: egressClass(*q.Egress)}, true
	}
	if q.Size != nil && size.ID != rec.Size {
		mem, cpu, disk := size.MemMiB, size.VCPUs, size.DiskGiB
		bp.MemMiB, bp.VCPUs, bp.DiskGiB, touch = &mem, &cpu, &disk, true
	}
	if q.AutoStopMin != nil {
		bp.IdleStopMin, touch = q.AutoStopMin, true
	}
	if q.Name != nil && name != rec.Name {
		next := rec
		next.Name = name
		bp.Defaults, touch = defaultsOf(next), true // SANDBOX_NAME
	}
	if touch && rec.Overlay != "" {
		return nil, &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: rec.Overlay, Message: "the sandbox is " + rec.Overlay}
	}
	if q.Size != nil && size.ID != rec.Size {
		m.mu.Lock()
		err := m.quotaCheck(o.rt, rec.Owner.Via, rec.Owner.User, sizeSpec{size.MemMiB, size.VCPUs, size.DiskGiB}, quotaNeed{disk: true}, nil, rec.ID)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	var in *xbin.SandboxInfo
	if touch {
		if in, err = m.backend().Patch(ctx, rec.Runtime, bp); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err = m.update(id, func(r *record) {
		if q.Name != nil {
			r.Name = name
		}
		if q.Visibility != nil {
			r.Visibility = *q.Visibility
		}
		if q.Members != nil {
			r.Members = append([]string{}, *q.Members...)
		}
		if q.Shares != nil {
			r.Shares = append([]share{}, *q.Shares...)
		}
		if q.Labels != nil {
			r.Labels = *q.Labels
		}
		if q.Egress != nil {
			r.Egress = *q.Egress
		}
		if q.Size != nil {
			r.Size = size.ID
		}
		r.Version++
	})
	return in, err
}

// --- DELETE ------------------------------------------------------------------------------

func (m *Manager) del(w http.ResponseWriter, r *http.Request) {
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	if rec.Owner.Via != c.from {
		fail(w, http.StatusForbidden, "not-allowed", "only its home consumer deletes a sandbox")
		return
	}
	if err := m.deleteSandbox(r.Context(), rec.ID); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteSandbox stops a creation under way, deletes the substrate's
// sandbox and forgets the record.
func (m *Manager) deleteSandbox(ctx context.Context, id string) error {
	m.mu.Lock()
	job := m.creates[id]
	m.mu.Unlock()
	if job != nil {
		job.cancel()
		<-job.done
	}
	defer m.lock(id)()
	m.mu.Lock()
	p := m.recs[id]
	if p == nil {
		m.mu.Unlock()
		if job != nil {
			return nil // its creation failed (and forgot it) as it was stopped
		}
		return errf(http.StatusNotFound, "not-found", "no such sandbox")
	}
	was := p.Overlay
	rec, err := m.update(id, func(r *record) { r.Overlay, r.Detail, r.Plan = "deleting", "", nil })
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := m.backend().Delete(ctx, rec.Runtime); err != nil && !errors.Is(err, xbin.ErrSandboxNotFound) {
		m.mu.Lock()
		_, _ = m.update(id, func(r *record) { r.Overlay = was })
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.forget(id)
	m.mu.Unlock()
	return nil
}

// --- lifecycle ----------------------------------------------------------------------------

func (m *Manager) action(w http.ResponseWriter, r *http.Request) {
	act := r.PathValue("action")
	switch act {
	case "start", "stop":
	case "archive", "thaw":
		fail(w, http.StatusNotImplemented, "unsupported", "this manager keeps no archives (yet)")
		return
	default:
		fail(w, http.StatusNotFound, "not-found", "no action "+act)
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	in, err := m.lifecycle(r.Context(), rec.ID, act, m.waitParam(r))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	m.answer(w, r, http.StatusOK, rec.ID, c, in)
}

// lifecycle starts or stops sandbox id.
func (m *Manager) lifecycle(ctx context.Context, id, act string, wait time.Duration) (*xbin.SandboxInfo, error) {
	unlock := m.lock(id)
	defer unlock()
	m.mu.Lock()
	p := m.recs[id]
	var rec record
	if p != nil {
		rec = *p
	}
	m.mu.Unlock()
	switch {
	case p == nil:
		return nil, errf(http.StatusNotFound, "not-found", "no such sandbox")
	case rec.Overlay != "":
		return nil, &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: rec.Overlay,
			Message: "a " + rec.Overlay + " sandbox can't " + act}
	}
	if act == "start" {
		in, err := m.startBox(ctx, rec, wait, true)
		if err != nil || in.State != "running" || rec.Prepared {
			return in, err
		}
		if err := m.prepare(ctx, rec); err != nil {
			return nil, err
		}
		m.mu.Lock()
		_, _ = m.update(id, func(r *record) { r.Prepared = true })
		m.mu.Unlock()
		return in, nil
	}
	in, err := m.backend().Stop(ctx, rec.Runtime, wait)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.live, id)
	_, _ = m.update(id, func(r *record) { r.Version++ })
	m.mu.Unlock()
	return in, nil
}

// resume finishes what a previous run left half done: creations under way
// start again (their spec's clientId makes that safe), deletions finish.
func (m *Manager) resume() {
	m.mu.Lock()
	var creating, deleting []string
	for id, r := range m.recs {
		switch {
		case r.Overlay == "creating" && r.Plan != nil:
			creating = append(creating, id)
		case r.Overlay == "deleting":
			deleting = append(deleting, id)
		}
	}
	for _, id := range creating {
		m.starting[id] = m.recs[id].Plan.Start
		m.launchCreate(id)
	}
	for id, im := range m.imgs {
		if im.State == "building" {
			next := *im
			next.State, next.Detail, next.Started = "error", "the build was interrupted (the manager restarted); the next sandbox of the image builds it again", 0
			if err := m.st.putImage(&next); err == nil {
				m.imgs[id] = &next
			}
		}
	}
	m.mu.Unlock()
	for _, id := range deleting {
		m.goBG(func(ctx context.Context) {
			if err := m.deleteSandbox(ctx, id); err != nil {
				m.logf("resume: delete %s: %v", id, err)
			}
		})
	}
}
