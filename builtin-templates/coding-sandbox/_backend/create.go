// create.go — the contract's create: a plain sandbox of the substrate's
// base, an image's clone (the image built first when it must be), or a
// clone of a sandbox the caller may use — made in the background, answered
// when done (or, while an image builds, after ?wait with state creating).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	xbin "github.com/xbin-dev/xbin/sdk"
)

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
	if c.refused != "" { // never made as the consumer's non-personal identity
		fail(w, http.StatusForbidden, "not-allowed", c.refused)
		return
	}
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
		defer m.lock("create\x00" + c.key() + "\x00" + q.ClientID)()
	}
	ikey, h := "create\x00"+c.key()+"\x00"+q.ClientID, hashOf(q) // per consumer (and user partition)
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
	mode, err := m.chooseMode(o.rt)
	if err != nil {
		writeErr(w, err, nil)
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
		plan.FromRuntime, plan.FromID, plan.FromSnap = s.Runtime, s.ID, q.From.Snapshot
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
		Egress: orStr(q.Egress, "none"), Owner: owner{User: c.user, Via: c.from, PartitionID: c.partID, Partition: c.part, Asserted: !c.verified && c.user != ""},
		Visibility: orStr(q.Visibility, "private"), Members: q.Members, Labels: q.Labels,
		Workdir: lay.Workdir, Home: lay.Home, User: lay.User, UID: lay.UID, GID: lay.GID, Shell: lay.Shell,
		Created: now(), Version: 1, Overlay: "creating", Plan: plan, Mode: mode}
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
	if q.ClientID != "" { // before the creation can fail and forget it (and its clientId)
		if err := m.st.putIdem(ikey, rec.ID, h); err != nil {
			m.logf("clientId %s: %v", q.ClientID, err)
		}
	}
	m.recs[rec.ID] = rec
	m.starting[rec.ID] = start
	id, made := rec.ID, *rec
	job := m.launchCreate(id)
	m.mu.Unlock()
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

// chooseMode is the mode a new sandbox runs in: the operators' choice
// (config.mode) while the substrate offers it, or — auto — a VM where it
// offers VMs now, else a namespace, else the substrate's first (another
// backend's: a cloud's cloud-vm). It is never another mode than the
// operators chose: without it, no sandbox is made (503, saying why), and
// the sandbox's `isolation` always says the mode it got.
func (m *Manager) chooseMode(rt *xbin.SandboxRuntime) (string, error) {
	cfg := m.config()
	has := func(mode string) bool {
		for _, x := range rt.Modes {
			if x.Mode == mode {
				return true
			}
		}
		return false
	}
	if !cfg.autoMode() {
		if has(cfg.Mode) {
			return cfg.Mode, nil
		}
		why := "it doesn't offer them"
		for _, u := range rt.Unavailable {
			if u.Mode == cfg.Mode && u.Reason != "" {
				why = u.Reason
			}
		}
		return "", errf(http.StatusServiceUnavailable, "unavailable",
			"this manager makes %s sandboxes (its operators' mode), and the substrate has none now: %s", cfg.Mode, why)
	}
	switch {
	case has("vm"):
		return "vm", nil
	case has("namespace"):
		return "namespace", nil
	case len(rt.Modes) > 0: // another substrate's own (a cloud's cloud-vm, say)
		return rt.Modes[0].Mode, nil
	}
	return "", errf(http.StatusServiceUnavailable, "unavailable", "the substrate runs no sandboxes now: %s", unavailableWhy(rt))
}

// runtimeLabels are the labels the substrate keeps with rec's sandbox
// (metadata, never the consumer's own labels; the orphans list shows them —
// the admin's sandbox registry doesn't): its contract id and, for one homed
// in a user partition, that partition's id (For stays the consumer tile).
func runtimeLabels(rec record) map[string]string {
	l := map[string]string{"coding-sandbox/id": rec.ID}
	if rec.Owner.PartitionID != "" {
		l["coding-sandbox/partition"] = rec.Owner.PartitionID
	}
	return l
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
	// srcName is the substrate's sandbox this one is made from, which an
	// error says as srcLabel: a clone's source by its contract id, an image's
	// template sandbox as image:<id>. A consumer never sees a runtime name.
	var srcName, srcLabel string
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		m.mu.Unlock()
		if err == nil {
			return
		}
		err = m.hideImageNames(hideName(err, srcName, srcLabel), rec.Image)
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
	mode := rec.Mode
	if mode == "" { // a record from before the mode was kept
		if mode, err = m.chooseMode(rt); err != nil {
			return err
		}
	}
	spec := xbin.SandboxSpec{Name: rec.Runtime, Mode: mode, MemMiB: plan.Size.MemMiB, VCPUs: plan.Size.VCPUs, DiskGiB: plan.Size.DiskGiB,
		Net: &xbin.SandboxNet{Egress: egressClass(rec.Egress)}, Defaults: defaultsOf(rec),
		Labels: runtimeLabels(rec), For: rec.Owner.Via, ForUser: rec.Owner.User,
		IdleStopMin: plan.AutoStopMin, ClientID: rec.Runtime, Mounts: cfg.sandboxMounts()}
	switch {
	case plan.FromRuntime != "":
		spec.From = &xbin.SandboxFrom{Sandbox: plan.FromRuntime, Snapshot: plan.FromSnap}
		srcName, srcLabel = plan.FromRuntime, m.sourceID(plan)
	default:
		if im, ok := cfg.image(rec.Image); ok && im.Setup != "" {
			built, err := m.imageFor(ctx, im, mode)
			if err != nil {
				return errf(http.StatusServiceUnavailable, "unavailable", "the image %s didn't build: %s", im.ID, errText(err))
			}
			spec.From = &xbin.SandboxFrom{Sandbox: built.Runtime, Snapshot: built.Snapshot}
			srcName, srcLabel = built.Runtime, "image:"+im.ID
		}
	}
	info, err := m.createOn(ctx, be, spec)
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
		if lay.UID != nil && lay.GID != nil {
			r.UID, r.GID = *lay.UID, *lay.GID
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

// createOn makes spec on be, waiting out a clone's copy. A clone of a
// sandbox as it is now (no snapshot named) where the substrate copies only a
// stopped one — xbind's runtime answers a running one 409 state — is made
// of a snapshot taken for it, which goes once the clone is made: the
// contract's clone of "the sandbox now" (a snapshot may stop it briefly).
func (m *Manager) createOn(ctx context.Context, be Backend, spec xbin.SandboxSpec) (*xbin.SandboxInfo, error) {
	info, err := be.Create(ctx, spec)
	var se *xbin.SandboxError
	if err != nil && spec.From != nil && spec.From.Snapshot == "" && errors.As(err, &se) && se.Refusal == "state" && se.State != "stopped" {
		box := be.Sandbox(spec.From.Sandbox)
		snap, serr := box.Snapshot(ctx, "clone "+spec.Name, "clone-"+spec.Name) // repeat-safe: a resumed creation takes the same one
		if serr == nil && snap.Pending {
			snap, serr = settleSnapshot(ctx, box, snap)
		}
		if serr != nil {
			return nil, serr
		}
		defer func() {
			dctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := box.DeleteSnapshot(dctx, snap.ID); err != nil {
				m.logf("clone %s: removing the snapshot %s it was made of: %v", spec.Name, snap.ID, err)
			}
		}()
		from := *spec.From
		from.Snapshot = snap.ID
		spec.From = &from
		info, err = be.Create(ctx, spec)
	}
	if err == nil {
		info, err = settleCreated(ctx, be, info) // a clone's copy may run past the substrate's wait
	}
	return info, err
}

// sourceID is a clone's source as a consumer knows it: its contract id (a
// plan from before FromID was kept: the record that has the runtime name).
func (m *Manager) sourceID(plan createPlan) string {
	if plan.FromID != "" {
		return plan.FromID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recs {
		if r.Runtime == plan.FromRuntime {
			return r.ID
		}
	}
	return "its source sandbox"
}

// hideImageNames is err with the template sandboxes of image id (its build,
// the previous one it keeps) said as image:<id>.
func (m *Manager) hideImageNames(err error, id string) error {
	m.mu.Lock()
	b := m.imgs[id]
	var names []string
	if b != nil {
		names = b.runtimes()
	}
	m.mu.Unlock()
	for _, n := range names {
		err = hideName(err, n, "image:"+id)
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
