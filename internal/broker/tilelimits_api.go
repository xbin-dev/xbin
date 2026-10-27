package broker

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// The tile limits routes (D203, docs/protocol.md §Tile limits):
//
//	GET    /api/xbin/limits[?tile=|?pending=1]   rows, one row, what waits
//	POST   /api/xbin/limits                      approve, raise or set
//	DELETE /api/xbin/limits?tile=&kind=          back to the default
//	POST   /api/xbin/limits/dismiss?tile=&kind=  clear a hit for everyone
//	PUT    /api/xbin/orgs/{org}/limits           an org's per-tile ceilings
//
// Every decision is a person's act (limitsActor): a workspace admin, or an
// admin of the owning org within the org's ceiling (never force); a tile's
// own credentials read their own row and decide nothing.

func (b *Broker) registerTileLimits(srv *server.Server) {
	srv.RegisterAPI("GET /limits", b.apiLimitsGet)
	srv.RegisterAPI("POST /limits", b.apiLimitsPost)
	srv.RegisterAPI("DELETE /limits", b.apiLimitsDelete)
	srv.RegisterAPI("POST /limits/dismiss", b.apiLimitsDismiss)
}

// limitTarget is what a request names: a tile, or for disk a scope (and
// the tile that roots it, if any).
type limitTarget struct {
	tile     string // the tile (disk: the scope's root tile; "" for none)
	scope    string // disk: the scope's path ("" = the workspace scope)
	key      string // the store's key: the tile, or the scope's data key
	kind     limits.Kind
	facts    tileFacts // the tile's (zero for a scope no tile roots)
	kf       kindFacts
	hasFacts bool
}

// target resolves tile or scope for kind k; on an error it has answered.
func (b *Broker) limitTarget(w http.ResponseWriter, v limitsViewer, tile, scope string, k limits.Kind) (limitTarget, bool) {
	tile, scope = strings.Trim(tile, "/"), strings.Trim(scope, "/")
	t := limitTarget{kind: k}
	fail := func(code int, msg string) (limitTarget, bool) {
		server.WriteError(w, code, msg, limitsDocs)
		return t, false
	}
	switch {
	case tile == "" && scope == "":
		return fail(http.StatusBadRequest, "name the tile (or, for diskGiB, the scope)")
	case tile != "" && scope != "":
		return fail(http.StatusBadRequest, "name the tile or the scope, not both")
	case scope != "" && k != limits.DiskGiB:
		return fail(http.StatusBadRequest, "scope names a scope's disk quota (diskGiB); "+string(k)+" is a tile's: name the tile")
	}
	if scope != "" {
		path, ok := b.scopeNamed(scope)
		if !ok {
			return fail(http.StatusNotFound, "no such scope: "+scope)
		}
		t.scope, t.key = path, limitScopeKey(path)
		if root := b.scopeRootOfKey(t.key); root != "" {
			tile = root
		} else {
			return t, true // the workspace scope, or one no tile roots: workspace admins
		}
	}
	if !b.mayReadLimits(v, tile, true) {
		return fail(http.StatusForbidden, "no access to this tile")
	}
	c, ok := b.rootComponent(tile)
	if !ok {
		return fail(http.StatusNotFound, "no such tile: "+tile)
	}
	t.tile, t.facts, t.hasFacts = tile, b.tileFacts(c), true
	t.kf = t.facts.kinds[k]
	if k == limits.DiskGiB {
		if !t.facts.rootsScope {
			where := "the workspace's"
			if c.Scope != "" {
				where = "its scope's (" + c.Scope + ")"
			}
			return fail(http.StatusBadRequest, tile+"'s disk quota is "+where+": name the tile that roots it, or the scope")
		}
		t.scope = c.Scope
	}
	t.key = t.facts.keyOf(k)
	return t, true
}

// scopeNamed resolves a scope by its path or its data key; "workspace" is
// the workspace scope (path "").
func (b *Broker) scopeNamed(s string) (string, bool) {
	for p := range b.Reg.Scopes() {
		if p == s || limitScopeKey(p) == s {
			return p, true
		}
	}
	return "", s == "workspace"
}

// diskScopeFacts is the facts of a scope no tile roots (the workspace's).
func (b *Broker) diskScopeFacts(t *limitTarget) {
	t.kf = kindFacts{def: b.diskDefault(), max: b.diskMax(), usage: b.diskUsageOf(t.key), enforced: b.disk != nil, placed: true}
	t.facts = tileFacts{scope: t.scope, scopeKey: t.key, rootsScope: true, kinds: map[limits.Kind]kindFacts{limits.DiskGiB: t.kf}}
}

// parseKind reads a kind query or body value (its name or bx alias).
func parseKind(w http.ResponseWriter, s string) (limits.Kind, bool) {
	k, ok := limits.Parse(s)
	if !ok {
		server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("kind %q: one of memMiB, diskGiB, vmMemMiB, vmVCPUs, sbxMemMiB, sbxVCPUs, sbxDiskGiB", s), limitsDocs)
	}
	return k, ok
}

// unreadable answers 500 while data/tile-limits.json can't be read.
func (b *Broker) unreadable(w http.ResponseWriter) bool {
	if err := b.tlUnreadable(); err != nil {
		server.WriteError(w, http.StatusInternalServerError, tileLimitsRel+" can't be read ("+err.Error()+"): every tile has its default limits, and no decision is saved until it is fixed or removed by hand", limitsDocs)
		return true
	}
	return false
}

// ---- GET /limits ----

func (b *Broker) apiLimitsGet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	v := b.limitsViewerOf(p)
	q := r.URL.Query()
	b.tlRefreshMaxes()
	f := b.tlDoc()
	switch {
	case q.Get("pending") != "" && q.Get("pending") != "0":
		server.WriteJSON(w, http.StatusOK, map[string]any{"pending": b.pendingFor(v, f)})
	case q.Get("tile") != "":
		c, ok := b.rootComponent(strings.Trim(q.Get("tile"), "/"))
		switch {
		case !b.mayReadLimits(v, strings.Trim(q.Get("tile"), "/"), true):
			server.WriteError(w, http.StatusForbidden, "no access to this tile", limitsDocs)
			return
		case !ok:
			server.WriteError(w, http.StatusNotFound, "no such tile: "+q.Get("tile"), limitsDocs)
			return
		}
		tf := b.tileFacts(c)
		var deciding []limits.Kind
		for _, k := range limits.Kinds {
			if kf := tf.kinds[k]; kf.applies() && b.mayDecideKind(v, c.Path, k, kf.def) {
				deciding = append(deciding, k)
			}
		}
		caps := b.capInputs(deciding...)
		f = b.tlDoc() // the newest, after the runtime's answers
		out := map[string]any{"tile": b.rowOf(v, tf, f, caps)}
		if len(caps) > 0 {
			cm := map[limits.Kind]limits.Capacity{}
			for k, ci := range caps {
				cm[k] = ci.at(f, tf.keyOf(k))
			}
			out["capacity"] = cm
		}
		server.WriteJSON(w, http.StatusOK, out)
	default:
		tiles := b.limitTiles(v, f)
		var caps map[limits.Kind]capInput
		if v.person && (v.admin || b.adminsAnyOrg(v)) {
			caps = b.capInputs(limits.Kinds...)
		}
		f = b.tlDoc()
		rows := make([]*limitRow, 0, len(tiles))
		for _, tf := range tiles {
			rows = append(rows, b.rowOf(v, tf, f, caps))
		}
		defs, enf := map[limits.Kind]int64{}, map[limits.Kind]bool{}
		for _, k := range limits.Kinds {
			defs[k], enf[k] = b.kindDefault("", k)
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"tiles": rows, "defaults": defs, "enforced": enf,
			"orgCeilings": b.orgCeilingsFor(v), "canSetCeilings": b.canManageUsers(p) || v.person && v.admin})
	}
}

// adminsAnyOrg reports whether v admins some org.
func (b *Broker) adminsAnyOrg(v limitsViewer) bool {
	if b.Users == nil || v.p.Access == nil {
		return false
	}
	for _, o := range b.Users.Orgs() {
		if v.p.Access.IsAdminOrg(o.ID) {
			return true
		}
	}
	return false
}

// orgCeilingsFor is the ceilings v may see: every org's that has one for a
// workspace admin, the orgs v admins otherwise.
func (b *Broker) orgCeilingsFor(v limitsViewer) map[string]map[limits.Kind]int64 {
	out := map[string]map[limits.Kind]int64{}
	if b.Users == nil {
		return out
	}
	for _, o := range b.Users.Orgs() {
		if v.admin && len(o.Limits) > 0 || !v.admin && v.p.Access != nil && v.p.Access.IsAdminOrg(o.ID) {
			c := map[limits.Kind]int64{}
			for k, n := range o.Limits {
				c[k] = n
			}
			out[o.ID] = c
		}
	}
	return out
}

// ---- POST /limits ----

// limitsPostBody is POST /limits' body.
type limitsPostBody struct {
	Tile    string `json:"tile"`
	Scope   string `json:"scope"`
	Kind    string `json:"kind"`
	Value   int64  `json:"value"`
	Source  string `json:"source"`
	Restart bool   `json:"restart"`
	Force   bool   `json:"force"`
}

func (b *Broker) apiLimitsPost(w http.ResponseWriter, r *http.Request) {
	v, ok := b.limitsActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body limitsPostBody
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {tile? | scope?, kind, value, source, restart?, force?}", limitsDocs)
		return
	}
	k, ok := parseKind(w, body.Kind)
	if !ok {
		return
	}
	switch {
	case !limits.ValidSource(body.Source):
		server.WriteError(w, http.StatusBadRequest, limits.ErrSource.Error(), limitsDocs)
		return
	case body.Value <= 0:
		server.WriteError(w, http.StatusBadRequest, "value is a positive number of "+k.Meta().Unit, limitsDocs)
		return
	}
	t, ok := b.limitTarget(w, v, body.Tile, body.Scope, k)
	if !ok || b.unreadable(w) {
		return
	}
	if !t.hasFacts {
		b.diskScopeFacts(&t)
	}
	kf := t.kf
	if body.Restart && kf.applies() && body.Value == kf.effective(entryIn(b.tlDoc(), t.key, k)) {
		b.restartAt(w, v, t, body) // "restart now" at the value that holds: records nothing
		return
	}
	if why := b.refusal(v, t, body); why.code != 0 {
		server.WriteError(w, why.code, why.msg, limitsDocs)
		return
	}
	ci := b.capInputs(k)[k]
	status, msg := b.decide(v, t, body, ci)
	if status != http.StatusOK {
		server.WriteError(w, status, msg, limitsDocs)
		return
	}
	b.afterDecision(t, body.Restart)
	slog.Info("audit", "who", v.p.From(), "method", "POST", "path", "/limits", "status", http.StatusOK, "tile", t.tile, "scope", t.scope,
		"kind", string(k), "value", body.Value, "default", kf.def, "source", body.Source, "force", body.Force, "restart", body.Restart)
	b.writeTargetRow(w, v, t)
}

// restartAt is POST /limits at the limit that already holds, with restart:
// the runtime restarts the backend so a size that applies at the next start
// (a VM's) applies now; nothing new is recorded. An approver of that value
// or one of the tile's managers.
func (b *Broker) restartAt(w http.ResponseWriter, v limitsViewer, t limitTarget, body limitsPostBody) {
	if ok, why := b.mayApprove(v, t.tile, t.kind, body.Value); !ok && !b.mayRevert(v, t.tile) {
		server.WriteError(w, http.StatusForbidden, why, limitsDocs)
		return
	}
	if f := b.tl.src.Apply; f != nil && t.tile != "" && t.kind != limits.DiskGiB {
		f(t.tile, t.kind, true)
	}
	slog.Info("audit", "who", v.p.From(), "method", "POST", "path", "/limits", "status", http.StatusOK, "tile", t.tile,
		"kind", string(t.kind), "value", body.Value, "restart", true, "recorded", false)
	b.writeTargetRow(w, v, t)
}

// refusal is why a decision is refused before the store is consulted (code
// 0: it isn't).
type refusalWhy struct {
	code int
	msg  string
}

func (b *Broker) refusal(v limitsViewer, t limitTarget, body limitsPostBody) refusalWhy {
	k, kf := t.kind, t.kf
	label := k.Meta().Label
	switch {
	case !kf.placed && kf.note != "":
		return refusalWhy{http.StatusBadRequest, t.tile + "'s " + label + " limit: " + kf.note}
	case !kf.placed:
		return refusalWhy{http.StatusBadRequest, t.tile + " has no " + label + " limit to raise (" + placementWhy(k) + ")"}
	case !kf.enforced:
		return refusalWhy{http.StatusBadRequest, label + " limits aren't enforced on this workspace: nothing to raise"}
	case body.Value <= kf.def:
		return refusalWhy{http.StatusBadRequest, fmt.Sprintf("%s is at or below the default (%s): use DELETE to go back to the default — lowering below it is per deployment (POST /deployments/limits)",
			limits.Format(k, body.Value), limits.Format(k, kf.def))}
	case kf.max > 0 && body.Value > kf.max:
		return refusalWhy{http.StatusBadRequest, fmt.Sprintf("%s is past the workspace's maximum of %s", limits.Format(k, body.Value), limits.Format(k, kf.max))}
	case body.Source == limits.SourceAsk && kf.ask <= kf.def:
		return refusalWhy{http.StatusBadRequest, t.describe() + "'s manifest asks no more " + label + " than the default: approve a raise (source raise) instead"}
	case body.Source == limits.SourceAsk && body.Value > kf.ask:
		return refusalWhy{http.StatusBadRequest, fmt.Sprintf("the manifest asks %s: approving its ask grants at most that (source raise to grant more)", limits.Format(k, kf.ask))}
	case body.Force && !v.admin:
		return refusalWhy{http.StatusForbidden, "force (past what fits the workspace) is a workspace admin's"}
	}
	if ok, why := b.mayApprove(v, t.tile, k, body.Value); !ok {
		return refusalWhy{http.StatusForbidden, why}
	}
	return refusalWhy{}
}

func placementWhy(k limits.Kind) string {
	switch k {
	case limits.MemMiB:
		return "it runs no backend outside a VM"
	case limits.VMMemMiB, limits.VMVCPUs:
		return "it runs no VM backend"
	case limits.DiskGiB:
		return "it doesn't root its scope"
	}
	return "it holds no cap:sandboxes grant"
}

func (t limitTarget) describe() string {
	if t.tile != "" {
		return t.tile
	}
	if t.scope == "" {
		return "the workspace scope"
	}
	return "scope " + t.scope
}

// decide records the decision under the store's lock, re-checking fit
// there: two approvals can't both take the same room. It answers the
// status and, on a refusal, why.
func (b *Broker) decide(v limitsViewer, t limitTarget, body limitsPostBody, ci capInput) (int, string) {
	k, kf := t.kind, t.kf
	s := &b.tl
	s.mu.Lock()
	defer s.mu.Unlock()
	f := b.tlDocLocked()
	if !s.read {
		return http.StatusInternalServerError, fmt.Sprintf("%s can't be read (%v): fix or remove it by hand", tileLimitsRel, s.bad)
	}
	cur := kf.effective(entryIn(f, t.key, k))
	if fits, why := ci.at(f, t.key).Fit(cur, body.Value); !fits && !(body.Force && v.admin) {
		return http.StatusConflict, fmt.Sprintf("%s of %s doesn't fit the workspace: %s", limits.Format(k, body.Value), k.Meta().Label, why)
	}
	next := cloneFile(f)
	bk := bookIn(next, t.key, k)
	e := bk.Limits[k]
	d := &limits.Decision{Value: body.Value, By: v.p.From(), At: time.Now().UTC().Truncate(time.Second), Source: body.Source}
	if body.Source == limits.SourceAsk {
		e.Ask = d
	} else {
		e.Set = d
	}
	bk.Limits[k] = e
	delete(bk.Hits, k) // a decision on the kind clears its hit
	if err := b.tlSaveLocked(next, true); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	return http.StatusOK, ""
}

// afterDecision runs a decision's effects, with no store lock held: the
// runtime applies it (disk: the monitor rescans, lifting a 507 now), the
// grant clamp refreshes, clients refetch.
func (b *Broker) afterDecision(t limitTarget, restart bool) {
	if t.kind == limits.DiskGiB {
		b.disk.kick()
	} else if f := b.tl.src.Apply; f != nil && t.tile != "" {
		f(t.tile, t.kind, restart)
	}
	b.RefreshLimitAsks() // the ask no longer pends; publishes for its tile
	b.publishLimits(t.tile)
}

// writeTargetRow answers a decision with the target's row ({"tile": Row};
// a scope no tile roots: its one kind).
func (b *Broker) writeTargetRow(w http.ResponseWriter, v limitsViewer, t limitTarget) {
	f := b.tlDoc()
	if !t.hasFacts {
		row := &limitRow{Scope: t.scope, ScopeKey: t.key, RootsScope: false, Limits: map[limits.Kind]*limitKind{}}
		e := entryIn(f, t.key, limits.DiskGiB)
		lk := &limitKind{Effective: t.kf.effective(e), Default: t.kf.def, Max: t.kf.max, Enforced: t.kf.enforced, Usage: t.kf.usage,
			Set: e.Set, Approved: e.Ask, Approvers: []string{limits.ApproverAdmin}, CanDecide: v.admin, CanRevert: v.admin}
		if h, ok := hitIn(f, t.key, limits.DiskGiB); ok {
			lk.Hit = &h
		}
		row.Limits[limits.DiskGiB] = lk
		server.WriteJSON(w, http.StatusOK, map[string]any{"tile": row})
		return
	}
	c, ok := b.rootComponent(t.tile)
	if !ok {
		server.WriteJSON(w, http.StatusOK, map[string]any{"tile": nil})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"tile": b.rowOf(v, b.tileFacts(c), f, nil)})
}

// ---- DELETE /limits, POST /limits/dismiss ----

func (b *Broker) apiLimitsDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := b.limitsActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	q := r.URL.Query()
	k, ok := parseKind(w, q.Get("kind"))
	if !ok {
		return
	}
	t, ok := b.limitTarget(w, v, q.Get("tile"), q.Get("scope"), k)
	if !ok || b.unreadable(w) {
		return
	}
	if !t.hasFacts {
		b.diskScopeFacts(&t)
	}
	if !b.mayRevert(v, t.tile) {
		server.WriteError(w, http.StatusForbidden, "putting "+t.describe()+"'s limits back to the default is its managers' or a workspace admin's", limitsDocs)
		return
	}
	changed, err := b.tlChange(t.key, k, func(bk *limitsBook) bool {
		if _, had := bk.Limits[k]; !had {
			return false
		}
		delete(bk.Limits, k)
		return true
	})
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error(), limitsDocs)
		return
	}
	if changed {
		b.afterDecision(t, false)
		slog.Info("audit", "who", v.p.From(), "method", "DELETE", "path", "/limits", "status", http.StatusOK, "tile", t.tile, "scope", t.scope, "kind", string(k))
	}
	b.writeTargetRow(w, v, t)
}

func (b *Broker) apiLimitsDismiss(w http.ResponseWriter, r *http.Request) {
	v, ok := b.limitsActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	q := r.URL.Query()
	k, ok := parseKind(w, q.Get("kind"))
	if !ok {
		return
	}
	t, ok := b.limitTarget(w, v, q.Get("tile"), q.Get("scope"), k)
	if !ok || b.unreadable(w) {
		return
	}
	if !t.hasFacts {
		b.diskScopeFacts(&t)
	}
	if !b.mayDecideKind(v, t.tile, k, t.kf.def) {
		server.WriteError(w, http.StatusForbidden, "dismissing a cap for everyone is an approver's (dismiss it for yourself in the shell instead)", limitsDocs)
		return
	}
	changed, err := b.tlChange(t.key, k, func(bk *limitsBook) bool {
		if _, had := bk.Hits[k]; !had {
			return false
		}
		delete(bk.Hits, k)
		return true
	})
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, err.Error(), limitsDocs)
		return
	}
	if changed {
		slog.Info("audit", "who", v.p.From(), "method", "POST", "path", "/limits/dismiss", "status", http.StatusNoContent, "tile", t.tile, "scope", t.scope, "kind", string(k))
		b.publishLimits(t.tile)
	}
	w.WriteHeader(http.StatusNoContent)
}

// limitsBook is a book being changed.
type limitsBook = limits.Book

// tlChange applies fn to a fresh copy of key's book (kind k's books) and
// saves the result when fn reports a change.
func (b *Broker) tlChange(key string, k limits.Kind, fn func(*limitsBook) bool) (bool, error) {
	s := &b.tl
	s.mu.Lock()
	defer s.mu.Unlock()
	f := b.tlDocLocked()
	if !s.read {
		return false, fmt.Errorf("%s can't be read (%v): fix or remove it by hand", tileLimitsRel, s.bad)
	}
	next := cloneFile(f)
	if !fn(bookIn(next, key, k)) {
		return false, nil
	}
	return true, b.tlSaveLocked(next, true)
}

// ---- PUT /orgs/{org}/limits ----

// apiOrgLimitsPut — PUT /orgs/{org}/limits {memMiB?: n|null, …}: an org's
// per-tile ceilings (workspace admins; null or 0 removes a kind, a kind not
// named keeps its ceiling) → {"limits": {…}}.
func (b *Broker) apiOrgLimitsPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body map[string]*int64
	if err := server.DecodeJSON(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, `need {"memMiB": 8192, "diskGiB": null, …}: each kind a number or null`, limitsDocs)
		return
	}
	change := map[limits.Kind]int64{}
	for name, n := range body {
		k := limits.Kind(name)
		if !k.Valid() {
			server.WriteError(w, http.StatusBadRequest, fmt.Sprintf("kind %q: one of memMiB, diskGiB, vmMemMiB, vmVCPUs, sbxMemMiB, sbxVCPUs, sbxDiskGiB", name), limitsDocs)
			return
		}
		change[k] = 0
		if n != nil {
			change[k] = *n
		}
	}
	org := r.PathValue("org")
	out, err := st.SetOrgLimits(org, change)
	if err != nil {
		code := http.StatusBadRequest
		if strings.HasPrefix(err.Error(), "no such org") {
			code = http.StatusNotFound
		}
		server.WriteError(w, code, err.Error(), limitsDocs)
		return
	}
	if out == nil {
		out = users.OrgLimits{}
	}
	slog.Info("audit", "who", auth.PrincipalOf(r).From(), "method", "PUT", "path", "/orgs/"+org+"/limits", "status", http.StatusOK, "limits", out)
	b.usersEvent()
	b.publishLimits("")
	server.WriteJSON(w, http.StatusOK, map[string]any{"limits": out})
}
