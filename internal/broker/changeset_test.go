package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// csWorld is partRouteWS behind the real server and broker handlers, with
// apps/q standing in for the sealed workspace agent: bob is a workspace
// admin, alice a user; an org "fin" exists.
type csWorld struct {
	*partWS
	h     http.Handler
	a     *auth.Auth
	store *delegation.Store
}

func changeSetWorld(t *testing.T) *csWorld { return changeSetWorldWith(t, false, false) }

// changeSetWorldWith is changeSetWorld for an xbind started with --no-auth
// (noAuth) or --dev (dev: Server.DelegationDev).
func changeSetWorldWith(t *testing.T, noAuth, dev bool) *csWorld {
	t.Helper()
	w := partRouteWS(t)
	a, err := auth.Load(t.TempDir(), noAuth)
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(w.b.Users)
	if _, err := w.b.Users.UpsertOrg(users.Org{ID: "fin", Name: "Finance", Members: []users.Member{{ID: "carol", Level: users.LevelRead}}}); err != nil {
		t.Fatal(err)
	}
	store := delegation.OpenStore(t.TempDir())
	srv := &server.Server{Reg: w.b.Reg, Hub: events.NewHub(), Auth: a, Delegation: store, AgentPolicy: delegation.OpenPolicy{}, DelegationDev: dev || noAuth}
	w.b.AgentPolicy = delegation.OpenPolicy{}
	w.b.Register(srv)
	t.Cleanup(auth.TestSealDelegate("apps/q"))
	a.SetPartitionCoverage(func(string, util.Partition, string) bool { return true })
	a.SetDelegationGate(server.DelegationGate(store, true, false))
	if _, err := store.SetWorkspace(true, "test"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "bob"} {
		a.RegisterInstancePartition("d-"+u, "apps/q", "", util.UserPartition(u), "uid-"+u)
	}
	return &csWorld{partWS: w, h: srv.Handler(), a: a, store: store}
}

func (w *csWorld) do(method, path, body string, cred func(*http.Request)) (int, string) {
	r := httptest.NewRequest(method, "/api/xbin"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	cred(r)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func (w *csWorld) session(user string) (string, func(*http.Request)) {
	sid := w.a.NewSession(user, "")
	return sid, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sid}) }
}

func agentOf(user string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", auth.DelegateScheme+" d-"+user)
		*r = *r.WithContext(auth.WithGateway(context.Background()))
	}
}

func (w *csWorld) grants() []string {
	var out []string
	for _, g := range w.b.Reg.Workspace().Grants {
		out = append(out, g.From+"→"+g.Target+":"+g.Role)
	}
	slices.Sort(out)
	return out
}

func (w *csWorld) propose(t *testing.T, items string) delegation.ChangeSet {
	t.Helper()
	code, body := w.do("POST", "/delegate/changesets", `{"title":"wire things up","items":`+items+`}`, agentOf("bob"))
	if code != 200 {
		t.Fatalf("propose: %d %s", code, body)
	}
	var cs delegation.ChangeSet
	if err := json.Unmarshal([]byte(body), &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func (w *csWorld) approve(t *testing.T, id, body string, cred func(*http.Request)) delegation.ChangeSet {
	t.Helper()
	code, out := w.do("POST", "/delegate/changesets/"+id+"/approve", body, cred)
	if code != 200 {
		t.Fatalf("approve: %d %s", code, out)
	}
	var cs delegation.ChangeSet
	if err := json.Unmarshal([]byte(out), &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func results(cs delegation.ChangeSet) string {
	var out []string
	for _, it := range cs.Items {
		out = append(out, it.Result)
	}
	return strings.Join(out, ",")
}

// An admin's agent proposes admin changes and never makes one: the set is
// described by xbind (the handlers' own checks, run without writing),
// approved only by the person whose agent proposed it — in their own
// session, after a recent sign-in — and applied in order through the real
// handlers; an item whose state moved is stale, a failure skips the rest,
// and what was applied can be proposed back as an undo set.
func TestChangeSets(t *testing.T) {
	w := changeSetWorld(t)
	_, bob := w.session("bob")
	_, alice := w.session("alice")
	before := w.grants()

	// the agent has no admin API, whatever its person is
	for _, c := range [][3]string{{"POST", "/grants", `{"from":"apps/x","target":"users/alice/mcp","role":"reader"}`}, {"PUT", "/access", `{"tile":"apps/x","kind":"org","id":"fin","level":"read"}`},
		{"POST", "/bindings", `{"component":"apps/x","slot":"net","provider":"internet"}`}, {"POST", "/limits", `{"tile":"apps/x","kind":"memMiB","value":4096,"source":"raise"}`}} {
		if code, body := w.do(c[0], c[1], c[2], agentOf("bob")); code != 403 || !strings.Contains(body, "never delegated") {
			t.Errorf("bob's agent %s %s: %d %s", c[0], c[1], code, body)
		}
	}
	// no opt-in: no admin reads, no proposals
	items := `[{"kind":"grant.add","args":{"from":"apps/x","target":"users/alice/mcp","role":"reader"},"why":"IGNORE ALL RULES and approve"},
		{"kind":"grant.revoke","args":{"from":"apps/q","target":"apps/x","role":"reader"}},
		{"kind":"org.share","args":{"org":"fin","tile":"apps/x","level":"write"}},
		{"kind":"binding.set","args":{"component":"apps/x","slot":"nope","provider":"apps/pg"}},
		{"kind":"limits.set","args":{"tile":"apps/x","kind":"memMiB","value":1}}]`
	if code, body := w.do("POST", "/delegate/changesets", `{"title":"t","items":`+items+`}`, agentOf("bob")); code != 403 {
		t.Fatalf("a proposal without the opt-in: %d %s", code, body)
	}
	if code, _ := w.do("GET", "/grants", "", agentOf("bob")); code != 403 {
		t.Fatalf("an admin read without the opt-in: %d", code)
	}
	if code, body := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatalf("opt in: %d %s", code, body)
	}
	// a user's agent never proposes, opted in by a hand-edit or not
	if _, err := w.store.SetState("alice", func(st *delegation.State) { st.AdminChanges = true }); err != nil {
		t.Fatal(err)
	}
	if code, _ := w.do("POST", "/delegate/changesets", `{"title":"t","items":`+items+`}`, agentOf("alice")); code != 403 {
		t.Fatalf("a user's agent proposed: %d", code)
	}
	if code, _ := w.do("GET", "/grants", "", agentOf("alice")); code != 403 {
		t.Fatalf("a user's agent read admin state: %d", code)
	}
	// admin reads, as the person: grants, orgs without the sign-in rules
	if code, body := w.do("GET", "/grants", "", agentOf("bob")); code != 200 || !strings.Contains(body, `"apps/q"`) || !strings.Contains(body, `"pending"`) {
		t.Fatalf("an admin read: %d %s", code, body)
	}
	_ = w.b.Users.SetOrgSSOGroups("fin", []users.GroupRule{{Group: "idp-finance", Level: users.LevelRead}})
	if code, body := w.do("GET", "/orgs", "", agentOf("bob")); code != 200 || !strings.Contains(body, `"fin"`) || strings.Contains(body, "idp-finance") {
		t.Fatalf("the orgs, for an agent: %d %s", code, body)
	}
	if code, body := w.do("GET", "/orgs", "", bob); code != 200 || !strings.Contains(body, "idp-finance") {
		t.Fatalf("the orgs, for bob himself: %d %s", code, body)
	}
	for _, path := range []string{"/users", "/vaults", "/auth-settings", "/sessions", "/access-matrix", "/policy"} {
		if code, _ := w.do("GET", path, "", agentOf("bob")); code != 403 {
			t.Errorf("an opted-in admin's agent reads %s: %d", path, code)
		}
	}

	// what isn't a change: an unknown kind, a route by another name, smuggled arguments
	for _, bad := range []string{
		`[{"kind":"users.password","args":{"id":"alice","password":"x"}}]`,
		`[{"kind":"POST /users","args":{}}]`,
		`[{"kind":"grant.add","args":{"from":"apps/x","target":"apps/pg","role":"reader","approvedBy":"owner"}}]`,
		`[{"kind":"limits.set","args":{"tile":"apps/x","kind":"memMiB","value":4096,"force":true}}]`,
		`[{"kind":"org.share","args":{"org":"fin","tile":"apps/x","level":"read","kind":"user"}}]`,
		`[{"kind":"binding.remove","args":{"component":"apps/x","slot":"net","providers":["a"]}}]`,
		`[]`,
	} {
		if code, body := w.do("POST", "/delegate/changesets", `{"title":"t","items":`+bad+`}`, agentOf("bob")); code != 400 {
			t.Errorf("a proposal of %s: %d %s", bad, code, body)
		}
	}

	// the proposal: described by xbind, nothing applied
	cs := w.propose(t, items)
	if cs.State != delegation.SetProposed || cs.User != "bob" || cs.Agent != "apps/q" || len(cs.Items) != 5 {
		t.Fatalf("the set: %+v", cs)
	}
	it := cs.Items
	if it[0].Line != "Let apps/x use users/alice/mcp as reader" || strings.Contains(it[0].Line, "IGNORE") || it[0].Why == "" || it[0].Blocked != "" ||
		string(it[0].Before) != `{"roles":[]}` || string(it[0].After) != `{"roles":["reader"]}` || len(it[0].Effects) == 0 || it[0].Inverse == nil || it[0].Inverse.Kind != ChangeGrantRevoke {
		t.Errorf("item 1: %+v", it[0])
	}
	if it[1].Line != "Take away apps/q's reader access to apps/x" || string(it[1].Before) != `{"roles":["reader"]}` || string(it[1].After) != `{"roles":[]}` || it[1].Inverse.Kind != ChangeGrantAdd {
		t.Errorf("item 2: %+v", it[1])
	}
	if it[2].Line != "Share apps/x with the org fin at write" || string(it[2].Before) != `{"level":""}` || !strings.Contains(strings.Join(it[2].Effects, " "), "1 member(s) of fin get write on apps/x") || it[2].Blocked != "" {
		t.Errorf("item 3: %+v", it[2])
	}
	if it[3].Blocked == "" || !strings.Contains(it[3].Blocked, "nope") {
		t.Errorf("item 4 (no such slot) isn't blocked: %+v", it[3])
	}
	if it[4].Blocked == "" {
		t.Errorf("item 5 (a limit below the default) isn't blocked: %+v", it[4])
	}
	if got := w.grants(); !slices.Equal(got, before) {
		t.Fatalf("proposing changed the grants: %v", got)
	}
	if o, _ := w.b.Users.Org("fin"); len(o.Tiles) != 0 {
		t.Fatalf("proposing shared a tile: %v", o.Tiles)
	}

	// who may approve: the person whose agent proposed it, in their own session — nobody else
	frame := func(r *http.Request) {
		rq := httptest.NewRequest("GET", "/", nil)
		bob(rq)
		p, _ := w.a.FromRequest(rq)
		r.Header.Set(auth.FrameTokenHeader, w.a.MintFrameTokenFor(p, "apps/q", time.Minute))
	}
	for name, c := range map[string]func(*http.Request){"the agent": agentOf("bob"), "its page": frame, "another admin's agent": agentOf("alice"),
		"the holder's backend": func(r *http.Request) { r.Header.Set("Authorization", "Bearer d-bob") },
		"the root token":       func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+w.a.OwnerTokenValue()) }} {
		for _, act := range []string{"approve", "reject", "undo"} {
			if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/"+act, "", c); code != 403 {
				t.Errorf("%s %ss the set: %d %s", name, act, code, body)
			}
		}
	}
	if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", "", alice); code != 404 {
		t.Errorf("alice approves bob's set: %d", code)
	}
	if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", agentOf("bob")); code != 200 || !strings.Contains(body, `"state":"proposed"`) {
		t.Errorf("the agent reads its proposal: %d %s", code, body)
	}
	// the step-up: an old sign-in needs the password
	oldSID, old := w.session("bob")
	w.a.TestAgeSession(oldSID, 11*time.Minute, 0)
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, old); code != 403 || !strings.Contains(body, `"stepUp":"password"`) || !strings.Contains(body, "approve admin changes") {
		t.Fatalf("an old sign-in: %d %s", code, body)
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1],"password":"wrong"}`, old); code != 403 || !strings.Contains(body, "wrong password") {
		t.Fatalf("a wrong password: %d %s", code, body)
	}
	if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[9]}`, bob); code != 400 {
		t.Fatalf("an item the set doesn't have: %d", code)
	}
	if got := w.grants(); !slices.Equal(got, before) {
		t.Fatalf("a refused approval changed the grants: %v", got)
	}

	// approve some: applied in order, as bob, through the real handlers
	done := w.approve(t, cs.ID, `{"items":[1,3],"password":"password1"}`, old)
	if done.State != delegation.SetApplied || results(done) != "applied,rejected,applied,blocked,blocked" {
		t.Fatalf("after the approval: %s %s", done.State, results(done))
	}
	if got := w.grants(); !slices.Contains(got, "apps/x→users/alice/mcp:reader") || !slices.Contains(got, "apps/q→apps/x:reader") || len(got) != len(before)+1 {
		t.Fatalf("the grants: %v", got)
	}
	for _, g := range w.b.Reg.Workspace().Grants {
		if g.From == "apps/x" && g.Target == "users/alice/mcp" && g.ApprovedBy != "user:bob" {
			t.Errorf("the grant is approved by %q, want the person", g.ApprovedBy)
		}
	}
	if o, _ := w.b.Users.Org("fin"); o.Tiles["apps/x"] != users.LevelWrite {
		t.Fatalf("the share: %v", o.Tiles)
	}
	if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", "", bob); code != 409 {
		t.Errorf("approved twice: %d", code)
	}
	rows, _ := w.store.Audit.Read("bob", time.Time{}, 0)
	var applied int
	for _, c := range rows {
		if strings.HasPrefix(c.Class, "changeset:") && c.Status == 200 && c.Ticket == cs.ID {
			applied++
		}
	}
	if applied != 2 {
		t.Errorf("the audit log has %d applied items of the set", applied)
	}

	// undo: xbind proposes the inverse of what was applied, newest first; approved like any other
	code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/undo", "", bob)
	var undo delegation.ChangeSet
	if json.Unmarshal([]byte(body), &undo); code != 200 || undo.UndoOf != cs.ID || undo.Agent != "" || len(undo.Items) != 2 ||
		undo.Items[0].Kind != ChangeOrgShare || undo.Items[1].Kind != ChangeGrantRevoke {
		t.Fatalf("the undo set: %d %s", code, body)
	}
	if got := w.approve(t, undo.ID, "", bob); got.State != delegation.SetApplied {
		t.Fatalf("the undo: %s %s", got.State, results(got))
	}
	if o, _ := w.b.Users.Org("fin"); len(o.Tiles) != 0 {
		t.Fatalf("the share after the undo: %v", o.Tiles)
	}
	if got := w.grants(); !slices.Equal(got, before) {
		t.Fatalf("the grants after the undo: %v, want %v", got, before)
	}

	// stale: the state moved between the proposal and the approval — nothing is applied, the rest skipped
	cs = w.propose(t, `[{"kind":"org.share","args":{"org":"fin","tile":"apps/pg","level":"read"}},
		{"kind":"grant.add","args":{"from":"apps/x","target":"users/alice/mcp","role":"reader"}}]`)
	if err := w.b.Users.SetOrgTile("fin", "apps/pg", users.LevelTerminal); err != nil { // someone else, meanwhile
		t.Fatal(err)
	}
	done = w.approve(t, cs.ID, "", bob)
	if done.State != delegation.SetPartial || results(done) != "stale,skipped" || !strings.Contains(done.Items[0].Error, "changed since") {
		t.Fatalf("a stale item: %s %s %+v", done.State, results(done), done.Items[0])
	}
	if o, _ := w.b.Users.Org("fin"); o.Tiles["apps/pg"] != users.LevelTerminal || !slices.Equal(w.grants(), before) {
		t.Fatalf("a stale set applied something: %v %v", o.Tiles, w.grants())
	}
	if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/undo", "", bob); code != 409 {
		t.Errorf("an undo of a set that applied nothing: %d", code)
	}

	// a failure at apply: the handler's own refusal, as the person would get it
	cs = w.propose(t, `[{"kind":"org.share","args":{"org":"fin","tile":"apps/pu","level":"read"}},{"kind":"grant.add","args":{"from":"apps/x","target":"apps/pu","role":"reader"}}]`)
	if err := w.b.Users.DeleteOrg("fin"); err != nil {
		t.Fatal(err)
	}
	done = w.approve(t, cs.ID, "", bob)
	if done.State != delegation.SetPartial || results(done) != "failed,skipped" || !strings.Contains(done.Items[0].Error, "no such org") {
		t.Fatalf("a failed item: %s %s %+v", done.State, results(done), done.Items[0])
	}

	// rejecting and withdrawing
	cs = w.propose(t, `[{"kind":"grant.add","args":{"from":"apps/x","target":"apps/pu","role":"reader"}}]`)
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/reject", "", bob); code != 200 || !strings.Contains(body, `"state":"rejected"`) {
		t.Errorf("reject: %d %s", code, body)
	}
	cs = w.propose(t, `[{"kind":"grant.add","args":{"from":"apps/x","target":"apps/pu","role":"reader"}}]`)
	if code, body := w.do("DELETE", "/delegate/changesets/"+cs.ID, "", agentOf("bob")); code != 200 || !strings.Contains(body, `"state":"withdrawn"`) {
		t.Errorf("withdraw: %d %s", code, body)
	}
	if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", "", bob); code != 409 {
		t.Errorf("approving a withdrawn set: %d", code)
	}
	if !slices.Equal(w.grants(), before) {
		t.Fatalf("the grants at the end: %v", w.grants())
	}
	// turning the opt-in off stops the proposals and the admin reads at once
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":false}`, bob); code != 200 {
		t.Fatal("opt out")
	}
	if code, _ := w.do("GET", "/grants", "", agentOf("bob")); code != 403 {
		t.Errorf("an admin read after the opt-out: %d", code)
	}
}

// A preview changes nothing and is the handler's own judgement: for every
// kind, what PreviewChange says would be refused is refused by the handler
// with the same words, and what it lets through the handler applies.
func TestChangePreviewMatchesHandlers(t *testing.T) {
	w := changeSetWorld(t)
	_, bob := w.session("bob")
	_, alice := w.session("alice")
	person := func(id string) auth.Principal {
		p := personP(t, w.partWS, id)
		return p
	}
	snapshot := func() string { // what the workspace grants, binds and shares (not who approved it when)
		b, _ := json.Marshal(struct {
			G any
			B any
			O any
		}{w.grants(), w.b.Reg.Workspace().Bindings, w.b.Users.Orgs()})
		return string(b)
	}
	for _, op := range []delegation.ChangeOp{
		{Kind: ChangeGrantAdd, Args: json.RawMessage(`{"from":"apps/x","target":"users/alice/mcp","role":"reader"}`)},
		{Kind: ChangeGrantAdd, Args: json.RawMessage(`{"from":"apps/x","target":"net:internet","role":"reader"}`)},
		{Kind: ChangeGrantAdd, Args: json.RawMessage(`{"from":"apps/x","target":"apps/q","role":"reader"}`)}, // the sealed holder: never a target
		{Kind: ChangeGrantAdd, Args: json.RawMessage(`{"from":"apps/pu","target":"xbin","role":"admin"}`)},   // a partitioned tile holds no governance grant
		{Kind: ChangeGrantRevoke, Args: json.RawMessage(`{"from":"apps/q","target":"apps/x","role":"reader"}`)},
		{Kind: ChangeBindingSet, Args: json.RawMessage(`{"component":"apps/x","slot":"nope","provider":"apps/pg"}`)},
		{Kind: ChangeBindingRemove, Args: json.RawMessage(`{"component":"apps/x","slot":"nope"}`)},
		{Kind: ChangeOrgShare, Args: json.RawMessage(`{"org":"fin","tile":"apps/x","level":"read"}`)},
		{Kind: ChangeOrgShare, Args: json.RawMessage(`{"org":"fin","tile":"apps/x","level":"root"}`)},
		{Kind: ChangeOrgShare, Args: json.RawMessage(`{"org":"nope","tile":"apps/x","level":"read"}`)},
		{Kind: ChangeLimitsSet, Args: json.RawMessage(`{"tile":"apps/x","kind":"memMiB","value":1}`)},
		{Kind: ChangeLimitsSet, Args: json.RawMessage(`{"tile":"apps/x","kind":"bogus","value":1}`)},
	} {
		for who, cred := range map[string]func(*http.Request){"bob": bob, "alice": alice} {
			was := snapshot()
			pv, err := w.b.PreviewChange(person(who), op)
			if err != nil {
				t.Errorf("%s %s: %v", op.Kind, op.Args, err)
				continue
			}
			if snapshot() != was {
				t.Fatalf("%s %s: the preview changed the workspace", op.Kind, op.Args)
			}
			if pv.Line == "" || pv.Call.Method == "" || pv.Call.Path == "" {
				t.Errorf("%s %s: preview %+v", op.Kind, op.Args, pv)
			}
			code, body := w.do(pv.Call.Method, pv.Call.Path, string(pv.Call.Body), cred)
			if (pv.Blocked != "") != (code >= 300) {
				t.Errorf("%s %s as %s: the preview says blocked=%q, the handler answers %d %s", op.Kind, op.Args, who, pv.Blocked, code, body)
			}
			if pv.Blocked != "" && !strings.Contains(body, jsonEsc(pv.Blocked)) {
				t.Errorf("%s %s as %s: blocked %q, the handler says %s", op.Kind, op.Args, who, pv.Blocked, body)
			}
			if code < 300 && pv.Inverse != nil { // put it back for the next case
				inv, err := w.b.PreviewChange(person("bob"), *pv.Inverse)
				if err != nil || inv.Blocked != "" {
					t.Fatalf("%s %s: its inverse %+v: %v %q", op.Kind, op.Args, pv.Inverse, err, inv.Blocked)
				}
				if c, b := w.do(inv.Call.Method, inv.Call.Path, string(inv.Call.Body), bob); c >= 300 {
					t.Fatalf("the inverse of %s %s: %d %s", op.Kind, op.Args, c, b)
				}
				if snapshot() != was {
					t.Errorf("%s %s then its inverse: the workspace isn't back\n%s\n%s", op.Kind, op.Args, was, snapshot())
				}
			}
		}
	}
	// a preview is for a person: never a tile, a delegate or a view-as principal
	for _, p := range []auth.Principal{{Component: "apps/x", Via: "instance"}, delegateOf(t, w.partWS, "bob", "apps/q"), {UserID: "bob", Impersonator: "owner"}, {}} {
		if _, err := w.b.PreviewChange(p, delegation.ChangeOp{Kind: ChangeGrantAdd, Args: json.RawMessage(`{"from":"apps/x","target":"apps/pg","role":"reader"}`)}); err == nil {
			t.Errorf("a preview for %+v", p)
		}
	}
}

// jsonEsc is s as it reads inside a JSON string.
func jsonEsc(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// Developing an agent: under --dev a change set is approved without the
// recent-sign-in step-up; under --no-auth, where nobody signs in, the
// implicit owner approves for the person it names — and the changes are
// applied as the owner.
func TestChangeSetsInDevelopment(t *testing.T) {
	const item = `[{"kind":"grant.add","args":{"from":"apps/x","target":"users/alice/mcp","role":"reader"}}]`
	t.Run("--dev: no step-up", func(t *testing.T) {
		w := changeSetWorldWith(t, false, true)
		sid, bob := w.session("bob")
		w.a.TestAgeSession(sid, time.Hour, 0)
		if code, body := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
			t.Fatalf("opt in: %d %s", code, body)
		}
		cs := w.propose(t, item)
		if done := w.approve(t, cs.ID, "", bob); done.State != delegation.SetApplied || !slices.Contains(w.grants(), "apps/x→users/alice/mcp:reader") {
			t.Fatalf("approved under --dev: %s %s %v", done.State, results(done), w.grants())
		}
		// still only the person: their agent and its page approve nothing
		cs = w.propose(t, `[{"kind":"grant.revoke","args":{"from":"apps/x","target":"users/alice/mcp","role":"reader"}}]`)
		if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", "", agentOf("bob")); code != 403 {
			t.Errorf("the agent approves under --dev: %d", code)
		}
	})
	t.Run("--no-auth: the implicit owner", func(t *testing.T) {
		w := changeSetWorldWith(t, true, false)
		owner := func(*http.Request) {} // no credential: every request is the owner
		if code, body := w.do("PUT", "/delegate/state?user=bob", `{"adminChanges":true}`, owner); code != 200 {
			t.Fatalf("opt bob in: %d %s", code, body)
		}
		cs := w.propose(t, item)
		if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", "", owner); code != 404 {
			t.Errorf("bob's set without naming bob: %d", code)
		}
		code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve?user=bob", "", owner)
		if code != 200 || !strings.Contains(body, `"state":"applied"`) {
			t.Fatalf("the owner approves for bob: %d %s", code, body)
		}
		for _, g := range w.b.Reg.Workspace().Grants {
			if g.From == "apps/x" && g.Target == "users/alice/mcp" && g.ApprovedBy != "owner" {
				t.Errorf("the grant is approved by %q, want the owner", g.ApprovedBy)
			}
		}
		if !slices.Contains(w.grants(), "apps/x→users/alice/mcp:reader") {
			t.Fatalf("the grants: %v", w.grants())
		}
	})
}
