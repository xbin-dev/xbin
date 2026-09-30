//go:build linux && integration

package isolated

// partitions_bridge_test.go — the builtin tiles around a partitioned agent
// (work pack B3 of the partitioned-tiles plan, 09 §2-§5), end to end on a
// real `xbind --isolate` with owner auth (psSetup's daemon and people).
//
//   - llm-gw counts calls per person's partition of each partitioned caller
//     (paLLMGWCounters, a case of TestPartitionsAgent, which already runs
//     alice's and bob's partitions through it).
//   - TestPartitionsBridge: a copy of the agent-messaging-bridge template
//     (its console plays the chat platform) and the webhooks tile (on the
//     ingress listener) bound to a partitioned agent. Both reach its global
//     instance, which keeps the channel, its rules and the links: the
//     channel is claimed there, a group mention is answered there in a
//     thread, alice links her chat account from the bridge's page (the link
//     lands at global) and unlinks it, a team push trigger runs there once
//     per delivery. The hand-offs to a person's partition — a linked DM
//     answered from alice's partition (a notice first, while it never ran),
//     her private push trigger (an empty or overlapping match refused) —
//     are work pack B2c's: those cases skip, naming it, until the agent
//     template has its handoff mail handlers (pbHasB2c).
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsBridge$' ./test/isolated/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// paLLMGWCounters checks llm-gw's usage by partition after
// TestPartitionsAgent's people chatted (pkeys: person → partition id): a
// row per person's partition of apps/agent and one for its global
// instance, none for the unpartitioned instance (counted per backend only);
// a person on llm-gw's page sees their own row only.
func paLLMGWCounters(t *testing.T, e *psEnv, pkeys map[string]string) {
	t.Helper()
	d := e.d
	type row struct {
		From, Deployment, Partition, PartitionID string
		Reqs, TokIn, TokOut                      int64
	}
	stats := func(hdrs ...xbindtest.Header) (rows []row, body string) {
		t.Helper()
		var out struct{ Callers []row }
		r := d.Must(t, "GET", "/api/"+paGW+"/stats", nil, 200, hdrs...)
		r.Decode(t, &out)
		return out.Callers, string(r.Body)
	}
	find := func(rows []row, from, pid string) *row {
		for i := range rows {
			if rows[i].From == from && rows[i].PartitionID == pid && rows[i].Deployment == "" {
				return &rows[i]
			}
		}
		return nil
	}
	rows, body := stats() // the owner token: every row
	for _, c := range []struct{ who, pid, part string }{
		{"alice", pkeys["alice"], "user:alice"},
		{"bob", pkeys["bob"], "user:bob"},
		{"the global instance", "", "global"},
	} {
		r := find(rows, paAgent, c.pid)
		if r == nil || r.Partition != c.part || r.Reqs < 1 { // tokens: hack/fakeopenai reports no usage
			t.Errorf("llm-gw's row for %s of %s (%s): %+v in %s", c.who, paAgent, c.pid, r, cut(body, 600))
		}
	}
	for _, r := range rows {
		if r.From == paPlain {
			t.Errorf("BUG: the unpartitioned instance has a row of its own: %+v", r)
		}
	}
	if strings.Contains(body, "secret") {
		t.Errorf("BUG: llm-gw's counters hold content: %s", cut(body, 600))
	}
	// alice (read on llm-gw) on its page: her own row only
	mine, body := stats(e.fr(t, paGW, "alice"))
	if len(mine) == 0 {
		t.Errorf("alice on llm-gw's page sees no row of hers: %s", cut(body, 400))
	}
	for _, r := range mine {
		if r.Partition != "user:alice" {
			t.Errorf("alice on llm-gw's page sees %+v", r)
		}
	}
}

// pbHasB2c: the agent template hands a linked DM and a private trigger's
// event to the person's partition (work pack B2c's mailbox handlers,
// plans/partitions 08 §5). Until it does, the cases that need it skip.
func pbHasB2c(repo string) bool {
	dir := filepath.Join(repo, "builtin-templates", "agent", "_backend")
	ents, _ := os.ReadDir(dir)
	for _, ent := range ents {
		if !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, ent.Name())); err == nil && bytes.Contains(b, []byte(`"handoff/dm"`)) {
			return true
		}
	}
	return false
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
	b2c := pbHasB2c(d.A.Repo)
	needB2c := func(t *testing.T) {
		if !b2c {
			t.Skip("needs work pack B2c — the agent's handoff/dm, handoff/event and outbox/add mail handlers (plans/partitions 08 §5); runs once pt/b2c is merged")
		}
	}
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
		// a person's partition holds no channel: it is the tile's (bob's —
		// alice's partition must not run before the linked-dm case)
		var mine struct{ Items []item }
		agent("bob", "GET", "/automations", nil).Decode(t, &mine)
		for _, it := range mine.Items {
			if it.Kind == "channel" {
				t.Errorf("BUG: bob's partition lists the bridge's channel: %+v", it)
			}
		}
		if st := agent("bob", "POST", fmt.Sprintf("/channels/%d/claim", chID), map[string]any{}).Status; st != 409 && st != 403 && st != 404 {
			t.Errorf("bob claiming the channel in his partition: %d, want it refused", st)
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

	t.Run("linked-dm", func(t *testing.T) {
		needB2c(t)
		// alice's partition never ran (nothing above called the agent as
		// her): her DM waits in her inbox, and the agent says so in the chat
		// (mail never starts a partition)
		from := len(transcript())
		say(dm, "hello from the chat", nil)
		waitOut("a notice for a partition that never ran", from, func(l pbLine) bool {
			return l.Conversation.ID == "D1" && l.Kind == "notice"
		})
		if ls := transcript()[from:]; strings.Contains(fmt.Sprint(ls), "Hello from the fake model") {
			t.Errorf("BUG: the DM was answered before alice's partition ran: %+v", ls)
		}
		// she opens the agent: her partition starts, takes the DM from its
		// inbox and answers it from her own conversation; the reply goes out
		// through the global instance's outbox
		agent("alice", "GET", "/me", nil)
		hit := waitOut("the DM answered from alice's partition", from, func(l pbLine) bool {
			return l.Conversation.ID == "D1" && strings.Contains(l.Text, "Hello from the fake model")
		})
		t.Logf("the answer: %+v", hit)
		var list struct {
			Items []struct {
				ID     int64
				Origin string
			}
		}
		agent("alice", "GET", "/conversations?scope=mine", nil).Decode(t, &list)
		var dmRun int64
		for _, it := range list.Items {
			if it.Origin == "channel" {
				dmRun = it.ID
			}
		}
		if dmRun < pa2to40 {
			t.Fatalf("alice's partition has no channel conversation of hers (ids ≥ 2^40): %+v", list.Items)
		}
		for _, p := range []string{"", "bob", "carol"} {
			if st := agent(p, "GET", fmt.Sprintf("/runs/%d", dmRun), nil).Status; st != 404 {
				t.Errorf("BUG: %q reads alice's DM conversation %d: %d", p, dmRun, st)
			}
		}
		// the next DM goes straight to her running partition
		from = len(transcript())
		say(dm, "hello again", nil)
		waitOut("the second DM answered", from, func(l pbLine) bool {
			return l.Conversation.ID == "D1" && strings.Contains(l.Text, "Hello from the fake model")
		})
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
		// linked people, so the model never answers it
		from := len(transcript())
		say(dm, "hello", nil)
		time.Sleep(10 * time.Second)
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

	t.Run("webhook-private", func(t *testing.T) {
		needB2c(t)
		// alice's private push trigger: made in her partition, registered
		// at the global instance (name, source, match), run in hers
		mk := func(person, name, match string) xbindtest.Resp {
			return agent(person, "POST", "/triggers", map[string]any{"name": name, "source": "push", "sourceRef": pbHooks,
				"match": match, "goal": "quick check of {{topic}}", "toolset": "web", "dataClass": "public"})
		}
		if r := mk("alice", "alice-any", ""); r.Status/100 != 4 {
			t.Errorf("a private push trigger with no match: %d %s, want refused", r.Status, r)
		}
		r := mk("alice", "alice-deploy", "alice-deploy")
		if r.Status != 200 {
			t.Fatalf("alice's private push trigger: %d %s", r.Status, r)
		}
		var tr struct{ ID int64 }
		r.Decode(t, &tr)
		for _, m := range []string{"alice-deploy/x", "alice"} {
			if r := mk("bob", "bob-"+strings.ReplaceAll(m, "/", "-"), m); r.Status/100 != 4 {
				t.Errorf("bob's private trigger overlapping alice's match (%q): %d %s, want refused", m, r.Status, r)
			}
		}
		id, secret := makeHook(t, "alice-deploy")
		var st int
		xbindtest.Eventually(t, 2*time.Minute, "the hook taken", func() (bool, string) {
			st = hook(t, id, secret, map[string]string{"ref": "alice"})
			return st == 202, fmt.Sprint(st)
		})
		agent("alice", "GET", "/me", nil) // her partition runs (it has, since linked-dm)
		xbindtest.Eventually(t, 2*time.Minute, "alice's private trigger ran in her partition", func() (bool, string) {
			ev := events("alice", tr.ID)
			return len(ev) == 1 && ev[0].Accepted, fmt.Sprintf("%+v", ev)
		})
		for _, p := range []string{"bob", ""} {
			if ev := events(p, tr.ID); len(ev) != 0 {
				t.Errorf("BUG: %q sees alice's private trigger's events: %+v", p, ev)
			}
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
		if b2c && seen["user:alice"] < 2 {
			t.Errorf("llm-gw's row for alice's partition (her two DMs): %v", seen)
		}
	})
}
