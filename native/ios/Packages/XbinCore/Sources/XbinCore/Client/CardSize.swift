/// A tile's card on a phone screen (plans/native.md §4, D117): one column
/// (`small`) or the screen's full width (`wide`). The phone arrangement
/// pref stores it per tile; a native widget is told it.
public enum CardSize: String, Codable, Sendable, CaseIterable {
    case small
    case wide
}
