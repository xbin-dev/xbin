import SwiftUI
import XbinCore
import XbinRenderer

/// The workspace agent on this iPhone, per workspace (D241; plans/wsagent/
/// ios-stage2.md §11): which notifications come, the Live Activity, and the
/// lock-screen actions. From the agent's Preferences and Settings → the
/// workspace. What xbind needs to know — the notifications turned off — it
/// is told through the push registration's kinds; the actions are the
/// app's own (the extension reads them), and an action re-checks them when
/// it runs.
///
/// Allow once from the lock screen is off until the person turns it on,
/// after the explanation of what it covers and what it never does; turning
/// it on widens what a notification can do, so it is armed like Allow.
/// Turning anything off is never held.
struct AgentNotifySettingsView: View {
    let workspace: WorkspaceModel
    @Environment(\.scenePhase) private var phase
    @State private var prefs = AgentNotifyPrefs()
    @State private var explaining = false
    @State private var widen = AgentArming()

    var body: some View {
        Form {
            Section {
                Toggle("Needs you", isOn: bind(\.needsYou))
                    .accessibilityIdentifier("agent-notify-needs")
                Toggle("Finished", isOn: bind(\.finished))
                    .accessibilityIdentifier("agent-notify-finished")
            } header: {
                Text("Notifications")
            } footer: {
                Text("Needs you: a request, a plan or admin changes waiting for your approval, and your agent's questions. Finished: when what you asked for is done. One notification each, never one a turn. Off: \(workspace.title) sends none of them to this \(UIDevice.current.model).")
            }
            Section {
                Toggle("Live Activity", isOn: bind(\.liveActivity))
                    .accessibilityIdentifier("agent-notify-live")
            } footer: {
                Text("One card on the Lock Screen and in the Dynamic Island while your agent works or something waits for you: what it is doing in counts, never a tile's name or what a request says.")
            }
            Section {
                Toggle("Deny from a notification", isOn: bind(\.lockDeny))
                    .accessibilityIdentifier("agent-notify-deny")
                Toggle("Allow once from a notification", isOn: Binding(get: { prefs.lockAllowOnce }, set: { on in
                    if !on { save { $0.lockAllowOnce = false }; return }
                    // turning it on widens: armed, and explained the first time
                    guard widen.armed else { return }
                    if prefs.allowOnceExplained { save { $0.lockAllowOnce = true } } else { explaining = true }
                }))
                .widens(armed: prefs.lockAllowOnce || widen.armed)
                .accessibilityIdentifier("agent-notify-allow-once")
            } header: {
                Text("On the Lock Screen")
            } footer: {
                Text(AgentNotifyWords.lockFooter)
            }
        }
        .navigationTitle("Workspace agent")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            prefs = AgentNotifications.shared.prefs(workspace)
            widen.arm(settle: 0.4)
        }
        .onChange(of: phase) { _, p in if p == .active { widen.arm() } else { widen.disarm() } }
        .onChange(of: explaining) { _, up in if up { widen.disarm() } else { widen.arm() } }
        .sheet(isPresented: $explaining) {
            AllowOnceExplainSheet { on in
                explaining = false
                if on {
                    save {
                        $0.lockAllowOnce = true
                        $0.allowOnceExplained = true
                    }
                }
            }
        }
    }

    private func bind(_ key: WritableKeyPath<AgentNotifyPrefs, Bool>) -> Binding<Bool> {
        Binding(get: { prefs[keyPath: key] }, set: { v in save { $0[keyPath: key] = v } })
    }

    private func save(_ change: (inout AgentNotifyPrefs) -> Void) {
        var p = AgentNotifications.shared.prefs(workspace)
        change(&p)
        prefs = p
        AgentNotifications.shared.setPrefs(p, for: workspace)
    }
}

/// What Allow once from the lock screen covers, and what it never does —
/// before it is turned on. Turn on is armed like Allow; Not now is not.
struct AllowOnceExplainSheet: View {
    let done: (Bool) -> Void
    @Environment(\.scenePhase) private var phase
    @State private var arming = AgentArming()

    var body: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    Text("Allow once from a notification").font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
                        .accessibilityIdentifier("allow-once-explain")
                    Text(AgentNotifyWords.explainWhat)
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(AgentNotifyWords.explainNever, id: \.self) { line in
                            Label { Text(verbatim: line) } icon: { Image(systemName: "xmark.square").foregroundStyle(XbinColor.muted) }
                                .font(.callout)
                        }
                    }
                    Text(AgentNotifyWords.explainHow).font(.callout).foregroundStyle(XbinColor.muted)
                }
                .padding(20)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack(spacing: 10) {
                Button { done(false) } label: { Text("Not now").frame(maxWidth: .infinity, minHeight: 44) }
                    .xbinSecondary()
                    .accessibilityIdentifier("allow-once-not-now")
                Button { if arming.armed { done(true) } } label: { Text("Turn on").frame(maxWidth: .infinity, minHeight: 44) }
                    .xbinPrimary()
                    .widens(armed: arming.armed)
                    .accessibilityIdentifier("allow-once-turn-on")
            }
            .padding(.horizontal, 16).padding(.vertical, 10)
        }
        .background(XbinColor.background)
        .presentationDetents([.large])
        .onAppear { arming.arm(settle: 0.4) }
        .onChange(of: phase) { _, p in if p == .active { arming.arm() } else { arming.disarm() } }
    }
}

/// The settings' words (the app's own: there is no lock screen on the web).
enum AgentNotifyWords {
    static let lockFooter = "Every action asks for Face ID or your passcode first, and the app reads the request again before it sends anything: one that changed, was decided elsewhere or can't be reached decides nothing, and a notification says why. Allow once is offered only for a single change to a tile whose whole request fits the notification — anything wider opens the app."

    static let explainWhat = "With this on, a notification for a single change your agent wants to make in a tile can offer Allow once — when the whole request, its call and everything it sends, fits on the notification, so you see all of it before you allow it."

    static let explainNever = [
        "Never for “always”, or every change to a tile",
        "Never for starting a tile agent, creating a tile, a web page or an MCP tool",
        "Never for a plan or admin changes",
        "Never for a request too long to show, or with hidden characters",
    ]

    static let explainHow = "Allow once asks for Face ID or your passcode, and the app checks the request with your workspace before it allows it. You can turn this off at any time; Deny stays."
}
