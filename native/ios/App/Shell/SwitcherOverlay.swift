import SwiftUI
import XbinCore

/// The workspace switcher (plans/native.md §4): every workspace with its
/// branding, what needs you there, and when it was last used; recents across
/// workspaces below. An overlay that goes away once something is picked.
struct SwitcherOverlay: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        ZStack(alignment: .top) {
            Rectangle().fill(.black.opacity(0.35)).ignoresSafeArea()
                .onTapGesture { withAnimation { app.showSwitcher = false } }
            VStack(spacing: 0) {
                List {
                    Section("Workspaces") {
                        ForEach(Array(app.workspaces.enumerated()), id: \.element.id) { i, w in
                            Button { withAnimation { app.select(w.id) } } label: { row(w, index: i) }
                        }
                        .onMove { app.move(from: $0, to: $1) }
                        Button("Add a workspace", systemImage: "plus") {
                            app.showSwitcher = false
                            app.addRequest = .blank
                        }
                    }
                    let recents = app.recents.prefix(8)
                    if !recents.isEmpty {
                        Section("Recent") {
                            ForEach(Array(recents)) { r in
                                Button {
                                    if let w = app.workspace(r.workspace) {
                                        app.select(w.id)
                                        w.open(r.surface)
                                    }
                                } label: {
                                    HStack {
                                        Text(verbatim: r.title)
                                        Spacer()
                                        Text(verbatim: app.workspace(r.workspace)?.title ?? "").font(.caption).foregroundStyle(.secondary)
                                    }
                                }
                            }
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
            if w.id == app.selectedID { Image(systemName: "checkmark").foregroundStyle(.tint) }
        }
    }
}
