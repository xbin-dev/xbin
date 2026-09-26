import Foundation

/// A JSON value that keeps what Foundation's bridging loses: booleans are
/// never numbers, and an integer literal (`42`) stays distinct from a
/// fractional one (`42.5`, `1e3`). Objects are unordered — key order never
/// matters, key presence does (`{"a":null}` is not `{}`).
///
/// Parse with ``JSONValue/init(parsing:)-(String)`` (the lossless path — use it for
/// everything that crosses the runtime bridge); encode with
/// ``JSONValue/jsonString`` (canonical: sorted keys, no whitespace, the same
/// bytes for the same value on every platform) or ``JSONValue/jsLiteral``
/// (the same, safe to splice into JavaScript source).
public enum JSONValue: Sendable, Hashable {
    case null
    case bool(Bool)
    /// An integer literal that fits in 64 bits.
    case int(Int64)
    /// Any other number (a fraction, an exponent, or an integer too large
    /// for `Int64`). Never NaN or infinite when parsed.
    case double(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])
}

// MARK: - Accessors

extension JSONValue {
    public var isNull: Bool { if case .null = self { return true } else { return false } }

    /// The boolean, only for `true`/`false` (never `0`/`1`).
    public var boolValue: Bool? { if case .bool(let b) = self { return b } else { return nil } }

    public var stringValue: String? { if case .string(let s) = self { return s } else { return nil } }

    /// The integer: an `.int`, or a `.double` with no fractional part that
    /// fits in `Int64`.
    public var intValue: Int64? {
        switch self {
        case .int(let i): return i
        case .double(let d):
            guard d.isFinite, d.rounded(.towardZero) == d,
                  d >= -9_223_372_036_854_775_808.0, d < 9_223_372_036_854_775_808.0 else { return nil }
            return Int64(d)
        default: return nil
        }
    }

    /// The number as a `Double`, for either numeric case.
    public var doubleValue: Double? {
        switch self {
        case .int(let i): return Double(i)
        case .double(let d): return d
        default: return nil
        }
    }

    public var isNumber: Bool {
        switch self {
        case .int, .double: return true
        default: return false
        }
    }

    public var arrayValue: [JSONValue]? { if case .array(let a) = self { return a } else { return nil } }

    public var objectValue: [String: JSONValue]? { if case .object(let o) = self { return o } else { return nil } }

    /// `object[key]`; nil when this is not an object or the key is absent.
    public subscript(key: String) -> JSONValue? {
        if case .object(let o) = self { return o[key] }
        return nil
    }

    /// `array[index]`; nil when this is not an array or out of range.
    public subscript(index: Int) -> JSONValue? {
        if case .array(let a) = self, a.indices.contains(index) { return a[index] }
        return nil
    }

    /// A short name for the case, for diagnostics ("object", "string", …).
    public var kindName: String {
        switch self {
        case .null: return "null"
        case .bool: return "boolean"
        case .int: return "integer"
        case .double: return "number"
        case .string: return "string"
        case .array: return "array"
        case .object: return "object"
        }
    }
}

// MARK: - Literals

// No ExpressibleByNilLiteral on purpose: with it, `dict[k] = cond ? nil : v`
// would store `.null` instead of removing the key. Write `.null`.

extension JSONValue: ExpressibleByBooleanLiteral,
    ExpressibleByIntegerLiteral, ExpressibleByFloatLiteral, ExpressibleByStringLiteral,
    ExpressibleByArrayLiteral, ExpressibleByDictionaryLiteral
{
    public init(booleanLiteral value: Bool) { self = .bool(value) }
    public init(integerLiteral value: Int64) { self = .int(value) }
    public init(floatLiteral value: Double) { self = .double(value) }
    public init(stringLiteral value: String) { self = .string(value) }
    public init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
    public init(dictionaryLiteral elements: (String, JSONValue)...) {
        var o: [String: JSONValue] = [:]
        for (k, v) in elements { o[k] = v }
        self = .object(o)
    }
}

// MARK: - Codable

/// Codable conformance, for embedding JSON values in Codable records (the
/// workspace record, wire types). It goes through the container's own
/// number handling, so `JSONDecoder` may read `1.0` as `.int(1)`; the bridge
/// and the fixtures use ``JSONValue/init(parsing:)-(String)``, which never does.
extension JSONValue: Codable {
    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let i = try? c.decode(Int64.self) { self = .int(i); return }
        if let d = try? c.decode(Double.self) { self = .double(d); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([JSONValue].self) { self = .array(a); return }
        if let o = try? c.decode([String: JSONValue].self) { self = .object(o); return }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "not a JSON value")
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .int(let i): try c.encode(i)
        case .double(let d): if d.isFinite { try c.encode(d) } else { try c.encodeNil() }
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }
}

extension JSONValue: CustomStringConvertible {
    public var description: String { jsonString }
}
