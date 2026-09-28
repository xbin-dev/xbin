import Observation
import SwiftUI
import UIKit
import XbinAgent
import XbinCore
import XbinTerm

/// The tools of a tile's sessions screen (D132). In the app "tools" means
/// these panels — never an agent's tools.
enum SessionsTool: String, CaseIterable, Identifiable {
    case deployments, code, logs, prs
    var id: String { rawValue }

    var title: String {
        switch self {
        case .deployments: return "Live reload & deployments"
        case .code: return "Code"
        case .logs: return "Logs"
        case .prs: return "PRs"
        }
    }

    var chip: String {
        switch self {
        case .deployments: return "Deployments"
        case .code: return "Code"
        case .logs: return "Logs"
        case .prs: return "PRs"
        }
    }

    var symbol: String {
        switch self {
        case .deployments: return "arrow.up.to.line.compact"
        case .code: return "curlybraces"
        case .logs: return "list.bullet.rectangle"
        case .prs: return "arrow.left.arrow.right"
        }
    }

    /// Built in this version of the app.
    var available: Bool { true }
}

/// What the sessions screen shows under its tab strip.
enum SessionsPane: Equatable {
    case launcher
    case tab(String)
    case tool(SessionsTool)
}

/// A tile's sessions screen (D132): its tabs, the launcher, the tools, and
/// the requests behind them. XbinTerm's SessionTabs keeps the tabs in step
/// with the session directory; TileLauncher says what the launcher shows.
@MainActor
@Observable
final class TileWorkspaceModel: SessionTabHost {
    let workspace: WorkspaceModel
    let tile: String
    private(set) var tabs = SessionTabs()
    var pane: SessionsPane = .launcher
    /// Tabs shown at least once: their views stay mounted (a hidden pane
    /// keeps its socket, its scrollback, its transcript).
    private(set) var mounted: Set<String> = []
    /// Each view's own title (a shell's OSC title, an agent's), by tab.
    private(set) var titles: [String: String] = [:]
    private(set) var providers: [AgentProvider] = []
    private(set) var history: [HistoryEntry] = []
    private(set) var env: TermEnvState?
    private(set) var vmPref = false
    var starting = false
    var problem: String?
    /// The deployments state and its live updates (the Deployments tool,
    /// the launcher's banner); nil state on an xbind without them.
    let deploy: DeploymentsModel
    /// The Code, Logs and PRs tools (their state lives as long as the screen).
    let code: CodeToolModel
    let logs: LogsToolModel
    let prs: ProposalsModel
    /// The tab last in front: its session's target is the Dev API tag.
    private(set) var lastTab: String?

    @ObservationIgnored private var links: [String: SessionTabLink] = [:]
    /// A new shell's options, until its tab learns its session.
    @ObservationIgnored private(set) var newShells: [String: TermNewSession] = [:]
    @ObservationIgnored private var focus: SessionsFocus
    /// The tile's sessions in the last listing.
    @ObservationIgnored private var listed: Set<String> = []

    init(workspace: WorkspaceModel, tile: String, focus: SessionsFocus) {
        self.workspace = workspace
        self.tile = tile
        self.focus = focus
        deploy = DeploymentsModel(workspace: workspace, tile: tile)
        code = CodeToolModel(workspace: workspace, tile: tile)
        logs = LogsToolModel(workspace: workspace, tile: tile)
        prs = ProposalsModel(workspace: workspace, tile: tile)
        tabs.sync(workspace.sessions, cwd: tile)
        apply(focus)
    }

    // MARK: Loading

    func load() async {
        await workspace.refreshSessions()
        sync()
        // The listing may name the session asked for, or the tile's first,
        // only now — unless the user moved on meanwhile.
        switch focus {
        case .session: apply(focus)
        case .first: if pane == .launcher { apply(focus) }
        case .launcher, .deployments, .prs: break
        }
        async let p: [AgentProvider] = (try? await workspace.agents.providers()) ?? []
        async let h: [HistoryEntry] = (try? await workspace.agents.history(cwd: tile)) ?? []
        async let e: TermEnvState? = loadEnv()
        async let v: Bool = loadVMPref()
        async let d: Void = deploy.load()
        async let r: Void = prs.load()
        (providers, history, env, vmPref, _, _) = await (p, h, e, v, d, r)
    }

    private func loadEnv() async -> TermEnvState? {
        guard let r = try? await workspace.auth.call(APIRequest("GET", TermPaths.env(cwd: tile))) else { return nil }
        return try? JSONDecoder().decode(TermEnvState.self, from: r.body)
    }

    private var vmPrefPath: String { "/api/xbin/prefs/" + URLComponent.encode(TileLauncher.vmPrefKey(tile)) }

    private func loadVMPref() async -> Bool {
        guard let r = try? await workspace.auth.send(APIRequest("GET", vmPrefPath)), r.isSuccess else { return false }
        return (try? r.json())?.boolValue == true
    }

    /// The launcher's VM switch: the per-user, per-tile choice the web's
    /// launcher and title bar share (`termvm:<tile>`).
    func setVM(_ on: Bool) {
        vmPref = on
        let path = vmPrefPath
        let auth = workspace.auth
        Task {
            _ = try? await auth.send(on ? APIRequest("PUT", path, headers: ["Content-Type": "application/json"], body: Data("true".utf8))
                                        : APIRequest("DELETE", path))
        }
    }

    /// A listing of the caller's sessions arrived (the `term` events keep
    /// the workspace's directory live).
    func sync() {
        let live = Set(workspace.sessions.filter { $0.cwd == tile }.map(\.id))
        let gone = !listed.subtracting(live).isEmpty
        listed = live
        tabs.sync(workspace.sessions, cwd: tile)
        if case .tab(let k) = pane, tabs.tab(k) == nil { pane = tabs.tabs.first.map { .tab($0.key) } ?? .launcher }
        // A session that ended here or elsewhere: an agent's is history now.
        if gone { Task { history = (try? await workspace.agents.history(cwd: tile)) ?? history } }
    }

    private func apply(_ f: SessionsFocus) {
        switch f {
        case .first:
            if let t = tabs.tabs.first { select(t.key) } else { pane = .launcher }
        case .launcher:
            pane = .launcher
        case .deployments:
            pane = .tool(.deployments)
        case .prs:
            pane = .tool(.prs)
        case .session(let id):
            let row = workspace.sessions.first { $0.id == id }
            guard row == nil || row?.cwd == tile else { return }
            select(tabs.open(session: id, kind: row?.kind == .agent ? .agent : .shell, provider: row?.provider ?? "",
                             name: row?.name ?? ""))
        }
    }

    /// A deployment's log (the Deployments tool's Logs link).
    func showLogs(deployment: String) {
        logs.deployment = deployment == deploy.state?.primary ? "" : deployment
        pane = .tool(.logs)
    }

    /// The tile's deployments, primary first ([] without any).
    var deploymentNames: [String] {
        guard let s = deploy.state, s.record else { return [] }
        return DeployView.rows(s).map(\.name)
    }

    /// Another way in to the same screen (the long press, New session…).
    func show(_ f: SessionsFocus) {
        focus = f
        apply(f)
    }

    // MARK: Tabs

    func select(_ key: String) {
        mounted.insert(key)
        pane = .tab(key)
        lastTab = key
    }

    /// What the last tab's session calls (deploy-state.js `sessionTarget`:
    /// "primary", a deployment, "off"): the Deployments tool's Dev API tag.
    var devAPITarget: String? {
        guard let k = lastTab, let id = tabs.tab(k)?.session, let e = workspace.sessions.first(where: { $0.id == id }) else { return nil }
        return DeployView.sessionTarget(api: e.api, deployment: e.deployment)
    }

    /// The tab after or before the one in front (a swipe on the strip).
    func step(_ by: Int) {
        guard case .tab(let k) = pane else {
            // From the launcher or a tool: onto the first tab, or the last.
            if let t = by > 0 ? tabs.tabs.first : tabs.tabs.last { select(t.key) }
            return
        }
        if let next = tabs.neighbour(of: k, step: by) { select(next) }
    }

    func link(_ key: String) -> SessionTabLink {
        if let l = links[key] { return l }
        let l = SessionTabLink(key: key, host: self)
        links[key] = l
        return l
    }

    func tab(_ key: String, showsSession id: String) {
        tabs.bind(key, session: id)
        newShells[key] = nil
    }

    func tab(_ key: String, title: String) {
        if titles[key] != title { titles[key] = title }
    }

    func label(_ t: SessionTab) -> String {
        t.label { id in self.providers.first { $0.id == id }?.name }
    }

    /// The bar's title: the tab in front's own title, else what shows.
    var title: String {
        switch pane {
        case .launcher: return "New session"
        case .tool(let t): return t.chip
        case .tab(let k):
            guard let t = tabs.tab(k) else { return TileInfo.humanize(tile) }
            let own = titles[k] ?? ""
            return own.isEmpty ? label(t) : own
        }
    }

    // MARK: Starting

    var wantVM: Bool { TileLauncher.wantVM(pref: vmPref, status: env?.vm) }

    var launcher: TileLauncher {
        TileLauncher(tile: tile, providers: providers.map { ($0.id, $0.name) },
                     history: history.filter { $0.cwd.isEmpty || $0.cwd == tile }.map {
                         TileLauncher.Past(id: $0.id, provider: $0.provider, name: $0.name, preview: $0.preview, turns: $0.turns,
                                           ended: $0.ended, loadable: $0.loadable)
                     },
                     vmStatus: env?.vm, vmPref: vmPref, target: deploy.launcherInfo?.subtitle ?? "")
    }

    /// Bash: a tab whose terminal opens the session.
    func startShell() {
        let key = tabs.newShell(vm: wantVM)
        newShells[key] = TermNewSession(cwd: tile, vm: wantVM)
        select(key)
    }

    /// An agent (or a past session resumed): created first, as the web's
    /// launcher eager-creates it, so its pickers load before the first prompt.
    func startAgent(provider: String, resume: String? = nil) async {
        guard !starting else { return }
        starting = true
        problem = nil
        defer { starting = false }
        do {
            let info = try await workspace.agents.create(CreateAgentSession(cwd: tile, provider: provider, vm: wantVM ? true : nil,
                                                                            resume: resume))
            select(tabs.open(session: info.id, kind: .agent, provider: provider))
            Task { await workspace.refreshSessions() }
        } catch let e as AgentAPIError {
            problem = e.description
        } catch {
            problem = workspace.describe(error)
        }
    }

    /// A running session without a tab here (closed on this screen).
    func reopen(_ e: TermDirectoryEntry) {
        select(tabs.open(session: e.id, kind: e.kind == .agent ? .agent : .shell, provider: e.provider, name: e.name))
    }

    // MARK: A tab's menu

    func rename(_ key: String, to name: String) async {
        guard let id = tabs.tab(key)?.session else { return }
        _ = try? await workspace.auth.send(APIRequest("PATCH", TermDirectory.sessionPath(id),
                                                      headers: ["Content-Type": "application/json"],
                                                      body: TermDirectory.renameBody(name)))
        await workspace.refreshSessions()
    }

    /// Ends the tab's session; the tab stays, ended, until closed.
    func end(_ key: String) async {
        guard let id = tabs.tab(key)?.session else { return }
        _ = try? await workspace.auth.send(APIRequest("DELETE", TermDirectory.sessionPath(id)))
        tabs.end(key)
        await workspace.refreshSessions()
    }

    /// Closes the tab here; a live session keeps running (the launcher
    /// lists it under Running here).
    func close(_ key: String) {
        let next = tabs.close(key)
        mounted.remove(key)
        links[key] = nil
        titles[key] = nil
        newShells[key] = nil
        if pane == .tab(key) {
            if let next { select(next) } else { pane = .launcher }
        }
    }

    var running: [TermDirectoryEntry] { tabs.running(workspace.sessions, cwd: tile) }
}

/// A tile's sessions and tools (D132): the web terminal window's
/// counterpart, native. A strip of tabs — the tile's shells and agents
/// together, as frame-titlebar.js shows them — with `+` (the launcher)
/// and the tools menu (live reload and deployments; code, logs and PRs
/// to come). Tabs host the terminal and agent screens and stay mounted
/// while hidden; tap a tab or swipe the strip to switch; a tab's long
/// press renames, ends or closes it.
struct TileWorkspaceScreen: View {
    let workspace: WorkspaceModel
    let tile: String
    let focus: SessionsFocus

    @State private var model: TileWorkspaceModel?
    @Environment(WorkspaceNav.self) private var nav
    @Environment(\.panelActive) private var panelActive

    var body: some View {
        Group {
            if let m = model {
                TileWorkspaceBody(model: m, panelActive: panelActive)
            } else {
                ProgressView()
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            if model == nil {
                let m = TileWorkspaceModel(workspace: workspace, tile: tile, focus: focus)
                model = m
                Task { await m.load() }
            }
        }
        .onChange(of: focus) { _, f in model?.show(f) }
        .onChange(of: workspace.sessions) { _, _ in model?.sync() }
        // The tile's `deployments` and `pr` events keep its tools live.
        .task(id: model == nil) {
            guard let m = model else { return }
            await withTaskGroup(of: Void.self) { g in
                g.addTask { await m.deploy.follow() }
                g.addTask { await m.prs.follow() }
            }
        }
    }
}

private struct TileWorkspaceBody: View {
    @Bindable var model: TileWorkspaceModel
    let panelActive: Bool

    @Environment(WorkspaceNav.self) private var nav
    @State private var renaming: String?
    @State private var newName = ""

    var body: some View {
        VStack(spacing: 0) {
            SessionStrip(model: model, renaming: $renaming, newName: $newName)
            Divider()
            ZStack {
                ForEach(model.tabs.tabs.filter { model.mounted.contains($0.key) }) { t in
                    let front = model.pane == .tab(t.key)
                    tabView(t)
                        .environment(\.sessionTab, model.link(t.key))
                        .environment(\.panelActive, panelActive && front)
                        .opacity(front ? 1 : 0)
                        .allowsHitTesting(front)
                        .accessibilityHidden(!front)
                        .zIndex(front ? 1 : 0)
                }
                switch model.pane {
                case .launcher:
                    SessionLauncherView(model: model)
                        .background(Color(uiColor: .systemGroupedBackground))
                        .zIndex(2)
                case .tool(let t):
                    toolView(t)
                        .background(Color(uiColor: .systemGroupedBackground))
                        .zIndex(2)
                case .tab:
                    EmptyView()
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .navigationTitle(Text(verbatim: model.title))
        .modifier(DeploymentsAlerts(model: model.deploy))
        .alert("Rename session", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
            TextField("Name", text: $newName)
            Button("Cancel", role: .cancel) { renaming = nil }
            Button("Save") {
                if let k = renaming { Task { await model.rename(k, to: newName) } }
                renaming = nil
            }
        }
    }

    @ViewBuilder private func tabView(_ t: SessionTab) -> some View {
        switch t.kind {
        case .shell:
            TerminalScreen(workspace: model.workspace, cwd: model.tile, sessionID: t.session, newSession: model.newShells[t.key])
        case .agent:
            AgentScreen(workspace: model.workspace, cwd: model.tile, sessionID: t.session)
        }
    }

    @ViewBuilder private func toolView(_ t: SessionsTool) -> some View {
        switch t {
        case .deployments:
            DeploymentsToolView(model: model.deploy, sessions: model)
        case .code:
            CodeToolView(model: model.code)
        case .logs:
            LogsToolView(model: model.logs, deployments: model.deploymentNames)
        case .prs:
            ProposalsToolView(model: model.prs)
        }
    }
}

/// The tab strip: the tile's sessions, then an open tool, then `+`. A tap
/// switches, a horizontal swipe steps to the next or previous tab, a long
/// press offers Rename, End session and Close.
private struct SessionStrip: View {
    let model: TileWorkspaceModel
    @Binding var renaming: String?
    @Binding var newName: String

    var body: some View {
        HStack(spacing: 6) {
            ScrollViewReader { proxy in
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(model.tabs.tabs) { t in chip(t).id(t.key) }
                        if case .tool(let tool) = model.pane { toolChip(tool) }
                    }
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                }
                .onChange(of: model.pane) { _, p in
                    if case .tab(let k) = p { withAnimation { proxy.scrollTo(k) } }
                }
            }
            // The tools, as the web window's layout switcher sits in its
            // title bar: beside the tabs, not in the navigation bar (whose
            // room is the tab's own title and items).
            Menu {
                Section("Tools") {
                    ForEach(SessionsTool.allCases) { t in
                        Button {
                            model.pane = .tool(t)
                        } label: {
                            Label(t.title, systemImage: t.symbol)
                            if t == .prs, model.prs.openCount > 0 { Text(verbatim: "\(model.prs.openCount) open") }
                        }
                    }
                }
            } label: {
                stripButton("wrench.and.screwdriver", on: { if case .tool = model.pane { return true } else { return false } }())
                    .overlay(alignment: .topTrailing) {
                        // Open proposals, as the web window's ⇄ counts them.
                        if model.prs.openCount > 0 {
                            Text(verbatim: "\(model.prs.openCount)").font(.caption2.bold().monospacedDigit())
                                .padding(.horizontal, 4).padding(.vertical, 1)
                                .background(Color.xbinAmber, in: Capsule()).foregroundStyle(.black)
                                .offset(x: 4, y: -4)
                        }
                    }
            }
            .accessibilityLabel("Tools")
            .accessibilityIdentifier("sessions-tools")
            Button {
                model.pane = .launcher
            } label: {
                stripButton("plus", on: model.pane == .launcher)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("New session")
            .accessibilityIdentifier("sessions-plus")
            .padding(.trailing, 10)
        }
        .background(Color(uiColor: .secondarySystemBackground))
        .simultaneousGesture(DragGesture(minimumDistance: 30).onEnded { v in
            let dx = v.translation.width
            guard abs(dx) > 80, abs(v.translation.height) < 40 else { return }
            withAnimation(.snappy) { model.step(dx < 0 ? 1 : -1) }
        })
    }

    private func stripButton(_ symbol: String, on: Bool) -> some View {
        Image(systemName: symbol).font(.subheadline.weight(.semibold))
            .frame(width: 34, height: 30)
            .background(on ? Color.xbinAmber.opacity(0.28) : Color.secondary.opacity(0.12), in: Capsule())
            .foregroundStyle(on ? Color.primary : Color.secondary)
    }

    private func chip(_ t: SessionTab) -> some View {
        let front = model.pane == .tab(t.key)
        let label = model.label(t)
        return Button {
            model.select(t.key)
        } label: {
            HStack(spacing: 5) {
                Image(systemName: t.kind == .shell ? "apple.terminal" : "sparkles").font(.caption.weight(.semibold))
                Text(verbatim: label).font(.subheadline.weight(front ? .semibold : .regular)).lineLimit(1)
                if t.vm { Text("VM").font(.caption2.bold()).foregroundStyle(.secondary) }
            }
            .padding(.horizontal, 11).padding(.vertical, 6)
            .background(front ? Color.xbinAmber.opacity(0.28) : Color.secondary.opacity(0.12), in: Capsule())
            .overlay(Capsule().strokeBorder(front ? Color.xbinAmber : Color.clear, lineWidth: 1))
            .foregroundStyle(t.ended ? Color.secondary : Color.primary)
            .opacity(t.ended ? 0.6 : 1)
        }
        .buttonStyle(.plain)
        .contextMenu {
            if t.session != nil, !t.ended {
                Button("Rename…", systemImage: "pencil") { newName = t.name; renaming = t.key }
                Button("End session", systemImage: "stop.circle", role: .destructive) { Task { await model.end(t.key) } }
            }
            Button(t.ended || t.session == nil ? "Close" : "Close tab (keeps running)", systemImage: "xmark") { model.close(t.key) }
        }
        .accessibilityLabel(Text(verbatim: "\(label), \(t.kind == .shell ? "terminal" : "agent")\(t.ended ? ", ended" : "")"))
        .accessibilityAddTraits(front ? .isSelected : [])
        .accessibilityIdentifier("session-tab")
    }

    private func toolChip(_ tool: SessionsTool) -> some View {
        HStack(spacing: 5) {
            Image(systemName: tool.symbol).font(.caption.weight(.semibold))
            Text(verbatim: tool.chip).font(.subheadline.weight(.semibold))
            if tool == .prs, model.prs.openCount > 0 {
                Text(verbatim: "\(model.prs.openCount)").font(.caption.bold().monospacedDigit())
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .background(Color.xbinAmber, in: Capsule()).foregroundStyle(.black)
                    .accessibilityLabel(Text("\(model.prs.openCount) open"))
            }
            Button {
                model.pane = model.tabs.tabs.first.map { .tab($0.key) } ?? .launcher
            } label: {
                Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Close \(tool.chip)")
        }
        .padding(.horizontal, 11).padding(.vertical, 6)
        .background(Color.xbinAmber.opacity(0.28), in: Capsule())
        .overlay(Capsule().strokeBorder(Color.xbinAmber, lineWidth: 1))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tool-tab")
    }
}

/// The launcher (web/frame-launcher.js `launcher`): a box for Bash and one
/// per agent provider, the VM switch, the tile's sessions running without
/// a tab here, its recent agent sessions with Resume — and, where the tile
/// has deployments, live reload's banner and what a new session calls.
private struct SessionLauncherView: View {
    let model: TileWorkspaceModel

    var body: some View {
        let l = model.launcher
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(verbatim: l.heading).font(.footnote.monospaced()).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
                if let b = model.deploy.launcherInfo?.banner {
                    DeployBannerView(banner: b, model: model.deploy)
                }
                if let vm = l.vm {
                    Toggle(isOn: Binding(get: { vm.on }, set: { model.setVM($0) })) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(verbatim: vm.title).font(.subheadline.weight(.semibold))
                            Text(verbatim: vm.subtitle).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    .padding(12)
                    .background(Color(uiColor: .secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                LazyVGrid(columns: [GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10)], spacing: 10) {
                    ForEach(l.boxes) { box in
                        Button {
                            if box.kind == .shell { model.startShell() } else { Task { await model.startAgent(provider: box.provider) } }
                        } label: {
                            VStack(alignment: .leading, spacing: 4) {
                                Image(systemName: box.kind == .shell ? "apple.terminal" : "sparkles")
                                    .font(.title3).foregroundStyle(Color.xbinAmber)
                                Text(verbatim: box.title)
                                    .font(box.kind == .shell ? .headline.monospaced() : .headline)
                                    .foregroundStyle(.primary).lineLimit(1)
                                Text(verbatim: box.subtitle).font(.caption).foregroundStyle(.secondary)
                                    .lineLimit(2).multilineTextAlignment(.leading)
                            }
                            .frame(maxWidth: .infinity, minHeight: 84, alignment: .topLeading)
                            .padding(12)
                            .background(Color(uiColor: .secondarySystemGroupedBackground),
                                        in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        }
                        .buttonStyle(.plain)
                        .disabled(model.starting)
                        .accessibilityIdentifier("launch:\(box.id)")
                    }
                }
                if l.loadingAgents {
                    Text("loading agents…").font(.caption).foregroundStyle(.secondary)
                }
                if let note = model.deploy.launcherInfo?.note {
                    Text(verbatim: note).font(.caption).foregroundStyle(.secondary)
                }
                if let p = model.problem {
                    Label { Text(verbatim: p) } icon: { Image(systemName: "exclamationmark.triangle") }
                        .font(.footnote).foregroundStyle(.red)
                }
                if !model.running.isEmpty {
                    section("Running here") {
                        ForEach(model.running) { e in
                            Button { model.reopen(e) } label: {
                                row(title: e.title, subtitle: e.statusText, symbol: e.kind == .shell ? "apple.terminal" : "sparkles")
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
                if !l.recent.isEmpty {
                    section("Recent sessions") {
                        ForEach(l.recent) { r in
                            HStack(spacing: 10) {
                                row(title: r.title, subtitle: r.subtitle, symbol: "clock.arrow.circlepath")
                                if r.resumable {
                                    Button("Resume") { Task { await model.startAgent(provider: r.provider, resume: r.id) } }
                                        .buttonStyle(.bordered).tint(Color.xbinAmber).controlSize(.small)
                                        .disabled(model.starting)
                                } else {
                                    Text("read-only").font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
            }
            .padding(16)
            .frame(maxWidth: 560)
            .frame(maxWidth: .infinity)
        }
        .overlay { if model.starting { ProgressView().controlSize(.large) } }
    }

    private func section<C: View>(_ title: String, @ViewBuilder content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.footnote.weight(.semibold)).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 8) { content() }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(uiColor: .secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }

    private func row(title: String, subtitle: String, symbol: String) -> some View {
        HStack(spacing: 10) {
            Image(systemName: symbol).foregroundStyle(.secondary).frame(width: 20)
            VStack(alignment: .leading, spacing: 1) {
                Text(verbatim: title).font(.subheadline).lineLimit(1)
                Text(verbatim: subtitle).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .contentShape(Rectangle())
    }
}
