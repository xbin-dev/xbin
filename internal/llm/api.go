package llm

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// docs is the page every management answer names.
const docs = "/docs/llm.md"

// Register mounts the connector's routes (docs/protocol/llm.md) and installs
// the gate for LLM keys.
func (s *Service) Register(srv *server.Server) {
	s.mount(srv)
	srv.LLMKey = s.ResolveKey
}

// registrar is the part of *server.Server the routes are mounted on.
type registrar interface {
	RegisterAPI(pattern string, h http.HandlerFunc)
}

func (s *Service) mount(srv registrar) {
	// the OpenAI-compatible surface: errors in OpenAI's shape
	srv.RegisterAPI("POST /llm/v1/chat/completions", s.apiCall(sdkllm.PathChat))
	srv.RegisterAPI("POST /llm/v1/responses", s.apiCall(sdkllm.PathResponses))
	srv.RegisterAPI("GET /llm/v1/models", s.apiModels)
	srv.RegisterAPI("GET /llm/preferred", s.apiPreferred)
	// management: xbind's own error shape
	srv.RegisterAPI("GET /llm", s.apiStatus)
	srv.RegisterAPI("GET /llm/config", s.apiConfig)
	srv.RegisterAPI("PUT /llm/config", s.apiPutConfig)
	srv.RegisterAPI("PUT /llm/upstreams/{name}", s.apiPutUpstream)
	srv.RegisterAPI("DELETE /llm/upstreams/{name}", s.apiDeleteUpstream)
	srv.RegisterAPI("POST /llm/upstreams/{name}/test", s.apiTestUpstream)
	srv.RegisterAPI("GET /llm/usage", s.apiUsage)
	srv.RegisterAPI("GET /llm/quotas", s.apiQuotas)
	srv.RegisterAPI("PUT /llm/quotas", s.apiPutQuotas)
}

// apiError is a management refusal.
type apiError struct {
	status int
	msg    string
	locked []string // the settings the host's file holds
}

func (e *apiError) Error() string { return e.msg }

func badRequest(msg string) *apiError { return &apiError{status: http.StatusBadRequest, msg: msg} }

func errNoUpstream(name string) *apiError {
	return &apiError{status: http.StatusNotFound, msg: "no upstream named " + name}
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae) && len(ae.locked) > 0:
		server.WriteJSON(w, ae.status, map[string]any{"error": ae.msg, "locked": ae.locked, "reason": LockReason, "docs": docs})
	case errors.As(err, &ae):
		server.WriteError(w, ae.status, ae.msg, docs)
	case errors.Is(err, ErrVaultSealed), errors.Is(err, ErrVaultLocked):
		server.WriteError(w, http.StatusConflict, err.Error(), docs)
	default:
		server.WriteError(w, http.StatusInternalServerError, err.Error(), docs)
	}
}

// lockedErr refuses a write to settings the host's file holds.
func lockedErr(paths ...string) *apiError {
	what := paths[0]
	if len(paths) > 1 {
		what = strings.Join(paths, ", ")
	}
	return &apiError{status: http.StatusConflict, locked: paths,
		msg: what + ": " + LockReason + " — it can't be changed here (the host's deployment file, XBIN_LLM_CONFIG, holds it)"}
}

// admin passes a workspace admin: an admin's own sign-in, the owner, or the
// admin console (a tile holding xbin:admin). A view-as session reads and
// changes nothing.
func (s *Service) admin(w http.ResponseWriter, r *http.Request) bool {
	p := auth.PrincipalOf(r)
	if s.opt.IsAdmin == nil || !s.opt.IsAdmin(p) || p.Via == "llm" {
		server.WriteError(w, http.StatusForbidden, "the LLM connector's settings are a workspace admin's", docs)
		return false
	}
	if p.ReadOnly() && r.Method != http.MethodGet {
		server.WriteError(w, http.StatusForbidden, "viewing as someone changes nothing", docs)
		return false
	}
	return true
}

// change applies f to a copy of the admin's settings and, when the result
// is valid, saves it and puts it in force. snap is the configuration in
// force before, for f's lock checks.
func (s *Service) change(r *http.Request, f func(next *Settings, snap *snapshot) error) error {
	snap := s.current()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErr != nil {
		return &apiError{status: http.StatusInternalServerError, msg: s.storeErr.Error()}
	}
	next := s.store.Settings.clone()
	if err := f(&next, snap); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return badRequest(err.Error())
	}
	file := s.store
	file.Settings, file.Set, file.By = next, s.now().Unix(), auth.PrincipalOf(r).From()
	if err := saveStore(s.opt.Dir, file); err != nil {
		return err
	}
	s.store = file
	s.rebuild()
	return nil
}

// decode reads a JSON body strictly; an empty body is an empty object.
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ---- the OpenAI-compatible reads ---------------------------------------------

// apiModels — GET /llm/v1/models.
func (s *Service) apiModels(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	c, ref := s.admit(r, snap)
	if ref != nil {
		ref.write(w)
		return
	}
	list, errs := s.listModels(r.Context(), snap, c)
	out := map[string]any{"object": list.Object, "data": list.Data}
	if len(errs) > 0 {
		if !c.Admin { // an upstream's address and its failure are an admin's to read
			for name := range errs {
				errs[name] = "its model list is unavailable"
			}
		}
		out["upstream_errors"] = errs
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// apiPreferred — GET /llm/preferred[?use=<role>]: the workspace's default
// models, in the shape the llm-gw tile answers, so a tile that resolves its
// default model from its provider works bound to either.
func (s *Service) apiPreferred(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	if _, ref := s.admit(r, snap); ref != nil {
		ref.write(w)
		return
	}
	named := func(role string) string {
		u, m, err := snap.resolve(pseudoUpstream + "/" + role)
		if err != nil {
			return ""
		}
		return publicName(u, m)
	}
	if use := r.URL.Query().Get("use"); use != "" {
		if !roleNameRE.MatchString(use) {
			server.WriteJSON(w, http.StatusOK, map[string]any{"use": use, "model": named(RoleDefault)})
			return
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"use": use, "model": named(use)})
		return
	}
	pref := map[string]string{}
	roles := append([]string{RoleDefault}, slices.Sorted(maps.Keys(snap.defaults.Roles))...)
	for _, role := range roles {
		if m := named(role); m != "" {
			pref[role] = m
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"preferred": pref, "useTypes": roles})
}

// ---- status -------------------------------------------------------------------

// apiStatus — GET /llm: the connector as the caller meets it. Always 200:
// a caller that may not call reads why.
func (s *Service) apiStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	v := StatusView{Configured: snap.configured(), Enabled: snap.enabled, URL: BaseURL, Ref: Ref, People: snap.people, Host: snap.host,
		Defaults: Defaults{Model: snap.defaults.Model, Roles: maps.Clone(snap.defaults.Roles)}}
	c, ref := s.admit(r, snap)
	if ref != nil {
		v.Reason, v.Code = ref.err.Message, ref.err.Code.(string)
		if id, _ := s.identify(r, snap); id.User != "" || id.Component != "" {
			v.User, v.Component = id.User, id.Component
		}
	} else {
		v.Callable, v.User, v.Component, v.Admin = c.ReadOnly == "", c.User, c.Component, c.Admin
		if c.ReadOnly != "" { // a non-primary deployment under the read clamp: models, not calls
			v.Reason, v.Code = c.ReadOnly, "xbin_llm_edge"
		}
		q := s.quotaView(snap, c)
		v.Quota = &q
	}
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, v)
}

// ---- configuration --------------------------------------------------------------

func (s *Service) apiConfig(w http.ResponseWriter, r *http.Request) {
	if s.admin(w, r) {
		w.Header().Set("Cache-Control", "no-store")
		server.WriteJSON(w, http.StatusOK, s.configView())
	}
}

// apiPutConfig — PUT /llm/config {enabled?, people?, defaults?: {model?,
// roles?: {<role>: model | ""}}, aliases?: {<alias>: target | ""}}: only
// what is named changes; "" removes a role, an alias or the default.
func (s *Service) apiPutConfig(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var body struct {
		Enabled  *bool   `json:"enabled"`
		People   *string `json:"people"`
		Defaults *struct {
			Model *string           `json:"model"`
			Roles map[string]string `json:"roles"`
		} `json:"defaults"`
		Aliases     map[string]string `json:"aliases"`
		Concurrency *int              `json:"concurrency"`
	}
	if err := decode(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {enabled?, people?: admins|everyone|nobody, defaults?: {model?, roles?: {<role>: <model>}}, aliases?: {<alias>: <model>}, concurrency?}", docs)
		return
	}
	err := s.change(r, func(next *Settings, snap *snapshot) error {
		var locked []string
		hold := func(path string, same bool) bool { // a locked setting may be sent back as it is
			if snap.locked(path) && !same {
				locked = append(locked, path)
			}
			return snap.locked(path)
		}
		if body.Enabled != nil && !hold("enabled", *body.Enabled == snap.enabled) {
			next.Enabled = body.Enabled
		}
		if body.People != nil && !hold("people", *body.People == snap.people) {
			if *body.People == "" {
				return badRequest("people: admins, everyone or nobody")
			}
			next.People = *body.People
		}
		if d := body.Defaults; d != nil {
			if d.Model != nil && !hold("defaults.model", *d.Model == snap.defaults.Model) {
				next.Defaults.Model = *d.Model
			}
			for _, role := range slices.Sorted(maps.Keys(d.Roles)) {
				m := d.Roles[role]
				if hold("defaults.roles."+role, m == snap.defaults.Roles[role]) {
					continue
				}
				if next.Defaults.Roles == nil {
					next.Defaults.Roles = map[string]string{}
				}
				if m == "" {
					delete(next.Defaults.Roles, role)
				} else {
					next.Defaults.Roles[role] = m
				}
			}
		}
		for _, a := range slices.Sorted(maps.Keys(body.Aliases)) {
			t := body.Aliases[a]
			if hold("aliases."+a, t == snap.aliases[a]) {
				continue
			}
			if next.Aliases == nil {
				next.Aliases = map[string]string{}
			}
			if t == "" {
				delete(next.Aliases, a)
				continue
			}
			// an alias adds a name; it never takes over a model of an
			// upstream in force — the admin's or the host's
			if up := aliasUpstream(a, snap.upstreams); up != "" {
				return badRequest(shadowErr(a, up).Error())
			}
			if a != AliasAny && len(snap.offering(a)) > 0 {
				return badRequest("alias " + a + ": the upstream " + snap.offering(a)[0] + " lists a model of that name — an alias can't stand in for one")
			}
			next.Aliases[a] = t
		}
		if body.Concurrency != nil && !hold("concurrency", *body.Concurrency == snap.concurrency) {
			next.Concurrency = *body.Concurrency
		}
		if len(locked) > 0 {
			return lockedErr(locked...)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, s.configView())
}

// validKey: an upstream key is 1–4096 printable characters without
// whitespace.
func validKey(k string) error {
	if len(k) > 4096 {
		return errors.New("key: at most 4096 characters")
	}
	for _, r := range k {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return errors.New("key: printable ASCII, no whitespace")
		}
	}
	return nil
}

// apiPutUpstream — PUT /llm/upstreams/{name} {baseURL?, key?, models?,
// private?, prices?, kind?}: add an upstream (baseURL needed) or change
// one; what isn't named stays. key goes to the vault ("" removes it) and is
// never answered back.
func (s *Service) apiPutUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	name := r.PathValue("name")
	var notices []string
	var body struct {
		Kind    *string            `json:"kind"`
		BaseURL *string            `json:"baseURL"`
		Key     *string            `json:"key"`
		Models  *[]string          `json:"models"`
		Private *bool              `json:"private"`
		Prices  *sdkllm.PriceTable `json:"prices"`
	}
	if err := decode(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {baseURL?, key?, models?: [<model>], private?, prices?: {<model>: {in, out, cachedIn?}}, kind?: openai}", docs)
		return
	}
	if body.Key != nil {
		*body.Key = strings.TrimSpace(*body.Key)
		if err := validKey(*body.Key); err != nil {
			server.WriteError(w, http.StatusBadRequest, err.Error(), docs)
			return
		}
	}
	err := s.change(r, func(next *Settings, snap *snapshot) error {
		if snap.locked("upstreams." + name) {
			return lockedErr("upstreams." + name)
		}
		u, exists := next.Upstreams[name]
		if !exists && snap.lockAll {
			return lockedErr("upstreams")
		}
		if !exists && body.BaseURL == nil {
			return badRequest("a new upstream needs baseURL (for example https://api.openai.com/v1)")
		}
		if body.Kind != nil {
			u.Kind = *body.Kind
		}
		moved := false
		if body.BaseURL != nil {
			was := u.BaseURL
			u.BaseURL = strings.TrimRight(strings.TrimSpace(*body.BaseURL), "/")
			moved = exists && !sameAddress(was, u.BaseURL)
		}
		if body.Models != nil {
			u.Models = *body.Models
		}
		if body.Private != nil {
			u.Private = *body.Private
		}
		if body.Prices != nil {
			u.Prices = *body.Prices
		}
		if err := validUpstream(name, u); err != nil {
			return badRequest(err.Error())
		}
		switch {
		case body.Key != nil && *body.Key != "":
			if err := s.keys.set(name, *body.Key); err != nil {
				return err
			}
			u.HasKey = true
		case moved && u.HasKey:
			// the key was given for another address: it never follows the
			// upstream to a new scheme, host or port. (While the vault is
			// sealed it can't be cut out of the sealed file; unmarked here,
			// it is not used again.)
			_ = s.keys.set(name, "")
			u.HasKey = false
			notices = append(notices, "the key of upstream "+name+" was removed: its address changed, and a key is only ever sent to the address it was set for — set it again (bx llm upstream set "+name+" --key-stdin)")
		case body.Key != nil || !exists: // key "": remove it; a new upstream never inherits a key left under its name
			if err := s.keys.set(name, ""); err != nil && (u.HasKey || body.Key != nil) {
				return err
			}
			u.HasKey = false
		}
		// a new upstream's name takes its models' names from any alias written under it
		for a := range next.Aliases {
			if !exists && aliasUpstream(a, map[string]Upstream{name: u}) != "" {
				return badRequest("upstream " + name + ": the alias " + a + " is named like one of its models — remove the alias first (bx llm alias rm " + a + ")")
			}
		}
		if next.Upstreams == nil {
			next.Upstreams = map[string]Upstream{}
		}
		next.Upstreams[name] = u
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	v := s.configView()
	v.Notices = notices
	server.WriteJSON(w, http.StatusOK, v)
}

// sameAddress reports whether two base URLs name the same scheme, host and
// port: where a key goes.
func sameAddress(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return ua.Scheme == ub.Scheme && strings.EqualFold(ua.Hostname(), ub.Hostname()) && port(ua) == port(ub)
}

// apiDeleteUpstream — DELETE /llm/upstreams/{name}: the upstream and its
// key go. Usage already metered under its name stays.
func (s *Service) apiDeleteUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	name := r.PathValue("name")
	err := s.change(r, func(next *Settings, snap *snapshot) error {
		if snap.locked("upstreams." + name) {
			return lockedErr("upstreams." + name)
		}
		if _, ok := next.Upstreams[name]; !ok {
			return errNoUpstream(name)
		}
		delete(next.Upstreams, name)
		// while the vault is sealed the key can't be cut out of the sealed
		// file: it stays there unused, and a new upstream of the same name
		// never takes it (apiPutUpstream)
		_ = s.keys.set(name, "")
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, s.configView())
}

// apiTestUpstream — POST /llm/upstreams/{name}/test {model?}.
func (s *Service) apiTestUpstream(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := decode(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {model?}", docs)
		return
	}
	// metered to the admin who runs it: their own sign-in, the owner, or
	// the person driving the admin console
	p := auth.PrincipalOf(r)
	who := Caller{User: p.UserID, Component: p.Component, Via: "test", Admin: true}
	switch {
	case p.Component != "":
		who.User = actor(p)
	case p.Owner:
		who.User = OwnerUser
	}
	res, err := s.Test(r.Context(), r.PathValue("name"), body.Model, who)
	if err != nil {
		writeErr(w, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, res)
}

// ---- usage ----------------------------------------------------------------------

// apiUsage — GET /llm/usage[?period=day|month][&from=&to=][&user=][&component=][&model=]:
// a person reads their own rows; a workspace admin every row.
func (s *Service) apiUsage(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	q := r.URL.Query()
	admin := s.opt.IsAdmin != nil && s.opt.IsAdmin(p) && p.Via != "llm"
	user := q.Get("user")
	scope := "all"
	if !admin {
		self := p.UserID
		if p.Component != "" || self == "" || p.Via == "llm" {
			server.WriteError(w, http.StatusForbidden, "usage is read by the person it is about (their own sign-in) or a workspace admin", docs)
			return
		}
		if user != "" && user != self {
			server.WriteError(w, http.StatusForbidden, "another person's usage is a workspace admin's to read", docs)
			return
		}
		user, scope = self, "own"
	}
	now := s.now().UTC()
	period := q.Get("period")
	from, to := q.Get("from"), q.Get("to")
	switch period {
	case "", "day":
		period = "day"
		if from == "" {
			from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format(dayFmt)
		}
	case "month":
		if from == "" {
			from = time.Date(now.Year(), now.Month()-11, 1, 0, 0, 0, 0, time.UTC).Format(dayFmt)
		}
	default:
		server.WriteError(w, http.StatusBadRequest, "period: day or month", docs)
		return
	}
	if to == "" {
		to = now.Format(dayFmt)
	}
	f, err1 := time.Parse(dayFmt, from)
	t, err2 := time.Parse(dayFmt, to)
	if err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > 5*366*24*time.Hour {
		server.WriteError(w, http.StatusBadRequest, "from and to: YYYY-MM-DD (UTC days), from first, at most five years apart", docs)
		return
	}
	component, model := q.Get("component"), q.Get("model")
	_, byUser := q["user"]
	rows := s.meter.query(from, to, period == "month", func(u, c, m string) bool {
		return (!(byUser || !admin) || u == user) && (component == "" || c == component) && (model == "" || m == model)
	})
	if rows == nil {
		rows = []UsageRow{}
	}
	var totals Counters
	for _, row := range rows {
		totals.add(row.Counters)
	}
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, map[string]any{"period": period, "from": from, "to": to, "scope": scope, "rows": rows, "totals": totals})
}

// ---- quotas ---------------------------------------------------------------------

// apiQuotas — GET /llm/quotas[?user=<id>]: the rules in force and their
// locks; with user, that person's limits as they stand.
func (s *Service) apiQuotas(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	snap := s.current()
	locks := map[string]Lock{}
	for _, part := range []string{"workspace", "default", "users", "orgs"} {
		if snap.locked("quotas." + part) {
			locks["quotas."+part] = lockOf()
		}
	}
	out := map[string]any{"quotas": snap.quotas.clone(), "locks": locks}
	if user := r.URL.Query().Get("user"); user != "" {
		out["effective"] = s.quotaView(snap, Caller{User: user})
	}
	w.Header().Set("Cache-Control", "no-store")
	server.WriteJSON(w, http.StatusOK, out)
}

// apiPutQuotas — PUT /llm/quotas {workspace?: Quota|null, default?:
// Quota|null, users?: {<id>: Quota|null}, orgs?: {<id>: Quota|null}}: what
// is named changes; null (or an empty quota) removes it.
func (s *Service) apiPutQuotas(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var body struct {
		Workspace json.RawMessage            `json:"workspace"`
		Default   json.RawMessage            `json:"default"`
		Users     map[string]json.RawMessage `json:"users"`
		Orgs      map[string]json.RawMessage `json:"orgs"`
	}
	const need = "need {workspace?, default?, users?: {<id>: quota}, orgs?: {<id>: quota}}; a quota is {tokensPerDay?, tokensPerMonth?, costPerDay?, costPerMonth?, action?: stop|warn} or null"
	if err := decode(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, need, docs)
		return
	}
	one := func(raw json.RawMessage) (*Quota, error) { // nil: remove
		if string(raw) == "null" {
			return nil, nil
		}
		var q Quota
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&q); err != nil {
			return nil, badRequest(need)
		}
		if q.empty() {
			return nil, nil
		}
		return &q, nil
	}
	many := func(dst *map[string]Quota, src map[string]json.RawMessage) error {
		for id, raw := range src {
			q, err := one(raw)
			if err != nil {
				return err
			}
			if q == nil {
				delete(*dst, id)
				continue
			}
			if *dst == nil {
				*dst = map[string]Quota{}
			}
			(*dst)[id] = *q
		}
		return nil
	}
	err := s.change(r, func(next *Settings, snap *snapshot) error {
		var locked []string
		for part, named := range map[string]bool{"workspace": body.Workspace != nil, "default": body.Default != nil, "users": body.Users != nil, "orgs": body.Orgs != nil} {
			if named && snap.locked("quotas."+part) {
				locked = append(locked, "quotas."+part)
			}
		}
		if len(locked) > 0 {
			slices.Sort(locked)
			return lockedErr(locked...)
		}
		var err error
		if body.Workspace != nil {
			if next.Quotas.Workspace, err = one(body.Workspace); err != nil {
				return err
			}
		}
		if body.Default != nil {
			if next.Quotas.Default, err = one(body.Default); err != nil {
				return err
			}
		}
		if err := many(&next.Quotas.Users, body.Users); err != nil {
			return err
		}
		return many(&next.Quotas.Orgs, body.Orgs)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.apiQuotas(w, r)
}
