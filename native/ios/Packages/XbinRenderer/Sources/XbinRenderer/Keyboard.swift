#if canImport(UIKit)
import SwiftUI
import UIKit

// The software keyboard can always be put away (#30): wherever something
// brings it up, something puts it down again. An iPhone keyboard has no key
// for it, so every text control gets a slim bar over the keyboard with one
// button, Hide keyboard (`keyboard-hide` to the UI tests), and a screen's
// lists, forms and transcripts let a drag down push the keyboard away with
// the finger (`scrollDismissesKeyboard(.interactively)`, as Messages does).
// The terminal keeps its own key row, which has its own way.

public enum XbinKeyboard {
    /// Ends editing wherever it is, as a tap on Hide keyboard does.
    @MainActor public static func hide() {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
    }

    /// The bar over the keyboard for a UIKit text control: a flexible space,
    /// then Hide keyboard, which ends editing of `owner`.
    @MainActor static func accessory(for owner: UIResponder) -> UIView {
        let bar = UIToolbar(frame: CGRect(x: 0, y: 0, width: 320, height: 44))
        let hide = UIBarButtonItem(image: UIImage(systemName: "keyboard.chevron.compact.down"),
                                   primaryAction: UIAction { [weak owner] _ in _ = owner?.resignFirstResponder() })
        hide.accessibilityLabel = "Hide keyboard"
        hide.accessibilityIdentifier = "keyboard-hide"
        bar.items = [UIBarButtonItem(systemItem: .flexibleSpace), hide]
        bar.sizeToFit()
        return bar
    }
}

extension View {
    /// For a screen or sheet of SwiftUI text fields (the app's own forms):
    /// Hide keyboard over the keyboard, and a drag down its list or form
    /// puts the keyboard away.
    public func keyboardHideButton() -> some View {
        modifier(KeyboardHideModifier())
    }
}

private struct KeyboardHideModifier: ViewModifier {
    func body(content: Content) -> some View {
        content
            .scrollDismissesKeyboard(.interactively)
            .toolbar {
                ToolbarItemGroup(placement: .keyboard) {
                    Spacer()
                    Button { XbinKeyboard.hide() } label: { Image(systemName: "keyboard.chevron.compact.down") }
                        .accessibilityLabel("Hide keyboard")
                        .accessibilityIdentifier("keyboard-hide")
                }
            }
    }
}
#endif
