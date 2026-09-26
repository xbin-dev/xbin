import SwiftUI
import XbinCore
import XbinRendererModel

/// The workspace switcher (plans/native.md §4): every workspace with its
/// branding, what needs you there, and when it was last used; recents across
/// workspaces below (a tile's icon and badge when its native UI set them).
/// An overlay that goes away once something is picked — in this window only
/// (each window has its own workspace); "Open in New Window" where the
/// device has windows.
struct SwitcherOverlay: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.openWindow) private var openWindow
    @Environment(\.supportsMultipleWindows) private var multipleWindows

    var body: some View {
        ZStack(alignment: .top) {
            Rectangle().fill(.black.opacity(0.35)).ignoresSafeArea()
                .onTapGesture { withAnimation { scene.showSwitcher = false } }
            VStack(spacing: 0) {
                List {
                    Section("Workspaces") {
                        ForEach(Array(app.workspaces.enumerated()), id: \.element.id) { i, w in
                            Button {
                                if w.id != scene.selectedID { Haptics.select() }
                                withAnimation { scene.select(w.id) }
                            } label: { row(w, index: i) }
                                .contextMenu {
                                    if multipleWindows {
                                        Button("Open in New Window", systemImage: "macwindow.badge.plus") {
                                            scene.showSwitcher = false
                                            openWindow(value: WindowTarget(workspace: w.id))
                                        }
                                    }
                                }
                        }
                        .onMove { app.move(from: $0, to: $1) }
                        Button("Add a workspace", systemImage: "plus") {
                            scene.showSwitcher = false
                            scene.addRequest = .blank
                        }
                    }
                    let recents = app.recents.prefix(8)
                    if !recents.isEmpty {
                        Section("Recent") {
                            ForEach(Array(recents)) { r in recentRow(r) }
                        }
                    }
                }
                .listStyle(.insetGrouped)
                .scrollContentBackground(.hidden)
                .frame(maxHeight: 520)
            }
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 22))
            .padding(.horizontal, 10)
            .padding(.top, 6)
            .frame(maxWidth: 560)
        }
    }

    private func recentRow(_ r: Recent) -> some View {
        let w = app.workspace(r.workspace)
        let meta = Self.meta(r, w)
        return Button {
            if let w {
                scene.select(w.id)
                w.open(r.surface, in: scene.nav(for: w))
            }
        } label: {
            HStack {
                if let symbol = meta?.symbol {
                    Image(systemName: symbol).foregroundStyle(.secondary).frame(width: 22)
                }
                Text(verbatim: r.title)
                Spacer()
                if let badge = meta?.badge { TileBadge(text: badge) }
                Text(verbatim: w?.title ?? "").font(.caption).foregroundStyle(.secondary)
            }
        }
        .contextMenu {
            if multipleWindows, w != nil {
                Button("Open in New Window", systemImage: "macwindow.badge.plus") {
                    scene.showSwitcher = false
                    openWindow(value: WindowTarget(workspace: r.workspace, surface: r.surface))
                }
            }
        }
    }

    private static func meta(_ r: Recent, _ w: WorkspaceModel?) -> TileMeta? {
        guard let w, case .tile(let path, _, _) = r.surface else { return nil }
        return w.tileMeta[path]
    }

    private func row(_ w: WorkspaceModel, index: Int) -> some View {
        HStack(spacing: 12) {
            BrandIcon(workspace: w, size: 34)
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: w.title).font(.headline).foregroundStyle(.primary)
                Text(verbatim: "\(w.userLabel) · \(w.origin.authority)").font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            let waiting = w.needsYou.count
            if waiting > 0 {
                Text(verbatim: "\(waiting)").font(.caption.bold()).padding(.horizontal, 7).padding(.vertical, 2)
                    .background(Color.xbinAmber, in: Capsule()).foregroundStyle(.black)
            }
            if let t = w.lastActivity ?? w.record.lastUsedAt {
                Text(t, style: .relative).font(.caption2).foregroundStyle(.secondary)
            }
            if index < 9 { Text(verbatim: "⌘\(index + 1)").font(.caption2.monospaced()).foregroundStyle(.tertiary) }
            if w.id == scene.selectedID { Image(systemName: "checkmark").foregroundStyle(.tint) }
        }
    }
}

/// A native tile's badge (`xbin.native.meta({badge})`).
struct TileBadge: View {
    let text: String

    var body: some View {
        Text(verbatim: text).font(.caption2.bold()).lineLimit(1)
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background(Color.xbinAmber.opacity(0.9), in: Capsule()).foregroundStyle(.black)
            .accessibilityLabel(Text(verbatim: text))
    }
}
