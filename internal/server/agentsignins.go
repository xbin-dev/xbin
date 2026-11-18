package server

// agentsignins.go — the coding-agent sign-ins xbind holds for each person
// (D230; the store: internal/signins; docs/agent-signins.md, docs/protocol/
// signins.md):
//
//	GET    /api/xbin/agent/signins                     the caller's sign-ins — metadata only
//	POST   /api/xbin/agent/signins                     save a pasted key or token
//	PUT    /api/xbin/agent/signins/{id}                rename, make default, replace the secret
//	DELETE /api/xbin/agent/signins/{id}                forget one
//	DELETE /api/xbin/agent/signins/notice              the notice was read
//	GET    /api/xbin/agent/signins/uses                where they were used
//	POST   /api/xbin/agent/signins/guided              start the guided sign-in (agentsigninflow.go)
//	POST   /api/xbin/agent/signins/guided/{flow}       the code
//	DELETE /api/xbin/agent/signins/guided/{flow}       cancel
//	GET    /api/xbin/agent/signins/people              admins: who has sign-ins — that, not what
//	DELETE /api/xbin/agent/signins/people/{user}/{id}  admins: revoke one
//	PUT    /api/xbin/agent/signins/policy              admins: the workspace's switch
//
// The person's routes are a person's own act (partitionclass.go:
// PersonOnly — their session, app or device; never a tile's credentials, a
// terminal's or an agent session's token, view-as, the root token or their
// workspace agent). No answer of any route carries a secret or a part of
// one: the store hands one to xbind's own session start and to nothing
// else (signins.Store.Use).

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/term"
)

const agentSigninsDocs = "/docs/protocol/signins.md"

// SigninsAvailability is whether this xbind keeps sign-ins at all.
type SigninsAvailability struct {
	OK bool
	// Why not, for a person to read.
	Why string
	// Unisolated: it works without sandboxes because this is a development
	// workspace (--dev, --no-auth): everything xbind runs could read a
	// session's environment.
	Unisolated bool
}

func (s *Server) registerAgentSigninsAPI() {
	s.RegisterAPI("GET /agent/signins", s.apiSigninsGet)
	s.RegisterAPI("POST /agent/signins", s.apiSigninsPost)
	s.RegisterAPI("PUT /agent/signins/{id}", s.apiSigninsPut)
	s.RegisterAPI("DELETE /agent/signins/{id}", s.apiSigninsDelete)
	s.RegisterAPI("DELETE /agent/signins/notice", s.apiSigninsNotice)
	s.RegisterAPI("GET /agent/signins/uses", s.apiSigninsUses)
	s.RegisterAPI("POST /agent/signins/guided", s.apiSigninsGuidedStart)
	s.RegisterAPI("POST /agent/signins/guided/{flow}", s.apiSigninsGuidedCode)
	s.RegisterAPI("DELETE /agent/signins/guided/{flow}", s.apiSigninsGuidedCancel)
	s.RegisterAPI("GET /agent/signins/people", s.apiSigninsPeople)
	s.RegisterAPI("DELETE /agent/signins/people/{user}/{id}", s.apiSigninsRevoke)
	s.RegisterAPI("PUT /agent/signins/policy", s.apiSigninsPolicyPut)
	s.RegisterAPI("POST /agent/signins/trust", s.apiSigninsTrust)
	s.RegisterAPI("DELETE /agent/signins/trust/{id}", s.apiSigninsUntrust)
	s.RegisterAPI("DELETE /agent/signins/homes/{id}", s.apiSigninsHomeForget) // signinshome.go (D245)
}

// signinsAvail is whether sign-ins are kept here, and why not.
func (s *Server) signinsAvail() SigninsAvailability {
	if s.Signins == nil {
		return SigninsAvailability{Why: "this xbind keeps no coding-agent sign-ins"}
	}
	if s.SigninsAvail != nil {
		return s.SigninsAvail()
	}
	return SigninsAvailability{OK: true}
}

// signinsPerson is the person the request is, or "" after it answered why
// there is none. The partition table already refused every credential that
// isn't a person's own; this holds when the handler is reached some other
// way.
func (s *Server) signinsPerson(w http.ResponseWriter, r *http.Request) string {
	p := auth.PrincipalOf(r)
	switch {
	case s.Signins == nil:
		WriteError(w, http.StatusNotImplemented, "this xbind keeps no coding-agent sign-ins", agentSigninsDocs)
	case p.Component != "" || p.ReadOnly() || p.UserID == "" || p.Agent != "" || p.Via == "terminal" || p.Via == "instance" || p.Via == "frame":
		WriteError(w, http.StatusForbidden, "saved sign-ins are a person's own: sign in and manage them yourself — a tile's credentials, a session's token and view-as can't", agentSigninsDocs)
	default:
		return p.UserID
	}
	return ""
}

// signinsWritable is signinsPerson for a route that changes or makes a
// sign-in: also refused where none are kept.
func (s *Server) signinsWritable(w http.ResponseWriter, r *http.Request) string {
	user := s.signinsPerson(w, r)
	if user == "" {
		return ""
	}
	if av := s.signinsAvail(); !av.OK {
		WriteError(w, http.StatusConflict, av.Why, agentSigninsDocs)
		return ""
	}
	return user
}

// writeSigninsErr answers a store's error.
func writeSigninsErr(w http.ResponseWriter, err error) {
	var e *signins.Error
	if errors.As(err, &e) {
		WriteError(w, e.Status, e.Msg, agentSigninsDocs)
		return
	}
	slog.Error("signins", "err", err)
	WriteError(w, http.StatusInternalServerError, "the saved sign-ins could not be read or written (xbind's log says why)", agentSigninsDocs)
}

// signinHarness is one coding agent in GET /agent/signins.
type signinHarness struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Guided: xbind can run this coding agent's own sign-in (its Mint) and
	// keep what it prints. false: paste a key.
	Guided bool `json:"guided"`
	// Keys are the kinds of credential it takes when pasted, in the order
	// a pasted value is matched (by its prefix).
	Keys []agentKey `json:"keys"`
	// Login is the line that signs it in on one tile, in a terminal.
	Login   string           `json:"login"`
	Max     int              `json:"max"`
	Signins []signins.Signin `json:"signins"`
	// Here, with ?tile=: whether a session of the caller's on that tile
	// would get a saved sign-in of this coding agent (term's rule) — and
	// why not. Absent without a tile, or for one they can't open a
	// terminal on.
	Here *signinHere `json:"here,omitempty"`
}

type signinHere struct {
	Inject bool   `json:"inject"`
	Why    string `json:"why,omitempty"`
	// Trust: it would be left out only because the tile's source isn't
	// only the caller's own — the question the card asks (signinstrust.go).
	Trust *term.SigninTrust `json:"trust,omitempty"`
	// Without: what a session there runs on without a saved sign-in — "own"
	// (the tile's own login, as a session last saw it), "connector" (the
	// workspace's model), "none" (it starts signed out).
	Without string `json:"without,omitempty"`
}

type agentKey struct {
	Env   string `json:"env"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// signinsView is GET /agent/signins' answer.
type signinsView struct {
	Available  bool            `json:"available"`
	Why        string          `json:"why,omitempty"`
	Unisolated bool            `json:"unisolated,omitempty"`
	Vault      string          `json:"vault"`
	Policy     signins.Policy  `json:"policy"`
	Notice     *signins.Notice `json:"notice,omitempty"`
	Harnesses  []signinHarness `json:"harnesses"`
	Flows      []signins.Flow  `json:"flows"`
	// Trusted: the tiles whose source the caller said they trust.
	Trusted []trustRow `json:"trusted"`
	// Homes: the copies kept of the caller's home, one per tile whose
	// source they had not trusted when a session of theirs ran there
	// (signinshome.go, D245). Home, with ?tile=: which home a session of
	// theirs on that tile would mount now.
	Homes []homeCopyRow `json:"homes"`
	Home  *homeHere     `json:"home,omitempty"`
	// Admin: the caller may see who has sign-ins and set the switch.
	Admin bool `json:"admin,omitempty"`
	// Kept, for admins: the copies of homes kept from accounts that no
	// longer exist (xbind removes no copy on its own) — theirs to forget
	// (DELETE /agent/signins/homes/{id}?of=<user>).
	Kept []keptHomeRow `json:"kept,omitempty"`
}

func (s *Server) signinsViewFor(r *http.Request, user string) (signinsView, error) {
	av := s.signinsAvail()
	out := signinsView{Available: av.OK, Why: av.Why, Unisolated: av.Unisolated, Vault: s.Signins.VaultState(),
		Policy: s.Signins.Policy(), Harnesses: []signinHarness{}, Flows: []signins.Flow{}, Trusted: s.trustedRows(user), Homes: []homeCopyRow{}, Admin: s.settingsAdmin(r)}
	all, notice, err := s.Signins.List(user)
	if err != nil {
		return out, err
	}
	out.Notice = notice
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	out.Homes = s.homeCopyRows(auth.PrincipalOf(r))
	if out.Admin {
		out.Kept = s.keptHomeRows()
	}
	if p := auth.PrincipalOf(r); tile != "" && s.Term != nil && p.CanTerminalTile(s.sessionTile(tile)) {
		out.Home = s.homeHereFor(p, s.sessionTile(tile), out.Homes) // the tile the path lies in, as for a session there
	}
	for _, prov := range agent.Providers() {
		if len(prov.Keys) == 0 {
			continue // it takes no credential xbind could keep
		}
		h := signinHarness{ID: prov.ID, Name: prov.Name, Guided: prov.Mint != nil, Login: prov.Login, Max: signins.MaxPerHarness,
			Keys: []agentKey{}, Signins: []signins.Signin{}}
		for _, k := range prov.Keys {
			h.Keys = append(h.Keys, agentKey{Env: k.Env, Label: k.Label, Kind: k.Kind})
		}
		for _, sg := range all {
			if sg.Harness == prov.ID {
				h.Signins = append(h.Signins, sg)
			}
		}
		// (the caller's level is the tile's the path lies in, as for a session there: sessionTile)
		if p := auth.PrincipalOf(r); tile != "" && s.Term != nil && p.CanTerminalTile(s.sessionTile(tile)) {
			why, trust := s.Term.SigninRule(p, tile, prov)
			h.Here = &signinHere{Inject: why == "", Why: why, Trust: trust, Without: s.withoutSignin(p, tile, prov)}
		}
		out.Harnesses = append(out.Harnesses, h)
	}
	if s.SigninFlows != nil {
		out.Flows = s.SigninFlows.Of(user)
	}
	return out, nil
}

func (s *Server) apiSigninsGet(w http.ResponseWriter, r *http.Request) {
	user := s.signinsTruster(w, r, false) // (the root token: its trusted tiles, its home and copies — signinsroot.go)
	if user == "" {
		return
	}
	v, err := s.signinsViewFor(r, user)
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

// apiSigninsPost — POST /agent/signins {harness, secret, name?, default?}:
// a pasted key or token. → 201 {signin}.
func (s *Server) apiSigninsPost(w http.ResponseWriter, r *http.Request) {
	user := s.signinsWritable(w, r)
	if user == "" {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Harness string `json:"harness"`
		Secret  string `json:"secret"`
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	if err := DecodeJSON(r, &body); err != nil || body.Harness == "" {
		WriteError(w, http.StatusBadRequest, "need {harness, secret, name?, default?}", agentSigninsDocs)
		return
	}
	sg, replaced, err := s.Signins.Save(user, body.Harness, body.Name, body.Secret, body.Default)
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	s.signinSaved(r, user, sg, replaced, "pasted")
	WriteJSON(w, http.StatusCreated, map[string]any{"signin": sg})
}

// signinSaved audits a saved sign-in and, when it took another secret's
// place, ends the sessions that still run on the old one.
func (s *Server) signinSaved(r *http.Request, user string, sg signins.Signin, replaced bool, how string) {
	slog.Info("audit", "event", "signin.save", "user", user, "harness", sg.Harness, "signin", sg.ID, "name", sg.Name, "kind", sg.Kind, "how", how, "replaced", replaced)
	if replaced {
		s.endSigninSessions(user, sg.ID, "Your saved sign-in "+sg.Name+" was replaced: this session ran on the old one and was ended. Start it again to use the new one.")
	}
}

// endSigninSessions ends user's running agent sessions that carry saved
// sign-in id ("" : any) and answers how many.
func (s *Server) endSigninSessions(user, id, why string) int {
	if s.Term == nil {
		return 0
	}
	return s.Term.EndSigninSessions(user, id, why)
}

// apiSigninsPut — PUT /agent/signins/{id} {name?, default?: true, secret?}.
func (s *Server) apiSigninsPut(w http.ResponseWriter, r *http.Request) {
	user := s.signinsWritable(w, r)
	if user == "" {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var p signins.Patch
	if err := DecodeJSON(r, &p); err != nil {
		WriteError(w, http.StatusBadRequest, "need {name?, default?: true, secret?}", agentSigninsDocs)
		return
	}
	sg, replaced, err := s.Signins.Update(user, r.PathValue("id"), p)
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	if p.Secret != nil {
		s.signinSaved(r, user, sg, replaced, "pasted")
	}
	WriteJSON(w, http.StatusOK, map[string]any{"signin": sg})
}

// apiSigninsDelete — DELETE /agent/signins/{id}: forget it, and end the
// sessions that run on it (they hold a copy). → {ok, ended}.
func (s *Server) apiSigninsDelete(w http.ResponseWriter, r *http.Request) {
	user := s.signinsPerson(w, r)
	if user == "" {
		return
	}
	sg, err := s.Signins.Remove(user, r.PathValue("id"))
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	slog.Info("audit", "event", "signin.remove", "user", user, "harness", sg.Harness, "signin", sg.ID, "name", sg.Name, "by", user)
	ended := s.endSigninSessions(user, sg.ID, "Your saved sign-in "+sg.Name+" was removed, so this session was ended.")
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "ended": ended})
}

func (s *Server) apiSigninsNotice(w http.ResponseWriter, r *http.Request) {
	if user := s.signinsPerson(w, r); user != "" {
		s.Signins.DismissNotice(user)
		WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) apiSigninsUses(w http.ResponseWriter, r *http.Request) {
	if user := s.signinsPerson(w, r); user != "" {
		WriteJSON(w, http.StatusOK, map[string]any{"uses": s.Signins.Uses(user)})
	}
}

// ---- admins: that people have sign-ins, never what -------------------------------

// signinsAdmin reports a workspace admin acting themselves (never view-as,
// never a delegate), after answering why not.
func (s *Server) signinsAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := auth.PrincipalOf(r)
	switch {
	case s.Signins == nil:
		WriteError(w, http.StatusNotImplemented, "this xbind keeps no coding-agent sign-ins", agentSigninsDocs)
	case p.ReadOnly() || p.Agent != "" || !s.settingsAdmin(r):
		WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability (docs/auth.md)", agentSigninsDocs)
	default:
		return true
	}
	return false
}

// apiSigninsPeople — GET /agent/signins/people: {policy, vault, people:
// [{user, total, signins: [{id, harness, kind, createdAt, lastUsedAt?,
// expiresAt?, default}]}]} — no name, no secret.
func (s *Server) apiSigninsPeople(w http.ResponseWriter, r *http.Request) {
	if !s.signinsAdmin(w, r) {
		return
	}
	people := s.Signins.People()
	if people == nil {
		people = []signins.PersonRow{}
	}
	av := s.signinsAvail()
	WriteJSON(w, http.StatusOK, map[string]any{"available": av.OK, "why": av.Why, "policy": s.Signins.Policy(), "vault": s.Signins.VaultState(), "people": people})
}

// apiSigninsRevoke — DELETE /agent/signins/people/{user}/{id}: an admin
// removes one of a person's sign-ins; its sessions end; the person's list
// says who did it.
func (s *Server) apiSigninsRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.signinsAdmin(w, r) {
		return
	}
	p := auth.PrincipalOf(r)
	by := p.UserID
	if by == "" {
		by = "the workspace's owner"
	}
	user := r.PathValue("user")
	sg, err := s.Signins.Revoke(user, r.PathValue("id"), by)
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	slog.Info("audit", "event", "signin.revoke", "user", user, "harness", sg.Harness, "signin", sg.ID, "by", p.From())
	ended := s.endSigninSessions(user, sg.ID, "Your saved sign-in "+sg.Name+" was removed by "+by+", so this session was ended.")
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "ended": ended})
}

// apiSigninsPolicyPut — PUT /agent/signins/policy {enabled}: the
// workspace's switch, and {trust}: whether people may trust a tile whose
// source isn't only their own. 409 while the host's file holds the one
// written. Off ends the running sessions that carry a saved sign-in (trust
// off: the ones on tiles that needed it) → {policy, ended}.
func (s *Server) apiSigninsPolicyPut(w http.ResponseWriter, r *http.Request) {
	if !s.signinsAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body struct {
		Enabled *bool `json:"enabled"`
		Trust   *bool `json:"trust"`
	}
	if err := DecodeJSON(r, &body); err != nil || body.Enabled == nil && body.Trust == nil {
		WriteError(w, http.StatusBadRequest, "need {enabled?: bool, trust?: bool}", agentSigninsDocs)
		return
	}
	old := s.Signins.Policy()
	cur := old
	var err error
	if body.Enabled != nil {
		if cur, err = s.Signins.SetPolicy(*body.Enabled); err != nil {
			writeSigninsErr(w, err)
			return
		}
	}
	if body.Trust != nil {
		if cur, err = s.Signins.SetTrustPolicy(*body.Trust); err != nil {
			writeSigninsErr(w, err)
			return
		}
	}
	ended := 0
	// off, or trusting forbidden: the running sessions that carry a sign-in
	// and may no longer end, as when one is removed
	if (!cur.Enabled || !cur.Trust.Allowed) && s.Term != nil {
		ended = s.Term.SigninSweep()
	}
	slog.Info("audit", "event", "signin.policy", "who", auth.PrincipalOf(r).From(), "enabled", cur.Enabled, "was", old.Enabled,
		"trust", cur.Trust.Allowed, "trustWas", old.Trust.Allowed, "ended", ended)
	WriteJSON(w, http.StatusOK, map[string]any{"policy": cur, "ended": ended})
}
