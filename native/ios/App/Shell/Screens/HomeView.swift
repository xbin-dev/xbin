import SwiftUI
import UIKit
import XbinCore
import XbinRenderer
import XbinTerm

/// Home, a workspace's first panel (plans/native.md §4, D125, D128): the
/// workspace's own icon and title on top, what needs you, then its screens
/// only — Mine, each org, Workspace, inside their folders as the web
/// sidebar files them (HomeModel) — and search and "All tiles" at the
/// bottom, in compact single-line rows. A screen opens as the next panel;
/// "New screen" adds a personal one to the layout the web shell uses too.
/// Terminals and agents are reached through their tiles (a row's or card's
/// long press) and the inbox.
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
                    ForEach(workspace.catalog.search(query)) {
                        TileRow(workspace: workspace, tile: $0, caption: $0.parent, pick: open)
                    }
                }
            } else {
                Section { NeedsYouRow(workspace: workspace) }
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
                    Section { Label { Text(verbatim: e) } icon: { Image(systemName: XbinGlyphs.symbol("error")) }.foregroundStyle(XbinColor.danger) }
                }
                Section {
                    NavigationLink {
                        AllTilesView(workspace: workspace, pick: open)
                    } label: {
                        Label("All tiles", systemImage: "square.stack.3d.up")
                    }
                    .compactRow()
                    Button { scene.showSettings = true } label: { Label("Settings", systemImage: "gearshape") }
                        .compactRow()
                }
            }
        }
        .compactList()
        .searchable(text: $query, prompt: "Search tiles")
        .refreshable { await workspace.refresh() }
        .navigationTitle(Text(verbatim: workspace.title))
        .toolbar {
            ToolbarItem(placement: .principal) { WorkspaceHeader(workspace: workspace) }
        }
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
            .compactRow()
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
        ScreenRow(workspace: workspace, screen: s)
    }

    /// A folder and what it files, open unless the user folded it.
    private func folder(_ f: HomeModel.Folder, depth: Int) -> AnyView {
        AnyView(DisclosureGroup(isExpanded: Binding(get: { !collapsed.contains(f.id) }, set: { open in
            if open { collapsed.remove(f.id) } else { collapsed.insert(f.id) }
        })) {
            ForEach(f.children) { c in folder(c, depth: depth + 1) }
            ForEach(f.screens) { screenRow($0) }
        } label: {
            Label { Text(verbatim: f.name).lineLimit(1) } icon: { Image(systemName: "folder") }
        }
        .compactRow())
    }
}

/// Home's header: the workspace's branding icon and title (D76) — its
/// address only when it has no title.
private struct WorkspaceHeader: View {
    let workspace: WorkspaceModel

    var body: some View {
        HStack(spacing: 7) {
            BrandIcon(workspace: workspace, size: 22)
            Text(verbatim: workspace.title).font(.headline).lineLimit(1).truncationMode(.middle)
        }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier("workspace-header")
    }
}

/// A screen on Home or in All tiles: its name, its card count; opens it.
struct ScreenRow: View {
    let workspace: WorkspaceModel
    let screen: ScreenInfo
    @Environment(WorkspaceNav.self) private var nav

    var body: some View {
        Button { nav.openScreen(screen.id) } label: {
            HStack(spacing: 8) {
                Label { Text(verbatim: screen.name).lineLimit(1) } icon: {
                    Image(systemName: "square.grid.2x2").foregroundStyle(XbinColor.muted)
                }
                Spacer(minLength: 4)
                let n = workspace.cards(for: screen).count
                Text(verbatim: "\(n)").font(.callout.monospacedDigit()).foregroundStyle(.secondary)
                    .accessibilityLabel(Text("\(n) tiles"))
                Image(systemName: "chevron.forward").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
            }
        }
        .tint(.primary) // a place to go, not an action: the title in the text color
        .compactRow()
        .accessibilityIdentifier("screen:\(screen.id)")
    }
}

/// What needs you (the inbox), with its count — Home's first row.
private struct NeedsYouRow: View {
    let workspace: WorkspaceModel
    @Environment(SceneModel.self) private var scene

    var body: some View {
        let n = workspace.needsYou.count
        Button { scene.showInbox = true } label: {
            HStack(spacing: 8) {
                Label("Needs you", systemImage: n > 0 ? "tray.full" : "tray")
                Spacer(minLength: 4)
                Text(verbatim: "\(n)").font(.callout.monospacedDigit().weight(n > 0 ? .bold : .regular))
                    .padding(.horizontal, n > 0 ? 7 : 0).padding(.vertical, 1)
                    .background(n > 0 ? XbinColor.accent : Color.clear, in: .xbinPlate)
                    .foregroundStyle(n > 0 ? XbinColor.onAccent : XbinColor.muted)
            }
        }
        .tint(.primary)
        .compactRow()
        .accessibilityLabel(Text("Needs you"))
        .accessibilityValue(Text(verbatim: "\(n)"))
    }
}

/// Every tile the user can see, as the web sidebar shows them (D128,
/// NavigatorModel): personal folders first, then each owner's section —
/// its shared folders, its tiles, an org's screens. Folders open as the
/// user left them on the web; search lists tiles flat.
struct AllTilesView: View {
    let workspace: WorkspaceModel
    let pick: (Surface) -> Void

    @State private var query = ""
    /// Folders the user opened or folded here (from the web's state).
    @State private var flipped: Set<String> = []
    @State private var folded: Set<String> = []

    var body: some View {
        let tree = workspace.navigator
        List {
            if !query.isEmpty {
                Section {
                    ForEach(workspace.catalog.search(query)) {
                        TileRow(workspace: workspace, tile: $0, caption: $0.parent, pick: pick)
                    }
                }
            } else {
                if !tree.folders.isEmpty {
                    Section {
                        ForEach(tree.folders) { f in folder(f) }
                    }
                }
                ForEach(tree.sections) { s in
                    if tree.showsSectionHeaders {
                        Section(isExpanded: Binding(get: { !folded.contains(s.id) }, set: { open in
                            if open { folded.remove(s.id) } else { folded.insert(s.id) }
                        })) {
                            ForEach(s.items) { item($0) }
                        } header: {
                            HStack(spacing: 6) {
                                if s.id == "mine" { Image(systemName: "person.fill") }
                                else if s.id.hasPrefix("org:") { Image(systemName: "flag.fill") }
                                Text(verbatim: s.title)
                                Text(verbatim: "\(s.count)").foregroundStyle(.secondary).monospacedDigit()
                            }
                            .accessibilityElement(children: .combine)
                        }
                    } else {
                        Section { ForEach(s.items) { item($0) } }
                    }
                }
                if tree.isEmpty, workspace.homeLoaded {
                    Text("No tiles yet.").foregroundStyle(.secondary)
                }
            }
        }
        .compactList()
        .searchable(text: $query, prompt: "Search tiles")
        .navigationTitle("All tiles")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func item(_ i: NavigatorModel.Item) -> AnyView {
        switch i {
        case .folder(let f): return folder(f)
        case .tile(let t, let label): return AnyView(TileRow(workspace: workspace, tile: t, label: label, pick: pick))
        case .screen(let s): return AnyView(ScreenRow(workspace: workspace, screen: s))
        }
    }

    private func folder(_ f: NavigatorModel.Folder) -> AnyView {
        let open = Binding(get: { f.open != flipped.contains(f.id) }, set: { now in
            if now == f.open { flipped.remove(f.id) } else { flipped.insert(f.id) }
        })
        return AnyView(DisclosureGroup(isExpanded: open) {
            ForEach(f.items) { item($0) }
        } label: {
            HStack(spacing: 8) {
                Label {
                    Text(verbatim: f.name).lineLimit(1)
                } icon: {
                    if let icon = f.icon, !icon.isEmpty {
                        Text(verbatim: icon)
                    } else {
                        Image(systemName: "folder").foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 4)
                Text(verbatim: "\(f.items.count)").font(.callout.monospacedDigit()).foregroundStyle(.secondary)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("folder:\(f.id)")
        }
        .compactRow())
    }
}

/// A tile in one line: its icon, name (the tree's label, else its title)
/// and a trailing caption (search: where it lives); what runs there, its
/// status, badge and whether it opens natively. Its path is in the
/// accessibility label. The long press: TileMenu.
struct TileRow: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    var label: String? = nil
    var caption: String? = nil
    let pick: (Surface) -> Void

    var body: some View {
        let meta = workspace.tileMeta[tile.path]
        let sessions = workspace.tileSessions[tile.path]
        let status = workspace.statuses[tile.path]
        let native = workspace.surfaceKind(for: tile) == .native
        Button { pick(.tile(tile.path)) } label: {
            HStack(spacing: 8) {
                Group {
                    if let symbol = meta?.symbol {
                        Image(systemName: symbol).foregroundStyle(XbinColor.muted)
                    } else {
                        // The web's live square (shell-kit.js liveSquare,
                        // D184): filled in the accent while the tile is on,
                        // hollow while it is switched off.
                        LiveSquare(on: Self.isOn(tile))
                    }
                }
                .frame(width: 22)
                .accessibilityHidden(true)
                Text(verbatim: label ?? tile.title).foregroundStyle(.primary).lineLimit(1).truncationMode(.middle)
                if let caption, !caption.isEmpty {
                    Text(verbatim: caption).font(.caption.monospaced()).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.head)
                }
                Spacer(minLength: 4)
                if let sessions, !sessions.isEmpty { SessionsBadge(sessions: sessions) }
                if let status { StatusDot(status: status) }
                if let badge = meta?.badge { TileBadge(text: badge) }
                if native {
                    Text("native").font(.caption2.bold()).foregroundStyle(XbinColor.muted)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
                }
                // Partitioned tiles (D181): paused while a mode switch waits
                // for a manager; the marker last, as the web sidebar has it
                // before its ⋯.
                if tile.isPaused { PausedBadge() }
                if tile.isPartitioned { PartitionMark(size: 9) }
            }
        }
        .compactRow()
        .accessibilityLabel(Text(verbatim: "\(label ?? tile.title), \(tile.path)"))
        .accessibilityValue(Text(verbatim: [sessions?.spoken ?? "", status.map { $0.message.isEmpty ? $0.level : "\($0.level): \($0.message)" } ?? "",
                                            meta?.badge ?? "", native ? "native" : "",
                                            tile.isPaused ? TilePartition.pausedText : "",
                                            tile.partition?.markTitle ?? ""].filter { !$0.isEmpty }.joined(separator: ", ")))
        .onDrag { TileMenu.dragItem(workspace, tile) }
        .contextMenu { TileMenu(workspace: workspace, tile: tile, pick: pick) }
    }

    /// A tile is on unless the workspace switched it off (its /components
    /// state: shell-kit.js liveState's 'off').
    static func isOn(_ tile: TileInfo) -> Bool { tile.state.isEmpty || tile.state == "enabled" }
}

/// The live square (product-ui 3, the web's liveSquare): 8 pt, filled in
/// the accent while the tile is on, hollow (the strong edge) while it is
/// off. The runtime its colour used to say is the web's tooltip; here the
/// row's words carry the tile.
struct LiveSquare: View {
    let on: Bool

    var body: some View {
        Rectangle()
            .fill(on ? XbinColor.accent : Color.clear)
            .overlay { if !on { Rectangle().strokeBorder(XbinColor.borderStrong, lineWidth: 1.5) } }
            .frame(width: 8, height: 8)
            .accessibilityLabel(Text(on ? "on" : "switched off"))
    }
}

/// What runs on a tile (D128): `>_ 2` for its terminals, the agents' glyph
/// and 1 for its agents — on the accent when one waits for you. On rows,
/// and in a corner of a screen's card (outside a widget's own tree).
struct SessionsBadge: View {
    let sessions: TileSessions

    var body: some View {
        let waiting = sessions.needsYou > 0
        HStack(spacing: 4) {
            if sessions.shells > 0 {
                Text(verbatim: ">_ \(sessions.shells)").font(.caption2.monospaced().bold())
            }
            if sessions.agents > 0 {
                HStack(spacing: 2) {
                    Image(systemName: waiting ? "exclamationmark.bubble.fill" : XbinGlyphs.symbol("agent")).font(.caption2.weight(.semibold))
                    Text(verbatim: "\(sessions.agents)").font(.caption2.monospacedDigit().bold())
                }
            }
        }
        .padding(.horizontal, 6).padding(.vertical, 2)
        .background(waiting ? XbinColor.accent : XbinColor.fill, in: .xbinPlate)
        .foregroundStyle(waiting ? XbinColor.onAccent : XbinColor.muted)
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: sessions.spoken))
        .accessibilityIdentifier("sessions-badge")
    }
}

/// A tile's long-press menu (rows and cards): the terminals and agents
/// running there (tap one: the tile's sessions screen on its tab), New
/// session… (that screen's launcher, D132), a new window, and the
/// native/web toggle.
struct TileMenu: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let pick: (Surface) -> Void

    @Environment(\.openWindow) private var openWindow
    @Environment(\.supportsMultipleWindows) private var multipleWindows

    var body: some View {
        let sessions = TermDirectory.forTile(workspace.sessions, cwd: tile.path)
        if !sessions.isEmpty {
            Section {
                ForEach(sessions) { s in
                    Button {
                        pick(.sessions(tile: tile.path, show: .session(s.id)))
                    } label: {
                        Text(verbatim: s.title)
                        Text(verbatim: s.statusText)
                        Image(systemName: s.kind == .shell ? "apple.terminal" : s.needsYou ? "exclamationmark.bubble" : XbinGlyphs.symbol("agent"))
                    }
                }
            }
        }
        Section {
            // One way in: the tile's sessions screen, on its launcher.
            Button("New session…", systemImage: "plus.rectangle.on.rectangle") {
                pick(.sessions(tile: tile.path, show: .launcher))
            }
            if multipleWindows {
                Button("Open in New Window", systemImage: "macwindow.badge.plus") {
                    openWindow(value: WindowTarget(workspace: workspace.id, surface: .tile(tile.path)))
                }
            }
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

/// A tile's reported status (`xbin.status`): the level's glyph in its
/// colour — a shape of its own, never a dot alone (the web shell's
/// statusIcon, D184) — named by its word for VoiceOver.
struct StatusDot: View {
    let status: TileStatuses.Status

    var body: some View {
        let s = XbinGlyphs.status(status.level)
        Image(systemName: s.symbol).font(.caption.weight(.semibold)).foregroundStyle(color)
            .accessibilityLabel(Text(verbatim: status.message.isEmpty ? s.word : "\(s.word): \(status.message)"))
    }

    private var color: Color {
        switch status.level {
        case "error": return XbinColor.danger
        case "warn": return XbinColor.warn
        case "ok": return XbinColor.ok
        default: return XbinColor.info
        }
    }
}

/// Compact lists (D128): single-line rows about 36 pt tall.
extension View {
    func compactList() -> some View {
        listStyle(.sidebar).environment(\.defaultMinListRowHeight, 34)
    }

    func compactRow() -> some View {
        listRowInsets(EdgeInsets(top: 3, leading: 16, bottom: 3, trailing: 16))
    }
}
