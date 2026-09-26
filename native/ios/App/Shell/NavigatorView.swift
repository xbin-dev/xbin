import SwiftUI
import XbinCore
import XbinRendererModel
import XbinTerm

/// The tile navigator (plans/native.md §4): the user's screens and org
/// screens, folders (personal and shared), personal tiles, and search over
/// every tile they can see. Each tile says whether it opens natively, and
/// shows the icon and badge its native UI set (`xbin.native.meta`). As an
/// overlay it dismisses once something is picked (§15). Where the device
/// has windows, a tile opens in a new one from its menu or by dragging it
/// out (Stage Manager).
struct NavigatorView: View {
    let workspace: WorkspaceModel
    var overlay = false

    @Environment(WorkspaceNav.self) private var nav
    @Environment(SceneModel.self) private var scene
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var screen: String?

    var body: some View {
        List {
            if !query.isEmpty {
                Section { ForEach(workspace.catalog.search(query)) { TileRow(workspace: workspace, tile: $0, pick: pick) } }
            } else {
                shortcuts
                if let nav = workspace.navigator {
                    if !nav.screens.isEmpty { screens(nav) }
                    ForEach(nav.sections) { s in section(s) }
                } else if workspace.loading {
                    HStack { Spacer(); ProgressView(); Spacer() }
                }
                if let e = workspace.lastError, workspace.signInProblem == nil {
                    Section { Label { Text(verbatim: e) } icon: { Image(systemName: "exclamationmark.triangle") }.foregroundStyle(.red) }
                }
            }
        }
        .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .automatic), prompt: "Search tiles")
        .refreshable { await workspace.refresh() }
        .navigationTitle(Text(verbatim: overlay ? "Tiles" : workspace.title))
        .toolbar {
            if overlay { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
    }

    private func pick(_ s: Surface) {
        workspace.open(s, in: nav)
        if overlay { dismiss() }
    }

    @ViewBuilder private var shortcuts: some View {
        Section {
            let waiting = workspace.needsYou.count
            Button { scene.showInbox = true } label: {
                Label { HStack { Text("Needs you"); Spacer(); if waiting > 0 { Text(verbatim: "\(waiting)").foregroundStyle(.secondary) } } }
                    icon: { Image(systemName: "tray") }
            }
            let shells = workspace.sessions.filter { $0.kind == .shell }
            if !shells.isEmpty {
                Menu {
                    ForEach(shells) { s in
                        Button("\(s.title) · \(TileInfo.humanize(s.cwd))") { pick(.terminal(cwd: s.cwd, session: s.id)) }
                    }
                } label: { Label("Terminals (\(shells.count))", systemImage: "apple.terminal") }
            }
            let agents = workspace.sessions.filter { $0.kind == .agent }
            if !agents.isEmpty {
                Menu {
                    ForEach(agents) { s in
                        Button("\(s.title) · \(TileInfo.humanize(s.cwd))\(s.needsYou ? " · waiting" : "")") {
                            pick(.agent(cwd: s.cwd, session: s.id))
                        }
                    }
                } label: { Label("Agents (\(agents.count))", systemImage: "sparkles") }
            }
            Button { scene.showSettings = true } label: { Label("Settings", systemImage: "gearshape") }
        }
    }

    @ViewBuilder private func screens(_ nav: NavigatorModel) -> some View {
        let current = nav.screens.first { $0.id == (screen ?? workspace.layout.active) } ?? nav.screens[0]
        Section {
            if nav.screens.count > 1 {
                Picker("Screen", selection: Binding(get: { current.id }, set: { screen = $0 })) {
                    ForEach(nav.screens) { s in Text(verbatim: s.name).tag(s.id) }
                }
                .pickerStyle(.menu)
            }
            ForEach(current.tiles, id: \.self) { path in
                if let t = workspace.tile(path) { TileRow(workspace: workspace, tile: t, pick: pick) }
            }
        } header: {
            Text(verbatim: current.kind == .org ? "\(current.name) · \(current.org ?? "org")" : current.name)
        }
    }

    @ViewBuilder private func section(_ s: NavigatorModel.Section) -> some View {
        Section {
            ForEach(s.folders) { f in FolderRow(workspace: workspace, folder: f, pick: pick) }
            ForEach(s.tiles) { t in TileRow(workspace: workspace, tile: t, pick: pick) }
        } header: { Text(verbatim: s.title) }
    }
}

private struct FolderRow: View {
    let workspace: WorkspaceModel
    let folder: NavigatorModel.FolderNode
    let pick: (Surface) -> Void

    var body: some View {
        DisclosureGroup {
            ForEach(folder.children) { c in FolderRow(workspace: workspace, folder: c, pick: pick) }
            ForEach(folder.tiles) { t in TileRow(workspace: workspace, tile: t, pick: pick) }
        } label: {
            Label { Text(verbatim: folder.name) } icon: { Image(systemName: "folder") }
        }
    }
}

/// A tile: its icon and name, its path, its badge, how it opens.
struct TileRow: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let pick: (Surface) -> Void

    @Environment(\.openWindow) private var openWindow
    @Environment(\.supportsMultipleWindows) private var multipleWindows

    var body: some View {
        let kind = workspace.surfaceKind(for: tile)
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
                if let badge = meta?.badge { TileBadge(text: badge) }
                switch kind {
                case .native:
                    Text("native").font(.caption2.bold()).padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Color.xbinAmber.opacity(0.25), in: Capsule())
                case .safari:
                    Image(systemName: "safari").foregroundStyle(.secondary)
                case .web:
                    EmptyView()
                }
            }
        }
        .onDrag { Self.dragItem(workspace, tile) }
        .contextMenu {
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
