// fakegh_test.go — the fake GitHub: an httptest server for the GitHub
// surface this tile uses (the App and its installations, installation
// tokens, the OAuth applications API, the device flow and refresh, users,
// repos, pulls, GraphQL, issues, comments, reviews, CI, logs, annotations,
// reruns, collaborators, the manifest conversion and the hook config), with
// ETags and 304s on every GET, request counters, and injected rate limits,
// SSO blocks and 5xx. CI and content routes are in fakegh_ci_test.go.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is the tests' time: the tile and the fake share it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fToken struct {
	inst    int64
	repos   []string // names; nil: every repo of the installation
	perms   map[string]string
	exp     time.Time
	revoked bool
}

type fUserTok struct {
	login   string
	exp     time.Time
	scoped  bool
	parent  string
	repos   []string
	perms   map[string]string
	revoked bool
}

type fDevice struct {
	state    string // pending | approved | denied | expired
	login    string
	slowDown int
	polls    int
}

// fakeGH is the fake. Tests set its maps directly under mu (or before use).
type fakeGH struct {
	t   *testing.T
	srv *httptest.Server
	now func() time.Time

	mu           sync.Mutex
	key          *rsa.PrivateKey
	keyPEM       string
	appID        int64
	clientID     string
	clientSecret string
	slug, owner  string
	appPerms     map[string]string
	hookURL      string
	hookSecret   string

	installs   map[string]int64   // account (lower) → installation
	instRepos  map[int64][]string // installation → repo names
	users      map[string]int64   // login → id
	collab     map[string]string  // "owner/name|login" → admin | write | read | none
	instTokens map[string]*fToken // token → …
	userTokens map[string]*fUserTok
	refresh    map[string]string // refresh token → login
	deadGrant  map[string]bool   // refresh tokens of a revoked grant
	devices    map[string]*fDevice
	deviceOff  bool
	longTokens bool
	manifests  map[string]bool // conversion codes

	mintReqs []map[string]any // POST access_tokens bodies
	scopeReq []map[string]any // POST token/scoped bodies
	hits     map[string]int   // "METHOD /path" → count (304s too)
	notMod   int              // 304s answered

	inject []*injection

	ci *fakeCI // fakegh_ci_test.go
}

type injection struct {
	match  string // a substring of "METHOD /path"
	status int
	header map[string]string
	body   string
	times  int
}

func newFakeGH(t *testing.T, now func() time.Time) *fakeGH {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGH{t: t, now: now, key: k, appID: 4242, clientID: "Iv23liFAKECLIENT", clientSecret: "fake-client-secret-0123456789",
		slug: "acme-xbin", owner: "acme",
		appPerms: map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "statuses": "read", "actions": "read", "metadata": "read", "members": "read"},
		installs: map[string]int64{"acme": 100}, instRepos: map[int64][]string{100: {"web", "api"}},
		users:      map[string]int64{"octocat": 583231, "hubot": 999, "acme-xbin[bot]": 777},
		collab:     map[string]string{"acme/web|octocat": "write", "acme/api|octocat": "read"},
		instTokens: map[string]*fToken{}, userTokens: map[string]*fUserTok{}, refresh: map[string]string{}, deadGrant: map[string]bool{},
		devices: map[string]*fDevice{}, manifests: map[string]bool{}, hits: map[string]int{},
	}
	f.keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	f.ci = newFakeCI()
	mux := http.NewServeMux()
	f.routes(mux)
	f.ciRoutes(mux)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.Method+" "+r.URL.Path]++
		for _, in := range f.inject {
			if in.times != 0 && strings.Contains(r.Method+" "+r.URL.Path, in.match) {
				in.times--
				for k, v := range in.header {
					w.Header().Set(k, v)
				}
				f.mu.Unlock()
				w.WriteHeader(in.status)
				io.WriteString(w, in.body)
				return
			}
		}
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fail makes the next n calls matching match answer status.
func (f *fakeGH) fail(match string, n, status int, header map[string]string, body string) {
	f.mu.Lock()
	f.inject = append(f.inject, &injection{match: match, status: status, header: header, body: body, times: n})
	f.mu.Unlock()
}

func (f *fakeGH) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

// reply answers JSON; a GET gets an ETag and a 304 to If-None-Match.
func (f *fakeGH) reply(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, _ := json.Marshal(v)
	if r.Method == http.MethodGet && status == http.StatusOK {
		sum := sha256.Sum256(b)
		tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:9]) + `"`
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			f.mu.Lock()
			f.notMod++
			f.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

func (f *fakeGH) msg(w http.ResponseWriter, r *http.Request, status int, m string) {
	f.reply(w, r, status, map[string]string{"message": m})
}

func randTok(prefix string, n int) string {
	const al = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = al[int(b[i])%len(al)]
	}
	return prefix + string(b)
}

// appJWT verifies the App's JWT: RS256 by the App's key, iss the client id
// (or the app id), live, at most ten minutes long.
func (f *fakeGH) appJWT(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return false
	}
	now := f.now().Unix()
	return (c.Iss == f.clientID || c.Iss == strconv.FormatInt(f.appID, 10)) && c.Iat <= now && now < c.Exp && c.Exp-c.Iat <= 600
}

func bearer(r *http.Request) string {
	t, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return t
}

// instTok is the request's live installation token.
func (f *fakeGH) instTok(r *http.Request) *fToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.instTokens[bearer(r)]
	if t == nil || t.revoked || !f.now().Before(t.exp) {
		return nil
	}
	return t
}

// userTok is the request's live user token (parent or scoped).
func (f *fakeGH) userTok(r *http.Request) *fUserTok {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.userTokens[bearer(r)]
	if t == nil || t.revoked || !f.now().Before(t.exp) {
		return nil
	}
	return t
}

func (f *fakeGH) basic(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	return ok && u == f.clientID && p == f.clientSecret
}

// access is what the request's token may do on owner/name: "" (nothing),
// read or write. contents decides for a token narrowed by permissions.
func (f *fakeGH) access(r *http.Request, repo, perm string) string {
	owner, name, _ := strings.Cut(repo, "/")
	if t := f.instTok(r); t != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.installs[strings.ToLower(owner)] != t.inst || !contains(f.instRepos[t.inst], name) {
			return ""
		}
		if t.repos != nil && !contains(t.repos, name) {
			return ""
		}
		return t.perms[perm]
	}
	if u := f.userTok(r); u != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.installs[strings.ToLower(owner)]; !ok {
			return ""
		}
		c := f.collab[repo+"|"+u.login]
		lvl := map[string]string{"admin": "write", "write": "write", "read": "read"}[c]
		if lvl == "" {
			return ""
		}
		if u.scoped {
			if !contains(u.repos, name) {
				return ""
			}
			if u.perms[perm] == "" || (u.perms[perm] == "read" && lvl == "write") {
				lvl = u.perms[perm]
			}
		}
		if f.appPerms[perm] == "read" && lvl == "write" {
			lvl = "read"
		}
		return lvl
	}
	return ""
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func (f *fakeGH) newInstToken(inst int64, repos []string, perms map[string]string) string {
	tok := randTok("ghs_", 40)
	if f.longTokens {
		tok = randTok("ghs_", 516)
	}
	f.instTokens[tok] = &fToken{inst: inst, repos: repos, perms: perms, exp: f.now().Add(time.Hour)}
	return tok
}

// newUser signs login in: a parent pair, as the device flow or a refresh
// answers it.
func (f *fakeGH) newUser(login string) map[string]any {
	acc, ref := randTok("ghu_", 36), randTok("ghr_", 76)
	f.userTokens[acc] = &fUserTok{login: login, exp: f.now().Add(8 * time.Hour)}
	f.refresh[ref] = login
	return map[string]any{"access_token": acc, "expires_in": 28800, "refresh_token": ref, "refresh_token_expires_in": 15897600, "token_type": "bearer", "scope": ""}
}

func (f *fakeGH) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		if !f.appJWT(r) {
			f.msg(w, r, 401, "A JSON web token could not be decoded")
			return
		}
		f.reply(w, r, 200, map[string]any{"id": f.appID, "slug": f.slug, "client_id": f.clientID, "html_url": f.srv.URL + "/apps/" + f.slug,
			"owner": map[string]any{"login": f.owner}, "permissions": f.appPerms, "events": []string{"push", "pull_request"}})
	})
	mux.HandleFunc("GET /app/hook/config", func(w http.ResponseWriter, r *http.Request) {
		if !f.appJWT(r) {
			f.msg(w, r, 401, "bad JWT")
			return
		}
		f.mu.Lock()
		u := f.hookURL
		f.mu.Unlock()
		if u == "" { // GitHub (live): an App whose webhook is off has no config at all
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, map[string]any{"url": u, "content_type": "json", "secret": "********", "insecure_ssl": "0"})
	})
	mux.HandleFunc("PATCH /app/hook/config", func(w http.ResponseWriter, r *http.Request) {
		if !f.appJWT(r) {
			f.msg(w, r, 401, "bad JWT")
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		if b["url"] != "" {
			f.hookURL = b["url"]
		}
		if b["secret"] != "" {
			f.hookSecret = b["secret"]
		}
		f.mu.Unlock()
		f.reply(w, r, 200, map[string]any{"url": f.hookURL, "content_type": "json"})
	})
	mux.HandleFunc("GET /app/hook/deliveries", func(w http.ResponseWriter, r *http.Request) { f.reply(w, r, 200, []any{}) })
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, r *http.Request) {
		if !f.appJWT(r) {
			f.msg(w, r, 401, "bad JWT")
			return
		}
		f.mu.Lock()
		var out []map[string]any
		for acct, id := range f.installs {
			out = append(out, map[string]any{"id": id, "html_url": f.srv.URL + "/settings/installations/" + strconv.FormatInt(id, 10),
				"account": map[string]any{"login": acct, "type": "Organization"}, "repository_selection": "selected"})
		}
		f.mu.Unlock()
		f.reply(w, r, 200, out)
	})
	mux.HandleFunc("GET /repos/{o}/{r}/installation", func(w http.ResponseWriter, r *http.Request) {
		if !f.appJWT(r) {
			f.msg(w, r, 401, "bad JWT")
			return
		}
		f.mu.Lock()
		id, ok := f.installs[strings.ToLower(r.PathValue("o"))]
		has := ok && contains(f.instRepos[id], r.PathValue("r"))
		f.mu.Unlock()
		if !has {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, map[string]any{"id": id, "account": map[string]any{"login": r.PathValue("o")}})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !f.appJWT(r) {
			f.msg(w, r, 401, "bad JWT")
			return
		}
		inst, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var b struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &b)
		var rec map[string]any
		json.Unmarshal(raw, &rec)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mintReqs = append(f.mintReqs, rec)
		if _, ok := f.instRepos[inst]; !ok {
			w.WriteHeader(404)
			return
		}
		for _, n := range b.Repositories {
			if !contains(f.instRepos[inst], n) {
				w.WriteHeader(422)
				io.WriteString(w, `{"message":"There is at least one repository that does not exist or is not accessible to the parent installation."}`)
				return
			}
		}
		for k, v := range b.Permissions {
			if have := f.appPerms[k]; have == "" || (v == "write" && have != "write") {
				w.WriteHeader(422)
				io.WriteString(w, `{"message":"The permissions requested are not granted to this installation."}`)
				return
			}
		}
		tok := f.newInstToken(inst, b.Repositories, b.Permissions)
		var owner string
		for a, id := range f.installs {
			if id == inst {
				owner = a
			}
		}
		names := b.Repositories
		if len(names) == 0 {
			names = f.instRepos[inst]
		}
		var repos []any
		for _, n := range names {
			repos = append(repos, f.repoJSON(owner+"/"+n, "none"))
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": f.instTokens[tok].exp.Format(time.RFC3339), "permissions": b.Permissions,
			"repository_selection": "selected", "repositories": repos})
	})
	mux.HandleFunc("DELETE /installation/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		t := f.instTokens[bearer(r)]
		if t == nil || t.revoked {
			w.WriteHeader(401)
			return
		}
		t.revoked = true
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		t := f.instTok(r)
		if t == nil {
			f.msg(w, r, 401, "Bad credentials")
			return
		}
		f.mu.Lock()
		var owner string
		for a, id := range f.installs {
			if id == t.inst {
				owner = a
			}
		}
		names := f.instRepos[t.inst]
		f.mu.Unlock()
		var repos []any
		for _, n := range names {
			repos = append(repos, f.repoJSON(owner+"/"+n, "none")) // GitHub (live): every flag false for an installation token
		}
		f.paged(w, r, "repositories", repos)
	})
	mux.HandleFunc("POST /applications/{cid}/token", func(w http.ResponseWriter, r *http.Request) {
		if !f.basic(r) || r.PathValue("cid") != f.clientID {
			f.msg(w, r, 404, "Not Found")
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		u := f.userTokens[b["access_token"]]
		f.mu.Unlock()
		if u == nil || u.revoked || !f.now().Before(u.exp) {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, map[string]any{"token": b["access_token"], "expires_at": u.exp.Format(time.RFC3339),
			"user": map[string]any{"login": u.login, "id": f.users[u.login]}, "app": map[string]any{"client_id": f.clientID}})
	})
	mux.HandleFunc("POST /applications/{cid}/token/scoped", func(w http.ResponseWriter, r *http.Request) {
		if !f.basic(r) || r.PathValue("cid") != f.clientID {
			f.msg(w, r, 404, "Not Found")
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var b struct {
			AccessToken  string            `json:"access_token"`
			Target       string            `json:"target"`
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		json.Unmarshal(raw, &b)
		var rec map[string]any
		json.Unmarshal(raw, &rec)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.scopeReq = append(f.scopeReq, rec)
		u := f.userTokens[b.AccessToken]
		if u == nil || u.revoked || u.scoped || !f.now().Before(u.exp) {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		tok := randTok("ghu_", 36)
		f.userTokens[tok] = &fUserTok{login: u.login, exp: u.exp, scoped: true, parent: b.AccessToken, repos: b.Repositories, perms: b.Permissions}
		json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": u.exp.Format(time.RFC3339), "permissions": b.Permissions,
			"user": map[string]any{"login": u.login, "id": f.users[u.login]}})
	})
	revoke := func(grant bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !f.basic(r) {
				f.msg(w, r, 404, "Not Found")
				return
			}
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			f.mu.Lock()
			defer f.mu.Unlock()
			u := f.userTokens[b["access_token"]]
			if u == nil { // GitHub (live): a token this App never issued (a PAT, a made-up one) is 404
				f.msg(w, r, 404, "Not Found")
				return
			}
			if u.revoked || !f.now().Before(u.exp) { // GitHub no longer knows a revoked or expired token
				w.WriteHeader(404)
				return
			}
			u.revoked = true
			if grant {
				for _, t := range f.userTokens {
					if t.login == u.login {
						t.revoked = true
					}
				}
				for rt, l := range f.refresh {
					if l == u.login {
						f.deadGrant[rt] = true
						delete(f.refresh, rt)
					}
				}
			}
			w.WriteHeader(204)
		}
	}
	mux.HandleFunc("DELETE /applications/{cid}/token", revoke(false))
	mux.HandleFunc("DELETE /applications/{cid}/grant", revoke(true))
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Form.Get("client_id") != f.clientID {
			json.NewEncoder(w).Encode(map[string]string{"error": "Not Found"})
			return
		}
		if f.deviceOff {
			json.NewEncoder(w).Encode(map[string]string{"error": "device_flow_disabled"})
			return
		}
		dc := randTok("dc_", 40)
		f.devices[dc] = &fDevice{state: "pending"}
		json.NewEncoder(w).Encode(map[string]any{"device_code": dc, "user_code": "WDJB-MJHT", "verification_uri": f.srv.URL + "/login/device", "expires_in": 900, "interval": 5})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		e := func(s string) { json.NewEncoder(w).Encode(map[string]string{"error": s}) }
		if r.Form.Get("client_id") != f.clientID || r.Form.Get("client_secret") != "" {
			e("incorrect_client_credentials")
			return
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			d := f.devices[r.Form.Get("device_code")]
			if d == nil {
				e("incorrect_device_code")
				return
			}
			d.polls++
			if d.slowDown > 0 {
				d.slowDown--
				json.NewEncoder(w).Encode(map[string]any{"error": "slow_down", "interval": 10})
				return
			}
			switch d.state {
			case "pending":
				e("authorization_pending")
			case "denied":
				e("access_denied")
			case "expired":
				e("expired_token")
			case "approved":
				d.state = "used"
				json.NewEncoder(w).Encode(f.newUser(d.login))
			default:
				e("incorrect_device_code")
			}
		case "refresh_token":
			login, ok := f.refresh[r.Form.Get("refresh_token")]
			if f.deadGrant[r.Form.Get("refresh_token")] {
				e("incorrect_client_credentials") // GitHub (live), for a revoked grant's
				return
			}
			if !ok {
				e("bad_refresh_token")
				return
			}
			delete(f.refresh, r.Form.Get("refresh_token"))
			for _, t := range f.userTokens { // the old access token (not its scoped children) dies
				if t.login == login && !t.scoped {
					t.revoked = true
				}
			}
			json.NewEncoder(w).Encode(f.newUser(login))
		default:
			e("unsupported_grant_type")
		}
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		u := f.userTok(r)
		if u == nil {
			f.msg(w, r, 401, "Bad credentials")
			return
		}
		f.reply(w, r, 200, map[string]any{"login": u.login, "id": f.users[u.login]})
	})
	mux.HandleFunc("GET /users/{login}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		id, ok := f.users[r.PathValue("login")]
		f.mu.Unlock()
		if !ok {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 200, map[string]any{"login": r.PathValue("login"), "id": id})
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		u := f.userTok(r)
		if u == nil {
			f.msg(w, r, 401, "Bad credentials")
			return
		}
		f.mu.Lock()
		var out []any
		for acct, id := range f.installs {
			for k := range f.collab {
				if strings.HasPrefix(strings.ToLower(k), acct+"/") && strings.HasSuffix(k, "|"+u.login) {
					out = append(out, map[string]any{"id": id, "account": map[string]any{"login": acct}})
					break
				}
			}
		}
		f.mu.Unlock()
		f.reply(w, r, 200, map[string]any{"total_count": len(out), "installations": out})
	})
	mux.HandleFunc("GET /user/installations/{id}/repositories", func(w http.ResponseWriter, r *http.Request) {
		u := f.userTok(r)
		if u == nil {
			f.msg(w, r, 401, "Bad credentials")
			return
		}
		inst, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		f.mu.Lock()
		var owner string
		for a, id := range f.installs {
			if id == inst {
				owner = a
			}
		}
		var names []string
		for _, n := range f.instRepos[inst] {
			if c := f.collab[owner+"/"+n+"|"+u.login]; c != "" && c != "none" {
				names = append(names, n)
			}
		}
		f.mu.Unlock()
		var repos []any
		for _, n := range names {
			repos = append(repos, f.repoJSON(owner+"/"+n, f.collab[owner+"/"+n+"|"+u.login]))
		}
		f.paged(w, r, "repositories", repos)
	})
	mux.HandleFunc("GET /repos/{o}/{r}/collaborators/{login}/permission", func(w http.ResponseWriter, r *http.Request) {
		repo := r.PathValue("o") + "/" + r.PathValue("r")
		if f.access(r, repo, "metadata") == "" {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.mu.Lock()
		p := f.collab[repo+"|"+r.PathValue("login")]
		f.mu.Unlock()
		if p == "" {
			p = "none"
		}
		f.reply(w, r, 200, map[string]any{"permission": p, "user": map[string]any{"login": r.PathValue("login")}})
	})
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := f.manifests[r.PathValue("code")]
		delete(f.manifests, r.PathValue("code"))
		f.mu.Unlock()
		if !ok {
			f.msg(w, r, 404, "Not Found")
			return
		}
		f.reply(w, r, 201, map[string]any{"id": f.appID, "slug": f.slug, "client_id": f.clientID, "client_secret": f.clientSecret,
			"webhook_secret": "fake-manifest-hook-secret", "pem": f.keyPEM, "owner": map[string]any{"login": f.owner},
			"html_url": f.srv.URL + "/apps/" + f.slug, "permissions": f.appPerms, "events": []string{"push"}})
	})
}

// paged answers a list GitHub's way: per_page and page, a Link to the
// next; field wraps it ({total_count, field: [...]}) when set.
func (f *fakeGH) paged(w http.ResponseWriter, r *http.Request, field string, all []any) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 {
		per = 30
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pg <= 0 {
		pg = 1
	}
	from := min((pg-1)*per, len(all))
	to := min(from+per, len(all))
	if to < len(all) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(pg+1))
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?%s>; rel="next"`, f.srv.URL, r.URL.Path, q.Encode()))
	}
	items := all[from:to]
	if items == nil {
		items = []any{}
	}
	if field == "" {
		f.reply(w, r, 200, items)
		return
	}
	f.reply(w, r, 200, map[string]any{"total_count": len(all), field: items})
}

func (f *fakeGH) repoJSON(full, perm string) map[string]any {
	owner, name, _ := strings.Cut(full, "/")
	p := map[string]bool{"admin": perm == "admin", "push": perm == "admin" || perm == "write", "pull": perm != "" && perm != "none"}
	return map[string]any{"name": name, "full_name": full, "owner": map[string]any{"login": owner},
		"clone_url": f.srv.URL + "/" + full + ".git", "html_url": f.srv.URL + "/" + full, "default_branch": "main",
		"private": true, "archived": false, "permissions": p}
}

// approve is the person entering the code on GitHub.
func (f *fakeGH) approve(login string, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.state == "pending" {
			d.state, d.login = state, login
		}
	}
}

func (f *fakeGH) url(p string) string { u, _ := url.JoinPath(f.srv.URL, p); return u }
