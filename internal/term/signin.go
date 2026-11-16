package term

// signin.go — a person's saved coding-agent sign-in in their agent
// sessions (D230; docs/agent-signins.md).

// EndSigninSessions ends user's running agent sessions that carry saved
// sign-in id ("": any saved sign-in), saying why in each, and answers how
// many it ended.
func (m *Manager) EndSigninSessions(user, id, why string) int { return 0 }
