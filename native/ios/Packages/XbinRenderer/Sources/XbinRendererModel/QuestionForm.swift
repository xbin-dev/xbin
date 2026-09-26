import Foundation
import XbinCore

/// A question's form from a **flat** JSON Schema (an object whose
/// properties are strings, numbers, integers or booleans, possibly with
/// `enum`/`oneOf` choices) — the ACP elicitation shape and the `question`
/// primitive's `schema` (plans/native.md §8.4, §13). The rules match the
/// reference renderer's `qFields` so a form reads the same on both.
public struct QuestionForm: Sendable, Equatable {
    public var title: String?
    public var description: String?
    public var fields: [QuestionField]

    public init(title: String? = nil, description: String? = nil, fields: [QuestionField]) {
        self.title = title
        self.description = description
        self.fields = fields
    }

    /// Reads `schema`: fields in the order of `properties` — JSON objects are
    /// unordered, so the order comes from `x-order`/`propertyOrder` when
    /// given, else required fields first, then by name.
    public init(schema: JSONValue?) {
        let s = schema?.objectValue ?? [:]
        title = s["title"]?.stringValue
        description = s["description"]?.stringValue
        let props = s["properties"]?.objectValue ?? [:]
        let required = Set((s["required"]?.arrayValue ?? []).compactMap(\.stringValue))
        var order = (s["x-order"]?.arrayValue ?? s["propertyOrder"]?.arrayValue ?? []).compactMap(\.stringValue)
        order = order.filter { props[$0] != nil }
        let rest = props.keys.filter { !order.contains($0) }.sorted { a, b in
            let ra = required.contains(a), rb = required.contains(b)
            return ra != rb ? ra : a < b
        }
        fields = (order + rest).map { QuestionField(name: $0, schema: props[$0] ?? [:], required: required.contains($0)) }
    }

    /// The initial values: each field's `default`, when it has one.
    public var defaults: [String: JSONValue] {
        var v: [String: JSONValue] = [:]
        for f in fields { if let d = f.defaultValue { v[f.name] = d } }
        return v
    }

    /// The answer to submit (`submit {content}`), or the titles of the
    /// required fields still empty. Empty values are left out; numbers are
    /// converted from the text typed.
    public func content(_ values: [String: JSONValue]) -> Result<JSONValue, QuestionForm.Missing> {
        var out: [String: JSONValue] = [:]
        var missing: [String] = []
        for f in fields {
            let v = f.normalize(values[f.name])
            if let v { out[f.name] = v } else if f.required { missing.append(f.title) }
        }
        if !missing.isEmpty { return .failure(Missing(titles: missing)) }
        return .success(.object(out))
    }

    public struct Missing: Error, Sendable, Equatable {
        public var titles: [String]
        /// "Required: Cluster, Audience".
        public var message: String { "Required: " + titles.joined(separator: ", ") }
    }
}

/// One field of a ``QuestionForm``.
public struct QuestionField: Sendable, Equatable, Identifiable {
    public enum Kind: Sendable, Equatable {
        /// Free text; `format` is the schema's (`email`, `uri`, `date`, …).
        case text(format: String?)
        case number(integer: Bool)
        case boolean
        /// A fixed set of values (`enum`, or `oneOf` with `const`s).
        case choice([Choice])
    }

    public struct Choice: Sendable, Equatable, Hashable {
        public var value: JSONValue
        public var label: String
    }

    public var name: String
    public var title: String
    public var description: String?
    public var required: Bool
    public var kind: Kind
    public var defaultValue: JSONValue?

    public var id: String { name }

    public init(name: String, schema: JSONValue, required: Bool) {
        let s = schema.objectValue ?? [:]
        self.name = name
        self.required = required
        title = s["title"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 } ?? name
        description = s["description"]?.stringValue
        defaultValue = s["default"].flatMap { $0.isNull ? nil : $0 }
        let type = s["type"]?.stringValue ?? "string"
        if let e = s["enum"]?.arrayValue {
            let names = s["enumNames"]?.arrayValue ?? []
            kind = .choice(e.enumerated().map { i, v in
                Choice(value: v, label: (i < names.count ? Props.text(names[i]) : nil) ?? Props.text(v) ?? "")
            })
        } else if let one = s["oneOf"]?.arrayValue ?? s["anyOf"]?.arrayValue,
                  case let cs = one.compactMap({ o -> Choice? in
                      guard let c = o["const"] else { return nil }
                      return Choice(value: c, label: o["title"]?.stringValue ?? Props.text(c) ?? "")
                  }), !cs.isEmpty {
            kind = .choice(cs)
        } else if type == "boolean" {
            kind = .boolean
        } else if type == "number" || type == "integer" {
            kind = .number(integer: type == "integer")
        } else {
            kind = .text(format: s["format"]?.stringValue)
        }
    }

    /// A value as submitted: nil when empty; number fields parse their text
    /// (a text that isn't a number is kept as typed, like the reference).
    public func normalize(_ v: JSONValue?) -> JSONValue? {
        guard let v, !v.isNull else { return nil }
        if case .string(let s) = v, s.isEmpty { return nil }
        if case .number(let integer) = kind, case .string(let s) = v {
            let t = s.trimmingCharacters(in: .whitespaces)
            if integer, let i = Int64(t) { return .int(i) }
            if let d = Double(t), d.isFinite {
                if d.rounded() == d, abs(d) < 9e15 { return .int(Int64(d)) }
                return .double(d)
            }
        }
        return v
    }

    /// The text a value shows in a text or number field.
    public static func text(_ v: JSONValue?) -> String { Props.text(v) ?? "" }
}
