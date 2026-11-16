package acptest

import (
	"context"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// codex's detached turn through a real acp.Client (the detach… script): the
// prompt's turn ends, the steer answers SteerStartedNewTurn — which
// SteerOutcome reads as delivered, detached — and the agent's own turn runs
// with the client idle: no turn.end follows it, and a permission it asks
// for leaves the client idle again once answered (the status a host that
// follows the turn — acp.DetachedTurn — reads as running).
func TestClientDetachedTurn(t *testing.T) {
	r := startTx(t, "detached", "not a shared transcript: the detach script through acp.Client", []string{"--steer"}, false)
	if !r.c.State().Steering {
		t.Fatal("the agent advertises steering")
	}
	r.prompt("detach now")
	r.until("the turn's first chunk", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvMessageDelta && d["role"] == "agent" })
	out, err := r.c.Steer(context.Background(), acp.Prompt{Text: "perm please"})
	if next, detached := acp.SteerOutcome(out, err); out != acp.SteerStartedNewTurn || next != acp.MessageDelivered || !detached {
		t.Fatalf("the steer: %q %v → %s, detached %v", out, err, next, detached)
	}
	// in the output's order: the prompt's end and its idle status, then the steer's echo
	_, end := r.until("the prompt's end", func(e txEvent, _ map[string]any) bool { return e.Type == acp.EvTurnEnd })
	if end["stopReason"] != "end_turn" || end["turn"] != float64(1) {
		t.Fatalf("the prompt's turn.end: %v", end)
	}
	r.until("idle", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvStatus && d["status"] == acp.StatusIdle })
	r.until("the steer's echo", func(e txEvent, d map[string]any) bool {
		return e.Type == acp.EvMessageDelta && d["role"] == "user" && d["steered"] == true && d["text"] == "perm please"
	})
	if q := (acp.QueueState{Status: acp.StatusIdle, Detached: true, Steering: true}); q.Message(false) != acp.MessageHold || q.Flush() {
		t.Fatal("a message during the agent's own turn is held")
	}
	// the agent's own turn asks: waiting, and — answered outside any prompt — idle again
	req := r.nth(1, acp.EvPermissionRequest, nil)
	r.until("waiting", func(e txEvent, d map[string]any) bool {
		return e.Type == acp.EvStatus && d["status"] == acp.StatusWaiting
	})
	res, err := r.perms.Resolve(req["pid"].(string), "once", "", "user:test")
	if err == nil {
		err = r.c.RespondPermission(res)
	}
	if err != nil {
		t.Fatal(err)
	}
	r.until("the resolution", func(e txEvent, _ map[string]any) bool { return e.Type == acp.EvPermissionResolved })
	if _, d := r.until("the status after the answer", func(e txEvent, _ map[string]any) bool { return e.Type == acp.EvStatus }); d["status"] != acp.StatusIdle {
		t.Fatalf("answered outside a prompt, the client says %v (it said waiting_permission for good)", d["status"])
	}
	r.until("the script's last word", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvMessageDelta && d["text"] == "listed" })
	r.until("its usage", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvStatus && d["usage"] != nil })
	// nothing ends that turn: the only turn.end is the prompt's
	r.mu.Lock()
	ends := 0
	for _, e := range r.evs {
		if e.Type == acp.EvTurnEnd {
			ends++
		}
	}
	r.mu.Unlock()
	if ends != 1 {
		t.Fatalf("%d turn.end events, want the prompt's only", ends)
	}
}
