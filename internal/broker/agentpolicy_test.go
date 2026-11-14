package broker

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// agentFx is limitsFixture's workspace — org sales owns apps/email (carol
// admins it, bob is a member, alice reads), apps/notes is dave's,
// apps/calendar the workspace's — plus org finance (dave admins it), which
// owns apps/ledger and has apps/calendar shared into it.
func agentFixture(t *testing.T) *limitsFx {
	t.Helper()
	fx := limitsFixture(t)
	if _, err := fx.st.UpsertOrg(users.Org{ID: "finance", Name: "Finance", Members: []users.Member{{ID: "dave", Level: users.LevelWrite, Admin: true}}}); err != nil {
		t.Fatal(err)
	}
	fx.write("apps/ledger/xbin.json", `{}`)
	fx.write("apps/ledger/index.html", `<p>ledger</p>`)
	fx.rescan()
	for _, err := range []error{
		fx.st.SetOwner("apps/ledger", "org:finance"),
		fx.st.SetOrgTile("finance", "apps/calendar", users.LevelRead),
		fx.st.SetDefaultTiles(map[string]string{"apps/calendar": users.LevelRead}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return fx
}

func (fx *limitsFx) agents(p auth.Principal, method, url, body string, pathVals map[string]string) (int, string) {
	fx.t.Helper()
	h := map[string]http.HandlerFunc{
		"GET /agents/policy": fx.b.apiAgentsPolicyGet, "PUT /agents/policy": fx.b.apiAgentsPolicyPut,
		"PUT /orgs": fx.b.apiOrgAgentsPut, "GET /agents/connection": fx.b.apiAgentsConnection,
	}
	path, _, _ := strings.Cut(url, "?")
	key := method + " " + path
	if strings.HasPrefix(path, "/orgs/") {
		key = "PUT /orgs"
	}
	// principals are resolved per request, as auth does: a stale Access
	// would hide a membership the test just made
	if p.UserID != "" && p.User != nil && p.Component == "" {
		fresh := principalFor(fx.t, fx.st, p.UserID)
		fresh.Impersonator, fresh.Via = p.Impersonator, p.Via
		p = fresh
	}
	w := call(fx.t, h[key], p, method, url, body, pathVals)
	return w.Code, w.Body.String()
}

// covers D229 — who may set what: workspace admins (and the xbin:users
// capability) the workspace's bounds and any org's rules; an admin of an
// org, signed in themselves, their own org's — within the bounds; nobody
// else, and never a tile driven by an org admin.
func TestAgentPolicyAuthzMatrix(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	ps["carolTerm"] = auth.Principal{Via: "terminal", UserID: "carol", User: ps["carol"].User, Access: ps["carol"].Access}
	sales, finance := map[string]string{"org": "sales"}, map[string]string{"org": "finance"}
	const rules = `{"harnesses":["claude"]}`
	for _, c := range []struct {
		who                        string
		bounds, ownOrg, otherOrg   int // PUT /agents/policy, PUT the org they admin (carol: sales, dave: finance), PUT the other org
		readPolicy, readConnection int
	}{
		{"root", 200, 200, 200, 200, 200},
		{"root2", 200, 200, 200, 200, 200},     // a workspace admin
		{"consoleB", 200, 200, 200, 200, 403},  // the admin console's own grant (xbin admin): user management, but no person's view
		{"carol", 403, 200, 403, 200, 200},     // admin of sales
		{"dave", 403, 200, 403, 200, 200},      // admin of finance
		{"bob", 403, 403, 403, 200, 200},       // a member, not an admin
		{"alice", 403, 403, 403, 200, 200},     // a reader
		{"frame", 403, 403, 403, 403, 403},     // apps/email's frame, driven by carol: never her org-adminship
		{"instance", 403, 403, 403, 403, 403},  // a tile's backend
		{"terminal", 403, 403, 403, 403, 403},  // a terminal on an org tile, carol's
		{"shellC", 403, 403, 403, 403, 403},    // the shell's frame token, driven by carol
		{"viewAs", 403, 403, 403, 200, 200},    // an admin viewing as carol: reads as her, sets nothing
		{"wsTerm", 200, 200, 200, 200, 200},    // a workspace admin's terminal token: the admin plane, as for every org knob (bx)
		{"carolTerm", 403, 403, 403, 200, 200}, // an org admin's terminal or agent session token: never her org's rules
	} {
		own, other := sales, finance
		if c.who == "dave" {
			own, other = finance, sales
		}
		p := ps[c.who]
		if code, body := fx.agents(p, "PUT", "/agents/policy", `{"credentials":"own-first"}`, nil); code != c.bounds {
			t.Errorf("%s PUT /agents/policy: %d %s, want %d", c.who, code, body, c.bounds)
		}
		if code, body := fx.agents(p, "PUT", "/orgs/"+own["org"]+"/agents", rules, own); code != c.ownOrg {
			t.Errorf("%s PUT own org: %d %s, want %d", c.who, code, body, c.ownOrg)
		}
		if code, body := fx.agents(p, "PUT", "/orgs/"+other["org"]+"/agents", rules, other); code != c.otherOrg {
			t.Errorf("%s PUT the other org: %d %s, want %d", c.who, code, body, c.otherOrg)
		}
		if code, body := fx.agents(p, "GET", "/agents/policy", "", nil); code != c.readPolicy {
			t.Errorf("%s GET /agents/policy: %d %s, want %d", c.who, code, body, c.readPolicy)
		}
		if code, body := fx.agents(p, "GET", "/agents/connection", "", nil); code != c.readConnection {
			t.Errorf("%s GET /agents/connection: %d %s, want %d", c.who, code, body, c.readConnection)
		}
	}
	if code, body := fx.agents(ps["root"], "PUT", "/orgs/nope/agents", rules, map[string]string{"org": "nope"}); code != 404 {
		t.Errorf("an unknown org: %d %s", code, body)
	}
	if code, body := fx.agents(ps["root"], "GET", "/agents/policy", "", nil); code != 200 || !strings.Contains(body, `"canSet":true`) ||
		!strings.Contains(body, `"knownHarnesses":["claude","codex","gemini","opencode"]`) || !strings.Contains(body, `"credentials":"own-first"`) {
		t.Errorf("an admin's view of the bounds: %s", body)
	}
	if _, body := fx.agents(ps["carol"], "GET", "/agents/policy", "", nil); !strings.Contains(body, `"canSet":false`) {
		t.Errorf("an org admin can't set the bounds: %s", body)
	}
}

// covers D229 — the rules on the wire: rule by rule, null back to the
// default, an org only narrows the workspace's bounds (for a workspace
// admin too), GET /orgs carries them, and with nothing set nothing shows.
func TestAgentPolicyRoutes(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	sales := map[string]string{"org": "sales"}
	w := call(t, fx.b.apiOrgsList, ps["root"], "GET", "/orgs", "", nil)
	if strings.Contains(w.Body.String(), `"agents"`) {
		t.Fatalf("GET /orgs names agents with nothing set: %s", w.Body.String())
	}
	code, body := fx.agents(ps["carol"], "PUT", "/orgs/sales/agents", `{"harnesses":["codex","claude"],"models":"private"}`, sales)
	if code != 200 || !strings.Contains(body, `"agents":{"workspaceAgent":"on","harnesses":["claude","codex"],"models":"private"}`) ||
		!strings.Contains(body, `"bounds":{"workspaceAgent":"on","harnesses":"any","models":"any"}`) {
		t.Fatalf("set: %d %s", code, body)
	}
	if code, body = fx.agents(ps["carol"], "PUT", "/orgs/sales/agents", `{"workspaceAgent":"off","models":null}`, sales); code != 200 ||
		!strings.Contains(body, `"agents":{"workspaceAgent":"off","harnesses":["claude","codex"],"models":"any"}`) {
		t.Errorf("a rule not named keeps its value, null resets one: %d %s", code, body)
	}
	for body, want := range map[string]string{
		`{"harnesses":["nope"]}`:     "no harness",
		`{"harnesses":"private"}`:    "rule for models",
		`{"models":"some"}`:          `or a list of ids, not`,
		`{"workspaceAgent":"maybe"}`: `workspaceAgent: `,
		`{"credentials":"own"}`:      "unknown key",
		`[1]`:                        "need {",
		`null`:                       "need {",
	} {
		if code, out := fx.agents(ps["root"], "PUT", "/orgs/sales/agents", body, sales); code != 400 || !strings.Contains(out, want) {
			t.Errorf("%s: %d %s, want 400 %q", body, code, out, want)
		}
	}
	if w = call(t, fx.b.apiOrgsList, ps["carol"], "GET", "/orgs", "", nil); !strings.Contains(w.Body.String(), `"agents":{"workspaceAgent":"off"`) {
		t.Errorf("GET /orgs lacks the org's rules: %s", w.Body.String())
	}

	// the bounds: workspace admins; an org can't reach past them, whoever asks
	if code, body = fx.agents(ps["root2"], "PUT", "/agents/policy", `{"harnesses":["claude","gemini"],"models":["local"],"credentials":"connector"}`, nil); code != 200 ||
		!strings.Contains(body, `"harnesses":["claude","gemini"]`) || !strings.Contains(body, `"credentials":"connector"`) {
		t.Fatalf("bounds: %d %s", code, body)
	}
	for _, who := range []string{"carol", "root"} {
		if code, body = fx.agents(ps[who], "PUT", "/orgs/sales/agents", `{"harnesses":["claude","opencode"]}`, sales); code != 400 ||
			!strings.Contains(body, "opencode is outside what the workspace allows (claude, gemini)") || !strings.Contains(body, "can't widen") {
			t.Errorf("%s, past the bounds: %d %s", who, code, body)
		}
	}
	// sales's stored list (claude, codex) now reaches past the bounds: it is
	// kept, and every call is asked against both
	if err := fx.b.AgentHarnessGate("apps/email", "codex"); err == nil || !strings.Contains(err.Error(), "This workspace allows these coding agents only: claude, gemini") {
		t.Errorf("codex on a sales tile, after the bounds tightened: %v", err)
	}
	if err := fx.b.AgentHarnessGate("apps/email", "gemini"); err == nil || !strings.Contains(err.Error(), "sales allows these coding agents only: claude, codex (not gemini)") {
		t.Errorf("gemini on a sales tile: %v", err)
	}
	if err := fx.b.AgentHarnessGate("apps/email", "claude"); err != nil {
		t.Errorf("claude on a sales tile: %v", err)
	}
	if use, forced := fx.b.AgentCredentials("own", "own"); use != "connector" || !forced {
		t.Errorf("the workspace forces the connector: %s %v", use, forced)
	}
	if code, body = fx.agents(ps["root"], "PUT", "/agents/policy", `{"credentials":"sometimes"}`, nil); code != 400 {
		t.Errorf("an unknown credential default: %d %s", code, body)
	}
	if code, body = fx.agents(ps["root"], "PUT", "/agents/policy", `{"harnesses":null,"models":null,"credentials":null}`, nil); code != 200 ||
		!strings.Contains(body, `"harnesses":"any","models":"any","credentials":"own-first"`) {
		t.Errorf("null puts the bounds back: %d %s", code, body)
	}
	if use, forced := fx.b.AgentCredentials("", "connector"); use != "connector" || forced {
		t.Errorf("a person's saved default: %s %v", use, forced)
	}
}

// covers D229 — filtering follows the tile's org: the org that owns it,
// every org it is shared into (the strictest wins), the workspace's bounds
// for a tile in no org; a model's privacy is the connector's to say.
func TestAgentGatesFollowTheTilesOrg(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	fx.b.AgentModels = func(id string) (agentpolicy.ModelRef, bool) {
		m := agentpolicy.ParseModelID(id)
		m.Private = m.Upstream == "local"
		return m, m.Upstream == "local" || m.Upstream == "hosted"
	}
	private, public := fx.b.ResolveModel("local/llama"), fx.b.ResolveModel("hosted/big")
	if !private.Private || public.Private || fx.b.ResolveModel("") != nil || fx.b.ResolveModel("mystery/x").Private {
		t.Fatalf("models: %+v %+v", private, public)
	}
	// nothing set: everything passes, with no model at all
	for _, tile := range []string{"apps/email", "apps/ledger", "apps/calendar", "apps/notes"} {
		if err := fx.b.WorkspaceAgentGate("dave", tile, nil); err != nil {
			t.Errorf("%s with no rules: %v", tile, err)
		}
		if err := fx.b.AgentHarnessGate(tile, "gemini"); err != nil {
			t.Errorf("%s, a harness with no rules: %v", tile, err)
		}
	}
	if code, body := fx.agents(ps["dave"], "PUT", "/orgs/finance/agents", `{"models":"private","harnesses":["claude"]}`, map[string]string{"org": "finance"}); code != 200 {
		t.Fatalf("finance: %d %s", code, body)
	}
	for _, c := range []struct {
		tile  string
		model *agentpolicy.ModelRef
		want  string
	}{
		{"apps/ledger", public, "Finance allows private models only"},
		{"apps/ledger", private, ""},
		{"apps/ledger", nil, "Finance allows private models only, and no model is chosen"},
		{"apps/calendar", public, "Finance allows private models only"}, // the workspace's tile, shared into finance
		{"apps/calendar", private, ""},
		{"apps/email", public, ""}, // sales's own
		{"apps/notes", public, ""}, // dave's own, in no org
	} {
		err := fx.b.WorkspaceAgentGate("dave", c.tile, c.model)
		if (c.want == "") != (err == nil) || (err != nil && err.Error() != c.want) {
			t.Errorf("%s with %+v: %v, want %q", c.tile, c.model, err, c.want)
		}
	}
	if err := fx.b.AgentHarnessGate("apps/calendar", "codex"); err == nil || !strings.Contains(err.Error(), "Finance allows these coding agents only: claude (not codex)") {
		t.Errorf("a harness on a tile shared into finance: %v", err)
	}
	// the delegation package's questions: a model by its id, as the agent states it
	for _, c := range []struct{ model, harness, want string }{
		{"hosted/big", "", "Finance allows private models only"},
		{"local/llama", "", ""},
		{"", "", "Finance allows private models only, and no model is chosen"},
		{"local/llama", "claude", ""},
		{"local/llama", "codex", "Finance allows these coding agents only: claude (not codex)"},
		{"hosted/big", "claude", "Finance allows private models only"},
		{"mystery/private-they-say", "claude", "Finance allows private models only"}, // a model the connector doesn't know is not private
	} {
		err := fx.b.AgentTileAllows("xbin/agent", "dave", "apps/ledger", c.model)
		if c.harness != "" {
			err = fx.b.AgentSessionAllows("xbin/agent", "dave", "apps/ledger", c.model, c.harness)
		}
		if (c.want == "") != (err == nil) || (err != nil && err.Error() != c.want) {
			t.Errorf("delegate on apps/ledger with %q, harness %q: %v, want %q", c.model, c.harness, err, c.want)
		}
	}
	// a session opened on a directory inside the tile is still under its org
	if err := fx.b.AgentHarnessGate("apps/ledger/src/deep", "codex"); err == nil || !strings.Contains(err.Error(), "Finance allows these coding agents only") {
		t.Errorf("a harness on a sub-path of a finance tile: %v", err)
	}
	if err := fx.b.WorkspaceAgentGate("dave", "/apps/ledger/", public); err == nil {
		t.Errorf("a tile path with slashes around it: %v", err)
	}
	if h := fx.b.AgentHarnesses("apps/ledger"); h.String() != "claude" {
		t.Errorf("the harnesses of a finance tile: %s", h)
	}
	if h := fx.b.AgentHarnesses("apps/email"); !h.IsAny() {
		t.Errorf("the harnesses of a sales tile: %s", h)
	}
	// sales turns the workspace agent off: its tiles close to every person's
	// agent, private model or not; people's own sessions are not its business
	if code, body := fx.agents(ps["carol"], "PUT", "/orgs/sales/agents", `{"workspaceAgent":"off"}`, map[string]string{"org": "sales"}); code != 200 {
		t.Fatalf("sales: %d %s", code, body)
	}
	if err := fx.b.WorkspaceAgentGate("carol", "apps/email", private); err == nil || err.Error() != "sales has the workspace agent turned off" {
		t.Errorf("agent off: %v", err)
	}
	if err := fx.b.AgentHarnessGate("apps/email", "codex"); err != nil {
		t.Errorf("agent off doesn't stop a person's session: %v", err)
	}
	// the workspace's bounds reach the tile in no org
	if code, body := fx.agents(ps["root"], "PUT", "/agents/policy", `{"models":["local"]}`, nil); code != 200 {
		t.Fatalf("bounds: %d %s", code, body)
	}
	if err := fx.b.WorkspaceAgentGate("dave", "apps/notes", public); err == nil || err.Error() != "This workspace allows these models only: local" {
		t.Errorf("a tile in no org follows the bounds: %v", err)
	}
	if err := fx.b.WorkspaceAgentGate("dave", "apps/notes", private); err != nil {
		t.Errorf("a tile in no org, an allowed model: %v", err)
	}
	// no users store (the single-user workspace): nothing constrains anything
	fx.b.Users = nil
	if err := fx.b.WorkspaceAgentGate("", "apps/ledger", nil); err != nil {
		t.Errorf("no users store: %v", err)
	}
	if err := fx.b.AgentHarnessGate("apps/ledger", "gemini"); err != nil {
		t.Errorf("no users store, a harness: %v", err)
	}
}

var privateOnly = agentpolicy.Private()

type connOut struct {
	agentpolicy.View
	WhatIf *agentpolicy.Change `json:"whatIf"`
}

func (fx *limitsFx) connection(p auth.Principal, query string) connOut {
	fx.t.Helper()
	code, body := fx.agents(p, "GET", "/agents/connection"+query, "", nil)
	if code != 200 {
		fx.t.Fatalf("GET /agents/connection%s: %d %s", query, code, body)
	}
	var out connOut
	// Allow's wire form is a word or a list; the view's rows decode with it
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		fx.t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

// covers D229 — the connection view over the API: a person sees their own
// tiles and orgs only, each with its state and the reason; the scope hides,
// a model change says what it would do, and the model's privacy is never
// the caller's to claim.
func TestAgentConnectionRoute(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	fx.b.AgentModels = func(id string) (agentpolicy.ModelRef, bool) {
		m := agentpolicy.ParseModelID(id)
		m.Private = m.Upstream == "local"
		return m, true
	}
	if _, err := fx.st.SetOrgAgents("finance", agentpolicy.Patch{Models: &privateOnly}, nil); err != nil {
		t.Fatal(err)
	}
	// dave: admin of finance (apps/ledger, apps/calendar shared in), owner of
	// apps/notes; nothing of sales
	v := fx.connection(ps["dave"], "?model=hosted/big")
	states := map[string]string{}
	for _, tl := range v.Tiles {
		states[tl.Tile] = string(tl.State) + "|" + tl.Reason + "|" + tl.Org
	}
	want := map[string]string{
		"apps/ledger":   "blocked|Finance allows private models only|finance",
		"apps/calendar": "blocked|Finance allows private models only|finance",
		"apps/notes":    "connected||workspace",
	}
	if len(states) != len(want) {
		t.Errorf("dave sees %v", states)
	}
	for tile, w := range want {
		if states[tile] != w {
			t.Errorf("dave, %s: %q, want %q", tile, states[tile], w)
		}
	}
	if len(v.Orgs) != 2 || v.Orgs[0].ID != "finance" || v.Orgs[0].State != agentpolicy.Blocked || v.Orgs[1].ID != agentpolicy.WorkspaceID {
		t.Errorf("dave's orgs (never sales): %+v", v.Orgs)
	}
	if v.Counts.Summary != "1 tile connected · 2 not connected" || v.Model == nil || v.Model.Private {
		t.Errorf("counts: %+v, model %+v", v.Counts, v.Model)
	}
	// the caller can't make a model private by saying so
	if v = fx.connection(ps["dave"], "?model=hosted/big&private=1"); v.Counts.Blocked != 2 {
		t.Errorf("a claimed private model: %+v", v.Counts)
	}
	// what a change to a private model would do
	v = fx.connection(ps["dave"], "?model=hosted/big&whatIf=local/llama")
	if v.WhatIf == nil || len(v.WhatIf.Connect) != 2 || len(v.WhatIf.Disconnect) != 0 || v.WhatIf.Counts.Summary != "3 tiles · 1 org connected" || !v.WhatIf.Model.Private {
		t.Errorf("whatIf: %+v", v.WhatIf)
	}
	// the scope: finance only; the rest is hidden, what dave can't see is no-access
	v = fx.connection(ps["dave"], "?model=local/llama&orgs=finance,sales&tiles=apps/email")
	states = map[string]string{}
	for _, tl := range v.Tiles {
		states[tl.Tile] = string(tl.State)
	}
	if states["apps/ledger"] != "connected" || states["apps/calendar"] != "connected" || states["apps/notes"] != "hidden" || states["apps/email"] != "no-access" {
		t.Errorf("scoped: %v", states)
	}
	for _, o := range v.Orgs {
		if o.ID == "sales" && (o.State != agentpolicy.NoAccess || o.Name != "sales" || o.Rules != nil) {
			t.Errorf("an org dave has nothing to do with: %+v", o)
		}
	}
	if v.Counts.Tiles != 2 || v.Counts.Hidden != 1 || v.Counts.NoAccess != 1 || v.Counts.Orgs != 1 {
		t.Errorf("scoped counts: %+v", v.Counts)
	}
	// a workspace admin sees every tile and org; the root token too
	v = fx.connection(ps["root"], "?model=local/llama")
	if len(v.Tiles) < 5 || v.Counts.NotConnected != 0 || len(v.Orgs) != 3 {
		t.Errorf("the owner: %d tiles, %+v, orgs %+v", len(v.Tiles), v.Counts, v.Orgs)
	}
	// alice reads sales's tile and the workspace's default tiles: she never learns finance's
	// name from a tile she can't see… but apps/calendar is shared into it, and she reads that
	v = fx.connection(ps["alice"], "")
	for _, tl := range v.Tiles {
		if tl.Tile == "apps/ledger" || tl.Tile == "apps/notes" {
			t.Errorf("alice sees %s", tl.Tile)
		}
	}
	if code, body := fx.agents(ps["dave"], "GET", "/agents/connection?tiles="+strings.Repeat("a,", 2001), "", nil); code != 200 || !strings.Contains(body, `"tiles":["a"]`) {
		t.Errorf("a repeated scope entry counts once: %d", code)
	}
}
