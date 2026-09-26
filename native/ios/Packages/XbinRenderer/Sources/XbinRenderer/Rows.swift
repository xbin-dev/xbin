#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// `row`: a list row — leading icon (or a tone dot), title and subtitle,
/// trailing detail, badge, check and chevron; content children below; an
/// `actions` child becomes swipe actions and a context menu. A row the tile
/// listens to taps on is a button.
struct RowView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement
    @Environment(\.xbinConfirm) private var confirm

    var body: some View {
        let p = node.props
        let actions = node.children.first { $0.type == "actions" }
        let content = node.children.filter { $0.type != "actions" }
        let disabled = p.bool("disabled")
        let tappable = node.listens(to: "tap") && !disabled
        VStack(alignment: .leading, spacing: 8) {
            if tappable {
                if placement == .list {
                    Button { cx?.emit(node, "tap") } label: { RowLabel(props: p).contentShape(Rectangle()) }
                        .foregroundStyle(XbinColor.text)
                } else {
                    Button { cx?.emit(node, "tap") } label: { RowLabel(props: p).contentShape(Rectangle()) }
                        .buttonStyle(.plain)
                }
            } else {
                RowLabel(props: p)
            }
            if !content.isEmpty {
                VStack(alignment: .leading, spacing: 8) { ForEach(content) { NodeView(node: $0) } }
                    .environment(\.xbinPlacement, .free)
            }
        }
        .opacity(disabled ? 0.5 : 1)
        .modifier(RowActionsModifier(buttons: actions?.children.filter { $0.type == "button" } ?? [],
                                     swipe: placement == .list, cx: cx, confirm: confirm))
    }
}

/// A row's text and accessories.
struct RowLabel: View {
    let props: Props

    var body: some View {
        let mono = props.string("mono")
        let tone = props.tone()
        let badge = props.nonEmpty("badge")
        HStack(alignment: .center, spacing: 12) {
            if let icon = props.nonEmpty("icon") {
                XbinIconImage(name: icon, tone: tone ?? .accent)
                    .font(.body)
                    .frame(width: 26)
            } else if let tone, badge == nil {
                Circle().fill(XbinColor.tone(tone)).frame(width: 8, height: 8)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: props.string("title") ?? "")
                    .font(.body)
                    .monospaced(mono == "title" || mono == "all")
                    .foregroundStyle(XbinColor.text)
                if let s = props.nonEmpty("subtitle") {
                    Text(verbatim: s)
                        .font(.subheadline)
                        .monospaced(mono == "subtitle" || mono == "all")
                        .foregroundStyle(XbinColor.muted)
                }
            }
            Spacer(minLength: 8)
            if let d = props.nonEmpty("detail") {
                Text(verbatim: d)
                    .font(.body)
                    .monospaced(mono == "detail" || mono == "all")
                    .foregroundStyle(XbinColor.muted)
                    .lineLimit(1)
            }
            if let badge { Pill(text: badge, tone: tone) }
            if props.bool("selected") {
                Image(systemName: XbinIcons.UI.checkmark)
                    .fontWeight(.semibold)
                    .foregroundStyle(XbinColor.accentText)
                    .accessibilityLabel("Selected")
            }
            if props.bool("nav") {
                Image(systemName: XbinIcons.UI.chevronForward)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.tertiary)
                    .accessibilityHidden(true)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// A row's `actions`: trailing swipe actions (in a `List`) and a context
/// menu (everywhere). Destructive buttons sit outermost.
private struct RowActionsModifier: ViewModifier {
    let buttons: [XbinNode]
    let swipe: Bool
    let cx: XbinRenderContext?
    let confirm: ConfirmHost?

    func body(content: Content) -> some View {
        if buttons.isEmpty {
            content
        } else if swipe {
            content
                .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                    ForEach(Array(buttons.reversed())) { b in
                        ActionButton(node: b, cx: cx, confirm: confirm, swipe: true)
                    }
                }
                .contextMenu {
                    ForEach(buttons) { ActionButton(node: $0, cx: cx, confirm: confirm, swipe: false) }
                }
        } else {
            content.contextMenu {
                ForEach(buttons) { ActionButton(node: $0, cx: cx, confirm: confirm, swipe: false) }
            }
        }
    }
}

/// A button drawn as a swipe action or a menu item. Context and confirm host
/// are passed in: menus and swipe actions are rendered outside the row's
/// environment.
struct ActionButton: View {
    let node: XbinNode
    let cx: XbinRenderContext?
    let confirm: ConfirmHost?
    var swipe = false

    var body: some View {
        let p = node.props
        let destructive = p.string("role") == "destructive"
        let label = p.string("label") ?? ""
        let button = Button(role: destructive ? .destructive : nil) {
            ButtonPress.press(node, cx: cx, confirm: confirm, copied: {})
        } label: {
            if let symbol = XbinIcons.symbol(p.string("icon")) {
                Label(label, systemImage: symbol)
            } else {
                Text(verbatim: label)
            }
        }
        .disabled(p.bool("disabled") || p.bool("busy"))
        if swipe && !destructive {
            button.tint(Color(uiColor: .systemGray))
        } else {
            button
        }
    }
}
#endif
