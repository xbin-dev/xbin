// lifecycle.go — start and stop, what makes a sandbox usable (started
// within the quotas, its workdir and home made), and what a restarted
// manager finishes.
package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// prepare makes the workdir and home, owned by the sandbox's user: a run as
// root (mkdir, chown), which holds on any substrate.
func (m *Manager) prepare(ctx context.Context, rec record) error {
	root := 0
	owner := strconv.Itoa(rec.UID) + ":" + strconv.Itoa(rec.GID)
	res, err := m.backend().Sandbox(rec.Runtime).Run(ctx, xbin.RunRequest{Argv: []string{"sh", "-c", prepareScript, "prepare",
		rec.Workdir, rec.Home, owner}, Cwd: "/", UID: &root, GID: &root, TimeoutMs: 60000, Merge: true})
	if err != nil {
		return err
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		out := ""
		if res.Output != nil {
			out = strings.TrimSpace(res.Output.Head + res.Output.Tail)
		}
		return errf(http.StatusServiceUnavailable, "unavailable", "making the workdir and home failed: %s", orStr(out, "exit "+res.Signal))
	}
	return nil
}

// prepareScript makes the workdir ($1) and home ($2), owned by the user
// ($3, uid:gid) — as root, so it holds on any substrate.
const prepareScript = `set -e
for d in "$1" "$2"; do
	mkdir -p -- "$d"
	[ "$(stat -c %u:%g -- "$d")" = "$3" ] || chown -- "$3" "$d"
done`

// ready makes rec usable for a command or a file operation: started (within
// the quotas) and prepared. A sandbox seen running within LiveTTL is taken
// to be; one the substrate stopped since starts on the operation itself —
// unless !start (a person who may only look): then a stopped one is refused.
func (m *Manager) ready(ctx context.Context, rec *record, start bool) error {
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
		if !start && in.State == "stopped" {
			return errf(http.StatusForbidden, "not-allowed", "the sandbox is stopped, and starting it needs write access to this tile")
		}
		if !start {
			return &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: in.State, Message: "the sandbox is starting"}
		}
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
			if next.Previous != nil {
				next.Detail = "the build was interrupted (the manager restarted); the previous build is kept, and a sandbox of the image that needs a new one builds it again"
			}
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
