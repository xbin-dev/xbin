//go:build linux && integration

package isolated

// partitions_bridge_test.go — the builtin tiles around a partitioned agent
// (work pack B3 of the partitioned-tiles plan, 09 §2-§5), end to end on a
// real `xbind --isolate` with owner auth (psSetup's daemon and people).
//
//   - llm-gw counts calls per person's partition of each partitioned caller
//     (paLLMGWCounters, a case of TestPartitionsAgent, which already runs
//     alice's and bob's partitions through it), and shows each viewer what
//     is theirs: the owner token every row, an admin (a manager of it) the
//     totals and the global instance, a person their own.
//   - TestPartitionsBridge: a copy of the agent-messaging-bridge template
//     (its console plays the chat platform) and the webhooks tile (on the
//     ingress listener) bound to a partitioned agent. Both reach its global
//     instance, which keeps the channel, its rules and the links: the
//     channel is claimed there (a person who doesn't manage the agent is
//     refused, 403, from their own partition too), a group mention is
//     answered there in a thread, alice links her chat account from the
//     bridge's page (the link lands at global) and unlinks it (her chat
//     account is a stranger again: a link code, no answer), a team push
//     trigger runs there once per delivery.
//
// The hand-offs to a person's partition — a linked DM answered from it, a
// private push trigger run in it — are work pack B2c's, and its e2e
// (partitions_agent_channels_test.go, TestPartitionsAgentChannels) drives
// them through the same bridge and webhooks tiles.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsBridge$' ./test/isolated/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// paLLMGWCounters checks llm-gw's usage by partition after
// TestPartitionsAgent's people chatted (pkeys: person → partition id): a
// row per person's partition of apps/agent and one for its global
// instance, none for the unpartitioned instance (counted per backend only).
// Who sees what (PD-46): the owner token every row — a person's without its
// last use — and so its page opened with the owner token; carol, an admin
// signed in (a manager of llm-gw, as far as a tile can tell), the people's
// partitions together and the global instance, never a person's row; alice
// on its page her own row only.
func paLLMGWCounters(t *testing.T, e *psEnv, pkeys map[string]string) {
	t.Helper()
	d := e.d
	type row struct {
		From, Deployment, Partition, PartitionID string
		Reqs, TokIn, TokOut, Last                int64
	}
	type total struct {
		From       string
		Partitions int
		Reqs       int64
	}
	stats := func(hdrs ...xbindtest.Header) (rows []row, totals []total, body string) {
		t.Helper()
		var out struct {
			Callers      []row
			CallerTotals []total
		}
		r := d.Must(t, "GET", "/api/"+paGW+"/stats", nil, 200, hdrs...)
		r.Decode(t, &out)
		return out.Callers, out.CallerTotals, string(r.Body)
	}
	find := func(rows []row, from, pid string) *row {
		for i := range rows {
			if rows[i].From == from && rows[i].PartitionID == pid && rows[i].Deployment == "" {
				return &rows[i]
			}
		}
		return nil
	}
	every := func(who string, rows []row, body string) {
		t.Helper()
		for _, c := range []struct{ who, pid, part string }{
			{"alice", pkeys["alice"], "user:alice"},
			{"bob", pkeys["bob"], "user:bob"},
			{"the global instance", "", "global"},
		} {
			r := find(rows, paAgent, c.pid)
			if r == nil || r.Partition != c.part || r.Reqs < 1 || (r.Last == 0) != (c.pid != "") { // tokens: hack/fakeopenai reports no usage
				t.Errorf("%s: llm-gw's row for %s of %s (%s): %+v in %s", who, c.who, paAgent, c.pid, r, cut(body, 600))
			}
		}
	}
	rows, _, body := stats() // the owner token
	every("the owner token", rows, body)
	for _, r := range rows {
		if r.From == paPlain {
			t.Errorf("BUG: the unpartitioned instance has a row of its own: %+v", r)
		}
	}
	if strings.Contains(body, "secret") {
		t.Errorf("BUG: llm-gw's counters hold content: %s", cut(body, 600))
	}
	rows, _, body = stats(e.fr(t, paGW, "")) // its page, opened with the owner token
	every("the owner token's page", rows, body)
	// carol, an admin signed in: llm-gw can't tell her from a manager — the
	// totals and the global instance, no one's row
	rows, totals, body := stats(e.fr(t, paGW, "carol"))
	if find(rows, paAgent, "") == nil {
		t.Errorf("carol on llm-gw's page: no row of the global instance in %s", cut(body, 600))
	}
	for _, r := range rows {
		if r.Partition != "global" && r.Partition != "user:carol" {
			t.Errorf("BUG: carol on llm-gw's page sees someone's row: %+v", r)
		}
	}
	if len(totals) != 1 || totals[0].From != paAgent || totals[0].Partitions < 2 || totals[0].Reqs < 2 ||
		strings.Contains(body, pkeys["alice"]) || strings.Contains(body, "user:alice") {
		t.Errorf("carol's totals: %+v in %s, want alice's and bob's partitions together, naming no one", totals, cut(body, 600))
	}
	// alice (read on llm-gw) on its page: her own row only, with its last use
	mine, totals, body := stats(e.fr(t, paGW, "alice"))
	if len(mine) == 0 || len(totals) != 0 {
		t.Errorf("alice on llm-gw's page: rows %+v, totals %+v in %s", mine, totals, cut(body, 400))
	}
	for _, r := range mine {
		if r.Partition != "user:alice" || r.Last == 0 {
			t.Errorf("alice on llm-gw's page sees %+v", r)
		}
	}
}

const (
	pbBridge = "apps/bridge"
	pbHooks  = "apps/webhooks"
	pbHost   = "hooks.test"
)

// pbLine is one line of the bridge console's transcript.
type pbLine struct {
	Dir, Kind, Text, Thread string
	Conversation            struct{ ID string }
}

func TestPartitionsBridge(t *testing.T) {
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

	// llm-gw → fakeopenai; the agent (partitioned by default) on it
	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "llm-gw", "path": paGW})
	d.WaitComponent(t, paGW)
	d.Bind(t, paGW, "net", "host")
	ok("PUT", "/api/xbin/vault/"+paGW+"/api-token-fake", map[string]string{"value": "sk-fake"})
	var inst struct{ Partition []string }
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": paAgent}).Decode(t, &inst)
	if strings.Join(inst.Partition, ",") != "user,global" {
		t.Fatalf("the agent's new instance isn't partitioned: %v", inst.Partition)
	}
	d.WaitComponent(t, paAgent)
	ok("POST", "/api/xbin/bindings", map[string]any{"component": paAgent, "slot": "llm", "providers": []string{paGW}})
	d.Bind(t, paAgent, "net", "none")
	// the bridge (its console plays the platform) and the webhooks tile,
	// bound to the partitioned agent: ordinary (global) binds
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent-messaging-bridge", "path": pbBridge})
	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "webhooks", "path": pbHooks})
	d.WaitComponent(t, pbBridge)
	d.WaitComponent(t, pbHooks)
	d.Bind(t, pbBridge, "agent", paAgent)
	d.Bind(t, pbHooks, "agents", paAgent)
	d.Must(t, "POST", "/api/xbin/bindings", map[string]string{"component": pbHooks, "slot": "hooks", "provider": "runtime", "host": pbHost}, 200)

	xbindtest.Eventually(t, 5*time.Minute, "llm-gw's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+paGW+"/config", nil)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
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

	// agent calls the agent as person through their frame ("" = the owner
	// token, which reaches the global instance)
	agent := func(person, method, path string, body any) xbindtest.Resp {
		t.Helper()
		var hdrs []xbindtest.Header
		if person != "" {
			hdrs = append(hdrs, e.fr(t, paAgent, person))
		}
		return d.Call(t, method, "/api/"+paAgent+path, body, hdrs...)
	}
	transcript := func() []pbLine {
		var out struct{ Lines []pbLine }
		if r := d.Call(t, "GET", "/api/"+pbBridge+"/console/transcript", nil); r.Status == 200 {
			_ = json.Unmarshal(r.Body, &out)
		}
		return out.Lines
	}
	// waitOut waits for an outgoing line after the first `from` lines.
	waitOut := func(what string, from int, match func(pbLine) bool) pbLine {
		t.Helper()
		var hit pbLine
		xbindtest.Eventually(t, 3*time.Minute, what, func() (bool, string) {
			ls := transcript()
			for i := from; i < len(ls); i++ {
				if ls[i].Dir == "out" && match(ls[i]) {
					hit = ls[i]
					return true, ""
				}
			}
			return false, fmt.Sprintf("%d lines, the last: %+v", len(ls), ls[max(0, len(ls)-3):])
		})
		return hit
	}
	say := func(conv map[string]string, text string, extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{"as": map[string]string{"id": "u1", "name": "Uma"}, "conversation": conv, "text": text}
		for k, v := range extra {
			body[k] = v
		}
		var out map[string]any
		ok("POST", "/api/"+pbBridge+"/console/send", body).Decode(t, &out)
		return out
	}
	dm := map[string]string{"id": "D1", "type": "dm"}
	general := map[string]string{"id": "C1", "type": "channel", "name": "general"}
	var chID int64

	t.Run("claim-at-global", func(t *testing.T) {
		// the bridge connects by itself (alwaysOn) and says hello: to the
		// global instance — the channel is offered there, to its managers
		xbindtest.Eventually(t, 5*time.Minute, "the bridge connected", func() (bool, string) {
			var st struct{ State struct{ Phase string } }
			r := d.Call(t, "GET", "/api/"+pbBridge+"/status", nil)
			_ = json.Unmarshal(r.Body, &st)
			return st.State.Phase == "connected", fmt.Sprint(r.Status, " ", cut(string(r.Body), 300))
		})
		type item struct {
			Kind, Access string
			ID           int64
		}
		var items []item
		xbindtest.Eventually(t, time.Minute, "the global instance offers the channel", func() (bool, string) {
			var out struct{ Items []item }
			r := agent("", "GET", "/automations", nil)
			_ = json.Unmarshal(r.Body, &out)
			items = out.Items
			for _, it := range items {
				if it.Kind == "channel" {
					chID = it.ID
					return true, ""
				}
			}
			return false, fmt.Sprint(r.Status, " ", cut(string(r.Body), 300))
		})
		// bob reads the agent, managing nothing: his partition doesn't offer
		// him the unclaimed channel, and claiming it from there is refused
		// as a manager's act (403) — before it could reach the global
		// instance, where it would be refused the same
		var mine struct{ Items []item }
		agent("bob", "GET", "/automations", nil).Decode(t, &mine)
		for _, it := range mine.Items {
			if it.Kind == "channel" {
				t.Errorf("BUG: bob's partition offers him the unclaimed channel: %+v", it)
			}
		}
		if r := agent("bob", "POST", fmt.Sprintf("/channels/%d/claim", chID), map[string]any{}); r.Status != 403 {
			t.Errorf("bob claiming the channel from his partition: %d %s, want 403", r.Status, r)
		}
		ok("POST", "/api/"+paAgent+fmt.Sprintf("/channels/%d/claim", chID), map[string]any{
			"policy": map[string]any{"dm": map[string]string{"policy": "linked"}, "groups": map[string]any{"allow": []string{"C1"}}},
		})
		xbindtest.Eventually(t, time.Minute, "the bridge is told it is claimed", func() (bool, string) {
			var st struct {
				State struct{ Accounts []struct{ Claimed string } }
			}
			r := d.Call(t, "GET", "/api/"+pbBridge+"/status", nil)
			_ = json.Unmarshal(r.Body, &st)
			return len(st.State.Accounts) > 0 && st.State.Accounts[0].Claimed == "active", cut(string(r.Body), 300)
		})
	})
	if chID == 0 {
		t.Fatal("no channel: the cases below need it")
	}

	t.Run("group-at-global", func(t *testing.T) {
		// a mention in a channel: a shared conversation at the global
		// instance, answered in a thread under it
		from := len(transcript())
		m := say(general, "quick question", map[string]any{"mentioned": true})
		root, _ := m["messageId"].(string)
		hit := waitOut("the thread answer", from, func(l pbLine) bool { return l.Conversation.ID == "C1" && l.Thread == root })
		if !strings.Contains(hit.Text, "Quick answer") {
			t.Errorf("the group answer: %+v", hit)
		}
	})

	linkCode := regexp.MustCompile(`\b([A-Z2-9]{8})\b`)
	link := func(t *testing.T) {
		t.Helper()
		from := len(transcript())
		say(dm, "hello", nil)
		l := waitOut("a link code for the unlinked DM", from, func(l pbLine) bool {
			return l.Conversation.ID == "D1" && strings.Contains(l.Text, "link") && linkCode.MatchString(l.Text)
		})
		code := linkCode.FindStringSubmatch(l.Text)[1]
		// alice pastes it on the bridge's page: its frame calls the agent as
		// her, which reaches the global instance (another tile's frame)
		from = len(transcript())
		d.Must(t, "POST", "/api/"+paAgent+"/adapter/link", map[string]string{"code": code}, 200, e.fr(t, pbBridge, "alice"))
		waitOut("the bridge tells the chat they are linked", from, func(l pbLine) bool { return strings.Contains(l.Text, "@alice") })
	}
	type linkRow struct {
		ChannelID int64
		PeerID    string
	}
	links := func(person string) []linkRow {
		t.Helper()
		var out struct{ Links []linkRow }
		d.Must(t, "GET", "/api/"+paAgent+"/adapter/links", nil, 200, e.fr(t, pbBridge, person)).Decode(t, &out)
		return out.Links
	}

	t.Run("link-at-global", func(t *testing.T) {
		link(t)
		if ls := links("alice"); len(ls) != 1 || ls[0].ChannelID != chID || ls[0].PeerID != "u1" {
			t.Errorf("alice's links (from the bridge's page): %+v", ls)
		}
		if ls := links("bob"); len(ls) != 0 {
			t.Errorf("BUG: bob's links list alice's: %+v", ls)
		}
	})

	t.Run("unlink", func(t *testing.T) {
		ls := links("alice")
		if len(ls) != 1 {
			t.Fatalf("alice's links: %+v", ls)
		}
		d.Must(t, "DELETE", fmt.Sprintf("/api/%s/adapter/links/%d/%s", paAgent, ls[0].ChannelID, ls[0].PeerID), nil, 200, e.fr(t, pbBridge, "alice"))
		if ls := links("alice"); len(ls) != 0 {
			t.Errorf("alice's links after unlinking: %+v", ls)
		}
		// unlinked, the account is a stranger again: the channel hears only
		// linked people, so its DM gets a new link code — and no answer
		from := len(transcript())
		say(dm, "hello", nil)
		waitOut("a link code for the account unlinked", from, func(l pbLine) bool {
			return l.Conversation.ID == "D1" && strings.Contains(l.Text, "only talk with people who linked") && linkCode.MatchString(l.Text)
		})
		time.Sleep(3 * time.Second)
		for _, l := range transcript()[from:] {
			if l.Dir == "out" && strings.Contains(l.Text, "Hello from the fake model") {
				t.Errorf("BUG: an unlinked DM was answered: %+v", l)
			}
		}
	})

	hook := func(t *testing.T, id, secret string, body any) int {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", "http://"+e.ingress+"/hook/"+id+"?token="+secret, bytes.NewReader(b))
		req.Host = pbHost
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	makeHook := func(t *testing.T, name string) (id, secret string) {
		t.Helper()
		var out struct {
			Hook   struct{ ID string }
			Secret string
		}
		ok("POST", "/api/"+pbHooks+"/hooks", map[string]string{"name": name, "auth": "token"}).Decode(t, &out)
		return out.Hook.ID, out.Secret
	}
	type trigEvent struct{ Accepted bool }
	events := func(person string, id int64) []trigEvent {
		var out struct{ Events []trigEvent }
		_ = json.Unmarshal(agent(person, "GET", fmt.Sprintf("/triggers/%d/events", id), nil).Body, &out)
		return out.Events
	}

	t.Run("webhook-team", func(t *testing.T) {
		// a team push trigger at the global instance: a delivery runs it
		// there, once however often it comes
		var tr struct{ ID int64 }
		ok("POST", "/api/"+paAgent+"/triggers", map[string]any{"name": "team-deploy", "source": "push", "sourceRef": pbHooks,
			"match": "deploy", "goal": "quick check of {{topic}}", "toolset": "web", "dataClass": "public"}).Decode(t, &tr)
		id, secret := makeHook(t, "deploy")
		var st int
		xbindtest.Eventually(t, 2*time.Minute, "the hook taken", func() (bool, string) {
			st = hook(t, id, secret, map[string]string{"ref": "main"})
			return st == 202, fmt.Sprint(st)
		})
		xbindtest.Eventually(t, time.Minute, "the team trigger ran once", func() (bool, string) {
			ev := events("", tr.ID)
			return len(ev) == 1 && ev[0].Accepted, fmt.Sprintf("%+v", ev)
		})
		if again := hook(t, id, secret, map[string]string{"ref": "main"}); again != 202 {
			t.Errorf("the same delivery again: %d", again)
		}
		time.Sleep(2 * time.Second)
		if ev := events("", tr.ID); len(ev) != 1 {
			t.Errorf("the same delivery ran the trigger again: %+v", ev)
		}
		// nobody's trigger takes another topic: 404 to the sender
		oid, osecret := makeHook(t, "nobody")
		if st := hook(t, oid, osecret, map[string]string{"x": "y"}); st != 404 {
			t.Errorf("a hook no trigger takes: %d, want 404", st)
		}
	})

	t.Run("llm-gw-counters", func(t *testing.T) {
		// the group answer and the team trigger ran at the global instance
		var out struct {
			Callers []struct {
				From, Partition string
				Reqs            int64
			}
		}
		d.Must(t, "GET", "/api/"+paGW+"/stats", nil, 200).Decode(t, &out)
		seen := map[string]int64{}
		for _, r := range out.Callers {
			if r.From == paAgent {
				seen[r.Partition] += r.Reqs
			}
		}
		if seen["global"] < 2 {
			t.Errorf("llm-gw's row for the agent's global instance: %v, want the group answer and the trigger's run", seen)
		}
	})
}
