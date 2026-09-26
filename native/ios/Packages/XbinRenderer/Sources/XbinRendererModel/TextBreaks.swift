import Foundation

/// Where long unbroken words may wrap. iOS hyphenates a word that doesn't
/// fit its line ("llmgw_active_re-quests"), which misreads an identifier —
/// the hyphen isn't in the value — and a monospaced value breaks at odd
/// points. The reference renderer wraps such words anywhere
/// (`overflow-wrap: anywhere`). SwiftUI has no switch for that, so the
/// renderer puts a zero-width space between the characters of the words
/// that need it: each character becomes its own word, which is never
/// hyphenated, and the line breaks where it is full.
///
/// Only for text that is shown, never selected or copied (the spaces
/// would come along): selectable text and code keep their bytes.
public enum TextBreaks {
    /// U+200B ZERO WIDTH SPACE.
    public static let breakOpportunity: Character = "\u{200B}"

    /// `text` with break opportunities inside its long identifier-like
    /// words (``needsBreaks(_:mono:)``); other words and all whitespace
    /// are kept as they are.
    public static func anywhere(_ text: String, mono: Bool) -> String {
        var out = ""
        out.reserveCapacity(text.count)
        var word = ""
        func flush() {
            if needsBreaks(word, mono: mono) {
                var first = true
                for ch in word {
                    if !first { out.append(breakOpportunity) }
                    out.append(ch)
                    first = false
                }
            } else {
                out += word
            }
            word = ""
        }
        for ch in text {
            if ch.isWhitespace {
                flush()
                out.append(ch)
            } else {
                word.append(ch)
            }
        }
        flush()
        return out
    }

    /// Whether `text` changes under ``anywhere(_:mono:)``.
    public static func applies(to text: String, mono: Bool) -> Bool {
        text.split(whereSeparator: \.isWhitespace).contains { needsBreaks($0, mono: mono) }
    }

    /// A word that may wrap at any character: in monospaced text, any word
    /// of 10 characters or more (hashes, hostnames, paths); in proportional
    /// text, a word of 12 or more that reads as an identifier rather than
    /// prose — it holds `_ . / : @ = # \` or a digit, or changes case
    /// inside (`camelCase`). Prose keeps the platform's hyphenation.
    public static func needsBreaks<S: StringProtocol>(_ word: S, mono: Bool) -> Bool {
        let n = word.count
        if mono { return n >= 10 }
        guard n >= 12 else { return false }
        var previousLower = false
        for ch in word {
            if "_./:@=#\\".contains(ch) || ch.isNumber { return true }
            if previousLower && ch.isUppercase { return true }
            previousLower = ch.isLowercase
        }
        return false
    }
}
