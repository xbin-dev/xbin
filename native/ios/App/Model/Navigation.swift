import Foundation
import Observation
import XbinCore

// A window's navigation in one workspace (plans/native.md §15: one surface
// full screen; lists are overlays). Windows are tabs: every window has its
// own WorkspaceNav per workspace (SceneModel keeps them), over the state the
// workspace shares (session, catalog, sockets: WorkspaceModel).
//
// A screen acts on its own window's nav — `@Environment(WorkspaceNav.self)`,
// handed to the models it creates — never on "the focused window": a tile
// page in a background window (Stage Manager) that calls `xbin.window` after
// a fetch pushes onto its own window, not over the tile in front.
//
// Foundation and Observation only: native/tools/app-check compiles this file
// on Linux and tests it (NavigationTests).

/// What a workspace shows full screen.
enum Surface: Hashable, Codable {
    case tile(String, sub: String = "", fragment: String? = nil)
    case terminal(cwd: String, session: String?)
    case agent(cwd: String?, session: String?)

    var title: String {
        switch self {
        case .tile(let t, _, _): return TileInfo.humanize(t)
        case .terminal(let cwd, _): return "Terminal · \(TileInfo.humanize(cwd))"
        case .agent(let cwd, _): return "Agent" + (cwd.map { " · \(TileInfo.humanize($0))" } ?? "")
        }
    }
}

/// A screen pushed over a tile: an `xbin.window` (another sub-path of the
/// tile, or another tile), opened by the tile's request `replyID`.
struct PushedWindow: Hashable {
    var fromTile: String
    var target: String
    var title: String
    var replyID: String
}

/// What a window shows: a workspace and, optionally, a surface in it. The
/// value `openWindow(value:)` opens a window with (WindowGroup(for:)), what
/// a dragged tile or a Handoff carries, and what `@SceneStorage` keeps.
struct WindowTarget: Codable, Hashable {
    var workspace: String
    var surface: Surface?

    /// For `@SceneStorage` (strings survive every restore).
    var encoded: String {
        guard let d = try? JSONEncoder().encode(self) else { return "" }
        return String(decoding: d, as: UTF8.self)
    }

    init(workspace: String, surface: Surface? = nil) {
        self.workspace = workspace
        self.surface = surface
    }

    init?(encoded s: String) {
        guard !s.isEmpty, let t = try? JSONDecoder().decode(WindowTarget.self, from: Data(s.utf8)) else { return nil }
        self = t
    }

    /// What a window restores after a launch: its place — or, when the app
    /// last ended in the foreground (a crash, or the watchdog killing a
    /// hang, possibly in a native tile mounting on restore: the case the
    /// kill switch exists for, plans/native.md §23), just its workspace, so
    /// the remote switch can land before anything mounts again.
    func restoring(afterUncleanExit unclean: Bool) -> WindowTarget {
        unclean ? WindowTarget(workspace: workspace) : self
    }
}

/// One window's navigation in one workspace.
@MainActor
@Observable
final class WorkspaceNav {
    let workspaceID: String
    /// The full-screen surface (nil: the navigator is the home).
    var surface: Surface?
    /// Windows pushed over the current tile (`xbin.window`).
    var windows: [PushedWindow] = []
    /// The navigator overlay is up.
    var showNavigator = false

    init(workspaceID: String) {
        self.workspaceID = workspaceID
    }

    func open(_ s: Surface) {
        windows = []
        surface = s
        showNavigator = false
    }

    /// A tile's `xbin.window`: a screen pushed over this window's tile.
    func push(_ w: PushedWindow) { windows.append(w) }

    /// The pushed window a tile opened as `replyID` closes (it asked to).
    func close(replyID: String) { windows.removeAll { $0.replyID == replyID } }

    /// An agent launcher on `cwd` created session `session`: this window
    /// now shows that session — if it still shows the launcher (the user
    /// may have moved on while it started). True when it did.
    @discardableResult
    func started(session: String, cwd: String) -> Bool {
        guard case .agent(let c, .none)? = surface, c == cwd else { return false }
        surface = .agent(cwd: cwd, session: session)
        return true
    }
}
