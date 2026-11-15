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

// MARK: - Rev 2 (D233): choices as rows, "Something else", pages

/// A `question`'s rev-2 props: how its choices are drawn, which string
/// property is the free text riding on a choice, whether several questions
/// are paged, and the buttons' words. All absent: the rev-1 form.
public struct QuestionOptions: Sendable, Equatable {
    public enum Style: String, Sendable, CaseIterable {
        /// Menus for choices (rev 1).
        case form
        /// A property with choices draws them as full-width rows, one tap
        /// each.
        case rows
    }

    public var style: Style
    /// Choice property → the string property that is its free text
    /// ("Something else"): that one is not drawn as a field of its own.
    public var other: [String: String]
    /// Several questions: one a page ("1 of 2").
    public var paged: Bool
    /// The submit button's words (nil: ``submitTitle``'s default).
    public var submitLabel: String?
    /// The skip button's words (nil: "Skip"). Skipping is never an answer.
    public var skipLabel: String?

    public init(style: Style = .form, other: [String: String] = [:], paged: Bool = false, submitLabel: String? = nil,
                skipLabel: String? = nil) {
        self.style = style
        self.other = other
        self.paged = paged
        self.submitLabel = submitLabel
        self.skipLabel = skipLabel
    }

    /// From a `question` node's props, checked against its form: an
    /// `other` pair counts only when it names a choice field and a text
    /// field of the form, each text field riding on one choice at most.
    public init(props p: Props, form: QuestionForm) {
        var other: [String: String] = [:]
        var taken = Set<String>()
        for (choice, v) in (p.object("other") ?? [:]).sorted(by: { $0.key < $1.key }) {
            guard let text = v.stringValue, text != choice, !taken.contains(text),
                  let c = form.field(choice), case .choice = c.kind,
                  let t = form.field(text), case .text = t.kind else { continue }
            other[choice] = text
            taken.insert(text)
        }
        self.init(style: Style(rawValue: p.string("style") ?? "") ?? .form, other: other, paged: p.bool("paged"),
                  submitLabel: p.nonEmpty("submitLabel"), skipLabel: p.nonEmpty("skipLabel"))
    }

    /// The submit button: its label, else "Answer" for rows and "Submit"
    /// for a form (as rev 1 reads).
    public var submitTitle: String { submitLabel ?? (style == .rows ? "Answer" : "Submit") }
    public var skipTitle: String { skipLabel ?? "Skip" }
}

extension QuestionForm {
    public func field(_ name: String) -> QuestionField? { fields.first { $0.name == name } }

    /// ``content(_:)`` with rev 2's riders: for each `other` pair the
    /// answer carries the choice OR the text, under its own property name
    /// (typed text wins — the session clears one when the other is set, so
    /// both only meet in values a caller made by hand); a required choice
    /// is answered by either.
    public func content(_ values: [String: JSONValue], other: [String: String]) -> Result<JSONValue, QuestionForm.Missing> {
        guard !other.isEmpty else { return content(values) }
        let choiceOf = Dictionary(other.map { ($0.value, $0.key) }, uniquingKeysWith: { a, _ in a })
        var out: [String: JSONValue] = [:]
        var missing: [String] = []
        for f in fields {
            if let rider = other[f.name], let r = field(rider) {
                if r.normalize(Self.trimmed(values[rider])) != nil { continue } // the text answers it
                if let v = f.normalize(values[f.name]) { out[f.name] = v } else if f.required || r.required { missing.append(f.title) }
            } else if choiceOf[f.name] != nil {
                if let v = f.normalize(Self.trimmed(values[f.name])) { out[f.name] = v }
            } else if let v = f.normalize(values[f.name]) {
                out[f.name] = v
            } else if f.required {
                missing.append(f.title)
            }
        }
        if !missing.isEmpty { return .failure(Missing(titles: missing)) }
        return .success(.object(out))
    }

    /// A typed "Something else" with nothing but spaces is no answer.
    static func trimmed(_ v: JSONValue?) -> JSONValue? {
        guard case .string(let s)? = v else { return v }
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : .string(t)
    }
}

/// A question being answered: the values, the page in view, and the rules
/// between a choice and its "Something else" — choosing clears the text,
/// typing clears the choice. A value type the view keeps as its state.
public struct QuestionSession: Sendable, Equatable {
    public let form: QuestionForm
    public let options: QuestionOptions
    public private(set) var values: [String: JSONValue]
    /// The page in view (0 when not paged).
    public private(set) var page = 0

    public init(form: QuestionForm, options: QuestionOptions = QuestionOptions(), values: [String: JSONValue] = [:]) {
        self.form = form
        self.options = options
        self.values = values
    }

    /// The fields drawn, in order: every field but the riders.
    public var questions: [QuestionField] {
        let riders = Set(options.other.values)
        return form.fields.filter { !riders.contains($0.name) }
    }

    /// The free-text field riding on `field`'s choices, if any.
    public func rider(of field: QuestionField) -> QuestionField? {
        options.other[field.name].flatMap(form.field)
    }

    /// Paged, and more than one question to page through.
    public var isPaged: Bool { options.paged && questions.count > 1 }
    public var pageCount: Int { isPaged ? questions.count : 1 }
    /// The questions on the page in view.
    public var visible: [QuestionField] {
        let q = questions
        guard isPaged else { return q }
        return q.indices.contains(page) ? [q[page]] : []
    }

    public var isLastPage: Bool { page >= pageCount - 1 }
    public var canGoBack: Bool { isPaged && page > 0 }
    /// "1 of 2" — nil when there is one page.
    public var pageLabel: String? { isPaged ? "\(page + 1) of \(pageCount)" : nil }
    /// The primary button on this page: "Next" before the last, else the
    /// submit button's words.
    public var primaryTitle: String { isLastPage ? options.submitTitle : "Next" }

    public func value(_ name: String) -> JSONValue? { values[name] }
    /// The text typed in a rider or a text field.
    public func text(_ name: String) -> String { QuestionField.text(values[name]) }

    /// Whether `choice` is the one chosen for `field` (never while its
    /// "Something else" holds text).
    public func isChosen(_ field: QuestionField, _ choice: QuestionField.Choice) -> Bool {
        values[field.name] == choice.value
    }

    /// Chooses `value` for `name` (nil: no choice). Its "Something else"
    /// is cleared: the answer is one or the other.
    public mutating func choose(_ name: String, _ value: JSONValue?) {
        if let value, !value.isNull { values[name] = value } else { values[name] = nil }
        if let rider = options.other[name], values[name] != nil { values[rider] = nil }
    }

    /// Sets a field's value as typed or toggled. Text typed into a
    /// "Something else" clears the choice it rides on.
    public mutating func set(_ name: String, _ value: JSONValue?) {
        var v = value
        if case .string(let s)? = v, s.isEmpty { v = nil }
        if let v, !v.isNull { values[name] = v } else { values[name] = nil }
        if values[name] != nil, let choice = options.other.first(where: { $0.value == name })?.key { values[choice] = nil }
    }

    /// Whether `field` has an answer: its own value, or its rider's text.
    public func isAnswered(_ field: QuestionField) -> Bool {
        if field.normalize(values[field.name]) != nil { return true }
        if let r = rider(of: field), r.normalize(QuestionForm.trimmed(values[r.name])) != nil { return true }
        return false
    }

    /// The required questions on the page in view that have no answer.
    public var missingHere: QuestionForm.Missing? {
        let titles = visible.filter { f in (f.required || (rider(of: f)?.required ?? false)) && !isAnswered(f) }.map(\.title)
        return titles.isEmpty ? nil : QuestionForm.Missing(titles: titles)
    }

    /// To the next page; refuses (and says what is missing) while a
    /// required question on this page has no answer. Nothing on the last.
    @discardableResult
    public mutating func next() -> QuestionForm.Missing? {
        if let m = missingHere { return m }
        if !isLastPage { page += 1 }
        return nil
    }

    public mutating func back() { if canGoBack { page -= 1 } }

    /// The answer to submit (`submit {content}`), or what is missing.
    public func content() -> Result<JSONValue, QuestionForm.Missing> {
        form.content(values, other: options.other)
    }

    /// A settled question's answer for `field`, as one line: the chosen
    /// row's label, else what was typed.
    public func answerText(_ field: QuestionField) -> String? {
        if let v = values[field.name], !v.isNull {
            if case .choice(let cs) = field.kind, let c = cs.first(where: { $0.value == v }) { return c.label }
            return Props.text(v)
        }
        if let r = rider(of: field) { return Props.text(values[r.name]).flatMap { $0.isEmpty ? nil : $0 } }
        return nil
    }
}
