import SwiftUI
import XbinCore
import XbinRenderer

// The grant (D233; docs/workspace-agent.md §How approvals work): the app's
// own sheet for one approval request, built from what xbind answers for the
// request's id (`GET /delegate/requests/<id>`, the device's session) and
// from nothing else — the agent's view supplied the id, and that is all.
//
// The rules it keeps:
//   - Allow is not armed until 600 ms after the sheet is fully shown, and
//     only while the app is in front; a change of the choice arms it again.
//     No default action allows.
//   - The agent's own words are quoted, labelled unchecked; nothing of the
//     request is rendered as markdown, a link or an image — verbatim text.
//   - Deny left, Allow right, equal width, at the bottom; stacked (Allow
//     below) at accessibility sizes.
//   - "Always, every change to <tile>" is a second sheet whose primary
//     button is Go back, and which asks Face ID.
//   - A tile agent's start leads with its mode; one that never asks — any
//     mode xbind doesn't say asks: only a harness's ask or plan mode does —
//     is said in the danger wording, and Deny becomes the primary button.
//   - What WIDENS the grant is armed like Allow (the web chrome's
//     WidenGuard): a standing choice, "until I take it back" — dimmed and
//     inert for 600 ms after the sheet shows, while another sheet covers it
//     and while the app is not in front. Narrowing (back to Once, to one
//     hour) is never held up.

/// The bar that says whose sheet this is: xbin's, asking this person.
struct AgentTrustedBar: View {
    let title: String
    let asking: String
    var position: String = ""
    var close: (() -> Void)?
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        // At accessibility sizes the words take the bar's width, and the
        // place and Not now a line of their own under them: nothing is cut.
        let big = typeSize.isAccessibilitySize
        let layout = big ? AnyLayout(VStackLayout(alignment: .leading, spacing: 6)) : AnyLayout(HStackLayout(spacing: 8))
        layout {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "shield.lefthalf.filled").accessibilityHidden(true)
                Text(verbatim: "\(title) — asking \(asking)").font(.caption.weight(.bold)).lineLimit(big ? nil : 2)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityLabel(Text(verbatim: "xbin \(title.split(separator: "·").last.map { $0.trimmingCharacters(in: .whitespaces).lowercased() } ?? "approval"), asking \(asking)"))
                    .accessibilityIdentifier("approval-bar")
            }
            if !big { Spacer(minLength: 4) }
            HStack(spacing: 12) {
                if !position.isEmpty {
                    Text(verbatim: position).font(.caption.monospacedDigit())
                        .accessibilityLabel(Text(verbatim: "Request \(position)"))
                }
                if let close {
                    // a whole 44 pt target at every size (it was 28 below
                    // the accessibility sizes); the bar's padding gives way
                    Button(action: close) {
                        Text("Not now").font(.caption.weight(.semibold))
                            .frame(minWidth: 44, minHeight: 44).contentShape(Rectangle())
                    }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Not now")
                        .accessibilityHint("Leaves it waiting. Nothing is allowed or denied.")
                        .accessibilityIdentifier("approval-later")
                }
            }
        }
        .padding(.horizontal, 14).padding(.vertical, close == nil || big ? 10 : 2)
        .frame(maxWidth: .infinity, alignment: .leading)
        .foregroundStyle(XbinColor.background)
        // Up under the status bar, never sideways: on an iPhone Duo the
        // system's vertical bar (its time, its signal) sits at the sheet's
        // side, and this band drawn under it hid them (dark at the largest
        // text, 2026-11-16). The decision bar keeps the same width.
        .background(XbinColor.text, ignoresSafeAreaEdges: .top)
        // The bar is chrome, like a navigation bar: it grows with the text
        // up to a point, so at the largest sizes the request under it keeps
        // most of the sheet.
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .accessibilityElement(children: .contain)
    }
}

/// A labelled row of an approval: the label beside the value, or above it
/// at accessibility sizes.
struct ApprovalRow<Content: View>: View {
    let label: String
    @ViewBuilder var content: Content
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2))
                                                  : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 10))
        layout {
            Text(verbatim: label).font(.caption.weight(.semibold)).foregroundStyle(XbinColor.muted)
                .frame(width: typeSize.isAccessibilitySize ? nil : 64, alignment: .leading)
            VStack(alignment: .leading, spacing: 3) { content }.frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 8)
        .overlay(alignment: .bottom) { Rectangle().fill(XbinColor.border).frame(height: 1) }
        .accessibilityElement(children: .combine)
    }
}

/// The agent's own text, quoted and labelled as unchecked.
struct AgentQuote: View {
    let label: String
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(verbatim: label).font(.caption2.weight(.bold)).foregroundStyle(XbinColor.muted)
            AgentHiddenNote(text: text)
            Text(AgentReveal.attributed(text, font: .callout.italic()))
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(XbinColor.surface2)
        .overlay(alignment: .leading) { Rectangle().fill(XbinColor.borderStrong).frame(width: 3) }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: "\(label.replacingOccurrences(of: " — ", with: ", ")): "
            + (DelegationWords.reveal(text).hidden > 0 ? "\(DelegationWords.hiddenNote). " : "") + DelegationWords.revealed(text)))
    }
}

/// What the agent wrote is never drawn as it came: each character that
/// reorders or hides text (bidi controls, zero-width ones, other controls
/// and format characters) is its escape, ⟨U+202E⟩, a chip of its own —
/// never interpreted (DelegationWords.reveal, the web chrome's rule).
enum AgentReveal {
    static func attributed(_ text: String, font: Font) -> AttributedString {
        var out = AttributedString()
        for p in DelegationWords.reveal(text).parts {
            switch p {
            case .text(let t):
                var a = AttributedString(t)
                a.font = font
                out += a
            case .hidden(let code):
                var a = AttributedString("⟨\(code)⟩")
                a.font = .caption2.monospaced().weight(.bold)
                a.foregroundColor = XbinColor.warn
                a.backgroundColor = XbinColor.warnBackground
                out += a
            }
        }
        return out
    }
}

/// Above text of the agent's that holds a hidden character: the line that
/// says so (nothing when it holds none).
struct AgentHiddenNote: View {
    let text: String

    var body: some View {
        if DelegationWords.reveal(text).hidden > 0 {
            Label { Text(verbatim: DelegationWords.hiddenNote).font(.caption.weight(.semibold)) } icon: { Image(systemName: "exclamationmark.triangle") }
                .foregroundStyle(XbinColor.warn)
                .accessibilityIdentifier("agent-hidden-note")
        }
    }
}

/// A square tag: a glyph and a word in a tone.
struct AgentTag: View {
    let text: String
    var symbol: String = ""
    var tone: String = ""

    var body: some View {
        HStack(spacing: 4) {
            if !symbol.isEmpty { Image(systemName: symbol).font(.caption2.weight(.bold)) }
            Text(verbatim: text).font(.caption2.weight(.bold))
        }
        .padding(.horizontal, 6).padding(.vertical, 2)
        .foregroundStyle(tone.isEmpty ? XbinColor.muted : AgentTone.color(tone))
        .overlay { RoundedRectangle.xbinPlate.strokeBorder(tone.isEmpty ? XbinColor.borderStrong : AgentTone.color(tone), lineWidth: 1) }
    }
}

/// When Allow may be pressed: armed a moment after the sheet is fully
/// shown, disarmed whenever the app leaves the front or the choice changes.
@MainActor
@Observable
final class AgentArming {
    private(set) var armed = false
    /// The rule (XbinCore's ArmingClock, tested there): shown since when.
    @ObservationIgnored private var clock = ArmingClock()
    @ObservationIgnored private var token = 0

    /// Arms after `AgentChoices.armSeconds` (plus `settle`, the sheet's own
    /// slide), unless disarmed or re-armed meanwhile.
    func arm(settle: Double = 0) {
        armed = false
        token += 1
        let t = token
        let delay = Self.seconds
        clock.shown(at: Date().addingTimeInterval(settle))
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64((delay + settle) * 1e9))
            guard let self, self.token == t else { return }
            // (a timer that fired early arms nothing: the clock decides)
            if let left = self.clock.remaining(at: Date(), delay: delay), left > 0 {
                try? await Task.sleep(nanoseconds: UInt64(left * 1e9))
                guard self.token == t else { return }
            }
            self.armed = self.clock.isArmed(at: Date(), delay: delay)
        }
    }

    func disarm() {
        token += 1
        clock.covered()
        armed = false
    }

    /// The delay — `AgentChoices.armSeconds`. A Debug build's UI tests
    /// stretch it (`-XbinAgentArmSeconds 6`) to see Allow unarmed for
    /// certain, however long a test's own query takes; never shorter.
    static var seconds: Double {
        #if DEBUG
        let s = UserDefaults.standard.double(forKey: "XbinAgentArmSeconds")
        if s > AgentChoices.armSeconds { return s }
        #endif
        return AgentChoices.armSeconds
    }
}

extension View {
    /// A control that WIDENS what the agent may do — a standing choice, a
    /// longer duration, "start without asking", the admin opt-in — answers
    /// like Allow: dimmed and inert until `armed` (600 ms after it shows,
    /// not under another sheet, the app in front), and VoiceOver says why.
    /// A control that narrows is never passed through this.
    func widens(armed: Bool) -> some View {
        disabled(!armed)
            .accessibilityHint(armed ? Text(verbatim: "") : Text("Available in a moment"))
    }
}

/// How long a standing approval lasts: two options side by side. "Until I
/// take it back" widens "for one hour", so it is armed like Allow; the
/// other way is never held.
struct AgentDurationPicker: View {
    @Binding var duration: String
    /// Whether what widens may be chosen now.
    var armed: Bool
    /// The options' identifiers' prefix.
    var id: String
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(spacing: 6)) : AnyLayout(HStackLayout(spacing: 6))
        layout {
            ForEach(DelegationWords.durations, id: \.id) { d in
                let on = duration == d.id
                let widening = AgentWidening.widens(fromDuration: duration, toDuration: d.id)
                Button {
                    if AgentWidening.takes(widening: widening, armed: armed) { duration = d.id }
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: on ? "smallcircle.filled.circle" : "circle").foregroundStyle(on ? XbinColor.accent : XbinColor.muted)
                        Text(verbatim: d.label).font(.callout.weight(on ? .semibold : .regular)).multilineTextAlignment(.leading)
                        Spacer(minLength: 0)
                    }
                    .padding(.horizontal, 10)
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .background(on ? XbinColor.selection : XbinColor.surface, in: .xbinPlate)
                    .overlay { RoundedRectangle.xbinPlate.strokeBorder(on ? XbinColor.accent : XbinColor.border, lineWidth: 1) }
                }
                .buttonStyle(.plain)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(Text(verbatim: d.label))
                .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
                .accessibilityIdentifier("\(id)-\(d.id)")
                // (outermost: the element made above is the one that is dimmed)
                .widens(armed: !widening || armed)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("How long")
    }
}

/// Deny and Allow at the bottom of a sheet: Deny left, Allow right, equal
/// width; stacked with Allow below at accessibility sizes. `primaryDeny`
/// (a mode that never asks; the wider grant's Go back) makes the safe
/// button the filled one and the other an outlined danger.
struct AgentDecisionBar: View {
    let deny: String
    let allow: String
    var allowSpoken: String = ""
    var primaryDeny = false
    /// The buttons' identifiers' prefix (a sheet over another names its own).
    var id = "approval"
    var armed: Bool
    var working: Bool
    /// Why nothing can be allowed now (the agent is stopped: off, paused,
    /// held, no agent): said in the place of Allow, and Deny is the filled
    /// button. "": Allow is there.
    var stopped: String = ""
    let onDeny: () -> Void
    let onAllow: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize
    /// A full touch target that grows with the text — to a point: at the
    /// largest sizes two stacked buttons must still leave the sheet its words.
    @ScaledMetric private var scaled: CGFloat = 44
    private var height: CGFloat { min(scaled, 76) }

    var body: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(spacing: 8)) : AnyLayout(HStackLayout(spacing: 10))
        Group {
            if stopped.isEmpty {
                layout {
                    denyButton
                    allowButton
                }
            } else {
                // why nothing can be allowed, the whole width, and Deny under it
                VStack(spacing: 8) {
                    stoppedLine
                    denyButton
                }
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .frame(maxWidth: .infinity)
        .background(XbinColor.surface, ignoresSafeAreaEdges: .bottom)
        .overlay(alignment: .top) { Rectangle().fill(XbinColor.border).frame(height: 1) }
    }

    @ViewBuilder private var denyButton: some View {
        let b = Button(action: onDeny) { Text(verbatim: deny).frame(maxWidth: .infinity, minHeight: height) }
            .disabled(working)
        Group { if primaryDeny || !stopped.isEmpty { b.xbinPrimary() } else { b.xbinSecondary() } }
            .accessibilityIdentifier("\(id)-deny")
    }

    /// Where Allow would be: why it isn't, in xbin's words — never a button.
    private var stoppedLine: some View {
        Label { Text(verbatim: stopped).font(.callout).multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true) }
            icon: { Image(systemName: "pause.circle").foregroundStyle(XbinColor.warn) }
            .padding(8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(XbinColor.warnBackground, in: .xbinPlate)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("\(id)-stopped")
    }

    @ViewBuilder private var allowButton: some View {
        let b = Button {
            // Not armed: the tap does nothing (it may have been meant for
            // what the sheet covered a moment ago).
            if armed, !working { onAllow() }
        } label: {
            Text(verbatim: allow).multilineTextAlignment(.center).frame(maxWidth: .infinity, minHeight: height)
        }
        .disabled(!armed || working)
        .accessibilityLabel(Text(verbatim: allowSpoken.isEmpty ? allow : allowSpoken))
        Group { if primaryDeny { b.xbinSecondary().tint(XbinColor.danger) } else { b.xbinPrimary() } }
            .accessibilityIdentifier("\(id)-allow")
    }
}

/// The approval of one request.
struct AgentApprovalSheet: View {
    let workspace: WorkspaceModel
    let id: String

    @Environment(SceneModel.self) private var scene
    @Environment(\.scenePhase) private var phase
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var request: DelegateRequest?
    @State private var loaded = false
    @State private var choice: GrantChoice.ID = .once
    @State private var duration = ""
    @State private var arming = AgentArming()
    /// What widens the grant (a standing choice, a longer duration) is armed
    /// like Allow — but not re-armed by a change of the choice itself.
    @State private var widen = AgentArming()
    @State private var working = false
    @State private var problem: String?
    @State private var wider = false
    @State private var showAll = false
    @State private var showAllBrief = false
    /// A 409's refusal: the agent stopped while the sheet was open.
    @State private var refusal = ""
    /// Where a request that no longer waits was decided ("on the web",
    /// "from a notification on this iPhone", D241).
    @State private var place = ""

    /// Why nothing can be allowed now ("" when it can): xbind's 409, else the state.
    private var stop: String { refusal.isEmpty ? DelegationWords.stopRefusal(workspace.agent.state) : refusal }

    var body: some View {
        VStack(spacing: 0) {
            AgentTrustedBar(title: "XBIN · APPROVAL", asking: workspace.userLabel, position: position) { scene.agentReview = nil }
            if let r = request {
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) { content(r) }
                        .padding(.horizontal, 14).padding(.vertical, 14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if r.isPending { buttons(r) } else { doneBar }
            } else if loaded {
                AgentStateView(title: "Nothing to approve", text: AgentNoticeWords.gone404, more: "", retry: nil)
                doneBar
            } else {
                ProgressView().controlSize(.large).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(XbinColor.background)
        .presentationDetents([.large])
        .presentationDragIndicator(.hidden)
        .interactiveDismissDisabled(working)
        .task(id: id) {
            request = await workspace.agent.request(id)
            loaded = true
            if let r = request, !r.isPending { place = await AgentNotifications.shared.placeOf(r, in: workspace) }
            duration = AgentChoices.duration(for: choice)
            arming.arm(settle: 0.4) // the sheet's own slide comes first
            widen.arm(settle: 0.4)
        }
        .onChange(of: phase) { _, p in
            if p == .active { arming.arm(); widen.arm() } else { arming.disarm(); widen.disarm() }
        }
        .onChange(of: choice) { _, c in
            duration = AgentChoices.duration(for: c)
            arming.arm()
        }
        // (what Allow means changed with the duration too)
        .onChange(of: duration) { _, _ in arming.arm() }
        // Something covers the sheet: nothing under it is armed.
        .onChange(of: wider) { _, up in
            if up { arming.disarm(); widen.disarm() } else { arming.arm(); widen.arm() }
        }
        .sheet(isPresented: $wider) {
            if let r = request {
                AgentWiderSheet(workspace: workspace, request: r, duration: $duration, stopped: DelegationWords.stopLine(stop)) { ok in
                    wider = false
                    if ok { send(r, choice: .tile) }
                }
            }
        }
    }

    /// "1 of 3" among what waits.
    private var position: String {
        let pending = workspace.agent.requests
        guard pending.count > 1, let i = pending.firstIndex(where: { $0.id == id }) else { return "" }
        return "\(i + 1) of \(pending.count)"
    }

    @ViewBuilder private func content(_ r: DelegateRequest) -> some View {
        let h = DelegationWords.heading(r)
        Text(AgentReveal.heading(h)) // a tile's or a tool's name is never drawn as it came (reveal)
            .accessibilityLabel(Text(verbatim: DelegationWords.revealed(h.plain)))
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("approval-heading")
        if !r.isPending {
            Label { Text(verbatim: r.kind == .unknown ? DelegationWords.unknownState.text : DelegationWords.settled(r)) } icon: { Image(systemName: "info.square") }
                .font(.callout.weight(.semibold))
                .accessibilityIdentifier("approval-settled")
            if !place.isEmpty {
                Text(verbatim: "Already decided \(place).").font(.callout).foregroundStyle(XbinColor.muted)
                    .accessibilityIdentifier("approval-decided-where")
            }
        }
        if r.preview["readWeb"]?.boolValue == true { AgentReadWebBanner() }
        switch r.kind {
        case .session: sessionRows(r)
        case .call, .template: callRows(r)
        case .web, .mcp: AgentNetRows(request: r)
        case .unknown: EmptyView() // a newer xbind's kind: said above, never decided here
        }
        if !r.summary.isEmpty { AgentQuote(label: "The agent's own words — not checked by xbin", text: r.summary) }
        if r.kind == .call || r.kind == .mcp, r.isPending { choices(r) }
        if let problem {
            Label { Text(verbatim: problem) } icon: { Image(systemName: "exclamationmark.octagon") }
                .font(.callout).foregroundStyle(XbinColor.danger)
        }
        Text(verbatim: footer(r)).font(.caption.monospacedDigit()).foregroundStyle(XbinColor.muted)
    }

    private func footer(_ r: DelegateRequest) -> String {
        let asked = DelegationWords.asked(created: r.created, expires: r.isPending ? r.expires : nil)
        if r.kind == .call || r.kind == .mcp { return asked }
        return [asked, r.kind == .web ? "asked every time" : "asked every time · change in Preferences"].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    // MARK: A tile call, a tile's creation

    @ViewBuilder private func callRows(_ r: DelegateRequest) -> some View {
        let t = DelegationWords.tile(r)
        let sends = DelegationWords.sends(r, all: showAll)
        VStack(alignment: .leading, spacing: 0) {
            if r.kind == .call {
                ApprovalRow(label: "Tile") {
                    Text(AgentReveal.attributed(t.name, font: .body.weight(.semibold)))
                    Text(AgentReveal.attributed([t.path, t.org].filter { !$0.isEmpty }.joined(separator: " · "), font: XbinFont.code)).foregroundStyle(XbinColor.muted)
                    let level = DelegationWords.levelWords(r.preview["level"]?.stringValue ?? "")
                    if !level.isEmpty { Text(verbatim: level).font(.caption).foregroundStyle(XbinColor.muted) }
                }
            }
            ApprovalRow(label: "Action") {
                Text(verbatim: "\(r.method) \(r.target)").font(XbinFont.code).textSelection(.enabled)
                    .accessibilityLabel(Text(verbatim: "\(r.method) \(r.target)"))
            }
            ApprovalRow(label: "Sends") {
                if !sends.text.isEmpty {
                    AgentHiddenNote(text: r.body)
                    Text(AgentReveal.attributed(sends.clipped ? sends.text + "…" : sends.text, font: XbinFont.code))
                        .textSelection(.enabled)
                        .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                        .background(XbinColor.surface2, in: .xbinPlate)
                    if sends.clipped { Button("Show all") { showAll = true }.font(.caption).accessibilityIdentifier("approval-show-all") }
                }
                Text(verbatim: sends.note).font(.caption).foregroundStyle(XbinColor.muted)
                if sends.binary {
                    Label("Allowing this allows bytes nobody could read.", systemImage: "exclamationmark.triangle")
                        .font(.caption.weight(.semibold)).foregroundStyle(XbinColor.warn)
                }
            }
            if r.kind == .call {
                ApprovalRow(label: "Undo") { Text(AgentReveal.attributed(DelegationWords.undoWords(r), font: .callout)) }
            }
        }
    }

    /// The three ways to allow: radio rows, Once first; the third is WIDER.
    @ViewBuilder private func choices(_ r: DelegateRequest) -> some View {
        let all = DelegationWords.grantChoices(r)
        VStack(alignment: .leading, spacing: 6) {
            Text("Allow").font(.caption.weight(.semibold)).foregroundStyle(XbinColor.muted)
            ForEach(all) { c in
                // Once narrows and is never held; a standing choice widens.
                let widening = AgentWidening.widens(from: choice, to: c.id)
                Button {
                    if AgentWidening.takes(widening: widening, armed: widen.armed) { choice = c.id }
                } label: {
                    HStack(alignment: .top, spacing: 10) {
                        Image(systemName: choice == c.id ? "smallcircle.filled.circle" : "circle")
                            .foregroundStyle(choice == c.id ? XbinColor.accent : XbinColor.muted)
                        VStack(alignment: .leading, spacing: 2) {
                            // (the tag under the words at accessibility sizes: neither is cut)
                            let row = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4)) : AnyLayout(HStackLayout(spacing: 6))
                            row {
                                Text(verbatim: c.label).font(.body.weight(.semibold)).multilineTextAlignment(.leading)
                                if c.wider { AgentTag(text: "WIDER", symbol: "exclamationmark.triangle", tone: "warn") }
                            }
                            Text(verbatim: c.text).font(.caption).foregroundStyle(XbinColor.muted).multilineTextAlignment(.leading)
                        }
                        Spacer(minLength: 0)
                    }
                    .padding(10)
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .background(choice == c.id ? XbinColor.selection : XbinColor.surface, in: .xbinPlate)
                    .overlay { RoundedRectangle.xbinPlate.strokeBorder(choice == c.id ? XbinColor.accent : XbinColor.border, lineWidth: 1) }
                }
                .buttonStyle(.plain)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(Text(verbatim: "\(c.label)\(c.wider ? ", wider" : ""). \(c.text)"))
                .accessibilityAddTraits(choice == c.id ? [.isButton, .isSelected] : .isButton)
                .accessibilityIdentifier("approval-choice-\(c.id.rawValue)")
                .widens(armed: !widening || widen.armed)
            }
            if choice == .call || choice == .tool {
                AgentDurationPicker(duration: $duration, armed: widen.armed, id: "approval-duration")
            }
        }
    }

    // MARK: A tile agent's start

    @ViewBuilder private func sessionRows(_ r: DelegateRequest) -> some View {
        let s = DelegationWords.sessionRows(r)
        let t = DelegationWords.tile(r)
        if !s.danger.isEmpty {
            Label { Text(verbatim: s.danger).font(.callout.weight(.semibold)) } icon: { Image(systemName: "exclamationmark.octagon.fill") }
                .foregroundStyle(XbinColor.danger)
                .padding(10).frame(maxWidth: .infinity, alignment: .leading)
                .background(XbinColor.dangerBackground, in: .xbinPlate)
                .accessibilityIdentifier("approval-danger")
        }
        VStack(alignment: .leading, spacing: 0) {
            // The mode leads: it is what stands between the brief and a change.
            ApprovalRow(label: "Mode") {
                AgentTag(text: s.modeTag, symbol: s.modeAsks ? "checkmark.shield" : "exclamationmark.octagon", tone: s.modeAsks ? "ok" : "danger")
                Text(verbatim: s.modeText).font(.callout)
                if !s.modeName.isEmpty { Text(verbatim: s.modeName).font(XbinFont.code).foregroundStyle(XbinColor.muted) }
            }
            ApprovalRow(label: "Tile") {
                Text(AgentReveal.attributed(t.name, font: .body.weight(.semibold)))
                Text(AgentReveal.attributed([t.path, t.org].filter { !$0.isEmpty }.joined(separator: " · "), font: XbinFont.code)).foregroundStyle(XbinColor.muted)
            }
            ApprovalRow(label: "Coding agent") { Text(verbatim: s.agent).font(.callout) }
            if !s.signIn.isEmpty {
                // what the session runs on, in xbin's words (D230): a saved sign-in goes in only by a tick on the web's sheet
                ApprovalRow(label: "Sign-in") {
                    Text(verbatim: s.signIn).font(.callout)
                    if !s.signInHolds.isEmpty { Text(verbatim: s.signInHolds).font(.callout).foregroundStyle(XbinColor.muted) }
                    // a saved sign-in is a line ticked on the web (with the tile's trust line where it isn't only theirs): from here the session has none
                    if !s.signInTrust.isEmpty {
                        if !s.signInTrustWhose.isEmpty { Text(verbatim: s.signInTrustWhose).font(.callout.weight(.semibold)) }
                        Text(verbatim: s.signInTrust).font(.callout).foregroundStyle(XbinColor.muted)
                    }
                }
                .accessibilityIdentifier("approval-signin")
            }
            ApprovalRow(label: "Network") { Text(verbatim: s.network).font(.callout) }
            ApprovalRow(label: "Other tiles") { Text(verbatim: s.others).font(.callout) }
        }
        let brief = DelegationWords.clipText(s.brief, all: showAllBrief)
        VStack(alignment: .leading, spacing: 4) {
            Text("What it will be told — sent exactly like this").font(.caption.weight(.semibold)).foregroundStyle(XbinColor.muted)
            if !s.briefBy.isEmpty { Text(verbatim: s.briefBy).font(.caption).accessibilityIdentifier("approval-brief-by") }
            AgentHiddenNote(text: s.brief)
            Text(AgentReveal.attributed(s.brief.isEmpty ? "Nothing: it starts without a first message." : brief.clipped ? brief.text + "…" : brief.text, font: XbinFont.code))
                .textSelection(.enabled)
                .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                .background(XbinColor.surface2, in: .xbinPlate)
            if brief.clipped { Text(verbatim: DelegationWords.clipNote(brief)).font(.caption).foregroundStyle(XbinColor.muted) }
        }
        .accessibilityElement(children: .combine)
        if brief.clipped { Button("Show all") { showAllBrief = true }.font(.caption).accessibilityIdentifier("approval-brief-show-all") }
        Text(verbatim: s.comes).font(.callout).foregroundStyle(XbinColor.muted)
    }

    // MARK: Deciding

    private func buttons(_ r: DelegateRequest) -> some View {
        AgentDecisionBar(deny: "Deny", allow: DelegationWords.allowLabel(r, choice: choice),
                         allowSpoken: DelegationWords.allowSpoken(r, choice: choice),
                         primaryDeny: r.neverAsks, armed: arming.armed, working: working,
                         stopped: DelegationWords.stopLine(stop),
                         onDeny: { deny(r) },
                         onAllow: {
                             // The wider grant is never one tap: its own sheet, Go back first.
                             if r.kind == .call, choice == .tile { wider = true } else { send(r, choice: choice) }
                         })
    }

    private var doneBar: some View {
        Button { scene.agentReview = nil } label: { Text("Done").frame(maxWidth: .infinity, minHeight: 44) }
            .xbinSecondary()
            .padding(.horizontal, 14).padding(.vertical, 10)
            .accessibilityIdentifier("approval-done")
    }

    private func send(_ r: DelegateRequest, choice: GrantChoice.ID) {
        working = true
        problem = nil
        Task {
            let out = await workspace.agent.approve(r, choice: choice, duration: duration)
            working = false
            settle(out, allowed: true)
        }
    }

    private func deny(_ r: DelegateRequest) {
        working = true
        problem = nil
        Task {
            let out = await workspace.agent.deny(r)
            working = false
            settle(out, allowed: false)
        }
    }

    private func settle(_ out: DelegateOutcome, allowed: Bool) {
        switch out {
        case .done:
            if allowed { Haptics.settle() }
            next()
        case .gone(let why):
            // Decided elsewhere, expired or withdrawn meanwhile: show what it is now.
            problem = "It no longer waits: \(why)"
            Task { request = await workspace.agent.request(id) }
        case .failed(let why), .stepUp(let why, _):
            problem = why
        case .unseen:
            problem = "xbin wants this body approved as unseen bytes. Open it again."
        case .stopped(let why, _):
            // stopped meanwhile: the reason shows where Allow was, and nothing is sent again
            refusal = why
        }
    }

    /// One request at a time: the next that waits shows after a decision.
    private func next() {
        if let n = workspace.agent.requests.first(where: { $0.id != id && $0.isPending }) {
            scene.agentReview = .request(n.id)
        } else {
            scene.agentReview = nil
        }
    }
}

/// The wider grant's second step: what "every change" means, for how long,
/// and two buttons — Go back, the primary one, and Allow every change,
/// which is armed late and asks Face ID.
struct AgentWiderSheet: View {
    let workspace: WorkspaceModel
    let request: DelegateRequest
    @Binding var duration: String
    /// Why nothing can be allowed now ("" when it can).
    var stopped: String = ""
    /// true: allow (after Face ID); false: go back.
    let done: (Bool) -> Void

    @Environment(\.scenePhase) private var phase
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var arming = AgentArming()
    /// "Until I take it back" widens "for one hour": armed like Allow.
    @State private var widen = AgentArming()
    @State private var working = false
    @State private var refused = false

    var body: some View {
        let w = DelegationWords.widerWords(request)
        VStack(spacing: 0) {
            AgentTrustedBar(title: "XBIN · APPROVAL", asking: workspace.userLabel)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 8) {
                        AgentTag(text: "WIDER", symbol: "exclamationmark.triangle", tone: "warn")
                        Spacer()
                    }
                    Text(AgentReveal.attributed(w.heading, font: .title3.weight(.semibold))).accessibilityAddTraits(.isHeader)
                        .accessibilityIdentifier("wider-heading")
                    Text(AgentReveal.attributed(w.means, font: .body))
                    AgentDurationPicker(duration: $duration, armed: widen.armed, id: "wider-duration")
                    Text(verbatim: w.ends).font(.callout).foregroundStyle(XbinColor.muted)
                    if refused {
                        Label("It wasn't you, so nothing was allowed.", systemImage: "faceid").font(.callout).foregroundStyle(XbinColor.danger)
                    }
                }
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            AgentDecisionBar(deny: w.back, allow: w.allow, allowSpoken: DelegationWords.allowSpoken(request, choice: .tile),
                             primaryDeny: true, id: "wider", armed: arming.armed, working: working, stopped: stopped,
                             onDeny: { done(false) }, onAllow: allow)
        }
        .background(XbinColor.background)
        // (at accessibility sizes the words and the two stacked buttons need the whole height)
        .presentationDetents(typeSize.isAccessibilitySize ? [.large] : [.medium, .large])
        .interactiveDismissDisabled(working)
        .onAppear {
            // the whole tile, however the first sheet's duration stood: one hour unless the person widens it here
            duration = AgentChoices.duration(for: .tile)
            arming.arm(settle: 0.4)
            widen.arm(settle: 0.4)
        }
        .onChange(of: phase) { _, p in
            if p == .active { arming.arm(); widen.arm() } else { arming.disarm(); widen.disarm() }
        }
        .onChange(of: duration) { _, _ in arming.arm() }
    }

    /// Face ID first — a local gate: it is the person holding the phone.
    /// It is never the server's recent sign-in.
    private func allow() {
        working = true
        refused = false
        Task {
            let ok = AgentChoices.biometricsForWider ? await AgentGate.confirm("Allow every change to \(DelegationWords.tile(request).name)") : true
            working = false
            if ok { done(true) } else { refused = true; arming.arm() }
        }
    }
}

/// The local gate (Face ID, Touch ID, else the passcode) before the wider
/// grant and before admin changes are sent. Debug builds' UI tests answer
/// it with `-XbinAgentGate allow|deny` (a simulator has no face).
@MainActor
enum AgentGate {
    static func confirm(_ reason: String) async -> Bool {
        #if DEBUG
        switch UserDefaults.standard.string(forKey: "XbinAgentGate") {
        case "allow": return true
        case "deny": return false
        default: break
        }
        #endif
        return await AppLock.unlock(reason: reason)
    }
}
