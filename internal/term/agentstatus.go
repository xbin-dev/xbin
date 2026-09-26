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
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// statusCoalesce folds the events of one change (a resolution, then the
// status it leads to) into one published summary.
const statusCoalesce = 40 * time.Millisecond

// StatusChange is an agent session's summary after a change: its status
// (starting | idle | running | waiting_permission | cancelling | error |
// exited), how many permission requests and questions wait for an answer,
// and how many turns it took — so a turn that ran and ended between two
// summaries still shows (idle → idle with a higher turn).
type StatusChange struct {
	User      string `json:"user"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	Pending   int    `json:"pending"`   // unanswered permission requests
	Questions int    `json:"questions"` // unanswered questions (elicitation.request)
	Turn      uint64 `json:"turn"`      // prompts taken so far (POST …/prompt's {turn})
}

// statusKey is what makes two summaries different.
type statusKey struct {
	status             string
	pending, questions int
	turn               uint64
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
	k := statusKey{pending: st.perms.Count(), questions: len(st.drv.PendingElicitations())}
	st.mu.Lock()
	k.status, k.turn = st.status, st.turn
	same := st.published == k
	st.published = k
	st.mu.Unlock()
	if same || m.OnStatus == nil {
		return
	}
	m.OnStatus(s.Cwd, StatusChange{User: s.homeKey, ID: s.ID, Status: k.status, Pending: k.pending, Questions: k.questions, Turn: k.turn})
}
