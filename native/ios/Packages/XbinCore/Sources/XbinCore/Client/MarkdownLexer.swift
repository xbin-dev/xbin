import Foundation

// Markdown → the runtime's wire tokens (native/spec/tree.md "Markdown
// tokens"; web/xb/rt-markdown.js on the runtime side, which uses marked).
// The ACP agent screen gets raw markdown from the agent, with no runtime to
// lex it, so the app lexes it here into the same token shapes the renderer
// already draws. The policy is the runtime's: GFM, a single newline is a
// line break, raw HTML is dropped, images show as "[image: alt]", links only
// for http/https/mailto. It covers what agents write — headings, fences,
// lists (nested, ordered, tasks), quotes, tables, rules, emphasis, code
// spans, links, autolinks — not every CommonMark corner; the corpus in the
// tests is checked against marked's own output.

public enum MarkdownLexer {
    /// The block tokens of `source`.
    public static func tokens(_ source: String) -> JSONValue {
        let norm = source.replacingOccurrences(of: "\r\n", with: "\n").replacingOccurrences(of: "\r", with: "\n")
        var lines = norm.components(separatedBy: "\n")
        if lines.last == "" { lines.removeLast() }
        return .array(blocks(lines[...]))
    }

    // MARK: Blocks

    static func blocks(_ lines: ArraySlice<String>) -> [JSONValue] {
        var out: [JSONValue] = []
        var i = lines.startIndex
        while i < lines.endIndex {
            let line = lines[i]
            if isBlank(line) { i += 1; continue }
            if let fence = fenceOpen(line) {
                var body: [String] = []
                var j = i + 1
                while j < lines.endIndex {
                    if isFenceClose(lines[j], fence) { break }
                    body.append(stripIndent(lines[j], upTo: fence.indent))
                    j += 1
                }
                var b: [String: JSONValue] = ["t": "code", "text": .string(body.joined(separator: "\n"))]
                if !fence.lang.isEmpty { b["lang"] = .string(fence.lang) }
                out.append(.object(b))
                i = min(j + 1, lines.endIndex)
                continue
            }
            if let h = atxHeading(line) {
                out.append(["t": "heading", "depth": .int(Int64(h.depth)), "c": .array(inline(h.text))])
                i += 1
                continue
            }
            if isRule(line) { out.append(["t": "hr"]); i += 1; continue }
            if indentWidth(line) >= 4 {
                var body: [String] = []
                var j = i
                while j < lines.endIndex, isBlank(lines[j]) || indentWidth(lines[j]) >= 4 {
                    body.append(stripIndent(lines[j], upTo: 4))
                    j += 1
                }
                while body.last.map(isBlank) == true { body.removeLast() }
                out.append(["t": "code", "text": .string(body.joined(separator: "\n"))])
                i = j
                continue
            }
            if quoteContent(line) != nil {
                var inner: [String] = []
                var j = i
                while j < lines.endIndex, let q = quoteContent(lines[j]) {
                    inner.append(q)
                    j += 1
                }
                out.append(["t": "blockquote", "c": .array(blocks(inner[...]))])
                i = j
                continue
            }
            if let marker = listMarker(line) {
                let (list, next) = listBlock(lines, from: i, first: marker)
                out.append(list)
                i = next
                continue
            }
            if i + 1 < lines.endIndex, let (header, align) = tableHead(line, lines[i + 1]) {
                var rows: [JSONValue] = []
                var j = i + 2
                while j < lines.endIndex, !isBlank(lines[j]), lines[j].contains("|") || !startsBlock(lines[j]) {
                    let cells = splitRow(lines[j])
                    var row: [JSONValue] = []
                    for k in 0..<header.count {
                        row.append(.array(inline(k < cells.count ? cells[k] : "")))
                    }
                    rows.append(.array(row))
                    j += 1
                }
                out.append(["t": "table", "align": .array(align),
                            "header": .array(header.map { .array(inline($0)) }), "rows": .array(rows)])
                i = j
                continue
            }
            // A paragraph: until a blank line or a block that may interrupt it.
            var para: [String] = [line]
            var j = i + 1
            while j < lines.endIndex {
                let l = lines[j]
                if isBlank(l) { break }
                if let level = setextLevel(l) {
                    out.append(["t": "heading", "depth": .int(Int64(level)),
                                "c": .array(inline(para.map(trimmed).joined(separator: "\n")))])
                    para = []
                    j += 1
                    break
                }
                if interruptsParagraph(l) { break }
                para.append(l)
                j += 1
            }
            if !para.isEmpty {
                out.append(["t": "paragraph", "c": .array(inline(para.map(trimmedLeading).joined(separator: "\n")))])
            }
            i = j
        }
        return out
    }

    struct Fence { var char: Character; var count: Int; var indent: Int; var lang: String }

    static func fenceOpen(_ line: String) -> Fence? {
        let ind = indentWidth(line)
        guard ind < 4 else { return nil }
        let s = line.drop(while: { $0 == " " })
        guard let c = s.first, c == "`" || c == "~" else { return nil }
        let n = s.prefix(while: { $0 == c }).count
        guard n >= 3 else { return nil }
        let info = s.dropFirst(n).trimmingCharacters(in: .whitespaces)
        if c == "`", info.contains("`") { return nil }
        let lang = info.split(whereSeparator: \.isWhitespace).first.map(String.init) ?? ""
        return Fence(char: c, count: n, indent: ind, lang: lang)
    }

    static func isFenceClose(_ line: String, _ f: Fence) -> Bool {
        guard indentWidth(line) < 4 else { return false }
        let s = line.trimmingCharacters(in: .whitespaces)
        return s.count >= f.count && s.allSatisfy { $0 == f.char }
    }

    static func atxHeading(_ line: String) -> (depth: Int, text: String)? {
        guard indentWidth(line) < 4 else { return nil }
        let s = line.drop(while: { $0 == " " })
        let hashes = s.prefix(while: { $0 == "#" }).count
        guard (1...6).contains(hashes) else { return nil }
        let rest = s.dropFirst(hashes)
        guard rest.isEmpty || rest.first == " " || rest.first == "\t" else { return nil }
        var text = rest.trimmingCharacters(in: .whitespaces)
        // A closing sequence of #s (preceded by a space) is dropped.
        if let r = text.range(of: #"(^|\s)#+\s*$"#, options: .regularExpression) {
            text = String(text[..<r.lowerBound]).trimmingCharacters(in: .whitespaces)
        }
        return (hashes, text)
    }

    static func isRule(_ line: String) -> Bool {
        guard indentWidth(line) < 4 else { return false }
        let s = line.filter { $0 != " " && $0 != "\t" }
        guard let c = s.first, c == "-" || c == "*" || c == "_", s.count >= 3 else { return false }
        return s.allSatisfy { $0 == c }
    }

    static func setextLevel(_ line: String) -> Int? {
        guard indentWidth(line) < 4 else { return nil }
        let s = line.trimmingCharacters(in: .whitespaces)
        guard !s.isEmpty else { return nil }
        if s.allSatisfy({ $0 == "=" }) { return 1 }
        if s.allSatisfy({ $0 == "-" }) { return 2 }
        return nil
    }

    static func quoteContent(_ line: String) -> String? {
        guard indentWidth(line) < 4 else { return nil }
        let s = line.drop(while: { $0 == " " })
        guard s.first == ">" else { return nil }
        var rest = s.dropFirst()
        if rest.first == " " { rest = rest.dropFirst() }
        return String(rest)
    }

    struct ListMarker { var ordered: Bool; var start: Int; var bullet: Character; var indent: Int; var content: Int }

    static func listMarker(_ line: String) -> ListMarker? {
        let ind = indentWidth(line)
        guard ind < 4 else { return nil }
        let s = Array(line.drop(while: { $0 == " " }))
        guard let c = s.first else { return nil }
        if c == "-" || c == "*" || c == "+" {
            guard s.count == 1 || s[1] == " " || s[1] == "\t" else { return nil }
            let spaces = s.dropFirst().prefix(while: { $0 == " " }).count
            let pad = spaces == 0 || spaces > 4 ? 1 : spaces
            return ListMarker(ordered: false, start: 1, bullet: c, indent: ind, content: ind + 1 + pad)
        }
        let digits = s.prefix(while: { $0.isASCII && $0.isNumber })
        guard (1...9).contains(digits.count), s.count > digits.count else { return nil }
        let d = s[digits.count]
        guard d == "." || d == ")" else { return nil }
        guard s.count == digits.count + 1 || s[digits.count + 1] == " " else { return nil }
        let spaces = s.dropFirst(digits.count + 1).prefix(while: { $0 == " " }).count
        let pad = spaces == 0 || spaces > 4 ? 1 : spaces
        return ListMarker(ordered: true, start: Int(String(digits)) ?? 1, bullet: d, indent: ind,
                          content: ind + digits.count + 1 + pad)
    }

    static func listBlock(_ lines: ArraySlice<String>, from start: Int, first: ListMarker) -> (JSONValue, Int) {
        var items: [[String]] = []
        var loose = false
        var i = start
        var marker = first
        var sawBlank = false
        while i < lines.endIndex {
            // One item: the marker line, then lines indented to its content
            // (or lazy paragraph continuations), blank lines between.
            var body = [String(lines[i].dropFirst(min(marker.content, lines[i].count)))]
            if lines[i].trimmingCharacters(in: .whitespaces).count <= marker.content - marker.indent - 1 { body = [""] }
            i += 1
            var blankInside = false
            while i < lines.endIndex {
                let l = lines[i]
                if isBlank(l) {
                    body.append("")
                    i += 1
                    continue
                }
                if indentWidth(l) >= marker.content {
                    if body.last == "" { blankInside = true }
                    body.append(stripIndent(l, upTo: marker.content))
                    i += 1
                    continue
                }
                // Lazy continuation of the item's paragraph.
                if body.last != "", listMarker(l) == nil, !startsBlock(l) {
                    body.append(l.trimmingCharacters(in: .whitespaces))
                    i += 1
                    continue
                }
                break
            }
            var trailingBlank = false
            while body.last == "" { body.removeLast(); trailingBlank = true }
            if blankInside { loose = true }
            items.append(body)
            guard i < lines.endIndex, let next = listMarker(lines[i]), next.ordered == first.ordered,
                  next.ordered || next.bullet == first.bullet, next.indent < first.content else { break }
            if trailingBlank || sawBlank { loose = true }
            sawBlank = trailingBlank
            marker = next
        }
        var list: [String: JSONValue] = ["t": "list", "ordered": .bool(first.ordered), "loose": .bool(loose)]
        if first.ordered { list["start"] = .int(Int64(first.start)) }
        list["items"] = .array(items.map { body in
            var lines = body
            var item: [String: JSONValue] = [:]
            if let f = lines.first, let (checked, rest) = taskBox(f) {
                item["task"] = true
                item["checked"] = .bool(checked)
                lines[0] = rest
            }
            item["c"] = .array(blocks(lines[...]))
            return .object(item)
        })
        return (.object(list), i)
    }

    static func taskBox(_ s: String) -> (Bool, String)? {
        guard s.count >= 3, s.hasPrefix("[") else { return nil }
        let a = Array(s)
        guard a[2] == "]", a[1] == " " || a[1] == "x" || a[1] == "X" else { return nil }
        guard a.count == 3 || a[3] == " " else { return nil }
        return (a[1] != " ", String(a.dropFirst(a.count > 3 ? 4 : 3)))
    }

    static func tableHead(_ head: String, _ delim: String) -> ([String], [JSONValue])? {
        guard head.contains("|"), delim.contains("-") else { return nil }
        let cols = splitRow(delim)
        guard !cols.isEmpty, cols.allSatisfy({ $0.range(of: #"^:?-+:?$"#, options: .regularExpression) != nil }) else { return nil }
        let header = splitRow(head)
        guard header.count == cols.count else { return nil }
        let align: [JSONValue] = cols.map { c in
            switch (c.hasPrefix(":"), c.hasSuffix(":")) {
            case (true, true): return "center"
            case (true, false): return "left"
            case (false, true): return "right"
            default: return .null
            }
        }
        return (header, align)
    }

    static func splitRow(_ line: String) -> [String] {
        var s = line.trimmingCharacters(in: .whitespaces)
        if s.hasPrefix("|") { s.removeFirst() }
        if s.hasSuffix("|"), !s.hasSuffix("\\|") { s.removeLast() }
        var cells: [String] = []
        var cur = ""
        var escaped = false
        for ch in s {
            if escaped { cur.append(ch == "|" ? "|" : "\\\(ch)"); escaped = false; continue }
            if ch == "\\" { escaped = true; continue }
            if ch == "|" { cells.append(cur.trimmingCharacters(in: .whitespaces)); cur = ""; continue }
            cur.append(ch)
        }
        if escaped { cur.append("\\") }
        cells.append(cur.trimmingCharacters(in: .whitespaces))
        return cells
    }

    static func startsBlock(_ l: String) -> Bool {
        fenceOpen(l) != nil || atxHeading(l) != nil || isRule(l) || quoteContent(l) != nil || listMarker(l) != nil
    }

    /// What ends a paragraph without a blank line (CommonMark: a list only
    /// when it isn't empty, and an ordered one only when it starts at 1).
    static func interruptsParagraph(_ l: String) -> Bool {
        if fenceOpen(l) != nil || atxHeading(l) != nil || isRule(l) || quoteContent(l) != nil { return true }
        if let m = listMarker(l) {
            let content = l.dropFirst(min(m.content, l.count)).trimmingCharacters(in: .whitespaces)
            return !content.isEmpty && (!m.ordered || m.start == 1)
        }
        return false
    }

    static func isBlank(_ s: String) -> Bool { s.allSatisfy { $0 == " " || $0 == "\t" } }

    static func indentWidth(_ s: String) -> Int {
        var w = 0
        for c in s {
            if c == " " { w += 1 } else if c == "\t" { w += 4 - w % 4 } else { break }
        }
        return w
    }

    static func stripIndent(_ s: String, upTo n: Int) -> String {
        var w = 0
        var idx = s.startIndex
        while idx < s.endIndex, w < n {
            let c = s[idx]
            if c == " " { w += 1 } else if c == "\t" { w += 4 - w % 4 } else { break }
            idx = s.index(after: idx)
        }
        return String(s[idx...])
    }

    static func trimmed(_ s: String) -> String { s.trimmingCharacters(in: .whitespaces) }
    static func trimmedLeading(_ s: String) -> String { String(s.drop(while: { $0 == " " || $0 == "\t" })) }

    // MARK: Inlines

    /// Inline tokens; adjacent text merges.
    public static func inline(_ text: String) -> [JSONValue] {
        var p = InlineParser(Array(text))
        return p.parse(until: nil)
    }

    static let safeLink = try! NSRegularExpression(pattern: "^(https?:|mailto:)", options: .caseInsensitive)

    static func isSafeLink(_ href: String) -> Bool {
        safeLink.firstMatch(in: href, range: NSRange(href.startIndex..., in: href)) != nil
    }

    static let entities: [String: String] = [
        "amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": "\u{a0}", "copy": "©", "reg": "®",
        "trade": "™", "hellip": "…", "mdash": "—", "ndash": "–", "lsquo": "‘", "rsquo": "’", "ldquo": "“", "rdquo": "”",
        "bull": "•", "middot": "·", "deg": "°", "times": "×", "divide": "÷", "euro": "€", "pound": "£", "yen": "¥",
        "cent": "¢", "sect": "§", "para": "¶", "plusmn": "±", "laquo": "«", "raquo": "»", "larr": "←", "rarr": "→",
        "uarr": "↑", "darr": "↓", "harr": "↔", "check": "✓", "hearts": "♥", "micro": "µ", "frac12": "½",
        "frac14": "¼", "frac34": "¾", "shy": "\u{ad}",
    ]
}

/// A small recursive-descent inline parser (the runtime's inline token set:
/// text, strong, em, del, codespan, br, link).
struct InlineParser {
    let s: [Character]
    var i = 0
    var out: [JSONValue] = []

    init(_ s: [Character]) { self.s = s }

    static let punct: Set<Character> = Set("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~")

    mutating func parse(until close: String?) -> [JSONValue] {
        var tokens: [JSONValue] = []
        var text = ""
        func flush() {
            guard !text.isEmpty else { return }
            if case .object(var last)? = tokens.last, last["t"] == "text", let prev = last["text"]?.stringValue {
                last["text"] = .string(prev + text)
                tokens[tokens.count - 1] = .object(last)
            } else {
                tokens.append(["t": "text", "text": .string(text)])
            }
            text = ""
        }
        func push(_ t: JSONValue) {
            flush()
            if case .object(let o) = t, o["t"] == "text", let x = o["text"]?.stringValue { text = x; flush(); return }
            tokens.append(t)
        }
        while i < s.count {
            if let close, matches(close, at: i) { break }
            let c = s[i]
            if c == "\\", i + 1 < s.count {
                if s[i + 1] == "\n" { push(["t": "br"]); i += 2; continue }
                if InlineParser.punct.contains(s[i + 1]) { text.append(s[i + 1]); i += 2; continue }
            }
            if c == "\n" {
                // breaks: true — every newline inside a paragraph is a line break.
                while text.last == " " { text.removeLast() }
                push(["t": "br"])
                i += 1
                while i < s.count, s[i] == " " { i += 1 }
                continue
            }
            if c == "`", let t = codeSpan() { push(t); continue }
            if c == "!", i + 1 < s.count, s[i + 1] == "[", let (alt, _, end) = linkAt(i + 1) {
                text += "[image: \(plain(alt))]"
                i = end
                continue
            }
            if c == "[", let (label, href, end) = linkAt(i) {
                let inner = InlineParser.sub(label)
                if MarkdownLexer.isSafeLink(href) {
                    push(["t": "link", "href": .string(href), "c": .array(inner)])
                } else {
                    for t in inner { push(t) }
                }
                i = end
                continue
            }
            if c == "<", let t = angle() { if let t { push(t) }; continue }
            if c == "h" || c == "w", let t = autolink() { push(t); continue }
            if c == "&", let e = entity() { text += e; continue }
            if c == "@", let (local, t) = emailAutolink(before: text) {
                text.removeLast(local)
                push(t)
                continue
            }
            if c == "*" || c == "_" || c == "~", let t = emphasis() { push(t); continue }
            text.append(c)
            i += 1
        }
        flush()
        return tokens
    }

    func matches(_ lit: String, at k: Int) -> Bool {
        let l = Array(lit)
        guard k + l.count <= s.count else { return false }
        return Array(s[k..<(k + l.count)]) == l
    }

    static func sub(_ text: [Character]) -> [JSONValue] {
        var p = InlineParser(text)
        return p.parse(until: nil)
    }

    func plain(_ chars: [Character]) -> String {
        InlineParser.sub(chars).map { InlineParser.plainText($0) }.joined()
    }

    static func plainText(_ t: JSONValue) -> String {
        if let x = t["text"]?.stringValue { return x }
        return (t["c"]?.arrayValue ?? []).map(plainText).joined()
    }

    /// `code`, ``code with ` inside``.
    mutating func codeSpan() -> JSONValue? {
        let n = s[i...].prefix(while: { $0 == "`" }).count
        var j = i + n
        while j < s.count {
            if s[j] == "`" {
                let m = s[j...].prefix(while: { $0 == "`" }).count
                if m == n {
                    var body = String(s[(i + n)..<j]).replacingOccurrences(of: "\n", with: " ")
                    if body.count >= 2, body.first == " ", body.last == " ", !body.allSatisfy({ $0 == " " }) {
                        body = String(body.dropFirst().dropLast())
                    }
                    i = j + m
                    return ["t": "codespan", "text": .string(body)]
                }
                j += m
            } else {
                j += 1
            }
        }
        return nil
    }

    /// `[label](href "title")` at k → (label, href, index after).
    func linkAt(_ k: Int) -> ([Character], String, Int)? {
        guard s[k] == "[" else { return nil }
        var depth = 0
        var j = k
        while j < s.count {
            if s[j] == "\\" { j += 2; continue }
            if s[j] == "[" { depth += 1 }
            if s[j] == "]" { depth -= 1; if depth == 0 { break } }
            j += 1
        }
        guard j < s.count, j + 1 < s.count, s[j + 1] == "(" else { return nil }
        let label = Array(s[(k + 1)..<j])
        var e = j + 2
        var paren = 0
        while e < s.count {
            if s[e] == "\\" { e += 2; continue }
            if s[e] == "(" { paren += 1 }
            if s[e] == ")" { if paren == 0 { break }; paren -= 1 }
            if s[e] == "\n" { return nil }
            e += 1
        }
        guard e < s.count else { return nil }
        var dest = String(s[(j + 2)..<e]).trimmingCharacters(in: .whitespaces)
        if let q = dest.range(of: #"\s+("[^"]*"|'[^']*')$"#, options: .regularExpression) { dest = String(dest[..<q.lowerBound]) }
        if dest.hasPrefix("<"), dest.hasSuffix(">") { dest = String(dest.dropFirst().dropLast()) }
        return (label, dest, e + 1)
    }

    /// `<https://…>` autolinks; other tags are raw HTML and dropped
    /// (returns .some(nil)).
    mutating func angle() -> JSONValue?? {
        guard let end = s[i...].firstIndex(of: ">") else { return nil }
        let inner = String(s[(i + 1)..<end])
        if inner.contains(" ") || inner.contains("\n") { return nil }
        if MarkdownLexer.isSafeLink(inner) {
            i = end + 1
            return .some(["t": "link", "href": .string(inner), "c": [["t": "text", "text": .string(inner)]]])
        }
        if inner.range(of: #"^/?[A-Za-z][A-Za-z0-9-]*(\s[^>]*)?/?$"#, options: .regularExpression) != nil {
            i = end + 1
            return .some(nil)
        }
        return nil
    }

    /// GFM autolink literals: `https://…`, `http://…`, `www.…`.
    mutating func autolink() -> JSONValue? {
        if i > 0, s[i - 1].isLetter || s[i - 1].isNumber { return nil }
        let rest = String(s[i...].prefix(2048))
        let isWWW = rest.lowercased().hasPrefix("www.")
        guard rest.lowercased().hasPrefix("https://") || rest.lowercased().hasPrefix("http://") || isWWW else { return nil }
        var url = String(rest.prefix(while: { !$0.isWhitespace && $0 != "<" }))
        // Trailing punctuation isn't part of the link; an unbalanced ) neither.
        while let l = url.last, ".,:;!?\"'*_~".contains(l) { url.removeLast() }
        while url.hasSuffix(")"), url.filter({ $0 == ")" }).count > url.filter({ $0 == "(" }).count { url.removeLast() }
        guard url.count > (isWWW ? 4 : 8) else { return nil }
        i += url.count
        let href = isWWW ? "http://" + url : url
        return ["t": "link", "href": .string(href), "c": [["t": "text", "text": .string(url)]]]
    }

    /// GFM email autolinks: `local@domain.tld`, the local part already
    /// read into `text` (returns its length to take back).
    mutating func emailAutolink(before text: String) -> (Int, JSONValue)? {
        let local = text.reversed().prefix(while: { $0.isASCII && ($0.isLetter || $0.isNumber || "._+-".contains($0)) })
        guard !local.isEmpty else { return nil }
        var j = i + 1
        var domain = ""
        while j < s.count, s[j].isASCII, s[j].isLetter || s[j].isNumber || s[j] == "-" || s[j] == "." || s[j] == "_" {
            domain.append(s[j])
            j += 1
        }
        while domain.hasSuffix(".") || domain.hasSuffix("-") || domain.hasSuffix("_") { domain.removeLast(); j -= 1 }
        guard domain.contains("."), !domain.hasPrefix("."), !domain.contains("_") else { return nil }
        let addr = String(local.reversed()) + "@" + domain
        i = j
        return (local.count, ["t": "link", "href": .string("mailto:" + addr), "c": [["t": "text", "text": .string(addr)]]])
    }

    mutating func entity() -> String? {
        guard let end = s[i...].prefix(40).firstIndex(of: ";") else { return nil }
        let name = String(s[(i + 1)..<end])
        var out: String?
        if name.hasPrefix("#") {
            let n = name.dropFirst().lowercased().hasPrefix("x") ? UInt32(name.dropFirst(2), radix: 16) : UInt32(name.dropFirst())
            if let n, n > 0, let u = Unicode.Scalar(n) { out = String(Character(u)) }
        } else {
            out = MarkdownLexer.entities[name]
        }
        guard let out else { return nil }
        i = end + 1
        return out
    }

    /// `**strong**`, `__strong__`, `*em*`, `_em_`, `~~del~~`, `~del~`.
    mutating func emphasis() -> JSONValue? {
        let c = s[i]
        let run = s[i...].prefix(while: { $0 == c }).count
        let open = min(run, 2)
        let after = i + run < s.count ? s[i + run] : " "
        let before: Character = i > 0 ? s[i - 1] : " "
        guard !after.isWhitespace else { return nil }
        if c == "_", before.isLetter || before.isNumber { return nil } // intraword _ is literal
        for n in stride(from: open, through: 1, by: -1) {
            let delim = String(repeating: c, count: n)
            // The closing delimiter: not preceded by whitespace, and for _
            // not followed by a letter or digit.
            var j = i + n
            while j < s.count {
                if s[j] == "\\" { j += 2; continue }
                if s[j] == "`", let skip = codeEnd(j) { j = skip; continue }
                if matches(delim, at: j), !s[j - 1].isWhitespace, j > i + n, j + n >= s.count || s[j + n] != c {
                    if c == "_", j + n < s.count, s[j + n].isLetter || s[j + n].isNumber { j += 1; continue }
                    let inner = Array(s[(i + n)..<j])
                    i = j + n
                    let kind = c == "~" ? "del" : (n == 2 ? "strong" : "em")
                    return ["t": .string(kind), "c": .array(InlineParser.sub(inner))]
                }
                j += 1
            }
        }
        return nil
    }

    func codeEnd(_ k: Int) -> Int? {
        let n = s[k...].prefix(while: { $0 == "`" }).count
        var j = k + n
        while j < s.count {
            if s[j] == "`" {
                let m = s[j...].prefix(while: { $0 == "`" }).count
                if m == n { return j + m }
                j += m
            } else { j += 1 }
        }
        return nil
    }
}
