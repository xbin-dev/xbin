import SwiftUI
import UIKit
import XbinCore
import XbinTerm

/// Hosts the terminal's UIKit view (SwiftTerm + the prediction overlay).
struct TerminalHost: UIViewRepresentable {
    let controller: TerminalController
    func makeUIView(context: Context) -> UIView { controller.container }
    func updateUIView(_ uiView: UIView, context: Context) {}
}

/// One full-screen terminal (plans/native.md §12, §15): more screen means
/// more columns and rows, never a second pane (on a Duo in tabletop, the
/// terminal above the fold and keys below: TerminalArea). Sessions, the
/// network scope and the VM toggle live in a sheet over it; scrollback search
/// (⌘F) and the precise selection mode in a bar under it.
struct TerminalScreen: View {
    let workspace: WorkspaceModel
    let cwd: String
    var sessionID: String?
    /// Typed into a fresh session once (an agent's sign-in command).
    var initialInput: String?
    var onExit: (() -> Void)?

    @State private var controller: TerminalController?
    @State private var showSessions = false
    @State private var showKeyboardSettings = false
    @State private var fullScreen = false
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.openURL) private var openURL

    var body: some View {
        ZStack(alignment: .top) {
            Color.black.ignoresSafeArea()
            if let c = controller {
                TerminalArea(controller: c)
                    .ignoresSafeArea(.container, edges: fullScreen ? .all : [])
                status(c)
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if let c = controller {
                if c.selecting {
                    TerminalSelectionBar(controller: c)
                } else if c.find.visible {
                    TerminalFindBar(controller: c)
                }
            }
        }
        .navigationTitle(Text(verbatim: controller?.title.isEmpty == false ? controller!.title : "Terminal · \(TileInfo.humanize(cwd))"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(fullScreen ? .hidden : .visible, for: .navigationBar)
        .statusBarHidden(fullScreen)
        .persistentSystemOverlays(fullScreen ? .hidden : .automatic)
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                if let c = controller, c.lagging, let rtt = c.rtt {
                    Text(verbatim: "\(Int(rtt)) ms").font(.caption.monospacedDigit()).foregroundStyle(.orange)
                }
                Button { showSessions = true } label: { Image(systemName: "rectangle.stack") }
                    .accessibilityLabel("Sessions")
                Menu {
                    Button("Find", systemImage: "magnifyingglass") { controller?.openFind() }
                    Button("Select Text", systemImage: "selection.pin.in.out") { controller?.enterSelectionMode() }
                    Button("Keyboard", systemImage: "keyboard") { showKeyboardSettings = true }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .accessibilityLabel("More")
                Button { withAnimation { fullScreen.toggle() } } label: {
                    Image(systemName: fullScreen ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right")
                }
                .accessibilityLabel("Full screen")
            }
        }
        .overlay(alignment: .topTrailing) {
            if fullScreen {
                Button { withAnimation { fullScreen = false } } label: {
                    Image(systemName: "arrow.down.right.and.arrow.up.left").padding(8).background(.ultraThinMaterial, in: Circle())
                }
                .padding(8)
            }
        }
        .onAppear {
            if controller == nil {
                let c = TerminalController(workspace: workspace, cwd: cwd, initialInput: initialInput)
                controller = c
                Task { await c.start(session: sessionID) }
            }
            controller?.setVisible(true)
            if controller?.selecting != true, controller?.find.visible != true {
                _ = controller?.terminalView.becomeFirstResponder()
            }
        }
        .onDisappear { controller?.setVisible(false) }
        .onChange(of: scenePhase) { _, p in controller?.setVisible(p == .active) }
        .sheet(isPresented: $showSessions) {
            if let c = controller { TermSessionsSheet(controller: c) }
        }
        .sheet(isPresented: $showKeyboardSettings) {
            NavigationStack {
                TerminalKeyboardSettingsView()
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { showKeyboardSettings = false } } }
            }
            .presentationDetents([.medium, .large])
        }
        .alert("Open this link?", isPresented: Binding(get: { controller?.pendingLink != nil }, set: { if !$0 { controller?.pendingLink = nil } })) {
            Button("Cancel", role: .cancel) { controller?.pendingLink = nil }
            Button("Open in Safari") {
                if let u = controller?.pendingLink { openURL(u) }
                controller?.pendingLink = nil
            }
        } message: {
            Text(verbatim: controller?.pendingLink?.absoluteString ?? "")
        }
        // ⌘K/⌘T/⌘⇧[ ]/⌘F/⌘G: handled by the terminal view itself (hardware keys).
    }

    @ViewBuilder private func status(_ c: TerminalController) -> some View {
        switch c.phase {
        case .connecting, .handshaking:
            Banner(text: "Connecting…", busy: true)
        case .reconnecting(let attempt, _):
            Banner(text: "Reconnecting (\(attempt))…", busy: true)
        case .exited:
            Banner(text: "The session ended.", action: ("New session", { Task { await c.newSession() } }))
        case .failed(let f):
            Banner(text: Self.describe(f, notice: c.notice), action: ("Retry", { Task { await c.start(session: c.info?.id) } }))
        default:
            if let n = c.notice {
                Banner(text: n, action: ("OK", { c.notice = nil }))
            } else if !c.netNote.isEmpty {
                Banner(text: c.netNote, action: ("OK", { c.netNote = "" }))
            }
        }
    }

    static func describe(_ f: TermSession.Failure, notice: String?) -> String {
        switch f {
        case .disconnected: return "Disconnected."
        case .unauthorized: return notice ?? "Signed out — signing in again…"
        case .forbidden(let why): return "Not allowed: \(why.isEmpty ? "no terminal on this tile" : why)"
        case .sessionGone: return "That session is gone."
        case .refused(let status, let reason): return "Refused (\(status))\(reason.isEmpty ? "" : ": \(reason)")"
        }
    }
}

struct Banner: View {
    var text: String
    var busy = false
    var action: (String, () -> Void)?

    var body: some View {
        HStack(spacing: 10) {
            if busy { ProgressView().controlSize(.small) }
            Text(verbatim: text).font(.footnote).lineLimit(3)
            if let action {
                Button(action.0, action: action.1).font(.footnote.bold())
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 8)
        .background(.regularMaterial, in: Capsule())
        .padding(.top, 8)
    }
}

/// Sessions on this tile (D73) — attach, rename, end, start another — and
/// the new-session pickers: network scope (D54/D65) and the VM sandbox
/// (D89/D90). A change of either restarts the session, so it asks first.
struct TermSessionsSheet: View {
    let controller: TerminalController
    @Environment(\.dismiss) private var dismiss
    @State private var renaming: TermDirectoryEntry?
    @State private var newName = ""
    @State private var confirmNet: TermNetChoice?
    @State private var confirmVM = false
    @State private var confirmReset = false

    var workspace: WorkspaceModel { controller.workspace }

    var body: some View {
        NavigationStack {
            List {
                Section("Sessions on \(TileInfo.humanize(controller.cwd))") {
                    ForEach(TermDirectory.forTile(workspace.sessions, cwd: controller.cwd).filter { $0.kind == .shell }) { s in
                        Button {
                            Task { await controller.attach(s.id) }
                            dismiss()
                        } label: {
                            HStack {
                                VStack(alignment: .leading) {
                                    Text(verbatim: s.title).font(.body)
                                    Text(verbatim: "\(s.label.isEmpty ? s.net : s.label)\(s.vm ? " · VM" : "") · \(s.clients) attached")
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                                Spacer()
                                if s.id == controller.info?.id { Image(systemName: "checkmark").foregroundStyle(.tint) }
                            }
                        }
                        .swipeActions {
                            Button("End", role: .destructive) {
                                Task {
                                    _ = try? await workspace.auth.send(APIRequest("DELETE", TermDirectory.sessionPath(s.id)))
                                    await workspace.refreshSessions()
                                }
                            }
                            Button("Rename") { newName = s.name; renaming = s }.tint(.blue)
                        }
                    }
                    Button("New session", systemImage: "plus") {
                        Task { await controller.newSession() }
                        dismiss()
                    }
                }
                Section {
                    NavigationLink {
                        TerminalKeyboardSettingsView()
                    } label: {
                        Label("Keyboard", systemImage: "keyboard")
                    }
                }
                if let info = controller.info {
                    Section {
                        ForEach(TermNetChoice.choices(info)) { n in
                            Button {
                                if !n.current { confirmNet = n }
                            } label: {
                                HStack {
                                    VStack(alignment: .leading) {
                                        Text(verbatim: n.label)
                                        if !n.desc.isEmpty { Text(verbatim: n.desc).font(.caption).foregroundStyle(.secondary) }
                                    }
                                    Spacer()
                                    if n.current { Image(systemName: "checkmark").foregroundStyle(.tint) }
                                }
                            }
                        }
                    } header: { Text("Network") } footer: {
                        if !controller.netNote.isEmpty { Text(verbatim: controller.netNote) }
                    }
                    let vm = TermVMChoice(env: controller.env, session: info)
                    Section {
                        Toggle("VM sandbox", isOn: Binding(get: { vm.on }, set: { _ in confirmVM = true }))
                            .disabled(!vm.available && !vm.on)
                        Button("Reset the environment…", role: .destructive) { confirmReset = true }
                    } footer: {
                        if !vm.note.isEmpty { Text(verbatim: vm.note) }
                    }
                }
            }
            .navigationTitle("Terminal")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task { await workspace.refreshSessions(); await controller.loadEnv() }
            .alert("Rename session", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
                TextField("Name", text: $newName)
                Button("Cancel", role: .cancel) { renaming = nil }
                Button("Save") {
                    if let s = renaming {
                        Task {
                            _ = try? await workspace.auth.send(APIRequest("PATCH", TermDirectory.sessionPath(s.id),
                                                                         headers: ["Content-Type": "application/json"],
                                                                         body: TermDirectory.renameBody(newName)))
                            await workspace.refreshSessions()
                        }
                    }
                    renaming = nil
                }
            }
            .confirmationDialog("Restart on \(confirmNet?.label ?? "")?", isPresented: Binding(get: { confirmNet != nil }, set: { if !$0 { confirmNet = nil } }), titleVisibility: .visible) {
                Button("Restart the session") {
                    if let n = confirmNet, let info = controller.info {
                        Task { await controller.restart(with: TermNewSession(cwd: controller.cwd, net: n.id, vm: info.vm)) }
                    }
                    confirmNet = nil
                }
            } message: { Text("The network is fixed when a session starts: this one ends and a new one opens.") }
            .confirmationDialog("Restart \(controller.info?.vm == true ? "without" : "in") a VM?", isPresented: $confirmVM, titleVisibility: .visible) {
                Button("Restart the session") {
                    if let info = controller.info {
                        Task { await controller.restart(with: TermNewSession(cwd: controller.cwd, net: info.net, vm: !info.vm)) }
                    }
                }
            } message: { Text("The sandbox is fixed when a session starts: this one ends and a new one opens.") }
            .confirmationDialog("Reset this tile's terminal environment?", isPresented: $confirmReset, titleVisibility: .visible) {
                Button("Reset", role: .destructive) { Task { await controller.resetEnvironment() } }
            } message: { Text("Installed packages and files outside the tile are wiped; the tile's own files stay.") }
        }
        .presentationDetents([.medium, .large])
    }
}
