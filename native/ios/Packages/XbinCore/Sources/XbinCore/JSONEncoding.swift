import Foundation

extension JSONValue {
    /// Canonical JSON: object keys sorted by their UTF-8 bytes (Go's
    /// `encoding/json` order), no whitespace, `.int` as a plain integer,
    /// `.double` in Swift's shortest round-trip form (always with a `.` or an
    /// exponent, so it parses back as `.double`), non-finite doubles as
    /// `null` (what `JSON.stringify` does). U+2028/U+2029 and control
    /// characters are escaped, so the output is also a valid JavaScript
    /// expression. The same value always encodes to the same bytes.
    public var jsonString: String {
        var out = ""
        write(to: &out, htmlSafe: false)
        return out
    }

    /// ``jsonString`` as UTF-8 bytes.
    public var jsonData: Data { Data(jsonString.utf8) }

    /// A JavaScript expression for this value, safe to splice into script
    /// source anywhere an expression may appear (a function argument, the
    /// right-hand side of an assignment). Identical to ``jsonString`` — JSON
    /// is a JavaScript expression once U+2028/U+2029 are escaped, which
    /// ``jsonString`` always does. With `htmlSafe`, `<`, `>` and `&` are
    /// escaped too, for source that ends up inside an HTML `<script>`.
    public func jsLiteral(htmlSafe: Bool = false) -> String {
        var out = ""
        write(to: &out, htmlSafe: htmlSafe)
        return out
    }

    /// An indented rendering for diagnostics and fixture diffs (two spaces,
    /// sorted keys). Not canonical — use ``jsonString`` on the wire.
    public var prettyJSONString: String {
        var out = ""
        writePretty(to: &out, indent: 0)
        return out
    }

    func write(to out: inout String, htmlSafe: Bool) {
        switch self {
        case .null: out += "null"
        case .bool(let b): out += b ? "true" : "false"
        case .int(let i): out += String(i)
        case .double(let d): out += Self.formatDouble(d)
        case .string(let s): Self.writeString(s, to: &out, htmlSafe: htmlSafe)
        case .array(let a):
            out += "["
            for (n, v) in a.enumerated() {
                if n > 0 { out += "," }
                v.write(to: &out, htmlSafe: htmlSafe)
            }
            out += "]"
        case .object(let o):
            out += "{"
            for (n, k) in Self.sortedKeys(o).enumerated() {
                if n > 0 { out += "," }
                Self.writeString(k, to: &out, htmlSafe: htmlSafe)
                out += ":"
                o[k]!.write(to: &out, htmlSafe: htmlSafe)
            }
            out += "}"
        }
    }

    func writePretty(to out: inout String, indent: Int) {
        let pad = String(repeating: "  ", count: indent + 1)
        let end = String(repeating: "  ", count: indent)
        switch self {
        case .array(let a) where !a.isEmpty:
            out += "[\n"
            for (n, v) in a.enumerated() {
                out += pad
                v.writePretty(to: &out, indent: indent + 1)
                out += n == a.count - 1 ? "\n" : ",\n"
            }
            out += end + "]"
        case .object(let o) where !o.isEmpty:
            out += "{\n"
            let keys = Self.sortedKeys(o)
            for (n, k) in keys.enumerated() {
                out += pad
                Self.writeString(k, to: &out, htmlSafe: false)
                out += ": "
                o[k]!.writePretty(to: &out, indent: indent + 1)
                out += n == keys.count - 1 ? "\n" : ",\n"
            }
            out += end + "}"
        default:
            write(to: &out, htmlSafe: false)
        }
    }

    /// Keys in UTF-8 byte order — deterministic everywhere, and the order
    /// Go's `encoding/json` uses for maps.
    static func sortedKeys(_ o: [String: JSONValue]) -> [String] {
        o.keys.sorted { $0.utf8.lexicographicallyPrecedes($1.utf8) }
    }

    static func formatDouble(_ d: Double) -> String {
        guard d.isFinite else { return "null" }
        // Swift's description is the shortest string that round-trips and
        // always carries a "." or an exponent ("1.0", "1e+16", "5e-324").
        return d.description
    }

    private static let hex: [Character] = Array("0123456789abcdef")

    static func writeString(_ s: String, to out: inout String, htmlSafe: Bool) {
        out += "\""
        for u in s.unicodeScalars {
            switch u {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            case "\u{08}": out += "\\b"
            case "\u{0C}": out += "\\f"
            case "\u{2028}": out += "\\u2028"
            case "\u{2029}": out += "\\u2029"
            case "<" where htmlSafe: out += "\\u003c"
            case ">" where htmlSafe: out += "\\u003e"
            case "&" where htmlSafe: out += "\\u0026"
            default:
                if u.value < 0x20 || u.value == 0x7F {
                    let v = Int(u.value)
                    out += "\\u00"
                    out.append(hex[v >> 4])
                    out.append(hex[v & 0xF])
                } else {
                    out.unicodeScalars.append(u)
                }
            }
        }
        out += "\""
    }
}
