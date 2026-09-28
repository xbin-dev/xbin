// sandboxes.go — the contract's routes and its sandboxes: finding one for
// a caller, list, get, PATCH and DELETE (create.go makes them, lifecycle.go
// starts, stops and prepares them).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

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
		if m.pageReader(r) { // this tile's own page, a person who may only look
			if mutates(r) {
				c := xbin.Caller(r)
				fail(w, http.StatusForbidden, "not-allowed", fmt.Sprintf("%s has %s access to %s: that lets them look, not change — changing a sandbox here needs write access",
					c.User, orStr(c.UserLevel, "no"), m.self))
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), lookOnlyKey{}, true))
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
