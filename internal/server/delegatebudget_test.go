package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// covers D246 — the context budget on the admin's card and to the agent:
// the default 1 000 000, what the agent role's model keeps under it (a
// 256k window: 198 144; unknown: 32 000), an admin's lower setting, null
// back to the default, the host's preset as a ceiling (409 past it, by
// name, in the one locks map), and GET /delegate/state's contextBudget.
func TestContextBudgetSetting(t *testing.T) {
	w := newWebRig(t)
	window := 262_144
	w.s.AgentModelWindow = func(context.Context) (string, int) { return "up/big", window }
	read := func() (v struct {
		ContextBudget *int `json:"contextBudget"`
		Policy        delegation.NetInForce
		Budget        struct {
			Setting, Keeps, Window, Margin int
			Model                          string
			ByHost                         bool
		}
		Locks map[string]map[string]any
	}) {
		t.Helper()
		rec := w.do(w.full, "GET", "/api/xbin/delegate/workspace", "", w.cookie("alice"))
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
			t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
		}
		return v
	}
	v := read()
	if v.ContextBudget != nil || v.Policy.ContextBudget != 1_000_000 || v.Budget.Setting != 1_000_000 || v.Budget.Keeps != 198_144 || v.Budget.Model != "up/big" || v.Budget.Margin != 64_000 {
		t.Fatalf("the default, on a 256k model: %+v", v)
	}
	window = 0
	if v := read(); v.Budget.Keeps != 32_000 || v.Budget.Window != 0 {
		t.Errorf("an unknown window: %+v", v.Budget)
	}
	window = 1_048_576
	if v := read(); v.Budget.Keeps != 984_576 {
		t.Errorf("a 1M window: %+v", v.Budget)
	}
	if rec := w.policy(`{"contextBudget":150000}`); rec.Code != 200 {
		t.Fatalf("a smaller setting: %d %s", rec.Code, rec.Body.String())
	}
	if v := read(); v.ContextBudget == nil || *v.ContextBudget != 150_000 || v.Budget.Keeps != 150_000 {
		t.Errorf("a smaller setting: %+v", v)
	}
	rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", w.cookie("bob"))
	if !strings.Contains(rec.Body.String(), `"contextBudget":150000`) {
		t.Errorf("the person's state: %s", rec.Body.String())
	}
	for _, bad := range []string{`{"contextBudget":5}`, `{"contextBudget":"lots"}`, `{"contextBudget":20000000}`} {
		if rec := w.policy(bad); rec.Code != 400 {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	// the host's preset: a ceiling the admin may stay under
	host := delegation.HostNet{ContextBudget: 120_000}
	w.store.SetHost(func() delegation.HostNet { return host })
	if v := read(); v.Policy.ContextBudget != 120_000 || !v.Policy.BudgetByHost || v.Locks["contextBudget"]["ceiling"] != "120000" || !v.Budget.ByHost {
		t.Errorf("under the host's ceiling: %+v", v)
	}
	if rec := w.policy(`{"contextBudget":500000}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"settings":["contextBudget"]`) {
		t.Errorf("past the host's ceiling: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.policy(`{"contextBudget":null}`); rec.Code != 200 {
		t.Errorf("back to the default: %d %s", rec.Code, rec.Body.String())
	}
	if v := read(); v.ContextBudget != nil || v.Policy.ContextBudget != 120_000 {
		t.Errorf("the default under a host preset is the host's: %+v", v)
	}
	// the agent reads it; nothing else of the admin's card
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/workspace", "", w.cookie("bob")); rec.Code != 403 {
		t.Errorf("a person who is no admin reads the card: %d", rec.Code)
	}
}
