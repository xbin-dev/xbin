// ws_budget.go — how much of a conversation a workspace agent keeps
// (D246; xbin's delegation/budget.go, plans/wsagent/access-and-status.md
// §3): the workspace's setting (GET /delegate/state's contextBudget,
// 1 000 000 unless an admin or the host set another), applied as
// min(setting, what the model's window allows) — the larger of the rule
// before the setting (compact.go's budgetForWindow: 60 % of the window, at
// least 32 000, never above 80 % of it) and the window less a margin of
// 64 000 tokens; 32 000 for an unknown window, as before. The margin:
// compaction is judged before each model call from the size the provider
// reported for the previous one, so the next call carries one step's
// growth — the reply and its tools' results, each capped at a few thousand
// tokens here — and needs room for the next reply. xbind's
// delegation.EffectiveBudget holds the same numbers (TestEffectiveBudget
// there, TestWsContextBudget here: the same vectors).
//
// It is not a spend limit: the connector's metering and quotas are.
package main

import (
	"fmt"
	"sync/atomic"
)

// wsBudgetMargin is what the budget stays below a known window.
const wsBudgetMargin = 64000

// wsBudgetSet is the workspace's setting as xbind last said it (0: an
// xbind before it — the window-derived rule alone).
var wsBudgetSet atomic.Int64

// wsBudgetSetting is the setting in force for this process: 0 outside
// workspace mode, or before xbind said one.
func wsBudgetSetting() int {
	if !wsMode() {
		return 0
	}
	return int(wsBudgetSet.Load())
}

// wsEffectiveBudget is what a conversation keeps under setting on a model
// whose window is window tokens (0: unknown).
func wsEffectiveBudget(setting, window int) int {
	if window <= 0 {
		return min(setting, budgetFloor)
	}
	return min(setting, max(budgetForWindow(window), window-wsBudgetMargin))
}

// wsBudgetFrom says where a workspace agent's budget came from, for the
// compaction's journal.
func wsBudgetFrom(setting, window int, model string) string {
	if window <= 0 {
		return fmt.Sprintf("the workspace's setting (%d), under %s's window, which its provider doesn't say", setting, bareModel(model))
	}
	return fmt.Sprintf("the workspace's setting (%d), under %s's %d-token window less %d", setting, bareModel(model), window, wsBudgetMargin)
}
