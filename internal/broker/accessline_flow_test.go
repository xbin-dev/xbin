package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// accessSecrets are what an agent may never read of an access.set line: the
// level before or after, the account it resolved, its effects and warnings,
// the hash of what the person saw, the undo.
var accessSecrets = []string{`"before"`, `"beforeText"`, `"after"`, `"afterText"`, `"effects"`, `"warnings"`, `"inverse"`, `"basis"`, "no access", "read by", "read through", "carol — ", `"error"`}

func accessLeaks(body string) (out []string) {
	for _, s := range accessSecrets {
		if strings.Contains(body, s) {
			out = append(out, s)
		}
	}
	return out
}

// covers D246 — the agent learns applied or failed of a person's access,
// never the access list: of an access.set line its credential and its page
// read the arguments, the flags and the result — in the proposal's answer,
// a plan's dry run and both GETs — also for an admin who opted in to admin
// reads. The person's own session reads the whole line.
func TestAccessLineRedactedForAgent(t *testing.T) {
	w := accessWorld(t)
	_, bob := w.session("bob")
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatal("opt in")
	}
	line := `[{"kind":"access.set","args":{"tile":"apps/x","user":"Carol Fin","level":"write"},"why":"she edits it"}]`
	for _, route := range []string{"/delegate/changesets", "/delegate/plans"} {
		for _, dry := range []string{"", `,"dryRun":true`} {
			if route == "/delegate/changesets" && dry != "" {
				continue
			}
			code, body := w.do("POST", route, `{"title":"carol edits x","items":`+line+dry+`}`, agentOf("bob"))
			if code != 200 {
				t.Fatalf("%s%s: %d %s", route, dry, code, body)
			}
			if l := accessLeaks(body); len(l) > 0 {
				t.Errorf("%s%s: the agent reads %v of a person's access\n%s", route, dry, l, body)
			}
			if !strings.Contains(body, "Set the access of “Carol Fin” on apps/x to write") {
				t.Errorf("%s%s: the agent's line isn't its arguments' own: %s", route, dry, body)
			}
			if dry != "" {
				continue
			}
			var cs delegation.ChangeSet
			_ = json.Unmarshal([]byte(body), &cs)
			for _, path := range []string{"/delegate/changesets", "/delegate/changesets/" + cs.ID} {
				if code, body := w.do("GET", path, "", agentOf("bob")); code != 200 || len(accessLeaks(body)) > 0 {
					t.Errorf("the agent reads %s: %d %v", path, code, accessLeaks(body))
				}
			}
			if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", bob); code != 200 || !strings.Contains(body, "carol — “Carol Fin”: read ") || !strings.Contains(body, `"basis"`) {
				t.Errorf("bob's own review is short of the line: %d %s", code, body)
			}
			done := w.approve(t, cs.ID, `{"items":[1],"password":"password1","knows":["access.set"]}`, bob)
			if results(done) != "applied" {
				t.Fatalf("%s applied: %s", route, results(done))
			}
			if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", agentOf("bob")); code != 200 || len(accessLeaks(body)) > 0 || !strings.Contains(body, `"result":"applied"`) {
				t.Errorf("after the approval the agent reads: %d %v %s", code, accessLeaks(body), body)
			}
			_ = w.b.Users.SetUserTile("carol", "apps/x", users.LevelRead)
		}
	}
}

// covers D246 — an older client never applies a kind it doesn't know: a
// chosen access.set line needs knows: ["access.set"] on the approve call,
// and nothing is applied without it.
func TestApproveRefusesAKindTheClientDoesNotKnow(t *testing.T) {
	w := accessWorld(t)
	_, alice := w.session("alice")
	cs := w.plan(t, "alice", `[{"kind":"access.set","args":{"tile":"apps/x","user":"carol","level":"write"}}]`)
	if got := flags(cs); got != "W[]" { // personal, and write is warned (H1)
		t.Fatalf("alice's own tile, someone else's access: a personal line, got %s", got)
	}
	code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1]}`, alice)
	if code != 409 || !strings.Contains(body, `"kinds":["access.set"]`) {
		t.Fatalf("an approve that doesn't know the kind: %d %s", code, body)
	}
	if u, _ := w.b.Users.Get("carol"); u.Tiles["apps/x"] != users.LevelRead || w.set(t, "alice", cs.ID).State != delegation.SetProposed {
		t.Fatal("something was applied")
	}
	// a line it doesn't choose needs nothing
	done := w.approve(t, cs.ID, `{"items":[1],"knows":["access.set"]}`, alice)
	if results(done) != "applied" {
		t.Fatalf("alice applies it: %s %+v", results(done), done.Items[0])
	}
	if u, _ := w.b.Users.Get("carol"); u.Tiles["apps/x"] != users.LevelWrite {
		t.Fatalf("carol's entry: %q", u.Tiles["apps/x"])
	}
}

// covers D246 — the line applies as PUT /access, as the approver with the
// rights they hold when they apply: alice's line about her tile, which bob
// took over before she applied it, is not applied — and what was
// warned starts nobody's tick: the approve call names it or it isn't
// applied.
func TestAccessLineAppliesAsTheApproverNow(t *testing.T) {
	w := accessWorld(t)
	_, alice := w.session("alice")
	cs := w.plan(t, "alice", `[{"kind":"access.set","args":{"tile":"apps/x","user":"carol","level":"terminal"}},{"kind":"access.set","args":{"tile":"apps/x","user":"erin","level":"read"}}]`)
	if got := flags(cs); got != "W[] []" {
		t.Fatalf("a raise to terminal is warned: %s", got)
	}
	done := w.approve(t, cs.ID, `{"items":[2],"knows":["access.set"]}`, alice)
	if results(done) != "rejected,applied" {
		t.Fatalf("only the named line: %s", results(done))
	}
	if u, _ := w.b.Users.Get("carol"); u.Tiles["apps/x"] != users.LevelRead {
		t.Fatalf("the warned line was applied without being named: %q", u.Tiles["apps/x"])
	}
	cs = w.plan(t, "alice", `[{"kind":"access.set","args":{"tile":"apps/x","user":"erin","level":"write"}}]`)
	if err := w.b.Users.SetOwner("apps/x", "user:bob"); err != nil {
		t.Fatal(err)
	}
	done = w.approve(t, cs.ID, `{"items":[1],"knows":["access.set"]}`, alice)
	if results(done) != "failed" {
		t.Fatalf("applied by a person who no longer manages the tile: %s %+v", results(done), done.Items[0])
	}
	if u, _ := w.b.Users.Get("erin"); u.Tiles["apps/x"] != users.LevelRead {
		t.Fatalf("erin's entry: %q", u.Tiles["apps/x"])
	}
}

// covers D246 — an admin's line is applied with the admin's rights as
// they are when they apply it: erin, fin's admin, proposes carol's access
// to fin's tile and is no admin of fin any more when she applies it — the
// by-hand route's own gate refuses it, nothing is written.
func TestAccessLineAppliesWithTheRightsHeldAtApply(t *testing.T) {
	w := accessWorld(t)
	_, erin := w.session("erin")
	cs := w.plan(t, "erin", `[{"kind":"access.set","args":{"tile":"apps/pg","user":"carol","level":"write"}}]`)
	if got := flags(cs); got != "AW[]" { // write: warned (it starts unticked)
		t.Fatalf("an org admin's line on the org's tile: %s", got)
	}
	org, _ := w.b.Users.Org("fin")
	o := *org
	o.Members = []users.Member{{ID: "carol", Level: users.LevelRead}, {ID: "erin", Level: users.LevelRead}} // erin is no admin of fin now
	if _, err := w.b.Users.UpsertOrg(o); err != nil {
		t.Fatal(err)
	}
	done := w.approve(t, cs.ID, `{"items":[1],"knows":["access.set"],"password":"password1"}`, erin)
	if results(done) != "failed" {
		t.Fatalf("applied with a right erin no longer holds: %s %+v", results(done), done.Items[0])
	}
	if u, _ := w.b.Users.Get("carol"); u.Tiles["apps/pg"] != "" {
		t.Fatalf("carol's entry on apps/pg: %q", u.Tiles["apps/pg"])
	}
}

// covers D246 — a person's own level is never a line of their agent's
// unless they are a workspace admin: by id or by their own display name it
// is blocked (never "needs an admin"), and a name only an admin's review
// resolves to the asker is blocked there.
func TestAccessSelfThroughTheAgent(t *testing.T) {
	w := accessWorld(t)
	_, alice := w.session("alice")
	cs := w.plan(t, "alice", `[{"kind":"access.set","args":{"tile":"apps/pu","user":"alice","level":"terminal"}},{"kind":"access.set","args":{"tile":"apps/pu","user":"Alice Smith","level":"write"}},{"kind":"access.set","args":{"tile":"apps/pu","user":"A. Smith","level":"write"}}]`)
	if got := flags(cs); got != "B[] B[] AN[]" {
		t.Fatalf("alice's own access: %s", got)
	}
	// her display name moves under the third: only an admin's review sees whose it is
	a, _ := w.b.Users.Get("alice")
	a.Name = "A. Smith"
	if _, err := w.b.Users.Upsert(*a, ""); err != nil {
		t.Fatal(err)
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 200 {
		t.Fatalf("ask: %d %s", code, body)
	}
	var asked delegation.ChangeSet
	sets, _ := w.store.ChangeSets("bob")
	for _, o := range sets {
		if o.From == "alice" {
			asked = o
		}
	}
	if len(asked.Items) != 1 || !strings.Contains(asked.Items[0].Blocked, "the person whose agent proposed it") {
		t.Fatalf("bob's copy of alice's own access: %+v", asked.Items)
	}
	// an admin's own: warned, an admin's line
	_, bob := w.session("bob")
	own := w.plan(t, "bob", `[{"kind":"access.set","args":{"tile":"apps/pu","user":"bob","level":"read"}}]`)
	if got := flags(own); got != "AW[]" {
		t.Fatalf("an admin's own access: %s", got)
	}
	_ = bob
}

// covers D246 — POST /delegate/accounts/lookup: a person who sees no
// accounts gets the same answer, byte for byte, for every name; an admin's
// agent gets exact matches only, never a list; a conversation's share runs
// out; a lookup is an act of the person's log, without the name.
func TestAccountLookupRoute(t *testing.T) {
	w := accessWorld(t)
	look := func(user, name string) (int, string) {
		return w.do("POST", "/delegate/accounts/lookup", fmt.Sprintf(`{"name":%q}`, name), agentOf(user))
	}
	var first string
	for i, n := range []string{"carol", "zed", "Erin Ops", "bob", "nobody-at-all"} {
		code, body := look("alice", n)
		got := strings.Replace(body, fmt.Sprintf("%q", n), `"<name>"`, 1)
		if code != 200 || !strings.Contains(body, `"unseen":true`) {
			t.Fatalf("alice looks up %q: %d %s", n, code, body)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Errorf("alice's answers differ by name:\n%s\n%s", first, got)
		}
	}
	for n, want := range map[string]string{"carol": `"id":"carol"`, "Erin Ops": `"id":"erin"`, "car": `"exists":false`, "CAROL": `"id":"carol"`} {
		if code, body := look("bob", n); code != 200 || !strings.Contains(body, want) {
			t.Errorf("bob looks up %q: %d %s", n, code, body)
		}
	}
	if _, err := w.b.Users.Upsert(users.User{ID: "eve", Name: "Erin Ops", Role: users.RoleUser}, "password1"); err != nil {
		t.Fatal(err)
	}
	if code, body := look("bob", "Erin Ops"); code != 200 || !strings.Contains(body, `"ambiguous":true`) || strings.Contains(body, "eve") || strings.Contains(body, "erin") {
		t.Errorf("two accounts by one name: %d %s", code, body)
	}
	if code, _ := w.do("POST", "/delegate/accounts/lookup", `{"name":"carol"}`, func(r *http.Request) { agentOf("bob")(r); r.Header.Del("X-XBin-Agent-Session") }); code != 403 {
		t.Errorf("a lookup outside a conversation: %d", code)
	}
	var limited bool
	for range 20 {
		if code, _ := look("bob", "carol"); code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("a conversation looks up accounts without limit")
	}
	_, bobS := w.session("bob")
	code, raw := w.do("GET", "/delegate/audit", "", bobS)
	var log struct{ Calls []delegation.Call }
	_ = json.Unmarshal([]byte(raw), &log)
	n := 0
	for _, r := range log.Calls {
		if r.Path == "/api/xbin/delegate/accounts/lookup" {
			n++
			if strings.Contains(fmt.Sprint(r), "carol") {
				t.Errorf("the log row names who was looked up: %+v", r)
			}
		}
	}
	if code != 200 {
		t.Fatalf("the log: %d %s", code, raw)
	}
	if n == 0 {
		t.Error("lookups aren't in the person's log")
	}
}

// covers D246 (review LOW a) — Ask an admin is no account oracle: a name
// the admin's review can't resolve (no such account, or several) comes
// back to the asker exactly as a line the admin left out does.
func TestAskAnAdminTellsTheAskerNoNames(t *testing.T) {
	w := accessWorld(t)
	for _, u := range []users.User{{ID: "sam1", Name: "Sam Two", Role: users.RoleUser}, {ID: "sam2", Name: "Sam Two", Role: users.RoleUser}} {
		if _, err := w.b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	_, alice := w.session("alice")
	cs := w.plan(t, "alice", `[{"kind":"access.set","args":{"tile":"apps/pu","user":"carol","level":"read"}},{"kind":"access.set","args":{"tile":"apps/pu","user":"erin","level":"read"}},
		{"kind":"access.set","args":{"tile":"apps/pu","user":"zed","level":"read"}},{"kind":"access.set","args":{"tile":"apps/pu","user":"Sam Two","level":"read"}}]`)
	if got := flags(cs); got != "AN[] AN[] AN[] AN[]" {
		t.Fatalf("alice's lines on the workspace's tile: %s", got)
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 200 {
		t.Fatalf("ask: %d %s", code, body)
	}
	var asked delegation.ChangeSet
	sets, _ := w.store.ChangeSets("bob")
	for _, o := range sets {
		if o.From == "alice" {
			asked = o
		}
	}
	if len(asked.Items) != 4 || asked.Items[2].Blocked == "" || asked.Items[3].Blocked == "" {
		t.Fatalf("bob's copy: %+v", asked.Items)
	}
	_, bob := w.session("bob")
	w.approve(t, asked.ID, `{"items":[1],"knows":["access.set"]}`, bob)
	mine := w.set(t, "alice", cs.ID)
	if mine.Items[0].Result != delegation.ItemApplied {
		t.Fatalf("the line bob applied: %+v", mine.Items[0])
	}
	left := mine.Items[1]
	for _, it := range mine.Items[2:] {
		if it.Result != left.Result || it.Error != left.Error || it.Blocked != left.Blocked {
			t.Errorf("a name bob's review couldn't resolve reads otherwise than a line he left out: %q %q / %q %q", it.Result, it.Error, left.Result, left.Error)
		}
	}
	// and as her agent reads it
	code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", agentOf("alice"))
	if code != 200 || strings.Contains(body, `"blocked"`) {
		t.Errorf("her agent reads: %d %s", code, body)
	}
}

// covers D246 (review LOW e) — an access line an agent proposes outside a
// plan (admin_propose: POST /delegate/changesets) is held to what its
// conversation may touch, as a plan's line is (holdAccessLines): a tile
// outside the conversation's scope, one at agent level "off", one its
// org's rules keep the agent from — each blocked; a tile it may touch is a
// line.
func TestAdminProposeHoldsAccessLines(t *testing.T) {
	w := accessWorld(t)
	_, bob := w.session("bob")
	if code, body := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatalf("opt in: %d %s", code, body)
	}
	w.write(map[string]string{"apps/off/xbin.json": `{"runtime":"go","agent":"off"}`, "apps/off/backend/main.go": "package main\n"})
	w.rescan()
	propose := func(cred func(*http.Request), tile string) delegation.ChangeItem {
		t.Helper()
		code, body := w.do("POST", "/delegate/changesets", `{"title":"t","items":[{"kind":"access.set","args":{"tile":"`+tile+`","user":"carol","level":"read"}}]}`, cred)
		var cs delegation.ChangeSet
		if json.Unmarshal([]byte(body), &cs); code != 200 {
			t.Fatalf("propose on %s: %d %s", tile, code, body)
		}
		return w.set(t, "bob", cs.ID).Items[0]
	}
	scoped := w.scoped(t, "bob", "apps/x")
	if it := propose(scoped, "apps/x"); it.Blocked != "" {
		t.Errorf("a tile in the conversation's scope: %q", it.Blocked)
	}
	if it := propose(scoped, "apps/pu"); !strings.Contains(it.Blocked, "outside what bob let this conversation see") {
		t.Errorf("a tile outside the conversation's scope: %q", it.Blocked)
	}
	if it := propose(agentOf("bob"), "apps/off"); !strings.Contains(it.Blocked, `agent level is "off"`) {
		t.Errorf("a tile at agent level off: %q", it.Blocked)
	}
	deny := delegation.OpenPolicy{Deny: map[string]error{"apps/pg": errors.New("fin keeps agents out")}}
	w.srv.AgentPolicy, w.b.AgentPolicy = deny, deny
	if it := propose(agentOf("bob"), "apps/pg"); !strings.Contains(it.Blocked, "fin keeps agents out") {
		t.Errorf("a tile its org's rules keep the agent from: %q", it.Blocked)
	}
}
