import SwiftUI
import XbinCore
import XbinRenderer

// The parts of the approval sheet a tile agent's start, a follow-up and a
// message add (D250–D253, the app's since D253): the pickers, "Remember for
// this tile", the choice between continuing the agent's own session and a
// new one, a session's facts as chips, and Details. What each says and
// decides is StartWords (XbinCore) — the web chrome's agent-start-words.js,
// held to it by vectors — so these only draw it. Every fact is an element
// of its own: no "·" joins two.

/// Facts as chips that wrap: a session's coding agent, model, mode and the
/// sign-in it carries, and which saved sign-in that is (its name from the
/// person's own list, a chip of its own).
struct AgentChips: View {
    let chips: [String]
    var signInName: String = ""

    var body: some View {
        AgentChipFlow(spacing: 6) {
            ForEach(Array(chips.enumerated()), id: \.offset) { _, c in AgentTag(text: c) }
            if !signInName.isEmpty { AgentTag(text: signInName, symbol: "key") }
        }
        .accessibilityElement(children: .combine)
    }
}

/// A wrapping row (the renderer's line breaking, FlowLayoutMath).
struct AgentChipFlow: Layout {
    var spacing: CGFloat

    private func place(_ width: CGFloat?, _ subviews: Subviews) -> ([CGSize], FlowLayoutMath.Placement) {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        let w = width.flatMap { $0.isFinite ? Double($0) : nil }
        return (sizes, FlowLayoutMath.place(sizes.map { (Double($0.width), Double($0.height)) }, maxWidth: w, spacing: Double(spacing), lineSpacing: Double(spacing)))
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let (_, p) = place(proposal.width, subviews)
        return CGSize(width: proposal.width.flatMap { $0.isFinite ? min($0, CGFloat(p.width)) : nil } ?? CGFloat(p.width), height: CGFloat(p.height))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let (sizes, p) = place(bounds.width, subviews)
        for (i, s) in subviews.enumerated() {
            let o = p.origins[i]
            s.place(at: CGPoint(x: bounds.minX + CGFloat(o.x), y: bounds.minY + CGFloat(o.y)), anchor: .topLeading, proposal: ProposedViewSize(sizes[i]))
        }
    }
}

/// A labelled row of chips (a message's "Tile agent").
struct AgentLabelledChips: View {
    let label: String
    let chips: [String]
    var signInName: String = ""

    var body: some View {
        ApprovalRow(label: label) { AgentChips(chips: chips, signInName: signInName) }
    }
}

/// One of a start's pickers — Coding agent, Model, Mode — with where its
/// value came from under it (an element of its own), and for the mode its
/// chip and plain line. A note on a bypass mode is said as a warning (the
/// web's treatment); the agent's suggestion that isn't picked, too.
struct AgentPickerRow: View {
    let picker: StartPicker
    var modeChip = ""
    var fact = ""
    let choose: (String) -> Void
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric private var height: CGFloat = 40

    var body: some View {
        let big = typeSize.isAccessibilitySize
        let layout = big ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 10))
        layout {
            Text(verbatim: picker.label).font(.caption.weight(.semibold)).foregroundStyle(XbinColor.muted)
                .frame(width: big ? nil : 64, alignment: .leading)
                .accessibilityHidden(true) // (the picker says its own name)
            VStack(alignment: .leading, spacing: 4) {
                Menu {
                    ForEach(picker.options, id: \.self) { o in
                        Button {
                            if o.value != picker.value { choose(o.value) }
                        } label: {
                            if o.value == picker.value { Label(o.label, systemImage: "checkmark") } else { Text(verbatim: o.label) }
                        }
                    }
                } label: {
                    HStack(spacing: 6) {
                        Text(verbatim: shown).font(.callout.weight(.semibold)).foregroundStyle(XbinColor.text)
                            .multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 4)
                        Image(systemName: "chevron.up.chevron.down").font(.caption).foregroundStyle(XbinColor.muted)
                    }
                    .padding(.horizontal, 10)
                    .frame(maxWidth: .infinity, minHeight: max(44, min(height, 72)), alignment: .leading)
                    .background(XbinColor.surface, in: .xbinPlate)
                    .overlay { RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1) }
                    .contentShape(Rectangle())
                }
                .accessibilityLabel(Text(verbatim: picker.label))
                .accessibilityValue(Text(verbatim: shown))
                .accessibilityIdentifier("approval-pick-\(picker.id)")
                if !picker.from.isEmpty {
                    Text(verbatim: picker.from).font(.caption).foregroundStyle(XbinColor.muted)
                        .accessibilityIdentifier("approval-from-\(picker.id)")
                }
                if !modeChip.isEmpty {
                    AgentTag(text: modeChip).accessibilityIdentifier("approval-mode-chip")
                }
                if !fact.isEmpty {
                    Text(verbatim: fact).font(.callout).fixedSize(horizontal: false, vertical: true).accessibilityIdentifier("approval-mode-fact")
                }
                if !picker.note.isEmpty {
                    if picker.id == "mode" { // a bypass mode the person picked: it skips the coding agent's own checks too
                        Label { Text(verbatim: picker.note).font(.callout) } icon: { Image(systemName: "exclamationmark.triangle") }
                            .foregroundStyle(XbinColor.warn)
                            .accessibilityElement(children: .ignore) // (the words, not the glyph's name)
                            .accessibilityLabel(Text(verbatim: picker.note))
                            .accessibilityIdentifier("approval-mode-warn")
                    } else {
                        Text(verbatim: picker.note).font(.caption).foregroundStyle(XbinColor.muted).accessibilityIdentifier("approval-note-\(picker.id)")
                    }
                }
                if !picker.warn.isEmpty {
                    Label { Text(verbatim: picker.warn).font(.callout) } icon: { Image(systemName: "exclamationmark.triangle") }
                        .foregroundStyle(XbinColor.danger)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(Text(verbatim: picker.warn))
                        .accessibilityIdentifier("approval-not-taken")
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 8)
        .overlay(alignment: .bottom) { Rectangle().fill(XbinColor.border).frame(height: 1) }
        .accessibilityElement(children: .contain)
    }

    /// The option picked, as it reads; a value no option has, as xbind named it.
    private var shown: String { picker.valueLabel.isEmpty ? picker.value : picker.valueLabel }
}

/// "Remember for this tile": a tick of the person's own, with what it keeps.
struct AgentRememberRow: View {
    let label: String
    let note: String
    let on: Bool
    let set: (Bool) -> Void

    var body: some View {
        Button { set(!on) } label: {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: on ? "checkmark.square.fill" : "square").foregroundStyle(on ? XbinColor.accent : XbinColor.muted)
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: label).font(.callout.weight(.semibold)).multilineTextAlignment(.leading)
                    Text(verbatim: note).font(.caption).foregroundStyle(XbinColor.muted).multilineTextAlignment(.leading)
                }
                Spacer(minLength: 0)
            }
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: label))
        .accessibilityHint(Text(verbatim: note))
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
        .accessibilityIdentifier("approval-remember")
    }
}

/// A follow-up's two ways (D251): continue the agent's own session there —
/// its facts as chips — or start a new session instead (the pickers).
struct AgentContinueChoice: View {
    let cont: StartView.Cont
    let continuing: Bool
    var signInName = ""
    let choose: (Bool) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: StartWords.Sheet.whereItGoes).font(.caption.weight(.semibold)).foregroundStyle(XbinColor.muted)
                .accessibilityAddTraits(.isHeader)
            radio(on: continuing, id: "approval-continue", spoken: ([cont.label] + cont.chips + (signInName.isEmpty ? [] : [signInName])).joined(separator: ". "), yes: true) {
                Text(verbatim: cont.label).font(.body.weight(.semibold)).multilineTextAlignment(.leading)
                AgentChips(chips: cont.chips, signInName: signInName)
            }
            radio(on: !continuing, id: "approval-new-session", spoken: cont.sub, yes: false) {
                Text(verbatim: cont.sub).font(.body).multilineTextAlignment(.leading)
            }
        }
        .accessibilityElement(children: .contain)
    }

    private func radio<Content: View>(on: Bool, id: String, spoken: String, yes: Bool, @ViewBuilder content: () -> Content) -> some View {
        Button { if !on { choose(yes) } } label: {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: on ? "smallcircle.filled.circle" : "circle").foregroundStyle(on ? XbinColor.accent : XbinColor.muted)
                VStack(alignment: .leading, spacing: 6) { content() }
                Spacer(minLength: 0)
            }
            .padding(10)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .background(on ? XbinColor.selection : XbinColor.surface, in: .xbinPlate)
            .overlay { RoundedRectangle.xbinPlate.strokeBorder(on ? XbinColor.accent : XbinColor.border, lineWidth: 1) }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: spoken))
        .accessibilityAddTraits(on ? [.isButton, .isSelected] : .isButton)
        .accessibilityIdentifier(id)
    }
}

/// The call itself, folded: what the sheet's Details shows.
struct AgentDetails: View {
    let call: String
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button { open.toggle() } label: {
                Label { Text(verbatim: StartWords.Sheet.details) } icon: { Image(systemName: open ? "chevron.down" : "chevron.right") }
                    .font(.caption).foregroundStyle(XbinColor.muted)
                    .frame(minHeight: 44, alignment: .leading).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(open ? [.isButton, .isSelected] : .isButton)
            .accessibilityIdentifier("approval-details")
            if open {
                Text(verbatim: call).font(XbinFont.code).textSelection(.enabled).accessibilityIdentifier("approval-details-call")
            }
        }
    }
}
