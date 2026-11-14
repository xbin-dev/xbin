package server

import (
	"encoding/json"
	"errors"
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
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// dHolder stands in for the sealed workspace-agent component.
const dHolder = "xbin/agent"

// dWorld is two servers over one Auth: full, the real handler (the term
// server's: every core route with its own handler), and inv, whose API mux
// holds a stub for EVERY route of the inventory (the class tables' keys) —
// a stub says it ran — plus a stub component proxy. alice and dave are
// admins, bob a user; each of alice and bob has a partition of the holder.
type dWorld struct {
	t    *testing.T
	full http.Handler
	s    *Server
	inv  http.Handler
	invS *Server
	log  *delegation.Log

	mu   sync.Mutex
	off  map[string]error // the off switch, per person
	tile []auth.Principal // the principals the stub component proxy saw
}

func delegateWorld(t *testing.T) *dWorld {
	t.Helper()
	full, s := termServer(t)
	w := &dWorld{t: t, full: full, s: s, off: map[string]error{}, log: delegation.Open(t.TempDir())}
	t.Cleanup(auth.TestSealDelegate(dHolder))
	s.Auth.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })
	s.Auth.SetDelegationGate(func(_, user string) error {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.off[user]
	})
	for _, u := range []string{"alice", "bob"} {
		s.Auth.RegisterInstancePartition("d-"+u, dHolder, "", util.UserPartition(u), "uid-"+u)
	}
	s.Auth.RegisterInstance("i-main", "apps/x")
	s.Auth.RegisterInstanceDeployment("i-dev", "apps/x", "dev")
	s.DelegateLog = w.log

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "x", "xbin.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s.Reg = reg // the static plane asks it
	w.invS = &Server{Auth: s.Auth, Reg: reg, DelegateLog: w.log}
	for pattern := range RouteClasses() {
		w.invS.RegisterAPI(pattern, func(rw http.ResponseWriter, r *http.Request) { rw.Header().Set("X-Ran", pattern) })
	}
	w.invS.RegisterAPI("GET /delegate/audit", w.invS.apiDelegateAudit) // mounted with the workspace agent; here for its test
	w.invS.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.tile = append(w.tile, auth.PrincipalOf(r))
		w.mu.Unlock()
		rw.Header().Set("X-Ran", "tile")
	})
	w.inv = w.invS.authedAPI(http.HandlerFunc(w.invS.handleAPI))
	return w
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

func delegateCred(tok string) cred {
	return func(r *http.Request) { r.Header.Set("Authorization", auth.DelegateScheme+" "+tok) }
}

func (w *dWorld) frame(tile, user string) cred {
	tok := w.s.Auth.MintFrameToken(tile, user, time.Minute)
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
// (delegateclass.go) and is refused everywhere else before a handler runs;
// every other kind of principal reaches what the gates that existed before
// delegation let it reach — the delegate gate changes nothing for them.
func TestDelegateAuthzMatrix(t *testing.T) {
	w := delegateWorld(t)
	isRead := func(m string) bool { return m == "GET" || m == "HEAD" }
	personOnly := func(p string) bool { return PartitionClassOf(p) == PersonOnly }
	kinds := []struct {
		name string
		cred cred
		runs func(method, pattern string) bool
	}{
		{"a person (a user)", w.cookie("bob"), func(string, string) bool { return true }},
		{"an admin", w.cookie("alice"), func(string, string) bool { return true }},
		{"view-as (an admin viewing as bob)", w.viewAs("alice", "bob"), func(m, p string) bool {
			return (isRead(m) && !personOnly(p)) || p == "POST /impersonate/stop"
		}},
		{"an element (apps/x's frame, driven by bob)", w.frame("apps/x", "bob"), func(_, p string) bool { return !personOnly(p) }},
		{"an element (apps/x's backend)", bearer("i-main"), func(_, p string) bool { return !personOnly(p) }},
		{"a non-primary deployment (apps/x+dev's backend)", bearer("i-dev"), func(_, p string) bool {
			c := RouteClassOf(p)
			return (c == DeploymentScoped || c == Neutral && primaryOnlyFor[p] != "instance") && !personOnly(p)
		}},
		{"nobody", func(*http.Request) {}, func(string, string) bool { return false }},
		{"a delegate (bob's agent)", delegateCred("d-bob"), func(_, p string) bool {
			c := DelegateClassOf(p)
			return (c == DelegateRead || c == DelegateAct) && delegateUnconverted[p] == ""
		}},
		{"a delegate (alice's agent; alice is an admin)", delegateCred("d-alice"), func(_, p string) bool {
			c := DelegateClassOf(p)
			return (c == DelegateRead || c == DelegateAct) && delegateUnconverted[p] == ""
		}},
		{"a credential that delegates nothing (apps/x's backend's token)", delegateCred("i-main"), func(string, string) bool { return false }},
	}
	patterns := make([]string, 0, len(RouteClasses()))
	for p := range RouteClasses() {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	if len(patterns) < 300 {
		t.Fatalf("the inventory has %d routes: the class table shrank?", len(patterns))
	}
	for _, k := range kinds {
		delegate := strings.HasPrefix(k.name, "a delegate")
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
			if delegate && !got {
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"refusal":"not-delegated"`) {
					t.Errorf("%s: %s refused with %d %s, want the gate's 403", k.name, p, rec.Code, strings.TrimSpace(rec.Body.String()))
				}
				if why := delegateClasses[p].why; why != "" && !strings.Contains(rec.Body.String(), why[:40]) {
					t.Errorf("%s: %s refused without its reason: %s", k.name, p, strings.TrimSpace(rec.Body.String()))
				}
			}
		}
		t.Logf("%-62s reaches %3d of %d routes", k.name, ran, len(patterns))
	}
}

// The table itself: every row is a route that exists; a read row is a GET
// and an act row isn't; nothing a person must do themselves, and nothing on
// the never-list of the plan, is ever listed as delegated.
func TestDelegateNeverList(t *testing.T) {
	inv := RouteClasses()
	for p, row := range delegateClasses {
		if _, ok := inv[p]; !ok {
			t.Errorf("delegateclass.go lists %s, which is no route", p)
		}
		method, _, _ := strings.Cut(p, " ")
		switch row.class {
		case DelegateRead:
			if method != "GET" {
				t.Errorf("%s is listed as a read", p)
			}
		case DelegateAct:
			if method == "GET" {
				t.Errorf("%s is listed as an act", p)
			}
		case DelegateNever:
			if row.why == "" {
				t.Errorf("%s is on the never-list without a reason", p)
			}
		}
		if row.class != DelegateNever && PartitionClassOf(p) == PersonOnly {
			t.Errorf("%s is a person's own act (partitionclass.go) and listed as delegated", p)
		}
	}
	for p := range delegateUnconverted {
		if c := DelegateClassOf(p); c != DelegateRead && c != DelegateAct {
			t.Errorf("delegateUnconverted lists %s, which isn't delegated", p)
		}
	}
	// the plan's never-list (plans/workspace-agent.md §4), by route: a later
	// edit that lists one of these fails here
	for _, p := range []string{
		"POST /term/sessions/{id}/permissions/{pid}", "POST /term/sessions/{id}/elicitations/{eid}", // answering a tile agent
		"POST /term/sessions/{id}/options", "POST /term/sessions/{id}/restart", // …or deciding what it asks
		"DELETE /term/sessions/{id}", "POST /lifecycle", // ending and deleting
		"POST /create", "POST /clone", "POST /git/import", "POST /code/prs", "POST /code/pr/comment", "POST /code/pr/state", // code writes
		"POST /account/password", "POST /devices/enroll", "POST /devices/enroll-code", "DELETE /devices/{id}", "POST /web-ticket", // the account
		"GET /frame-token", "POST /path-tickets", "POST /impersonate", // other identities
		"POST /partitions/consents", "DELETE /partitions/consents", "POST /partitions/binds", // the person's own consents
		"POST /grants", "DELETE /grants", "POST /bindings", "DELETE /bindings", "POST /limits", "PUT /access", "POST /users", // the admin API
		"PATCH /auth-settings", "POST /vault-unseal", "POST /vault-rekey", "POST /auth-rotate-token", // never in a change set either
	} {
		if DelegateClassOf(p) != DelegateNever {
			t.Errorf("%s must be on the never-list, and is %s", p, DelegateClassOf(p))
		}
	}
	// every admin, governance and store write stays undelegated: whatever a
	// delegate may do that isn't a GET is one of these, and nothing else
	acts := []string{}
	for p, row := range delegateClasses {
		if row.class == DelegateAct {
			acts = append(acts, p)
		}
	}
	sort.Strings(acts)
	want := []string{"DELETE /term/sessions/{id}/queue", "DELETE /term/sessions/{id}/queue/{qid}", "POST /templates/new",
		"POST /term/sessions", "POST /term/sessions/{id}/cancel", "POST /term/sessions/{id}/prompt", "POST /term/sessions/{id}/queue"}
	if strings.Join(acts, "\n") != strings.Join(want, "\n") {
		t.Errorf("the delegated side effects are\n%s\nwant\n%s\n(a new one is a decision: plans/wsagent/s2-delegation.md)", strings.Join(acts, "\n"), strings.Join(want, "\n"))
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
		{"DELETE", "/api/xbin/term/sessions/nope", "", neverDelete},
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
		t.Errorf("an unlisted route: %d", rec.Code)
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
	// the delegate scheme beside a real session is judged as the delegate
	if rec := w.do(w.inv, "POST", "/api/xbin/users", `{}`, alice, delegateCred("d-bob")); rec.Code != http.StatusForbidden || rec.Header().Get("X-Ran") != "" {
		t.Errorf("an admin's cookie beside bob's delegate credential: %d ran %q", rec.Code, rec.Header().Get("X-Ran"))
	}
}

// One agent session create as the person: the person's own gates decide
// (terminal level on the tile), then the delegate's narrowing — and the
// create stops only at the missing bx, after every gate (the session itself
// runs in internal/term's TestAgentSessionAsDelegate).
func TestDelegateCreatesAgentSession(t *testing.T) {
	w := delegateWorld(t)
	create := func(c cred, body string) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, c)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	alice, bob := delegateCred("d-alice"), delegateCred("d-bob")
	if c, b := create(alice, `{"cwd":"apps/x","kind":"agent","provider":"claude"}`); c != 503 || !strings.Contains(b, "bx binary") {
		t.Errorf("alice's agent: %d %s, want every gate passed (503: no bx here)", c, b)
	}
	if c, b := create(alice, `{"cwd":"apps/x","kind":"agent","provider":"claude","model":"sonnet"}`); c != 503 {
		t.Errorf("alice's agent, a model: %d %s", c, b)
	}
	// bob holds no terminal level on apps/x: his agent is refused as he is
	if c, b := create(bob, `{"cwd":"apps/x","kind":"agent","provider":"claude"}`); c != 403 || !strings.Contains(b, "terminal access") {
		t.Errorf("bob's agent: %d %s", c, b)
	}
	if c, b := create(w.cookie("bob"), `{"cwd":"apps/x","kind":"agent","provider":"claude"}`); c != 403 || !strings.Contains(b, "terminal access") {
		t.Errorf("bob: %d %s", c, b)
	}
	// a mode that stops asking the person is answering for them
	for _, body := range []string{
		`{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"bypassPermissions"}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"acceptEdits"}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","options":{"mode":"bypassPermissions"}}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","vm":true}`,
	} {
		if c, b := create(alice, body); c != 403 || !strings.Contains(b, "workspace agent") {
			t.Errorf("alice's agent %s: %d %s", body, c, b)
		}
		if c, b := create(w.cookie("alice"), body); c != 503 { // alice herself picks freely
			t.Errorf("alice %s: %d %s", body, c, b)
		}
	}
	// never a shell
	if c, _ := create(alice, `{"cwd":"apps/x","kind":"shell"}`); c != 400 {
		t.Errorf("a shell: %d", c)
	}
}

type dPolicy struct {
	NoopPolicy
	deny map[string]error
}

func (d dPolicy) DelegateTile(p auth.Principal, tile string) (string, string, error) {
	if err := d.deny[tile]; err != nil {
		return "", "", err
	}
	return "write", "writer", nil
}

// One tile API read as the person: the gate hands the component proxy the
// person's delegate principal (the broker's Route and the proxy's headers
// are TestRouteDelegate and TestDelegateCallReachesBackend). Then the dry
// run, the off switch and the audit log.
func TestDelegateTileCallAuditAndOffSwitch(t *testing.T) {
	w := delegateWorld(t)
	bob := delegateCred("d-bob")
	rec := w.do(w.inv, "GET", "/api/apps/x/notes?q=secret", "", bob)
	if rec.Code != 200 || rec.Header().Get("X-Ran") != "tile" || len(w.tile) != 1 {
		t.Fatalf("the tile read: %d ran %q (%d calls)", rec.Code, rec.Header().Get("X-Ran"), len(w.tile))
	}
	if p := w.tile[0]; p.UserID != "bob" || p.Via != "delegate" || p.Agent != dHolder || p.Component != "" || p.IsAdmin() || p.User == nil {
		t.Fatalf("the proxy saw %+v", p)
	}
	if rec := w.do(w.inv, "POST", "/api/apps/x/send", `{"to":"x"}`, bob); rec.Code != 200 || len(w.tile) != 2 {
		t.Fatalf("the tile write: %d", rec.Code)
	}
	// no socket, no path ticket
	up := func(r *http.Request) { r.Header.Set("Upgrade", "websocket") }
	if rec := w.do(w.inv, "GET", "/api/apps/x/ws", "", bob, up); rec.Code != 403 || !strings.Contains(rec.Body.String(), "plain requests") {
		t.Errorf("an upgrade: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "GET", "/api/~ticket/x", "", bob); rec.Code != 403 {
		t.Errorf("a path ticket: %d", rec.Code)
	}

	// the dry run: what the call would be; nothing runs
	w.invS.Pol = dPolicy{deny: map[string]error{}}
	dry := func(r *http.Request) { r.Header.Set(HeaderDryRun, "1") }
	rec = w.do(w.inv, "POST", "/api/apps/x/send", `{"to":"x"}`, bob, dry)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || rec.Header().Get("X-Ran") != "" || len(w.tile) != 2 || out["dryRun"] != true || out["allowed"] != true ||
		out["sideEffect"] != true || out["reversible"] != false || out["tile"] != "apps/x" || out["role"] != "writer" || out["level"] != "write" || out["as"] != "bob" {
		t.Errorf("the dry run: %d ran %q %v", rec.Code, rec.Header().Get("X-Ran"), out)
	}
	w.invS.Pol = dPolicy{deny: map[string]error{"apps/x": errors.New("Finance has switched the workspace agent off for its tiles")}}
	rec = w.do(w.inv, "GET", "/api/apps/x/notes", "", bob, dry)
	out = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["allowed"] != false || !strings.Contains(out["refusal"].(string), "Finance") || out["sideEffect"] != false {
		t.Errorf("a dry run of a call the tile's org blocks: %v", out)
	}
	w.invS.Pol = nil
	if rec := w.do(w.inv, "POST", "/api/xbin/grants", `{}`, bob, dry); rec.Code != 403 { // a dry run is no way round the cap
		t.Errorf("a dry run of an undelegated route: %d", rec.Code)
	}
	if rec := w.do(w.inv, "POST", "/api/xbin/term/sessions", `{}`, bob, dry); rec.Code != 200 || rec.Header().Get("X-Ran") != "" {
		t.Errorf("a dry run of a session create: %d ran %q", rec.Code, rec.Header().Get("X-Ran"))
	}

	// the person pauses their agent: at once, for them alone
	w.mu.Lock()
	w.off["bob"] = errors.New("bob paused their agent")
	w.mu.Unlock()
	for _, c := range [][2]string{{"GET", "/api/apps/x/notes"}, {"GET", "/api/xbin/components"}, {"POST", "/api/xbin/term/sessions"}} {
		rec := w.do(w.inv, c[0], c[1], "{}", bob)
		if rec.Code != 403 || rec.Header().Get("X-Ran") != "" || !strings.Contains(rec.Body.String(), "bob paused their agent") {
			t.Errorf("paused, %s %s: %d ran %q %s", c[0], c[1], rec.Code, rec.Header().Get("X-Ran"), rec.Body.String())
		}
	}
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", delegateCred("d-alice")); rec.Code != 200 {
		t.Errorf("alice's agent while bob's is paused: %d", rec.Code)
	}
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", delegateCred("nope")); rec.Code != 401 {
		t.Errorf("an unknown token: %d", rec.Code)
	}
	w.mu.Lock()
	delete(w.off, "bob")
	w.mu.Unlock()

	// the audit log: every side effect and refusal, reads once a minute,
	// never a query or a body
	for i := 0; i < 3; i++ {
		w.do(w.inv, "GET", "/api/xbin/components", "", bob)
	}
	rows, err := w.log.Read("bob", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		method, path, class string
		status              int
	}
	seen := map[key]delegation.Call{}
	for _, c := range rows {
		if c.User != "bob" || c.Agent != dHolder || c.Time.IsZero() || strings.Contains(c.Path, "?") || strings.Contains(c.Path, "secret") {
			t.Errorf("row %+v", c)
		}
		seen[key{c.Method, c.Path, c.Class, c.Status}] = c
	}
	for _, k := range []struct {
		key
		effect, dry bool
		refused     string
		tile        string
	}{
		{key{"GET", "/api/apps/x/notes", "tile-read", 200}, false, false, "", "apps/x"},
		{key{"POST", "/api/apps/x/send", "tile-write", 200}, true, false, "", "apps/x"},
		{key{"GET", "/api/apps/x/ws", "unlisted", 403}, false, false, "plain requests", ""},
		{key{"POST", "/api/xbin/grants", "never", 403}, false, false, "admin API is never delegated", ""},
		{key{"POST", "/api/xbin/term/sessions", "act", 200}, true, true, "", ""},
		{key{"GET", "/api/xbin/components", "read", 200}, false, false, "", ""},
	} {
		c, ok := seen[k.key]
		if !ok {
			t.Errorf("no audit row for %+v (have %d rows)", k.key, len(rows))
			continue
		}
		if k.refused != "" && !strings.Contains(c.Refused, k.refused) || k.refused == "" && c.Refused != "" && !c.DryRun {
			t.Errorf("%+v: refused %q, want %q", k.key, c.Refused, k.refused)
		}
		if k.tile != "" && c.Tile != k.tile {
			t.Errorf("%+v: tile %q, want %q", k.key, c.Tile, k.tile)
		}
		if k.refused == "" && !k.dry && c.Effect != k.effect {
			t.Errorf("%+v: effect %v", k.key, c.Effect)
		}
	}
	reads := 0
	for _, c := range rows {
		if c.Method == "GET" && c.Path == "/api/xbin/components" && c.Status == 200 {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("3 repeated reads wrote %d rows, want 1 (coalesced)", reads)
	}
	// while paused nothing was recorded as done, and an unknown token wrote nothing anywhere
	if other, _ := w.log.Read("alice", time.Time{}, 0); len(other) != 1 {
		t.Errorf("alice's log has %d rows, want her one read", len(other))
	}

	// reading it: the person's own; an admin with ?user=; never the agent,
	// a tile, view-as or another user
	read := func(q string, c ...cred) (int, string) {
		rec := w.do(w.inv, "GET", "/api/xbin/delegate/audit"+q, "", c...)
		return rec.Code, rec.Body.String()
	}
	if c, b := read("", w.cookie("bob")); c != 200 || !strings.Contains(b, `"path":"/api/apps/x/send"`) || !strings.Contains(b, `"user":"bob"`) {
		t.Errorf("bob reads his log: %d %s", c, b)
	}
	if c, b := read("?limit=2", w.cookie("bob")); c != 200 || strings.Count(b, `"ts"`) != 2 {
		t.Errorf("limit: %d %s", c, b)
	}
	if c, b := read("?user=bob", w.cookie("alice")); c != 200 || !strings.Contains(b, `"path":"/api/apps/x/send"`) {
		t.Errorf("an admin reads bob's: %d %s", c, b)
	}
	if c, _ := read("?user=alice", w.cookie("bob")); c != 403 {
		t.Errorf("bob reads alice's: %d", c)
	}
	for name, c := range map[string]cred{"the agent": bob, "a tile's frame": w.frame("apps/x", "bob"), "view-as": w.viewAs("alice", "bob"), "a backend": bearer("i-main")} {
		if code, _ := read("", c); code != 403 {
			t.Errorf("%s reads the log: %d", name, code)
		}
	}
}
