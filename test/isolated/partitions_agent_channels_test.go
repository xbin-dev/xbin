//go:build linux && integration

package isolated

// partitions_agent_channels_test.go — chat channels and event triggers of a
// partitioned agent (work pack B2c of the partitioned-tiles plan; the agent
// template's API.md "Partitioned instances"), end to end on a real
// `xbind --isolate` with owner auth (psSetup's people: alice and bob read
// apps/*). The messaging bridge (a copy of the agent-messaging-bridge
// template, its built-in console playing the chat platform) and the webhooks
// tile aren't partitioned: they reach the agent's global instance.
//
//   - A DM from the chat account alice linked (on the bridge's page, signed
//     in) goes global → her partition (partition mail; her partition has run
//     before, so the doorbell starts it) → her partition answers → an
//     outbox/add mail → global posts it to the bridge's outbox — and the
//     bridge posts it into the chat. The conversation is in alice's
//     partition, not in the global instance's list.
//   - An outbox/add naming alice's handoff mailed by bob's frame is refused
//     (nothing posted); the same from alice's own frame is posted.
//   - alice's private webhook trigger: registered at global with a prefix
//     (an empty one, and bob's overlapping one, refused); a delivery through
//     the ingress runs it in her partition and announces into her DM; the
//     same delivery again runs nothing more; her egress ledger counts it.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsAgentChannels$' ./test/isolated/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	pcBridge = "apps/bridge"
	pcHooks  = "apps/webhooks"
	pcHost   = "hooks.test"
)

// pcHandoffProbe is a builder's topic compiled into the instance, so the
// test can learn alice's handoff ids the way only she could: at the global
// instance, a person's e2e/handoffs is answered by mailing them the ids of
// their DM handoffs (an e2e/ids item, which her partition leaves in her
// inbox — no handler knows it — where her frame reads it).
const pcHandoffProbe = `package main

import (
	"context"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	mailHandlers["e2e/handoffs"] = func(_ context.Context, t *DB, it mailItem) error {
		if !globalMode() || !strings.HasPrefix(it.From, "user:") {
			return nil
		}
		rows, err := t.q.Query("SELECT id FROM handoffs WHERE person=? AND kind='dm' ORDER BY created", strings.TrimPrefix(it.From, "user:"))
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		t.AfterCommit(func() { _, _ = xbin.Mail(it.From, "e2e/ids", ids) })
		return nil
	}
}
`

type pcLine struct {
	Dir, Kind, Text string
	Conv            struct{ ID, Type string }
}

func TestPartitionsAgentChannels(t *testing.T) {
	t.Parallel()
	e := psSetup(t)
	d := e.d
	fake := startFakeOpenAI(t, d)
	ok := func(method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body, hdrs...)
		if r.Status/100 != 2 {
			t.Fatalf("%s %s: %d %s", method, path, r.Status, r)
		}
		return r
	}
	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "llm-gw", "path": paGW})
	d.WaitComponent(t, paGW)
	d.Bind(t, paGW, "net", "host")
	ok("PUT", "/api/xbin/vault/"+paGW+"/api-token-fake", map[string]string{"value": "sk-fake"})

	var inst struct{ Partition []string }
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": paAgent}).Decode(t, &inst)
	if strings.Join(inst.Partition, ",") != "user,global" {
		t.Fatalf("the new agent: partition %v", inst.Partition)
	}
	if err := d.WriteFiles(paAgent, map[string]string{"_backend/zz_e2e_handoffs.go": pcHandoffProbe}); err != nil {
		t.Fatal(err)
	}
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent-messaging-bridge", "path": pcBridge})
	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "webhooks", "path": pcHooks})
	d.WaitComponent(t, paAgent)
	ok("POST", "/api/xbin/bindings", map[string]any{"component": paAgent, "slot": "llm", "providers": []string{paGW}})
	d.Bind(t, paAgent, "net", "none")
	d.WaitComponent(t, pcBridge)
	d.WaitComponent(t, pcHooks)
	ok("POST", "/api/xbin/bindings", map[string]any{"component": pcBridge, "slot": "agent", "provider": paAgent})
	ok("POST", "/api/xbin/bindings", map[string]any{"component": pcHooks, "slot": "agents", "providers": []string{paAgent}})
	ok("POST", "/api/xbin/bindings", map[string]any{"component": pcHooks, "slot": "hooks", "provider": "runtime", "host": pcHost})

	xbindtest.Eventually(t, 5*time.Minute, "llm-gw's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+paGW+"/config", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), "backends"), fmt.Sprint(r.Status, " ", r)
	})
	ok("PUT", "/api/"+paGW+"/config/backend", map[string]string{"name": "fake", "baseURL": "http://" + fake})
	var cfg map[string]any
	xbindtest.Eventually(t, 8*time.Minute, "the agent's global instance answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+paAgent+"/config", nil)
		cfg = nil
		if r.Status == 200 {
			_ = json.Unmarshal(r.Body, &cfg)
		}
		return cfg["system"] != nil, fmt.Sprint(r.Status, " ", r)
	})
	cfg["model"] = "fake/fake-chat"
	ok("PUT", "/api/"+paAgent+"/config", cfg)

	ag := func(person, method, path string, body any) xbindtest.Resp {
		t.Helper()
		var hdrs []xbindtest.Header
		if person != "" {
			hdrs = append(hdrs, e.fr(t, paAgent, person))
		}
		return d.Call(t, method, "/api/"+paAgent+path, body, hdrs...)
	}
	// alice's partition has run before (mail never starts a person's first
	// instance); bob's too
	for _, p := range []string{"alice", "bob"} {
		xbindtest.Eventually(t, 3*time.Minute, p+"'s partition answers", func() (bool, string) {
			r := ag(p, "GET", "/me", nil)
			return r.Status == 200 && strings.Contains(string(r.Body), `"partition":"user:`+p+`"`), fmt.Sprint(r.Status, " ", r)
		})
	}

	// the bridge connects by itself; its account is claimed at the global
	// instance, DMs for linked people only
	xbindtest.Eventually(t, 5*time.Minute, "the bridge connected", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+pcBridge+"/status", nil)
		return strings.Contains(string(r.Body), `"phase":"connected"`), fmt.Sprint(r.Status, " ", cut(string(r.Body), 300))
	})
	var chID int64
	xbindtest.Eventually(t, 2*time.Minute, "the channel at the global instance", func() (bool, string) {
		var list struct {
			Items []struct {
				Kind, Access string
				ID           int64
			}
		}
		r := ag("", "GET", "/automations", nil)
		_ = json.Unmarshal(r.Body, &list)
		for _, it := range list.Items {
			if it.Kind == "channel" && it.Access == "claim" {
				chID = it.ID
			}
		}
		return chID != 0, cut(string(r.Body), 300)
	})
	ok("POST", fmt.Sprintf("/api/%s/channels/%d/claim", paAgent, chID), map[string]any{"visibility": "team", "policy": map[string]any{"dm": map[string]any{"policy": "linked"}}})
	// alice's own partition lists the global instance's channels
	var mine struct {
		Items []struct {
			Kind string
			ID   int64
		}
	}
	ag("alice", "GET", "/automations", nil).Decode(t, &mine)
	if len(mine.Items) != 1 || mine.Items[0].Kind != "channel" || mine.Items[0].ID != chID {
		t.Errorf("alice's Automations list the channel: %+v", mine.Items)
	}

	transcript := func() []pcLine {
		var out struct{ Lines []pcLine }
		_ = json.Unmarshal(d.Call(t, "GET", "/api/"+pcBridge+"/console/transcript", nil).Body, &out)
		return out.Lines
	}
	outs := func(pred func(pcLine) bool) []pcLine {
		var hits []pcLine
		for _, l := range transcript() {
			if l.Dir == "out" && pred(l) {
				hits = append(hits, l)
			}
		}
		return hits
	}
	say := func(text string) {
		t.Helper()
		ok("POST", "/api/"+pcBridge+"/console/send", map[string]any{"as": map[string]string{"id": "u1", "name": "Uma"},
			"conversation": map[string]string{"id": "D1", "type": "dm"}, "text": text})
	}

	t.Run("linked-dm", func(t *testing.T) {
		say("/link")
		var code string
		xbindtest.Eventually(t, 2*time.Minute, "the link code", func() (bool, string) {
			for _, l := range outs(func(l pcLine) bool { return strings.Contains(l.Text, "Code") || strings.Contains(l.Text, "code") }) {
				if m := regexp.MustCompile(`[A-Z2-9]{8}`).FindString(l.Text); m != "" {
					code = m
				}
			}
			return code != "", fmt.Sprint(transcript())
		})
		// alice pastes it on the bridge's page, signed in: the bridge's frame
		// calls the agent (its global instance) as her
		ok("POST", "/api/"+paAgent+"/adapter/link", map[string]string{"code": code}, e.fr(t, pcBridge, "alice"))
		xbindtest.Eventually(t, time.Minute, "told they are linked", func() (bool, string) {
			return len(outs(func(l pcLine) bool { return strings.Contains(l.Text, "@alice") })) > 0, fmt.Sprint(transcript())
		})

		say("hello from uma")
		xbindtest.Eventually(t, 4*time.Minute, "the answer from alice's partition", func() (bool, string) {
			return len(outs(func(l pcLine) bool { return strings.Contains(l.Text, "Hello from the fake model") })) > 0, fmt.Sprint(transcript())
		})
		// the conversation is hers, in her partition — not global's
		var list struct {
			Items []struct {
				ID            int64
				Origin, Title string
			}
		}
		ag("alice", "GET", "/conversations?scope=mine", nil).Decode(t, &list)
		var dm int64
		for _, it := range list.Items {
			if it.Origin == "channel" {
				dm = it.ID
			}
		}
		if dm < pa2to40 {
			t.Errorf("alice's DM conversation: %+v (want one of hers, id ≥ 2^40)", list.Items)
		}
		var glob struct{ Items []struct{ Origin string } }
		ag("", "GET", "/conversations?scope=mine", nil).Decode(t, &glob)
		for _, it := range glob.Items {
			if it.Origin == "channel" {
				t.Errorf("BUG: the global instance lists a channel conversation: %+v", glob.Items)
			}
		}
		if r := ag("bob", "GET", fmt.Sprintf("/runs/%d", dm), nil); strings.Contains(string(r.Body), "hello from uma") {
			t.Errorf("BUG: bob reads alice's DM: %d", r.Status)
		}
	})

	t.Run("forged-outbox", func(t *testing.T) {
		const mailAPI = "/api/xbin/partitions/mail"
		d.Must(t, "POST", mailAPI, map[string]any{"to": "global", "topic": "e2e/handoffs"}, 200, e.fr(t, paAgent, "alice"))
		var ids []string
		xbindtest.Eventually(t, 2*time.Minute, "alice's handoff ids", func() (bool, string) {
			var pg struct {
				Items []struct {
					Topic string
					Data  json.RawMessage
				}
			}
			d.Must(t, "GET", mailAPI, nil, 200, e.fr(t, paAgent, "alice")).Decode(t, &pg)
			for _, it := range pg.Items {
				if it.Topic == "e2e/ids" {
					_ = json.Unmarshal(it.Data, &ids)
				}
			}
			return len(ids) > 0, fmt.Sprint(pg.Items)
		})
		h := ids[len(ids)-1]
		// bob's partition names alice's handoff: refused, nothing posted
		d.Must(t, "POST", mailAPI, map[string]any{"to": "global", "topic": "outbox/add",
			"data": map[string]string{"handoff": h, "key": "forged-1", "kind": "answer", "text": "forged by bob"}}, 200, e.fr(t, paAgent, "bob"))
		// …and alice's own frame may (it is her chat)
		d.Must(t, "POST", mailAPI, map[string]any{"to": "global", "topic": "outbox/add",
			"data": map[string]string{"handoff": h, "key": "alice-1", "kind": "notice", "text": "posted by alice's frame"}}, 200, e.fr(t, paAgent, "alice"))
		xbindtest.Eventually(t, time.Minute, "alice's post", func() (bool, string) {
			return len(outs(func(l pcLine) bool { return l.Text == "posted by alice's frame" })) == 1, fmt.Sprint(transcript())
		})
		owner := e.fr(t, paAgent, "")
		xbindtest.Eventually(t, time.Minute, "global's inbox acked", func() (bool, string) {
			var pg struct{ Items []any }
			d.Must(t, "GET", mailAPI, nil, 200, owner).Decode(t, &pg)
			return len(pg.Items) == 0, fmt.Sprint(pg.Items)
		})
		if f := outs(func(l pcLine) bool { return strings.Contains(l.Text, "forged by bob") }); len(f) != 0 {
			t.Errorf("BUG: bob's forged reply was posted: %+v", f)
		}
	})

	t.Run("private-webhook-trigger", func(t *testing.T) {
		dmKey := fmt.Sprintf("chan:%d:dm:u1", chID)
		trig := func(person, name, match string) xbindtest.Resp {
			return ag(person, "POST", "/triggers", map[string]any{"name": name, "source": "push", "sourceRef": pcHooks, "match": match,
				"goal": "quick check of {{topic}}", "toolset": "web", "dataClass": "public", "deliver": map[string]string{"alice": dmKey}[person]})
		}
		if r := trig("alice", "alice-all", ""); r.Status != 400 || !strings.Contains(string(r.Body), "topic prefix") {
			t.Errorf("alice's trigger without a prefix: %d %s", r.Status, r)
		}
		if r := trig("alice", "alice-deploys", "deploy/"); r.Status != 200 {
			t.Fatalf("alice's private trigger: %d %s", r.Status, r)
		}
		if r := trig("bob", "bob-prod", "deploy/prod"); r.Status != 409 {
			t.Errorf("bob's trigger overlapping alice's: %d %s", r.Status, r)
		}
		if r := trig("bob", "alice-deploys", "bob/"); r.Status != 409 {
			t.Errorf("bob's trigger with alice's name: %d %s", r.Status, r)
		}
		var hook struct {
			Hook   struct{ ID string }
			Secret string
		}
		ok("POST", "/api/"+pcHooks+"/hooks", map[string]string{"name": "deploy", "auth": "token", "topicFrom": "json:env", "eventIdFrom": "json:id"}).Decode(t, &hook)
		post := func() int {
			req, _ := http.NewRequest("POST", "http://"+e.ingress+"/hook/"+hook.Hook.ID+"?token="+hook.Secret, strings.NewReader(`{"env":"prod","id":"d-1"}`))
			req.Host = pcHost
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		var st int
		xbindtest.Eventually(t, 2*time.Minute, "the webhook taken", func() (bool, string) {
			st = post()
			return st == 202, fmt.Sprint(st)
		})
		xbindtest.Eventually(t, 4*time.Minute, "the trigger announces into alice's DM", func() (bool, string) {
			return len(outs(func(l pcLine) bool {
				return l.Kind == "announce" && strings.Contains(l.Text, "[trigger alice-deploys]")
			})) > 0, fmt.Sprint(transcript())
		})
		if st := post(); st != 202 {
			t.Errorf("the same delivery again: %d", st)
		}
		time.Sleep(5 * time.Second)
		if a := outs(func(l pcLine) bool { return l.Kind == "announce" }); len(a) != 1 {
			t.Errorf("the same delivery ran %d times", len(a))
		}
		var ledger struct {
			Rows []struct {
				Kind, Target string
				Count        int
			}
		}
		d.Must(t, "GET", "/api/xbin/partitions/ledger?tile="+paAgent, nil, 200, e.as("alice")...).Decode(t, &ledger)
		counted := 0
		for _, r := range ledger.Rows {
			if r.Kind == "trigger" && r.Target == pcHooks {
				counted += r.Count
			}
		}
		if counted != 1 {
			t.Errorf("alice's ledger counts the trigger's source %d times: %+v", counted, ledger.Rows)
		}
	})
}
