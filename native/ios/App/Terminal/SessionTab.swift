import SwiftUI

/// What hosts terminal and agent views as tabs: a tile's sessions screen
/// (D132, TileWorkspaceScreen's model).
@MainActor
protocol SessionTabHost: AnyObject {
    /// The tab's view shows session `id` (a new shell's arrives with its
    /// session frame; a shell restarted in place gets another).
    func tab(_ key: String, showsSession id: String)
    /// The tab's view's own title changed (a shell's OSC title, an agent
    /// session's title).
    func tab(_ key: String, title: String)
}

/// A terminal or agent hosted as a tab of a tile's sessions screen rather
/// than as a screen of its own. The screen hands the view one in the
/// environment (`\.sessionTab`); the view tells the screen what it shows.
/// Hidden tabs stay mounted, like the web window's hidden panes; the tab in
/// front gets `\.panelActive` (the keyboard, VoiceOver) and puts its items
/// in the bar.
@MainActor
final class SessionTabLink {
    let key: String
    weak var host: (any SessionTabHost)?

    init(key: String, host: any SessionTabHost) {
        self.key = key
        self.host = host
    }

    func shows(session id: String) { host?.tab(key, showsSession: id) }
    func titled(_ title: String) { host?.tab(key, title: title) }
}

private struct SessionTabKey: EnvironmentKey {
    static let defaultValue: SessionTabLink? = nil
}

extension EnvironmentValues {
    /// The tab a terminal or agent view is hosted as (nil: a screen of its
    /// own, as before).
    var sessionTab: SessionTabLink? {
        get { self[SessionTabKey.self] }
        set { self[SessionTabKey.self] = newValue }
    }
}

/// A terminal's or agent's title: the navigation title of a screen of its
/// own; hosted as a tab, told to the sessions screen instead, which shows
/// the title of the tab in front (every mounted tab setting one would
/// fight over the bar).
struct SessionTitle: ViewModifier {
    let title: String
    @Environment(\.sessionTab) private var tab

    func body(content: Content) -> some View {
        if let tab {
            content.onChange(of: title, initial: true) { _, t in tab.titled(t) }
        } else {
            content.navigationTitle(Text(verbatim: title))
        }
    }
}
