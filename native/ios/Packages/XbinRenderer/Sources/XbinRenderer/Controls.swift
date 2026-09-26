#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

// Controls (plans/native.md §8.3): button, toggle, picker, menu. The field
// is Field.swift.

/// A button's tap: its confirmation dialog first (`confirm`), then the
/// native copy (`copy`, no round trip), then the tile's `tap`.
@MainActor
enum ButtonPress {
    static func press(_ node: XbinNode, cx: XbinRenderContext?, confirm: ConfirmHost?, copied: @escaping @MainActor () -> Void) {
        let p = node.props
        guard !p.bool("disabled"), !p.bool("busy") else { return }
        let go: @MainActor () -> Void = {
            if let text = p.string("copy") {
                cx?.services.copy(text)
                copied()
            }
            if node.listens(to: "tap") { cx?.emit(node, "tap") }
        }
        guard let c = p.object("confirm") else { return go() }
        // Never act without the confirmation the tile asked for.
        confirm?.ask(ConfirmHost.Request(
            title: Props.text(c["title"]) ?? "",
            message: Props.text(c["message"]),
            label: Props.text(c["label"]) ?? p.nonEmpty("label") ?? "OK",
            destructive: c["destructive"]?.boolValue ?? false,
            action: go
        ))
    }
}

/// `button`: looks like its place — prominent full width in a free layout,
/// a tinted row in a list, an icon in the toolbar, a chip in a composer.
struct ButtonNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement
    @Environment(\.xbinConfirm) private var confirm
    @State private var copied = false

    var body: some View {
        let p = node.props
        let role = p.string("role")
        let busy = p.bool("busy")
        let text = copied ? "Copied" : (p.string("label") ?? "")
        let symbol = copied ? XbinIcons.UI.checkmark : XbinIcons.symbol(p.string("icon"))
        let fill = placement == .free || placement == .dock
        let iconOnly = (placement == .toolbar || placement == .inlineActions) && symbol != nil
        let button = Button(role: role == "destructive" ? .destructive : nil) {
            ButtonPress.press(node, cx: cx, confirm: confirm) { flashCopied() }
        } label: {
            ButtonLabel(text: text, symbol: symbol, busy: busy, iconOnly: iconOnly, fill: fill)
                .fontWeight(role == "primary" && placement != .toolbar ? .semibold : nil)
        }
        .disabled(p.bool("disabled") || busy)
        .accessibilityLabel(Text(verbatim: text))
        switch placement {
        case .free, .dock:
            switch role ?? "" {
            case "primary": button.buttonStyle(.borderedProminent).controlSize(.large)
            case "plain": button.buttonStyle(.borderless)
            default: button.buttonStyle(.bordered).controlSize(.large)
            }
        case .list:
            button
        case .chips:
            button.buttonStyle(.bordered).controlSize(.small).buttonBorderShape(.capsule)
        case .inlineActions:
            button.buttonStyle(.borderless).controlSize(.small).foregroundStyle(XbinColor.muted)
        default:
            button.buttonStyle(.borderless)
        }
    }

    private func flashCopied() {
        copied = true
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(1.2))
            copied = false
        }
    }
}

/// A button's icon (or spinner while `busy`) and label.
struct ButtonLabel: View {
    let text: String
    let symbol: String?
    let busy: Bool
    var iconOnly = false
    var fill = false

    var body: some View {
        HStack(spacing: 6) {
            if busy {
                ProgressView().controlSize(.small)
            } else if let symbol {
                Image(systemName: symbol)
            }
            if !(iconOnly && (symbol != nil || busy)) && !text.isEmpty {
                Text(verbatim: text).lineLimit(1)
            }
        }
        .frame(maxWidth: fill ? .infinity : nil)
    }
}

/// `menu`: a pull-down of its buttons (dividers between groups).
struct MenuNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement
    @Environment(\.xbinConfirm) private var confirm

    var body: some View {
        let p = node.props
        let label = p.string("label") ?? ""
        let symbol = XbinIcons.symbol(p.string("icon"))
        let context = cx
        let host = confirm
        Menu {
            ForEach(node.children) { child in
                if child.type == "divider" {
                    Divider()
                } else if child.type == "button" {
                    ActionButton(node: child, cx: context, confirm: host)
                }
            }
        } label: {
            if placement == .toolbar, let symbol {
                Label(label.isEmpty ? "More" : label, systemImage: symbol).labelStyle(.iconOnly)
            } else {
                Label {
                    Text(verbatim: label)
                } icon: {
                    if let symbol { Image(systemName: symbol) }
                }
            }
        }
    }
}

/// `toggle`: `value` is controlled (`change {value}`).
struct ToggleNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let isOn = mainBinding(
            get: { node.value("value")?.boolValue ?? false },
            set: { cx?.emit(node, "change", ["value": .bool($0)]) }
        )
        Toggle(isOn: isOn) {
            Text(verbatim: node.props.string("label") ?? "")
        }
        .disabled(node.props.bool("disabled"))
    }
}

/// `picker`: menu (default), segmented or inline; `value` is controlled
/// (`change {value}`).
struct PickerNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let p = node.props
        let options = PickerOption.list(p)
        let label = p.string("label") ?? ""
        let style = p.string("style") ?? "menu"
        let selection = mainBinding(
            get: { node.value("value") ?? .null },
            set: { v in
                if v != (node.value("value") ?? .null) { cx?.emit(node, "change", ["value": v]) }
            }
        )
        let picker = Picker(selection: selection) {
            ForEach(options) { o in
                PickerOptionLabel(option: o).tag(o.value)
            }
        } label: {
            Text(verbatim: label)
        }
        .disabled(p.bool("disabled"))
        switch style {
        case "segmented":
            if placement == .toolbar || label.isEmpty {
                picker.pickerStyle(.segmented).labelsHidden()
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    Text(verbatim: label).font(.footnote).foregroundStyle(XbinColor.muted)
                    picker.pickerStyle(.segmented).labelsHidden()
                }
            }
        case "inline" where placement == .list:
            picker.pickerStyle(.inline)
        case "inline":
            InlineChoices(options: options, selection: selection, label: label)
        default:
            if placement == .toolbar {
                picker.pickerStyle(.menu).labelsHidden()
            } else {
                picker.pickerStyle(.menu)
            }
        }
    }
}

private struct PickerOptionLabel: View {
    let option: PickerOption

    var body: some View {
        if let symbol = XbinIcons.symbol(option.icon) {
            Label(option.label, systemImage: symbol)
        } else {
            Text(verbatim: option.label)
        }
    }
}

/// An inline picker outside a `List`: its options as a card of rows with a
/// check on the selected one.
private struct InlineChoices: View {
    let options: [PickerOption]
    let selection: Binding<JSONValue>
    let label: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !label.isEmpty { Text(verbatim: label).font(.footnote).foregroundStyle(XbinColor.muted).padding(.horizontal, 16) }
            VStack(spacing: 0) {
                ForEach(Array(options.enumerated()), id: \.offset) { i, o in
                    if i > 0 { Divider().padding(.leading, 16) }
                    Button { selection.wrappedValue = o.value } label: {
                        HStack {
                            PickerOptionLabel(option: o).foregroundStyle(XbinColor.text)
                            Spacer()
                            if selection.wrappedValue == o.value {
                                Image(systemName: XbinIcons.UI.checkmark).foregroundStyle(XbinColor.accentText)
                            }
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 11)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
            .background(XbinColor.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }
}
#endif
