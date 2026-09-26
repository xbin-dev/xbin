import Foundation

// Shell output as it reached the terminal: SGR colour/weight turned into
// styled spans the native view draws (the web strips them), every other
// escape (cursor moves, erase, OSC titles/links) dropped, "\r\n" a newline,
// a lone "\r" (a progress bar redrawing its line) overwriting the line, and
// a backspace removing the character before it.

public enum TerminalColor: Sendable, Hashable {
    /// 0–7 standard, 8–15 bright, 16–255 the xterm cube and greys.
    case palette(UInt8)
    case rgb(UInt8, UInt8, UInt8)
}

public struct TextStyle: Sendable, Hashable {
    public var foreground: TerminalColor?
    public var background: TerminalColor?
    public var bold = false
    public var dim = false
    public var italic = false
    public var underline = false
    public var inverse = false
    public var strikethrough = false

    public init() {}

    public static let plain = TextStyle()
    public var isPlain: Bool { self == .plain }
}

public struct StyledSpan: Sendable, Hashable {
    public var text: String
    public var style: TextStyle

    public init(_ text: String, _ style: TextStyle = .plain) {
        self.text = text
        self.style = style
    }
}

/// Parsed terminal output: styled spans (adjacent equal styles merged).
public struct ANSIText: Sendable, Hashable {
    public var spans: [StyledSpan]

    public init(spans: [StyledSpan]) { self.spans = spans }

    /// The plain text (no escapes).
    public var plain: String { spans.map(\.text).joined() }

    /// Parses raw terminal output.
    public static func parse(_ raw: String) -> ANSIText {
        var p = ANSIParser()
        p.feed(raw)
        return ANSIText(spans: p.finish())
    }

    /// The last `maxCharacters` characters (Swift Characters), styles kept.
    public func suffix(_ maxCharacters: Int) -> ANSIText {
        var need = maxCharacters
        var out: [StyledSpan] = []
        for s in spans.reversed() {
            if need <= 0 { break }
            let n = s.text.count
            if n <= need {
                out.append(s)
                need -= n
            } else {
                out.append(StyledSpan(String(s.text.suffix(need)), s.style))
                need = 0
            }
        }
        return ANSIText(spans: out.reversed())
    }

    /// The last `maxLines` lines, styles kept (a trailing newline does not
    /// count as an empty last line).
    public func suffix(lines maxLines: Int) -> ANSIText {
        guard maxLines > 0 else { return ANSIText(spans: []) }
        var seen = 0
        var out: [StyledSpan] = []
        var first = true
        for s in spans.reversed() {
            var text = Substring(s.text)
            if first, text.hasSuffix("\n") { // the output's final newline ends the last line
                out.append(StyledSpan("\n", s.style))
                text = text.dropLast()
            }
            if !text.isEmpty { first = false }
            var cut: Substring.Index?
            var i = text.endIndex
            while i > text.startIndex {
                i = text.index(before: i)
                if text[i] == "\n" {
                    seen += 1
                    if seen >= maxLines { cut = text.index(after: i); break }
                }
            }
            if let cut {
                if cut < text.endIndex { out.append(StyledSpan(String(text[cut...]), s.style)) }
                return ANSIText(spans: out.reversed())
            }
            if !text.isEmpty { out.append(StyledSpan(String(text), s.style)) }
        }
        return ANSIText(spans: out.reversed())
    }
}

struct ANSIParser {
    var lines: [(spans: [StyledSpan], newline: TextStyle)] = [] // finished lines, and the style their "\n" was written in
    var cur: [StyledSpan] = [] // the line being written
    var style = TextStyle()
    var pendingCR = false

    mutating func put(_ c: Character) {
        if pendingCR { // a lone "\r": the cursor went back to the line's start — what follows overwrites it
            pendingCR = false
            cur = []
        }
        if let last = cur.last, last.style == style {
            cur[cur.count - 1].text.append(c)
        } else {
            cur.append(StyledSpan(String(c), style))
        }
    }

    mutating func newline() {
        pendingCR = false
        lines.append((cur, style))
        cur = []
    }

    mutating func backspace() {
        guard !cur.isEmpty else { return }
        cur[cur.count - 1].text.removeLast()
        if cur[cur.count - 1].text.isEmpty { cur.removeLast() }
    }

    mutating func feed(_ raw: String) {
        let u = Array(raw.unicodeScalars)
        var i = 0
        while i < u.count {
            let c = u[i]
            switch c.value {
            case 0x1B:
                i = escape(u, i)
                continue
            case 0x0A:
                newline()
            case 0x0D:
                if i + 1 < u.count, u[i + 1].value == 0x0A { i += 1; newline() } else { pendingCR = true }
            case 0x08:
                backspace()
            case 0x09:
                put("\t")
            case 0x00..<0x20, 0x7F:
                break // other C0 controls: bell, shift-in/out, …
            default:
                put(Character(c))
            }
            i += 1
        }
    }

    /// Consumes an escape sequence starting at u[i] (ESC); returns the index after it.
    mutating func escape(_ u: [Unicode.Scalar], _ i: Int) -> Int {
        guard i + 1 < u.count else { return i + 1 }
        let n = u[i + 1].value
        if n == 0x5B { // CSI: params 0x30–0x3F, intermediates 0x20–0x2F, final 0x40–0x7E
            var j = i + 2
            var params = ""
            while j < u.count, (0x30...0x3F).contains(u[j].value) { params.unicodeScalars.append(u[j]); j += 1 }
            while j < u.count, (0x20...0x2F).contains(u[j].value) { j += 1 }
            guard j < u.count else { return j }
            if (0x40...0x7E).contains(u[j].value) {
                if u[j] == "m" { sgr(params) }
                return j + 1
            }
            return j // malformed: drop what was read
        }
        if n == 0x5D || n == 0x50 || n == 0x5F || n == 0x5E { // OSC, DCS, APC, PM: until BEL or ST
            var j = i + 2
            while j < u.count {
                if u[j].value == 0x07 { return j + 1 }
                if u[j].value == 0x1B, j + 1 < u.count, u[j + 1].value == 0x5C { return j + 2 }
                j += 1
            }
            return j
        }
        if (0x20...0x2F).contains(n) { return min(i + 3, u.count) } // ESC ( B and friends: one more byte
        return i + 2 // a two-byte escape (ESC 7, ESC =, …)
    }

    mutating func sgr(_ params: String) {
        let parts = params.isEmpty ? [""] : params.split(separator: ";", omittingEmptySubsequences: false).map(String.init)
        var k = 0
        func num(_ s: String) -> Int { Int(s) ?? 0 }
        while k < parts.count {
            let p = parts[k]
            if p.contains(":") { // colon sub-parameters: 38:2::r:g:b, 38:5:n, 4:3 (curly underline)
                let sub = p.split(separator: ":", omittingEmptySubsequences: false).map { Int($0) ?? 0 }
                if sub.first == 38 || sub.first == 48 || sub.first == 58 {
                    var color: TerminalColor?
                    if sub.count >= 3, sub[1] == 5 { color = .palette(UInt8(clamping: sub[2])) }
                    if sub.count >= 5, sub[1] == 2 {
                        let rgb = sub.count >= 6 ? Array(sub[3...5]) : Array(sub[2...4])
                        color = .rgb(UInt8(clamping: rgb[0]), UInt8(clamping: rgb[1]), UInt8(clamping: rgb[2]))
                    }
                    if sub[0] == 38 { style.foreground = color } else if sub[0] == 48 { style.background = color }
                } else if sub.first == 4 {
                    style.underline = sub.count < 2 || sub[1] != 0
                }
                k += 1
                continue
            }
            let v = num(p)
            switch v {
            case 0: style = TextStyle()
            case 1: style.bold = true
            case 2: style.dim = true
            case 3: style.italic = true
            case 4: style.underline = true
            case 7: style.inverse = true
            case 9: style.strikethrough = true
            case 21: style.underline = true
            case 22: style.bold = false; style.dim = false
            case 23: style.italic = false
            case 24: style.underline = false
            case 27: style.inverse = false
            case 29: style.strikethrough = false
            case 30...37: style.foreground = .palette(UInt8(v - 30))
            case 39: style.foreground = nil
            case 40...47: style.background = .palette(UInt8(v - 40))
            case 49: style.background = nil
            case 90...97: style.foreground = .palette(UInt8(v - 90 + 8))
            case 100...107: style.background = .palette(UInt8(v - 100 + 8))
            case 38, 48, 58:
                var color: TerminalColor?
                if k + 2 < parts.count, num(parts[k + 1]) == 5 {
                    color = .palette(UInt8(clamping: num(parts[k + 2])))
                    k += 2
                } else if k + 4 < parts.count, num(parts[k + 1]) == 2 {
                    color = .rgb(UInt8(clamping: num(parts[k + 2])), UInt8(clamping: num(parts[k + 3])), UInt8(clamping: num(parts[k + 4])))
                    k += 4
                }
                if v == 38 { style.foreground = color } else if v == 48 { style.background = color }
            default: break
            }
            k += 1
        }
    }

    mutating func finish() -> [StyledSpan] {
        var out: [StyledSpan] = []
        func add(_ sp: StyledSpan) {
            if let last = out.last, last.style == sp.style { out[out.count - 1].text += sp.text } else { out.append(sp) }
        }
        for l in lines {
            for sp in l.spans { add(sp) }
            add(StyledSpan("\n", l.newline))
        }
        for sp in cur { add(sp) }
        return out
    }
}

/// The web's stripAnsi, exactly: colour and cursor escapes out, the rest
/// verbatim (use `ANSIText.parse(…).plain` for the terminal's view).
public func stripANSI(_ s: String) -> String {
    s.replacing(RX.ansi, with: "")
}

extension RX {
    nonisolated(unsafe) static let ansi = make(#"\x{1B}\[[0-9;?]*[ -/]*[@-~]|\x{1B}\][^\x{07}]*(?:\x{07}|\x{1B}\\)|\x{1B}[@-Z\\-_]"#)
}

/// A command's output as a card shows it: the whole text, and when it is
/// long, only its tail with a count of what was left out ("… N earlier
/// characters — show all").
public struct OutputView: Sendable, Hashable {
    /// The whole output, parsed.
    public let full: ANSIText
    /// What the card shows by default (== full when short).
    public let tail: ANSIText
    /// Characters left out of `tail` (0 = nothing hidden).
    public let hiddenCharacters: Int
    /// Lines left out of `tail` when a line cap applied.
    public let hiddenLines: Int

    /// The web caps at 20 000 characters; a phone card wants fewer lines —
    /// pass `maxLines` for that (both caps apply).
    public init(raw: String, maxCharacters: Int = 20_000, maxLines: Int? = nil) {
        full = ANSIText.parse(raw)
        let plain = full.plain
        var t = full
        var hiddenChars = 0
        var hiddenLines = 0
        let total = plain.count
        if total > maxCharacters {
            t = full.suffix(maxCharacters)
            hiddenChars = total - maxCharacters
        }
        if let maxLines {
            let lines = t.plain.split(separator: "\n", omittingEmptySubsequences: false).count - (t.plain.hasSuffix("\n") ? 1 : 0)
            if lines > maxLines {
                let before = t.plain.count
                t = t.suffix(lines: maxLines)
                hiddenChars += before - t.plain.count
                hiddenLines = lines - maxLines
            }
        }
        tail = t
        hiddenCharacters = hiddenChars
        self.hiddenLines = hiddenLines
    }

    public var isTruncated: Bool { hiddenCharacters > 0 }
    /// Whitespace only: the card shows nothing.
    public var isBlank: Bool { trim(full.plain).isEmpty }
}
