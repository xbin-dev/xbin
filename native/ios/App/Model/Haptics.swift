import UIKit

/// The app's haptics (plans/native.md §8): a light tap when something is
/// sent, a firmer one when the user approves, a success when a turn they
/// are watching settles, a warning when it starts waiting on them. Screens
/// call these at the moment it happens; Settings can turn them off (the
/// system's own switch applies too).
///
///   Haptics.send()        // a prompt, a message, a form submitted
///   Haptics.approve()     // a permission allowed, a plan approved
///   Haptics.settle()      // the watched agent turn finished
///   Haptics.needsYou()    // the watched session now waits on the user
///   Haptics.failed()      // an action or a turn failed
///   Haptics.select()      // a selection changed (workspace, picker)
@MainActor
enum Haptics {
    static func send() { impact(.light) }
    static func approve() { impact(.medium) }
    static func settle() { notify(.success) }
    static func needsYou() { notify(.warning) }
    static func failed() { notify(.error) }

    static func select() {
        guard AppSettings.haptics else { return }
        UISelectionFeedbackGenerator().selectionChanged()
    }

    private static func impact(_ style: UIImpactFeedbackGenerator.FeedbackStyle) {
        guard AppSettings.haptics else { return }
        UIImpactFeedbackGenerator(style: style).impactOccurred()
    }

    private static func notify(_ type: UINotificationFeedbackGenerator.FeedbackType) {
        guard AppSettings.haptics else { return }
        UINotificationFeedbackGenerator().notificationOccurred(type)
    }
}
