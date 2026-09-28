package term

// Long conversations (D130): pages of an agent session's log, a command's
// output coalesced like the agent's text, and a resume's replay published
// as one event instead of hundreds.

import (
	"encoding/json"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// AgentPage is one page of a live session's log (agent.PageOf): the events
// before seq `before` (0 = the tail), about `limit` of them, cut where the
// clients' folds allow. The tail page carries the requests waiting now.
func (m *Manager) AgentPage(id string, before uint64, limit int) (agent.Page, error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return agent.Page{}, err
	}
	p := st.log.Page(before, limit, st.snap.Open())
	if before == 0 {
		p.State.Permissions = st.perms.List()
		p.State.Elicitations = st.drv.PendingElicitations()
	}
	return p, nil
}

// outRun is a tool call's output chunks being coalesced: a tool.update that
// carries nothing but outputDelta (and the subagent it runs under).
type outRun struct {
	ID          string `json:"id"`
	OutputDelta string `json:"outputDelta"`
	Parent      string `json:"parent,omitempty"`
}

// outputChunk reports whether e is a bare output chunk — merging two of the
// same call appends their text, exactly what a fold does with the two.
func outputChunk(e agent.Event) (outRun, bool) {
	if e.Type != agent.EvToolUpdate {
		return outRun{}, false
	}
	var d map[string]json.RawMessage
	if json.Unmarshal(e.Data, &d) != nil || len(d["outputDelta"]) == 0 || len(d["id"]) == 0 {
		return outRun{}, false
	}
	for k := range d {
		if k != "id" && k != "outputDelta" && k != "parent" {
			return outRun{}, false
		}
	}
	var o outRun
	if json.Unmarshal(e.Data, &o) != nil || o.ID == "" || o.OutputDelta == "" {
		return outRun{}, false
	}
	return o, true
}

// replayState is a resumed session's replay: session/load streams the
// earlier turns back before the handshake ends. They are logged like any
// event, but the hub gets one `replayed` event when the replay is over
// (the first status that is not `starting`) instead of each — a replay of
// hundreds would evict every /ws/events subscriber (a 64-event buffer),
// and replayed prompts are not new ones (push, Live Activities).
type replayState struct {
	on          bool
	first, last uint64 // the replayed events held back
}

// replayed reports whether ev is held back from the hub (the replay is on),
// and ends the replay at the status that closes it, publishing `replayed`
// before that status goes out.
func (s *Session) replayed(m *Manager, ev agent.Event) bool {
	st := s.agent
	st.mu.Lock()
	if !st.replay.on {
		st.mu.Unlock()
		return false
	}
	if ev.Type != agent.EvStatus {
		if st.replay.first == 0 {
			st.replay.first = ev.Seq
		}
		st.replay.last = ev.Seq
		st.mu.Unlock()
		return true
	}
	var d struct{ Status string }
	_ = json.Unmarshal(ev.Data, &d)
	st.mu.Unlock()
	if d.Status != "" && d.Status != agent.StatusStarting {
		s.endReplay(m)
	}
	return false
}

// endReplay ends a replay: one `replayed` event on the hub ({first, last}:
// the seqs held back; the log has them), when anything was held back.
func (s *Session) endReplay(m *Manager) {
	st := s.agent
	st.mu.Lock()
	r := st.replay
	st.replay = replayState{}
	st.mu.Unlock()
	if !r.on || r.first == 0 || m.OnEvent == nil {
		return
	}
	data, _ := json.Marshal(map[string]uint64{"first": r.first, "last": r.last})
	m.OnEvent(s.Cwd, SessionEvent{Event: agent.Event{TS: time.Now().UnixMilli(), Type: agent.EvReplayed, Data: data}, User: s.homeKey, ID: s.ID})
}
