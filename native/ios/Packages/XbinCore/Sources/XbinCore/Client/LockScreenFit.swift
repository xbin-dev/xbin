import Foundation

/// What a collapsed notification certainly shows (D241, review finding 1):
/// the app's copy of xbind's `delegation.LockScreenFits`, character for
/// character — the vectors xbind's test writes (lockscreen-fit.json) hold
/// the two together. iOS draws the body — the call line, then the body or
/// "(no body)" — in at most `lines` lines collapsed, wrapping at word ends
/// and breaking a word longer than a line; the line is 12 em (about 281 pt
/// of text at 23 pt, the largest default Dynamic Type on the narrowest
/// iPhone) and every character's advance is counted at or above SF Pro's.
/// Only characters whose width and direction are known count at all
/// (printable Latin and a few typographic marks): any other — a line or
/// paragraph separator, a tab, a space but U+0020, a right-to-left letter,
/// a wide, combining or emoji character — and two spaces together refuse.
public enum LockScreenFit {
    public static let lines = 4
    public static let lineWidth = 1200 // hundredths of an em

    public static func fits(method: String, target: String, body: String) -> Bool {
        guard !target.unicodeScalars.contains("\n"), countable(method), countable(target), countable(body) else { return false }
        return self.lines(method: method, target: target, body: body) <= lines
    }

    /// The lines the drawn text takes (the call line, then each body line).
    public static func lines(method: String, target: String, body: String) -> Int {
        var n = rendered(Array("\(method) \(target)".unicodeScalars))
        let b = body.isEmpty ? Array("(no body)".unicodeScalars) : Array(body.unicodeScalars)
        for line in b.split(separator: "\n", omittingEmptySubsequences: false) { n += rendered(Array(line)) }
        return n
    }

    static func countable(_ s: String) -> Bool {
        let u = Array(s.unicodeScalars)
        for (i, c) in u.enumerated() {
            if c == " ", i + 1 < u.count, u[i + 1] == " " { return false }
            if c != "\n", advance(c) == 0 { return false }
        }
        return true
    }

    /// One line (no line feed): greedy at spaces; a word wider than a line
    /// is broken between its characters.
    static func rendered(_ s: [Unicode.Scalar]) -> Int {
        let space = advance(" ")
        var lines = 1, cur = 0, started = false
        for w in s.split(separator: " ", omittingEmptySubsequences: false) {
            let ww = w.reduce(0) { $0 + advance($1) }
            if started {
                if cur + space + ww <= lineWidth { cur += space + ww; continue }
                lines += 1
                cur = 0
            }
            started = true
            if ww <= lineWidth { cur = ww; continue }
            for c in w {
                let cw = advance(c)
                if cur + cw > lineWidth { lines += 1; cur = cw } else { cur += cw }
            }
        }
        return lines
    }

    /// A character's advance in hundredths of an em, at or above SF Pro
    /// Text's; 0 where its width or direction isn't known here.
    static func advance(_ c: Unicode.Scalar) -> Int {
        let v = c.value
        if c == " " { return 30 }
        if "',.:;ijl|!".unicodeScalars.contains(c) { return 30 }
        if "frt\"()[]{}/\\`-I".unicodeScalars.contains(c) { return 45 }
        if "mwMW@%&".unicodeScalars.contains(c) { return 100 }
        if v >= 0x41 && v <= 0x5A { return 80 }
        if v > 0x20 && v < 0x7F { return 65 }
        if v >= 0xA1 && v <= 0x24F && v != 0xAD { return 100 }
        switch v {
        case 0x2013, 0x2014, 0x2018, 0x2019, 0x201C, 0x201D, 0x2026, 0x20AC: return 100
        default: return 0
        }
    }
}
