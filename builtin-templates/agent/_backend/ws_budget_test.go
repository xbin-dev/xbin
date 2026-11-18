package main

import (
	"context"
	"fmt"
	"testing"
)

// covers D246 — a workspace agent's context budget: min(setting, what the
// model's window allows) with the vectors xbind's TestEffectiveBudget
// holds (delegation/budget.go) — the default on a 1M-window model keeps up
// to the window less 64 000, a 256k model what its window allows, a small
// window and an unknown one today's rule — and xbind's setting reaches the
// compaction trigger through the state read (an xbind before it: today's
// rule alone).
func TestWsContextBudget(t *testing.T) {
	for _, c := range []struct{ setting, window, want int }{
		{1_000_000, 1_048_576, 984_576},
		{1_000_000, 262_144, 198_144},
		{1_000_000, 131_072, 78_643},
		{1_000_000, 32_768, 26_214},
		{1_000_000, 0, 32_000},
		{200_000, 1_048_576, 200_000},
		{40_000, 0, 32_000},
		{10_000_000, 2_000_000, 1_936_000},
	} {
		if got := wsEffectiveBudget(c.setting, c.window); got != c.want {
			t.Errorf("wsEffectiveBudget(%d, %d) = %d, want %d", c.setting, c.window, got, c.want)
		}
	}
	fx := newWsFix(t)
	s := fx.main(t)
	t.Cleanup(func() { wsBudgetSet.Store(0) })
	wsBudgetSet.Store(0)
	e := &Engine{ag: &Agent{noGateway: true}}
	if got, why := e.tokenBudget(context.Background(), Config{}); got != budgetFloor || why != "default" {
		t.Fatalf("no setting from xbind (an older one): %d %q", got, why)
	}
	fx.x.mu.Lock()
	fx.x.state.ContextBudget = 150_000
	fx.x.mu.Unlock()
	wsOffForget()
	_ = wsOffCached() // what a model call checks first: the state read, which carries the setting
	if wsBudgetSetting() != 150_000 {
		t.Fatalf("the setting xbind says: %d", wsBudgetSetting())
	}
	if got, why := (&Engine{ag: &Agent{}}).tokenBudget(context.Background(), Config{Model: s.Model}); got != wsEffectiveBudget(150_000, contextWindow(context.Background(), s.Model)) || why == "default" {
		t.Errorf("the compaction trigger under the setting: %d %q", got, why)
	}
	var conn struct {
		Budget struct{ Tokens, Window, Setting int } `json:"budget"`
	}
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/sessions/%d/connection", s.Run), nil), 200, &conn)
	if conn.Budget.Setting != 150_000 || conn.Budget.Tokens == 0 {
		t.Errorf("the scope panel's line: %+v", conn.Budget)
	}
}
