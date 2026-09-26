package term

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// RestartAgent ends an agent session and opens it again with other sandbox
// pickers — network scope, tile-API access, GPU are fixed when a sandbox
// starts, exactly as for a shell. Unlike a shell's, the conversation is worth
// keeping: where the agent can reopen its own session (loadSession, D75) the
// new one resumes it — the agent replays the turns, then continues — else it
// starts fresh and the old transcript stays under Recent sessions. The same
// tile, provider, current mode, current settings (model, effort, …) and name
// carry over. Only the session's creator may: the new one mounts the
// caller's $HOME and resumes from the caller's history. resumed reports
// which it was.
func (m *Manager) RestartAgent(p auth.Principal, id, net, gpu string, api bool, vm *bool) (info SessionInfo, resumed bool, code int, err error) {
	s, st, err := m.agentOf(id)
	if err != nil {
		return SessionInfo{}, false, 404, err
	}
	if s.homeKey != HomeKey(p) {
		return SessionInfo{}, false, 403, fmt.Errorf("%w: only the session's creator can restart it", ErrForbidden)
	}
	st.mu.Lock()
	mode, provider, reopened := st.mode, st.provider.ID, st.resumed
	st.mu.Unlock()
	s.mu.Lock()
	name, inVM := s.name, s.vm
	s.mu.Unlock()
	if vm != nil {
		inVM = *vm // nil keeps the session's sandbox kind
	}
	options := currentOptions(st.log)

	s.kill()
	select { // history saved, the tile's layer released, the row gone
	case <-st.gone:
	case <-time.After(15 * time.Second):
		return SessionInfo{}, false, 500, errors.New("the agent session did not end in time — try again")
	}
	// what to resume: this session's own transcript — or, when it took no
	// prompt (never saved), the past session it had itself reopened
	resume := ""
	for _, cand := range []string{id, reopened} {
		if meta, _, err := m.ReadHistory(HomeKey(p), cand); cand != "" && err == nil && meta.Loadable && meta.ACPSessionID != "" {
			resume = cand
			break
		}
	}
	info, code, err = m.OpenAgentWith(p, AgentOpen{Cwd: s.Cwd, Net: net, GPU: gpu, NoAPI: !api, VM: inVM,
		Provider: provider, Mode: mode, Name: name, Resume: resume, Options: options})
	return info, resume != "", code, err
}

// currentOptions is the agent's settings as its last status reported them
// (id → current value) — re-applied after the restart's session/new or load.
func currentOptions(log *agent.Log) map[string]string {
	evs, _ := log.Since(0)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type != agent.EvStatus {
			continue
		}
		var d struct {
			Options []struct {
				ID           string `json:"id"`
				CurrentValue string `json:"currentValue"`
			} `json:"options"`
		}
		if json.Unmarshal(evs[i].Data, &d) != nil || len(d.Options) == 0 {
			continue
		}
		out := map[string]string{}
		for _, o := range d.Options {
			if o.ID != "" && o.CurrentValue != "" && o.ID != "mode" {
				out[o.ID] = o.CurrentValue
			}
		}
		return out
	}
	return nil
}
