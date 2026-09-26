#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

/// A question as a native form built from a flat JSON Schema
/// (``QuestionForm``): toggles for booleans, menus for choices, text and
/// number fields; required fields are checked before `onSubmit` gets the
/// answer object. Settled: read-only with what was answered.
public struct QuestionView: View {
    public let question: ChatQuestion
    public var onSubmit: @MainActor (JSONValue) -> Void
    public var onSkip: (@MainActor () -> Void)?
    @State private var values: [String: JSONValue]
    @State private var error: String?

    public init(question: ChatQuestion, onSubmit: @escaping @MainActor (JSONValue) -> Void,
                onSkip: (@MainActor () -> Void)? = nil) {
        self.question = question
        self.onSubmit = onSubmit
        self.onSkip = onSkip
        _values = State(initialValue: question.initialValues)
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: question.settled ? XbinIcons.UI.answered : XbinIcons.UI.question)
                    .foregroundStyle(question.settled ? XbinColor.tone(.ok) : XbinColor.accentText)
                Text(verbatim: question.title).font(.headline)
            }
            if let d = question.form.description {
                Text(verbatim: d).font(.subheadline).foregroundStyle(XbinColor.muted)
            }
            ForEach(question.form.fields) { field in
                QuestionFieldRow(field: field, value: binding(field.name))
            }
            .disabled(question.settled)
            if let error {
                Text(verbatim: error).font(.footnote).foregroundStyle(XbinColor.toneText(.danger))
            }
            if question.settled {
                Label("Answered", systemImage: XbinIcons.UI.checkmark)
                    .font(.subheadline)
                    .foregroundStyle(XbinColor.muted)
            } else {
                HStack {
                    if let onSkip {
                        Button("Skip") { onSkip() }.buttonStyle(.bordered)
                    }
                    Spacer()
                    Button("Submit") { submit() }
                        .buttonStyle(.borderedProminent)
                        .tint(XbinColor.tint)
                        .foregroundStyle(XbinColor.onTint)
                }
                .controlSize(.large)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ChatCard())
        .onChange(of: question.settled) { values = question.initialValues }
    }

    private func binding(_ name: String) -> Binding<JSONValue?> {
        mainBinding(get: { values[name] }, set: { v in
            values[name] = v
            error = nil
        })
    }

    private func submit() {
        switch question.form.content(values) {
        case .success(let content):
            error = nil
            onSubmit(content)
        case .failure(let missing):
            error = missing.message
        }
    }
}

/// One field of a question form.
private struct QuestionFieldRow: View {
    let field: QuestionField
    let value: Binding<JSONValue?>

    var body: some View {
        switch field.kind {
        case .boolean:
            Toggle(isOn: mainBinding(get: { value.wrappedValue?.boolValue ?? false },
                                 set: { value.wrappedValue = .bool($0) })) {
                title
            }
        case .choice(let choices):
            VStack(alignment: .leading, spacing: 6) {
                title
                Picker(selection: mainBinding(get: { value.wrappedValue ?? .null },
                                                     set: { value.wrappedValue = $0.isNull ? nil : $0 })) {
                    Text("Choose…").tag(JSONValue.null)
                    ForEach(choices, id: \.self) { c in Text(verbatim: c.label).tag(c.value) }
                } label: {
                    Text(verbatim: field.title)
                }
                .pickerStyle(.menu)
                .labelsHidden()
                description
            }
        case .number(let integer):
            VStack(alignment: .leading, spacing: 6) {
                title
                TextField(text: text, prompt: nil) { Text(verbatim: field.title) }
                    .keyboardType(integer ? .numberPad : .decimalPad)
                    .padding(10)
                    .background(XbinColor.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                description
            }
        case .text(let format):
            VStack(alignment: .leading, spacing: 6) {
                title
                TextField(text: text, prompt: nil) { Text(verbatim: field.title) }
                    .keyboardType(format == "email" ? .emailAddress : format == "uri" ? .URL : .default)
                    .textInputAutocapitalization(format == nil ? .sentences : .never)
                    .padding(10)
                    .background(XbinColor.fill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                description
            }
        }
    }

    private var text: Binding<String> {
        mainBinding(get: { QuestionField.text(value.wrappedValue) },
                set: { value.wrappedValue = $0.isEmpty ? nil : .string($0) })
    }

    private var title: some View {
        HStack(spacing: 2) {
            Text(verbatim: field.title)
            if field.required { Text(verbatim: "*").foregroundStyle(XbinColor.toneText(.danger)) }
        }
        .font(.subheadline.weight(.medium))
    }

    @ViewBuilder
    private var description: some View {
        if let d = field.description {
            Text(verbatim: d).font(.footnote).foregroundStyle(XbinColor.muted)
        }
    }
}
#endif
