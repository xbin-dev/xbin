import SwiftUI
import XbinCore
import XbinRenderer

// The chrome's own panels for the workspace agent (D233): the standing
// approvals with Take back, "What my agent did" — the person's log, which
// the agent and its view can't read — and Preferences. All three read and
// write with the device's session; none is the agent's to draw.

struct AgentPanelSheet: View {
    let workspace: WorkspaceModel
    let panel: AgentPanel
    @Environment(SceneModel.self) private var scene

    var body: some View {
        NavigationStack {
            Group {
                switch panel {
                case .approvals: AgentApprovalsView(workspace: workspace)
                case .activity: AgentActivityView(workspace: workspace)
                case .prefs: AgentPrefsView(workspace: workspace)
                case .setup: AgentSetupView(workspace: workspace)
                }
            }
            .concreteBackground()
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { scene.agentPanel = nil } } }
        }
    }

    private var title: String {
        switch panel {
        case .approvals: return "Approvals"
        case .activity: return "What my agent did"
        case .prefs: return "Preferences"
        case .setup: return "Workspace agent"
        }
    }
}

/// A panel's closing note, as a row of its own on the canvas (a List draws
/// no footer for a section without rows).
struct AgentPanelNote: View {
    let text: String

    var body: some View {
        Section {
            Text(verbatim: text).font(.footnote).foregroundStyle(XbinColor.muted)
                .fixedSize(horizontal: false, vertical: true)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 4, leading: 4, bottom: 4, trailing: 4))
        }
    }
}

/// The standing approvals: one row each, **Take back** on every one.
struct AgentApprovalsView: View {
    let workspace: WorkspaceModel
    @State private var problem: String?
    @State private var working: String?

    var body: some View {
        let agent = workspace.agent
        List {
            if agent.approvals.isEmpty {
                ContentUnavailableView("No standing approvals", systemImage: "checkmark.shield",
                                       description: Text("Everything your agent changes is asked each time."))
            }
            ForEach(agent.approvals) { a in
                let row = DelegationWords.standingRow(a)
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 6) {
                        Text(verbatim: row.tile).font(.body.weight(.semibold))
                        if row.wider { AgentTag(text: "WIDER", symbol: "exclamationmark.triangle", tone: "warn") }
                    }
                    Text(verbatim: row.text).font(XbinFont.code)
                    Text(verbatim: row.until).font(.caption).foregroundStyle(XbinColor.muted)
                    Button {
                        working = a.id
                        Task {
                            problem = await agent.takeBack(a)
                            working = nil
                        }
                    } label: { Text("Take back").frame(minHeight: 30) }
                    .xbinSecondary()
                    .disabled(working != nil)
                    .accessibilityIdentifier("approval-takeback:\(a.id)")
                }
                .padding(.vertical, 4)
            }
            if let problem { Text(verbatim: problem).foregroundStyle(XbinColor.danger) }
            AgentPanelNote(text: DelegationWords.standingNote)
        }
        .task { await agent.readApprovals() }
        .refreshable { await agent.readApprovals() }
    }
}

/// "What my agent did": the log in sentences, newest first, by day.
struct AgentActivityView: View {
    let workspace: WorkspaceModel
    @State private var rows: [AuditRow]?
    @State private var filter = "change"

    var body: some View {
        List {
            Picker("Show", selection: $filter) {
                ForEach(DelegationWords.didFilters, id: \.id) { f in Text(verbatim: f.label).tag(f.id) }
            }
            .pickerStyle(.segmented)
            .listRowBackground(Color.clear)
            if let rows {
                let days = Self.byDay(rows, filter: filter)
                if days.isEmpty {
                    ContentUnavailableView("Nothing here yet", systemImage: "list.bullet.rectangle")
                }
                ForEach(days, id: \.day) { d in
                    Section {
                        ForEach(Array(d.rows.enumerated()), id: \.offset) { _, r in
                            HStack(alignment: .firstTextBaseline, spacing: 10) {
                                Image(systemName: XbinIcons.symbol(r.icon) ?? "square").foregroundStyle(XbinColor.muted).frame(width: 20)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text("\(Text(verbatim: r.text))\(Text(verbatim: r.strong).bold())")
                                    if !r.detail.isEmpty { Text(verbatim: r.detail).font(XbinFont.code).foregroundStyle(XbinColor.muted) }
                                }
                                Spacer(minLength: 4)
                                if let at = r.at { Text(verbatim: DelegationWords.clock(at)).font(.caption.monospacedDigit()).foregroundStyle(XbinColor.muted) }
                            }
                            .accessibilityElement(children: .combine)
                        }
                    } header: { Text(verbatim: d.day) }
                }
            } else {
                HStack { Spacer(); ProgressView(); Spacer() }
            }
            AgentPanelNote(text: DelegationWords.didNote)
        }
        .task { rows = await workspace.agent.audit() ?? [] }
        .refreshable { rows = await workspace.agent.audit() ?? [] }
    }

    static func byDay(_ rows: [AuditRow], filter: String) -> [(day: String, rows: [DelegationWords.Did])] {
        var out: [(day: String, rows: [DelegationWords.Did])] = []
        let f = DateFormatter()
        f.setLocalizedDateFormatFromTemplate("EEEEMMMMd")
        for row in rows {
            let d = DelegationWords.did(row)
            if filter != "all", d.group != filter { continue }
            let day = d.at.map { f.string(from: $0) } ?? "Earlier"
            if out.last?.day == day { out[out.count - 1].rows.append(d) } else { out.append((day, [d])) }
        }
        return out
    }
}

/// Preferences: whether the agent may start tile agents without asking
/// (xbind lets that through only in a mode it says asks before acting), and
/// — for an admin — whether it may propose admin changes. Opened from the
/// shield's menu alone: the agent's view can't ask for it (AgentBridge).
///
/// Both controls WIDEN what the agent may do, so both are armed like Allow
/// (`widens(armed:)`): "Start without asking" and turning the admin opt-in
/// ON answer only a tap made 600 ms after the panel shows, with the app in
/// front. Narrowing — back to asking, the opt-in off — is never held. The
/// web and MCP tools (D239) are the person's own switches and widen too:
/// the same gate, and one the workspace policy has off can't be turned on
/// and says why. (The
/// web's Preferences also set a started tile agent's network and whether it
/// may call other tiles' APIs; the app has no such control. One added here
/// is a widening control and gets the same gate.)
struct AgentPrefsView: View {
    let workspace: WorkspaceModel
    @Environment(\.scenePhase) private var phase
    @State private var start = "ask"
    @State private var loaded = false
    @State private var problem: String?
    @State private var widen = AgentArming()

    var body: some View {
        let agent = workspace.agent
        Form {
            Section {
                startRow("ask", "Ask me each time")
                startRow("allow", "Start without asking")
            } header: {
                Text("When my agent wants to start a tile agent")
            } footer: {
                Text("Without asking: xbin lets a start through only in a mode it says asks you before acting, with its model as its only network. You are told in the chat. Modes that never ask can't be saved here.")
            }
            Section {
                ForEach(DelegationWords.netPrefs(agent.state)) { p in netRow(p) }
            } header: {
                Text("The web and MCP tools")
            }
            if agent.state?.canAdminChanges == true {
                let on = agent.state?.adminChanges ?? false
                Section {
                    Toggle("Let my agent propose admin changes", isOn: Binding(get: { on }, set: { next in
                        // turning it on widens: only armed; off is never held
                        guard AgentWidening.takes(widening: AgentWidening.widens(from: on, to: next), armed: widen.armed) else { return }
                        Task { problem = await agent.setAdminChanges(next) }
                    }))
                    .widens(armed: on || widen.armed)
                    .accessibilityIdentifier("prefs-admin-changes")
                } footer: {
                    Text("It can read grants, bindings, org shares and limits, and propose changes in a batch. Nothing is applied until you approve the batch here.")
                }
            }
            if let problem { Section { Text(verbatim: problem).foregroundStyle(XbinColor.danger) } }
            // this phone's notifications, card and lock-screen actions (D241)
            Section {
                NavigationLink { AgentNotifySettingsView(workspace: workspace) } label: {
                    Label("Notifications and the Lock Screen", systemImage: "bell.badge")
                }
                .accessibilityIdentifier("prefs-notifications")
            } header: {
                Text("On this \(UIDevice.current.model)")
            }
            AgentPanelNote(text: "Which coding agent a started tile agent uses, its mode, its network and whether it may call other tiles are set on the web: workspace agent → Preferences.")
        }
        .task {
            widen.arm(settle: 0.4) // the sheet's own slide comes first
            if let p = await agent.prefs() {
                start = p["agentSessions"]?["start"]?.stringValue == "allow" ? "allow" : "ask"
                loaded = true
            }
        }
        .onChange(of: phase) { _, p in if p == .active { widen.arm() } else { widen.disarm() } }
    }

    /// One of the web and MCP settings (D239): the person's own, turned on
    /// only armed (it widens); one the workspace policy has off can't be
    /// turned on and says why.
    @ViewBuilder private func netRow(_ p: NetPref) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Toggle(isOn: Binding(get: { p.on }, set: { next in
                guard p.allowed || !next else { return }
                guard AgentWidening.takes(widening: AgentWidening.widens(from: p.on, to: next), armed: widen.armed) else { return }
                Task { problem = await workspace.agent.setNet(p.id, next) }
            })) { Text(verbatim: p.label) }
            .disabled(!p.allowed && !p.on)
            .widens(armed: p.on || widen.armed)
            .accessibilityIdentifier("prefs-net-\(p.id)")
            Text(verbatim: p.why.isEmpty ? p.note : p.why).font(.caption).foregroundStyle(XbinColor.muted)
        }
        .padding(.leading, p.sub ? 16 : 0)
    }

    /// One of the two answers, as a radio row. "Start without asking"
    /// widens "Ask me each time" and is armed like Allow.
    private func startRow(_ id: String, _ label: String) -> some View {
        let on = start == id
        let widening = AgentWidening.widens(fromStart: start, toStart: id)
        return Button {
            if !on, AgentWidening.takes(widening: widening, armed: widen.armed) { save(start: id) }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: on ? "smallcircle.filled.circle" : "circle").foregroundStyle(on ? XbinColor.accent : XbinColor.muted)
                Text(verbatim: label).foregroundStyle(XbinColor.text)
                Spacer(minLength: 0)
            }
            .frame(minHeight: 30)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: label))
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
        .accessibilityIdentifier("prefs-start-\(id)")
        // (outermost: the element made above is the one that is dimmed)
        .widens(armed: loaded && (!widening || widen.armed))
    }

    private func save(start next: String) {
        let before = start
        start = next
        Task {
            if await workspace.agent.setPrefs(["agentSessions": ["start": .string(next)]]) == nil {
                start = before
                problem = "That couldn't be saved."
            } else {
                problem = nil
            }
        }
    }
}
