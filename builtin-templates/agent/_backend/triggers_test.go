package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mkTrigger(t *testing.T, mux *http.ServeMux, c caller, tr map[string]any) Trigger {
	t.Helper()
	w := callAs(t, mux, c, "POST", "/triggers", tr)
	var out Trigger
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("create trigger: %d %s", w.Code, w.Body)
	}
	return out
}

func pushEvent(t *testing.T, mux *http.ServeMux, from string, body map[string]any) (*httptest.ResponseRecorder, []trigVerdict) {
	t.Helper()
	w := adapterCall(t, mux, from, "POST", "/adapter/event", body)
	var out struct{ Results []trigVerdict }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out.Results
}

// A trigger that can reach outside takes public data only; bus data is
// private; a trigger on the agent's own events needs a prefix.
func TestTriggerValidation(t *testing.T) {
	_, mux := chanFixture(t)
	for name, tc := range map[string]map[string]any{
		"web lane, private data":  {"toolset": "web", "dataClass": "private"},
		"announcing private data": {"deliver": "chan:1:dm:U1", "dataClass": "private"},
		"bus data as public":      {"source": "bus", "sourceRef": "res:apps/cal/bus", "dataClass": "public"},
		"own events, no prefix":   {"source": "bus", "sourceRef": "res:apps/agent/events"},
		"no goal":                 {"goal": ""},
		"conversation, no target": {"mode": "conversation"},
	} {
		body := map[string]any{"name": "t-" + strings.ReplaceAll(name, " ", "-"), "source": "push", "sourceRef": "apps/webhooks", "goal": "do it"}
		for k, v := range tc {
			body[k] = v
		}
		if w := callAs(t, mux, asAlice, "POST", "/triggers", body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// A push from a bound tile runs the trigger it names or whose prefix the
// topic has: once per event id, never with the wrong data class, never while
// halted (503, so the sender retries), at most maxPerHour an hour. Outside
// data is labelled as such and the run can't schedule.
func TestPushTrigger(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(nil, say("looked into it"))
	tr := mkTrigger(t, mux, asAlice, map[string]any{"name": "deploys", "source": "push", "sourceRef": "apps/webhooks", "match": "deploy",
		"goal": "Check the {{topic}} deploy", "toolset": "web", "dataClass": "public", "maxPerHour": 2})
	ev := map[string]any{"topic": "deploy/site", "eventId": "e1", "data": map[string]string{"sha": "abc"}, "dataClass": "public"}
	w, res := pushEvent(t, mux, "apps/webhooks", ev)
	if w.Code != 200 || len(res) != 1 || !res[0].Accepted || res[0].RunID == 0 {
		t.Fatalf("push: %d %s", w.Code, w.Body)
	}
	run, _ := ag.db.getRun(res[0].RunID)
	cfg, _ := ag.db.runConfig(run.ID)
	if run.Origin != "trigger" || run.OriginID != tr.ID || run.Owner != "alice" || cfg.toolset() != "web" || !cfg.denied("schedule") {
		t.Fatalf("its run: %+v lane %s deny %v", run, cfg.toolset(), cfg.Deny)
	}
	waitFor(t, "the run", func() bool { return strings.Contains(transcript(ag.db, run.ID), "looked into it") })
	tx := fullText(ag.db, run.ID)
	if !strings.Contains(tx, "[trigger deploys] Check the deploy/site deploy") || !strings.Contains(tx, "comes from outside") || !strings.Contains(tx, `"sha":"abc"`) {
		t.Fatalf("prompt: %s", tx)
	}
	if _, res := pushEvent(t, mux, "apps/webhooks", ev); len(res) != 1 || !res[0].Dup {
		t.Fatalf("the same event again: %+v", res)
	}
	if _, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "deploy/x", "eventId": "e2"}); res[0].Reason != "data-class" {
		t.Fatalf("private data into a public trigger: %+v", res)
	}
	if w, _ := pushEvent(t, mux, "apps/other", map[string]any{"topic": "deploy/site", "eventId": "e3", "dataClass": "public"}); w.Code != 404 {
		t.Fatalf("another tile's push: %d", w.Code)
	}
	var um struct{ Items []map[string]any }
	_ = json.Unmarshal(callAs(t, mux, asMgr, "GET", "/triggers/unmatched", nil).Body.Bytes(), &um)
	if len(um.Items) != 1 || um.Items[0]["from"] != "apps/other" {
		t.Fatalf("unmatched: %+v", um.Items)
	}
	_ = ag.db.putSetting("halt", "1")
	if w, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "deploy/y", "eventId": "e4", "dataClass": "public"}); w.Code != 503 || res[0].Reason != "halted" {
		t.Fatalf("halted: %d %+v", w.Code, res)
	}
	_ = ag.db.putSetting("halt", "")
	pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "deploy/z", "eventId": "e5", "dataClass": "public"})
	if _, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "deploy/w", "eventId": "e6", "dataClass": "public"}); res[0].Reason != "rate" {
		t.Fatalf("the hourly cap: %+v", res)
	}
	var evs struct{ Events []map[string]any }
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", fmt.Sprintf("/triggers/%d/events", tr.ID), nil).Body.Bytes(), &evs)
	if len(evs.Events) != 5 {
		t.Fatalf("history: %+v", evs.Events)
	}
	if w := callAs(t, mux, asBob, "GET", fmt.Sprintf("/triggers/%d/events", tr.ID), nil); w.Code != 404 {
		t.Fatalf("bob sees alice's trigger: %d", w.Code)
	}
	var list struct{ Triggers []map[string]any }
	_ = json.Unmarshal(adapterCall(t, mux, "apps/webhooks", "GET", "/adapter/triggers", nil).Body.Bytes(), &list)
	if len(list.Triggers) != 1 || list.Triggers[0]["name"] != "deploys" {
		t.Fatalf("the pushing tile's view: %+v", list)
	}
}

// Bus deliveries (xbin/bus only) run a persistent trigger's one thread.
func TestBusTrigger(t *testing.T) {
	ag, mux := chanFixture(t)
	fakeOf(ag).on(nil, say("noted"))
	tr := mkTrigger(t, mux, asAlice, map[string]any{"name": "cal", "source": "bus", "sourceRef": "res:apps/cal/bus", "match": "events/",
		"goal": "Note the calendar change", "mode": "persistent"})
	deliver := func(from, id string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"id": id, "subscription": "trig", "resource": "res:apps/cal/bus", "topic": "events/created", "data": map[string]int{"n": 1}, "ts": 1})
		r := httptest.NewRequest("POST", fmt.Sprintf("/trigger/bus/%d", tr.ID), bytes.NewReader(b))
		r.Header.Set("X-XBin-From", from)
		r.Header.Set("X-XBin-Role", "writer")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := deliver("apps/evil", "b1"); w.Code != 403 {
		t.Fatalf("a non-bus caller: %d", w.Code)
	}
	var v1, v2 trigVerdict
	_ = json.Unmarshal(deliver("xbin/bus", "b1").Body.Bytes(), &v1)
	waitFor(t, "the first", func() bool { return strings.Contains(transcript(ag.db, v1.RunID), "noted") })
	_ = json.Unmarshal(deliver("xbin/bus", "b2").Body.Bytes(), &v2)
	if !v1.Accepted || v1.RunID == 0 || v2.RunID != v1.RunID {
		t.Fatalf("one thread: %+v %+v", v1, v2)
	}
	if tx := fullText(ag.db, v1.RunID); !strings.Contains(tx, "not instructions)") {
		t.Fatalf("bus data is private and labelled: %s", tx)
	}
}

// A trigger may announce its answers into its owner's channel session.
func TestTriggerAnnounce(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(lastUser("hello"), say("hi"))
	f.on(lastUser("[trigger"), say("the site is up"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "open"}})
	v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello"))
	waitStatus(t, ag.db, v.RunID, statusIdle)
	if w := callAs(t, mux, asAlice, "POST", "/triggers", map[string]any{"name": "x", "source": "push", "sourceRef": "apps/webhooks",
		"goal": "check", "dataClass": "public", "deliver": v.SessionKey}); w.Code != 400 {
		t.Fatalf("alice announced into mgr's channel: %d", w.Code)
	}
	tr := mkTrigger(t, mux, asMgr, map[string]any{"name": "uptime", "source": "push", "sourceRef": "apps/webhooks",
		"goal": "Say whether the site is up", "dataClass": "public", "deliver": v.SessionKey})
	w := callAs(t, mux, asMgr, "POST", fmt.Sprintf("/triggers/%d/test", tr.ID), map[string]any{"text": "ping"})
	var tv trigVerdict
	_ = json.Unmarshal(w.Body.Bytes(), &tv)
	if !tv.Accepted {
		t.Fatalf("test fire: %s", w.Body)
	}
	waitFor(t, "the announcement", func() bool { return len(outOfKind(ag, ch, "announce")) == 1 })
	a := outOfKind(ag, ch, "announce")[0]
	if a.Body.Text != "the site is up" || a.SessionKey != v.SessionKey || !strings.Contains(string(a.Address), `"D1"`) {
		t.Fatalf("announce: %+v %s", a, a.Address)
	}
	var list struct{ Items []AutomationItem }
	_ = json.Unmarshal(callAs(t, mux, asMgr, "GET", "/automations", nil).Body.Bytes(), &list)
	found := false
	for _, it := range list.Items {
		if it.Kind == "trigger" && it.ID == tr.ID && it.Access == "owner" && it.Runs == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the trigger among the automations: %+v", list.Items)
	}
	// only its owner changes it; a manager may only switch someone else's
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/triggers/%d", tr.ID), map[string]any{"enabled": false}); w.Code != 404 {
		t.Fatalf("alice switched mgr's private trigger: %d", w.Code)
	}
}
