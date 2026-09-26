import Foundation

// A question the agent asks (elicitation.request, ACP form mode — Claude's
// AskUserQuestion, an MCP server's form) as a native form: the port of
// web/agent-tools.js formFields/formContent/missingRequired. The schema is a
// flat JSON Schema object; properties are read in schema order.

public enum FormFieldKind: String, Sendable, Hashable {
    /// A single choice (`oneOf`/`enum`).
    case radio
    /// A multi-choice (an array of `anyOf`/`enum` items).
    case check
    case bool
    /// number or integer.
    case number
    case text
}

public struct FormOption: Sendable, Hashable, Identifiable {
    /// What the answer carries (usually a string).
    public var value: JSONValue
    public var title: String
    public var description: String
    public var id: String { value.compactString }
}

public struct FormField: Sendable, Hashable, Identifiable {
    public var key: String
    public var kind: FormFieldKind
    public var title: String
    public var description: String
    public var required: Bool
    public var options: [FormOption]
    /// The key of this question's free-text "Other" answer, when it has one.
    public var other: String?
    public var otherHint: String
    public var id: String { key }

    /// Reads a schema into fields, in property order. A text field marked as
    /// another question's custom answer (`_meta._askUserQuestionCustomAnswer
    /// .questionId`) rides on that question as `other`.
    public static func fields(schema: JSONValue?) -> [FormField] {
        guard let props = schema?["properties"]?.object else { return [] }
        let required = Set((schema?["required"]?.array ?? []).compactMap(\.string))
        func opts(_ list: JSONValue?) -> [FormOption] {
            (list?.array ?? []).map { o in
                if o.object != nil {
                    let v = ToolReading.nonNull(o["const"]) ?? ToolReading.nonNull(o["value"]) ?? ToolReading.nonNull(o["title"]) ?? .null
                    let title = o["title"]?.text ?? (ToolReading.nonNull(o["const"])?.text ?? "")
                    return FormOption(value: v, title: title, description: o["description"]?.text ?? "")
                }
                return FormOption(value: o, title: o.text ?? o.compactString, description: "")
            }
        }
        var fields: [FormField] = []
        var others: [(qid: String, key: String, p: JSONValue)] = []
        for (key, p) in props {
            guard p.object != nil else { continue }
            if let qid = p["_meta"]?["_askUserQuestionCustomAnswer"]?["questionId"]?.string, !qid.isEmpty {
                others.append((qid, key, p))
                continue
            }
            var f = FormField(key: key, kind: .text, title: p["title"]?.text ?? "", description: p["description"]?.text ?? "",
                              required: required.contains(key), options: [], other: nil, otherHint: "")
            if p["type"]?.string == "array" {
                f.kind = .check
                let items = p["items"]
                f.options = opts(items?["anyOf"] ?? items?["oneOf"] ?? items?["enum"])
            } else if let list = ToolReading.nonNull(p["oneOf"]) ?? ToolReading.nonNull(p["enum"]) {
                f.kind = .radio
                f.options = opts(list)
            } else if p["type"]?.string == "boolean" {
                f.kind = .bool
            } else if p["type"]?.string == "number" || p["type"]?.string == "integer" {
                f.kind = .number
            }
            fields.append(f)
        }
        for o in others {
            if let i = fields.firstIndex(where: { $0.key == o.qid }) {
                fields[i].other = o.key
                fields[i].otherHint = o.p["description"]?.text ?? ""
            } else {
                fields.append(FormField(key: o.key, kind: .text, title: o.p["title"]?.text ?? "Other", description: o.p["description"]?.text ?? "",
                                        required: required.contains(o.key), options: [], other: nil, otherHint: ""))
            }
        }
        return fields
    }

    /// An accept's content from the form's values (keyed by field key and
    /// `other` key): empty answers are left out; numbers are parsed; an
    /// "Other" text is trimmed.
    public static func content(_ fields: [FormField], values: [String: JSONValue]) -> JSONValue {
        var out = JSONObject()
        for f in fields {
            let x = values[f.key]
            switch f.kind {
            case .check:
                if let a = x?.array, !a.isEmpty { out[f.key] = .array(a) }
            case .bool:
                if let b = x?.bool { out[f.key] = .bool(b) }
            case .number:
                if let n = x?.double {
                    out[f.key] = .number(n)
                } else if let s = x?.string, !trim(s).isEmpty, let n = Double(trim(s)) {
                    out[f.key] = .number(n)
                }
            case .radio, .text:
                if let x, !x.isNull, !trim(x.text ?? x.compactString).isEmpty { out[f.key] = x }
            }
            if let ok = f.other, let v = values[ok]?.text, !trim(v).isEmpty { out[ok] = .string(trim(v)) }
        }
        return .object(out)
    }

    /// The required fields a content leaves out (their titles, else keys).
    public static func missingRequired(_ fields: [FormField], content: JSONValue) -> [String] {
        fields.filter { $0.required && content[$0.key] == nil }.map { $0.title.isEmpty ? $0.key : $0.title }
    }

    /// A settled card's "question: answer" line for this field ("" when unanswered).
    public func answer(in content: JSONValue?) -> String {
        guard let c = content else { return "" }
        var parts: [String] = []
        if let v = c[key], !v.isNull {
            if let a = v.array { parts.append(a.map { $0.text ?? $0.compactString }.joined(separator: ", ")) } else { parts.append(v.text ?? v.compactString) }
        }
        if let ok = other, let o = c[ok]?.text { parts.append(o) }
        return parts.filter { !$0.isEmpty }.joined(separator: " — ")
    }

    /// The label a settled answer line uses.
    public var answerLabel: String { !title.isEmpty ? title : !description.isEmpty ? description : key }
}
