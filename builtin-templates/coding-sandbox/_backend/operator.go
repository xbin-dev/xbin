// operator.go — the tile's operators' own routes (API.md §Operators): the
// state of every sandbox's metadata, usage by consumer and person, the
// config (backend, images, sizes, quotas, layout), lifecycle and deletion,
// image builds. Operators are the tile's owner and the people with write
// access to it. Nothing here reads or writes a sandbox's contents — no
// commands, no files: those go through a consumer that may use it. A
// sandbox homed in a partitioned consumer's user partition shows neither its
// name, its labels nor its snapshots' names here (opRedact).
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// operator: the caller holds the tile's admin role and, when a person
// drives it, write access to the tile.
func operator(r *http.Request) bool {
	c := xbin.Caller(r)
	return xbin.RoleSatisfies(c.Role, "admin") && c.UserCanWrite() && c.From != "" && c.From != "xbin/cron"
}

func (m *Manager) op(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !operator(r) {
			fail(w, http.StatusForbidden, "not-allowed", "this needs write access to the tile (an operator)")
			return
		}
		if why := partitionCheck(r); why != "" { // as on /sbx/: never read as another identity
			fail(w, http.StatusForbidden, "not-allowed", why)
			return
		}
		h(w, r)
	}
}

// operatorRoutes are the operators' routes, under /ops/ (and /me for anyone
// the tile serves).
func (m *Manager) operatorRoutes(mux *http.ServeMux) {
	// /me: who the page serves — the person, their level on the tile, whether
	// they may change sandboxes here (write access: pageReader), whether they
	// are an operator.
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		writeJSON(w, http.StatusOK, map[string]any{"user": c.User, "level": c.UserLevel, "write": c.UserCanWrite(),
			"operator": operator(r), "self": m.self})
	})
	mux.HandleFunc("GET /ops/state", m.op(m.opState))
	mux.HandleFunc("PUT /ops/config", m.op(m.opConfig))
	mux.HandleFunc("PATCH /ops/sandboxes/{id}", m.op(m.opPatch))
	mux.HandleFunc("POST /ops/sandboxes/{id}/{action}", m.op(m.opAction))
	mux.HandleFunc("DELETE /ops/sandboxes/{id}", m.op(m.opDelete))
	mux.HandleFunc("GET /ops/sandboxes/{id}/snapshots", m.op(m.opSnapshots))
	mux.HandleFunc("POST /ops/sandboxes/{id}/snapshots", m.op(m.opSnapshot))
	mux.HandleFunc("POST /ops/sandboxes/{id}/snapshots/{sid}/restore", m.op(m.opRestore))
	mux.HandleFunc("DELETE /ops/sandboxes/{id}/snapshots/{sid}", m.op(m.opSnapDelete))
	mux.HandleFunc("POST /ops/images/{id}/build", m.op(m.opBuild))
	mux.HandleFunc("DELETE /ops/orphans/{name}", m.op(m.opOrphan))
	m.portProbeRoutes(mux) // the pages' Ports rows (ports.go): operators, and the people who may use a sandbox
}

// opView is a sandbox's metadata as operators see it: the contract's
// resource (as its home consumer sees it) and the substrate's side.
type opView struct {
	sandboxView
	Consumer     string           `json:"consumer"`
	Runtime      string           `json:"runtime"`
	Mode         string           `json:"mode,omitempty"`
	DiskBytes    int64            `json:"diskBytes,omitempty"`
	ExecsRunning int              `json:"execsRunning,omitempty"`
	Base         xbin.SandboxBase `json:"base"`
	Sudo         bool             `json:"sudo,omitempty"` // its user may sudo (its image's, D182)
}

// orphan is a sandbox the substrate has for this tile and the manager
// doesn't know (a creation that crashed before it was written down, say).
type orphan struct {
	Name    string            `json:"name"`
	State   string            `json:"state"`
	Labels  map[string]string `json:"labels"`
	Created int64             `json:"created"`
}

// opState: GET /ops/state.
func (m *Manager) opState(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	m.mu.Lock()
	cfg, beErr := m.cfg, m.beErr
	imgs := []builtImage{}
	for _, im := range m.imgs {
		imgs = append(imgs, *im)
	}
	var recs []record
	for _, rec := range m.recs {
		recs = append(recs, *rec)
	}
	m.mu.Unlock()
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].ID < imgs[j].ID })
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Created < recs[j].Created || recs[i].Created == recs[j].Created && recs[i].ID < recs[j].ID
	})
	be := map[string]any{"name": cfg.backendName(), "registered": backendNames()}
	if beErr != nil {
		be["error"] = beErr.Error()
	}
	out["backend"], out["config"], out["images"], out["self"] = be, cfg, imgs, m.self
	if o, err := m.offer(r.Context()); err != nil {
		out["runtimeError"] = errText(err)
	} else {
		images := []string{}
		for _, im := range o.images {
			images = append(images, im.ID)
		}
		sizes := []string{}
		for _, s := range o.sizes {
			sizes = append(sizes, s.ID)
		}
		out["runtime"] = o.rt
		out["offer"] = map[string]any{"caps": o.caps, "egress": o.egress, "images": images, "sizes": sizes, "notes": o.notes}
	}
	infos, err := m.backend().List(r.Context())
	if err != nil {
		out["listError"] = errText(err)
	}
	byName, running := map[string]*xbin.SandboxInfo{}, map[string]bool{}
	for i := range infos {
		byName[infos[i].Name] = &infos[i]
		if infos[i].State == "running" || infos[i].State == "starting" {
			running[infos[i].Name] = true
		}
	}
	known := map[string]bool{}
	views := []opView{}
	viewer, seq := callerOf(r), map[string]int{} // seq: each user partition's sandboxes so far (recs are oldest first)
	for _, rec := range recs {
		known[rec.Runtime] = true
		in := byName[rec.Runtime]
		v := opView{sandboxView: m.view(rec, in, homeOf(rec), nil), Consumer: rec.Owner.Via, Runtime: rec.Runtime, Sudo: rec.Sudo}
		if in != nil {
			v.Mode, v.DiskBytes, v.ExecsRunning, v.Base = in.Mode, in.DiskBytes, in.ExecsRunning, in.Base
		}
		if rec.Owner.PartitionID != "" {
			k := homeOf(rec).key()
			seq[k]++
			opRedact(&v.sandboxView, rec, seq[k], viewer)
		}
		views = append(views, v)
	}
	for _, im := range imgs {
		for _, name := range im.runtimes() {
			known[name] = true
		}
	}
	orphans := []orphan{}
	if err == nil {
		for _, in := range infos {
			if !known[in.Name] {
				orphans = append(orphans, orphan{Name: in.Name, State: in.State, Labels: in.Labels, Created: in.Created})
			}
		}
	}
	consumers, people := m.usageBy(running)
	out["sandboxes"], out["orphans"] = views, orphans
	out["usage"] = map[string]any{"consumers": consumers, "people": people}
	writeJSON(w, http.StatusOK, out)
}

// opConfig: PUT /ops/config — the fields present replace the stored ones.
// Changing the backend needs no sandboxes and no built images left: they
// live on the old one.
func (m *Manager) opConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	m.mu.Lock()
	cur := m.cfg
	busy := len(m.recs) > 0 || len(m.imgs) > 0
	m.mu.Unlock()
	next, err := cur.merge(body)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	switchBackend := next.backendName() != cur.backendName() || hashOf(next.BackendConfig) != hashOf(cur.BackendConfig)
	if switchBackend && busy {
		fail(w, http.StatusConflict, "state", "the sandboxes and images live on the current backend: delete them before changing it")
		return
	}
	var be Backend
	var beErr error
	if switchBackend {
		be, beErr = openBackend(next.backendName(), BackendEnv{Config: next.BackendConfig})
	}
	if err := m.st.putConfig(next); err != nil {
		writeErr(w, err, nil)
		return
	}
	m.mu.Lock()
	m.cfg = next
	if switchBackend {
		m.be, m.beErr = be, beErr
	}
	m.mu.Unlock()
	m.forgetRuntime()
	m.opState(w, r)
}

// opPatch: PATCH /ops/sandboxes/{id} — the contract's PATCH body without
// who may use the sandbox. Operators run every consumer's sandboxes, but
// visibility, members and shares change only through the sandbox's home
// consumer (its backend, or its verified owner there): an operator must
// not reach into another partition to change who uses a sandbox. They
// change their own sandboxes' sharing where they own them, on /sbx/.
func (m *Manager) opPatch(w http.ResponseWriter, r *http.Request) {
	var q patchReq
	if err := decode(r, 64<<10, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	if q.Visibility != nil || q.Members != nil || q.Shares != nil {
		fail(w, http.StatusForbidden, "not-allowed", "operators don't change who may use a sandbox (visibility, members, shares): its home consumer or its owner does")
		return
	}
	id := r.PathValue("id")
	in, err := m.patchSandbox(r.Context(), id, q)
	if err != nil {
		writeErr(w, err, m.recCopy(id))
		return
	}
	m.opAnswer(w, r, id, in)
}

func (m *Manager) opAction(w http.ResponseWriter, r *http.Request) {
	act := r.PathValue("action")
	if act != "start" && act != "stop" {
		fail(w, http.StatusNotFound, "not-found", "no action "+act)
		return
	}
	id := r.PathValue("id")
	in, err := m.lifecycle(r.Context(), id, act, m.waitParam(r))
	if err != nil {
		writeErr(w, err, m.recCopy(id))
		return
	}
	m.opAnswer(w, r, id, in)
}

func (m *Manager) opDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := m.deleteSandbox(r.Context(), id); err != nil {
		writeErr(w, err, m.recCopy(id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- snapshots: an operator's backups, whoever's sandbox it is ----------------------------

// opSnapRec is {id} for a snapshot route: its record, the substrate offering
// snapshots (a refusal answered when not).
func (m *Manager) opSnapRec(w http.ResponseWriter, r *http.Request) (record, bool) {
	o, err := m.offer(r.Context())
	if err != nil {
		writeErr(w, err, nil)
		return record{}, false
	}
	if !contains(o.caps, "snapshots") {
		fail(w, http.StatusNotImplemented, "unsupported", "the substrate keeps no snapshots (yet)")
		return record{}, false
	}
	rec := m.recCopy(r.PathValue("id"))
	if rec == nil {
		fail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return record{}, false
	}
	return *rec, true
}

func (m *Manager) opSnapshots(w http.ResponseWriter, r *http.Request) {
	rec, ok := m.opSnapRec(w, r)
	if !ok {
		return
	}
	snaps, err := m.box(rec).Snapshots(r.Context())
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	if snaps == nil {
		snaps = []xbin.Snapshot{}
	}
	if opHides(rec, callerOf(r)) {
		opRedactSnapshots(snaps)
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": snaps})
}

func (m *Manager) opSnapshot(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Name string `json:"name"`
	}
	if err := decode(r, 64<<10, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	rec, ok := m.opSnapRec(w, r)
	if !ok {
		return
	}
	if rec.Overlay != "" {
		failState(w, "the sandbox is "+rec.Overlay, rec.Overlay)
		return
	}
	s, err := m.snapshot(r.Context(), rec, q.Name, "")
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

func (m *Manager) opRestore(w http.ResponseWriter, r *http.Request) {
	rec, ok := m.opSnapRec(w, r)
	if !ok {
		return
	}
	in, err := m.restore(r.Context(), rec, r.PathValue("sid"))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	m.opAnswer(w, r, rec.ID, in)
}

func (m *Manager) opSnapDelete(w http.ResponseWriter, r *http.Request) {
	rec, ok := m.opSnapRec(w, r)
	if !ok {
		return
	}
	if err := m.box(rec).DeleteSnapshot(r.Context(), r.PathValue("sid")); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) opBuild(w http.ResponseWriter, r *http.Request) {
	if err := m.rebuildImage(context.WithoutCancel(r.Context()), r.PathValue("id")); err != nil {
		writeErr(w, err, nil)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// opOrphan: DELETE /ops/orphans/{name} — a substrate sandbox the manager
// doesn't know.
func (m *Manager) opOrphan(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m.mu.Lock()
	known := false
	for _, rec := range m.recs {
		known = known || rec.Runtime == name
	}
	for _, im := range m.imgs {
		known = known || slices.Contains(im.runtimes(), name)
	}
	m.mu.Unlock()
	if known {
		fail(w, http.StatusConflict, "state", name+" is one of this manager's sandboxes or images, not an orphan")
		return
	}
	if err := m.backend().Delete(r.Context(), name); err != nil {
		writeErr(w, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) recCopy(id string) *record {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.recs[id]; p != nil {
		cp := *p
		return &cp
	}
	return nil
}

func (m *Manager) opAnswer(w http.ResponseWriter, r *http.Request, id string, in *xbin.SandboxInfo) {
	rec := m.recCopy(id)
	if rec == nil {
		fail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return
	}
	if in == nil {
		var err error
		if in, err = m.info(r.Context(), *rec); err != nil {
			writeErr(w, err, rec)
			return
		}
	}
	v := opView{sandboxView: m.view(*rec, in, homeOf(*rec), nil), Consumer: rec.Owner.Via, Runtime: rec.Runtime}
	if in != nil {
		v.Mode, v.DiskBytes, v.ExecsRunning, v.Base = in.Mode, in.DiskBytes, in.ExecsRunning, in.Base
	}
	if rec.Owner.PartitionID != "" {
		opRedact(&v.sandboxView, *rec, m.partitionSeq(*rec), callerOf(r))
	}
	writeJSON(w, http.StatusOK, v)
}

// homeOf is the caller rec's home consumer is: its consumer and partition.
func homeOf(rec record) caller { return caller{from: rec.Owner.Via, partID: rec.Owner.PartitionID} }

// opHides: rec is homed in a user partition, and the viewer may not use it
// as a consumer (it isn't shared with them) — so what it carries of its
// consumer's content is hidden from them (S19): its name, its labels, its
// snapshots' names. A model may have chosen any of them.
func opHides(rec record, viewer caller) bool {
	return rec.Owner.PartitionID != "" && !(rec.visible(viewer) && rec.personOK(viewer))
}

// opRedact is v as operators see it (opHides): its name shows as
// <consumer>/<partition id, first 8> #<n>, n its place among that
// partition's sandboxes (oldest first), and its labels as none.
func opRedact(v *sandboxView, rec record, n int, viewer caller) {
	if !opHides(rec, viewer) {
		return
	}
	id := rec.Owner.PartitionID
	if len(id) > 8 {
		id = id[:8]
	}
	v.Name, v.Labels = fmt.Sprintf("%s/%s #%d", rec.Owner.Via, id, n), map[string]string{}
}

// opRedactSnapshots names a hidden sandbox's snapshots (opHides) snapshot
// #<n>, n their place oldest first; the answer keeps its order.
func opRedactSnapshots(snaps []xbin.Snapshot) {
	order := make([]int, len(snaps))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return snaps[order[a]].Created < snaps[order[b]].Created })
	for n, i := range order {
		snaps[i].Name = fmt.Sprintf("snapshot #%d", n+1)
	}
}

// partitionSeq is rec's place among its user partition's sandboxes, oldest
// first (opRedact's n).
func (m *Manager) partitionSeq(rec record) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.recs {
		if r.Owner.Via == rec.Owner.Via && r.Owner.PartitionID == rec.Owner.PartitionID &&
			(r.Created < rec.Created || r.Created == rec.Created && r.ID <= rec.ID) {
			n++
		}
	}
	return n
}
