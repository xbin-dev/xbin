package broker

import (
	"encoding/json"
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
var accessSecrets = []string{`"before"`, `"beforeText"`, `"after"`, `"afterText"`, `"effects"`, `"warnings"`, `"inverse"`, `"basis"`, "no access →", "read →", "(carol)", `"error"`}

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
			if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", bob); code != 200 || !strings.Contains(body, "“Carol Fin” (carol): read → write on apps/x") || !strings.Contains(body, `"basis"`) {
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
	if got := flags(cs); got != "[]" {
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
	if got := flags(cs); got != "A[]" {
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
