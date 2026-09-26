import Foundation
import XbinCore

// Markdown tokens (native/spec/tree.md §11): the runtime lexes markdown with
// the vendored marked into a small block/inline subset; the app draws the
// tokens natively. Nothing here parses markup — a token's text is always
// shown verbatim. Unknown token types (a newer runtime) degrade to their
// text rather than failing.

/// An inline markdown token.
public indirect enum MarkdownInline: Sendable, Hashable {
    case text(String)
    case strong([MarkdownInline])
    case emphasis([MarkdownInline])
    case strikethrough([MarkdownInline])
    case code(String)
    /// `href` is http(s) or mailto (the runtime drops other schemes).
    case link(href: String, [MarkdownInline])
    case lineBreak
    /// Children with no styling of their own: a newer inline token, or a
    /// link the renderer won't open (the runtime keeps http(s)/mailto only).
    case span([MarkdownInline])

    /// The text without styling (accessibility, copying, tests).
    public var plainText: String {
        switch self {
        case .text(let s), .code(let s): return s
        case .strong(let c), .emphasis(let c), .strikethrough(let c), .link(_, let c), .span(let c):
            return MarkdownInline.plainText(c)
        case .lineBreak: return "\n"
        }
    }

    public static func plainText(_ inlines: [MarkdownInline]) -> String { inlines.map(\.plainText).joined() }
}

/// A block markdown token.
public indirect enum MarkdownBlock: Sendable, Hashable {
    case heading(level: Int, [MarkdownInline])
    case paragraph([MarkdownInline])
    case list(MarkdownList)
    case code(text: String, language: String?)
    case quote([MarkdownBlock])
    case table(MarkdownTable)
    case rule
}

public struct MarkdownList: Sendable, Hashable {
    public var ordered: Bool
    /// The first number of an ordered list.
    public var start: Int
    /// Loose lists put space between items.
    public var loose: Bool
    public var items: [MarkdownListItem]

    /// The marker of item `i`: `1.`, `2.` … for ordered lists, `•` else.
    public func marker(_ i: Int) -> String { ordered ? "\(start + i)." : "•" }
}

public struct MarkdownListItem: Sendable, Hashable {
    public var blocks: [MarkdownBlock]
    /// A task item (`- [ ]`); ``checked`` says whether it is ticked.
    public var task: Bool
    public var checked: Bool
}

public enum MarkdownAlignment: String, Sendable, Hashable {
    case left, center, right
}

public struct MarkdownTable: Sendable, Hashable {
    /// One per column; nil = default (leading).
    public var alignments: [MarkdownAlignment?]
    public var header: [[MarkdownInline]]
    public var rows: [[[MarkdownInline]]]

    /// The column count (the widest of header and rows).
    public var columns: Int { max(header.count, rows.map(\.count).max() ?? 0) }

    public func alignment(_ column: Int) -> MarkdownAlignment? {
        column < alignments.count ? alignments[column] : nil
    }
}

/// One styled run of inline text — what a `Text` span needs. Flattening
/// the inline tree into runs keeps the SwiftUI side a loop.
public struct MarkdownRun: Sendable, Hashable {
    public var text: String
    public var strong = false
    public var emphasis = false
    public var strikethrough = false
    public var code = false
    public var link: String?

    public init(text: String, strong: Bool = false, emphasis: Bool = false, strikethrough: Bool = false,
                code: Bool = false, link: String? = nil) {
        self.text = text
        self.strong = strong
        self.emphasis = emphasis
        self.strikethrough = strikethrough
        self.code = code
        self.link = link
    }

    /// Whether `other` has the same styling (so the two can merge).
    func sameStyle(_ other: MarkdownRun) -> Bool {
        strong == other.strong && emphasis == other.emphasis && strikethrough == other.strikethrough
            && code == other.code && link == other.link
    }
}

public enum Markdown {
    /// The blocks of a `tokens` prop (absent or malformed: none).
    public static func blocks(_ tokens: JSONValue?) -> [MarkdownBlock] {
        (tokens?.arrayValue ?? []).compactMap(block)
    }

    /// A `markdown` node's blocks: its `tokens`; without them (a runtime
    /// that didn't lex) its `source` as plain paragraphs — verbatim, never
    /// parsed here.
    public static func blocks(props: Props) -> [MarkdownBlock] {
        if props.has("tokens") { return blocks(props["tokens"]) }
        guard let source = props.string("source"), !source.isEmpty else { return [] }
        return source.components(separatedBy: "\n\n")
            .map { $0.trimmingCharacters(in: .newlines) }
            .filter { !$0.isEmpty }
            .map { .paragraph([.text($0)]) }
    }

    static func block(_ t: JSONValue) -> MarkdownBlock? {
        guard let o = t.objectValue else { return nil }
        switch o["t"]?.stringValue {
        case "heading":
            let depth = o["depth"]?.intValue.map { Int(max(1, min(6, $0))) } ?? 1
            return .heading(level: depth, inlines(o["c"]))
        case "paragraph":
            return .paragraph(inlines(o["c"]))
        case "list":
            let items: [MarkdownListItem] = (o["items"]?.arrayValue ?? []).compactMap { it in
                guard let io = it.objectValue else { return nil }
                return MarkdownListItem(blocks: blocks(io["c"]), task: io["task"]?.boolValue ?? false,
                                        checked: io["checked"]?.boolValue ?? false)
            }
            let ordered = o["ordered"]?.boolValue ?? false
            let start = o["start"]?.intValue.map { Int(clamping: $0) } ?? 1
            return .list(MarkdownList(ordered: ordered, start: start, loose: o["loose"]?.boolValue ?? false, items: items))
        case "code":
            let lang = o["lang"]?.stringValue
            return .code(text: o["text"]?.stringValue ?? "", language: (lang?.isEmpty ?? true) ? nil : lang)
        case "blockquote":
            return .quote(blocks(o["c"]))
        case "table":
            let align = (o["align"]?.arrayValue ?? []).map { MarkdownAlignment(rawValue: $0.stringValue ?? "") }
            let header = (o["header"]?.arrayValue ?? []).map { inlines($0) }
            let rows = (o["rows"]?.arrayValue ?? []).map { ($0.arrayValue ?? []).map { inlines($0) } }
            return .table(MarkdownTable(alignments: align, header: header, rows: rows))
        case "hr":
            return .rule
        default:
            // A newer block: its text, or its inline children, as a paragraph.
            if let s = o["text"]?.stringValue { return .paragraph([.text(s)]) }
            if o["c"] != nil {
                let c = inlines(o["c"])
                return c.isEmpty ? nil : .paragraph(c)
            }
            return nil
        }
    }

    /// Inline tokens (absent or malformed: none).
    public static func inlines(_ tokens: JSONValue?) -> [MarkdownInline] {
        (tokens?.arrayValue ?? []).compactMap(inline)
    }

    static func inline(_ t: JSONValue) -> MarkdownInline? {
        guard let o = t.objectValue else { return nil }
        switch o["t"]?.stringValue {
        case "text": return .text(o["text"]?.stringValue ?? "")
        case "strong": return .strong(inlines(o["c"]))
        case "em": return .emphasis(inlines(o["c"]))
        case "del": return .strikethrough(inlines(o["c"]))
        case "codespan": return .code(o["text"]?.stringValue ?? "")
        case "link":
            let c = inlines(o["c"])
            guard let href = o["href"]?.stringValue, isOpenable(href) else { return .span(c) }
            return .link(href: href, c)
        case "br": return .lineBreak
        default:
            if let s = o["text"]?.stringValue { return .text(s) }
            let c = inlines(o["c"])
            return c.isEmpty ? nil : .span(c)
        }
    }

    /// Schemes a link may carry (tree.md §11); anything else shows as text.
    public static func isOpenable(_ href: String) -> Bool {
        let h = href.lowercased()
        return h.hasPrefix("https:") || h.hasPrefix("http:") || h.hasPrefix("mailto:")
    }

    /// Flattens inlines into styled runs, merging neighbours of one style.
    public static func runs(_ inlines: [MarkdownInline]) -> [MarkdownRun] {
        var out: [MarkdownRun] = []
        func add(_ r: MarkdownRun) {
            guard !r.text.isEmpty else { return }
            if let last = out.last, last.sameStyle(r) {
                out[out.count - 1].text += r.text
            } else {
                out.append(r)
            }
        }
        func walk(_ xs: [MarkdownInline], _ style: MarkdownRun) {
            for x in xs {
                var s = style
                switch x {
                case .text(let t):
                    s.text = t
                    add(s)
                case .code(let t):
                    s.text = t
                    s.code = true
                    add(s)
                case .lineBreak:
                    s.text = "\n"
                    add(s)
                case .strong(let c):
                    s.strong = true
                    walk(c, s)
                case .emphasis(let c):
                    s.emphasis = true
                    walk(c, s)
                case .strikethrough(let c):
                    s.strikethrough = true
                    walk(c, s)
                case .link(let href, let c):
                    s.link = href
                    walk(c, s)
                case .span(let c):
                    walk(c, s)
                }
            }
        }
        walk(inlines, MarkdownRun(text: ""))
        return out
    }

    /// The whole document as plain text, blocks separated by blank lines
    /// (for copying and accessibility).
    public static func plainText(_ blocks: [MarkdownBlock]) -> String {
        blocks.map { b -> String in
            switch b {
            case .heading(_, let c), .paragraph(let c): return MarkdownInline.plainText(c)
            case .list(let l):
                return l.items.enumerated().map { i, it in "\(l.marker(i)) " + plainText(it.blocks) }.joined(separator: "\n")
            case .code(let t, _): return t
            case .quote(let bs): return plainText(bs)
            case .table(let t):
                return ([t.header] + t.rows).map { $0.map(MarkdownInline.plainText).joined(separator: "\t") }.joined(separator: "\n")
            case .rule: return "—"
            }
        }.joined(separator: "\n\n")
    }
}
