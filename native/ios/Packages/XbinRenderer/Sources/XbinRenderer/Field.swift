#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

/// `field`: a text field (text, secure, number, email, url, multiline,
/// search) or a date/time picker. The value is controlled and app-owned
/// while focused (§7.3, tree.md §6): every committed change is shown at
/// once and reported as `input {value}` — text still being composed with
/// an input method is not (``XbinTextField``, ``TextInputGate``) — and the
/// tile's value replaces it only when the tile sets it (a reset, a
/// correction), at the end of a composition in progress. Focus loss (or
/// return) reports `change` when the text changed; return reports
/// `submit`.
struct FieldNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement
    @Environment(\.scenePhase) private var scenePhase
    @State private var focused = false
    @State private var atFocus: String?

    var body: some View {
        let p = node.props
        let kind = p.string("kind") ?? "text"
        let label = p.string("label") ?? ""
        VStack(alignment: .leading, spacing: 6) {
            if kind == "date" || kind == "time" {
                DateField(kind: kind, label: label, text: text) { set($0, change: true) }
            } else {
                if !label.isEmpty {
                    Text(verbatim: label).font(.footnote).foregroundStyle(XbinColor.muted)
                }
                input(kind: kind, label: label, p: p)
                    .padding(placement == .list ? 0 : 10)
                    .background(placement == .list ? Color.clear : XbinColor.fill,
                                in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
            if let error = p.nonEmpty("error") {
                Text(verbatim: error).font(.footnote).foregroundStyle(XbinColor.tone(.danger))
            } else if let hint = p.nonEmpty("hint") {
                Text(verbatim: hint).font(.footnote).foregroundStyle(XbinColor.muted)
            }
        }
        .disabled(p.bool("disabled"))
        .onChange(of: scenePhase) { _, phase in
            // Secure values never outlive the app going to the background.
            if phase == .background && kind == "secure" && !text.isEmpty { set("") }
        }
    }

    /// What the field shows.
    private var text: String { Props.text(node.value("value")) ?? "" }

    private func set(_ value: String, change: Bool = false) {
        cx?.emit(node, "input", ["value": .string(value)])
        if change { cx?.emit(node, "change", ["value": .string(value)]) }
    }

    private func focus(_ now: Bool) {
        focused = now
        if now { atFocus = text } else { commit() }
    }

    /// Reports `change` when the text differs from when focus began.
    private func commit() {
        if let before = atFocus, before != text { cx?.emit(node, "change", ["value": .string(text)]) }
        atFocus = focused ? text : nil
    }

    private func submit() {
        commit()
        cx?.emit(node, "submit", ["value": .string(text)])
    }

    @ViewBuilder
    private func input(kind: String, label: String, p: Props) -> some View {
        // No placeholder → none: iOS would show the label again in the
        // field, under the label already drawn above it.
        let style = Self.style(kind: kind, label: label, p: p)
        let onInput: @MainActor (String) -> Void = { set($0) }
        let onFocus: @MainActor (Bool) -> Void = { focus($0) }
        let onReturn: @MainActor () -> Void = { submit() }
        switch kind {
        case "secure":
            XbinTextField(text: text, style: style, onInput: onInput, onFocus: onFocus, onReturn: onReturn)
                .privacySensitive()
        case "multiline":
            XbinTextArea(text: text, style: style, lines: 3...8, onInput: onInput, onFocus: onFocus)
        case "search":
            HStack(spacing: 6) {
                Image(systemName: "magnifyingglass").foregroundStyle(XbinColor.muted)
                XbinTextField(text: text, style: style, onInput: onInput, onFocus: onFocus, onReturn: onReturn)
            }
        default:
            XbinTextField(text: text, style: style, onInput: onInput, onFocus: onFocus, onReturn: onReturn)
        }
    }

    /// The UIKit traits of a field kind.
    static func style(kind: String, label: String, p: Props) -> TextInputStyle {
        var s = TextInputStyle()
        s.placeholder = p.nonEmpty("placeholder") ?? ""
        s.accessibilityLabel = label.isEmpty ? s.placeholder : label
        s.keyboard = keyboard(kind)
        s.contentType = contentType(kind)
        let prose = kind == "text" || kind == "multiline"
        s.autocapitalization = prose ? .sentences : .none
        s.autocorrection = prose ? .default : .no
        s.returnKey = returnKey(p.string("submit")) ?? (kind == "search" ? .search : .default)
        s.secure = kind == "secure"
        return s
    }

    static func keyboard(_ kind: String) -> UIKeyboardType {
        switch kind {
        case "number": return .decimalPad
        case "email": return .emailAddress
        case "url": return .URL
        case "search": return .webSearch
        default: return .default
        }
    }

    static func contentType(_ kind: String) -> UITextContentType? {
        switch kind {
        case "email": return .emailAddress
        case "url": return .URL
        case "secure": return .password
        default: return nil
        }
    }

    /// The return key: `go`, `send`, `done`, `search`, `next` (else the
    /// kind's default).
    static func returnKey(_ s: String?) -> UIReturnKeyType? {
        switch s ?? "" {
        case "go": return .go
        case "send": return .send
        case "done": return .done
        case "search": return .search
        case "next": return .next
        default: return nil
        }
    }
}

/// `field kind=date|time`: a compact native picker over the web's value
/// format (`YYYY-MM-DD`, `HH:MM`), in UTC so the value never shifts.
private struct DateField: View {
    let kind: String
    let label: String
    let text: String
    let onChange: (String) -> Void

    var body: some View {
        let isDate = kind == "date"
        let parsed = isDate ? FieldValue.date(text) : FieldValue.time(text)
        let selection = mainBinding(
            get: { parsed ?? Date(timeIntervalSince1970: 0) },
            set: { onChange(isDate ? FieldValue.dateString($0) : FieldValue.timeString($0)) }
        )
        DatePicker(selection: selection, displayedComponents: isDate ? .date : .hourAndMinute) {
            Text(verbatim: label)
        }
        .environment(\.timeZone, FieldValue.pickerTimeZone)
        .environment(\.calendar, {
            var c = Calendar(identifier: .gregorian)
            c.timeZone = FieldValue.pickerTimeZone
            return c
        }())
    }
}
#endif
