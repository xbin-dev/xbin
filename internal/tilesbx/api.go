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
	"log/slog"
	"net/http"
	"regexp"
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

// The id grammars (§3.1), checked on every route before any lookup: exec
// ids are <boot prefix>-<n>, snapshot ids s-<n>.
var (
	execIDRE = regexp.MustCompile(`^[0-9a-f]{6}-[0-9]{1,12}$`)
	snapIDRE = regexp.MustCompile(`^s-[0-9]{1,12}$`)
)

// hygiene refuses (400 invalid) a request whose path could address
// something other than what its segments say, before anything is looked
// up — the caller included: a "." or ".." segment, an encoded "/", "." or
// "\" (or a bare "\") in any segment, or a {name}, {id} or {sid} that fails
// its grammar. Go's mux redirects a plain "..", but an encoded one would
// arrive as one segment's value, and a manager forwarding a consumer's id
// must never be steered to another route (§8.3). Every form of the path is
// checked: RawPath (what the client sent, when it isn't the default
// encoding), EscapedPath and the decoded Path.
func hygiene(r *http.Request) error {
	for _, p := range []string{r.URL.RawPath, r.URL.EscapedPath(), r.URL.Path} {
		for _, seg := range strings.Split(p, "/") {
			up := strings.ToUpper(seg)
			if seg == "." || seg == ".." || strings.Contains(seg, `\`) ||
				strings.Contains(up, "%2F") || strings.Contains(up, "%2E") || strings.Contains(up, "%5C") {
				return refuse(RefInvalid, "the path has a dot segment or an encoded /, . or \\ in a segment")
			}
		}
	}
	if n := r.PathValue("name"); n != "" {
		if err := validName(n); err != nil {
			return err
		}
	}
	if id := r.PathValue("id"); id != "" && !execIDRE.MatchString(id) {
		return refuse(RefInvalid, "exec id %q must match %s", id, execIDRE)
	}
	if sid := r.PathValue("sid"); sid != "" && !snapIDRE.MatchString(sid) {
		return refuse(RefInvalid, "snapshot id %q must match %s", sid, snapIDRE)
	}
	return nil
}

// manager gates a manager route: 400 for a path that fails hygiene, 403
// for anyone but a manager, 501 without isolation or for a non-main
// deployment. The key is the caller's own (keyOf): a manager only names
// its own sandboxes.
func (m *Manager) manager(w http.ResponseWriter, r *http.Request) (Key, bool) {
	if err := hygiene(r); err != nil {
		writeErr(w, err)
		return Key{}, false
	}
	p := auth.PrincipalOf(r)
	if !m.Manages(p) {
		writeErr(w, refuse(RefNotAllowed, msgNotManager))
		return Key{}, false
	}
	if !m.isolated {
		writeErr(w, refuse(RefUnsupported, msgIsolation))
		return Key{}, false
	}
	k, err := keyOf(p)
	if err != nil {
		writeErr(w, err)
		return Key{}, false
	}
	return k, true
}

// managerOrAdmin gates stop and delete: a manager on its own sandboxes, or
// a workspace admin naming the tile (?tile=, &deployment= for a
// deployment's). An admin never creates, execs, reads files or attaches.
func (m *Manager) managerOrAdmin(w http.ResponseWriter, r *http.Request) (Key, bool) {
	if err := hygiene(r); err != nil {
		writeErr(w, err)
		return Key{}, false
	}
	p := auth.PrincipalOf(r)
	if m.Manages(p) {
		if !m.isolated {
			writeErr(w, refuse(RefUnsupported, msgIsolation))
			return Key{}, false
		}
		k, err := keyOf(p)
		if err != nil {
			writeErr(w, err)
			return Key{}, false
		}
		return k, true
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
	k := keyFor(tile, q.Get("deployment"))
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

// managed gates a manager route on one sandbox and finds it: {name}, and
// any {id} or {sid}, have passed their grammars (hygiene) first; an exec id
// of an earlier boot is 410 lost (its exec ended with that xbind). The call
// is activity on the sandbox (the idle stop, idle.go).
func (m *Manager) managed(w http.ResponseWriter, r *http.Request) (Key, *Def, bool) {
	k, ok := m.manager(w, r)
	if !ok {
		return Key{}, nil, false
	}
	d, ok := m.lookup(w, k, r.PathValue("name"))
	if !ok {
		return Key{}, nil, false
	}
	if err := m.execLost(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return Key{}, nil, false
	}
	m.touchBox(k, d.Name)
	return k, d, true
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
	if err := hygiene(r); err != nil {
		writeErr(w, err)
		return
	}
	p := auth.PrincipalOf(r)
	if !m.Manages(p) {
		writeErr(w, refuse(RefNotAllowed, msgNotManager))
		return
	}
	k, err := keyOf(p)
	if err != nil {
		writeErr(w, err)
		return
	}
	m.mu.Lock()
	rt := m.runtime(k)
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, rt)
}

// policyView is the policy routes' answer: effective and as stored, and
// why the policy file can't be read (error), when it can't.
func (m *Manager) policyView() map[string]any {
	st := m.policy.get()
	v := map[string]any{"policy": m.policy.effective(), "stored": st}
	if m.policy.err != nil {
		v["error"] = m.policy.err.Error()
	}
	return v
}

// adminGate gates the policy routes: hygiene, then admins only.
func (m *Manager) adminGate(w http.ResponseWriter, r *http.Request) bool {
	if err := hygiene(r); err != nil {
		writeErr(w, err)
		return false
	}
	if !m.isAdmin(auth.PrincipalOf(r)) {
		writeErr(w, refuse(RefNotAllowed, "admin only"))
		return false
	}
	return true
}

// ServePolicy answers GET /sandboxes/policy (admins).
func (m *Manager) ServePolicy(w http.ResponseWriter, r *http.Request) {
	if !m.adminGate(w, r) {
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
	if !m.adminGate(w, r) {
		return
	}
	body, err := readBody(w, r, defBodyMax)
	if err != nil {
		writeErr(w, err)
		return
	}
	m.mu.Lock()
	p, err := mergePolicy(m.policy.get(), body)
	if err != nil {
		m.mu.Unlock()
		writeErr(w, refuse(RefInvalid, "bad policy: %v", err))
		return
	}
	if err := m.policy.set(p); err != nil {
		m.mu.Unlock()
		writeErr(w, err)
		return
	}
	v := m.policyView()
	on, why := m.policy.on()
	m.mu.Unlock()
	m.applyTotal() // the cgroup parent's caps follow policy.total
	if !on {
		m.switchedOff(why + ": stopped, state kept")
	}
	m.rearmIdle(nil) // idleStopMin applies to running sandboxes now
	writeJSON(w, http.StatusOK, v)
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
// the existing one when clientId repeats the same request. With start, the
// new sandbox is started (outside the definitions mutex, in its flight).
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
	wait, err := waitOf(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	m.mu.Lock()
	in, status, err := m.create(k, &req)
	var clone *copyJob
	if b := m.live[k][req.Name]; err == nil && b != nil {
		clone = b.clone
	}
	m.mu.Unlock()
	if err != nil {
		writeErr(w, err)
		return
	}
	if clone != nil { // a clone copying: ?wait bounds the wait (it starts itself, with start)
		waitJob(clone, wait)
		in, _ = m.infoOf(k, req.Name)
	} else if status == http.StatusCreated && req.Start {
		// A start that fails leaves the sandbox stopped, the failure in
		// stateDetail — the create itself stands. ?wait bounds the wait.
		if done, err := within(wait, func() error { return m.Start(k, req.Name) }); done && err != nil {
			m.mu.Lock()
			if b := m.live[k][req.Name]; b != nil && b.run == nil && b.detail == "" {
				b.detail = err.Error()
			}
			m.mu.Unlock()
		}
		in, _ = m.infoOf(k, req.Name)
	}
	writeJSON(w, status, in)
}

// infoOf is k's sandbox's SandboxInfo now.
func (m *Manager) infoOf(k Key, name string) (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok {
		return Info{}, false
	}
	return m.info(k, d), true
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
	var src *cloneSource
	if req.From != nil { // a clone (clone.go)
		if src, err = m.cloneSource(k, req.From, d); err != nil {
			return Info{}, 0, err
		}
		d.Base = src.st.Base // it takes its source's base
		if d.Mode == ModeVM {
			d.DiskGiB = max(d.DiskGiB, src.diskGiB) // a disk never shrinks
		}
	}
	if n := m.defs.count(k); n >= lim.PerTile.Max {
		return Info{}, 0, refuse(RefLimit, "the tile has %d sandboxes, its limit (sandboxes policy: perTile.max)", n)
	}
	if err := m.vmDisks(k, d, lim); err != nil {
		return Info{}, 0, err
	}
	if src != nil && src.dir != "" {
		if err := m.diskRoom(k.Tile, src.bytes); err != nil {
			return Info{}, 0, err
		}
		d.Pending = "clone"
	}
	d.UID, d.Created, d.Version = newUID(), m.now().UnixMilli(), 1
	if req.ClientID != "" {
		d.ClientID, d.ReqHash = req.ClientID, hash
	}
	if err := m.defs.put(k, d); err != nil {
		return Info{}, 0, err
	}
	if d.Pending != "" {
		m.beginClone(k, d, src, req.Start)
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

// ServeGet answers GET /sandboxes/{name}. Reading xbind's record of a
// sandbox isn't activity on it: a manager polling its state doesn't keep
// it from idling.
func (m *Manager) ServeGet(w http.ResponseWriter, r *http.Request) {
	k, ok := m.manager(w, r)
	if !ok {
		return
	}
	d, ok := m.lookup(w, k, r.PathValue("name"))
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
	if b := m.live[k][name]; b != nil && b.state == StateCreating { // (busy with a copy, it may change)
		writeErr(w, busyLocked(name, b))
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
	if changed && n.IdleStopMin != d.IdleStopMin { // a running sandbox's idle stop follows at once
		m.rearmIdleLocked(func(rk Key, rd *Def) bool { return rk == k && rd.UID == n.UID })
	}
	writeJSON(w, http.StatusOK, m.info(k, n))
}

// ServeDelete answers DELETE /sandboxes/{name} (the manager, or an admin
// with ?tile=): stop it, put its state aside for the confined remover
// (trash.go), forget it. 204 — the removal goes on in the background.
func (m *Manager) ServeDelete(w http.ResponseWriter, r *http.Request) {
	k, ok := m.managerOrAdmin(w, r)
	if !ok {
		return
	}
	if err := m.Delete(k, r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete stops k's sandbox, puts its state aside and forgets it, in its
// flight: a start waiting for the flight then finds no sandbox.
func (m *Manager) Delete(k Key, name string) error {
	if err := m.endClone(k, name); err != nil { // a clone still copying ends first
		return err
	}
	b, err := m.boxFor(k, name)
	if err != nil {
		return err
	}
	m.mu.Lock()
	e := deleteBlockedLocked(name, b)
	m.mu.Unlock()
	if e != nil {
		return e
	}
	b.flight.Lock()
	defer b.flight.Unlock()
	if err := m.stopLocked(b, "deleted"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defs.get(k, name)
	if !ok || m.live[k][name] != b {
		return refuse(RefNotFound, "no sandbox %q", name)
	}
	if e := deleteBlockedLocked(name, b); e != nil { // a copy of it began meanwhile
		return e
	}
	to, undo, err := m.trashState(k, d)
	if err != nil {
		return err
	}
	if err := m.defs.del(k, name); err != nil {
		if uerr := undo(); uerr != nil {
			slog.Error("tile sandbox: a failed delete couldn't put its state back", "tile", k.Tile, "sandbox", name, "at", to, "err", uerr)
		}
		return err
	}
	delete(m.live[k], name)
	b.execs.forgetAll() // their rings go back to the tile's budget
	if to != "" {
		m.trash.putSized(to, b.diskBytes)
	}
	return nil
}
