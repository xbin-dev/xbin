package tilesbx

// api.go — the gates and the definition routes: runtime, policy, list,
// create, get, patch and delete (plans/tile-sandbox-runtime.md §3.1–3.3).
// internal/boot registers every route; the handlers live here and in
// api_*.go.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
)

// AdminFunc says whether a principal is a workspace admin.
type AdminFunc func(auth.Principal) bool

const (
	msgNotManager = "tile sandboxes are driven by manager tiles only: the backend of a tile holding cap:sandboxes"
	msgIsolation  = "tile sandboxes need isolation (xbind --isolate)"
)

// Manages reports a manager call: the backend (instance token) of a tile
// holding cap:sandboxes, checked on every call. A frame of the same tile, a
// terminal, cron or a signed-in person never is one.
func (m *Manager) Manages(p auth.Principal) bool {
	return p.Component != "" && p.Via == "instance" && m.deps.Caps != nil && m.deps.Caps.SandboxesFor(p.Component)
}

func (m *Manager) isAdmin(p auth.Principal) bool { return m.deps.Admin != nil && m.deps.Admin(p) }

// manager gates a manager route: 403 for anyone else, 501 without
// isolation. The key is the caller's own: a manager only names its own
// sandboxes.
func (m *Manager) manager(w http.ResponseWriter, r *http.Request) (Key, bool) {
	p := auth.PrincipalOf(r)
	if !m.Manages(p) {
		writeErr(w, refuse(RefNotAllowed, msgNotManager))
		return Key{}, false
	}
	if !m.isolated {
		writeErr(w, refuse(RefUnsupported, msgIsolation))
		return Key{}, false
	}
	return KeyOf(p), true
}

// managerOrAdmin gates stop and delete: a manager on its own sandboxes, or
// a workspace admin naming the tile (?tile=, &deployment= for a
// deployment's). An admin never creates, execs, reads files or attaches.
func (m *Manager) managerOrAdmin(w http.ResponseWriter, r *http.Request) (Key, bool) {
	p := auth.PrincipalOf(r)
	if m.Manages(p) {
		if !m.isolated {
			writeErr(w, refuse(RefUnsupported, msgIsolation))
			return Key{}, false
		}
		return KeyOf(p), true
	}
	if !m.isAdmin(p) {
		writeErr(w, refuse(RefNotAllowed, msgNotManager))
		return Key{}, false
	}
	q := r.URL.Query()
	tile := strings.Trim(q.Get("tile"), "/")
	if !validTilePath(tile) {
		writeErr(w, refuse(RefInvalid, "?tile= names the tile whose sandbox this is"))
		return Key{}, false
	}
	k := Key{Tile: tile, Deployment: q.Get("deployment")}
	if !k.Main() {
		writeErr(w, errDeployment())
		return Key{}, false
	}
	return k, true
}

// lookup finds {name} under k: 404 when there is none.
func (m *Manager) lookup(w http.ResponseWriter, k Key, name string) (*Def, bool) {
	m.mu.Lock()
	d, ok := m.defs.get(k, name)
	m.mu.Unlock()
	if !ok {
		writeErr(w, refuse(RefNotFound, "no sandbox %q", name))
	}
	return d, ok
}

// managed gates a manager route on one sandbox and finds it.
func (m *Manager) managed(w http.ResponseWriter, r *http.Request) (Key, *Def, bool) {
	k, ok := m.manager(w, r)
	if !ok {
		return Key{}, nil, false
	}
	d, ok := m.lookup(w, k, r.PathValue("name"))
	return k, d, ok
}

// notBuilt is a route of the contract this runtime doesn't serve yet.
func notBuilt(what string) *Error {
	return refuse(RefUnsupported, "%s: not supported by this xbind yet", what)
}

// readBody reads a request body of at most max bytes (413 too-large past it).
func readBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, refuse(RefTooLarge, "the body is over %d bytes", max)
		}
		return nil, refuse(RefInvalid, "reading the body: %v", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, refuse(RefInvalid, "a JSON body is required")
	}
	return b, nil
}

// decode reads a JSON body leniently: unknown fields are ignored, so a
// newer SDK works against an older xbind.
func decode(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	b, err := readBody(w, r, max)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return refuse(RefInvalid, "bad JSON: %v", err)
	}
	return nil
}

// ServeRuntime answers GET /sandboxes/runtime: what the calling manager may
// use now. It answers without isolation too (isolation: false).
func (m *Manager) ServeRuntime(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !m.Manages(p) {
		writeErr(w, refuse(RefNotAllowed, msgNotManager))
		return
	}
	m.mu.Lock()
	rt := m.runtime(KeyOf(p))
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, rt)
}

// policyView is the policy routes' answer: effective and as stored.
func (m *Manager) policyView() map[string]any {
	st := m.policy.get()
	return map[string]any{"policy": st.Effective(), "stored": st}
}

// ServePolicy answers GET /sandboxes/policy (admins).
func (m *Manager) ServePolicy(w http.ResponseWriter, r *http.Request) {
	if !m.isAdmin(auth.PrincipalOf(r)) {
		writeErr(w, refuse(RefNotAllowed, "admin only"))
		return
	}
	m.mu.Lock()
	v := m.policyView()
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// ServeSetPolicy answers PUT /sandboxes/policy (admins): a partial policy
// merged onto the stored one, then validated.
func (m *Manager) ServeSetPolicy(w http.ResponseWriter, r *http.Request) {
	if !m.isAdmin(auth.PrincipalOf(r)) {
		writeErr(w, refuse(RefNotAllowed, "admin only"))
		return
	}
	body, err := readBody(w, r, defBodyMax)
	if err != nil {
		writeErr(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := mergePolicy(m.policy.get(), body)
	if err != nil {
		writeErr(w, refuse(RefInvalid, "bad policy: %v", err))
		return
	}
	if err := m.policy.set(p); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m.policyView())
}

// ServeList answers a manager's GET /sandboxes: its own sandboxes. (An
// admin's GET /sandboxes is the registry view, which internal/boot serves.)
func (m *Manager) ServeList(w http.ResponseWriter, r *http.Request) {
	k, ok := m.manager(w, r)
	if !ok {
		return
	}
	m.mu.Lock()
	out := []Info{}
	for _, d := range m.defs.list(k) {
		out = append(out, m.info(k, d))
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"sandboxes": out})
}

// ServeCreate answers POST /sandboxes: 201 and the new sandbox, or 200 and
// the existing one when clientId repeats the same request.
func (m *Manager) ServeCreate(w http.ResponseWriter, r *http.Request) {
	k, ok := m.manager(w, r)
	if !ok {
		return
	}
	var req CreateRequest
	if err := decode(w, r, defBodyMax, &req); err != nil {
		writeErr(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	in, status, err := m.create(k, &req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if status == http.StatusCreated && req.Start {
		// A start that fails leaves the sandbox stopped, the failure in
		// stateDetail — the create itself stands.
		if err := m.start(k, req.Name); err != nil {
			m.setDetail(k, req.Name, err.Error())
		}
		d, _ := m.defs.get(k, req.Name)
		in = m.info(k, d)
	}
	writeJSON(w, status, in)
}

// reqHash identifies a create request, for a clientId repeat.
func reqHash(req *CreateRequest) string {
	b, _ := json.Marshal(req)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}

// create stores a new definition. Callers hold m.mu.
func (m *Manager) create(k Key, req *CreateRequest) (Info, int, error) {
	hash := reqHash(req)
	if req.ClientID != "" {
		for _, d := range m.defs.list(k) {
			if d.ClientID != req.ClientID {
				continue
			}
			if d.ReqHash != hash {
				return Info{}, 0, refuse(RefExists, "clientId %q was used for a different request (sandbox %q)", req.ClientID, d.Name)
			}
			return m.info(k, d), http.StatusOK, nil
		}
	}
	if _, ok := m.defs.get(k, req.Name); ok {
		return Info{}, 0, refuse(RefExists, "a sandbox named %q exists", req.Name)
	}
	lim := m.limitsFor(k.Tile)
	d, err := m.define(k, req, lim)
	if err != nil {
		return Info{}, 0, err
	}
	if n := m.defs.count(k); n >= lim.PerTile.Max {
		return Info{}, 0, refuse(RefLimit, "the tile has %d sandboxes, its limit (sandboxes policy: perTile.max)", n)
	}
	if err := m.vmDisks(k, d, lim); err != nil {
		return Info{}, 0, err
	}
	d.Created, d.Version = m.now().UnixMilli(), 1
	if req.ClientID != "" {
		d.ClientID, d.ReqHash = req.ClientID, hash
	}
	if err := m.defs.put(k, d); err != nil {
		return Info{}, 0, err
	}
	return m.info(k, d), http.StatusCreated, nil
}

// vmDisks checks the tile's declared VM disks, d's included (in place of
// its stored self), stay within perTile.diskGiB. Callers hold m.mu.
func (m *Manager) vmDisks(k Key, d *Def, lim Limits) error {
	if d.Mode != ModeVM {
		return nil
	}
	sum := d.DiskGiB
	for _, o := range m.defs.list(k) {
		if o.Mode == ModeVM && o.Name != d.Name {
			sum += o.DiskGiB
		}
	}
	if sum > lim.PerTile.DiskGiB {
		return refuse(RefLimit, "the tile's VM disks would be %d GiB, over its %d GiB (sandboxes policy: perTile.diskGiB)", sum, lim.PerTile.DiskGiB)
	}
	return nil
}

// ServeGet answers GET /sandboxes/{name}.
func (m *Manager) ServeGet(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	m.mu.Lock()
	in := m.info(k, d)
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, in)
}

// ServePatch answers PATCH /sandboxes/{name}: the sandbox, with
// restartNeeded when a change waits for the next start.
func (m *Manager) ServePatch(w http.ResponseWriter, r *http.Request) {
	k, ok := m.manager(w, r)
	if !ok {
		return
	}
	var p PatchRequest
	if err := decode(w, r, defBodyMax, &p); err != nil {
		writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok {
		writeErr(w, refuse(RefNotFound, "no sandbox %q", name))
		return
	}
	lim := m.limitsFor(k.Tile)
	n, changed, err := m.amend(k, d, &p, lim)
	if err == nil && changed && n.DiskGiB > d.DiskGiB {
		err = m.vmDisks(k, n, lim)
	}
	if err == nil && changed {
		err = m.defs.put(k, n)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m.info(k, n))
}

// ServeDelete answers DELETE /sandboxes/{name} (the manager, or an admin
// with ?tile=): stop it, remove its state (confined), forget it. 204.
func (m *Manager) ServeDelete(w http.ResponseWriter, r *http.Request) {
	k, ok := m.managerOrAdmin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.defs.get(k, name); !ok {
		writeErr(w, refuse(RefNotFound, "no sandbox %q", name))
		return
	}
	err := m.stop(k, name, "deleted")
	if err == nil {
		err = m.removeState(k, name)
	}
	if err == nil {
		err = m.defs.del(k, name)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	delete(m.live[k], name)
	w.WriteHeader(http.StatusNoContent)
}
