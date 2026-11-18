package server

// delegatebudget.go — the workspace agent's context budget as an admin
// sets it and as its person's agent reads it (D246; delegation/budget.go;
// plans/wsagent/access-and-status.md §3): PUT /delegate/workspace
// {contextBudget: tokens | null}, GET's contextBudget, policy.contextBudget
// and budget (what the agent role's model keeps under it), and the
// contextBudget of GET /delegate/state.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// webWorkspaceIn is the web part of PUT /delegate/workspace (D239).
type webWorkspaceIn struct {
	Mode   *string   `json:"mode"`
	Allow  *[]string `json:"allow"`
	Deny   *[]string `json:"deny"`
	Search *bool     `json:"search"`
}

// netWorkspacePut writes the web and MCP policy and the context budget of
// PUT /delegate/workspace (each part only when sent) — before the switch,
// so a refusal changes nothing. false: it wrote the refusal.
func (s *Server) netWorkspacePut(w http.ResponseWriter, p auth.Principal, web *webWorkspaceIn, mcp *mcpWorkspaceIn, budget json.RawMessage) bool {
	var n *int // the budget asked for; nil: back to the default
	if budget != nil && string(bytes.TrimSpace(budget)) != "null" {
		var v int
		if err := json.Unmarshal(budget, &v); err != nil {
			WriteError(w, http.StatusBadRequest, "contextBudget is a number of tokens, or null for the default", delegationDocs)
			return false
		}
		n = &v
	}
	if budget != nil { // checked before anything is written: a refused budget leaves the policy as it was
		if err := s.contextBudgetRefusal(n); err != nil {
			netWriteError(w, err)
			return false
		}
	}
	if web != nil || mcp != nil {
		var np delegation.NetPatch
		if web != nil {
			np.WebMode, np.WebAllow, np.WebDeny, np.WebSearch = web.Mode, web.Allow, web.Deny, web.Search
		}
		var err error
		if mcp != nil {
			np.MCPMode = mcp.Mode
			if np.MCPServers, err = s.mcpServersPatch(mcp.Servers); err != nil {
				netWriteError(w, err)
				return false
			}
		}
		if _, err := s.Delegation.SetNet(np, p.From()); err != nil {
			netWriteError(w, err)
			return false
		}
		if np.MCPServers != nil {
			s.SweepDelegation() // approvals of a server that left the list, or moved, go now
		}
	}
	if budget != nil {
		if _, err := s.Delegation.SetContextBudget(n, p.From()); err != nil {
			netWriteError(w, err)
			return false
		}
	}
	s.netPolicyChanged() // every person's agent reads its state again
	return true
}

// contextBudgetRefusal is what SetContextBudget would refuse of n, asked
// before anything is written.
func (s *Server) contextBudgetRefusal(n *int) error {
	if n == nil {
		return nil
	}
	if *n < delegation.MinContextBudget || *n > delegation.MaxContextBudget {
		return errors.New("contextBudget is 32000 to 10000000 tokens, or null for the default (1000000)")
	}
	if h := s.Delegation.Host().ContextBudget; h > 0 && *n > h {
		return &delegation.ErrHostLocked{Settings: []string{"contextBudget"}}
	}
	return nil
}

// budgetWorkspaceView is GET /delegate/workspace's context budget: the
// admin's own setting, and what the agent role's model keeps under the one
// in force — "this model: 256k window → keeps up to 198 144".
func (s *Server) budgetWorkspaceView(out map[string]any) {
	if ws := s.Delegation.Workspace(); ws.ContextBudget > 0 {
		out["contextBudget"] = ws.ContextBudget
	}
	inForce, byHost := s.Delegation.ContextBudget()
	view := map[string]any{"setting": inForce, "default": delegation.DefaultContextBudget, "margin": delegation.ContextMargin,
		"min": delegation.MinContextBudget, "max": delegation.MaxContextBudget}
	if byHost {
		view["byHost"] = true
	}
	if s.AgentModelWindow != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		model, window := s.AgentModelWindow(ctx)
		cancel()
		if model != "" {
			view["model"], view["window"] = model, window
		}
		view["keeps"] = delegation.EffectiveBudget(inForce, window)
	}
	out["budget"] = view
}
