import Foundation

// The person's theme (D184, D185): the web shell's Settings → Theme writes
// it as the `theme` key of the person's shell bucket (`root`, the bucket
// the app's own session reads, like `layout`): "light" or "dark", absent or
// "system" for the system's. xbind injects it into every document the
// person opens (docs/protocol.md), so the tiles in the app's web views
// already follow it; the app's own screens follow it too, or the phone's
// appearance when the person never chose. Any other value reads as the
// system's, as on the server.

/// The person's light/dark choice in a workspace.
public enum AppearancePref: String, Sendable, Equatable {
    case system, light, dark

    public static let key = "theme"
    /// `GET /api/xbin/prefs/theme` (404: never chose).
    public static let path = "/api/xbin/prefs/theme"
    /// The bucket the shell writes and the app reads (LayoutPref.component).
    public static let component = "root"

    /// The stored value: exactly "light" or "dark", else the system's.
    public init(json: JSONValue?) {
        switch json?.stringValue {
        case "light"?: self = .light
        case "dark"?: self = .dark
        default: self = .system
        }
    }

    /// Whether a `prefs` event changed the person's theme (from anywhere:
    /// the web shell's settings, another phone — no writer is skipped, the
    /// app never writes it).
    public static func concerns(component: String, key: String) -> Bool {
        (component == Self.component || component.isEmpty) && key == Self.key
    }
}
