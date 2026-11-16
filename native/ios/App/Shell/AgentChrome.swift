import SwiftUI
import UIKit
import XbinCore
import XbinRenderer

// The workspace agent in the app (D233; docs/workspace-agent.md §In the
// app): the one button every panel's bar ends with, Home's row, and the
// container the agent opens in — a sheet with a medium and a large detent
// on a compact width (medium leaves the tile behind usable), a trailing
// column on a regular one, the second pane of an open Duo.
//
// Two layers, visibly different. The conversation is the agent's own
// native view (the sealed component's `native.js`, drawn by XbinRenderer
// like any native tile). Everything that GRANTS — an approval, an admin
// batch, resuming after a pause — is this chrome's: drawn by the app from
// what xbind answers for an id, sent with the device's session
// (AgentApproval.swift, AgentChangeSet.swift). The chrome's own controls
// sit behind the shield in the pane's bar.

/// The agent glyph: one node above three — a parent and its delegates.
/// Squares, as every dot of the theme is.
struct AgentGlyph: View {
    var size: CGFloat = 20

    var body: some View {
        AgentGlyphShape().frame(width: size, height: size).accessibilityHidden(true)
    }
}

/// The glyph's outline in a square: four square nodes and the rules that
/// join the top one to the three below.
struct AgentGlyphShape: Shape {
    func path(in rect: CGRect) -> Path {
        let u = rect.width / 10
        let node = CGSize(width: 2.6 * u, height: 2.6 * u)
        let line = max(1, 0.9 * u)
        let top = CGRect(x: rect.minX + (rect.width - node.width) / 2, y: rect.minY + 0.6 * u, width: node.width, height: node.height)
        let y = rect.maxY - node.height - 0.6 * u
        let xs = [rect.minX + 0.4 * u, rect.minX + (rect.width - node.width) / 2, rect.maxX - node.width - 0.4 * u]
        let mid = top.maxY + (y - top.maxY) / 2
        var p = Path()
        p.addRect(top)
        for x in xs { p.addRect(CGRect(x: x, y: y, width: node.width, height: node.height)) }
        // the stem, the bar across, and the two drops at its ends
        p.addRect(CGRect(x: top.midX - line / 2, y: top.maxY, width: line, height: y - top.maxY))
        let left = xs[0] + node.width / 2, right = xs[2] + node.width / 2
        p.addRect(CGRect(x: left - line / 2, y: mid - line / 2, width: right - left + line, height: line))
        p.addRect(CGRect(x: left - line / 2, y: mid, width: line, height: y - mid))
        p.addRect(CGRect(x: right - line / 2, y: mid, width: line, height: y - mid))
        return p
    }
}

/// The entry button: the trailing-most item of every panel's bar (on an
/// open Duo, of the vertical bar), with the count of what waits for the
/// person because of their agent — or a pause glyph.
struct AgentBarButton: View {
    let workspace: WorkspaceModel
    @Environment(SceneModel.self) private var scene
    @Environment(WorkspaceNav.self) private var nav

    var body: some View {
        let chip = workspace.agent.chip
        Button {
            if scene.showAgent { scene.showAgent = false } else { scene.openAgent(about: context) }
        } label: {
            HStack(spacing: 3) {
                AgentGlyph(size: 20).foregroundStyle(XbinColor.partAgent)
                if chip.paused {
                    Image(systemName: "pause.fill").font(.caption2.weight(.bold)).foregroundStyle(XbinColor.warn)
                } else if chip.count > 0 {
                    Text(verbatim: "\(chip.count)").font(.caption2.bold().monospacedDigit())
                        .padding(.horizontal, 5).padding(.vertical, 1)
                        .background(XbinColor.accent, in: .xbinPlate).foregroundStyle(XbinColor.onAccent)
                }
            }
            .frame(minWidth: 28, minHeight: 28)
        }
        .accessibilityLabel(Text(verbatim: chip.spoken))
        .accessibilityIdentifier("panel-agent")
        .accessibilityAddTraits(scene.showAgent ? .isSelected : [])
    }

    /// What the panel in front is about: its tile (never the agent itself
    /// or one of xbind's pages), else the screen shown.
    private var context: AgentContext? {
        if let t = nav.surface?.tilePath, !t.isEmpty, !workspace.agent.isHolder(t) { return AgentContext(tile: t) }
        if nav.surface == nil, let id = nav.screenID, let s = workspace.home.screen(id) { return AgentContext(screen: s.name) }
        return nil
    }
}

/// Home's row for the agent, under Needs you: its status in a few words
/// ("idle · 2 need you"). No count badge of its own — Needs you above it
/// and the bar's item carry the one count; a second and a third blue plate
/// on one screen said nothing more. Paused keeps its glyph.
struct AgentHomeRow: View {
    let workspace: WorkspaceModel
    @Environment(SceneModel.self) private var scene

    var body: some View {
        let chip = workspace.agent.chip
        if chip.shown {
            Button { scene.openAgent() } label: {
                HStack(spacing: 8) {
                    Label {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(chip.setup ? "Set up the workspace agent" : "Workspace agent")
                            Text(verbatim: workspace.agent.homeStatus).font(.caption).foregroundStyle(.secondary)
                        }
                    } icon: {
                        AgentGlyph(size: 20).foregroundStyle(XbinColor.partAgent)
                    }
                    Spacer(minLength: 4)
                    if chip.paused {
                        Image(systemName: "pause.fill").font(.footnote).foregroundStyle(XbinColor.warn)
                    }
                    Image(systemName: "chevron.forward").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                }
            }
            .tint(.primary)
            .compactRow()
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text(verbatim: chip.spoken))
            .accessibilityValue(Text(verbatim: workspace.agent.homeStatus))
            .accessibilityAddTraits(.isButton)
            .accessibilityIdentifier("home-agent")
        }
    }
}

/// The agent as a sheet (a compact width): large first; medium keeps the
/// tile behind usable and shows the newest lines, the quick replies and
/// the composer. Focusing the composer raises it to large.
struct AgentSheet: View {
    let workspace: WorkspaceModel
    @Environment(SceneModel.self) private var scene
    @State private var detent: PresentationDetent = .large

    var body: some View {
        AgentPane(workspace: workspace, asSheet: true, toMedium: { detent = .medium })
            .presentationDetents([.medium, .large], selection: $detent)
            .presentationBackgroundInteraction(.enabled(upThrough: .medium))
            .presentationDragIndicator(.visible)
            .background(KeyboardShowReporter { if detent != .large { detent = .large } })
            .onChange(of: scene.agentOpened) { _, _ in detent = .large }
    }
}

/// The agent's container, whatever holds it: a bar of its own (close, the
/// chrome's shield menu) over the conversation, or over what xbin says in
/// its place — not set up, turned off, held, out of reach.
struct AgentPane: View {
    let workspace: WorkspaceModel
    var asSheet: Bool
    /// Makes room for what the conversation opened (a tile agent's
    /// session behind the sheet).
    var toMedium: () -> Void = {}

    @Environment(SceneModel.self) private var scene
    @Environment(WorkspaceNav.self) private var nav
    @Environment(\.scenePhase) private var phase
    /// The conversation's runtime, held by the pane — not by the view inside
    /// its navigation stack, which goes whenever the agent's own tree pushes
    /// a screen over it (the notebook, a tool call in full): the runtime,
    /// its asks and its `request` and `session` cards live as long as the
    /// pane shows.
    @State private var box = AgentRuntimeBox()

    var body: some View {
        let agent = workspace.agent
        NavigationStack {
            content(agent)
                .concreteBackground()
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    // xbin's own controls lead the bar — close, and the shield —
                    // apart from the agent's view's, which trail it.
                    ToolbarItem(placement: .topBarLeading) {
                        Button { scene.showAgent = false } label: { Image(systemName: "xmark") }
                            .accessibilityLabel("Close the workspace agent")
                            .accessibilityIdentifier("agent-close")
                    }
                    ToolbarItem(placement: .topBarLeading) { AgentChromeMenu(workspace: workspace) }
                }
        }
        // Over a sheet, the sheet presents the chrome's own sheets; beside
        // the panels (a column), the window does (WorkspaceView).
        .modifier(AgentChromeSheets(workspace: workspace, active: asSheet))
        .task(id: scene.agentOpened) { await agent.refresh() }
        // Back in front: what waits, and whether xbind can be reached, are read again.
        .onChange(of: phase) { _, p in if p == .active { Task { await agent.refresh() } } }
        .onDisappear { box.close() }
    }

    @ViewBuilder private func conversation(_ agent: WorkspaceAgentModel) -> some View {
        if let tile = agent.holder {
            AgentTileView(workspace: workspace, tile: tile, box: box, compact: true, opened: toMedium)
                .safeAreaInset(edge: .top, spacing: 0) {
                    if agent.dock == .offline { AgentOfflineBar(workspace: workspace) }
                }
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    if agent.dock == .paused { AgentPausedBar(workspace: workspace) }
                }
        } else {
            ProgressView().controlSize(.large).frame(maxWidth: .infinity, maxHeight: .infinity)
                .navigationTitle("Workspace agent")
        }
    }

    @ViewBuilder private func content(_ agent: WorkspaceAgentModel) -> some View {
        switch agent.dock {
        case .page, .paused:
            conversation(agent)
        // …also while xbind can't be reached, once it was loaded: it stays
        // readable, under xbin's own line that says so (its cards' Review…
        // wait for the connection: AgentRequestCard).
        case .offline where agent.offlineWithConversation:
            conversation(agent)
        case .setup:
            AgentSetupView(workspace: workspace).navigationTitle("Workspace agent")
        case .hidden:
            AgentStateView(title: "No workspace agent here", text: "You don't have one in this workspace.", more: "", retry: nil)
                .navigationTitle("Workspace agent")
        default:
            // which switch is on (xbin's words, the web's), then who changes it and where
            let w = DelegationWords.stateWords(agent.dock, state: agent.state)
            let who = [w?.more ?? "", DelegationWords.whoChanges(agent.dock, state: agent.state)].filter { !$0.isEmpty }.joined(separator: " ")
            AgentStateView(title: w?.title ?? "", text: w?.text ?? "", more: who, retry: { await agent.refresh() })
                .navigationTitle("Workspace agent")
        }
    }
}

/// xbin's own controls for the agent, behind a shield: what waits, the
/// standing approvals, the log, the preferences, and Pause — the last item,
/// always there. None of it is the agent's to draw.
struct AgentChromeMenu: View {
    let workspace: WorkspaceModel
    @Environment(SceneModel.self) private var scene
    @State private var problem: String?

    var body: some View {
        let agent = workspace.agent
        Menu {
            if agent.dock == .page || agent.dock == .paused || agent.dock == .soon {
                if agent.count > 0 {
                    // (its own flag: over the agent's sheet the inbox is the sheet's to present)
                    Button("Needs you (\(agent.count))", systemImage: "tray.full") { scene.showAgentInbox = true }
                }
                Button("Approvals", systemImage: "checkmark.shield") { scene.agentPanel = .approvals }
                Button("What my agent did", systemImage: "list.bullet.rectangle") { scene.agentPanel = .activity }
                Button("Preferences", systemImage: "slider.horizontal.3") { scene.agentPanel = .prefs }
                Divider()
                if agent.state?.paused == true {
                    Button("Resume my agent", systemImage: "play.fill") { set(paused: false) }
                } else {
                    Button("Pause my agent", systemImage: "pause.fill") { set(paused: true) }
                }
            } else {
                Button("Check again", systemImage: "arrow.clockwise") { Task { await agent.refresh() } }
            }
        } label: {
            Image(systemName: "shield.lefthalf.filled")
        }
        .accessibilityLabel("xbin's controls for your agent")
        .accessibilityIdentifier("agent-chrome-menu")
        .alert("That didn't work", isPresented: Binding(get: { problem != nil }, set: { if !$0 { problem = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(verbatim: problem ?? "")
        }
    }

    /// Pausing is immediate and asks nothing (the safe direction).
    /// Resuming is the person's own tap, here or on the paused bar.
    private func set(paused: Bool) {
        Task {
            problem = await workspace.agent.setPaused(paused)
            if problem == nil { Haptics.settle() }
        }
    }
}

/// While paused: that it is, what that means, and Resume — under the
/// view's composer, which keeps the person's unsent words in view,
/// read-only. The one place that says the agent is paused: the view draws
/// no banner of its own then (a second one, pinned above the conversation,
/// covered what it scrolled over). Resuming is a grant, so the button is
/// the app's.
struct AgentPausedBar: View {
    let workspace: WorkspaceModel
    @State private var working = false
    @State private var problem: String?

    var body: some View {
        let w = DelegationWords.stateWords(.paused, state: workspace.agent.state)
        VStack(alignment: .leading, spacing: 8) {
            Label {
                Text(verbatim: w?.title ?? "Paused by you.").font(.subheadline.weight(.semibold))
            } icon: {
                Image(systemName: "pause.fill").foregroundStyle(XbinColor.warn)
            }
            if let text = w?.text {
                Text(verbatim: text).font(.footnote).foregroundStyle(XbinColor.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let problem { Text(verbatim: problem).font(.footnote).foregroundStyle(XbinColor.danger) }
            Button {
                working = true
                Task {
                    problem = await workspace.agent.setPaused(false)
                    working = false
                }
            } label: {
                Text("Resume").frame(maxWidth: .infinity, minHeight: 30)
            }
            .xbinPrimary()
            .disabled(working)
            .accessibilityLabel("Resume my agent")
            .accessibilityIdentifier("agent-resume")
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(XbinColor.warnBackground)
        .overlay(alignment: .top) { Rectangle().fill(XbinColor.warn).frame(height: 2) }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("agent-paused")
    }
}

/// xbind can't be reached, over a conversation that was loaded: xbin's own
/// line above it — what is shown is what was loaded, and approvals wait.
struct AgentOfflineBar: View {
    let workspace: WorkspaceModel
    @State private var checking = false

    var body: some View {
        let w = DelegationWords.stateWords(.offline, state: workspace.agent.state)
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "wifi.exclamationmark").foregroundStyle(XbinColor.warn).accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: w?.title ?? "Can't reach xbin.").font(.subheadline.weight(.semibold))
                    .accessibilityIdentifier("agent-offline")
                Text(verbatim: w?.text ?? "").font(.footnote).foregroundStyle(XbinColor.muted).fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 4)
            Button {
                checking = true
                Task {
                    await workspace.agent.refresh()
                    checking = false
                }
            } label: { Text("Try now").frame(minHeight: 30) }
            .xbinSecondary()
            .disabled(checking)
            .accessibilityIdentifier("agent-offline-retry")
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(XbinColor.warnBackground)
        .overlay(alignment: .bottom) { Rectangle().fill(XbinColor.warn).frame(height: 2) }
    }
}

/// What xbin says in place of the conversation: what is wrong, that
/// nothing is lost, and who fixes it.
struct AgentStateView: View {
    let title: String
    let text: String
    let more: String
    let retry: (() async -> Void)?
    @State private var checking = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                Text(verbatim: title).font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("agent-state")
                Text(verbatim: text).foregroundStyle(XbinColor.text)
                if !more.isEmpty { Text(verbatim: more).font(.callout).foregroundStyle(XbinColor.muted) }
                if let retry {
                    Button {
                        checking = true
                        Task {
                            await retry()
                            checking = false
                        }
                    } label: { Text("Check again").frame(minHeight: 30) }
                    .xbinSecondary()
                    .disabled(checking)
                    .padding(.top, 6)
                }
            }
            .padding(20)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// An admin before setup: the three steps, read-only, and the way to the
/// web, where setup lives.
struct AgentSetupView: View {
    let workspace: WorkspaceModel
    @Environment(SceneModel.self) private var scene

    var body: some View {
        let agent = workspace.agent
        let steps = DelegationWords.setupSteps(state: agent.state, llmConfigured: agent.llmConfigured ?? false, hosted: agent.llmHosted)
        List {
            Section {
                VStack(alignment: .leading, spacing: 6) {
                    Text(verbatim: DelegationWords.setupIntro.title).font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
                        .accessibilityIdentifier("agent-setup")
                    Text(verbatim: DelegationWords.setupIntro.text).font(.callout).foregroundStyle(XbinColor.muted)
                }
            }
            Section {
                ForEach(Array(steps.enumerated()), id: \.offset) { i, step in
                    HStack(alignment: .firstTextBaseline, spacing: 10) {
                        Image(systemName: step.done ? "checkmark.square.fill" : "square")
                            .foregroundStyle(step.done ? XbinColor.ok : XbinColor.muted)
                            .accessibilityLabel(step.done ? "Done" : "Not done")
                        VStack(alignment: .leading, spacing: 2) {
                            Text(verbatim: "\(i + 1). \(step.title)").font(.body.weight(.semibold))
                            if !step.tag.isEmpty {
                                Text(verbatim: step.tag.uppercased()).font(.caption2.weight(.bold)).foregroundStyle(XbinColor.muted)
                            }
                            Text(verbatim: step.text).font(.callout).foregroundStyle(XbinColor.muted)
                        }
                    }
                    .accessibilityElement(children: .combine)
                }
            } footer: {
                Text("Setup is done on the web. Nothing here changes the workspace.")
            }
            Section {
                Button {
                    scene.showAgent = false
                    workspace.openPage(.agent, in: scene.nav(for: workspace))
                } label: {
                    Label("Open the setup on the web", systemImage: "globe")
                }
                .accessibilityIdentifier("agent-setup-web")
            }
        }
    }
}

/// The chrome's own sheets — an approval, an admin batch, the panels —
/// presented from one place at a time: the agent's sheet while it is up,
/// else the window.
struct AgentChromeSheets: ViewModifier {
    let workspace: WorkspaceModel
    let active: Bool
    @Environment(SceneModel.self) private var scene

    func body(content: Content) -> some View {
        let review = Binding<AgentReview?>(get: { active ? scene.agentReview : nil }, set: { if active { scene.agentReview = $0 } })
        let panel = Binding<AgentPanel?>(get: { active ? scene.agentPanel : nil }, set: { if active { scene.agentPanel = $0 } })
        let inbox = Binding<Bool>(get: { active && scene.showAgentInbox }, set: { if active { scene.showAgentInbox = $0 } })
        content
            .sheet(item: review) { r in
                Group {
                    switch r {
                    case .request(let id): AgentApprovalSheet(workspace: workspace, id: id)
                    case .changeset(let id): AgentChangeSetSheet(workspace: workspace, id: id)
                    }
                }
                // One sheet a request: the next one that waits starts from
                // nothing — Once chosen, Allow unarmed — never from what the
                // person chose for the one before it.
                .id(r.id)
                .environment(scene)
            }
            // Needs you, asked for from the shield: presented where the
            // chrome's sheets are (over the agent's own sheet, or the window).
            .sheet(isPresented: inbox, onDismiss: { scene.inboxDismissed() }) { InboxView().environment(scene) }
            .sheet(item: panel) { p in
                AgentPanelSheet(workspace: workspace, panel: p).environment(scene)
            }
    }
}

/// Tells when the software keyboard is about to show (the agent's sheet
/// rises to large for the composer).
struct KeyboardShowReporter: UIViewRepresentable {
    let shows: () -> Void

    func makeUIView(context: Context) -> Reporter { Reporter(shows: shows) }
    func updateUIView(_ uiView: Reporter, context: Context) { uiView.shows = shows }

    final class Reporter: UIView {
        var shows: () -> Void

        init(shows: @escaping () -> Void) {
            self.shows = shows
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        // A selector observer: NotificationCenter drops it with the view.
        override func didMoveToWindow() {
            super.didMoveToWindow()
            NotificationCenter.default.removeObserver(self, name: UIResponder.keyboardWillShowNotification, object: nil)
            guard window != nil else { return }
            NotificationCenter.default.addObserver(self, selector: #selector(keyboardWillShow(_:)),
                                                   name: UIResponder.keyboardWillShowNotification, object: nil)
        }

        @objc private func keyboardWillShow(_ note: Notification) { shows() }
    }
}

/// An approval never appears with the keyboard up: it goes first.
@MainActor
enum AgentKeyboard {
    static func dismiss() {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }
}
