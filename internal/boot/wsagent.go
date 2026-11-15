package boot

import "github.com/xbin-dev/xbin/internal/sealed"

// wsagent.go — what boot tells the server about the workspace agent
// (xbin/wsagent; D231 × D227), beside which component it is
// (Server.AgentHolder, the table's row).

// agentHeld is Server.AgentHeld: why the agent can't run right now, in the
// kinds xbin's chrome has words for. A hold of the sealed component is the
// table's own (hold.go): its data written by a newer release is "older"
// (this xbin is the older one); everything else — no build in this
// install, no sandboxes, data it doesn't own at its key — is
// "unavailable", with the hold's reason. A locked vault holds nothing of
// the component, but no person's partition can open: "locked".
func (st *State) agentHeld() (kind, why string) {
	h := st.Sealed.Agent()
	if h == "" {
		return "", ""
	}
	if hold, ok := st.Sealed.Hold(h); ok {
		if hold.Code == sealed.HoldNewerData {
			return "older", hold.Reason
		}
		return "unavailable", hold.Reason
	}
	if st.Broker != nil && st.Broker.VaultLocked() {
		return "locked", "the workspace's vault is locked: people's data can't be opened until an admin unlocks it"
	}
	return "", ""
}
