import ActivityKit
import XbinAgent

// The ActivityKit side of XbinAgent's WorkspaceAgentActivityAttributes (the
// workspace agent's card, D241), compiled into the app and the widget
// extension as AgentActivityAttributes' is. Its name is what a
// push-to-start names ("attributes-type": "WorkspaceAgentActivityAttributes",
// relay/liveactivity.go).
extension WorkspaceAgentActivityAttributes: ActivityAttributes {}
