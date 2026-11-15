import SwiftUI
import XbinCore
import XbinRenderer

// An admin batch (D233; docs/protocol/delegation.md §Change sets): what an
// admin's agent proposed, reviewed in the app's own full-height sheet. An
// agent never calls the admin API — it proposes, the person ticks, xbind
// applies what they ticked, as them.
//
// Every line, its before and after, its effects and its warnings are
// xbind's, written from the change itself; the agent's title and reasons
// are quoted as the agent's. Before sits above after (a phone is narrow),
// in fixed places, never told apart by colour alone. An item with a warning
// starts unticked; one xbind would refuse is shown with the reason and
// can't be ticked. Applying asks Face ID here, and still shows xbind's own
// recent-sign-in step when the server asks for it.

struct AgentChangeSetSheet: View {
    let workspace: WorkspaceModel
    let id: String

    @Environment(SceneModel.self) private var scene
    @Environment(\.scenePhase) private var phase
    @State private var set: AdminChangeSet?
    @State private var loaded = false
    @State private var ticks: Set<Int> = []
    @State private var arming = AgentArming()
    @State private var working = false
    @State private var problem: String?
    /// xbind asked for a recent sign-in: "password" | "signin".
    @State private var stepUp: String?
    @State private var password = ""
    @State private var wrongPassword = false
    /// A 409's refusal: the agent stopped while the review was open.
    @State private var refusal = ""

    /// Why nothing can be applied now ("" when it can): xbind's 409, else the state.
    private var stop: String { refusal.isEmpty ? DelegationWords.stopRefusal(workspace.agent.state) : refusal }

    var body: some View {
        VStack(spacing: 0) {
            AgentTrustedBar(title: "XBIN · ADMIN CHANGES", asking: workspace.userLabel) { scene.agentReview = nil }
            if let s = set {
                if let kind = stepUp {
                    stepUpView(s, kind: kind)
                } else if s.isProposed {
                    review(s)
                } else {
                    results(s)
                }
            } else if loaded {
                AgentStateView(title: "Nothing to review", text: "These changes aren't waiting any more, or aren't yours.", more: "", retry: nil)
                Button { scene.agentReview = nil } label: { Text("Done").frame(maxWidth: .infinity, minHeight: 44) }
                    .xbinSecondary().padding(14)
            } else {
                ProgressView().controlSize(.large).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(XbinColor.background)
        .presentationDetents([.large])
        .presentationDragIndicator(.hidden)
        .interactiveDismissDisabled(working)
        .task(id: id) {
            let s = await workspace.agent.changeset(id)
            set = s
            loaded = true
            if let s { ticks = Set(DelegationWords.initialTicks(s)) }
            arming.arm(settle: 0.4)
        }
        .onChange(of: phase) { _, p in if p == .active { arming.arm() } else { arming.disarm() } }
    }

    // MARK: The review

    @ViewBuilder private func review(_ s: AdminChangeSet) -> some View {
        let w = DelegationWords.reviewWords(s, ticked: ticks.count)
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text(verbatim: w.heading).font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("changeset-heading")
                if !s.title.isEmpty { AgentQuote(label: "The agent's title — not checked by xbin", text: s.title) }
                Text(verbatim: w.intro).font(.callout).foregroundStyle(XbinColor.muted)
                ForEach(s.items) { item in itemCard(item, of: s.items.count) }
                if let problem {
                    Label { Text(verbatim: problem) } icon: { Image(systemName: "exclamationmark.octagon") }
                        .font(.callout).foregroundStyle(XbinColor.danger)
                }
                Text(verbatim: w.footer).font(.caption).foregroundStyle(XbinColor.muted)
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        // ("Apply none" rejects: nothing is allowed by it, so it stays while stopped)
        AgentDecisionBar(deny: "Reject all", allow: w.apply, id: "changeset", armed: arming.armed, working: working,
                         stopped: ticks.isEmpty ? "" : DelegationWords.stopLine(stop, changeset: true),
                         onDeny: { decide(s, apply: false) }, onAllow: { decide(s, apply: !ticks.isEmpty) })
    }

    /// One change: a tick box, xbind's line and kind, before above after,
    /// effects, warnings as prominent as the line, the agent's reason last.
    @ViewBuilder private func itemCard(_ item: AdminChangeSet.Item, of total: Int) -> some View {
        let canTick = DelegationWords.tickable(item)
        let on = ticks.contains(item.n)
        VStack(alignment: .leading, spacing: 8) {
            Button {
                guard canTick else { return }
                if on { ticks.remove(item.n) } else { ticks.insert(item.n) }
                arming.arm() // the Apply button's meaning just changed
            } label: {
                HStack(alignment: .top, spacing: 10) {
                    Image(systemName: !canTick ? "square.slash" : on ? "checkmark.square.fill" : "square")
                        .font(.title3)
                        .foregroundStyle(!canTick ? XbinColor.muted : on ? XbinColor.accent : XbinColor.text)
                    VStack(alignment: .leading, spacing: 4) {
                        Text(verbatim: item.line).font(.body.weight(.semibold)).multilineTextAlignment(.leading)
                        AgentTag(text: DelegationWords.kindTag(item.kind))
                    }
                    Spacer(minLength: 0)
                }
                .frame(minHeight: 44, alignment: .top)
            }
            .buttonStyle(.plain)
            // one element a change: its number, whether it is ticked, xbind's line, its warnings, before and after
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text(verbatim: spoken(item, of: total, on: on, canTick: canTick)))
            .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
            .accessibilityHint(canTick ? Text(on ? "Leaves it out" : "Includes it") : Text(verbatim: ""))
            .accessibilityIdentifier("changeset-tick-\(item.n)")
            // (outermost: the element made above is the one that is dimmed)
            .disabled(!canTick)
            if item.before != nil || item.after != nil {
                VStack(alignment: .leading, spacing: 0) {
                    beforeAfter("Before", item.before, fill: XbinColor.dangerBackground)
                    Image(systemName: "arrow.down").font(.caption2).foregroundStyle(XbinColor.muted).padding(.leading, 10).padding(.vertical, 2)
                        .accessibilityHidden(true)
                    beforeAfter("After", item.after, fill: XbinColor.okBackground)
                }
            }
            ForEach(item.effects, id: \.self) { e in Text(verbatim: e).font(.callout) }
            ForEach(item.warnings, id: \.self) { raw in
                let w = DelegationWords.warning(raw)
                VStack(alignment: .leading, spacing: 3) {
                    AgentTag(text: w.tag, symbol: XbinIcons.symbol(w.icon) ?? "exclamationmark.triangle", tone: "danger")
                    Text(verbatim: w.text).font(.callout.weight(.semibold))
                }
                .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                .background(XbinColor.dangerBackground, in: .xbinPlate)
            }
            if !canTick {
                Label { Text(verbatim: "Can't be applied. \(item.blocked)") } icon: { Image(systemName: "lock") }
                    .font(.callout).foregroundStyle(XbinColor.warn)
            }
            if !item.why.isEmpty {
                AgentHiddenNote(text: item.why)
                Text(AgentReveal.attributed("Agent's reason: “\(item.why)”", font: .caption.italic())).foregroundStyle(XbinColor.muted)
                    .accessibilityLabel(Text(verbatim: "Agent's reason: \(DelegationWords.revealed(item.why))"))
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .xbinCard()
        .accessibilityElement(children: .contain)
    }

    private func beforeAfter(_ label: String, _ value: String?, fill: Color) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(verbatim: label.uppercased()).font(.caption2.weight(.bold)).foregroundStyle(XbinColor.muted).frame(width: 52, alignment: .leading)
            Text(verbatim: value ?? "—").font(XbinFont.code).frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(8)
        .background(fill, in: .xbinPlate)
    }

    private func spoken(_ item: AdminChangeSet.Item, of total: Int, on: Bool, canTick: Bool) -> String {
        var parts = ["Change \(item.n) of \(total), \(!canTick ? "can't be applied" : on ? "selected" : "not selected")", item.line]
        for raw in item.warnings {
            let w = DelegationWords.warning(raw)
            parts.append("Warning, \(w.tag.lowercased()): \(w.text)")
        }
        if !canTick, !item.blocked.isEmpty { parts.append(item.blocked) }
        if let b = item.before { parts.append("Before: \(b)") }
        if let a = item.after { parts.append("After: \(a)") }
        return parts.joined(separator: ". ")
    }

    // MARK: Deciding

    private func decide(_ s: AdminChangeSet, apply: Bool, password: String = "") {
        working = true
        problem = nil
        Task {
            defer { working = false }
            guard apply else {
                // "Apply none" and Reject all end the set rejected.
                settle(await workspace.agent.reject(s))
                return
            }
            // The app's own gate first; the server's rule still stands after it.
            if password.isEmpty, AgentChoices.biometricsForAdminChanges {
                guard await AgentGate.confirm("Apply \(ticks.count) admin change\(ticks.count == 1 ? "" : "s") as you") else {
                    problem = "It wasn't you, so nothing was applied."
                    arming.arm()
                    return
                }
            }
            settle(await workspace.agent.apply(s, items: s.items.map(\.n).filter { ticks.contains($0) }, password: password))
        }
    }

    private func settle(_ out: DelegateOutcome) {
        switch out {
        case .done(let j):
            stepUp = nil
            password = ""
            if let s = AdminChangeSet(json: j) { set = s } else { Task { set = await workspace.agent.changeset(id) } }
            Haptics.settle()
        case .stepUp(let kind, let wrong):
            stepUp = kind
            wrongPassword = wrong
            password = ""
        case .gone(let why):
            problem = "It no longer waits: \(why)"
            Task { set = await workspace.agent.changeset(id) }
        case .failed(let why):
            problem = why
        case .unseen:
            problem = "xbin refused it."
        case .stopped(let why, _):
            // stopped meanwhile: the reason shows where Apply was; nothing is sent again
            refusal = why
            stepUp = nil
            password = ""
        }
    }

    // MARK: The recent-sign-in step

    @ViewBuilder private func stepUpView(_ s: AdminChangeSet, kind: String) -> some View {
        let w = DelegationWords.stepUp(kind)
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text(verbatim: w.heading).font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("changeset-stepup")
                Text(verbatim: w.text)
                if w.password {
                    SecureField("Password", text: $password)
                        .textContentType(.password)
                        .accessibilityLabel("Your password")
                        .padding(10)
                        .overlay { RoundedRectangle.xbinPlate.strokeBorder(XbinColor.borderStrong, lineWidth: 1) }
                        .accessibilityIdentifier("changeset-password")
                    if wrongPassword {
                        Label("That password was not accepted.", systemImage: "exclamationmark.octagon").font(.callout).foregroundStyle(XbinColor.danger)
                    }
                    Text("Signed in with single sign-on? Sign in again from Settings, then apply; this review keeps waiting.")
                        .font(.caption).foregroundStyle(XbinColor.muted)
                }
                Text("Your ticks are kept.").font(.caption).foregroundStyle(XbinColor.muted)
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        AgentDecisionBar(deny: "Not now", allow: w.action, id: "stepup", armed: w.password ? !password.isEmpty : true, working: working,
                         stopped: DelegationWords.stopLine(stop, changeset: true),
                         onDeny: { stepUp = nil; password = "" },
                         onAllow: {
                             if w.password {
                                 decide(s, apply: true, password: password)
                             } else {
                                 // Sign in again, in the app's own flow; the proposal waits.
                                 scene.agentReview = nil
                                 scene.addRequest = .signInAgain(workspace: workspace.id, server: workspace.origin)
                             }
                         })
    }

    // MARK: Results

    @ViewBuilder private func results(_ s: AdminChangeSet) -> some View {
        let w = DelegationWords.resultsWords(s)
        let decided = s.state == .applied || s.state == .partial
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text(verbatim: decided ? w.title : stateTitle(s)).font(.title3.weight(.semibold)).accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("changeset-results")
                if !w.banner.isEmpty, decided {
                    Label { Text(verbatim: w.banner) } icon: { Image(systemName: "exclamationmark.triangle") }
                        .font(.callout.weight(.semibold))
                        .padding(10).frame(maxWidth: .infinity, alignment: .leading)
                        .background(XbinColor.warnBackground, in: .xbinPlate)
                }
                ForEach(s.items) { item in
                    let r = DelegationWords.resultOf(item)
                    VStack(alignment: .leading, spacing: 4) {
                        Text(verbatim: item.line).font(.body.weight(.semibold))
                        if !r.word.isEmpty {
                            AgentTag(text: r.word, symbol: XbinIcons.symbol(r.icon) ?? "", tone: r.tone)
                        }
                        if !r.text.isEmpty { Text(verbatim: r.text).font(.callout).foregroundStyle(XbinColor.muted) }
                    }
                    .padding(12).frame(maxWidth: .infinity, alignment: .leading)
                    .xbinCard()
                    .accessibilityElement(children: .combine)
                }
                if let problem {
                    Label { Text(verbatim: problem) } icon: { Image(systemName: "exclamationmark.octagon") }
                        .font(.callout).foregroundStyle(XbinColor.danger)
                }
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        VStack(spacing: 8) {
            if decided, !w.undo.isEmpty {
                // Undo proposes a new set to review; it undoes nothing by itself.
                Button { undo(s) } label: { Text(verbatim: w.undo).frame(maxWidth: .infinity, minHeight: 44) }
                    .xbinSecondary().disabled(working)
                    .accessibilityIdentifier("changeset-undo")
            }
            Button { scene.agentReview = nil } label: { Text("Done").frame(maxWidth: .infinity, minHeight: 44) }
                .xbinPrimary()
                .accessibilityIdentifier("changeset-done")
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .background(XbinColor.surface)
    }

    private func stateTitle(_ s: AdminChangeSet) -> String {
        switch s.state {
        case .rejected, .expired, .withdrawn: return DelegationWords.endedWords(s)
        case .applying: return "Applying now…"
        default: return DelegationWords.resultsWords(s).title
        }
    }

    private func undo(_ s: AdminChangeSet) {
        working = true
        Task {
            let out = await workspace.agent.undo(s)
            working = false
            if case .done(let j) = out, let next = AdminChangeSet(json: j) {
                scene.agentReview = .changeset(next.id)
            } else if case .gone(let why) = out {
                problem = why
            } else if case .failed(let why) = out {
                problem = why
            }
        }
    }
}
