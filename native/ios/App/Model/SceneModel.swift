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
    /// The inbox asked for from the agent's own chrome (its shield menu):
    /// presented where that chrome presents — over the agent's sheet when
    /// that is up, where the window's own inbox could not show.
    var showAgentInbox = false
    /// The workspace agent's container is open (D233): a sheet on a
    /// compact width, a trailing column on a regular one.
    var showAgent = false
    /// What it was opened about (the tile or screen in front), offered to
    /// the agent's composer as a chip.
    var agentContext: AgentContext?
    /// Counts openings, so the same context is offered again.
    var agentOpened = 0
    /// The approval in front — the app's own sheet, drawn from xbind's
    /// reading of the id.
    var agentReview: AgentReview?
    /// One of the chrome's panels (Approvals, What my agent did, …).
    var agentPanel: AgentPanel?
    /// A review asked for while another sheet is up (the inbox): shown
    /// once that sheet has gone.
    @ObservationIgnored var pendingReview: AgentReview?
    /// Where the agent's view is to go once it shows (D241's router: a
    /// conversation, `s=<id>`); the view takes it and it is cleared.
    var agentAddress: String?
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
        return WindowTarget(workspace: id, screen: navs[id]?.screenID, surface: navs[id]?.surface)
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
        // A place with its screen restores as it was; a tile alone (a
        // dragged one, Handoff) opens over the screen it sits on.
        if target.screen != nil || target.surface == nil {
            w.restore(target, in: nav(for: w))
        } else if let s = target.surface {
            w.open(s, in: nav(for: w))
        }
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
        case .tunnelEnroll(let link):
            addRequest = .tunnelEnroll(link)
        case .sso, .ssoError:
            // The web authentication session delivers these to its caller.
            break
        default:
            guard let w = app.resolve(link) else { app.holdLink(link); return }
            select(w.id)
            // A link to the person's agent (an approval's push names its
            // component): the agent's own container, never a tile screen (D233).
            // (`xbin/` is the sealed namespace no workspace can hold a tile in:
            // such a path is never a tile screen, read state or not.)
            if case .tile(_, let tile, _) = link, w.agent.isHolder(tile) || tile.hasPrefix("xbin/") {
                openAgentFromLink(w)
                return
            }
            // …and so does a link to xbin's page for it (`xbin/agent`).
            if case .page(_, .agent) = link {
                openAgentFromLink(w)
                return
            }
            // …on exactly one thing (D241: a push, the Live Activity, Handoff)
            if case .workspaceAgent(_, let route) = link {
                openAgent(route, in: w)
                return
            }
            w.open(link: link, in: nav(for: w))
        }
    }

    // MARK: The workspace agent (D233)

    /// Opens the agent about what the window shows (nil: nothing offered).
    func openAgent(about context: AgentContext? = nil) {
        agentContext = context
        agentOpened += 1
        showAgent = true
    }

    /// A link named the person's agent: it opens — once xbind has said the
    /// person has one (or its setup, for an admin). A person without an
    /// agent gets the workspace's Home: never an empty pane, and nothing
    /// left behind that would open one later.
    func openAgentFromLink(_ w: WorkspaceModel) {
        Task {
            if w.agent.state == nil { await w.agent.refresh() }
            guard selectedID == w.id else { return }
            if w.agent.chip.shown { openAgent() } else { showAgent = false; nav(for: w).goHome() }
        }
    }

    /// The router (D241; plans/wsagent/ios-stage2.md §7): the agent opened on
    /// one thing — a request's sheet, a change set's review, a tile agent's
    /// session, a conversation, Needs you, or what waits first — once xbind
    /// has said the person has an agent and the app lock is passed. A person
    /// without one gets the workspace's Home, as any link to the agent. A
    /// sheet opened for a thing that no longer waits says what came of it
    /// (AgentApprovalSheet's settled state), never an empty sheet.
    func openAgent(_ route: AgentRoute, in w: WorkspaceModel) {
        Task {
            if w.agent.state == nil || w.agent.unreachable { await w.agent.refresh() }
            guard selectedID == w.id else { return }
            guard w.agent.chip.shown else {
                showAgent = false
                nav(for: w).goHome()
                return
            }
            // the app lock first: nothing of the agent shows over it (bounded: a lock left up holds the route)
            for _ in 0..<600 where AppModel.shared.locked { try? await Task.sleep(nanoseconds: 200_000_000) }
            guard !AppModel.shared.locked else { return }
            showInbox = false
            switch route {
            case .request(let id):
                await presentAfterOpening(.request(id))
            case .changeset(let id):
                await presentAfterOpening(.changeset(id))
            case .tileSession(let id):
                showAgent = false
                w.open(.agent(cwd: w.sessions.first { $0.id == id }?.cwd, session: id), in: nav(for: w))
            case .conversation(let id):
                agentAddress = "s=" + id
                openAgent()
                AgentNotifications.shared.seen(conversation: id, in: w)
            case .needs:
                openAgent()
                try? await Task.sleep(nanoseconds: 600_000_000)
                showAgentInbox = true
            case .first:
                await w.agent.refresh()
                if let n = w.agent.needs.first {
                    await presentAfterOpening(n.review)
                } else if let s = w.agent.waitingSessions.first {
                    showAgent = false
                    w.open(.agent(cwd: s.cwd, session: s.id), in: nav(for: w))
                } else {
                    openAgent()
                }
            }
        }
    }

    /// The agent's container first, then the review over it (two sheets in
    /// one moment would leave the second unshown).
    private func presentAfterOpening(_ r: AgentReview) async {
        AgentKeyboard.dismiss()
        let wasOpen = showAgent
        openAgent()
        if !wasOpen { try? await Task.sleep(nanoseconds: 700_000_000) }
        agentReview = r
    }

    /// Shows the approval of one request or change set. `afterInbox`: the
    /// inbox is up and goes first.
    func review(_ r: AgentReview, afterInbox: Bool = false) {
        if afterInbox, showInbox || showAgentInbox {
            pendingReview = r
            showInbox = false
            showAgentInbox = false
        } else {
            agentReview = r
        }
    }

    /// The inbox has gone: the review it asked for shows now.
    func inboxDismissed() {
        if let r = pendingReview {
            pendingReview = nil
            agentReview = r
        }
    }

    /// Workspace removed: forget its navigation, pick another.
    func forget(_ id: String) {
        navs[id] = nil
        if selectedID == id { selectedID = app.workspaces.first?.id }
    }
}
