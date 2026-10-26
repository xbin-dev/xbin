import Foundation
import XbinCore

// Markdown copied as rich text (#34): a `button` with `copyFormat:
// "markdown"` (and the Agent tab's message Copy) puts two items on the
// pasteboard — HTML, so Notes, Mail, Pages or a document keep the headings,
// the list numbers, the table and the links, and the plain text beside it
// for anything that takes only text. Both are made from the same tokens the
// renderer draws (MarkdownLexer → ``Markdown/blocks(_:)``): every text is
// escaped, a link keeps only an http(s) or mailto address (as on screen),
// and nothing of the source passes through unescaped.

extension Markdown {
    /// The rich and the plain form of a Markdown source, for the pasteboard.
    public static func pasteboard(markdown source: String) -> (html: String, plain: String) {
        let b = blocks(MarkdownLexer.tokens(source))
        return (html(b), plainText(b))
    }

    /// The blocks as an HTML fragment (UTF-8 declared, for the apps that
    /// guess otherwise).
    public static func html(_ blocks: [MarkdownBlock]) -> String {
        "<meta charset=\"utf-8\">" + blocks.map(html).joined()
    }

    static func html(_ b: MarkdownBlock) -> String {
        switch b {
        case .heading(let level, let c):
            let l = min(max(level, 1), 6)
            return "<h\(l)>\(html(c))</h\(l)>"
        case .paragraph(let c):
            return "<p>\(html(c))</p>"
        case .list(let l):
            let tag = l.ordered ? "ol" : "ul"
            let start = l.ordered && l.start != 1 ? " start=\"\(l.start)\"" : ""
            let items = l.items.map { it -> String in
                let box = it.task ? (it.checked ? "☑ " : "☐ ") : ""
                // A tight item's single paragraph reads as the item's text.
                if !l.loose, it.blocks.count == 1, case .paragraph(let c) = it.blocks[0] {
                    return "<li>\(box)\(html(c))</li>"
                }
                return "<li>\(box)\(it.blocks.map(html).joined())</li>"
            }
            return "<\(tag)\(start)>\(items.joined())</\(tag)>"
        case .code(let text, _):
            return "<pre><code>\(escape(text))</code></pre>"
        case .quote(let bs):
            return "<blockquote>\(bs.map(html).joined())</blockquote>"
        case .table(let t):
            let cols = t.columns
            func cells(_ row: [[MarkdownInline]], _ tag: String) -> String {
                (0..<cols).map { i -> String in
                    let align = t.alignment(i).map { " style=\"text-align:\($0.rawValue)\"" } ?? ""
                    return "<\(tag)\(align)>\(i < row.count ? html(row[i]) : "")</\(tag)>"
                }.joined()
            }
            let head = "<thead><tr>\(cells(t.header, "th"))</tr></thead>"
            let body = t.rows.isEmpty ? "" : "<tbody>" + t.rows.map { "<tr>\(cells($0, "td"))</tr>" }.joined() + "</tbody>"
            return "<table>\(head)\(body)</table>"
        case .rule:
            return "<hr>"
        }
    }

    /// Inline tokens as HTML.
    public static func html(_ inlines: [MarkdownInline]) -> String {
        inlines.map { x -> String in
            switch x {
            case .text(let s): return escape(s)
            case .code(let s): return "<code>\(escape(s))</code>"
            case .strong(let c): return "<strong>\(html(c))</strong>"
            case .emphasis(let c): return "<em>\(html(c))</em>"
            case .strikethrough(let c): return "<del>\(html(c))</del>"
            case .link(let href, let c):
                guard isOpenable(href) else { return html(c) }
                return "<a href=\"\(escape(href))\">\(html(c))</a>"
            case .lineBreak: return "<br>"
            case .span(let c): return html(c)
            }
        }.joined()
    }

    /// Text safe inside an element or a double-quoted attribute.
    static func escape(_ s: String) -> String {
        var out = ""
        out.reserveCapacity(s.utf8.count)
        for ch in s {
            switch ch {
            case "&": out += "&amp;"
            case "<": out += "&lt;"
            case ">": out += "&gt;"
            case "\"": out += "&quot;"
            case "'": out += "&#39;"
            default: out.append(ch)
            }
        }
        return out
    }
}
