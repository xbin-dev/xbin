#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

/// `field`: a text field (text, secure, number, email, url, multiline,
/// search) or a date/time picker. The value is controlled and app-owned
/// while focused (§7.3, tree.md §6): every keystroke is shown at once and
/// reported as `input {value}`; the tile's value replaces it only when the
/// tile sets it (a reset, a correction). Focus loss (or return) reports
/// `change` when the text changed; return reports `submit`.
struct FieldNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement
    @Environment(\.scenePhase) private var scenePhase
    @FocusState private var focused: Bool
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
                    .focused($focused)
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
        .onChange(of: focused) { _, now in
            if now { atFocus = text } else { commit() }
        }
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
        let binding = mainBinding(get: { text }, set: { set($0) })
        let prompt = p.nonEmpty("placeholder").map { Text(verbatim: $0) }
        let submitLabel = Self.submitLabel(p.string("submit"))
        switch kind {
        case "secure":
            SecureField(text: binding, prompt: prompt) { Text(verbatim: label) }
                .textContentType(.password)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .privacySensitive()
                .submitLabel(submitLabel)
                .onSubmit { submit() }
        case "multiline":
            TextField(text: binding, prompt: prompt, axis: .vertical) { Text(verbatim: label) }
                .lineLimit(3...8)
        case "search":
            HStack(spacing: 6) {
                Image(systemName: "magnifyingglass").foregroundStyle(XbinColor.muted)
                TextField(text: binding, prompt: prompt) { Text(verbatim: label) }
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .keyboardType(.webSearch)
                    .submitLabel(p.string("submit") == nil ? .search : submitLabel)
                    .onSubmit { submit() }
            }
        default:
            TextField(text: binding, prompt: prompt) { Text(verbatim: label) }
                .keyboardType(Self.keyboard(kind))
                .textContentType(Self.contentType(kind))
                .textInputAutocapitalization(kind == "text" ? .sentences : .never)
                .autocorrectionDisabled(kind != "text")
                .submitLabel(submitLabel)
                .onSubmit { submit() }
        }
    }

    static func keyboard(_ kind: String) -> UIKeyboardType {
        switch kind {
        case "number": return .decimalPad
        case "email": return .emailAddress
        case "url": return .URL
        default: return .default
        }
    }

    static func contentType(_ kind: String) -> UITextContentType? {
        switch kind {
        case "email": return .emailAddress
        case "url": return .URL
        default: return nil
        }
    }

    /// The return key: `go`, `send`, `done`, `search`, `next` (else return).
    static func submitLabel(_ s: String?) -> SubmitLabel {
        switch s ?? "" {
        case "go": return .go
        case "send": return .send
        case "done": return .done
        case "search": return .search
        case "next": return .next
        default: return .return
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
