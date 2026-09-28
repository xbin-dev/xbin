import Foundation
import Observation
import XbinCore

// A window's navigation in one workspace (plans/native.md §4, §15; D125):
// panels you swipe between — Home (level 0: the workspace's screens) →
// a Screen (1: its tiles as cards) → a tile, terminal or agent (2: full
// screen) — one PanelStack per window. Back is a left-edge swipe (or the
// bar's Home / ‹ screen); forward, a right-edge swipe, is there only right
// after a back, to the panel just left, and any new navigation forgets it.
// Windows are tabs: every window has its own WorkspaceNav per workspace
// (SceneModel keeps them), over the state the workspace shares (session,
// catalog, sockets: WorkspaceModel).
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
    /// A tile just created on the phone: "What should this tile be?" —
    /// an agent to build it, or a terminal (BuildChooser).
    case build(tile: String)
    /// A tile's sessions and tools (D132): its terminals and agents as
    /// tabs, the launcher, live reload and deployments — the web terminal
    /// window's counterpart (TileWorkspaceScreen).
    case sessions(tile: String, show: SessionsFocus)

    var title: String {
        switch self {
        case .tile(let t, _, _), .build(let t): return TileInfo.humanize(t)
        case .terminal(let cwd, _): return "Terminal · \(TileInfo.humanize(cwd))"
        case .agent(let cwd, _): return "Agent" + (cwd.map { " · \(TileInfo.humanize($0))" } ?? "")
        case .sessions(let t, _): return "Sessions · \(TileInfo.humanize(t))"
        }
    }

    /// The tile it is about (a terminal's or agent's directory), if any.
    var tilePath: String? {
        switch self {
        case .tile(let t, _, _), .build(let t), .sessions(let t, _): return t
        case .terminal(let cwd, _): return cwd
        case .agent(let cwd, _): return cwd
        }
    }
}

/// What a tile's sessions screen shows first (D132).
enum SessionsFocus: Hashable, Codable {
    /// The tile's first session, or the launcher when it has none.
    case first
    /// The launcher (New session…, the `+`).
    case launcher
    /// A session's tab (the long press's list).
    case session(String)
    /// A tool: live reload and deployments.
    case deployments
}

/// One panel of a window's stack.
enum Panel: Hashable, Codable {
    case home
    case screen(String)
    case surface(Surface)

    var level: Int {
        switch self {
        case .home: return 0
        case .screen: return 1
        case .surface: return 2
        }
    }
}

/// A panel in the stack, with an identity of its own: a panel changed in
/// place (an agent launcher that started its session) stays the same view
/// in the stack — it doesn't slide out and in again.
struct PanelEntry: Hashable, Identifiable {
    let id: Int
    var panel: Panel
}

/// A screen pushed over a tile: an `xbin.window` (another sub-path of the
/// tile, or another tile), opened by the tile's request `replyID`.
struct PushedWindow: Hashable {
    var fromTile: String
    var target: String
    var title: String
    var replyID: String
}

/// What a window shows: a workspace and, optionally, a screen and a
/// surface in it. The value `openWindow(value:)` opens a window with
/// (WindowGroup(for:)), what a dragged tile or a Handoff carries, and what
/// `@SceneStorage` keeps.
struct WindowTarget: Codable, Hashable {
    var workspace: String
    /// The screen (level 1) under the surface, or shown (nil: Home).
    var screen: String?
    var surface: Surface?

    /// For `@SceneStorage` (strings survive every restore).
    var encoded: String {
        guard let d = try? JSONEncoder().encode(self) else { return "" }
        return String(decoding: d, as: UTF8.self)
    }

    init(workspace: String, screen: String? = nil, surface: Surface? = nil) {
        self.workspace = workspace
        self.screen = screen
        self.surface = surface
    }

    init?(encoded s: String) {
        guard !s.isEmpty, let t = try? JSONDecoder().decode(WindowTarget.self, from: Data(s.utf8)) else { return nil }
        self = t
    }

    /// What a window restores after a launch: its place — or, when the app
    /// last ended in the foreground (a crash, or the watchdog killing a
    /// hang, possibly in a native tile mounting on restore: the case the
    /// kill switch exists for, plans/native.md §23), its workspace and
    /// screen only, so the remote switch can land before anything mounts
    /// again.
    func restoring(afterUncleanExit unclean: Bool) -> WindowTarget {
        unclean ? WindowTarget(workspace: workspace, screen: screen) : self
    }
}

/// One window's navigation in one workspace.
@MainActor
@Observable
final class WorkspaceNav {
    let workspaceID: String
    /// Every panel: the ones shown (the first `depth`) and, after them, the
    /// ones a back left — the forward memory.
    private(set) var entries: [PanelEntry]
    /// How many panels are shown; the top one is current.
    private(set) var depth = 1
    /// Windows pushed over the current tile (`xbin.window`).
    var windows: [PushedWindow] = []
    /// Runs a change of panels — the window's PanelStack animates it (a
    /// panel slides in or out). Unset: at once.
    @ObservationIgnored var animate: ((() -> Void) -> Void)?
    @ObservationIgnored private var serial = 0

    private func animated(_ change: () -> Void) {
        if let animate { animate(change) } else { change() }
    }

    init(workspaceID: String) {
        self.workspaceID = workspaceID
        entries = [PanelEntry(id: 0, panel: .home)]
    }

    var current: Panel { entries[depth - 1].panel }
    var level: Int { current.level }
    /// The surface shown full screen (level 2), if one is.
    var surface: Surface? {
        if case .surface(let s) = current { return s }
        return nil
    }
    /// The screen shown or under the surface.
    var screenID: String? {
        for e in entries[..<depth].reversed() { if case .screen(let id) = e.panel { return id } }
        return nil
    }
    var canGoBack: Bool { depth > 1 }
    var canGoForward: Bool { depth < entries.count }
    /// The panel a back would show.
    var below: Panel? { depth > 1 ? entries[depth - 2].panel : nil }
    /// The panel a forward would show again.
    var ahead: Panel? { canGoForward ? entries[depth].panel : nil }

    private func entry(_ p: Panel) -> PanelEntry {
        serial += 1
        return PanelEntry(id: serial, panel: p)
    }

    /// New navigation to `panels` (Home first, implied): the forward memory
    /// goes, and panels already there (the same place) keep their views —
    /// reopening the tile a back just left is the forward swipe.
    private func show(_ panels: [Panel]) {
        let want = [Panel.home] + panels
        var next: [PanelEntry] = []
        var shared = true
        for (i, p) in want.enumerated() {
            if shared, i < entries.count, entries[i].panel == p {
                next.append(entries[i])
            } else {
                shared = false
                next.append(entry(p))
            }
        }
        // Another panel on top: the windows pushed over the old one go.
        if next[next.count - 1].id != entries[depth - 1].id { windows = [] }
        entries = next
        depth = next.count
    }

    /// Opens `s` full screen over `screen` (nil: over Home). Where back
    /// goes is the caller's to know (WorkspaceModel: the screen the tile
    /// sits on, `screenFor`).
    func open(_ s: Surface, on screen: String?) {
        animated { show((screen.map { [Panel.screen($0)] } ?? []) + [.surface(s)]) }
    }

    /// Opens `s` over the screen shown now (or under the surface shown).
    func open(_ s: Surface) { open(s, on: screenID) }

    /// Shows screen `id` (level 1).
    func openScreen(_ id: String) { animated { show([.screen(id)]) } }

    /// Home, as new navigation (a link to the workspace itself).
    func goHome() { animated { show([]) } }

    /// Back one panel; what it left is the forward memory.
    @discardableResult
    func back() -> Bool {
        guard depth > 1 else { return false }
        if surface != nil { windows = [] }
        animated { depth -= 1 }
        return true
    }

    /// Forward to the panel a back left.
    @discardableResult
    func forward() -> Bool {
        guard canGoForward else { return false }
        animated { depth += 1 }
        return true
    }

    /// The surface shown becomes `s` in place (the build chooser handing
    /// over to the agent it started): same panel, no forward memory.
    func replace(with s: Surface) {
        guard surface != nil else { open(s); return }
        animated {
            entries[depth - 1].panel = .surface(s)
            entries.removeSubrange(depth...)
        }
        windows = []
    }

    /// A window's restored place.
    func restore(screen: String?, surface: Surface?) {
        show((screen.map { [Panel.screen($0)] } ?? []) + (surface.map { [Panel.surface($0)] } ?? []))
    }

    /// The screen back goes to from `s`: the screen the tile sits on
    /// (`containing`: Home's lookup, preferring `current`); a terminal or
    /// agent on a tile no screen has stays over the current screen; a tile
    /// no screen has goes back to Home.
    static func screen(for s: Surface, current: String?, containing: (String, String?) -> String?) -> String? {
        let found = s.tilePath.flatMap { containing($0, current) }
        switch s {
        case .tile, .build: return found
        case .terminal, .agent, .sessions: return found ?? current
        }
    }

    /// A tile's `xbin.window`: a screen pushed over this window's tile.
    func push(_ w: PushedWindow) { windows.append(w) }

    /// The pushed window a tile opened as `replyID` closes (it asked to).
    func close(replyID: String) { windows.removeAll { $0.replyID == replyID } }

    /// Whether a screen of this window that just disappeared is only
    /// covered — a window pushed over it, so it shows again on the pop —
    /// rather than gone. SwiftUI calls onDisappear for both; a covered
    /// screen keeps its page or runtime (tearing it down left the tile dead
    /// after the pop, and told an opener its window closed while it was
    /// only covered), a gone one ends it. `window`: the replyID of the
    /// pushed window the screen shows; nil for the window's surface (the
    /// stack's root).
    func stillStacked(window replyID: String?) -> Bool {
        guard let replyID else { return !windows.isEmpty }
        return windows.contains { $0.replyID == replyID }
    }

    /// An agent launcher on `cwd` created session `session`: this window
    /// now shows that session — if it still shows the launcher (the user
    /// may have moved on while it started). True when it did.
    @discardableResult
    func started(session: String, cwd: String) -> Bool {
        guard case .agent(let c, .none)? = surface, c == cwd else { return false }
        entries[depth - 1].panel = .surface(.agent(cwd: cwd, session: session))
        return true
    }
}
