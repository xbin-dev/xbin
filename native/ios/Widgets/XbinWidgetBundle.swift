import SwiftUI
import WidgetKit

/// The widget extension: the agent turn's Live Activity and the workspace
/// agent's card (lock screen and Dynamic Island; D241). No home-screen
/// widgets yet.
@main
struct XbinWidgetBundle: WidgetBundle {
    var body: some Widget {
        AgentActivityWidget()
        WorkspaceAgentActivityWidget()
    }
}
