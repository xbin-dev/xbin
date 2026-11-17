package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// A plan's dry run stores nothing, so the cap on open plans never stops it.
// It is an act at the gate (the person's act bucket), and a conversation
// has a share of its own on top — one conversation's loop of dry runs
// can't spend what the person's other conversations, and their acts,
// need. Its lines are capped as a proposal's are.
func TestPlanDryRunIsRateLimited(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.ChangeSets = planStub{}
	now := time.Now()
	w.s.delegateRT.now = func() time.Time { return now }
	other, err := w.store.NewAgentSession("alice", dHolder, nil, "local/test")
	if err != nil {
		t.Fatal(err)
	}
	conv := func(id string) cred {
		base := delegateCredBare("d-alice")
		return func(r *http.Request) { base(r); r.Header.Set(HeaderAgentSession, id) }
	}
	first, _ := testHandles.Load("d-alice")
	const body = `{"title":"t","dryRun":true,"items":[{"kind":"agent.start","args":{"tile":"apps/x","brief":"Goal: fix the bug.","harness":"claude"}}]}`
	dry := func(c cred) (int, string, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", body, c)
		return rec.Code, rec.Header().Get("Retry-After"), rec.Body.String()
	}
	// one conversation: its share, then 429 with when to try again
	for i := range planDryShareBurst {
		if code, _, b := dry(conv(first.(string))); code != 200 {
			t.Fatalf("dry run %d: %d %s", i+1, code, b)
		}
	}
	code, retry, b := dry(conv(first.(string)))
	if n, _ := strconv.Atoi(retry); code != http.StatusTooManyRequests || n < 1 || !strings.Contains(b, "this conversation") {
		t.Fatalf("a conversation's dry run past its share: %d (Retry-After %q) %s", code, retry, b)
	}
	// another conversation of the same person still has its own — until the person's acts are spent
	convs := []string{other.ID}
	for range delegateActBurst/planDryShareBurst - 2 {
		h, err := w.store.NewAgentSession("alice", dHolder, nil, "local/test")
		if err != nil {
			t.Fatal(err)
		}
		convs = append(convs, h.ID)
	}
	spent := planDryShareBurst + 1 // the refused one was an act at the gate too
	for _, id := range convs {
		for i := 0; i < planDryShareBurst && spent < delegateActBurst; i++ {
			if code, _, b := dry(conv(id)); code != 200 {
				t.Fatalf("another conversation's dry run (the person's act %d): %d %s", spent+1, code, b)
			}
			spent++
		}
	}
	fresh, _ := w.store.NewAgentSession("alice", dHolder, nil, "local/test")
	if code, retry, b := dry(conv(fresh.ID)); code != http.StatusTooManyRequests || retry == "" || !strings.Contains(b, "your agent is acting too fast") {
		t.Fatalf("a dry run with the person's acts spent: %d (Retry-After %q) %s", code, retry, b)
	}
	// time gives it back
	now = now.Add(planDryShareRefill)
	if code, _, b := dry(conv(first.(string))); code != 200 {
		t.Fatalf("after the refill: %d %s", code, b)
	}
	// the lines of a dry run are capped as a proposal's
	many := `{"title":"t","dryRun":true,"items":[` + strings.TrimSuffix(strings.Repeat(`{"kind":"agent.start","args":{"tile":"apps/x","brief":"go"}},`, delegation.MaxItems+1), ",") + `]}`
	now = now.Add(time.Hour)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/plans", many, conv(first.(string))); rec.Code != 400 {
		t.Fatalf("a dry run of %d lines: %d", delegation.MaxItems+1, rec.Code)
	}
	if all, _ := w.store.ChangeSets("alice"); len(all) != 0 {
		t.Fatalf("dry runs kept %d change sets", len(all))
	}
}
