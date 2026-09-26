import SwiftUI
import UIKit
import XbinTerm

/// The terminal's keyboard settings (plans/native.md §12): the accessory row's
/// customizable slot — a key, a character or a snippet of your own — and the
/// hardware keyboard's ⌥ behaviour. Saved at once (TerminalPrefs); open
/// terminals redraw their row. Pushed from the terminal's sessions sheet, and
/// usable from the app's settings as `NavigationLink { TerminalKeyboardSettingsView() }`.
struct TerminalKeyboardSettingsView: View {
    @State private var slot: AccessoryKey? = TerminalPrefs.accessorySlot
    @State private var snippet = ""
    @State private var snippetReturn = false
    @State private var snippetProblem: String?
    @State private var optionAsMeta = TerminalPrefs.keyboard.optionAsMeta
    @State private var optionArrowsByWord = TerminalPrefs.keyboard.optionArrowsMoveByWord

    private let columns = [GridItem(.adaptive(minimum: 52), spacing: 8)]

    var body: some View {
        Form {
            Section {
                AccessoryRowPreview(slot: slot)
                    .listRowInsets(EdgeInsets(top: 10, leading: 8, bottom: 10, trailing: 8))
            } header: {
                Text("Accessory row")
            } footer: {
                Text(verbatim: slot.map { "The last key: \(AccessorySlot.describe($0))." } ?? "No extra key.")
            }

            Section("Extra key") {
                Button {
                    choose(nil)
                } label: {
                    HStack {
                        Text("None")
                        Spacer()
                        if slot == nil { Image(systemName: "checkmark").foregroundStyle(.tint) }
                    }
                }
                .foregroundStyle(.primary)
            }

            ForEach(AccessorySlot.groups) { group in
                Section(group.title) {
                    LazyVGrid(columns: columns, spacing: 8) {
                        ForEach(group.keys, id: \.self) { key in
                            KeyCapButton(key: key, selected: key == slot) { choose(key) }
                        }
                    }
                    .padding(.vertical, 4)
                }
            }

            Section {
                TextField("Text to type, e.g. sudo ", text: $snippet)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.body.monospaced())
                Toggle("Press Return after it", isOn: $snippetReturn)
                Button("Use as the extra key") { useSnippet() }
                    .disabled(snippet.isEmpty)
                if let snippetProblem {
                    Text(verbatim: snippetProblem).font(.footnote).foregroundStyle(.red)
                }
            } header: {
                Text("Snippet")
            } footer: {
                Text("Up to \(AccessorySlot.snippetLimit) characters, typed as they are (spaces too).")
            }

            Section {
                Toggle("Use ⌥ as Meta", isOn: $optionAsMeta)
                    .onChange(of: optionAsMeta) { _, v in saveKeyboard { $0.optionAsMeta = v } }
                Toggle("⌥← and ⌥→ move by word", isOn: $optionArrowsByWord)
                    .onChange(of: optionArrowsByWord) { _, v in saveKeyboard { $0.optionArrowsMoveByWord = v } }
            } header: {
                Text("Hardware keyboard")
            } footer: {
                Text("With ⌥ as Meta, ⌥-letter sends Escape and the letter (for Emacs and shells) instead of the layout's ⌥ characters.")
            }
        }
        .navigationTitle("Terminal Keyboard")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            if let key = slot, let parts = AccessorySlot.snippetParts(key) {
                snippet = parts.text
                snippetReturn = parts.pressReturn
            }
        }
    }

    private func choose(_ key: AccessoryKey?) {
        slot = key
        snippetProblem = nil
        TerminalPrefs.accessorySlot = key
    }

    private func useSnippet() {
        switch AccessorySlot.snippet(snippet, pressReturn: snippetReturn) {
        case .success(let key): choose(key)
        case .failure(let p): snippetProblem = p.message
        }
    }

    private func saveKeyboard(_ change: (inout TermKeyboardSettings) -> Void) {
        var s = TerminalPrefs.keyboard
        change(&s)
        TerminalPrefs.keyboard = s
    }
}

/// One key cap in the chooser.
private struct KeyCapButton: View {
    let key: AccessoryKey
    let selected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(verbatim: AccessorySlot.capLabel(key))
                .font(.system(size: 15, weight: .medium, design: .monospaced))
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .frame(maxWidth: .infinity, minHeight: 36)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(selected ? Color.white : Color.primary)
        .background(selected ? Color.accentColor : Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8))
        .accessibilityLabel(AccessorySlot.describe(key))
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// The accessory row as the keyboard will show it, the slot highlighted.
private struct AccessoryRowPreview: View {
    let slot: AccessoryKey?

    var body: some View {
        HStack(spacing: 4) {
            ForEach(Array(AccessoryKey.defaultRow.enumerated()), id: \.offset) { _, key in
                cap(key, highlighted: false)
            }
            if let slot { cap(slot, highlighted: true) }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Accessory row preview")
    }

    private func cap(_ key: AccessoryKey, highlighted: Bool) -> some View {
        Text(verbatim: AccessorySlot.capLabel(key))
            .font(.system(size: 12, weight: .medium, design: .monospaced))
            .lineLimit(1)
            .minimumScaleFactor(0.5)
            .frame(maxWidth: .infinity, minHeight: 30)
            .background(highlighted ? Color.accentColor.opacity(0.3) : Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 6))
            .layoutPriority(highlighted && AccessorySlot.capLabel(key).count > 3 ? 1 : 0)
    }
}
