import SwiftUI
import XbinTerm

/// Scrollback search (plans/native.md §12), Safari-style above the keyboard:
/// typing searches from the newest output up; Return, ⌘G and ↑ go to older
/// matches, ⇧Return, ⌘⇧G and ↓ to newer ones; Escape or Done closes. The
/// counter is XbinTerm's `TermFind.status`.
struct TerminalFindBar: View {
    @Bindable var controller: TerminalController
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
            TextField("Find in scrollback", text: $controller.find.query)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .focused($focused)
                .onSubmit { step(.older) }
                .onKeyPress(phases: .down) { press in keyPressed(press) }
            if !controller.find.status.isEmpty {
                Text(verbatim: controller.find.status)
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(controller.find.problem == nil ? Color.secondary : Color.red)
                    .lineLimit(1)
                    .fixedSize()
            }
            Button { step(.older) } label: { Image(systemName: "chevron.up") }
                .disabled(!controller.find.canSearch)
                .accessibilityLabel("Older match")
            Button { step(.newer) } label: { Image(systemName: "chevron.down") }
                .disabled(!controller.find.canSearch)
                .accessibilityLabel("Newer match")
            Menu {
                Toggle("Match Case", isOn: $controller.find.options.caseSensitive)
                Toggle("Whole Words", isOn: $controller.find.options.wholeWord)
                Toggle("Regular Expression", isOn: $controller.find.options.regex)
            } label: {
                Image(systemName: "line.3.horizontal.decrease.circle")
            }
            .accessibilityLabel("Search options")
            Button("Done") { step(.close) }
                .font(.body.bold())
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(.bar)
        .onAppear { focused = true }
        .onChange(of: controller.findFocusRequests) { _, _ in focused = true }
        .onChange(of: controller.find.query) { _, _ in controller.findChanged() }
        .onChange(of: controller.find.options) { _, _ in controller.findChanged() }
    }

    private func step(_ a: TermFindAction) {
        controller.findStep(a)
        if a != .close { focused = true }
    }

    /// Hardware keys in the field (TermFind.action): Return, ⇧Return, Escape, ⌘G, ⌘⇧G.
    private func keyPressed(_ press: KeyPress) -> KeyPress.Result {
        let key: TermFindKey
        if press.key == .return {
            key = .returnKey
        } else if press.key == .escape {
            key = .escape
        } else if press.characters.lowercased() == "g" {
            key = .g
        } else {
            return .ignored
        }
        let shift = press.modifiers.contains(.shift)
        let command = press.modifiers.contains(.command)
        guard let a = TermFind.action(key: key, shift: shift, command: command) else { return .ignored }
        step(a)
        return .handled
    }
}

/// The precise selection mode's bar: select by character, word or line; move
/// the selection's moving end one unit at a time (and swap which end moves);
/// select everything; copy. Drags on the terminal are SelectionOverlayView's.
struct TerminalSelectionBar: View {
    @Bindable var controller: TerminalController

    var body: some View {
        VStack(spacing: 8) {
            HStack(spacing: 10) {
                Picker("Select by", selection: $controller.selection.unit) {
                    ForEach(TermSelectionUnit.allCases, id: \.self) { u in
                        Text(u.label).tag(u)
                    }
                }
                .pickerStyle(.segmented)
                Button("Done") { controller.exitSelectionMode() }
                    .font(.body.bold())
                    .keyboardShortcut(.cancelAction)
            }
            HStack(spacing: 6) {
                nudge(.left, "chevron.left", "Move left")
                nudge(.up, "chevron.up", "Move up")
                nudge(.down, "chevron.down", "Move down")
                nudge(.right, "chevron.right", "Move right")
                Button { controller.swapSelectionEnds() } label: { Image(systemName: "arrow.left.arrow.right") }
                    .buttonStyle(.bordered)
                    .disabled(controller.selection.isEmpty)
                    .accessibilityLabel("Move the other end")
                Spacer(minLength: 4)
                Button("All") { controller.selectAllText() }
                    .buttonStyle(.bordered)
                Button("Copy") { controller.copySelection() }
                    .buttonStyle(.borderedProminent)
                    .disabled(controller.selection.isEmpty)
                    .keyboardShortcut("c", modifiers: .command)
            }
            if controller.selection.isEmpty {
                Text("Drag across the text to select it. Two fingers scroll.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(.bar)
        .onChange(of: controller.selection.unit) { _, _ in controller.showSelection() }
    }

    private func nudge(_ n: TermNudge, _ symbol: String, _ label: String) -> some View {
        Button { controller.nudgeSelection(n) } label: { Image(systemName: symbol) }
            .buttonStyle(.bordered)
            .disabled(controller.selection.isEmpty)
            .accessibilityLabel(label)
    }
}
