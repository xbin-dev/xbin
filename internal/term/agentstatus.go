package term

// STATUS CHANGES of agent sessions — what an inbox needs (the native app's
// "Needs you", plans/native.md §20 row 8): which of a user's sessions is
// running, waiting for an answer, idle or gone, without following every
// session's log. The pump watches the events that can move the summary
// (status, permission and question requests and resolutions), coalesces a
// burst (a permission's resolution and the status that follows it are one
// change, not two) and hands the summary to Manager.OnStatus only when it
// differs from the last one sent — the server publishes it as a `term`
// event with op "status" beside open/close/rename (D73), so it reaches the
// owner's clients and admins like the rest of the directory's changes.

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// A GLANCE at an agent session — what a status poll shows beside the
// status (GET /term/sessions?compact=1, for the caller's own sessions):
// the prompts it took so far, and the last line it wrote. The line is kept
// as the log is written (noteSaid): the tail of the agent's own text since
// its last prompt — a subagent's text and its thoughts are not its answer —
// or, when the session failed, what the failure said.
const (
	saidKeep    = 1 << 10 // bytes of the agent's text kept for its last line
	lastLineMax = 300     // runes of the last line a glance gives
)

// noteSaid keeps what e adds to the agent's last line.
func (st *agentState) noteSaid(e agent.Event) {
	switch e.Type {
	case agent.EvMessageDelta:
		var d struct{ Role, Text, Parent string }
		if json.Unmarshal(e.Data, &d) != nil || d.Parent != "" {
			return
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		if d.Role == "user" { // a new prompt: what it wrote before is an earlier turn's
			st.said = st.said[:0]
			return
		}
		st.said = append(st.said, d.Text...)
		if len(st.said) > 2*saidKeep {
			st.said = append(st.said[:0], st.said[len(st.said)-saidKeep:]...)
		}
	case agent.EvStatus:
		var d struct{ Status, Detail string }
		if json.Unmarshal(e.Data, &d) != nil || d.Status != agent.StatusError || d.Detail == "" {
			return
		}
		st.mu.Lock()
		st.said = append(st.said[:0], d.Detail...)
		st.mu.Unlock()
	}
}

// lastLine is the last line of text that isn't blank, at most lastLineMax
// runes of it (its end, when it is longer).
func lastLine(text []byte) string {
	lines := strings.Split(strings.ToValidUTF8(string(text), ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if n := utf8.RuneCountInString(l); n > lastLineMax {
			l = "…" + string([]rune(l)[n-lastLineMax+1:])
		}
		return l
	}
	return ""
}

// AgentGlance is what a status poll shows of agent session id beside its
// status: the prompts it took so far and the last line it wrote ("" when
// it wrote none since its last prompt). ok false: no such agent session.
func (m *Manager) AgentGlance(id string) (turn uint64, last string, ok bool) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return 0, "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.turn, lastLine(st.said), true
}

// statusCoalesce folds the events of one change (a resolution, then the
// status it leads to) into one published summary.
const statusCoalesce = 40 * time.Millisecond

// StatusChange is an agent session's summary after a change — sdk/acp's
// Summary (the same numbers the agent template's lists show of a coding
// agent): its status (starting | idle | running | waiting_permission |
// cancelling | error | exited), how many permission requests and questions
// wait for an answer, and how many turns it took — so a turn that ran and
// ended between two summaries still shows (idle → idle with a higher turn).
type StatusChange struct {
	User string `json:"user"`
	ID   string `json:"id"`
	acp.Summary
}

// movesStatus reports whether a logged event can change the summary.
func movesStatus(typ string) bool {
	switch typ {
	case agent.EvStatus, agent.EvPermissionRequest, agent.EvPermissionResolved, agent.EvElicitRequest, agent.EvElicitResolved:
		return true
	}
	return false
}

// publishStatus hands the session's summary to OnStatus when it changed
// since the last one sent (the pump goroutine only).
func (s *Session) publishStatus(m *Manager) {
	st := s.agent
	k := acp.Summary{Pending: st.perms.Count(), Questions: len(st.drv.PendingElicitations())}
	st.mu.Lock()
	k.Status, k.Turn = st.status, st.turn
	same := st.published == k
	st.published = k
	st.mu.Unlock()
	if same || m.OnStatus == nil {
		return
	}
	m.OnStatus(s.Cwd, StatusChange{User: s.homeKey, ID: s.ID, Summary: k})
}
