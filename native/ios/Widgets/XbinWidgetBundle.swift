import SwiftUI
import WidgetKit

/// The widget extension: the agent turn's Live Activity (lock screen and
/// Dynamic Island). No home-screen widgets yet.
@main
struct XbinWidgetBundle: WidgetBundle {
    var body: some Widget {
        AgentActivityWidget()
    }
}
