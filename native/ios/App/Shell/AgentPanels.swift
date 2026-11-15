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
            Section {} footer: { Text(verbatim: DelegationWords.standingNote) }
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
            Section {} footer: { Text(verbatim: DelegationWords.didNote) }
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
/// (only ever in a mode that asks before acting), and — for an admin —
/// whether it may propose admin changes.
struct AgentPrefsView: View {
    let workspace: WorkspaceModel
    @State private var start = "ask"
    @State private var loaded = false
    @State private var problem: String?

    var body: some View {
        let agent = workspace.agent
        Form {
            Section {
                Picker("When my agent wants to start a tile agent", selection: Binding(get: { start }, set: { save(start: $0) })) {
                    Text("Ask me each time").tag("ask")
                    Text("Start without asking").tag("allow")
                }
                .pickerStyle(.inline)
                .disabled(!loaded)
                .accessibilityIdentifier("prefs-start")
            } footer: {
                Text("Without asking: only in a mode that asks before acting, with its model as its only network. You are told in the chat. Modes that never ask can't be saved here.")
            }
            if agent.state?.canAdminChanges == true {
                Section {
                    Toggle("Let my agent propose admin changes", isOn: Binding(get: { agent.state?.adminChanges ?? false }, set: { on in
                        Task { problem = await agent.setAdminChanges(on) }
                    }))
                    .accessibilityIdentifier("prefs-admin-changes")
                } footer: {
                    Text("It can read grants, bindings, org shares and limits, and propose changes in a batch. Nothing is applied until you approve the batch here.")
                }
            }
            if let problem { Section { Text(verbatim: problem).foregroundStyle(XbinColor.danger) } }
            Section {} footer: {
                Text("Which coding agent a started tile agent uses, and its mode, are set on the web: workspace agent → Preferences.")
            }
        }
        .task {
            if let p = await agent.prefs() {
                start = p["agentSessions"]?["start"]?.stringValue == "allow" ? "allow" : "ask"
                loaded = true
            }
        }
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
