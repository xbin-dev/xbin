package delegation

// budget.go — how much of a conversation a person's workspace agent keeps
// sending its model (D246; plans/wsagent/access-and-status.md §3): a
// workspace setting beside the agent's web and MCP policy, which the host's
// file may preset as a ceiling. The owner's decision: "keep at 1M default,
// models can deal with that, cost is fine".
//
// The agent applies min(setting, cap(window)) (its compaction trigger,
// the template's compact.go, which holds the same numbers): cap is the
// larger of the rule before this setting — 60 % of the model's window, at
// least 32 000 tokens, never above 80 % of it — and the window less a
// margin of 64 000 tokens. Compaction is judged before each model call
// from the size the provider reported for the previous one, so the next
// call carries what one step added since — the model's reply and the
// results of the tools it called (each of the workspace agent's capped at
// a few thousand tokens) — and room for the next reply: 64 000 covers a
// step of a dozen tool calls and a long answer, and it doesn't grow with
// the window. Below a 160 000-token window 60 % is the smaller cap, so
// nothing changes there. An unknown window keeps today's 32 000.
//
// It is not a spend limit: per-person metering and quotas (the LLM
// connector's) bound what the agent spends.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// DefaultContextBudget is the setting where nobody set one.
	DefaultContextBudget = 1_000_000
	// MinContextBudget and MaxContextBudget bound what may be set.
	MinContextBudget = 32_000
	MaxContextBudget = 10_000_000
	// ContextMargin is what the budget stays below a known window.
	ContextMargin = 64_000
	contextShare  = 60     // % of the window: the rule before the setting
	contextFloor  = 32_000 // …its floor, and the budget for an unknown window
)

// EffectiveBudget is what a conversation keeps (tokens) under setting on a
// model whose context window is window tokens (0: unknown).
func EffectiveBudget(setting, window int) int {
	if setting <= 0 {
		setting = DefaultContextBudget
	}
	if window <= 0 {
		return min(setting, contextFloor)
	}
	share := window * contextShare / 100
	if share < contextFloor {
		share = min(contextFloor, window*80/100)
	}
	return min(setting, max(share, window-ContextMargin))
}

// ContextBudget is the setting in force: the admin's, else the host's
// preset, else DefaultContextBudget — never above the host's (byHost: the
// host's file set what is in force).
func (s *Store) ContextBudget() (budget int, byHost bool) {
	return budgetInForce(s.Workspace(), s.Host())
}

func budgetInForce(ws Workspace, h HostNet) (budget int, byHost bool) {
	host, admin := h.ContextBudget, ws.ContextBudget
	switch {
	case admin > 0 && (host <= 0 || admin <= host):
		return admin, false
	case host > 0:
		return host, true
	}
	return DefaultContextBudget, false
}

// SetContextBudget writes the admin's setting: n tokens, or nil for the
// default again. Past the host's ceiling it is refused (ErrHostLocked),
// out of range too.
func (s *Store) SetContextBudget(n *int, by string) (Workspace, error) {
	if s == nil {
		return Workspace{}, ErrNoStore
	}
	if n != nil && (*n < MinContextBudget || *n > MaxContextBudget) {
		return Workspace{}, fmt.Errorf("contextBudget is %d to %d tokens (or null for the default, %d)", MinContextBudget, MaxContextBudget, DefaultContextBudget)
	}
	if h := s.Host().ContextBudget; h > 0 && n != nil && *n > h {
		return Workspace{}, &ErrHostLocked{Settings: []string{"contextBudget"}}
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	w, err := load(&s.ws, filepath.Join(s.dir, "workspace.json"))
	if err != nil {
		w = Workspace{}
	}
	w.ContextBudget = 0
	if n != nil {
		w.ContextBudget = *n
	}
	w.BudgetBy, w.BudgetAt = by, s.now().UTC().Truncate(time.Second)
	return w, save(&s.ws, filepath.Join(s.dir, "workspace.json"), w)
}

// budgetLock is the host's preset as a held setting: its ceiling.
func budgetLock(h HostNet) (HostLock, bool) {
	if h.ContextBudget <= 0 {
		return HostLock{}, false
	}
	return HostLock{Locked: true, Reason: HostLockReason, Ceiling: strconv.Itoa(h.ContextBudget)}, true
}
