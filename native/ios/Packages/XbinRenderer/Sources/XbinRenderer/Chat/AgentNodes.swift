#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

// The workspace agent's nodes (D233; docs/native.md → The workspace agent's
// nodes). `card`, `connection` and a transcript's `empty` slot are drawn
// from the tile's tree, like every other primitive. `request` and `session`
// are NOT: the tile names an id and the app draws the node from xbind's own
// reading (``XbinServices/trustedNode``) — the renderer shows nothing of
// its own there but an inert placeholder.

// MARK: - card

/// A card of the tile's own in a transcript: a header (icon, title,
/// subtitle, a badge), its body, a quiet note, and its buttons in a row at
/// the bottom — stacked, one under the other, at the accessibility text
/// sizes and when there are more than two. A toned card (ok, warn, danger,
/// accent) carries a rule in that tone down its leading edge; the tone is
/// never the only signal — the icon and the badge's word say it too.
public struct CardView<Content: View, Buttons: View>: View {
    public let card: CardModel
    /// How many buttons `buttons` draws (0: no button row).
    public var buttonCount: Int
    let content: Content
    let buttons: Buttons
    @Environment(\.dynamicTypeSize) private var typeSize

    public init(card: CardModel, buttonCount: Int = 0, @ViewBuilder content: () -> Content,
                @ViewBuilder buttons: () -> Buttons) {
        self.card = card
        self.buttonCount = buttonCount
        self.content = content()
        self.buttons = buttons()
    }

    public var body: some View {
        let accent = card.accent
        VStack(alignment: .leading, spacing: 10) {
            if card.hasHeader { header }
            content
            if let note = card.note {
                Text(verbatim: note)
                    .font(.footnote)
                    .foregroundStyle(XbinColor.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if buttonCount > 0 {
                if typeSize.isAccessibilitySize || buttonCount > 2 {
                    VStack(spacing: 8) { buttons }
                } else {
                    HStack(alignment: .center, spacing: 8) { buttons }
                }
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ChatCard())
        .overlay(alignment: .leading) {
            if let accent, accent != .muted {
                Rectangle().fill(XbinColor.tone(accent)).frame(width: 3).accessibilityHidden(true)
            }
        }
        .clipShape(.xbinPlate)
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if let symbol = XbinIcons.symbol(card.icon) {
                Image(systemName: symbol)
                    .foregroundStyle(card.accent.map(XbinColor.tone) ?? XbinColor.muted)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: 2) {
                if let title = card.title {
                    Text(verbatim: title).font(.headline).foregroundStyle(XbinColor.text)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if let subtitle = card.subtitle {
                    Text(verbatim: subtitle).font(.footnote).foregroundStyle(XbinColor.muted)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: 4)
            if let badge = card.badge { Pill(text: badge, tone: card.accent) }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: card.accessibilityHeader))
        .accessibilityAddTraits(.isHeader)
    }
}

extension CardView where Buttons == EmptyView {
    public init(card: CardModel, @ViewBuilder content: () -> Content) {
        self.init(card: card, buttonCount: 0, content: content) { EmptyView() }
    }
}

/// `card`: its `button` children are the button row (wherever they sit
/// among the children); the rest is the body, drawn as in a free layout.
struct CardNodeView: View {
    let node: XbinNode

    var body: some View {
        let parts = CardModel.split(node.children) { $0.type }
        CardView(card: CardModel(props: node.props), buttonCount: parts.buttons.count) {
            ForEach(parts.body) { NodeView(node: $0) }
                .environment(\.xbinPlacement, .free)
        } buttons: {
            ForEach(parts.buttons) { NodeView(node: $0) }
                .environment(\.xbinPlacement, .free)
        }
    }
}

// MARK: - connection

/// The line that says what a conversation is connected to, above its
/// transcript: what is connected (a plug), what is not (a lock; absent when
/// nothing is left out) and the model. One element for VoiceOver — the
/// line as a sentence, a button — with the model as a named action. The
/// segments stack at the accessibility text sizes.
public struct ConnectionView: View {
    public let connection: ConnectionModel
    public var onTap: (@MainActor () -> Void)?
    /// The model segment is a target of its own ("big ▾"); nil: it is part
    /// of the line.
    public var onModel: (@MainActor () -> Void)?
    @Environment(\.dynamicTypeSize) private var typeSize

    public init(connection: ConnectionModel, onTap: (@MainActor () -> Void)? = nil, onModel: (@MainActor () -> Void)? = nil) {
        self.connection = connection
        self.onTap = onTap
        self.onModel = onModel
    }

    public var body: some View {
        let tap = connection.disabled ? nil : onTap
        let model = connection.disabled ? nil : onModel
        let line = strip(tap: tap, model: model)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text(verbatim: connection.accessibilityLabel))
            .accessibilityAddTraits(tap == nil ? [] : .isButton)
            .accessibilityAction { tap?() }
        if let model {
            line.accessibilityAction(named: Text("Change model")) { model() }
        } else {
            line
        }
    }

    /// The strip as drawn: the scope (a target when the tile listens), then
    /// the model as a target of its own when the tile listens to `model`.
    private func strip(tap: (@MainActor () -> Void)?, model: (@MainActor () -> Void)?) -> some View {
        let stacked = typeSize.isAccessibilitySize
        return HStack(alignment: .center, spacing: 0) {
            if let tap {
                Button(action: tap) { scope(stacked: stacked, withModel: model == nil).contentShape(Rectangle()) }
                    .buttonStyle(.plain)
            } else {
                scope(stacked: stacked, withModel: model == nil)
            }
            if let model, let text = connection.modelText {
                Button(action: model) { modelSegment(text, chevrons: true).contentShape(Rectangle()) }
                    .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 16)
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .background(XbinColor.surface)
        .overlay(alignment: .bottom) { Rectangle().fill(XbinColor.border).frame(height: 1).accessibilityHidden(true) }
        .opacity(connection.disabled ? 0.55 : 1)
    }

    @ViewBuilder
    private func scope(stacked: Bool, withModel: Bool) -> some View {
        let segments = Group {
            if let c = connection.connected { segment("powerplug", c, strong: true, tone: nil) }
            if let n = connection.not { segment("lock", n, strong: false, tone: .warn) }
            if withModel, let m = connection.modelText { modelSegment(m, chevrons: false) }
        }
        if stacked {
            VStack(alignment: .leading, spacing: 4) { segments }
                .padding(.vertical, 8)
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        } else {
            HStack(alignment: .center, spacing: 12) {
                segments
                Spacer(minLength: 0)
            }
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        }
    }

    private func segment(_ symbol: String, _ text: String, strong: Bool, tone: XbinTone?) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 5) {
            Image(systemName: symbol)
                .font(.caption)
                .foregroundStyle(tone.map(XbinColor.tone) ?? XbinColor.muted)
            Text(verbatim: text)
                .font(.footnote.weight(strong ? .semibold : .regular))
                .foregroundStyle(strong ? XbinColor.text : XbinColor.muted)
                .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
        }
    }

    private func modelSegment(_ text: String, chevrons: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 4) {
            Text(verbatim: text)
                .font(XbinFont.mono(.footnote))
                .foregroundStyle(XbinColor.text)
                .lineLimit(1)
            if chevrons {
                Image(systemName: XbinIcons.UI.chevronDown).font(.caption2.weight(.semibold)).foregroundStyle(XbinColor.muted)
            }
        }
        .frame(minHeight: 44)
        .padding(.leading, chevrons ? 12 : 0)
    }
}

/// `connection`: `tap` (the tile's scope panel) and `model` when the tile
/// listens to them.
struct ConnectionNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        ConnectionView(connection: ConnectionModel(props: node.props), onTap: cx?.action(node, "tap"),
                       onModel: cx?.action(node, "model"))
    }
}

// MARK: - request, session (drawn by the app)

/// `request` and `session`: the app's view for the id the tile named
/// (``XbinServices/trustedNode``) — or, with no provider, an inert
/// placeholder. Nothing else of the node is read: no text, no state, no
/// button comes from the tree.
struct TrustedNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        if let trusted = XbinTrustedNode(type: node.type, props: node.props) {
            if let draw = cx?.services.trustedNode {
                draw(trusted).id(trusted)
            } else {
                TrustedPlaceholder(title: trusted.placeholderTitle, session: node.type == "session")
            }
        } else {
            TrustedPlaceholder(title: XbinTrustedNode.placeholderTitle(type: node.type), session: node.type == "session")
        }
    }
}

/// What stands where the app draws a `request` or a `session`, when no app
/// does (a preview, a snapshot, a tile that isn't the person's agent): a
/// neutral line that can't be tapped.
struct TrustedPlaceholder: View {
    let title: String
    let session: Bool

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: session ? "apple.terminal" : XbinIcons.UI.approval)
                .foregroundStyle(XbinColor.muted)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: title).font(.subheadline.weight(.medium)).foregroundStyle(XbinColor.text)
                Text(verbatim: XbinTrustedNode.placeholderCaption).font(.caption).foregroundStyle(XbinColor.muted)
            }
            Spacer(minLength: 0)
        }
        .padding(12)
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
        .accessibilityElement(children: .combine)
    }
}

// MARK: - a transcript's empty slot

/// What a transcript with no children shows (rev 3): a heading, a
/// sentence, short titled lists, and suggestions — full-width rows, the
/// kind in small caps over the line. A tap reports the suggestion; it
/// never sends (the tile fills its composer with it).
public struct TranscriptEmptyView: View {
    public let empty: TranscriptEmpty
    public var onSuggestion: (@MainActor (String) -> Void)?

    public init(empty: TranscriptEmpty, onSuggestion: (@MainActor (String) -> Void)? = nil) {
        self.empty = empty
        self.onSuggestion = onSuggestion
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            if empty.title != nil || empty.text != nil {
                VStack(alignment: .leading, spacing: 6) {
                    if let title = empty.title {
                        Text(verbatim: title).font(.title3.weight(.bold)).foregroundStyle(XbinColor.text)
                            .fixedSize(horizontal: false, vertical: true)
                            .accessibilityAddTraits(.isHeader)
                    }
                    if let text = empty.text {
                        Text(verbatim: text).font(.subheadline).foregroundStyle(XbinColor.muted)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            ForEach(empty.lists) { list in
                VStack(alignment: .leading, spacing: 4) {
                    if let title = list.title { caps(title).accessibilityAddTraits(.isHeader) }
                    ForEach(Array(list.items.enumerated()), id: \.offset) { _, item in
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            Text(verbatim: "–").foregroundStyle(XbinColor.muted).accessibilityHidden(true)
                            Text(verbatim: item).foregroundStyle(XbinColor.text).fixedSize(horizontal: false, vertical: true)
                        }
                        .font(.subheadline)
                    }
                }
            }
            if !empty.suggestions.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    if let title = empty.suggestionsTitle { caps(title).accessibilityAddTraits(.isHeader) }
                    ForEach(empty.suggestions) { s in suggestion(s) }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 8)
    }

    /// Small caps for a list's or the suggestions' title (read by VoiceOver
    /// as written).
    private func caps(_ text: String) -> some View {
        Text(verbatim: text.uppercased())
            .font(.caption.weight(.semibold))
            .foregroundStyle(XbinColor.muted)
            .accessibilityLabel(Text(verbatim: text))
    }

    @ViewBuilder
    private func suggestion(_ s: TranscriptEmpty.Suggestion) -> some View {
        let row = VStack(alignment: .leading, spacing: 2) {
            if let kind = s.kind {
                Text(verbatim: kind.uppercased()).font(.caption2.weight(.bold)).foregroundStyle(XbinColor.accentText)
            }
            Text(verbatim: s.text).font(.subheadline).foregroundStyle(XbinColor.text)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .modifier(ChatCard())
        .contentShape(Rectangle())
        if let onSuggestion {
            Button { onSuggestion(s.id) } label: { row }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(verbatim: s.accessibilityLabel))
        } else {
            row.accessibilityElement(children: .ignore).accessibilityLabel(Text(verbatim: s.accessibilityLabel))
        }
    }
}
#endif
