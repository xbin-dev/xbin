package main

// scm_fake_test.go — an scm provider for the agent's tests: the scm
// contract, protocol 1 (docs/scm.md), served from memory by an
// httptest.Server. Every agent work package's tests use it; E and V extend
// it through the helpers below from their own _test.go files (never by
// editing this one).
//
//	f := newFakeSCM(t, "apps/scm-github")   // hello, tokens, sign-in, repos, pulls, checks…
//	bindSCM(t, f)                           // XBIN_IFACE_SCM as the runner injects it
//	f.SetSignedIn("octocat", 583231)        // a person's sign-in (else as:person → 409 signin)
//	f.SetChecks("acme/web", "main", &scmChecks{…})
//	f.Deliver(mux, ev)                      // POST /adapter/scm/event as the provider's tile
//
// Tokens are "ghs_" and 40 random characters (LongTokens: a 520-character
// stateless one), cached per (purpose, repos, permissions) while they have
// 15 minutes to run, as the contract says; every request is recorded.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSCMReq is one request the fake saw.
type fakeSCMReq struct {
	Method, Path, Query, Body string
}

// fakeToken is one token the fake handed out.
type fakeToken struct {
	Value, Purpose, As string
	Repos              []string
	Permissions        map[string]string
	Expires            int64
	Revoked            bool
}

type fakeLog struct {
	text    string
	running bool
}

type fakeSCM struct {
	t        *testing.T
	Provider string
	srv      *httptest.Server

	mu         sync.Mutex
	Caps       []string // hello's caps (default: every one)
	Identities []string // you.identities (default person, bot)
	Hosts      []string
	Title      string
	Speaks     int         // the protocol its hello says it speaks (0: 1, the agent's)
	Person     *scmAccount // signed in (nil: not)
	LongTokens bool
	TokenTTL   time.Duration // default 1 h
	NoCache    bool          // every token request mints a new one
	TokenHold  func()        // called before each POST /token is answered (outside the fake's lock)
	signin     *scmSignin    // under way
	signinOut  string        // what the next poll says: pending | done | denied | expired
	tokens     []*fakeToken
	reqs       []fakeSCMReq
	revokes    []string // purposes (or "token") revoked
	repos      []scmRepo
	pulls      map[string][]*scmPull
	comments   map[string][]scmComment
	checks     map[string]*scmChecks
	logs       map[string]*fakeLog
	notes      map[string][]scmAnnotation
	issues     map[string][]*scmIssue
	subs       map[string]*scmSubscription
	fails      map[string]*scmError // route ("POST /token") → its next answer
	nextSub    int
}

// newFakeSCM starts a fake provider named provider (its tile path).
func newFakeSCM(t *testing.T, provider string) *fakeSCM {
	t.Helper()
	f := &fakeSCM{t: t, Provider: provider, Hosts: []string{"github.com"}, Title: "GitHub",
		Caps:       []string{scmCapCredentials, scmCapRepos, scmCapPulls, scmCapIssues, scmCapChecks, scmCapRerun, scmCapEvents, scmCapPoll, scmCapPartitions},
		Identities: []string{scmAsPerson, scmAsBot}, TokenTTL: time.Hour,
		pulls: map[string][]*scmPull{}, comments: map[string][]scmComment{}, checks: map[string]*scmChecks{},
		logs: map[string]*fakeLog{}, notes: map[string][]scmAnnotation{}, issues: map[string][]*scmIssue{},
		subs: map[string]*scmSubscription{}, fails: map[string]*scmError{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// bindSCM injects the scm slot's bindings as the runner would, points the
// client at plain HTTP and forgets every cached hello (restored after).
func bindSCM(t *testing.T, fakes ...*fakeSCM) {
	t.Helper()
	var eps []map[string]string
	for _, f := range fakes {
		prov, inst, _ := strings.Cut(f.Provider, "#")
		ep := map[string]string{"provider": prov, "url": f.srv.URL + "/", "service": "scm"}
		if inst != "" {
			ep["instance"] = inst
		}
		eps = append(eps, ep)
	}
	raw, _ := json.Marshal(eps)
	t.Setenv("XBIN_IFACE_SCM", string(raw))
	old := setSCMClient(func() *http.Client { return http.DefaultClient })
	forgetSCMHellos()
	t.Cleanup(func() {
		setSCMClient(old)
		forgetSCMHellos()
	})
}

// --- the test's levers ----------------------------------------------------------------

func (f *fakeSCM) SetSignedIn(login string, id int64) {
	f.mu.Lock()
	f.Person, f.signin = &scmAccount{Login: login, ID: id}, nil
	f.mu.Unlock()
}

// CompleteSignin makes the sign-in under way finish (done) at its next poll
// — or, with out, end as denied or expired.
func (f *fakeSCM) CompleteSignin(out ...string) {
	f.mu.Lock()
	f.signinOut = "done"
	if len(out) > 0 {
		f.signinOut = out[0]
	}
	f.mu.Unlock()
}

func (f *fakeSCM) AddRepo(full string, private bool) {
	owner, name, _ := strings.Cut(full, "/")
	f.mu.Lock()
	f.repos = append(f.repos, scmRepo{Host: "github.com", Owner: owner, Name: name, CloneURL: "https://github.com/" + full + ".git",
		DefaultBranch: "main", Private: private, Permission: "write", URL: "https://github.com/" + full})
	f.mu.Unlock()
}

func (f *fakeSCM) AddPull(repo string, p *scmPull) {
	f.mu.Lock()
	f.pulls[repo] = append(f.pulls[repo], p)
	f.mu.Unlock()
}

func (f *fakeSCM) AddComment(repo string, n int, c scmComment) {
	f.mu.Lock()
	k := repo + "#" + strconv.Itoa(n)
	f.comments[k] = append(f.comments[k], c)
	f.mu.Unlock()
}

func (f *fakeSCM) SetChecks(repo, ref string, c *scmChecks) {
	f.mu.Lock()
	f.checks[repo+"@"+ref] = c
	f.mu.Unlock()
}

// SetJobLog sets job id's log; running: the job still runs (409 in-progress).
func (f *fakeSCM) SetJobLog(id, text string, running bool) {
	f.mu.Lock()
	f.logs[id] = &fakeLog{text: text, running: running}
	f.mu.Unlock()
}

func (f *fakeSCM) SetAnnotations(check string, items []scmAnnotation) {
	f.mu.Lock()
	f.notes[check] = items
	f.mu.Unlock()
}

func (f *fakeSCM) AddIssue(repo string, i *scmIssue) {
	f.mu.Lock()
	f.issues[repo] = append(f.issues[repo], i)
	f.mu.Unlock()
}

// FailNext makes the next request to route ("POST /token") answer e.
func (f *fakeSCM) FailNext(route string, e *scmError) {
	f.mu.Lock()
	f.fails[route] = e
	f.mu.Unlock()
}

// Tokens is every token handed out, oldest first.
func (f *fakeSCM) Tokens() []fakeToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeToken, len(f.tokens))
	for i, t := range f.tokens {
		out[i] = *t
	}
	return out
}

// Requests is every request to route ("" = all), as "METHOD /path".
func (f *fakeSCM) Requests(route string) []fakeSCMReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeSCMReq
	for _, r := range f.reqs {
		if route == "" || r.Method+" "+r.Path == route {
			out = append(out, r)
		}
	}
	return out
}

// Revoked is each revoke's purpose ("token" for one by value).
func (f *fakeSCM) Revoked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revokes...)
}

func (f *fakeSCM) Subscriptions() []scmSubscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []scmSubscription
	for _, s := range f.subs {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Deliver POSTs an event v1 to the agent's /adapter/scm/event as this
// provider's tile (the channel role, as a bound provider holds it).
func (f *fakeSCM) Deliver(h http.Handler, ev scmEvent) *httptest.ResponseRecorder {
	if ev.Protocol == 0 {
		ev.Protocol = scmProtocol
	}
	if ev.SCM.Provider == "" {
		ev.SCM = scmEventSource{Provider: f.Provider, Host: "github.com"}
	}
	b, _ := json.Marshal(ev)
	r := httptest.NewRequest("POST", "/adapter/scm/event", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-XBin-From", strings.SplitN(f.Provider, "#", 2)[0])
	r.Header.Set("X-XBin-Role", "channel")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// --- serving --------------------------------------------------------------------------

func fakeTokenValue(long bool) string {
	n := 40
	if long {
		n = 516
	}
	const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = abc[int(b[i])%len(abc)]
	}
	return "ghs_" + string(b)
}

func fakeETag(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return `W/"` + hex.EncodeToString(h[:8]) + `"`
}

func (f *fakeSCM) refuse(w http.ResponseWriter, e *scmError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e)
}

func fakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// conditional answers v with its ETag, or 304 when the request has it.
func (f *fakeSCM) conditional(w http.ResponseWriter, r *http.Request, v any) {
	et := fakeETag(v)
	w.Header().Set("ETag", et)
	if m := orStr(r.URL.Query().Get("ifNoneMatch"), r.Header.Get("If-None-Match")); m != "" && m == et {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	fakeJSON(w, 200, v)
}

func (f *fakeSCM) has(c string) bool { return slices.Contains(f.Caps, c) }

// needCap: the route's capability, else 501 unsupported.
func (f *fakeSCM) needCap(w http.ResponseWriter, c string) bool {
	if f.has(c) {
		return true
	}
	f.refuse(w, &scmError{Status: 501, Refusal: scmRefUnsupported, Message: "this provider doesn't offer " + c})
	return false
}

// who is the identity a call is made as: as (or the default, person when
// the fake offers it), checked — 409 signin (starting one) for a person not
// signed in, 403 identity for one it doesn't offer.
func (f *fakeSCM) who(w http.ResponseWriter, as string) (string, bool) {
	if as == "" {
		as = f.Identities[0]
	}
	if !slices.Contains(f.Identities, as) {
		f.refuse(w, &scmError{Status: 403, Refusal: scmRefIdentity, Message: "not as " + as + " here", Identities: f.Identities})
		return "", false
	}
	if as == scmAsPerson && f.Person == nil {
		if f.signin == nil {
			f.signin = &scmSignin{URL: "https://github.com/login/device", UserCode: "ABCD-1234",
				ExpiresAt: nowMs() + 15*60*1000, PollID: "p_" + fakeTokenValue(false)[4:14], IntervalMs: 5000}
			f.signinOut = "pending"
		}
		f.refuse(w, &scmError{Status: 409, Refusal: scmRefSignin, Message: "sign in to GitHub first", Signin: f.signin})
		return "", false
	}
	return as, true
}

func (f *fakeSCM) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	route := strings.TrimPrefix(r.URL.Path, "/scm")
	f.mu.Lock()
	hold := f.TokenHold
	f.mu.Unlock()
	if hold != nil && r.Method == "POST" && route == "/token" {
		hold()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, fakeSCMReq{Method: r.Method, Path: route, Query: r.URL.RawQuery, Body: string(body)})
	if e := f.fails[r.Method+" "+route]; e != nil {
		delete(f.fails, r.Method+" "+route)
		if e.Status == 0 {
			w.WriteHeader(503) // a 5xx with no body
			return
		}
		f.refuse(w, e)
		return
	}
	q := r.URL.Query()
	var in map[string]json.RawMessage
	_ = json.Unmarshal(body, &in)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(in[k], &s)
		return s
	}
	parts := strings.Split(strings.Trim(route, "/"), "/")
	switch {
	case r.Method == "GET" && route == "/hello":
		f.hello(w, q.Get("protocol"))
	case r.Method == "POST" && route == "/token":
		f.token(w, body)
	case r.Method == "POST" && route == "/token/revoke":
		f.revoke(w, str("token"), str("purpose"))
	case route == "/signin" || (len(parts) == 2 && parts[0] == "signin"):
		f.signinRoute(w, r.Method, parts)
	case r.Method == "GET" && route == "/repos":
		if f.needCap(w, scmCapRepos) {
			if _, ok := f.who(w, q.Get("as")); ok {
				fakeJSON(w, 200, f.repoPage(q.Get("q")))
			}
		}
	case r.Method == "GET" && route == "/repo":
		f.repo(w, r, q.Get("repo"))
	case len(parts) >= 1 && parts[0] == "pulls":
		if f.needCap(w, scmCapPulls) {
			f.pullRoute(w, r, parts, q, in)
		}
	case len(parts) >= 1 && parts[0] == "checks":
		if f.needCap(w, scmCapChecks) {
			f.checkRoute(w, r, parts, q, in)
		}
	case len(parts) >= 1 && parts[0] == "issues":
		if f.needCap(w, scmCapIssues) {
			f.issueRoute(w, r, parts, q)
		}
	case r.Method == "POST" && route == "/poll":
		if f.needCap(w, scmCapPoll) {
			f.poll(w, body)
		}
	case len(parts) >= 1 && parts[0] == "subscriptions":
		if f.needCap(w, scmCapEvents) {
			f.subRoute(w, r.Method, parts, body)
		}
	default:
		f.refuse(w, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no route " + r.Method + " " + route})
	}
}

func (f *fakeSCM) hello(w http.ResponseWriter, protocol string) {
	if protocol != "1" {
		f.refuse(w, &scmError{Status: 400, Refusal: scmRefProtocol, Message: "protocol 1 only", Protocols: []int{1}})
		return
	}
	you := scmYou{Identities: f.Identities, Default: f.Identities[0], Person: f.Person}
	speaks := max(f.Speaks, 1)
	fakeJSON(w, 200, scmHello{Protocol: speaks, Protocols: []int{speaks}, SCM: scmProvider{Name: "scm-github", Title: f.Title, Version: "1.0.0", Kind: "github"},
		Hosts: f.Hosts, Caps: f.Caps, Identities: []string{scmAsPerson, scmAsBot}, You: you,
		App:    scmApp{Slug: "acme-xbin", InstallURL: "https://github.com/apps/acme-xbin/installations/new", Configured: true},
		Events: scmEventsHealth{Webhooks: "active", Healthy: true, PollMinMs: 120000},
		Limits: scmLimits{ReposPerToken: 100, MinTTLSec: 900, PageMax: 100, PollItems: 50}})
}

func (f *fakeSCM) token(w http.ResponseWriter, body []byte) {
	var req scmTokenReq
	_ = json.Unmarshal(body, &req)
	as, ok := f.who(w, req.As)
	if !ok {
		return
	}
	repos := req.Repos
	if req.Repo != "" {
		repos = append(repos, req.Repo)
	}
	owner := ""
	for _, rp := range repos {
		o, _, _ := strings.Cut(rp, "/")
		if owner != "" && o != owner {
			f.refuse(w, &scmError{Status: 400, Refusal: scmRefInvalid, Message: "repos span owners"})
			return
		}
		owner = o
	}
	now := time.Now()
	var tok *fakeToken
	for _, t := range f.tokens {
		if !f.NoCache && !t.Revoked && t.Purpose == req.Purpose && slices.Equal(t.Repos, repos) && t.As == as &&
			time.UnixMilli(t.Expires).Sub(now) >= 15*time.Minute && fmt.Sprint(t.Permissions) == fmt.Sprint(req.Permissions) {
			tok = t
		}
	}
	if tok == nil {
		tok = &fakeToken{Value: fakeTokenValue(f.LongTokens), Purpose: req.Purpose, As: as, Repos: repos,
			Permissions: req.Permissions, Expires: now.Add(f.TokenTTL).UnixMilli()}
		f.tokens = append(f.tokens, tok)
	}
	login, id := "acme-xbin[bot]", int64(1)
	if as == scmAsPerson {
		login, id = f.Person.Login, f.Person.ID
	}
	fakeJSON(w, 200, map[string]any{"host": "github.com", "username": "x-access-token", "token": tok.Value,
		"expiresAt": tok.Expires, "refreshAfter": tok.Expires - 10*60*1000,
		"identity": scmIdentity{Kind: as, Login: login, ID: id},
		"author":   scmAuthor{Name: login, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", id, login)},
		"repos":    repos, "permissions": map[string]string{"contents": "write", "metadata": "read"}})
}

func (f *fakeSCM) revoke(w http.ResponseWriter, token, purpose string) {
	n := 0
	for _, t := range f.tokens {
		if !t.Revoked && ((token != "" && t.Value == token) || (purpose != "" && t.Purpose == purpose)) {
			t.Revoked = true
			n++
		}
	}
	f.revokes = append(f.revokes, orStr(purpose, "token"))
	if n == 0 {
		f.refuse(w, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such token"})
		return
	}
	w.WriteHeader(204)
}

func (f *fakeSCM) signinRoute(w http.ResponseWriter, method string, parts []string) {
	switch {
	case method == "DELETE" && len(parts) == 1:
		f.Person, f.signin = nil, nil
		for _, t := range f.tokens {
			if t.As == scmAsPerson {
				t.Revoked = true
			}
		}
		w.WriteHeader(204)
	case method == "GET" && len(parts) == 1:
		fakeJSON(w, 200, f.signinState(false))
	case method == "POST" && len(parts) == 1:
		if f.Person == nil && f.signin == nil {
			f.who(httptest.NewRecorder(), scmAsPerson) // starts one
		}
		fakeJSON(w, 200, f.signinState(false))
	case method == "GET" && len(parts) == 2:
		if f.signin == nil || parts[1] != f.signin.PollID {
			if f.Person != nil {
				fakeJSON(w, 200, scmSigninState{State: "done", Identity: &scmIdentity{Kind: scmAsPerson, Login: f.Person.Login, ID: f.Person.ID}})
				return
			}
			f.refuse(w, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such sign-in"})
			return
		}
		switch f.signinOut {
		case "done":
			f.Person, f.signin = &scmAccount{Login: "octocat", ID: 583231}, nil
		case "denied", "expired":
			out := f.signinOut
			f.signin = nil
			fakeJSON(w, 200, scmSigninState{State: out})
			return
		}
		fakeJSON(w, 200, f.signinState(true))
	default:
		f.refuse(w, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such route"})
	}
}

func (f *fakeSCM) signinState(poll bool) scmSigninState {
	switch {
	case f.Person != nil:
		return scmSigninState{State: "done", Identity: &scmIdentity{Kind: scmAsPerson, Login: f.Person.Login, ID: f.Person.ID}}
	case f.signin != nil:
		st := scmSigninState{State: "pending", Signin: f.signin}
		if poll {
			st.Signin, st.RetryAfterMs = nil, f.signin.IntervalMs
		}
		return st
	}
	return scmSigninState{State: "none"}
}

func (f *fakeSCM) repoPage(q string) scmPage[scmRepo] {
	out := scmPage[scmRepo]{Items: []scmRepo{}}
	for _, r := range f.repos {
		if q == "" || strings.Contains(r.Owner+"/"+r.Name, q) {
			out.Items = append(out.Items, r)
		}
	}
	return out
}

func (f *fakeSCM) repo(w http.ResponseWriter, r *http.Request, full string) {
	if _, ok := f.who(w, r.URL.Query().Get("as")); !ok {
		return
	}
	for _, x := range f.repos {
		if x.Owner+"/"+x.Name == full {
			p := false
			x.Protected = &p
			f.conditional(w, r, x)
			return
		}
	}
	f.refuse(w, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such repo"})
}
