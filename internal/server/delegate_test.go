package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// dHolder stands in for the sealed workspace-agent component.
const dHolder = "xbin/wsagent"

// dWorld is two servers over one Auth and one delegation store: full, the
// real handler (the term server's: every core route with its own handler,
// the delegation API included), and inv, whose API mux holds a stub for
// EVERY route of the inventory (the delegate table's keys) — a stub says it
// ran — plus a stub component proxy. alice and dave are admins; bob is a
// user who reads apps/x and apps/mail; each of alice and bob has a
// partition of the holder. Workspace agents are on, the agent policy allows
// every tile and starts nothing unasked.
type dWorld struct {
	t      *testing.T
	full   http.Handler
	s      *Server
	inv    http.Handler
	invS   *Server
	store  *delegation.Store
	policy *dPolicy

	mu   sync.Mutex
	tile []tileCall // what the stub component proxy saw
}

type tileCall struct {
	p      auth.Principal
	method string
	path   string
	body   string
	ctx    context.Context
}

// dPolicy is the world's delegation.Policy and its servers' tile routing
// (DelegatePolicy): everything allowed, until a test narrows it.
type dPolicy struct {
	NoopPolicy
	mu    sync.Mutex
	deny  map[string]error                 // tile → the org's refusal
	prefs map[string]delegation.StartPrefs // user → starts without asking
	asked []delegation.Subject
	// readCheck: DelegateTile also refuses a tile its person can't read (as
	// the broker's does); onAsk runs once when the tile's rules are asked.
	readCheck bool
	onAsk     func()
}

func (d *dPolicy) TileAllows(s delegation.Subject) error {
	d.mu.Lock()
	hook := d.onAsk
	d.asked = append(d.asked, s)
	err := d.deny[s.Tile]
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}
func (d *dPolicy) SessionAllows(s delegation.Subject, _ string) error { return d.TileAllows(s) }
func (d *dPolicy) StartPrefs(user, _, _ string) (delegation.StartPrefs, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.prefs[user]
	return p, ok
}
func (d *dPolicy) DelegateTile(p auth.Principal, tile string, write bool) (string, string, bool, error) {
	if err := d.TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}); err != nil {
		return "", "", false, err
	}
	d.mu.Lock()
	check := d.readCheck
	d.mu.Unlock()
	if check && !p.CanReadTile(tile) {
		return "", "", false, errors.New(p.UserID + " can't read " + tile)
	}
	return "write", "writer", false, nil
}
func (d *dPolicy) set(f func(*dPolicy)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}
func (d *dPolicy) last() delegation.Subject {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.asked[len(d.asked)-1]
}

func delegateWorld(t *testing.T) *dWorld {
	t.Helper()
	full, s := termServer(t)
	w := &dWorld{t: t, full: full, s: s, store: delegation.OpenStore(t.TempDir()),
		policy: &dPolicy{deny: map[string]error{}, prefs: map[string]delegation.StartPrefs{}}}
	t.Cleanup(auth.TestSealDelegate(dHolder))
	if _, err := s.Auth.Users.Upsert(users.User{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead, "apps/mail": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}
	s.Auth.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })
	s.Auth.SetDelegationGate(DelegationGate(w.store, true, false))
	if _, err := w.store.SetWorkspace(true, "test"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "bob", "dave"} {
		s.Auth.RegisterInstancePartition("d-"+u, dHolder, "", util.UserPartition(u), "uid-"+u)
	}
	s.Auth.RegisterInstance("i-main", "apps/x")
	s.Auth.RegisterInstanceDeployment("i-dev", "apps/x", "dev")
	s.Delegation, s.AgentPolicy, s.Pol = w.store, w.policy, w.policy
	s.Term.AgentPolicy = w.policy

	root := t.TempDir()
	for _, tile := range []string{"apps/x", "apps/mail"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(tile)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(tile), "xbin.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Reg = reg // the static plane asks it
	w.invS = &Server{Auth: s.Auth, Reg: reg, Delegation: w.store, AgentPolicy: w.policy, Pol: w.policy, Hub: events.NewHub()}
	for pattern := range DelegateClasses() {
		w.invS.RegisterAPI(pattern, func(rw http.ResponseWriter, r *http.Request) { rw.Header().Set("X-Ran", pattern) })
	}
	w.invS.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.tile = append(w.tile, tileCall{p: auth.PrincipalOf(r), method: r.Method, path: r.URL.Path, body: string(b), ctx: r.Context()})
		w.mu.Unlock()
		rw.Header().Set("X-Ran", "tile")
	})
	w.inv = w.invS.authedAPI(http.HandlerFunc(w.invS.handleAPI))
	return w
}

func (w *dWorld) tileCalls() []tileCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]tileCall(nil), w.tile...)
}

// cred puts one kind of credential on a request.
type cred func(r *http.Request)

func (w *dWorld) cookie(user string) cred {
	sid := w.s.Auth.NewSession(user, "")
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
}

// viewAs is admin looking at the workspace as user (the real flow).
func (w *dWorld) viewAs(admin, user string) cred {
	w.t.Helper()
	sid := w.s.Auth.NewSession(admin, "")
	rec := httptest.NewRecorder()
	w.full.ServeHTTP(rec, withCookie("POST", "/api/xbin/impersonate", `{"user":"`+user+`"}`, sid))
	var m struct{ URL string }
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	rec = httptest.NewRecorder()
	w.full.ServeHTTP(rec, withCookie("GET", m.URL, "", sid))
	view := sessionCookie(w.t, rec)
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: view}) }
}

func bearer(tok string) cred {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// delegateCred is a delegate call as a sandbox makes it: the scheme, on the
// gateway socket.
func delegateCred(tok string) cred {
	return func(r *http.Request) {
		r.Header.Set("Authorization", auth.DelegateScheme+" "+tok)
		*r = *r.WithContext(auth.WithGateway(r.Context()))
	}
}

func header(k, v string) cred { return func(r *http.Request) { r.Header.Set(k, v) } }

// frame is tile's frame for user as a terminal or the root token would
// mint it: no login of the person's behind it.
func (w *dWorld) frame(tile, user string) cred {
	tok := w.s.Auth.MintFrameToken(tile, user, time.Minute)
	return func(r *http.Request) { r.Header.Set(auth.FrameTokenHeader, tok) }
}

// loginFrame is tile's frame for user under their own login: the token a
// document served to their browser session carries.
func (w *dWorld) loginFrame(tile, user string) cred {
	w.t.Helper()
	sid := w.s.Auth.NewSession(user, "")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid})
	p, ok := w.s.Auth.FromRequest(r)
	if !ok {
		w.t.Fatal("no session principal")
	}
	tok := w.s.Auth.MintFrameTokenFor(p, tile, time.Minute)
	return func(r *http.Request) { r.Header.Set(auth.FrameTokenHeader, tok) }
}

func (w *dWorld) do(h http.Handler, method, path, body string, creds ...cred) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, c := range creds {
		c(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

// ask posts an approval request as the agent and returns it.
func (w *dWorld) ask(agent cred, method, path, body string) delegation.Request {
	w.t.Helper()
	in, _ := json.Marshal(map[string]string{"method": method, "path": path, "body": body, "summary": "the agent's words"})
	rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), agent)
	if rec.Code != 200 {
		w.t.Fatalf("ask %s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	return decode[delegation.Request](w.t, rec)
}

// approve approves request id as the person.
func (w *dWorld) approve(person cred, id, grant string) {
	w.t.Helper()
	body := ""
	if grant != "" {
		body = `{"grant":"` + grant + `"}`
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+id+"/approve", body, person); rec.Code != 200 {
		w.t.Fatalf("approve %s: %d %s", id, rec.Code, rec.Body.String())
	}
}

// routeURL is a request for a route pattern: its method (GET for a pattern
// without one) and a path with every wildcard filled.
func routeURL(pattern string) (method, path string) {
	method, path = "GET", pattern
	if m, p, ok := strings.Cut(pattern, " "); ok {
		method, path = m, p
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		switch {
		case strings.HasSuffix(s, "...}"):
			segs[i] = "a/b"
		case strings.HasPrefix(s, "{"):
			segs[i] = "x1"
		}
	}
	return method, "/api/xbin" + strings.Join(segs, "/")
}

// The authz matrix: every route of the inventory, for every kind of
// principal. A delegate reaches exactly the rows the delegation cap lists
// (delegateclass.go) — less what needs its person's approval or opt-in —
// and is refused everywhere else before a handler runs; every other kind of
// principal reaches what the gates that existed before delegation let it
// reach: the delegate gate changes nothing for them.
func TestDelegateAuthzMatrix(t *testing.T) {
	w := delegateWorld(t)
	if _, err := w.store.SetState("dave", func(st *delegation.State) { st.AdminChanges = true }); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	w.invS.delegateRT.setClock(func() time.Time { now = now.Add(time.Minute); return now }) // the matrix is no burst of acts
	readOnly := func(m string) bool { return m == "GET" || m == "HEAD" }
	personOnly := func(p string) bool { return PartitionClassOf(p) == PersonOnly }
	delegated := func(adminReads, admin bool) func(string, string) bool {
		return func(_, p string) bool {
			row := delegateClasses[p]
			switch {
			case p == "GET /components/{path...}" && !admin:
				return false // the matrix asks for a/b, a tile bob can't read: refused at the person's level

			case delegateUnconverted[p] != "", row.ticket: // a ticketed act without a ticket or a preference
				return false
			case row.class == DelegateAdminRead:
				return adminReads
			}
			return row.class == DelegateRead || row.class == DelegateAct
		}
	}
	kinds := []struct {
		name string
		cred cred
		runs func(method, pattern string) bool
	}{
		{"a person (a user)", w.cookie("bob"), func(string, string) bool { return true }},
		{"an admin", w.cookie("alice"), func(string, string) bool { return true }},
		{"view-as (an admin viewing as bob)", w.viewAs("alice", "bob"), func(m, p string) bool {
			return (readOnly(m) && !personOnly(p)) || p == "POST /impersonate/stop"
		}},
		{"an element (apps/x's frame, driven by bob)", w.frame("apps/x", "bob"), func(_, p string) bool { return !personOnly(p) }},
		{"an element (apps/x's backend)", bearer("i-main"), func(_, p string) bool { return !personOnly(p) }},
		{"a non-primary deployment (apps/x+dev's backend)", bearer("i-dev"), func(_, p string) bool {
			c := RouteClassOf(p)
			return (c == DeploymentScoped || c == Neutral && primaryOnlyFor[p] != "instance") && !personOnly(p)
		}},
		{"nobody", func(*http.Request) {}, func(string, string) bool { return false }},
		{"a delegate (bob's agent)", delegateCred("d-bob"), delegated(false, false)},
		{"a delegate (alice's agent; alice is an admin who didn't opt in)", delegateCred("d-alice"), delegated(false, true)},
		{"a delegate (dave's agent; dave is an admin who opted in)", delegateCred("d-dave"), delegated(true, true)},
		{"a credential that delegates nothing (apps/x's backend's token)", delegateCred("i-main"), func(string, string) bool { return false }},
		{"a delegate credential off the gateway socket", header("Authorization", auth.DelegateScheme+" d-bob"), func(string, string) bool { return false }},
	}
	patterns := make([]string, 0, len(DelegateClasses()))
	for p := range DelegateClasses() {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	if len(patterns) < 320 {
		t.Fatalf("the inventory has %d routes: the class table shrank?", len(patterns))
	}
	for _, k := range kinds {
		delegate := strings.HasPrefix(k.name, "a delegate (")
		ran := 0
		for _, p := range patterns {
			method, path := routeURL(p)
			rec := w.do(w.inv, method, path, "{}", k.cred)
			got, want := rec.Header().Get("X-Ran") == p, k.runs(method, p)
			if got {
				ran++
			}
			if got != want {
				t.Errorf("%s: %s ran=%v, want %v (%d %s)", k.name, p, got, want, rec.Code, strings.TrimSpace(rec.Body.String()))
			}
			if !got && rec.Header().Get("X-Ran") != "" {
				t.Errorf("%s: %s ran another route's handler (%s)", k.name, p, rec.Header().Get("X-Ran"))
			}
			if delegate && !got {
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"refusal":"`) {
					t.Errorf("%s: %s refused with %d %s, want the gate's 403", k.name, p, rec.Code, strings.TrimSpace(rec.Body.String()))
				}
				if why := delegateClasses[p].why; why != "" && !strings.Contains(rec.Body.String(), why[:40]) {
					t.Errorf("%s: %s refused without its reason: %s", k.name, p, strings.TrimSpace(rec.Body.String()))
				}
			}
		}
		t.Logf("%-66s reaches %3d of %d routes", k.name, ran, len(patterns))
	}
}

// The table itself. (Its completeness over the routes xbind really mounts
// is internal/apicheck's TestDelegateRouteClasses.)
func TestDelegateNeverList(t *testing.T) {
	for p, row := range delegateClasses {
		if (row.class == DelegateNever || row.class == DelegateRefused) != (row.why != "") {
			t.Errorf("%s: class %s, reason %q", p, row.class, row.why)
		}
		if row.tile != nil && !row.class.Allowed() {
			t.Errorf("%s names a tile and isn't delegated", p)
		}
	}
	// the never-list (docs/auth.md §Delegation), by route: a later edit that
	// delegates one of these fails here
	for _, p := range []string{
		"POST /term/sessions/{id}/permissions/{pid}", "POST /term/sessions/{id}/elicitations/{eid}", // answering a tile agent
		"POST /term/sessions/{id}/options", "POST /term/sessions/{id}/restart", "PATCH /term/sessions/{id}", // …or deciding what it asks
		"POST /lifecycle", "DELETE /agent/history/{id}", // deleting
		"POST /create", "POST /clone", "POST /git/import", "POST /code/prs", "POST /code/pr/comment", "POST /code/pr/state", // code writes
		"POST /account/password", "POST /devices/enroll", "POST /devices/enroll-code", "DELETE /devices/{id}", "POST /web-ticket", // the account
		"GET /frame-token", "POST /path-tickets", "POST /impersonate", // other identities
		"POST /partitions/consents", "DELETE /partitions/consents", "POST /partitions/binds", // the person's own consents
		"POST /grants", "DELETE /grants", "POST /bindings", "DELETE /bindings", "POST /limits", "PUT /access", "POST /users", // the admin API
		"PATCH /auth-settings", "POST /vault-unseal", "POST /vault-rekey", "POST /auth-rotate-token", "GET /users", "GET /vaults", "GET /sessions",
		"GET /vault/{rest...}", "PUT /kv/{rest...}", "POST /bus/publish", // a tile's stores
		// answering itself
		"POST /delegate/requests/{id}/approve", "POST /delegate/requests/{id}/deny", "POST /delegate/approvals", "DELETE /delegate/approvals/{id}",
		"PUT /delegate/state", "PUT /delegate/workspace", "GET /delegate/audit",
		"POST /delegate/changesets/{id}/approve", "POST /delegate/changesets/{id}/reject", "POST /delegate/changesets/{id}/undo",
	} {
		if DelegateClassOf(p) != DelegateNever {
			t.Errorf("%s must be on the never-list, and is %s", p, DelegateClassOf(p))
		}
	}
	// whatever a delegate may do that isn't a read is one of these, and
	// nothing else; a new one is a decision
	var acts, tickets, admin []string
	for p, row := range delegateClasses {
		switch {
		case row.class == DelegateAct && row.ticket:
			tickets = append(tickets, p)
		case row.class == DelegateAct:
			acts = append(acts, p)
		case row.class == DelegateAdminRead:
			admin = append(admin, p)
		}
	}
	for _, c := range []struct {
		name string
		got  []string
		want string
	}{
		{"acts that need the person's approval", tickets, "POST /templates/new\nPOST /term/sessions"},
		{"acts that need none (driving its own sessions, asking)", acts, "DELETE /delegate/changesets/{id}\nDELETE /delegate/requests/{id}\nDELETE /term/sessions/{id}\n" +
			"DELETE /term/sessions/{id}/queue\nDELETE /term/sessions/{id}/queue/{qid}\nPOST /delegate/changesets\nPOST /delegate/requests\n" +
			"POST /term/sessions/{id}/cancel\nPOST /term/sessions/{id}/prompt\nPOST /term/sessions/{id}/queue"},
		{"admin reads", admin, "GET /bindings\nGET /grants\nGET /limits\nGET /orgs"},
	} {
		sort.Strings(c.got)
		if got := strings.Join(c.got, "\n"); got != c.want {
			t.Errorf("the %s are\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

// The same person's browser answers a tile agent's permission request and
// question; their agent, holding their identity, cannot — on the real
// handlers. And a delegate credential is nobody's on every plane but /api/.
func TestDelegateCannotAnswerOrLeaveTheAPI(t *testing.T) {
	w := delegateWorld(t)
	alice, agent := w.cookie("alice"), delegateCred("d-alice")
	for _, c := range []struct{ method, path, body, why string }{
		{"POST", "/api/xbin/term/sessions/nope/permissions/p1", `{"decision":"allow_once"}`, neverAnswer},
		{"POST", "/api/xbin/term/sessions/nope/elicitations/e1", `{"action":"accept","content":{}}`, neverAnswer},
		{"POST", "/api/xbin/term/sessions/nope/options", `{"id":"mode","value":"bypassPermissions"}`, neverSettings},
		{"POST", "/api/xbin/term/sessions/nope/restart", `{}`, neverSettings},
	} {
		// the person: the handler runs and finds no such session
		if rec := w.do(w.full, c.method, c.path, c.body, alice); rec.Code != http.StatusNotFound {
			t.Errorf("alice %s %s: %d %s, want the handler's 404", c.method, c.path, rec.Code, rec.Body.String())
		}
		// their agent: refused before the handler, with the reason
		if rec := w.do(w.full, c.method, c.path, c.body, agent); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), c.why) {
			t.Errorf("alice's agent %s %s: %d %s, want 403 %q", c.method, c.path, rec.Code, rec.Body.String(), c.why)
		}
	}
	// what the agent may do to a session reaches the handler as the person
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/xbin/term/sessions/nope/prompt", `{"text":"hi"}`}, {"POST", "/api/xbin/term/sessions/nope/queue", `{"text":"hi"}`},
		{"POST", "/api/xbin/term/sessions/nope/cancel", ""}, {"GET", "/api/xbin/term/sessions/nope", ""}, {"GET", "/api/xbin/term/sessions/nope/events", ""},
		{"GET", "/api/xbin/term/sessions/nope/log", ""}, {"GET", "/api/xbin/term/sessions/nope/diff?turn=1", ""}, {"DELETE", "/api/xbin/term/sessions/nope", ""},
	} {
		if rec := w.do(w.full, c.method, c.path, c.body, agent); rec.Code != http.StatusNotFound {
			t.Errorf("alice's agent %s %s: %d %s, want the handler's 404", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	// the session directory is the person's own
	if rec := w.do(w.full, "GET", "/api/xbin/term/sessions", "", agent); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("the directory: %d %s", rec.Code, rec.Body.String())
	}
	// an admin's agent is no admin: ?user= is refused as for any user
	if rec := w.do(w.full, "GET", "/api/xbin/term/sessions?user=bob", "", agent); rec.Code != http.StatusForbidden {
		t.Errorf("?user= by an admin's agent: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "GET", "/api/xbin/probe", "", agent); rec.Code != http.StatusForbidden {
		t.Errorf("a route without a row: %d", rec.Code)
	}

	// no terminal, no static plane, no event socket, no docs, no pages
	for _, c := range []struct{ method, path string }{
		{"GET", "/ws/term?cwd=apps/x"}, {"DELETE", "/ws/term?session=x"}, {"GET", "/ws/events"},
		{"GET", "/c/root/"}, {"GET", "/c/apps/x/index.html"}, {"GET", "/docs/auth.md"}, {"GET", "/"}, {"GET", PersonPagePath},
	} {
		if rec := w.do(w.full, c.method, c.path, "", agent); rec.Code != http.StatusUnauthorized {
			t.Errorf("alice's agent %s %s: %d, want 401 (the credential is nobody's there)", c.method, c.path, rec.Code)
		}
	}
	// off the gateway socket the scheme is no credential, on /api/ too
	if rec := w.do(w.full, "GET", "/api/xbin/whoami", "", header("Authorization", auth.DelegateScheme+" d-alice")); rec.Code != http.StatusUnauthorized {
		t.Errorf("off the gateway: %d", rec.Code)
	}
	// the delegate scheme beside a real session is judged as the delegate
	if rec := w.do(w.inv, "POST", "/api/xbin/users", `{}`, alice, delegateCred("d-bob")); rec.Code != http.StatusForbidden || rec.Header().Get("X-Ran") != "" {
		t.Errorf("an admin's cookie beside bob's delegate credential: %d ran %q", rec.Code, rec.Header().Get("X-Ran"))
	}
}
