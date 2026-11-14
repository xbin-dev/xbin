package term

// delegate.go — the mark on a tile agent session that a person's workspace
// agent opened for them (D229): SessionInfo.Origin and the history entry's
// Origin read "agent", and the session keeps the component that opened it.
//
// This file holds only the names the session directory needs. Opening such
// a session — who may, with which mode and pickers, and setting the mark —
// is the delegation package's (branch wsagent/s2-delegation), whose file
// of this name replaces this one whole: until it lands nothing sets a
// session's origin, and every row reads as its person's own.

// OriginAgent is SessionInfo.Origin of a session a person's workspace agent
// opened for them.
const OriginAgent = "agent"

func originOf(holder string) string {
	if holder != "" {
		return OriginAgent
	}
	return ""
}
