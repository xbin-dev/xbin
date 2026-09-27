package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// lastRunEvent is the latest run event for id a subscriber got.
func lastRunEvent(t *testing.T, s *subscriber, id int64) map[string]any {
	t.Helper()
	var last map[string]any
	for _, ev := range s.drain() {
		if ev.Type == evRun && ev.Run == id {
			last, _ = ev.Data.(map[string]any)
		}
	}
	if last == nil {
		t.Fatalf("no run event for #%d", id)
	}
	return last
}

// Run events (and the view's run) carry the active binding and how many
// are attached, so a view follows a rebind without reading the view again.
func TestRunEventsCarryTheSandbox(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := createConv(t, ag, alicePrivate, nil)
	sub, _, _ := ag.eng.hub.subscribe(conv.ID, ag.eng.hub.now(), who{kind: whoSystem})
	defer ag.eng.hub.unsubscribe(sub)
	ag.eng.publishRun(conv.ID)
	if ev := lastRunEvent(t, sub, conv.ID); ev["sandbox"] != nil || ev["attached"] != 0 {
		t.Fatalf("unbound: sandbox %v, attached %v", ev["sandbox"], ev["attached"])
	}
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "evbox"})
	ref := sandboxRef("apps/cs", box.ID)
	if got := bindTo(t, mux, asAlice, conv.ID, ref, ""); got != 200 {
		t.Fatalf("bind: %d", got)
	}
	ev := lastRunEvent(t, sub, conv.ID)
	sb, _ := ev["sandbox"].(map[string]any)
	if sb == nil || sb["ref"] != ref || sb["name"] != "evbox" || sb["egress"] != "none" || sb["cwd"] != box.Workdir ||
		sb["manager"] != "Fake sandboxes (test fixture)" || ev["attached"] != 1 {
		t.Fatalf("bound: %v (attached %v)", ev["sandbox"], ev["attached"])
	}
	raw, _ := json.Marshal(ev)
	if !strings.Contains(string(raw), `"attached":1`) {
		t.Fatalf("on the wire: %s", raw)
	}
	var view struct {
		Run struct {
			Sandbox  *struct{ Ref, Name string }
			Attached int
		}
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", conv.ID), nil).Body.Bytes(), &view)
	if view.Run.Sandbox == nil || view.Run.Sandbox.Ref != ref || view.Run.Attached != 1 {
		t.Fatalf("the view's run: %+v", view.Run)
	}
	// unbound again (it stays attached)
	if w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", conv.ID), map[string]any{"sandbox": nil}); w.Code != 200 {
		t.Fatalf("unbind: %d", w.Code)
	}
	if ev := lastRunEvent(t, sub, conv.ID); ev["sandbox"] != nil || ev["attached"] != 1 {
		t.Fatalf("unbound: %v %v", ev["sandbox"], ev["attached"])
	}
}

// Deleting a conversation kills the jobs it left running (in the
// background: the delete doesn't wait for the manager).
func TestDeletingAConversationKillsItsJobs(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	r, cfg, box := sbxRun(t, ag, "del", "none")
	mustTool(t, ag, r, cfg, "c1", "bash", map[string]any{"command": "sleep 30", "background": true})
	mustTool(t, ag, r, cfg, "c2", "bash", map[string]any{"command": "true"})
	j, err := ag.db.job(r.ID, 1)
	if err != nil || j.Exec == "" || !j.running() {
		t.Fatalf("job 1: %+v %v", j, err)
	}
	conn, _ := sbxDial("apps/cs", "")
	start := time.Now()
	if w := callAs(t, mux, asSystem, "DELETE", fmt.Sprintf("/runs/%d", r.ID), nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the delete waited %s", took)
	}
	waitFor(t, "the job to be killed", func() bool {
		ex, err := conn.ExecGet(context.Background(), box.ID, j.Exec)
		return err == nil && ex.State != "running"
	})
	if ex, _ := conn.ExecGet(context.Background(), box.ID, j.Exec); ex.State != "killed" || ex.Signal != "KILL" {
		t.Fatalf("job 1 ended as %s (%s)", ex.State, ex.Signal)
	}
	if n := ag.db.jobList(r.ID, false, 10); len(n) != 0 {
		t.Fatalf("its jobs are still listed: %d", len(n))
	}
	if _, err := conn.Get(context.Background(), box.ID); err != nil {
		t.Fatalf("the sandbox itself stays: %v", err)
	}
}

// A sandbox whose network access changed since it was bound: the stored
// bindings follow it, and in Approve mode a call that would now park is
// refused once — called again, it parks for approval.
func TestEgressChangeReasks(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := createConv(t, ag, alicePrivate, func(c *Config) { c.Approve = true })
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "net"})
	ref := sandboxRef("apps/cs", box.ID)
	if got := bindTo(t, mux, asAlice, conv.ID, ref, ""); got != 200 {
		t.Fatalf("bind: %d", got)
	}
	conn, _ := sbxDial("apps/cs", "")
	internet := "internet"
	if _, err := conn.Patch(context.Background(), box.ID, sbxPatch{Egress: &internet}); err != nil {
		t.Fatal(err)
	}
	f := fakeOf(ag)
	f.on(lastIs("tool", "network access changed"), callTools(tc("b2", "bash", `{"command":"echo hi"}`)))
	f.on(lastIs("tool", ""), say("done"))
	f.on(lastUser("run it"), callTools(tc("b1", "bash", `{"command":"echo hi"}`)))
	send(t, ag, conv.ID, "run it")
	waitStatus(t, ag.db, conv.ID, statusWaiting)
	p := parsePending(mustRun(t, ag, conv.ID).Pending)
	if p.Kind != "approval" || p.Grant != "" || len(p.ToolCalls) != 1 || p.ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("parked on %+v", p)
	}
	msgs, _ := ag.db.messages(conv.ID, false)
	refused := false
	for _, m := range msgs {
		if m.Role == "tool" && m.Content == "error: the sandbox's network access changed from none to internet; call the tool again to ask for approval" {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the first call wasn't refused:\n%s", transcript(ag.db, conv.ID))
	}
	act, att := sandboxOf(t, ag, conv.ID)
	if act.Egress != "internet" || att[0].Egress != "internet" {
		t.Fatalf("stored: %+v %+v", act, att)
	}
	if got := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", conv.ID), map[string]any{"approve": true}).Code; got != 200 {
		t.Fatalf("approve: %d", got)
	}
	waitQuiet(t, ag)
	if out := lastToolOf(ag, conv.ID); !strings.HasPrefix(out, "hi\n[exit 0") {
		t.Fatalf("approved: %q", out)
	}

	// what doesn't change a sandbox goes on; a subagent's own copy follows too
	none := "none"
	if _, err := conn.Patch(context.Background(), box.ID, sbxPatch{Egress: &none}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := ag.db.runConfig(conv.ID)
	kcfg := childConfig(cfg, "")
	raw, _ := json.Marshal(kcfg)
	kid, _ := ag.db.createRun("kid", string(raw), conv.ID)
	kr, _ := ag.db.getRun(kid)
	if _, err := ag.runTool(context.Background(), kr, kcfg, "ls", map[string]any{}); err != nil {
		t.Fatalf("ls after a change: %v", err)
	}
	if act, _ := sandboxOf(t, ag, conv.ID); act.Egress != "none" {
		t.Fatalf("the conversation's: %+v", act)
	}
	if act, _ := sandboxOf(t, ag, kid); act.Egress != "none" {
		t.Fatalf("the subagent's copy: %+v", act)
	}
	// none → internet again, from a subagent in Approve mode: write is refused once
	if _, err := conn.Patch(context.Background(), box.ID, sbxPatch{Egress: &internet}); err != nil {
		t.Fatal(err)
	}
	kcfg, _ = ag.db.runConfig(kid)
	if _, err := ag.runTool(context.Background(), kr, kcfg, "write", map[string]any{"path": "x", "content": "y"}); err == nil ||
		!strings.Contains(err.Error(), "changed from none to internet; call the tool again") {
		t.Fatalf("write after a change: %v", err)
	}
	kcfg, _ = ag.db.runConfig(kid)
	if !sideEffect("write", kcfg) {
		t.Fatal("the subagent's stored copy doesn't park write now")
	}
}
