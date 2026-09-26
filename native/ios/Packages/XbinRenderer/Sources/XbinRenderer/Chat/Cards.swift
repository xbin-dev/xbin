#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// The rounded surface every chat card sits on.
struct ChatCard: ViewModifier {
    func body(content: Content) -> some View {
        content
            .background(XbinColor.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(XbinColor.border, lineWidth: 0.5))
    }
}

/// A tool call: icon, headline, chips, a state badge (spinner while
/// writing/running, ✓, ✗, ⊘) and, when it has content, a fold. `onOpen`
/// adds a button that opens the call full screen (a subagent).
public struct ToolCardView<Content: View>: View {
    public let card: ChatToolCard
    @Binding public var isOpen: Bool
    public var hasContent: Bool
    public var onOpen: (@MainActor () -> Void)?
    let content: Content

    public init(card: ChatToolCard, isOpen: Binding<Bool>, hasContent: Bool = true, onOpen: (@MainActor () -> Void)? = nil,
                @ViewBuilder content: () -> Content) {
        self.card = card
        _isOpen = isOpen
        self.hasContent = hasContent
        self.onOpen = onOpen
        self.content = content()
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .center, spacing: 8) {
                Button {
                    if hasContent { withAnimation(.snappy) { isOpen.toggle() } }
                } label: {
                    header.contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityValue(hasContent ? (isOpen ? "expanded" : "collapsed") : "")
                if let onOpen {
                    Button(action: onOpen) {
                        Image(systemName: XbinIcons.UI.expandFull).font(.footnote)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Open")
                }
            }
            .padding(12)
            if isOpen && hasContent {
                Divider()
                VStack(alignment: .leading, spacing: 8) { content }
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .modifier(ChatCard())
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: XbinIcons.symbol(card.icon) ?? "wrench.and.screwdriver")
                .foregroundStyle(XbinColor.muted)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 4) {
                Text(verbatim: card.title)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(XbinColor.text)
                    .lineLimit(2)
                if !card.chips.isEmpty {
                    FlowLayout(spacing: 4) {
                        ForEach(Array(card.chips.enumerated()), id: \.offset) { _, chip in
                            Pill(text: chip.text, tone: chip.tone, small: true)
                        }
                    }
                }
            }
            Spacer(minLength: 4)
            ToolStateBadge(state: card.state)
            if hasContent {
                Image(systemName: XbinIcons.UI.chevronForward)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(XbinColor.muted)
                    .rotationEffect(.degrees(isOpen ? 90 : 0))
            }
        }
    }
}

extension ToolCardView where Content == EmptyView {
    public init(card: ChatToolCard, onOpen: (@MainActor () -> Void)? = nil) {
        self.init(card: card, isOpen: .constant(false), hasContent: false, onOpen: onOpen) { EmptyView() }
    }
}

/// A tool call's state at the end of its header.
struct ToolStateBadge: View {
    let state: ToolState?

    var body: some View {
        switch state {
        case .writing?, .running?:
            ProgressView().controlSize(.small).accessibilityLabel("Running")
        case .ok?:
            Image(systemName: XbinIcons.UI.stateOK).foregroundStyle(XbinColor.tone(.ok)).accessibilityLabel("Done")
        case .error?:
            Image(systemName: XbinIcons.UI.stateError).foregroundStyle(XbinColor.tone(.danger)).accessibilityLabel("Failed")
        case .canceled?:
            Image(systemName: XbinIcons.UI.stateCanceled).foregroundStyle(XbinColor.muted).accessibilityLabel("Canceled")
        case nil:
            EmptyView()
        }
    }
}

/// A permission card (D77 rules live in the model: the agent's own option
/// order, destructive rejects, the first plain allow prominent). Settled:
/// which option, and who answered.
public struct ApprovalView: View {
    public let approval: ChatApproval
    public var onChoose: @MainActor (_ optionID: String, _ feedback: String) -> Void
    @State private var feedback = ""

    public init(approval: ChatApproval, onChoose: @escaping @MainActor (_ optionID: String, _ feedback: String) -> Void) {
        self.approval = approval
        self.onChoose = onChoose
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: approval.settled == nil ? XbinIcons.UI.approval : XbinIcons.UI.answered)
                    .foregroundStyle(approval.settled == nil ? XbinColor.accentText : XbinColor.tone(.ok))
                Text(verbatim: approval.title).font(.headline)
            }
            if let text = approval.text {
                Text(verbatim: text)
                    .font(.callout)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            if let note = approval.note {
                Text(verbatim: note).font(.footnote).foregroundStyle(XbinColor.muted)
            }
            if let settled = approval.settledText {
                Label(settled, systemImage: XbinIcons.UI.checkmark)
                    .font(.subheadline)
                    .foregroundStyle(XbinColor.muted)
            } else {
                if approval.feedback {
                    TextField("Feedback (optional)", text: $feedback, axis: .vertical)
                        .lineLimit(2...5)
                        .padding(10)
                        .background(XbinColor.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }
                VStack(spacing: 8) {
                    ForEach(Array(approval.options.enumerated()), id: \.offset) { i, option in
                        optionButton(option, emphasis: approval.emphasis(i))
                    }
                }
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ChatCard())
    }

    @ViewBuilder
    private func optionButton(_ option: ApprovalOption, emphasis: ApprovalOption.Emphasis) -> some View {
        let choose = onChoose
        let text = feedback
        let button = Button(role: emphasis == .destructive ? .destructive : nil) {
            choose(option.id, approval.feedback ? text : "")
        } label: {
            Text(verbatim: option.label).frame(maxWidth: .infinity)
        }
        .controlSize(.large)
        if emphasis == .prominent {
            button.buttonStyle(.borderedProminent).tint(XbinColor.tint).foregroundStyle(XbinColor.onTint)
        } else {
            // A reject is red (the reference's r-destructive), not the tint.
            button.buttonStyle(.bordered).modifier(DangerTint(on: emphasis == .destructive))
        }
    }
}

/// The agent's plan: a checklist with a done count.
public struct PlanView: View {
    public let plan: ChatPlan

    public init(plan: ChatPlan) { self.plan = plan }

    public var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Plan").font(.subheadline.weight(.semibold))
                Spacer()
                Text(verbatim: plan.summary).font(.caption).foregroundStyle(XbinColor.muted)
            }
            ForEach(Array(plan.entries.enumerated()), id: \.offset) { _, entry in
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Image(systemName: Self.symbol(entry.status))
                        .foregroundStyle(Self.color(entry.status))
                    Text(verbatim: entry.text)
                        .font(.subheadline)
                        .foregroundStyle(entry.status == .completed ? XbinColor.muted : XbinColor.text)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ChatCard())
    }

    static func symbol(_ s: PlanStatus) -> String {
        switch s {
        case .completed: return XbinIcons.UI.planDone
        case .inProgress: return XbinIcons.UI.planActive
        case .pending: return XbinIcons.UI.planPending
        }
    }

    static func color(_ s: PlanStatus) -> Color {
        switch s {
        case .completed: return XbinColor.tone(.ok)
        case .inProgress: return XbinColor.accentText
        case .pending: return XbinColor.muted
        }
    }
}

/// Changed files (status letter, path, +/−) and, when given, the unified
/// patch with added/removed lines coloured. `onOpenFile` makes rows
/// tappable.
public struct DiffView: View {
    public let diff: ChatDiff
    public var onOpenFile: (@MainActor (String) -> Void)?

    public init(diff: ChatDiff, onOpenFile: (@MainActor (String) -> Void)? = nil) {
        self.diff = diff
        self.onOpenFile = onOpenFile
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(diff.files) { file in
                if let open = onOpenFile {
                    Button { open(file.path) } label: { fileRow(file).contentShape(Rectangle()) }
                        .buttonStyle(.plain)
                } else {
                    fileRow(file)
                }
            }
            if !diff.lines.isEmpty {
                if !diff.files.isEmpty { Divider().padding(.vertical, 6) }
                ScrollView(.horizontal, showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(diff.lines.enumerated()), id: \.offset) { _, line in
                            Text(verbatim: line.text.isEmpty ? " " : line.text)
                                .font(.caption.monospaced())
                                .foregroundStyle(Self.foreground(line.kind))
                                .fixedSize(horizontal: true, vertical: false)
                                .padding(.horizontal, 4)
                                .background(Self.background(line.kind))
                        }
                    }
                }
                .textSelection(.enabled)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ChatCard())
    }

    private func fileRow(_ f: ChatDiff.File) -> some View {
        HStack(spacing: 8) {
            Text(verbatim: f.letter)
                .font(.caption.weight(.bold).monospaced())
                .foregroundStyle(Self.letterColor(f.letter))
                .frame(width: 16)
            Text(verbatim: f.path)
                .font(.footnote.monospaced())
                .foregroundStyle(XbinColor.text)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 4)
            if f.added > 0 { Text(verbatim: "+\(f.added)").foregroundStyle(XbinColor.toneText(.ok)) }
            if f.removed > 0 { Text(verbatim: "−\(f.removed)").foregroundStyle(XbinColor.toneText(.danger)) }
        }
        .font(.caption.monospacedDigit())
        .padding(.vertical, 5)
        .accessibilityElement(children: .combine)
    }

    static func letterColor(_ l: String) -> Color {
        switch l {
        case "A": return XbinColor.toneText(.ok)
        case "D": return XbinColor.toneText(.danger)
        case "R": return Color(uiColor: .systemBlue)
        default: return XbinColor.toneText(.warn)
        }
    }

    static func foreground(_ k: ChatDiff.Line.Kind) -> Color {
        switch k {
        case .file: return XbinColor.muted
        case .hunk: return Color(uiColor: .systemBlue)
        case .added: return XbinColor.toneText(.ok)
        case .removed: return XbinColor.toneText(.danger)
        case .context: return XbinColor.text
        }
    }

    static func background(_ k: ChatDiff.Line.Kind) -> Color {
        switch k {
        case .added: return XbinColor.tone(.ok).opacity(0.12)
        case .removed: return XbinColor.tone(.danger).opacity(0.12)
        default: return Color.clear
        }
    }
}
#endif
