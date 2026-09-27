// files.go — the contract's files, trees (tar) and snapshots, relayed to the
// backend's box. Paths are the sandbox's own: the backend resolves them
// inside the sandbox, never on a host (the manager never touches them).
package main

import (
	"context"
	"io"
	"net/http"
	"strconv"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func (m *Manager) fileRoutes(x *http.ServeMux) {
	x.HandleFunc("GET /sbx/sandboxes/{id}/files/stat", m.fileStat)
	x.HandleFunc("GET /sbx/sandboxes/{id}/files/content", m.fileRead)
	x.HandleFunc("PUT /sbx/sandboxes/{id}/files/content", m.fileWrite)
	x.HandleFunc("GET /sbx/sandboxes/{id}/files/list", m.fileList)
	x.HandleFunc("POST /sbx/sandboxes/{id}/files/mkdir", m.fileMkdir)
	x.HandleFunc("POST /sbx/sandboxes/{id}/files/remove", m.fileRemove)
	x.HandleFunc("POST /sbx/sandboxes/{id}/files/move", m.fileMove)
	x.HandleFunc("GET /sbx/sandboxes/{id}/tar", m.tarGet)
	x.HandleFunc("PUT /sbx/sandboxes/{id}/tar", m.tarPut)
	x.HandleFunc("GET /sbx/sandboxes/{id}/snapshots", m.snapList)
	x.HandleFunc("POST /sbx/sandboxes/{id}/snapshots", m.snapCreate)
	x.HandleFunc("POST /sbx/sandboxes/{id}/snapshots/{sid}/restore", m.snapRestore)
	x.HandleFunc("DELETE /sbx/sandboxes/{id}/snapshots/{sid}", m.snapDelete)
}

func (m *Manager) fileStat(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	st, err := m.box(rec).Stat(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (m *Manager) fileRead(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	off, err1 := strconv.ParseInt(orStr(qs.Get("offset"), "0"), 10, 64)
	n, err2 := strconv.ParseInt(orStr(qs.Get("length"), "-1"), 10, 64)
	if err1 != nil || err2 != nil || off < 0 || n < -1 {
		fail(w, http.StatusBadRequest, "invalid", "offset and length are byte counts")
		return
	}
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	p := qs.Get("path")
	box := m.box(rec)
	if n == 0 { // nothing to read, but the path must be a file
		st, err := box.Stat(r.Context(), p)
		if err != nil {
			writeErr(w, err, &rec)
			return
		}
		w.Header().Set("ETag", `"`+st.ETag+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		return
	}
	body, st, err := box.ReadFile(r.Context(), p, off, max(n, 0))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	defer body.Close()
	if st != nil && st.ETag != "" {
		w.Header().Set("ETag", `"`+st.ETag+`"`)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (m *Manager) fileWrite(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	st, err := m.box(rec).WriteFile(r.Context(), qs.Get("path"), r.Body, xbin.WriteOptions{Mode: qs.Get("mode"),
		Mkdirs: qs.Get("mkdirs") == "1", IfMatch: qs.Get("ifMatch"), IfNoneMatch: qs.Get("ifNoneMatch")})
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (m *Manager) fileList(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	l, err := m.box(rec).List(r.Context(), r.URL.Query().Get("path"), max(limit, 0))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	if l.Entries == nil {
		l.Entries = []xbin.FileEntry{}
	}
	writeJSON(w, http.StatusOK, l)
}

// fileOp decodes a files/* body, then runs op on the usable sandbox.
func (m *Manager) fileOp(w http.ResponseWriter, r *http.Request, body any, op func(Box) error) {
	if err := decode(r, 64<<10, body); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	if err := op(m.box(rec)); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) fileMkdir(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Path    string `json:"path"`
		Parents bool   `json:"parents"`
	}
	m.fileOp(w, r, &q, func(b Box) error { return b.Mkdir(r.Context(), q.Path, q.Parents) })
}

func (m *Manager) fileRemove(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	m.fileOp(w, r, &q, func(b Box) error { return b.Remove(r.Context(), q.Path, q.Recursive) })
}

func (m *Manager) fileMove(w http.ResponseWriter, r *http.Request) {
	var q struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	m.fileOp(w, r, &q, func(b Box) error { return b.Move(r.Context(), q.From, q.To, q.Overwrite) })
}

// --- trees -------------------------------------------------------------------------------

func (m *Manager) tarGet(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "tar", "trees") {
		return
	}
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	body, err := m.box(rec).GetTar(r.Context(), qs.Get("path"), qs["exclude"])
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (m *Manager) tarPut(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "tar", "trees") {
		return
	}
	rec, _, ok := m.usable(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	if err := m.box(rec).PutTar(r.Context(), qs.Get("path"), r.Body, qs.Get("mkdirs") == "1"); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- snapshots ---------------------------------------------------------------------------

func (m *Manager) snapList(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "snapshots", "snapshots") {
		return
	}
	rec, _, ok := m.find(w, r)
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
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": snaps})
}

// snapCreate: POST …/snapshots {name, clientId} — a clientId is per consumer
// and sandbox, kept in the table (snapshots outlive the substrate's run).
func (m *Manager) snapCreate(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "snapshots", "snapshots") {
		return
	}
	var q struct {
		Name     string `json:"name"`
		ClientID string `json:"clientId"`
	}
	if err := decode(r, 64<<10, &q); err != nil {
		fail(w, http.StatusBadRequest, "invalid", "bad body: "+err.Error())
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	if rec.Overlay != "" {
		failState(w, "the sandbox is "+rec.Overlay, rec.Overlay)
		return
	}
	box := m.box(rec)
	key, h := "snap\x00"+c.from+"\x00"+rec.ID+"\x00"+q.ClientID, hashOf(q)
	if q.ClientID != "" {
		defer m.lock(key)()
		target, prev, found, err := m.st.idem(key)
		if err != nil {
			writeErr(w, err, &rec)
			return
		}
		if found && prev != h {
			fail(w, http.StatusConflict, "exists", "clientId "+q.ClientID+" was used for a different snapshot")
			return
		}
		if found {
			snaps, err := box.Snapshots(r.Context())
			if err != nil {
				writeErr(w, err, &rec)
				return
			}
			for _, s := range snaps {
				if rec.ID+"/"+s.ID == target {
					writeJSON(w, http.StatusOK, s)
					return
				}
			}
		}
	}
	cid := ""
	if q.ClientID != "" {
		cid = clientPrefix(c.from) + q.ClientID
	}
	s, err := m.snapshot(r.Context(), rec, q.Name, cid)
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	if q.ClientID != "" {
		if err := m.st.putIdem(key, rec.ID+"/"+s.ID, h); err != nil {
			m.logf("snapshot clientId: %v", err)
		}
	}
	writeJSON(w, http.StatusCreated, s)
}

// snapshot saves rec's state as name (the contract's, and the operators').
func (m *Manager) snapshot(ctx context.Context, rec record, name, clientID string) (*xbin.Snapshot, error) {
	box := m.box(rec)
	s, err := box.Snapshot(ctx, name, clientID)
	if err == nil {
		s, err = settleSnapshot(ctx, box, s) // the contract answers it taken
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.live, rec.ID) // it may have stopped for it
	m.mu.Unlock()
	return s, nil
}

// restore puts rec back to snapshot sid (its execs are killed).
func (m *Manager) restore(ctx context.Context, rec record, sid string) (*xbin.SandboxInfo, error) {
	defer m.lock(rec.ID)()
	if rec.Overlay != "" {
		return nil, &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: rec.Overlay, Message: "the sandbox is " + rec.Overlay}
	}
	in, err := m.box(rec).RestoreSnapshot(ctx, sid)
	if err == nil {
		in, err = settleBusy(ctx, m.backend(), in) // the contract answers it restored
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.live, rec.ID)
	_, _ = m.update(rec.ID, func(x *record) { x.Version++ })
	m.mu.Unlock()
	return in, nil
}

func (m *Manager) snapRestore(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "snapshots", "snapshots") {
		return
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	in, err := m.restore(r.Context(), rec, r.PathValue("sid"))
	if err != nil {
		writeErr(w, err, &rec)
		return
	}
	m.answer(w, r, http.StatusOK, rec.ID, c, in)
}

func (m *Manager) snapDelete(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "snapshots", "snapshots") {
		return
	}
	rec, _, ok := m.find(w, r)
	if !ok {
		return
	}
	if err := m.box(rec).DeleteSnapshot(r.Context(), r.PathValue("sid")); err != nil {
		writeErr(w, err, &rec)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
