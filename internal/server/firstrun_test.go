package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// frServer: a workspace with no accounts, the owner token's browser cookie,
// and a probe route that reports who a request is.
func frServer(t *testing.T) (http.Handler, *Server, *users.Store) {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	s := &Server{Auth: a}
	s.RegisterAPI("GET /probe", func(w http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		WriteJSON(w, http.StatusOK, map[string]any{"user": p.UserID, "owner": p.Owner, "admin": p.IsAdmin(), "via": p.Via})
	})
	return s.Handler(), s, st
}

// frReq: a request from this origin's own page (Fetch Metadata same-origin)
// with the session cookie sid ("" none).
func frReq(method, path, body, sid string) *http.Request {
	r := withCookie(method, path, body, sid)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Origin", "http://"+r.Host)
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func bootstrapOf(t *testing.T, w *httptest.ResponseRecorder) bool {
	t.Helper()
	var v struct{ Bootstrap bool }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("GET /first-run: %d %s", w.Code, w.Body.String())
	}
	return v.Bootstrap
}

const adaPass = "ada-first-password"

func makeAda(t *testing.T, st *users.Store, u users.User) {
	t.Helper()
	if u.ID == "" {
		u.ID = "ada"
	}
	if u.Role == "" {
		u.Role = users.RoleAdmin
	}
	if _, err := st.Upsert(u, adaPass); err != nil {
		t.Fatal(err)
	}
}

// The bootstrap state is the owner token's browser session in a workspace
// with no accounts — nobody else, and never once an account exists.
func TestFirstRunBootstrapState(t *testing.T) {
	h, s, st := frServer(t)
	tok := s.Auth.OwnerTokenValue()
	if !bootstrapOf(t, serve(h, frReq("GET", "/api/xbin/first-run", "", tok))) {
		t.Fatal("the owner token's browser session, no accounts: want bootstrap")
	}
	bearer := httptest.NewRequest("GET", "/api/xbin/first-run", nil)
	bearer.Header.Set("Authorization", "Bearer "+tok)
	if bootstrapOf(t, serve(h, bearer)) {
		t.Fatal("bx's bearer is no browser: no sheet")
	}
	makeAda(t, st, users.User{})
	if bootstrapOf(t, serve(h, frReq("GET", "/api/xbin/first-run", "", tok))) {
		t.Fatal("an account exists: never the sheet again")
	}
	if bootstrapOf(t, serve(h, frReq("GET", "/api/xbin/first-run", "", s.Auth.NewSession("ada", "10.0.0.1")))) {
		t.Fatal("a person's session: no sheet")
	}

	// an invited account that never set a password counts as an account
	h2, s2, st2 := frServer(t)
	if _, err := st2.UpsertInvited(users.User{ID: "bob", Role: users.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if bootstrapOf(t, serve(h2, frReq("GET", "/api/xbin/first-run", "", s2.Auth.OwnerTokenValue()))) {
		t.Fatal("an invited account: not the bootstrap state")
	}

	// --no-auth: every request is the implicit owner, and nobody signs in
	na, err := auth.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	st3, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	na.SetUsers(st3)
	if bootstrapOf(t, serve((&Server{Auth: na}).Handler(), frReq("GET", "/api/xbin/first-run", "", ""))) {
		t.Fatal("--no-auth: no sheet")
	}
}

// The swap: this browser's token cookie becomes the new account's session,
// in one step; the token itself keeps working.
func TestFirstRunSessionSwap(t *testing.T) {
	h, s, st := frServer(t)
	tok := s.Auth.OwnerTokenValue()
	makeAda(t, st, users.User{Name: "Ada Lovelace"})
	w := serve(h, frReq("POST", "/api/xbin/first-run/session", `{"id":"Ada","password":"`+adaPass+`"}`, tok))
	if w.Code != http.StatusOK {
		t.Fatalf("swap: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		User struct{ ID, Name, Role string }
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.User.ID != "ada" || out.User.Role != users.RoleAdmin || out.User.Name != "Ada Lovelace" {
		t.Fatalf("answer: %s", w.Body.String())
	}
	sid := sessionCookie(t, w)
	if sid == tok {
		t.Fatal("the cookie must be a session, not the token")
	}
	var who struct {
		User  string
		Owner bool
		Via   string
	}
	pw := serve(h, frReq("GET", "/api/xbin/probe", "", sid))
	_ = json.Unmarshal(pw.Body.Bytes(), &who)
	if who.User != "ada" || who.Owner || who.Via != "session" {
		t.Fatalf("the new cookie is ada's own session: %s", pw.Body.String())
	}
	if u, _ := st.Get("ada"); u.LastLogin == 0 || u.LastLoginVia != "first-run" {
		t.Fatalf("the sign-in is stamped: %+v", u)
	}
	// the token stays the bootstrap credential
	b := httptest.NewRequest("GET", "/api/xbin/probe", nil)
	b.Header.Set("Authorization", "Bearer "+tok)
	if pw := serve(h, b); !strings.Contains(pw.Body.String(), `"owner":true`) {
		t.Fatalf("the owner token still works: %s", pw.Body.String())
	}
	// once is all: the account has signed in now
	if w := serve(h, frReq("POST", "/api/xbin/first-run/session", `{"id":"ada","password":"`+adaPass+`"}`, tok)); w.Code != http.StatusConflict {
		t.Fatalf("a second swap: %d %s", w.Code, w.Body.String())
	}
}

// Who may swap: the owner token's browser session, from this origin's own
// page — nobody else, so no one gains a session they couldn't have.
func TestFirstRunSessionCallers(t *testing.T) {
	h, s, st := frServer(t)
	tok := s.Auth.OwnerTokenValue()
	makeAda(t, st, users.User{})
	body := `{"id":"ada","password":"` + adaPass + `"}`
	check := func(name string, r *http.Request, want int) {
		t.Helper()
		if w := serve(h, r); w.Code != want {
			t.Fatalf("%s: %d %s (want %d)", name, w.Code, w.Body.String(), want)
		} else if want != http.StatusOK && len(w.Result().Cookies()) > 0 {
			t.Fatalf("%s: a refusal sets no cookie", name)
		}
	}
	bearer := frReq("POST", "/api/xbin/first-run/session", body, "")
	bearer.Header.Set("Authorization", "Bearer "+tok)
	check("bx's bearer", bearer, http.StatusForbidden)
	check("a person's own session", frReq("POST", "/api/xbin/first-run/session", body, s.Auth.NewSession("ada", "10.0.0.1")), http.StatusForbidden)
	check("no credential", frReq("POST", "/api/xbin/first-run/session", body, ""), http.StatusUnauthorized)
	for _, site := range []string{"cross-site", "same-site", "none"} {
		r := frReq("POST", "/api/xbin/first-run/session", body, tok)
		r.Header.Set("Sec-Fetch-Site", site)
		want := http.StatusForbidden
		if site != "none" {
			want = http.StatusUnauthorized // a cross- or same-site fetch carrying the cookie reads as a tile's: the cookie is dropped first
		}
		check("Sec-Fetch-Site "+site, r, want)
	}
	noMeta := func(origin string) *http.Request {
		r := withCookie("POST", "/api/xbin/first-run/session", body, tok)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	check("no Fetch Metadata, no Origin", noMeta(""), http.StatusForbidden)
	check("no Fetch Metadata, another Origin", noMeta("https://evil.example"), http.StatusForbidden)
	check("no Fetch Metadata, Origin null", noMeta("null"), http.StatusForbidden)
	// the synthetic principals no route hands out to a browser
	for _, p := range []auth.Principal{
		{Owner: true, Via: "bearer"}, {Owner: true, Via: "dev"}, {Owner: true, Via: "frame", Component: "apps/x"},
		{Owner: true, Via: "cookie", Impersonator: "owner"}, {UserID: "ada", Via: "session"}, {UserID: "ada", Via: "delegate", Agent: "xbin/wsagent"},
	} {
		if tokenSession(p) {
			t.Fatalf("tokenSession(%+v) must be false", p)
		}
	}
	if !tokenSession(auth.Principal{Owner: true, Via: "cookie"}) {
		t.Fatal("the bootstrap cookie is a token session")
	}
	check("Origin naming this host, no Fetch Metadata", noMeta("http://example.com"), http.StatusOK)
}

// The bootstrap account only: never one that existed before, was used, or
// isn't the workspace's only enabled admin.
func TestFirstRunSessionAccount(t *testing.T) {
	swap := func(t *testing.T, setup func(st *users.Store), id, pass string) *httptest.ResponseRecorder {
		t.Helper()
		h, s, st := frServer(t)
		setup(st)
		return serve(h, frReq("POST", "/api/xbin/first-run/session", `{"id":"`+id+`","password":"`+pass+`"}`, s.Auth.OwnerTokenValue()))
	}
	cases := []struct {
		name  string
		setup func(st *users.Store)
		id    string
		want  int
	}{
		{"no account yet", func(*users.Store) {}, "ada", http.StatusConflict},
		{"two accounts", func(st *users.Store) { makeAda(t, st, users.User{}); makeAda(t, st, users.User{ID: "bob"}) }, "ada", http.StatusConflict},
		{"another id than the one account", func(st *users.Store) { makeAda(t, st, users.User{}) }, "root", http.StatusConflict},
		{"a non-admin", func(st *users.Store) { makeAda(t, st, users.User{Role: users.RoleUser}) }, "ada", http.StatusConflict},
		{"a disabled admin", func(st *users.Store) { makeAda(t, st, users.User{Disabled: true}) }, "ada", http.StatusConflict},
		{"signed in before", func(st *users.Store) {
			makeAda(t, st, users.User{})
			if err := st.TouchLogin("ada", "password"); err != nil {
				t.Fatal(err)
			}
		}, "ada", http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if w := swap(t, c.setup, c.id, adaPass); w.Code != c.want || len(w.Result().Cookies()) > 0 {
				t.Fatalf("%d %s (want %d, no cookie)", w.Code, w.Body.String(), c.want)
			}
		})
	}
	t.Run("made over 15 minutes ago", func(t *testing.T) {
		defer func(f func() time.Time) { firstRunNow = f }(firstRunNow)
		firstRunNow = func() time.Time { return time.Now().Add(16 * time.Minute) }
		if w := swap(t, func(st *users.Store) { makeAda(t, st, users.User{}) }, "ada", adaPass); w.Code != http.StatusConflict {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("an invited account with no password", func(t *testing.T) {
		w := swap(t, func(st *users.Store) {
			if _, err := st.UpsertInvited(users.User{ID: "ada", Role: users.RoleAdmin}); err != nil {
				t.Fatal(err)
			}
		}, "ada", "anything-at-all")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
}

// The password is the account's own, throttled with /login.
func TestFirstRunSessionThrottle(t *testing.T) {
	h, s, st := frServer(t)
	tok := s.Auth.OwnerTokenValue()
	makeAda(t, st, users.User{})
	for i := 0; i < throttleMaxFails; i++ {
		if w := serve(h, frReq("POST", "/api/xbin/first-run/session", `{"id":"ada","password":"not-it-at-all"}`, tok)); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := serve(h, frReq("POST", "/api/xbin/first-run/session", `{"id":"ada","password":"`+adaPass+`"}`, tok))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("throttled even with the right one: %d %s", w.Code, w.Body.String())
	}
	// …and so is /login, from the same address: one throttle
	r := httptest.NewRequest("POST", "/login", strings.NewReader("username=ada&password="+adaPass))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w := serve(h, r); w.Code != http.StatusTooManyRequests {
		t.Fatalf("/login shares the throttle: %d", w.Code)
	}
	if w := serve(h, frReq("POST", "/api/xbin/first-run/session", `{"id":"ada"}`, tok)); w.Code != http.StatusTooManyRequests && w.Code != http.StatusBadRequest {
		t.Fatalf("a body without a password: %d", w.Code)
	}
}
