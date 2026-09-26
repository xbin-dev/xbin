import SwiftUI
import XbinCore
import XbinTerm

/// Needs you (plans/native.md §4): what waits for the user across every
/// workspace — agent sessions waiting on a permission or a question (from
/// each workspace's session directory, kept live by its events socket).
/// Tapping opens it in its workspace, in this window.
struct InboxView: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openWindow) private var openWindow
    @Environment(\.supportsMultipleWindows) private var multipleWindows

    var body: some View {
        NavigationStack {
            List {
                ForEach(app.workspaces) { w in
                    let waiting = w.needsYou
                    if !waiting.isEmpty {
                        Section {
                            ForEach(waiting) { s in
                                Button {
                                    scene.select(w.id)
                                    w.open(.agent(cwd: s.cwd, session: s.id), in: scene.nav(for: w))
                                    dismiss()
                                } label: {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(verbatim: s.title)
                                        Text(verbatim: "\(TileInfo.humanize(s.cwd)) · \(s.waitingFor)")
                                            .font(.caption).foregroundStyle(.secondary)
                                    }
                                }
                                .contextMenu {
                                    if multipleWindows {
                                        Button("Open in New Window", systemImage: "macwindow.badge.plus") {
                                            dismiss()
                                            openWindow(value: WindowTarget(workspace: w.id, surface: .agent(cwd: s.cwd, session: s.id)))
                                        }
                                    }
                                }
                            }
                        } header: {
                            HStack { BrandIcon(workspace: w, size: 18); Text(verbatim: w.title) }
                        }
                    }
                }
                if app.needsYouCount == 0 {
                    ContentUnavailableView("Nothing needs you", systemImage: "checkmark.circle",
                                           description: Text("Agent permissions and questions from every workspace show up here."))
                }
            }
            .refreshable { for w in app.workspaces { await w.refreshSessions() } }
            .navigationTitle("Needs you")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task { for w in app.workspaces { await w.refreshSessions() } }
        }
    }
}

/// Settings: this window's workspace (account, devices and adding one,
/// push, native views, sign out, remove) and the app (lock, native views,
/// haptics, Handoff, Live Activities, predictive echo, the terminal
/// keyboard, the push relay).
struct SettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.dismiss) private var dismiss
    @State private var devices: [DeviceInfo] = []
    @State private var pushNote: String?
    @State private var confirmRemove = false
    @State private var addingDevice = false
    @State private var appLock = AppSettings.appLock
    @State private var predict = AppSettings.predictMode
    @State private var relay = AppSettings.pushRelay
    @State private var nativeViews = !AppSettings.nativeViewsOff
    @State private var haptics = AppSettings.haptics
    @State private var handoff = AppSettings.handoff
    @State private var liveActivities = LiveActivities.enabled

    var body: some View {
        NavigationStack {
            Form {
                if let w = scene.selected {
                    Section {
                        LabeledContent("Signed in as") { Text(verbatim: w.userLabel) }
                        LabeledContent("Server") { Text(verbatim: w.origin.origin).font(.callout.monospaced()) }
                        LabeledContent("Sign-in") { Text(w.canResign ? "This device (Face ID)" : "Token (not renewed)") }
                    } header: { Text(verbatim: w.title) }

                    Section {
                        ForEach(devices) { d in
                            HStack {
                                VStack(alignment: .leading) {
                                    Text(verbatim: d.name + (d.current ? " (this device)" : ""))
                                    Text(verbatim: "\(d.platform) · last used \(d.lastUsed.map { $0.formatted(.relative(presentation: .named)) } ?? "never")")
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                            }
                            .swipeActions {
                                if !d.current {
                                    Button("Remove", role: .destructive) {
                                        Task {
                                            _ = try? await w.auth.send(APIRequest("DELETE", "\(AppAuthRoute.devices)/\(URLComponent.encode(d.id))"))
                                            await loadDevices(w)
                                        }
                                    }
                                }
                            }
                        }
                        Button("Add another device…", systemImage: "qrcode") { addingDevice = true }
                    } header: { Text("Devices") } footer: {
                        Text("Shows a QR code the new device scans; it gets its own key and signs in with Face ID.")
                    }

                    Section {
                        Button("Open in Safari", systemImage: "safari") { Task { await w.openInSafari(path: "/") } }
                        if let gate = NativeRuntimeGate.workspace(nativeRuntime: w.whoami?.nativeRuntime, loaded: w.whoami != nil) {
                            Label { Text(verbatim: gate.explanation) } icon: { Image(systemName: "globe") }
                                .font(.footnote).foregroundStyle(.secondary)
                        }
                    } header: { Text("This workspace on the web") } footer: {
                        Text("Opens as you where the workspace supports it — tap “Continue as …” once; otherwise the browser asks you to sign in.")
                    }

                    Section {
                        Button("Send a test notification") { Task { pushNote = await PushManager.shared.sendTest(w) } }
                            .disabled(PushManager.shared.relay == nil)
                        if let pushNote { Text(verbatim: pushNote).font(.footnote) }
                    } header: { Text("Notifications") } footer: {
                        Text(PushManager.shared.relay == nil
                             ? "No push relay is configured for this build (set one below)."
                             : "Agent permissions, questions and finished turns, and tiles that notify you.")
                    }

                    Section {
                        Button("Sign out") { Task { await w.auth.signOut(); await w.refresh() } }
                        Button("Remove this workspace…", role: .destructive) { confirmRemove = true }
                    }
                }

                Section("App") {
                    Toggle("Require Face ID to open", isOn: $appLock)
                        .onChange(of: appLock) { _, v in AppSettings.appLock = v }
                    Toggle("Native views", isOn: $nativeViews)
                        .onChange(of: nativeViews) { _, v in
                            AppSettings.nativeViewsOff = !v
                            app.runtimeSwitchChanged()
                        }
                    if let gate = app.runtimeGate, gate != .user {
                        Text(verbatim: gate.explanation).font(.footnote).foregroundStyle(.secondary)
                    }
                    Toggle("Haptics", isOn: $haptics)
                        .onChange(of: haptics) { _, v in AppSettings.haptics = v }
                    Toggle("Handoff", isOn: $handoff)
                        .onChange(of: handoff) { _, v in AppSettings.handoff = v }
                    Toggle("Live Activities", isOn: $liveActivities)
                        .onChange(of: liveActivities) { _, v in LiveActivities.enabled = v }
                    Picker("Predictive echo", selection: $predict) {
                        Text("Auto").tag("auto"); Text("On").tag("on"); Text("Off").tag("off")
                    }
                    .onChange(of: predict) { _, v in AppSettings.predictMode = v }
                    NavigationLink {
                        TerminalKeyboardSettingsView()
                    } label: {
                        Label("Terminal keyboard", systemImage: "keyboard")
                    }
                    TextField("Push relay URL", text: $relay)
                        .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                        .onSubmit {
                            AppSettings.pushRelay = relay
                            Task { await PushManager.shared.start(); await PushManager.shared.maintainAll() }
                        }
                    LabeledContent("Version") { Text(verbatim: AppInfo.version) }
                }
            }
            .navigationTitle("Settings")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task { if let w = scene.selected { await loadDevices(w) } }
            .sheet(isPresented: $addingDevice, onDismiss: { if let w = scene.selected { Task { await loadDevices(w) } } }) {
                if let w = scene.selected { AddDeviceView(workspace: w) }
            }
            .confirmationDialog("Remove \(scene.selected?.title ?? "")?", isPresented: $confirmRemove, titleVisibility: .visible) {
                Button("Remove and revoke this device", role: .destructive) {
                    if let w = scene.selected { Task { await app.remove(w.id, removeDevice: true); dismiss() } }
                }
                Button("Remove from this app only") {
                    if let w = scene.selected { Task { await app.remove(w.id, removeDevice: false); dismiss() } }
                }
            } message: {
                Text("Its key, session and web data are deleted from this device.")
            }
        }
    }

    private func loadDevices(_ w: WorkspaceModel) async {
        guard let j = try? await w.auth.json(APIRequest("GET", AppAuthRoute.devices)) else { return }
        devices = DeviceInfo.list(json: j)
    }
}
