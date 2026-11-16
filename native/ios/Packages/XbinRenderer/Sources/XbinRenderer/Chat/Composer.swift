#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// The message composer: a growing text field with send (or stop while
/// busy), an attach button when attaching is possible, attachment chips
/// with upload progress, suggestion chips above, and a slash-command
/// palette while the draft is a `/word`. Rev 2 (D233): context chips in
/// the box, quick replies above it, a hint under it (``ComposerExtras``). The draft is the caller's binding,
/// set with committed text only: while an input method composes, the
/// binding keeps the text before the composition (``XbinTextArea``), and
/// send commits the composition first.
public struct ComposerView<Chips: View>: View {
    public let composer: ChatComposer
    @Binding public var text: String
    public var onSend: @MainActor (String) -> Void
    public var onStop: (@MainActor () -> Void)?
    public var onAttach: (@MainActor () -> Void)?
    public var onRemoveAttachment: (@MainActor (String) -> Void)?
    /// Rev 2: what a tap on a context chip, its ✕ and a quick reply do.
    public var extras: ComposerExtras
    let chips: Chips
    @State private var input = TextInputHandle()
    @State private var composing = false

    public init(composer: ChatComposer, text: Binding<String>, onSend: @escaping @MainActor (String) -> Void,
                onStop: (@MainActor () -> Void)? = nil, onAttach: (@MainActor () -> Void)? = nil,
                onRemoveAttachment: (@MainActor (String) -> Void)? = nil, extras: ComposerExtras = ComposerExtras(),
                @ViewBuilder chips: () -> Chips) {
        self.composer = composer
        _text = text
        self.onSend = onSend
        self.onStop = onStop
        self.onAttach = onAttach
        self.onRemoveAttachment = onRemoveAttachment
        self.extras = extras
        self.chips = chips()
    }

    public var body: some View {
        let matches = composer.slashMatches(text)
        VStack(alignment: .leading, spacing: 8) {
            if !matches.isEmpty {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(matches) { cmd in
                        Button {
                            text = "/\(cmd.bare) "
                        } label: {
                            VStack(alignment: .leading, spacing: 2) {
                                HStack(spacing: 6) {
                                    Text(verbatim: "/\(cmd.bare)").font(.subheadline.weight(.semibold).monospaced())
                                    if let hint = cmd.hint {
                                        Text(verbatim: hint).font(.footnote.monospaced()).foregroundStyle(XbinColor.muted)
                                    }
                                }
                                if let d = cmd.description {
                                    Text(verbatim: d).font(.footnote).foregroundStyle(XbinColor.muted)
                                }
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 8)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
                .modifier(ChatCard())
            }
            // Rev 2: quick replies, above the box; they scroll sideways and
            // stay with the keyboard down. Where the app says whose
            // suggestions they are (the workspace agent's, D241), it says so.
            if !composer.replies.isEmpty {
                if let label = extras.repliesLabel {
                    Text(verbatim: label).font(.caption).foregroundStyle(XbinColor.muted)
                        .accessibilityIdentifier("composer-replies-label")
                }
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(composer.replies) { r in QuickReply(reply: r, disabled: composer.disabled, onReply: extras.onReply) }
                    }
                }
            }
            if Chips.self != EmptyView.self {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) { chips }
                }
            }
            if !composer.attachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(composer.attachments) { a in AttachmentChip(attachment: a, onRemove: onRemoveAttachment) }
                    }
                }
            }
            HStack(alignment: .bottom, spacing: 8) {
                if composer.canAttach, let onAttach {
                    Button(action: onAttach) {
                        ComposerPlate(symbol: XbinIcons.UI.attach, fill: XbinColor.fill, ink: XbinColor.text)
                    }
                    .buttonStyle(.plain)
                    .disabled(composer.disabled)
                    .accessibilityLabel("Attach")
                }
                // The row's width, as the TextField it replaced took: the
                // text view alone measured at its draft's width, and the
                // composer shrank to it ("Thank / you," beside send).
                // Base Two's field: the panel, a 1 pt edge, 2 pt corners
                // (it was a pill, as the send and attach buttons were
                // circles; the web's composer is square).
                // Dictating (D241): the level, the time and the words so far
                // in place of the field; ✓ puts them in the draft.
                if let d = extras.dictation, d.listening {
                    DictationBar(model: d) { heard in
                        let t = heard.trimmingCharacters(in: .whitespacesAndNewlines)
                        if !t.isEmpty { text = text.isEmpty ? t : text + " " + t }
                    }
                } else {
                // Rev 2: what the next message is about sits inside the
                // box, at its top edge, over the text.
                VStack(alignment: .leading, spacing: 0) {
                    if !composer.context.isEmpty {
                        FlowLayout(spacing: 6) {
                            ForEach(composer.context) { c in
                                ContextChip(chip: c, onTap: extras.onContext, onRemove: extras.onRemoveContext)
                            }
                        }
                        .padding(.horizontal, 6)
                    }
                    XbinTextArea(text: text, style: Self.style(composer), lines: 1...6, handle: input,
                                 onInput: { text = $0 }, onComposing: { composing = $0 })
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 7)
                        .disabled(composer.disabled)
                }
                .background(XbinColor.surface, in: .xbinPlate)
                .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
                }
                if composer.busy, let onStop {
                    Button(action: onStop) {
                        ComposerPlate(symbol: XbinIcons.UI.stop, fill: XbinColor.text, ink: XbinColor.background, small: true)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Stop")
                }
                if let d = extras.dictation, d.available, !d.listening, text.isEmpty, !composing, !composer.disabled,
                   !composer.busy || onStop == nil || composer.sendWhileBusy {
                    // The mic where Send will be while the draft is empty (D241).
                    Button { Task { await d.start() } } label: {
                        ComposerPlate(symbol: "mic", fill: XbinColor.fill, ink: XbinColor.text)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Dictate")
                    .accessibilityIdentifier("composer-dictate")
                } else if extras.dictation?.listening != true, !composer.busy || onStop == nil || composer.sendWhileBusy {
                    // The accent while there is something to send, else the
                    // neutral fill with the arrow muted (never a near-white
                    // or black square, as the system's disabled button was).
                    let sendable = composer.canSend(text) || (composing && !composer.disabled)
                    Button {
                        // Marked text is committed (and reported) first.
                        input.commitComposition()
                        let draft = text
                        if composer.canSend(draft) { onSend(draft) }
                    } label: {
                        ComposerPlate(symbol: XbinIcons.UI.send, fill: sendable ? XbinColor.accent : XbinColor.fill,
                                      ink: sendable ? XbinColor.onAccent : XbinColor.muted)
                    }
                    .buttonStyle(.plain)
                    .disabled(!sendable)
                    .accessibilityLabel("Send")
                }
            }
            if let problem = extras.dictation?.problem, !problem.isEmpty, extras.dictation?.listening != true {
                Text(verbatim: problem).font(.caption).foregroundStyle(XbinColor.muted)
            }
            // Rev 2: a quiet line under the box.
            if let hint = composer.hint {
                Text(verbatim: hint)
                    .font(.caption)
                    .foregroundStyle(XbinColor.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.bar)
    }
}

extension ComposerView {
    /// The composer's text traits: prose, the placeholder.
    static func style(_ composer: ChatComposer) -> TextInputStyle {
        var s = TextInputStyle()
        s.placeholder = composer.placeholder
        s.accessibilityLabel = composer.placeholder.isEmpty ? "Message" : composer.placeholder
        return s
    }
}

extension ComposerView where Chips == EmptyView {
    public init(composer: ChatComposer, text: Binding<String>, onSend: @escaping @MainActor (String) -> Void,
                onStop: (@MainActor () -> Void)? = nil, onAttach: (@MainActor () -> Void)? = nil,
                onRemoveAttachment: (@MainActor (String) -> Void)? = nil, extras: ComposerExtras = ComposerExtras()) {
        self.init(composer: composer, text: text, onSend: onSend, onStop: onStop, onAttach: onAttach,
                  onRemoveAttachment: onRemoveAttachment, extras: extras) { EmptyView() }
    }
}

/// What a composer's rev-2 parts do (D233): a tap on a context chip, its
/// ✕, a quick reply — each with the item's id. A nil action leaves the
/// part drawn and inert (a chip without ✕, a reply that can't be tapped).
public struct ComposerExtras {
    public var onContext: (@MainActor (String) -> Void)?
    public var onRemoveContext: (@MainActor (String) -> Void)?
    public var onReply: (@MainActor (String) -> Void)?
    /// The app's dictation (D241; ``XbinServices/dictation``); nil: no mic.
    public var dictation: XbinDictationModel?
    /// Whose suggestions the quick replies are (``XbinServices/suggestionsLabel``).
    public var repliesLabel: String?

    public init(onContext: (@MainActor (String) -> Void)? = nil, onRemoveContext: (@MainActor (String) -> Void)? = nil,
                onReply: (@MainActor (String) -> Void)? = nil, dictation: XbinDictationModel? = nil, repliesLabel: String? = nil) {
        self.onContext = onContext
        self.onRemoveContext = onRemoveContext
        self.onReply = onReply
        self.dictation = dictation
        self.repliesLabel = repliesLabel
    }
}

/// A context chip in the composer's box: an icon, the label ("About
/// Orders") and a ✕. Each half is a 44 pt target; the plate drawn behind
/// them is smaller. A `warn` chip (it can't be used as it is) takes the
/// warn tint and says so with its glyph.
private struct ContextChip: View {
    let chip: ChatContextChip
    let onTap: (@MainActor (String) -> Void)?
    let onRemove: (@MainActor (String) -> Void)?

    var body: some View {
        let ink = chip.tone.map(XbinColor.toneText) ?? XbinColor.text
        let face = HStack(spacing: 5) {
            if let symbol = XbinIcons.symbol(chip.icon) ?? (chip.tone == .warn ? "exclamationmark.triangle" : nil) {
                Image(systemName: symbol).font(.caption2)
            }
            Text(verbatim: chip.label).lineLimit(2).multilineTextAlignment(.leading)
        }
        .font(.caption.weight(.medium))
        .foregroundStyle(ink)
        .padding(.leading, 8)
        .padding(.trailing, onRemove == nil ? 8 : 0)
        .frame(minHeight: 44)
        .contentShape(Rectangle())
        HStack(spacing: 0) {
            if let onTap {
                Button { onTap(chip.id) } label: { face }.buttonStyle(.plain)
            } else {
                face
            }
            if let onRemove {
                Button { onRemove(chip.id) } label: {
                    Image(systemName: XbinIcons.UI.close).font(.caption2.weight(.bold)).foregroundStyle(XbinColor.muted)
                        .frame(minWidth: 36, minHeight: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Remove \(chip.label)")
            }
        }
        .background {
            RoundedRectangle.xbinPlate.fill(chip.tone.map { XbinColor.tone($0).opacity(0.15) } ?? XbinColor.fill)
                .padding(.vertical, 7)
        }
    }
}

/// A quick reply above the composer: a bordered button, a 44 pt target.
private struct QuickReply: View {
    let reply: ChatReply
    let disabled: Bool
    let onReply: (@MainActor (String) -> Void)?

    var body: some View {
        Button { onReply?(reply.id) } label: {
            Text(verbatim: reply.label).font(.subheadline.weight(.medium)).lineLimit(1)
                .padding(.horizontal, 4)
                .frame(minHeight: 30)
        }
        .buttonStyle(.bordered)
        .buttonBorderShape(.roundedRectangle(radius: XbinShapes.radius))
        .frame(minHeight: 44)
        .disabled(disabled || onReply == nil)
    }
}

/// A composer button's face: a 36 pt square plate in `fill` with the glyph
/// in `ink`, as the reference draws `.cm-send` and `.cm-attach` (Base Two:
/// no circles).
private struct ComposerPlate: View {
    let symbol: String
    let fill: Color
    let ink: Color
    var small = false

    var body: some View {
        Image(systemName: symbol)
            .font(small ? .footnote.weight(.bold) : .body.weight(.semibold))
            .foregroundStyle(ink)
            .frame(width: 36, height: 36)
            .background(fill, in: .xbinPlate)
            .contentShape(Rectangle())
    }
}

/// An attachment chip: kind icon, name, upload progress, remove.
private struct AttachmentChip: View {
    let attachment: ChatAttachment
    let onRemove: (@MainActor (String) -> Void)?

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: attachment.mime.hasPrefix("image/") ? XbinIcons.UI.image : "doc")
            Text(verbatim: attachment.name).lineLimit(1)
            if attachment.isUploading, let p = attachment.progress {
                Text(verbatim: "\(Int((p * 100).rounded()))%").monospacedDigit().foregroundStyle(XbinColor.muted)
            }
            if let onRemove {
                Button { onRemove(attachment.id) } label: { Image(systemName: XbinIcons.UI.close).font(.caption2.weight(.bold)) }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Remove \(attachment.name)")
            }
        }
        .font(.caption)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(XbinColor.fill, in: .xbinPlate)
    }
}
#endif
