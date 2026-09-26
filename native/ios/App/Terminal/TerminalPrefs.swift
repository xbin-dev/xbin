import Foundation
import XbinTerm

/// The terminal's keyboard preferences (UserDefaults; nothing secret): the
/// accessory row's customizable slot (XbinTerm's `AccessorySlot` storage,
/// key `termCustomKey`) and the hardware-keyboard options
/// (`TermKeyboardSettings` as JSON, key `termKeyboard`). Writing either posts
/// `.xbinTerminalKeysChanged`, so open terminals redraw their accessory row
/// and pick the options up without restarting. UIKit-free.
enum TerminalPrefs {
    private static var d: UserDefaults { .standard }
    static let keyboardKey = "termKeyboard"

    /// The extra key after the designed row (nil: none; never set: F1).
    static var accessorySlot: AccessoryKey? {
        get { AccessorySlot.decode(d.data(forKey: AccessorySlot.defaultsKey)) }
        set {
            d.set(AccessorySlot.encode(newValue), forKey: AccessorySlot.defaultsKey)
            changed()
        }
    }

    static var keyboard: TermKeyboardSettings {
        get {
            guard let data = d.data(forKey: keyboardKey),
                  let s = try? JSONDecoder().decode(TermKeyboardSettings.self, from: data) else { return TermKeyboardSettings() }
            return s
        }
        set {
            if let data = try? JSONEncoder().encode(newValue) { d.set(data, forKey: keyboardKey) }
            changed()
        }
    }

    private static func changed() {
        NotificationCenter.default.post(name: .xbinTerminalKeysChanged, object: nil)
    }
}

extension Notification.Name {
    /// The terminal's keyboard preferences changed (TerminalPrefs).
    static let xbinTerminalKeysChanged = Notification.Name("dev.xbin.terminalKeysChanged")
}
