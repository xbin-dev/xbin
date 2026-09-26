import Foundation
import Observation
import XbinCore

/// One window (scene): which workspace it shows, its navigation there, and
/// its sheets. The app's shared state — workspaces, sessions, sockets — is
/// in AppModel and WorkspaceModel.
@MainActor
@Observable
final class SceneModel: Identifiable {
    let id = UUID()
    var selectedID: String?
    var showSwitcher = false
    var addRequest: AddRequest?
    var showInbox = false
    var showSettings = false
    /// The window is on screen (foreground active or inactive): its
    /// workspace keeps an events socket.
    var isForeground = false
    /// Navigation per workspace, created on first use.
    @ObservationIgnored private var navs: [String: WorkspaceNav] = [:]

    init(selectedID: String? = nil) {
        self.selectedID = selectedID
    }

    private var app: AppModel { AppModel.shared }

    var selected: WorkspaceModel? {
        guard let id = selectedID else { return nil }
        return app.workspace(id)
    }

    func nav(for w: WorkspaceModel) -> WorkspaceNav { nav(forID: w.id) }

    func nav(forID id: String) -> WorkspaceNav {
        if let n = navs[id] { return n }
        let n = WorkspaceNav(workspaceID: id)
        navs[id] = n
        return n
    }

    /// The nav if one exists (no side effects: safe inside `body`).
    func existingNav(_ id: String) -> WorkspaceNav? { navs[id] }

    /// What this window shows now (the scene's storage and Handoff).
    var current: WindowTarget? {
        guard let id = selectedID else { return nil }
        return WindowTarget(workspace: id, surface: navs[id]?.surface)
    }

    func select(_ id: String) {
        guard let w = app.workspace(id) else { return }
        selectedID = id
        showSwitcher = false
        app.selectedInScene(id)
        if w.whoami == nil { Task { await w.refresh() } }
    }

    /// ⌘1…⌘9.
    func select(index: Int) {
        guard app.workspaces.indices.contains(index) else { return }
        select(app.workspaces[index].id)
    }

    /// Shows `target` (a new window's value, a restore, a Handoff).
    func show(_ target: WindowTarget) {
        guard let w = app.workspace(target.workspace) else { return }
        select(w.id)
        if let s = target.surface { w.open(s, in: nav(for: w)) }
    }

    /// `xbin://…` in this window.
    func open(url: URL) {
        guard let link = try? DeepLink(url: url) else { return }
        open(link: link)
    }

    func open(link: DeepLink) {
        switch link {
        case .enroll(let server, let code):
            addRequest = .enroll(server: server, code: code)
        case .sso, .ssoError:
            // The web authentication session delivers these to its caller.
            break
        default:
            guard let w = app.resolve(link) else { app.holdLink(link); return }
            select(w.id)
            w.open(link: link, in: nav(for: w))
        }
    }

    /// Workspace removed: forget its navigation, pick another.
    func forget(_ id: String) {
        navs[id] = nil
        if selectedID == id { selectedID = app.workspaces.first?.id }
    }
}
