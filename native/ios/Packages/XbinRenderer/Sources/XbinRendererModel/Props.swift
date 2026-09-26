import Foundation
import XbinCore

/// Typed, lenient reads of a node's props. The runtime validates props
/// before sending them (a wrong type is dropped with a diagnostic), so the
/// renderer only has to be robust: a missing or ill-typed prop reads as
/// absent and the view draws its default.
public struct Props: Sendable, Equatable {
    public var raw: [String: JSONValue]

    public init(_ raw: [String: JSONValue]) { self.raw = raw }

    public subscript(_ name: String) -> JSONValue? { raw[name] }

    public func has(_ name: String) -> Bool { raw[name] != nil }

    /// A string prop. Numbers read as their decimal text (the runtime
    /// already coerces them; this keeps an older runtime's tree readable).
    public func string(_ name: String) -> String? { Props.text(raw[name]) }

    /// A non-empty string prop.
    public func nonEmpty(_ name: String) -> String? {
        guard let s = string(name), !s.isEmpty else { return nil }
        return s
    }

    /// `true` only for a JSON `true`.
    public func bool(_ name: String) -> Bool { raw[name]?.boolValue ?? false }

    /// A bool that may be absent (nil) — for props whose absence means
    /// something other than false.
    public func optionalBool(_ name: String) -> Bool? { raw[name]?.boolValue }

    /// A finite number.
    public func number(_ name: String) -> Double? {
        guard let d = raw[name]?.doubleValue, d.isFinite else { return nil }
        return d
    }

    public func array(_ name: String) -> [JSONValue] { raw[name]?.arrayValue ?? [] }

    /// The objects of an array prop (non-objects skipped).
    public func objects(_ name: String) -> [[String: JSONValue]] { array(name).compactMap(\.objectValue) }

    public func object(_ name: String) -> [String: JSONValue]? { raw[name]?.objectValue }

    public func tone(_ name: String = "tone") -> XbinTone? { XbinTone(string(name)) }

    /// A JSON value as display text: strings verbatim, numbers without a
    /// trailing `.0`, booleans as `true`/`false`; nil for null, arrays and
    /// objects.
    public static func text(_ v: JSONValue?) -> String? {
        switch v {
        case .string(let s)?: return s
        case .int(let i)?: return String(i)
        case .double(let d)?: return number(d)
        case .bool(let b)?: return b ? "true" : "false"
        default: return nil
        }
    }

    /// A double as JavaScript's `String(n)` would print it for ordinary
    /// values (`42`, `0.5`, `-3.25`).
    public static func number(_ d: Double) -> String {
        guard d.isFinite else { return "" }
        if d.rounded() == d, abs(d) < 1e15 { return String(Int64(d)) }
        return String(d)
    }
}
