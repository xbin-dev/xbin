// scm_routes.go — the agent's scm routes (API.md §scm providers and
// credentials): the bound providers as this home sees them, the repos a
// caller may name, the scm bot rule, and a person's sign-in.
//
//	GET    /projects/scm                     every bound provider's hello, as this home sees it
//	GET    /projects/scm/repos?scm=&q=&cursor=  the repos (at a bot home: those the caller may name)
//	GET    /projects/scm/bot                 the scm bot rule (managers)
//	PUT    /projects/scm/bot                 set it (managers; 409 in a person's partition)
//	GET    /projects/scm/signin?scm=         a person's sign-in state (starts nothing)
//	POST   /projects/scm/signin {scm}        start (or continue) one
//	GET    /projects/scm/signin/{pollId}?scm=  how it went
//	DELETE /projects/scm/signin?scm=         Forget: every credential of the person's projects scrubbed first
//
// The sign-in routes are a person's own, in their own partition: elsewhere
// there is no person to sign in as (409). The device code goes only to
// that person — these routes answer only them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	routeTables = append(routeTables, scmRoutes)
}

func scmRoutes() []routeDef {
	return []routeDef{
		{"GET /projects/scm", needAny, handleSCMProviders},
		{"GET /projects/scm/repos", needAny, handleSCMRepos},
		{"GET /projects/scm/bot", needAny, handleSCMBotGet},
		{"PUT /projects/scm/bot", needAny, handleSCMBotPut},
		{"GET /projects/scm/signin", needUser, handleSCMSigninGet},
		{"POST /projects/scm/signin", needUser, handleSCMSigninPost},
		{"GET /projects/scm/signin/{pollId}", needUser, handleSCMSigninPoll},
		{"DELETE /projects/scm/signin", needUser, handleSCMSigninDelete},
	}
}

// writeSCMErr answers a provider's refusal as the agent's: its status, its
// words, and for programs the refusal with its payload (signin, install,
// identities, retryAfterMs) passed through.
func writeSCMErr(w http.ResponseWriter, err error) {
	var se *scmError
	if !errors.As(err, &se) {
		var sb *sbxError
		if errors.As(err, &sb) {
			writeSbxErr(w, err)
			return
		}
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	st := se.Status
	if st < 400 || st > 599 {
		st = http.StatusBadGateway
	}
	xbin.WriteJSON(w, st, se)
}

// --- providers ------------------------------------------------------------------------

// scmProviderView is one entry of GET /projects/scm.
type scmProviderView struct {
	SCM        string           `json:"scm"`
	Title      string           `json:"title"`
	Kind       string           `json:"kind,omitempty"`
	Hosts      []string         `json:"hosts"`
	Caps       []string         `json:"caps"`
	Identities []string         `json:"identities"`
	You        *scmYou          `json:"you,omitempty"`
	App        *scmApp          `json:"app,omitempty"`
	Events     *scmEventsHealth `json:"events,omitempty"`
	Notes      []string         `json:"notes"`
	Error      string           `json:"error,omitempty"`
	Refusal    string           `json:"refusal,omitempty"`
}

// handleSCMProviders: every bound provider's hello (cached 60 s, 10 s after
// an error), asked in parallel; one that fails is listed with why.
func handleSCMProviders(w http.ResponseWriter, r *http.Request) {
	names := scmBound()
	out := make([]scmProviderView, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := scmProviderView{SCM: name, Title: name, Hosts: []string{}, Caps: []string{}, Identities: []string{}, Notes: []string{}}
			defer func() { out[i] = v }()
			api, err := scmFor(name)
			if err == nil {
				ctx, cancel := context.WithTimeout(r.Context(), scmHelloTimeout+time.Second)
				defer cancel()
				var h *scmHello
				if h, err = api.Hello(ctx); err == nil {
					v.Title, v.Kind = orStr(h.SCM.Title, name), h.SCM.Kind
					v.Hosts, v.Caps, v.Identities = scmNonNil(h.Hosts), scmNonNil(h.Caps), scmNonNil(h.Identities)
					v.You, v.App, v.Events, v.Notes = &h.You, &h.App, &h.Events, scmNonNil(h.Notes)
					return
				}
			}
			v.Error = err.Error()
			var se *scmError
			if errors.As(err, &se) {
				v.Refusal = se.Refusal
			}
		}()
	}
	wg.Wait()
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func scmNonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// handleSCMRepos: the repos the provider shows this home — at a bot home
// only those the caller may name for the bot (scmBotAllowed).
func handleSCMRepos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	api, err := scmOnly(q.Get("scm"))
	if err != nil {
		writeSCMErr(w, err)
		return
	}
	page, err := api.Repos(r.Context(), scmQuery{Q: q.Get("q"), Cursor: q.Get("cursor"), Limit: 100})
	if err != nil {
		writeSCMErr(w, err)
		return
	}
	items := []scmRepo{}
	c := callerOf(r)
	for _, it := range page.Items {
		if userMode() || scmBotAllowed(c, it.Owner+"/"+it.Name) {
			items = append(items, it)
		}
	}
	page.Items = items
	xbin.WriteJSON(w, http.StatusOK, page)
}

// --- the bot rule ---------------------------------------------------------------------

func handleSCMBotGet(w http.ResponseWriter, r *http.Request) {
	if !callerOf(r).manager() {
		xbin.WriteError(w, http.StatusForbidden, "the scm bot rule is the agent's managers' to see")
		return
	}
	xbin.WriteJSON(w, http.StatusOK, scmLoadBotRule(agent.db))
}

func handleSCMBotPut(w http.ResponseWriter, r *http.Request) {
	if !callerOf(r).manager() {
		xbin.WriteError(w, http.StatusForbidden, "only the agent's managers can change who may name repos for the scm bot")
		return
	}
	if !scmBotHome() {
		xbin.WriteError(w, http.StatusConflict, "your own space uses your own sign-in, never the bot: the scm bot rule is set at the agent's shared instance")
		return
	}
	var in scmBotRule
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		xbin.WriteError(w, http.StatusBadRequest, "body: {users: [person ids], repos: [owner/name globs]}")
		return
	}
	rule, err := scmCleanBotRule(in)
	if err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := agent.db.putSetting(settingSCMBotRule, mustJSON(rule)); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, rule)
}

// --- sign-in --------------------------------------------------------------------------

// signinAPI is the provider a person's sign-in route names, or why not (the
// error is written): only the partition's own person, never view-as, and
// only in a person's partition.
func scmSigninAPI(w http.ResponseWriter, r *http.Request, name string) (scmAPI, bool) {
	c := callerOf(r)
	if c.kind != whoUser || c.viewedBy != "" || (userMode() && c.user != runUser) {
		xbin.WriteError(w, http.StatusForbidden, "only the person signing in can do that")
		return nil, false
	}
	api, err := scmOnly(name)
	if err != nil {
		writeSCMErr(w, err)
		return nil, false
	}
	if !userMode() {
		title := scmTitle(r.Context(), api)
		xbin.WriteError(w, http.StatusConflict, "sign in to "+title+" from your own space: a person's sign-in is kept in their own partition, never here")
		return nil, false
	}
	return api, true
}

// scmKeepSignin keeps (or clears) what a sign-in state says for this person.
func scmKeepSignin(provider string, st *scmSigninState) {
	switch {
	case st == nil:
	case st.State == "pending" && st.Signin != nil:
		scmNoteSignin(runUser, provider, st.Signin)
	case st.State == "done", st.State == "none", st.State == "denied", st.State == "expired":
		scmClearSignin(runUser, provider)
	}
}

func handleSCMSigninGet(w http.ResponseWriter, r *http.Request) {
	api, ok := scmSigninAPI(w, r, r.URL.Query().Get("scm"))
	if !ok {
		return
	}
	st, err := api.SigninState(r.Context())
	if err != nil {
		writeSCMErr(w, err)
		return
	}
	scmKeepSignin(api.Provider(), st)
	xbin.WriteJSON(w, http.StatusOK, st)
}

func handleSCMSigninPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SCM string `json:"scm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	api, ok := scmSigninAPI(w, r, orStr(in.SCM, r.URL.Query().Get("scm")))
	if !ok {
		return
	}
	st, err := api.Signin(r.Context())
	if err != nil {
		writeSCMErr(w, err)
		return
	}
	scmKeepSignin(api.Provider(), st)
	xbin.WriteJSON(w, http.StatusOK, st)
}

func handleSCMSigninPoll(w http.ResponseWriter, r *http.Request) {
	api, ok := scmSigninAPI(w, r, r.URL.Query().Get("scm"))
	if !ok {
		return
	}
	poll := r.PathValue("pollId")
	if poll == "" || len(poll) > 200 || strings.ContainsAny(poll, "/?#\\ ") {
		xbin.WriteError(w, http.StatusBadRequest, "no such sign-in")
		return
	}
	st, err := api.SigninPoll(r.Context(), poll)
	if err != nil {
		writeSCMErr(w, err)
		return
	}
	scmKeepSignin(api.Provider(), st)
	xbin.WriteJSON(w, http.StatusOK, st)
}

// handleSCMSigninDelete is Forget: every credential of this person's
// projects from that provider is scrubbed first (a credential that can't be
// emptied keeps the sign-in: try again), then the provider revokes the
// grant and forgets the sign-in.
func handleSCMSigninDelete(w http.ResponseWriter, r *http.Request) {
	api, ok := scmSigninAPI(w, r, r.URL.Query().Get("scm"))
	if !ok {
		return
	}
	if err := scmForgetScrub(r.Context(), api.Provider()); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "the sign-in isn't forgotten: "+err.Error()+" — try again")
		return
	}
	if err := api.Forget(r.Context()); err != nil {
		writeSCMErr(w, err)
		return
	}
	scmClearSignin(runUser, api.Provider())
	w.WriteHeader(http.StatusNoContent)
}
