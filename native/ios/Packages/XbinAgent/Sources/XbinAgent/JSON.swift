import Foundation

// JSONValue is the package's JSON: what an agent event's `data`, a tool's
// rawInput or an elicitation schema is before (and besides) its typed view.
// Two properties the wire needs and JSONDecoder does not give:
//
//  - object keys keep their order (an elicitation form lists its questions
//    in schema order; a tool's raw input shows as the agent wrote it), while
//    equality ignores order — key presence matters, order does not;
//  - booleans stay distinct from numbers (never round-tripped via NSNumber).
//
// JSONValue.parse is a small strict parser (RFC 8259) used for every
// event-bearing response; the Codable conformance exists for callers that
// persist values with their own coders (key order is then the coder's).

public enum JSONValue: Sendable, Hashable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object(JSONObject)
}

/// An order-preserving JSON object. Equality and hashing ignore key order.
public struct JSONObject: Sendable, Hashable, Sequence {
    public private(set) var keys: [String] = []
    private var storage: [String: JSONValue] = [:]

    public init() {}

    public init(_ pairs: [(String, JSONValue)]) {
        for (k, v) in pairs { self[k] = v }
    }

    public subscript(key: String) -> JSONValue? {
        get { storage[key] }
        set {
            if let newValue {
                if storage.updateValue(newValue, forKey: key) == nil { keys.append(key) }
            } else if storage.removeValue(forKey: key) != nil {
                keys.removeAll { $0 == key }
            }
        }
    }

    public var count: Int { keys.count }
    public var isEmpty: Bool { keys.isEmpty }

    /// The entries in their original order.
    public var entries: [(key: String, value: JSONValue)] { keys.map { ($0, storage[$0]!) } }

    public func makeIterator() -> IndexingIterator<[(key: String, value: JSONValue)]> { entries.makeIterator() }

    public static func == (a: JSONObject, b: JSONObject) -> Bool { a.storage == b.storage }
    public func hash(into h: inout Hasher) { h.combine(storage) }
}

// (deliberately not ExpressibleByNilLiteral: `nil` must always mean Optional.none)
extension JSONValue: ExpressibleByBooleanLiteral, ExpressibleByIntegerLiteral,
    ExpressibleByFloatLiteral, ExpressibleByStringLiteral, ExpressibleByArrayLiteral, ExpressibleByDictionaryLiteral
{
    public init(booleanLiteral v: Bool) { self = .bool(v) }
    public init(integerLiteral v: Int) { self = .number(Double(v)) }
    public init(floatLiteral v: Double) { self = .number(v) }
    public init(stringLiteral v: String) { self = .string(v) }
    public init(arrayLiteral vs: JSONValue...) { self = .array(vs) }
    public init(dictionaryLiteral pairs: (String, JSONValue)...) { self = .object(JSONObject(pairs)) }
}

// MARK: - Accessors (lenient: a wrong type reads as absent, like the daemon's
// per-key decoding — an unknown shape drops only itself)

extension JSONValue {
    public subscript(key: String) -> JSONValue? {
        if case .object(let o) = self { return o[key] }
        return nil
    }

    public var string: String? { if case .string(let s) = self { return s }; return nil }
    public var bool: Bool? { if case .bool(let b) = self { return b }; return nil }
    public var double: Double? { if case .number(let n) = self { return n }; return nil }
    public var array: [JSONValue]? { if case .array(let a) = self { return a }; return nil }
    public var object: JSONObject? { if case .object(let o) = self { return o }; return nil }
    public var isNull: Bool { if case .null = self { return true }; return false }

    /// An integral number as Int (nil for a fraction, a non-number or out of range).
    public var int: Int? {
        guard case .number(let n) = self, n.rounded() == n, n.magnitude < 9.2e18 else { return nil }
        return Int(n)
    }

    /// A non-negative integral number as UInt64.
    public var uint64: UInt64? {
        guard case .number(let n) = self, n.rounded() == n, n >= 0, n < 1.8e19 else { return nil }
        return UInt64(n)
    }

    /// Int64 (unix milliseconds and the like).
    public var int64: Int64? {
        guard case .number(let n) = self, n.rounded() == n, n.magnitude < 9.2e18 else { return nil }
        return Int64(n)
    }

    /// A string, or a number/bool rendered as the web would (`String(x)`).
    public var text: String? {
        switch self {
        case .string(let s): return s
        case .number: return compactString
        case .bool(let b): return b ? "true" : "false"
        default: return nil
        }
    }

    /// JavaScript truthiness (a non-empty string, a non-zero number, true, any object/array).
    public var truthy: Bool {
        switch self {
        case .null: return false
        case .bool(let b): return b
        case .number(let n): return n != 0 && !n.isNaN
        case .string(let s): return !s.isEmpty
        case .array, .object: return true
        }
    }
}

// MARK: - Parsing

public struct JSONParseError: Error, Equatable, CustomStringConvertible {
    public let offset: Int
    public let reason: String
    public var description: String { "JSON: \(reason) at byte \(offset)" }
}

extension JSONValue {
    /// Parses one JSON text (surrounding whitespace allowed).
    public static func parse(_ data: Data) throws -> JSONValue {
        try data.withUnsafeBytes { raw in
            var p = JSONParser(raw.bindMemory(to: UInt8.self))
            let v = try p.value(depth: 0)
            p.skipSpace()
            guard p.i == p.b.count else { throw p.fail("trailing characters") }
            return v
        }
    }

    public static func parse(_ text: String) throws -> JSONValue { try parse(Data(text.utf8)) }
}

private struct JSONParser {
    let b: UnsafeBufferPointer<UInt8>
    var i = 0
    init(_ b: UnsafeBufferPointer<UInt8>) { self.b = b }

    func fail(_ reason: String) -> JSONParseError { JSONParseError(offset: i, reason: reason) }

    mutating func skipSpace() {
        while i < b.count, b[i] == 0x20 || b[i] == 0x0A || b[i] == 0x0D || b[i] == 0x09 { i += 1 }
    }

    mutating func value(depth: Int) throws -> JSONValue {
        guard depth < 512 else { throw fail("nested too deeply") }
        skipSpace()
        guard i < b.count else { throw fail("unexpected end") }
        switch b[i] {
        case UInt8(ascii: "{"):
            i += 1
            var o = JSONObject()
            skipSpace()
            if i < b.count, b[i] == UInt8(ascii: "}") { i += 1; return .object(o) }
            while true {
                skipSpace()
                guard i < b.count, b[i] == UInt8(ascii: "\"") else { throw fail("expected a key") }
                let k = try string()
                skipSpace()
                guard i < b.count, b[i] == UInt8(ascii: ":") else { throw fail("expected ':'") }
                i += 1
                o[k] = try value(depth: depth + 1)
                skipSpace()
                guard i < b.count else { throw fail("unexpected end") }
                if b[i] == UInt8(ascii: ",") { i += 1; continue }
                if b[i] == UInt8(ascii: "}") { i += 1; return .object(o) }
                throw fail("expected ',' or '}'")
            }
        case UInt8(ascii: "["):
            i += 1
            var a: [JSONValue] = []
            skipSpace()
            if i < b.count, b[i] == UInt8(ascii: "]") { i += 1; return .array(a) }
            while true {
                a.append(try value(depth: depth + 1))
                skipSpace()
                guard i < b.count else { throw fail("unexpected end") }
                if b[i] == UInt8(ascii: ",") { i += 1; continue }
                if b[i] == UInt8(ascii: "]") { i += 1; return .array(a) }
                throw fail("expected ',' or ']'")
            }
        case UInt8(ascii: "\""):
            return .string(try string())
        case UInt8(ascii: "t"): try literal("true"); return .bool(true)
        case UInt8(ascii: "f"): try literal("false"); return .bool(false)
        case UInt8(ascii: "n"): try literal("null"); return .null
        default:
            return .number(try number())
        }
    }

    mutating func literal(_ word: String) throws {
        let w = Array(word.utf8)
        guard i + w.count <= b.count else { throw fail("unexpected end") }
        for k in 0..<w.count where b[i + k] != w[k] { throw fail("bad literal") }
        i += w.count
    }

    mutating func number() throws -> Double {
        let start = i
        if i < b.count, b[i] == UInt8(ascii: "-") { i += 1 }
        guard i < b.count, b[i] >= 0x30, b[i] <= 0x39 else { throw fail("bad number") }
        if b[i] == 0x30 { i += 1 } else { while i < b.count, b[i] >= 0x30, b[i] <= 0x39 { i += 1 } }
        if i < b.count, b[i] == UInt8(ascii: ".") {
            i += 1
            guard i < b.count, b[i] >= 0x30, b[i] <= 0x39 else { throw fail("bad fraction") }
            while i < b.count, b[i] >= 0x30, b[i] <= 0x39 { i += 1 }
        }
        if i < b.count, b[i] == UInt8(ascii: "e") || b[i] == UInt8(ascii: "E") {
            i += 1
            if i < b.count, b[i] == UInt8(ascii: "+") || b[i] == UInt8(ascii: "-") { i += 1 }
            guard i < b.count, b[i] >= 0x30, b[i] <= 0x39 else { throw fail("bad exponent") }
            while i < b.count, b[i] >= 0x30, b[i] <= 0x39 { i += 1 }
        }
        let s = String(decoding: UnsafeBufferPointer(rebasing: b[start..<i]), as: UTF8.self)
        guard let d = Double(s) else { throw fail("bad number") }
        return d
    }

    mutating func hex4() throws -> UInt32 {
        guard i + 4 <= b.count else { throw fail("bad \\u escape") }
        var v: UInt32 = 0
        for _ in 0..<4 {
            let c = b[i]
            let d: UInt32
            switch c {
            case 0x30...0x39: d = UInt32(c - 0x30)
            case 0x41...0x46: d = UInt32(c - 0x41 + 10)
            case 0x61...0x66: d = UInt32(c - 0x61 + 10)
            default: throw fail("bad \\u escape")
            }
            v = v << 4 | d
            i += 1
        }
        return v
    }

    mutating func string() throws -> String {
        i += 1 // the opening quote
        var out: [UInt8] = []
        var runStart = i
        while true {
            guard i < b.count else { throw fail("unterminated string") }
            let c = b[i]
            if c == UInt8(ascii: "\"") {
                out.append(contentsOf: UnsafeBufferPointer(rebasing: b[runStart..<i]))
                i += 1
                return String(decoding: out, as: UTF8.self)
            }
            if c < 0x20 { throw fail("control character in string") }
            if c != UInt8(ascii: "\\") { i += 1; continue }
            out.append(contentsOf: UnsafeBufferPointer(rebasing: b[runStart..<i]))
            i += 1
            guard i < b.count else { throw fail("unterminated escape") }
            let e = b[i]
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
                if scalar >= 0xD800, scalar < 0xDC00 { // a high surrogate: its pair follows
                    if i + 6 <= b.count, b[i] == UInt8(ascii: "\\"), b[i + 1] == UInt8(ascii: "u") {
                        let save = i
                        i += 2
                        let low = try hex4()
                        if low >= 0xDC00, low < 0xE000 {
                            scalar = 0x10000 + ((scalar - 0xD800) << 10) + (low - 0xDC00)
                        } else {
                            i = save
                            scalar = 0xFFFD
                        }
                    } else {
                        scalar = 0xFFFD
                    }
                } else if scalar >= 0xDC00, scalar < 0xE000 {
                    scalar = 0xFFFD // a lone low surrogate
                }
                out.append(contentsOf: Array(String(Character(Unicode.Scalar(scalar) ?? "\u{FFFD}")).utf8))
            default:
                throw fail("bad escape")
            }
            runStart = i
        }
    }
}

// MARK: - Serializing

extension JSONValue {
    /// Compact JSON, keys in their order. Integral numbers print without a
    /// fraction (as JavaScript's JSON.stringify does).
    public var compactString: String {
        var s = ""
        write(to: &s, indent: nil, level: 0)
        return s
    }

    /// Indented JSON (`JSON.stringify(v, null, indent)`).
    public func prettyString(indent: Int = 2) -> String {
        var s = ""
        write(to: &s, indent: String(repeating: " ", count: indent), level: 0)
        return s
    }

    public var data: Data { Data(compactString.utf8) }

    private func write(to s: inout String, indent: String?, level: Int) {
        switch self {
        case .null: s += "null"
        case .bool(let v): s += v ? "true" : "false"
        case .number(let n): s += JSONValue.format(n)
        case .string(let v): JSONValue.quote(v, into: &s)
        case .array(let a):
            if a.isEmpty { s += "[]"; return }
            s += "["
            for (k, v) in a.enumerated() {
                if k > 0 { s += "," }
                if let indent { s += "\n" + String(repeating: indent, count: level + 1) }
                v.write(to: &s, indent: indent, level: level + 1)
            }
            if let indent { s += "\n" + String(repeating: indent, count: level) }
            s += "]"
        case .object(let o):
            if o.isEmpty { s += "{}"; return }
            s += "{"
            for (k, e) in o.entries.enumerated() {
                if k > 0 { s += "," }
                if let indent { s += "\n" + String(repeating: indent, count: level + 1) }
                JSONValue.quote(e.key, into: &s)
                s += indent == nil ? ":" : ": "
                e.value.write(to: &s, indent: indent, level: level + 1)
            }
            if let indent { s += "\n" + String(repeating: indent, count: level) }
            s += "}"
        }
    }

    static func format(_ n: Double) -> String {
        if !n.isFinite { return "null" }
        if n.rounded() == n, n.magnitude < 1e15 { return String(Int64(n)) }
        return "\(n)"
    }

    static func quote(_ v: String, into s: inout String) {
        s += "\""
        for u in v.unicodeScalars {
            switch u {
            case "\"": s += "\\\""
            case "\\": s += "\\\\"
            case "\n": s += "\\n"
            case "\r": s += "\\r"
            case "\t": s += "\\t"
            case "\u{08}": s += "\\b"
            case "\u{0C}": s += "\\f"
            default:
                if u.value < 0x20 {
                    let h = String(u.value, radix: 16)
                    s += "\\u" + String(repeating: "0", count: 4 - h.count) + h
                } else {
                    s.unicodeScalars.append(u)
                }
            }
        }
        s += "\""
    }
}

// MARK: - Codable (for callers' own persistence)

extension JSONValue: Codable {
    public init(from decoder: any Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let n = try? c.decode(Double.self) { self = .number(n); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([JSONValue].self) { self = .array(a); return }
        if let o = try? c.decode([String: JSONValue].self) {
            self = .object(JSONObject(o.keys.sorted().map { ($0, o[$0]!) }))
            return
        }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "not a JSON value")
    }

    public func encode(to encoder: any Encoder) throws {
        switch self {
        case .null:
            var c = encoder.singleValueContainer()
            try c.encodeNil()
        case .bool(let b):
            var c = encoder.singleValueContainer()
            try c.encode(b)
        case .number(let n):
            var c = encoder.singleValueContainer()
            try c.encode(n)
        case .string(let s):
            var c = encoder.singleValueContainer()
            try c.encode(s)
        case .array(let a):
            var c = encoder.unkeyedContainer()
            for v in a { try c.encode(v) }
        case .object(let o):
            var c = encoder.container(keyedBy: AnyKey.self)
            for (k, v) in o.entries { try c.encode(v, forKey: AnyKey(k)) }
        }
    }
}

struct AnyKey: CodingKey {
    var stringValue: String
    var intValue: Int? { nil }
    init(_ s: String) { stringValue = s }
    init?(stringValue: String) { self.stringValue = stringValue }
    init?(intValue: Int) { return nil }
}

/// Types read leniently from a JSONValue (the wire's shapes, missing or
/// mistyped fields read as absent) — every event payload and API model.
public protocol JSONReadable {
    init?(json: JSONValue)
}

extension JSONValue {
    /// The array's elements that read as T (unreadable ones are skipped).
    func list<T: JSONReadable>(_: T.Type) -> [T]? {
        guard case .array(let a) = self else { return nil }
        return a.compactMap(T.init(json:))
    }
}

/// Codable through JSONValue: a JSONReadable model that also keeps its wire
/// form (`json`) encodes exactly what it read.
public protocol JSONBacked: JSONReadable, Codable {
    var json: JSONValue { get }
}

extension JSONBacked {
    public init(from decoder: any Decoder) throws {
        let v = try JSONValue(from: decoder)
        guard let s = Self(json: v) else {
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "unreadable \(Self.self)"))
        }
        self = s
    }

    public func encode(to encoder: any Encoder) throws { try json.encode(to: encoder) }
}
