import Foundation

/// Why a JSON text was rejected, with the byte offset where it happened.
public struct JSONParseError: Error, Equatable, Sendable, CustomStringConvertible {
    public var offset: Int
    public var reason: String
    public var description: String { "JSON parse error at byte \(offset): \(reason)" }
}

extension JSONValue {
    /// The deepest nesting the parser accepts. The bridge carries tile-made
    /// JSON, so hostile input must not be able to exhaust the stack.
    public static let maxParseDepth = 512

    /// Parses one JSON text (RFC 8259, strict: no comments, no trailing
    /// commas, no NaN/Infinity, nothing after the value but whitespace).
    ///
    /// Numbers keep their kind: an integer literal that fits in `Int64`
    /// becomes `.int`, anything with a fraction or exponent (or too large)
    /// becomes `.double`. Duplicate object keys: the last one wins, as in
    /// JavaScript. A lone UTF-16 surrogate escape (which `JSON.stringify`
    /// emits for a lone surrogate) decodes to U+FFFD.
    public init(parsing text: String) throws {
        var text = text
        self = try text.withUTF8 { try Self.parse($0) }
    }

    /// Parses one JSON text from UTF-8 bytes; invalid UTF-8 is an error.
    public init(parsing data: Data) throws {
        self = try data.withUnsafeBytes { raw in try Self.parse(raw.bindMemory(to: UInt8.self)) }
    }

    /// Parses one JSON text from UTF-8 bytes; invalid UTF-8 is an error.
    public init(parsing bytes: [UInt8]) throws {
        self = try bytes.withUnsafeBufferPointer { try Self.parse($0) }
    }

    static func parse(_ buf: UnsafeBufferPointer<UInt8>) throws -> JSONValue {
        var p = JSONParser(buf: buf)
        p.skipWhitespace()
        let v = try p.value()
        p.skipWhitespace()
        if p.i != buf.count { throw p.fail("unexpected content after the value") }
        return v
    }
}

private struct JSONParser {
    let buf: UnsafeBufferPointer<UInt8>
    var i = 0

    init(buf: UnsafeBufferPointer<UInt8>) { self.buf = buf }

    func fail(_ reason: String, at offset: Int? = nil) -> JSONParseError {
        JSONParseError(offset: offset ?? i, reason: reason)
    }

    mutating func skipWhitespace() {
        while i < buf.count {
            switch buf[i] {
            case 0x20, 0x09, 0x0A, 0x0D: i += 1
            default: return
            }
        }
    }

    private enum Open { case array, object }

    /// One value, iteratively: the open containers live on the heap, not the
    /// call stack, so the nesting limit is the only bound — a thread's stack
    /// (512 KiB off the main thread on Apple platforms) never is.
    mutating func value() throws -> JSONValue {
        var open: [Open] = []               // innermost last
        var arrays: [[JSONValue]] = []      // one per open array
        var objects: [[String: JSONValue]] = [] // one per open object
        var keys: [String] = []             // the pending key of each open object
        while true {
            // A value starts at i (whitespace already skipped).
            guard i < buf.count else { throw fail("unexpected end of input") }
            var v: JSONValue
            switch buf[i] {
            case UInt8(ascii: "{"):
                if open.count + 1 > JSONValue.maxParseDepth { throw fail("nesting deeper than \(JSONValue.maxParseDepth)") }
                i += 1
                skipWhitespace()
                if i < buf.count, buf[i] == UInt8(ascii: "}") {
                    i += 1
                    v = .object([:])
                } else {
                    open.append(.object)
                    objects.append([:])
                    keys.append(try key())
                    skipWhitespace()
                    continue
                }
            case UInt8(ascii: "["):
                if open.count + 1 > JSONValue.maxParseDepth { throw fail("nesting deeper than \(JSONValue.maxParseDepth)") }
                i += 1
                skipWhitespace()
                if i < buf.count, buf[i] == UInt8(ascii: "]") {
                    i += 1
                    v = .array([])
                } else {
                    open.append(.array)
                    arrays.append([])
                    continue
                }
            case UInt8(ascii: "\""): v = .string(try string())
            case UInt8(ascii: "t"): try literal("true"); v = .bool(true)
            case UInt8(ascii: "f"): try literal("false"); v = .bool(false)
            case UInt8(ascii: "n"): try literal("null"); v = .null
            case UInt8(ascii: "-"), UInt8(ascii: "0")...UInt8(ascii: "9"): v = try number()
            default: throw fail("unexpected character")
            }
            // v is complete: store it in the innermost container, closing
            // containers for as long as they end here.
            ascend: while true {
                guard let top = open.last else { return v }
                skipWhitespace()
                switch top {
                case .array:
                    arrays[arrays.count - 1].append(v)
                    guard i < buf.count else { throw fail("unexpected end of input") }
                    if buf[i] == UInt8(ascii: ",") { i += 1; skipWhitespace(); break ascend }
                    if buf[i] == UInt8(ascii: "]") { i += 1; open.removeLast(); v = .array(arrays.removeLast()); continue }
                    throw fail("expected ',' or ']'")
                case .object:
                    objects[objects.count - 1][keys.removeLast()] = v
                    guard i < buf.count else { throw fail("unexpected end of input") }
                    if buf[i] == UInt8(ascii: ",") {
                        i += 1
                        keys.append(try key())
                        skipWhitespace()
                        break ascend
                    }
                    if buf[i] == UInt8(ascii: "}") { i += 1; open.removeLast(); v = .object(objects.removeLast()); continue }
                    throw fail("expected ',' or '}'")
                }
            }
        }
    }

    /// An object member's key and its ':' (leading whitespace skipped).
    mutating func key() throws -> String {
        skipWhitespace()
        guard i < buf.count, buf[i] == UInt8(ascii: "\"") else { throw fail("expected a string key") }
        let k = try string()
        skipWhitespace()
        guard i < buf.count, buf[i] == UInt8(ascii: ":") else { throw fail("expected ':'") }
        i += 1
        return k
    }

    mutating func literal(_ word: StaticString) throws {
        let n = word.utf8CodeUnitCount
        guard i + n <= buf.count else { throw fail("unexpected end of input") }
        let w = UnsafeBufferPointer(start: word.utf8Start, count: n)
        for j in 0..<n where buf[i + j] != w[j] { throw fail("invalid literal") }
        i += n
    }

    mutating func number() throws -> JSONValue {
        let start = i
        var integral = true
        if buf[i] == UInt8(ascii: "-") { i += 1 }
        guard i < buf.count, isDigit(buf[i]) else { throw fail("expected a digit") }
        if buf[i] == UInt8(ascii: "0") {
            i += 1
            if i < buf.count, isDigit(buf[i]) { throw fail("leading zero") }
        } else {
            while i < buf.count, isDigit(buf[i]) { i += 1 }
        }
        if i < buf.count, buf[i] == UInt8(ascii: ".") {
            integral = false
            i += 1
            guard i < buf.count, isDigit(buf[i]) else { throw fail("expected a digit after '.'") }
            while i < buf.count, isDigit(buf[i]) { i += 1 }
        }
        if i < buf.count, buf[i] == UInt8(ascii: "e") || buf[i] == UInt8(ascii: "E") {
            integral = false
            i += 1
            if i < buf.count, buf[i] == UInt8(ascii: "+") || buf[i] == UInt8(ascii: "-") { i += 1 }
            guard i < buf.count, isDigit(buf[i]) else { throw fail("expected a digit in the exponent") }
            while i < buf.count, isDigit(buf[i]) { i += 1 }
        }
        let text = String(decoding: UnsafeBufferPointer(rebasing: buf[start..<i]), as: UTF8.self)
        if integral, let n = Int64(text) { return .int(n) }
        guard let d = Double(text), d.isFinite else { throw fail("number out of range", at: start) }
        return .double(d)
    }

    func isDigit(_ b: UInt8) -> Bool { b >= UInt8(ascii: "0") && b <= UInt8(ascii: "9") }

    mutating func string() throws -> String {
        let open = i
        i += 1 // "
        // Fast path: no escapes.
        var j = i
        while j < buf.count {
            let b = buf[j]
            if b == UInt8(ascii: "\"") {
                let s = try utf8String(i..<j, at: open)
                i = j + 1
                return s
            }
            if b == UInt8(ascii: "\\") { break }
            if b < 0x20 { throw fail("control character in a string", at: j) }
            j += 1
        }
        var out = [UInt8]()
        out.append(contentsOf: UnsafeBufferPointer(rebasing: buf[i..<j]))
        i = j
        while true {
            guard i < buf.count else { throw fail("unterminated string", at: open) }
            let b = buf[i]
            if b == UInt8(ascii: "\"") { i += 1; break }
            if b < 0x20 { throw fail("control character in a string") }
            if b != UInt8(ascii: "\\") { out.append(b); i += 1; continue }
            i += 1
            guard i < buf.count else { throw fail("unterminated escape") }
            let e = buf[i]
            i += 1
            switch e {
            case UInt8(ascii: "\""): out.append(0x22)
            case UInt8(ascii: "\\"): out.append(0x5C)
            case UInt8(ascii: "/"): out.append(0x2F)
            case UInt8(ascii: "b"): out.append(0x08)
            case UInt8(ascii: "f"): out.append(0x0C)
            case UInt8(ascii: "n"): out.append(0x0A)
            case UInt8(ascii: "r"): out.append(0x0D)
            case UInt8(ascii: "t"): out.append(0x09)
            case UInt8(ascii: "u"):
                var scalar = try hex4()
                if (0xD800...0xDBFF).contains(scalar) {
                    // A high surrogate: combine with a following low one.
                    if i + 1 < buf.count, buf[i] == UInt8(ascii: "\\"), buf[i + 1] == UInt8(ascii: "u") {
                        let save = i
                        i += 2
                        let low = try hex4()
                        if (0xDC00...0xDFFF).contains(low) {
                            scalar = 0x10000 + ((scalar - 0xD800) << 10) + (low - 0xDC00)
                        } else {
                            scalar = 0xFFFD
                            i = save // re-read the second escape on its own
                        }
                    } else {
                        scalar = 0xFFFD
                    }
                } else if (0xDC00...0xDFFF).contains(scalar) {
                    scalar = 0xFFFD
                }
                appendUTF8(UnicodeScalar(scalar) ?? "\u{FFFD}", to: &out)
            default:
                throw fail("invalid escape", at: i - 1)
            }
        }
        guard let s = String(validating: out, as: UTF8.self) else { throw fail("invalid UTF-8 in a string", at: open) }
        return s
    }

    func utf8String(_ r: Range<Int>, at: Int) throws -> String {
        let slice = UnsafeBufferPointer(rebasing: buf[r])
        guard let s = String(validating: slice, as: UTF8.self) else { throw fail("invalid UTF-8 in a string", at: at) }
        return s
    }

    mutating func hex4() throws -> UInt32 {
        guard i + 4 <= buf.count else { throw fail("truncated \\u escape") }
        var v: UInt32 = 0
        for _ in 0..<4 {
            let b = buf[i]
            let d: UInt32
            switch b {
            case UInt8(ascii: "0")...UInt8(ascii: "9"): d = UInt32(b - UInt8(ascii: "0"))
            case UInt8(ascii: "a")...UInt8(ascii: "f"): d = UInt32(b - UInt8(ascii: "a") + 10)
            case UInt8(ascii: "A")...UInt8(ascii: "F"): d = UInt32(b - UInt8(ascii: "A") + 10)
            default: throw fail("invalid \\u escape")
            }
            v = v << 4 | d
            i += 1
        }
        return v
    }

    func appendUTF8(_ s: UnicodeScalar, to out: inout [UInt8]) {
        out.append(contentsOf: UTF8.encode(s)!)
    }
}
