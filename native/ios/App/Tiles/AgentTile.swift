import Observation
import SwiftUI
import UIKit
import XbinCore
import XbinRenderer

/// What the agent's container gives the runtime it shows (D233): the
/// answers to the view's asks (`xbin.native.agent`), and the views of the
/// two nodes the app draws itself.
@MainActor
struct AgentHost {
    /// Shows what `action` names; false when there was nothing to show.
    var ask: (AgentBridge.Action) -> Bool
    /// A `request` or `session` node, drawn from xbind's own reading.
    var node: @MainActor (XbinTrustedNode) -> AnyView
}

/// The agent's runtime while its container shows: opened when the
/// conversation first appears, kept — with its host — while the agent's own
/// tree pushes screens over the conversation, closed when the container
/// goes (AgentPane).
@MainActor
@Observable
final class AgentRuntimeBox {
    private(set) var runtime: NativeTileRuntime?
    /// Why the native view can't be shown (the runtime fell back).
    private(set) var failed: String?
    @ObservationIgnored private var claim: UUID?
    @ObservationIgnored private weak var workspace: WorkspaceModel?
    @ObservationIgnored private var tile = ""

    /// The runtime of `tile`, claimed for this container (once: a second
    /// call for the same tile only refreshes the host).
    func open(_ workspace: WorkspaceModel, _ tile: TileInfo, compact: Bool, nav: WorkspaceNav, host: AgentHost) {
        if let rt = runtime, !rt.stopped, claim != nil, self.tile == tile.path {
            rt.agentHost = host
            rt.setVisible(true)
            return
        }
        close()
        failed = nil
        let id = UUID()
        let rt = NativeRuntimePool.shared.open(workspace, tile, screen: id, fragment: nil) { [weak self] banner in self?.failed = banner }
        rt.hatches.nav = nav
        rt.setWidth(compact ? .compact : .regular)
        rt.agentHost = host
        rt.setVisible(true)
        claim = id
        runtime = rt
        self.workspace = workspace
        self.tile = tile.path
    }

    /// The container went: the view's asks answer nothing and its trusted
    /// nodes are inert again; the pool keeps the runtime warm.
    func close() {
        runtime?.agentHost = nil
        if let id = claim, let workspace { NativeRuntimePool.shared.close(workspace, tile, screen: id) }
        claim = nil
        runtime = nil
    }
}

/// The workspace agent's conversation: the sealed component's native view,
/// drawn by the renderer like any native tile's, in the agent's container.
/// The runtime is the tile's own — its frame token, its sandbox; it never
/// holds the person's session — and it can ask this chrome only to show
/// something by id. When the view can't be drawn (an older template, a
/// failure), the container says so: the page's own web view would need the
/// dock around it.
struct AgentTileView: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let box: AgentRuntimeBox
    /// The container is narrow (a sheet, a column): the view lays out compact.
    var compact: Bool
    /// The conversation opened something behind the sheet.
    var opened: () -> Void

    @Environment(SceneModel.self) private var scene
    @Environment(WorkspaceNav.self) private var nav

    var body: some View {
        ZStack {
            if let failed = box.failed {
                AgentStateView(title: "The conversation can't be shown here",
                               text: "\(failed) Approvals, what your agent did and your preferences still work from the shield.",
                               more: "", retry: { reopen() })
                    .navigationTitle("Workspace agent")
            } else if let rt = box.runtime, rt.lifecycle.phase == .live {
                XbinTreeView(store: rt.store, send: { rt.call($0) }, services: rt.services,
                             options: XbinRenderOptions(hostNavigation: true))
                    .modifier(AttachPickers(picker: rt.hatches.attach.picker))
                    .overlay(alignment: .bottom) { AttachStatusView(flow: rt.hatches.attach) }
                    .environment(\.horizontalSizeClass, compact ? .compact : .regular)
            } else {
                ProgressView().controlSize(.large).navigationTitle("Workspace agent")
            }
        }
        .onAppear(perform: open)
        // Opened again, perhaps about another tile — or about none: the view is told.
        .onChange(of: scene.agentOpened) { _, _ in tellContext() }
        // Live reload: the component's code is the release's, and a new
        // release reloads its pages.
        .task(id: tile.path) { await workspace.events.onReload(of: tile.path) { box.runtime?.reload() } }
    }

    private func open() {
        let fresh = box.runtime == nil
        box.open(workspace, tile, compact: compact, nav: nav, host: host)
        if fresh { tellContext() }
    }

    private func reopen() {
        box.close()
        open()
    }

    /// What the agent was opened about: the tile or screen in front, as
    /// `xbin:agent-context` — an offer, the composer's chip. Opened about
    /// nothing (from Home), the view is told that too: a chip of an earlier
    /// opening is not offered again.
    private func tellContext() {
        let c = scene.agentContext
        box.runtime?.tellAgent(AgentBridge.context(tile: c?.tile, screen: c?.screen))
    }

    private var host: AgentHost {
        let workspace = workspace, scene = scene, nav = nav, opened = opened
        let ask: (AgentBridge.Action) -> Bool = { action in
            switch action {
            case .openRequest(let id):
                AgentKeyboard.dismiss()
                scene.review(.request(id))
                return true
            case .openChangeset(let id):
                AgentKeyboard.dismiss()
                scene.review(.changeset(id))
                return true
            case .openSession(let session, let tile, let draft):
                guard workspace.tile(tile) != nil else { return false }
                if session == "new" {
                    // A new session of the tile's own agent: the person's
                    // words wait, unsent, in the composer of the one they pick.
                    if let draft, !draft.isEmpty { workspace.setPendingDraft(draft, tile: tile) }
                    workspace.open(.agent(cwd: tile, session: nil), in: nav)
                } else {
                    workspace.open(.agent(cwd: tile, session: session), in: nav)
                }
                opened()
                return true
            case .openView(let v):
                switch v {
                case .approvals: scene.agentPanel = .approvals
                case .activity: scene.agentPanel = .activity
                case .prefs: scene.agentPanel = .prefs
                }
                return true
            case .refuse:
                return false
            }
        }
        let node: @MainActor (XbinTrustedNode) -> AnyView = { n in
            switch n {
            case .request(let id, let kind):
                return AnyView(AgentRequestCard(workspace: workspace, id: id, isChangeSet: kind == "changeset").environment(scene))
            case .session(let id, let tile):
                return AnyView(AgentSessionCard(workspace: workspace, id: id, tileHint: tile, opened: opened).environment(scene).environment(nav))
            }
        }
        return AgentHost(ask: ask, node: node)
    }
}
