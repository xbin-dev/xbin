import SwiftUI
import XbinRenderer
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
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.scenePhase) private var phase
    /// A follow-up row's Send and Deny answer as Allow does (D251 review
    /// L1): not before the moment after the list shows, and again whenever
    /// its rows or their order change — a row going moves another under the
    /// finger — or the app comes back to the front.
    @State private var rowArming = AgentArming()

    /// Every row of the list, in order — the agent's and the sessions that
    /// wait, of every workspace: any of them coming or going moves the rows
    /// under the finger, so a change re-arms every row's Send.
    private var order: [String] {
        app.workspaces.flatMap { w in w.agent.needs.map { "\(w.id)/\($0.kind.rawValue):\($0.id)" } + w.needsYou.map { "\(w.id)/session:\($0.id)" } }
    }

    var body: some View {
        NavigationStack {
            List {
                ForEach(app.workspaces) { w in
                    let waiting = w.needsYou
                    let asked = w.agent.needs
                    if !waiting.isEmpty || !asked.isEmpty {
                        Section {
                            // What the person's workspace agent asked (D233):
                            // approvals and proposed admin changes, oldest
                            // first, above the sessions. Review… opens the
                            // app's own sheet. A follow-up whose text the row
                            // shows whole may be sent from the row (D253):
                            // its text, Review…, Deny and Send — armed like
                            // Allow; at an accessibility text size the row
                            // is Review… alone.
                            ForEach(asked) { n in
                                if let inline = n.inline, !typeSize.isAccessibilitySize {
                                    AgentInlineRow(workspace: w, need: n, inline: inline, armed: rowArming.armed) {
                                        scene.select(w.id)
                                        scene.review(n.review, afterInbox: true)
                                    }
                                } else {
                                    Button {
                                        scene.select(w.id)
                                        scene.review(n.review, afterInbox: true)
                                    } label: {
                                        // (at accessibility sizes Review… goes under the words: neither is squeezed)
                                        let row = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 6))
                                                                               : AnyLayout(HStackLayout(spacing: 8))
                                        row {
                                            VStack(alignment: .leading, spacing: 2) {
                                                Text(verbatim: n.title).foregroundStyle(XbinColor.text).multilineTextAlignment(.leading)
                                                if n.kind == .retry {
                                                    // (a start that didn't open, D253: each fact an element of its own)
                                                    ForEach(Array(n.parts.enumerated()), id: \.offset) { _, p in
                                                        Text(verbatim: p).font(.caption).foregroundStyle(XbinColor.muted).multilineTextAlignment(.leading)
                                                    }
                                                } else if !n.note.isEmpty {
                                                    Text(verbatim: n.note).font(.caption.monospacedDigit()).foregroundStyle(XbinColor.muted)
                                                        .multilineTextAlignment(.leading)
                                                }
                                            }
                                            .frame(maxWidth: .infinity, alignment: .leading)
                                            Text(verbatim: n.kind == .retry ? DelegationWords.tryAgain : "Review…").font(.callout.weight(.semibold)).foregroundStyle(XbinColor.accentText)
                                        }
                                    }
                                    .accessibilityIdentifier("inbox-\(n.kind.rawValue):\(n.id)")
                                }
                            }
                            ForEach(waiting) { s in
                                Button {
                                    scene.select(w.id)
                                    w.open(.agent(cwd: s.cwd, session: s.id), in: scene.nav(for: w))
                                    dismiss()
                                } label: {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(verbatim: s.title)
                                        Text(verbatim: "\(TileInfo.humanize(s.cwd)) · \(s.waitingFor)\(s.startedByAgent ? " · started by your agent" : "")")
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
                                           description: Text("Agent permissions and questions, and what your workspace agent asks you to approve, show up here."))
                }
            }
            .refreshable { for w in app.workspaces { await w.refreshSessions(); await w.agent.refresh() } }
            .concreteBackground()
            .navigationTitle("Needs you")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task {
                for w in app.workspaces {
                    await w.refreshSessions()
                    // which saved sign-in a follow-up's session carries, by the person's own name for it
                    await w.agent.readSignInNames(for: w.agent.needs.map(\.signInId))
                }
            }
            .onAppear { rowArming.arm(settle: 0.4) }
            .onChange(of: order) { _, _ in rowArming.arm() }
            .onChange(of: phase) { _, p in if p == .active { rowArming.arm() } else { rowArming.disarm() } }
        }
    }
}

/// A follow-up allowed on its Needs you row (D251 B2, the app's since
/// D253): what it is, the session's facts as chips, ALL of its text — the
/// row is offered only where it fits whole (StartWords.appInPlace) — when it
/// was asked and expires, and Review…, Deny and Send. Send sends what the
/// sheet would, with the whole text's length (xbind allows only all of it);
/// what the row showed changed since: its sheet shows it as it is now.
struct AgentInlineRow: View {
    let workspace: WorkspaceModel
    let need: AgentNeed
    let inline: InPlace
    let armed: Bool
    let review: () -> Void
    @State private var working = false
    @State private var problem = ""

    var body: some View {
        let agent = workspace.agent
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: need.title).font(.body.weight(.semibold)).foregroundStyle(XbinColor.text)
                .accessibilityIdentifier("inbox-title:\(need.id)")
            AgentChips(chips: inline.chips, signInName: agent.signInName(need.signInId))
            // all of it, the goal first: xbin's own brief's standing opening paragraph after it (the web's row, rowText)
            let t = StartWords.rowText(inline.text, all: true, byXbin: inline.byXbin)
            Text(verbatim: t.lead).font(XbinFont.code).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                .background(XbinColor.surface2, in: .xbinPlate)
                .accessibilityLabel(Text(verbatim: "\(StartWords.Sheet.messageRow): \(t.lead)"))
                .accessibilityIdentifier("inbox-text:\(need.id)")
            if !t.opening.isEmpty {
                Text(verbatim: StartWords.Sheet.opensWith + t.opening).font(.caption).foregroundStyle(XbinColor.muted)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("inbox-opening:\(need.id)")
            }
            HStack(spacing: 12) {
                ForEach(Array(need.parts.enumerated()), id: \.offset) { _, p in
                    Text(verbatim: p).font(.caption.monospacedDigit()).foregroundStyle(XbinColor.muted)
                }
            }
            if !problem.isEmpty {
                Label { Text(verbatim: problem) } icon: { Image(systemName: "exclamationmark.octagon") }
                    .font(.callout).foregroundStyle(XbinColor.danger)
            }
            HStack(spacing: 8) {
                Button("Review…", action: review)
                    .buttonStyle(.borderless)
                    .frame(minHeight: 44)
                    .accessibilityIdentifier("inbox-review:\(need.id)")
                Spacer(minLength: 4)
                Button { deny() } label: { Text("Deny").frame(minWidth: 64, minHeight: 36) }
                    .xbinSecondary() // (a styled button: its own target in a list row)
                    .disabled(!armed || working)
                    .accessibilityHint(armed ? Text(verbatim: "") : Text("Available in a moment"))
                    .accessibilityIdentifier("inbox-deny:\(need.id)")
                Button { send() } label: { Text("Send").frame(minWidth: 64, minHeight: 36) }
                    .xbinPrimary()
                    .disabled(!armed || working)
                    .accessibilityLabel(Text(verbatim: "Send: \(need.title)"))
                    .accessibilityHint(armed ? Text(verbatim: "") : Text("Available in a moment"))
                    .accessibilityIdentifier("inbox-send:\(need.id)")
            }
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("inbox-inline:\(need.id)")
    }

    private func send() {
        guard armed, !working else { return }
        working = true
        problem = ""
        // all of the text is on the row (it is offered only where it fits whole): its whole length (D252)
        let body = JSONValue.object(StartWords.withSeenText(inline.body.objectValue ?? [:], onScreen: inline.text))
        Task {
            let out = await workspace.agent.approve(id: need.id, body: body)
            working = false
            switch out {
            case .done: Haptics.settle()
            case .gone: break // decided elsewhere, withdrawn or expired: the list reads it so
            case .changed: review() // what the row showed changed since: the sheet shows it as it is now
            case .stopped(_, let why), .failed(let why): problem = why
            case .stepUp(let why, _): problem = why
            case .unseen: review()
            }
        }
    }

    private func deny() {
        guard armed, !working else { return }
        working = true
        problem = ""
        Task {
            guard let r = workspace.agent.requests.first(where: { $0.id == need.id }) else { working = false; return }
            let out = await workspace.agent.deny(r)
            working = false
            if case .failed(let why) = out { problem = why }
        }
    }
}

/// Settings: this window's workspace (account, devices and adding one,
/// push, native views, sign out, remove) and the app (lock, native views,
/// haptics, Handoff, Live Activities, predictive echo, the terminal
/// keyboard, the push relay), and About (the version, the open-source
/// licences: Licences.swift).
struct SettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene
    @Environment(\.dismiss) private var dismiss
    @State private var devices: [DeviceInfo] = []
    /// `{count, limit}` of the devices list (D204), when the workspace has one.
    @State private var deviceLimit: (count: Int, limit: Int)?
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
                        // How requests travel: the app tunnel, or the address (D204).
                        NavigationLink {
                            ConnectionView(workspace: w)
                        } label: {
                            LabeledContent("Connection") { Text(verbatim: w.tunnelStatus.summary(server: w.origin)) }
                        }
                        .accessibilityIdentifier("settings-connection")
                        LabeledContent("Sign-in") { Text(w.canResign ? "This device (Face ID)" : "Token (not renewed)") }
                        // xbind's partitions page (I13, D181): only where the
                        // workspace serves it and you see a partitioned tile.
                        if w.showsPartitionsEntry {
                            Button {
                                dismiss()
                                w.openPage(.partitions, in: scene.nav(for: w))
                            } label: {
                                Label {
                                    Text(verbatim: XbindPage.partitions.title)
                                } icon: {
                                    PartitionMark(size: 14)
                                }
                            }
                            .accessibilityHint("Your own data in each partitioned tile, the consents and personal binds you gave, sign-ins waiting for you and the switches you decide")
                        }
                    } header: { Text(verbatim: w.title) }

                    Section {
                        ForEach(devices) { d in
                            HStack {
                                VStack(alignment: .leading) {
                                    Text(verbatim: d.name + (d.current ? " (this device)" : ""))
                                    Text(verbatim: "\(d.platform)\(d.tunnel ? " · tunnel" : "") · last used \(d.lastUsed.map { $0.formatted(.relative(presentation: .named)) } ?? "never")")
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
                        if let l = deviceLimit {
                            Text(verbatim: "\(l.count) of \(l.limit) devices this workspace allows you. "
                                + "Shows a QR code the new device scans; it gets its own key and signs in with Face ID.")
                        } else {
                            Text("Shows a QR code the new device scans; it gets its own key and signs in with Face ID.")
                        }
                    }

                    if let gate = NativeRuntimeGate.workspace(nativeRuntime: w.whoami?.nativeRuntime, loaded: w.whoami != nil) {
                        Section {
                            Label { Text(verbatim: gate.explanation) } icon: { Image(systemName: "globe") }
                                .font(.footnote).foregroundStyle(.secondary)
                        } header: { Text("Native views") }
                    }

                    Section {
                        // the workspace agent on this phone (D241): its notifications, card and lock-screen actions
                        if w.agent.chip.shown {
                            NavigationLink { AgentNotifySettingsView(workspace: w) } label: {
                                Label { Text("Workspace agent") } icon: { AgentGlyph(size: 16).foregroundStyle(XbinColor.partAgent) }
                            }
                            .accessibilityIdentifier("settings-agent-notifications")
                        }
                        Button("Send a test notification") { Task { pushNote = await PushManager.shared.sendTest(w) } }
                            .disabled(PushManager.shared.relay == nil)
                        if let pushNote { Text(verbatim: pushNote).font(.footnote) }
                    } header: { Text("Notifications") } footer: {
                        Text(PushManager.shared.relay == nil
                             ? "No push relay is configured for this build (set one below)."
                             : "Agent permissions, questions and finished turns, tiles that notify you, and the workspace's notices about your partitions and your account.")
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
                }

                Section("About") {
                    LabeledContent("Version") { Text(verbatim: AppInfo.version) }
                    NavigationLink {
                        LicencesView()
                    } label: {
                        Label("Open-source licences", systemImage: "doc.text")
                    }
                }
            }
            .keyboardHideButton()
            .concreteBackground()
            .navigationTitle("Settings")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task {
                if let w = scene.selected {
                    await loadDevices(w)
                    await w.probePartitionsPage()
                }
            }
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
        deviceLimit = DeviceInfo.limit(json: j)
    }
}
