import Foundation

/// The controlled-text rules of a text control that knows about input
/// method composition (native/spec/tree.md §6, plans/native.md §24): the
/// renderer's `field` and `composer` wrap UIKit text views and ask this
/// what to do on every change, so the rules are tested here.
///
/// - While marked (composing) text is in the control — a Japanese reading
///   before its kanji are chosen, a Korean syllable being built — nothing
///   is reported: the tile never sees half-composed text, and so never
///   rewrites it under the user's fingers.
/// - When the composition commits, the text is reported once (`input`).
/// - A value the tile sets is applied at once, a focused control included
///   (that is how resets work), unless a composition is in progress: then
///   it waits for the composition to end and replaces what was composed.
/// - The control's own reports never come back as sets: a value equal to
///   the last one reported or applied changes nothing.
public struct TextInputGate: Sendable, Equatable {
    /// What the tile has been told the control shows: the last value
    /// reported or applied.
    public private(set) var reported: String
    /// A value the tile set during a composition, applied when it ends.
    public private(set) var deferred: String?

    public init(value: String) {
        reported = value
    }

    /// What the control should do.
    public enum Effect: Sendable, Equatable {
        case none
        /// Report the text (`input {value}`).
        case report(String)
        /// Replace the control's text with the tile's value.
        case replace(String)
    }

    /// The control's text or its marked range changed (typing, a
    /// composition step or commit, a paste, dictation). `text` is the
    /// control's whole text, marked text included; `composing` is whether
    /// any of it is still marked.
    public mutating func changed(text: String, composing: Bool) -> Effect {
        if composing { return .none }
        if let d = deferred {
            deferred = nil
            reported = d
            return d == text ? .none : .replace(d)
        }
        guard text != reported else { return .none }
        reported = text
        return .report(text)
    }

    /// The tile's value for the control (after a render): what to show.
    /// `shown` is the control's text now.
    public mutating func tile(_ value: String, shown: String, composing: Bool) -> Effect {
        guard value != reported else {
            // The tile agrees with what it was told: a deferred set it
            // took back is dropped.
            deferred = nil
            return .none
        }
        if composing {
            deferred = value
            return .none
        }
        reported = value
        deferred = nil
        return value == shown ? .none : .replace(value)
    }
}
