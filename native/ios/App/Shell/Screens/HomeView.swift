import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel
import XbinTerm

/// Home, a workspace's first panel (plans/native.md §4, D117): its screens
/// only — Mine, each org, Workspace, inside their folders as the web
/// sidebar files them (HomeModel) — with what needs you, the terminals and
/// the agents on top, and search and "All tiles" at the bottom. A screen
/// opens as the next panel; "New screen" adds a personal one to the layout
/// the web shell uses too.
struct HomeView: View {
    let workspace: WorkspaceModel

    @Environment(WorkspaceNav.self) private var nav
    @Environment(SceneModel.self) private var scene
    @State private var query = ""
    @State private var collapsed: Set<String> = []
    @State private var naming = false
    @State private var newName = ""
    @State private var problem: String?

    var body: some View {
        List {
            if !query.isEmpty {
                Section {
                    ForEach(workspace.catalog.search(query)) { TileRow(workspace: workspace, tile: $0, pick: open) }
                }
            } else {
                Section { Shortcuts(workspace: workspace, pick: open) }
                ForEach(workspace.home.sections) { s in
                    Section {
                        ForEach(s.folders) { f in folder(f, depth: 0) }
                        ForEach(s.screens) { screenRow($0) }
                        if s.id == "mine" { newScreenButton }
                    } header: { Text(verbatim: s.title) }
                }
                if canAddScreens, !workspace.home.sections.contains(where: { $0.id == "mine" }) {
                    Section { newScreenButton } header: { Text("Mine") } footer: {
                        if workspace.homeLoaded, workspace.home.screens.isEmpty {
                            Text("Screens gather tiles as cards — the same screens the workspace shows on the web.")
                        }
                    }
                }
                if workspace.loading, !workspace.homeLoaded {
                    HStack { Spacer(); ProgressView(); Spacer() }
                }
                if let e = workspace.lastError, workspace.signInProblem == nil {
                    Section { Label { Text(verbatim: e) } icon: { Image(systemName: "exclamationmark.triangle") }.foregroundStyle(.red) }
                }
                Section {
                    NavigationLink {
                        AllTilesView(workspace: workspace, pick: open)
                    } label: {
                        Label("All tiles", systemImage: "square.stack.3d.up")
                    }
                    Button { scene.showSettings = true } label: { Label("Settings", systemImage: "gearshape") }
                }
            }
        }
        .searchable(text: $query, prompt: "Search tiles")
        .refreshable { await workspace.refresh() }
        .navigationTitle(Text(verbatim: workspace.title))
        .alert("New screen", isPresented: $naming) {
            TextField("Name", text: $newName)
            Button("Cancel", role: .cancel) {}
            Button("Create") { Task { await addScreen() } }
        } message: {
            Text("A screen of your own, on the web too.")
        }
        .alert("Couldn't make the screen", isPresented: Binding(get: { problem != nil }, set: { if !$0 { problem = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(verbatim: problem ?? "")
        }
    }

    private var canAddScreens: Bool { !(workspace.whoami?.userID ?? "").isEmpty && workspace.whoami?.readOnly != true }

    /// A tile found by search takes the keyboard's place: the search
    /// field lets it go first (a terminal then takes it for itself).
    private func open(_ s: Surface) {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        workspace.open(s, in: nav)
    }

    @ViewBuilder private var newScreenButton: some View {
        if canAddScreens {
            Button {
                newName = "Screen \(workspace.layout.screens.count + 1)"
                naming = true
            } label: {
                Label("New screen", systemImage: "plus")
            }
        }
    }

    private func addScreen() async {
        do {
            let id = try await workspace.addScreen(name: newName)
            nav.openScreen(id)
        } catch {
            problem = workspace.describe(error)
        }
    }

    private func screenRow(_ s: ScreenInfo) -> some View {
        Button { nav.openScreen(s.id) } label: {
            HStack {
                Label { Text(verbatim: s.name) } icon: { Image(systemName: "square.grid.2x2").foregroundStyle(Color.xbinAmber) }
                Spacer()
                let n = workspace.cards(for: s).count
                Text(verbatim: "\(n)").font(.callout.monospacedDigit()).foregroundStyle(.secondary)
                    .accessibilityLabel(Text("\(n) tiles"))
                Image(systemName: "chevron.forward").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            }
        }
        .tint(.primary) // a place to go, not an action: the title in the text color
        .accessibilityIdentifier("screen:\(s.id)")
    }

    /// A folder and what it files, open unless the user folded it.
    private func folder(_ f: HomeModel.Folder, depth: Int) -> AnyView {
        AnyView(DisclosureGroup(isExpanded: Binding(get: { !collapsed.contains(f.id) }, set: { open in
            if open { collapsed.remove(f.id) } else { collapsed.insert(f.id) }
        })) {
            ForEach(f.children) { c in folder(c, depth: depth + 1) }
            ForEach(f.screens) { screenRow($0) }
        } label: {
            Label { Text(verbatim: f.name) } icon: { Image(systemName: "folder") }
        })
    }
}

/// Needs you, the terminals and the agents — Home's top row.
private struct Shortcuts: View {
    let workspace: WorkspaceModel
    let pick: (Surface) -> Void
    @Environment(SceneModel.self) private var scene

    var body: some View {
        let shells = workspace.sessions.filter { $0.kind == .shell }
        let agents = workspace.sessions.filter { $0.kind == .agent }
        HStack(spacing: 10) {
            Button { scene.showInbox = true } label: {
                shortcut("Needs you", "tray", workspace.needsYou.count, highlight: !workspace.needsYou.isEmpty)
            }
            Menu {
                if shells.isEmpty { Text("No terminals open") }
                ForEach(shells) { s in
                    Button("\(s.title) · \(TileInfo.humanize(s.cwd))") { pick(.terminal(cwd: s.cwd, session: s.id)) }
                }
            } label: { shortcut("Terminals", "apple.terminal", shells.count) }
            Menu {
                if agents.isEmpty { Text("No agent sessions") }
                ForEach(agents) { s in
                    Button("\(s.title) · \(TileInfo.humanize(s.cwd))\(s.needsYou ? " · waiting" : "")") {
                        pick(.agent(cwd: s.cwd, session: s.id))
                    }
                }
            } label: { shortcut("Agents", "sparkles", agents.count) }
        }
        .buttonStyle(.borderless)
        .listRowInsets(EdgeInsets(top: 8, leading: 12, bottom: 8, trailing: 12))
    }

    private func shortcut(_ title: LocalizedStringKey, _ symbol: String, _ count: Int, highlight: Bool = false) -> some View {
        VStack(spacing: 4) {
            ZStack(alignment: .topTrailing) {
                Image(systemName: symbol).font(.title3).frame(width: 34, height: 28)
                if count > 0 {
                    Text(verbatim: "\(count)").font(.caption2.bold()).padding(.horizontal, 5).padding(.vertical, 1)
                        .background(highlight ? Color.xbinAmber : Color.secondary.opacity(0.25), in: Capsule())
                        .foregroundStyle(highlight ? Color.black : Color.primary)
                        .offset(x: 10, y: -6)
                }
            }
            Text(title).font(.caption).foregroundStyle(.primary).lineLimit(1)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 6)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

/// Every tile the user can see, by path.
struct AllTilesView: View {
    let workspace: WorkspaceModel
    let pick: (Surface) -> Void

    var body: some View {
        List {
            ForEach(workspace.catalog.listed) { TileRow(workspace: workspace, tile: $0, pick: pick) }
        }
        .navigationTitle("All tiles")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A tile: its icon and name, its path, its badge and status, whether it
/// opens natively. Its menu: a new window, a terminal or agent there, the
/// web page instead of the native view.
struct TileRow: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let pick: (Surface) -> Void

    var body: some View {
        let meta = workspace.tileMeta[tile.path]
        Button { pick(.tile(tile.path)) } label: {
            HStack {
                if let symbol = meta?.symbol {
                    Image(systemName: symbol).foregroundStyle(.tint).frame(width: 24)
                        .accessibilityHidden(true)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: tile.title).foregroundStyle(.primary)
                    Text(verbatim: tile.path).font(.caption.monospaced()).foregroundStyle(.secondary)
                }
                Spacer()
                if let st = workspace.statuses[tile.path] { StatusDot(status: st) }
                if let badge = meta?.badge { TileBadge(text: badge) }
                if workspace.surfaceKind(for: tile) == .native {
                    Text("native").font(.caption2.bold()).padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Color.xbinAmber.opacity(0.25), in: Capsule())
                }
            }
        }
        .onDrag { TileMenu.dragItem(workspace, tile) }
        .contextMenu { TileMenu(workspace: workspace, tile: tile, pick: pick) }
    }
}

/// A tile's long-press menu (rows and cards).
struct TileMenu: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let pick: (Surface) -> Void

    @Environment(\.openWindow) private var openWindow
    @Environment(\.supportsMultipleWindows) private var multipleWindows

    var body: some View {
        if multipleWindows {
            Button("Open in New Window", systemImage: "macwindow.badge.plus") {
                openWindow(value: WindowTarget(workspace: workspace.id, surface: .tile(tile.path)))
            }
        }
        Button("Terminal here", systemImage: "apple.terminal") { pick(.terminal(cwd: tile.path, session: nil)) }
        Button("Agent here", systemImage: "sparkles") { pick(.agent(cwd: tile.path, session: nil)) }
        if tile.opensNatively {
            let forced = AppSettings.forcesWeb(workspace.id, tile.path)
            Button(forced ? "Use the native view" : "Open as web page", systemImage: forced ? "rectangle.stack" : "globe") {
                AppSettings.setForcesWeb(workspace.id, tile.path, !forced)
                pick(.tile(tile.path))
            }
        }
    }

    /// Dragging a tile carries its activity: dropped outside the window it
    /// becomes a new window on that tile (RootView continues it).
    static func dragItem(_ w: WorkspaceModel, _ tile: TileInfo) -> NSItemProvider {
        let p = NSItemProvider()
        p.registerObject(HandoffActivity.make(w, .tile(tile.path)), visibility: .all)
        p.suggestedName = tile.title
        return p
    }
}

/// A tile's reported status (`xbin.status`) as a dot: blue info, orange
/// warn, red error, green a message with ok.
struct StatusDot: View {
    let status: TileStatuses.Status

    var body: some View {
        Circle().fill(color).frame(width: 9, height: 9)
            .accessibilityLabel(Text(verbatim: status.message.isEmpty ? status.level : "\(status.level): \(status.message)"))
    }

    private var color: Color {
        switch status.level {
        case "error": return .red
        case "warn": return .orange
        case "ok": return .green
        default: return .blue
        }
    }
}
