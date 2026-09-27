package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// putClassesAs saves classes through PUT /classes (confirmMixed) and returns
// the error it answered with, if any.
func putClassesAs(t *testing.T, mux *http.ServeMux, want int, classes ...map[string]any) string {
	t.Helper()
	list := []any{}
	for _, c := range classes {
		list = append(list, c)
	}
	w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": list, "confirmMixed": true})
	if w.Code != want {
		t.Fatalf("PUT /classes %v: %d %s", classes, w.Code, w.Body)
	}
	var out struct{ Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.Error
}

// storeClasses puts classes in force the way an older process (or an edit
// made before the guards) left them: straight into the setting.
func storeClasses(t *testing.T, ag *Agent, list ...agentClass) {
	t.Helper()
	raw, _ := json.Marshal(classSettings{Classes: list})
	if err := ag.db.putSetting("classes", string(raw)); err != nil {
		t.Fatal(err)
	}
	loadClasses(ag.db)
	t.Cleanup(func() { classStore.Store(nil) })
}

func trigEvents(ag *Agent, id int64) []string {
	var out []string
	rows, _ := ag.db.q.Query(`SELECT accepted, reason FROM trigger_events WHERE trigger_id=? ORDER BY rowid`, id)
	defer rows.Close()
	for rows.Next() {
		var ok int
		var reason string
		_ = rows.Scan(&ok, &reason)
		out = append(out, fmt.Sprintf("%d:%s", ok, reason))
	}
	return out
}

// A class a public-data trigger runs in isn't made mixed (PUT /classes
// refuses, naming the trigger — a legacy webhook trigger's built-in too); a
// class made mixed anyway (before this guard) takes no public event: the
// trigger records class-mixed. The same for an event into a conversation
// whose class is mixed.
func TestPublicTriggersKeepTheirClassUnmixed(t *testing.T) {
	ag, mux := chanFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	fakeOf(ag).on(nil, say("ok"))
	ops := map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"internal", "files"}}
	putClassesAs(t, mux, 200, ops)
	tr := mkTrigger(t, mux, asAlice, map[string]any{"name": "hook", "source": "push", "sourceRef": "apps/webhooks", "match": "hook",
		"goal": "Handle {{topic}}", "class": "ops", "dataClass": "public"})
	opsWeb := map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"internal", "files", "web"}}
	if out := putClassesAs(t, mux, 400, opsWeb); !strings.Contains(out, `class ops takes data from outside (dataClass public) through trigger "hook"`) {
		t.Fatalf("made mixed under a public trigger: %s", out)
	}
	// a legacy webhook trigger (private lane, public data) runs in the built-in internal
	legacy := mkTrigger(t, mux, asAlice, map[string]any{"name": "legacy", "source": "push", "sourceRef": "apps/webhooks", "match": "legacy",
		"goal": "g", "toolset": "private", "dataClass": "public"})
	if legacy.Class != classInternal {
		t.Fatalf("a legacy trigger: %+v", legacy)
	}
	internalWeb := map[string]any{"id": "internal", "name": "Internal", "toolsets": []string{"internal", "files", "web"}}
	if out := putClassesAs(t, mux, 400, ops, internalWeb); !strings.Contains(out, `class internal takes data from outside`) || !strings.Contains(out, `trigger "legacy"`) {
		t.Fatalf("the built-in made mixed under a legacy trigger: %s", out)
	}
	putClassesAs(t, mux, 200, ops) // anything else still saves

	// made mixed anyway: the event is refused, and says why
	storeClasses(t, ag, agentClass{ID: "ops", Name: "Ops", Toolsets: []string{tsInternal, tsFiles, tsWeb}, MCP: classSet{All: true}})
	_, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "hook/1", "eventId": "h1", "dataClass": "public"})
	if len(res) != 1 || res[0].Accepted || res[0].Reason != "class-mixed" {
		t.Fatalf("a public event into a mixed class: %+v", res)
	}
	if ev := trigEvents(ag, tr.ID); len(ev) != 1 || ev[0] != "0:class-mixed" {
		t.Fatalf("recorded: %v", ev)
	}
	// into a conversation whose class is mixed
	storeClasses(t, ag, agentClass{ID: "bridge", Name: "Bridge", Toolsets: []string{tsInternal, tsWeb}, MCP: classSet{All: true}})
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "hi", "hold": true, "class": "bridge"})
	var conv struct{ ID int64 }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &conv) != nil {
		t.Fatalf("a bridge conversation: %d %s", w.Code, w.Body)
	}
	into := mkTrigger(t, mux, asAlice, map[string]any{"name": "into", "source": "push", "sourceRef": "apps/webhooks", "match": "into",
		"goal": "g", "class": "web", "dataClass": "public", "mode": "conversation", "targetRun": conv.ID})
	_, res = pushEvent(t, mux, "apps/webhooks", map[string]any{"trigger": "into", "topic": "into/1", "eventId": "i1", "dataClass": "public"})
	if len(res) != 1 || res[0].Accepted || res[0].Reason != "class-mixed" {
		t.Fatalf("a public event into a mixed conversation: %+v", res)
	}
	if ev := trigEvents(ag, into.ID); len(ev) != 1 || ev[0] != "0:class-mixed" {
		t.Fatalf("recorded: %v", ev)
	}
}

// A class a trigger or a channel's policy names can't be deleted (PUT
// /classes names who uses it); once nothing names it, it can.
func TestNamedClassesStay(t *testing.T) {
	ag, mux := chanFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	research := map[string]any{"id": "research", "name": "Research", "toolsets": []string{"web", "files"}}
	ops := map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"internal", "files"}}
	putClassesAs(t, mux, 200, research, ops)
	tr := mkTrigger(t, mux, asAlice, map[string]any{"name": "news", "source": "push", "sourceRef": "apps/webhooks", "match": "news",
		"goal": "g", "class": "research", "dataClass": "public"})
	if out := putClassesAs(t, mux, 400, ops); !strings.Contains(out, `class research is used by trigger "news"`) {
		t.Fatalf("deleting a trigger's class: %s", out)
	}
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"privateLane": true, "privateClass": "ops"})
	if out := putClassesAs(t, mux, 400, research); !strings.Contains(out, `class ops is used by channel "Slack · Acme" (privateClass)`) {
		t.Fatalf("deleting a channel's private class: %s", out)
	}
	// moved elsewhere: free to go
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/triggers/%d", tr.ID), map[string]any{"class": "web"}); w.Code != 200 {
		t.Fatalf("the trigger's class: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d", ch), map[string]any{"policy": map[string]any{"privateLane": true}}); w.Code != 200 {
		t.Fatalf("the channel's rules: %d %s", w.Code, w.Body)
	}
	putClassesAs(t, mux, 200)
	if _, ok := loadClasses(ag.db).find("research"); ok {
		t.Fatal("research is still there")
	}
}

// A trigger is always switchable: {enabled} alone is never refused over its
// class (deleted or made mixed since — by a manager or its owner), nor is an
// edit that leaves the class, the data class and the delivery be; the lane
// it was saved in stays its lane through edits. A channel's rules save the
// same way.
func TestTriggerSwitchSurvivesClassChanges(t *testing.T) {
	ag, mux := chanFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	fakeOf(ag).on(nil, say("ok"))
	research := map[string]any{"id": "research", "name": "Research", "toolsets": []string{"web", "files"}}
	ops := map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"internal", "files"}}
	putClassesAs(t, mux, 200, research, ops)
	tr := mkTrigger(t, mux, asAlice, map[string]any{"name": "news", "source": "push", "sourceRef": "apps/webhooks", "match": "news",
		"goal": "g", "class": "research", "dataClass": "public"})
	priv := mkTrigger(t, mux, asAlice, map[string]any{"name": "inside", "source": "push", "sourceRef": "apps/webhooks", "match": "inside",
		"goal": "g", "class": "ops", "dataClass": "private"})
	pub := mkTrigger(t, mux, asAlice, map[string]any{"name": "pub", "source": "push", "sourceRef": "apps/webhooks", "match": "pub",
		"goal": "g", "class": "ops", "dataClass": "public"})
	put := func(c caller, id int64, body map[string]any) (int, Trigger) {
		t.Helper()
		w := callAs(t, mux, c, "PUT", fmt.Sprintf("/triggers/%d", id), body)
		var out Trigger
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 && testing.Verbose() {
			t.Logf("PUT trigger %d %v: %d %s", id, body, w.Code, w.Body)
		}
		return w.Code, out
	}

	// research deleted (before the guard) — the trigger runs in the web built-in
	storeClasses(t, ag, agentClass{ID: "ops", Name: "Ops", Toolsets: []string{tsInternal, tsFiles}, MCP: classSet{All: true}})
	for _, c := range []caller{asMgr, asAlice} {
		if code, got := put(c, tr.ID, map[string]any{"enabled": false}); code != 200 || got.Enabled {
			t.Fatalf("%s switches it off: %d %+v", c.user, code, got)
		}
		if code, _ := put(c, tr.ID, map[string]any{"enabled": true}); code != 200 {
			t.Fatalf("%s switches it on: %d", c.user, code)
		}
	}
	// the owner's editor sends the whole trigger back
	stored, _ := ag.db.getTrigger(tr.ID)
	var whole map[string]any
	raw, _ := json.Marshal(stored)
	_ = json.Unmarshal(raw, &whole)
	whole["enabled"], whole["goal"] = false, "g, again"
	if code, got := put(asAlice, tr.ID, whole); code != 200 || got.Enabled || got.Class != "research" || got.Toolset != "web" {
		t.Fatalf("an edit that keeps the class: %d %+v", code, got)
	}
	// changing what the class rules are about checks them
	if code, _ := put(asAlice, tr.ID, map[string]any{"deliver": "chan:9:dm:X"}); code != 400 {
		t.Fatalf("a new delivery on a deleted class: %d", code)
	}

	// ops made mixed (before the guard): its public trigger still switches
	storeClasses(t, ag, agentClass{ID: "ops", Name: "Ops", Toolsets: []string{tsInternal, tsFiles, tsWeb}, MCP: classSet{All: true}})
	for _, c := range []caller{asMgr, asAlice} {
		if code, _ := put(c, pub.ID, map[string]any{"enabled": false}); code != 200 {
			t.Fatalf("%s switches off a public trigger on a mixed class: %d", c.user, code)
		}
	}
	if code, _ := put(asAlice, pub.ID, map[string]any{"enabled": true, "dataClass": "public", "goal": "g2"}); code != 200 {
		t.Fatalf("an edit that leaves the data class be: %d", code)
	}
	if code, _ := put(asAlice, pub.ID, map[string]any{"class": "research"}); code != 400 {
		t.Fatalf("a class that isn't: %d", code)
	}

	// ops made outward instead: the private trigger keeps its lane through
	// an edit, and its runs don't reach outside
	storeClasses(t, ag, agentClass{ID: "ops", Name: "Ops", Toolsets: []string{tsWeb, tsFiles}})
	if code, got := put(asAlice, priv.ID, map[string]any{"goal": "look inside"}); code != 200 || got.Toolset != "private" || got.Class != "ops" {
		t.Fatalf("an edit on a class gone outward: %d %+v", code, got)
	}
	_, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "inside/1", "eventId": "p1"})
	if len(res) != 1 || !res[0].Accepted {
		t.Fatalf("push: %+v", res)
	}
	if cfg, _ := ag.db.runConfig(res[0].RunID); specNames(cfg, 0, nil)["web_fetch"] || cfg.Toolset != "private" {
		t.Fatalf("a private trigger's run reaches outside: %+v", cfg)
	}

	// a channel's rules save while the class they name is gone
	storeClasses(t, ag, agentClass{ID: "ops", Name: "Ops", Toolsets: []string{tsInternal, tsFiles}, MCP: classSet{All: true}})
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"privateLane": true, "privateClass": "ops"})
	storeClasses(t, ag)
	if w := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d", ch), map[string]any{"policy": map[string]any{"privateLane": true,
		"privateClass": "ops", "system": "be brief"}}); w.Code != 200 {
		t.Fatalf("the channel's rules, ops gone: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d", ch), map[string]any{"policy": map[string]any{"privateLane": true,
		"privateClass": "nope"}}); w.Code != 400 {
		t.Fatalf("naming a class that isn't: %d %s", w.Code, w.Body)
	}
}
