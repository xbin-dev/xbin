#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// The message composer: a growing text field with send (or stop while
/// busy), an attach button when attaching is possible, attachment chips
/// with upload progress, suggestion chips above, and a slash-command
/// palette while the draft is a `/word`. The draft is the caller's binding,
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
    let chips: Chips
    @State private var input = TextInputHandle()
    @State private var composing = false

    public init(composer: ChatComposer, text: Binding<String>, onSend: @escaping @MainActor (String) -> Void,
                onStop: (@MainActor () -> Void)? = nil, onAttach: (@MainActor () -> Void)? = nil,
                onRemoveAttachment: (@MainActor (String) -> Void)? = nil, @ViewBuilder chips: () -> Chips) {
        self.composer = composer
        _text = text
        self.onSend = onSend
        self.onStop = onStop
        self.onAttach = onAttach
        self.onRemoveAttachment = onRemoveAttachment
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
                        Image(systemName: XbinIcons.UI.attach).font(.body.weight(.semibold)).frame(width: 22, height: 22)
                    }
                    .buttonStyle(.bordered)
                    .buttonBorderShape(.circle)
                    .disabled(composer.disabled)
                    .accessibilityLabel("Attach")
                }
                // The row's width, as the TextField it replaced took: the
                // text view alone measured at its draft's width, and the
                // composer shrank to it ("Thank / you," beside send).
                XbinTextArea(text: text, style: Self.style(composer), lines: 1...6, handle: input,
                             onInput: { text = $0 }, onComposing: { composing = $0 })
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 9)
                    .background(XbinColor.fill, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                    .disabled(composer.disabled)
                if composer.busy, let onStop {
                    Button(action: onStop) {
                        Image(systemName: XbinIcons.UI.stop).font(.title)
                    }
                    .accessibilityLabel("Stop")
                } else {
                    Button {
                        // Marked text is committed (and reported) first.
                        input.commitComposition()
                        let draft = text
                        if composer.canSend(draft) { onSend(draft) }
                    } label: {
                        Image(systemName: XbinIcons.UI.send).font(.title)
                    }
                    .disabled(!(composer.canSend(text) || (composing && !composer.disabled)))
                    .accessibilityLabel("Send")
                }
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
                onRemoveAttachment: (@MainActor (String) -> Void)? = nil) {
        self.init(composer: composer, text: text, onSend: onSend, onStop: onStop, onAttach: onAttach,
                  onRemoveAttachment: onRemoveAttachment) { EmptyView() }
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
        .background(XbinColor.fill, in: Capsule())
    }
}
#endif
