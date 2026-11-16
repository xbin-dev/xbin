#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

/// A question as a native form built from a flat JSON Schema
/// (``QuestionForm``): toggles for booleans, menus for choices, text and
/// number fields; required fields are checked before `onSubmit` gets the
/// answer object. Settled: read-only with what was answered.
///
/// Rev 2 (D233; ``QuestionOptions``): with `rows` a property's choices are
/// full-width rows, one tap each; a property named in `other` is the free
/// text riding on a choice ("Something else") — typing clears the choice,
/// choosing clears the text; `paged` shows one question a page ("1 of 2")
/// with Back and Next at the bottom; the buttons take the tile's words.
/// The rules are the model's (``QuestionSession``).
public struct QuestionView: View {
    public let question: ChatQuestion
    public var onSubmit: @MainActor (JSONValue) -> Void
    public var onSkip: (@MainActor () -> Void)?
    @State private var session: QuestionSession
    @State private var error: String?
    @Environment(\.dynamicTypeSize) private var typeSize

    public init(question: ChatQuestion, onSubmit: @escaping @MainActor (JSONValue) -> Void,
                onSkip: (@MainActor () -> Void)? = nil) {
        self.question = question
        self.onSubmit = onSubmit
        self.onSkip = onSkip
        _session = State(initialValue: question.session)
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: question.settled ? XbinIcons.UI.answered : XbinIcons.UI.question)
                    .foregroundStyle(question.settled ? XbinColor.tone(.ok) : XbinColor.accentText)
                Text(verbatim: question.title).font(.headline)
                    .fixedSize(horizontal: false, vertical: true)
                if !question.settled, let page = session.pageLabel {
                    Spacer(minLength: 4)
                    Text(verbatim: page).font(.caption.monospacedDigit()).foregroundStyle(XbinColor.muted)
                        .accessibilityLabel(Text(verbatim: "Question \(page)"))
                }
            }
            if let d = question.form.description {
                Text(verbatim: d).font(.subheadline).foregroundStyle(XbinColor.muted)
            }
            // Settled: every question, read-only; else the page in view.
            ForEach(question.settled ? session.questions : session.visible) { field in
                if question.options.style == .rows, case .choice(let choices) = field.kind {
                    QuestionChoiceRows(field: field, choices: choices, rider: session.rider(of: field), session: $session,
                                       settled: question.settled) { error = nil }
                } else {
                    QuestionFieldRow(field: field, value: binding(field.name))
                    if let rider = session.rider(of: field) {
                        QuestionOtherField(rider: rider, session: $session, settled: question.settled) { error = nil }
                    }
                }
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
                buttons
                    .controlSize(.large)
                    // Square, as the reference's `.b-opt` (D185).
                    .buttonBorderShape(.roundedRectangle(radius: XbinShapes.radius))
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .modifier(ChatCard())
        .onChange(of: question.settled) {
            session = question.session
            error = nil
        }
    }

    /// Skip (never an answer) and Back at the leading end, the primary
    /// button — Next, or the submit — at the trailing end; one under the
    /// other at the accessibility text sizes, the primary last.
    @ViewBuilder
    private var buttons: some View {
        if stacked {
            VStack(spacing: 8) {
                skipButton
                backButton
                primaryButton
            }
        } else {
            HStack {
                skipButton
                backButton
                Spacer()
                primaryButton
            }
        }
    }

    @ViewBuilder
    private var skipButton: some View {
        if let onSkip {
            Button { onSkip() } label: { buttonLabel(question.options.skipTitle) }
                .buttonStyle(.bordered)
        }
    }

    @ViewBuilder
    private var backButton: some View {
        if session.canGoBack {
            Button {
                session.back()
                error = nil
            } label: { buttonLabel("Back") }
                .buttonStyle(.bordered)
        }
    }

    private var primaryButton: some View {
        Button { advance() } label: { buttonLabel(session.primaryTitle) }
            .buttonStyle(.borderedProminent)
            .tint(XbinColor.tint)
            .foregroundStyle(XbinColor.onTint)
    }

    private func buttonLabel(_ title: String) -> some View {
        Text(verbatim: title).frame(maxWidth: stacked ? .infinity : nil)
    }

    private var stacked: Bool { typeSize.isAccessibilitySize }

    private func binding(_ name: String) -> Binding<JSONValue?> {
        mainBinding(get: { session.value(name) }, set: { v in
            session.set(name, v)
            error = nil
        })
    }

    /// Next on a page before the last, else submit.
    private func advance() {
        if !session.isLastPage {
            error = session.next()?.message
            return
        }
        switch session.content() {
        case .success(let content):
            error = nil
            onSubmit(content)
        case .failure(let missing):
            error = missing.message
        }
    }
}

/// A question's choices as full-width rows (rev 2 `rows`): one tap
/// chooses; the row chosen carries a filled mark and the selection fill,
/// and reads as selected. Its "Something else" rides under the rows.
private struct QuestionChoiceRows: View {
    let field: QuestionField
    let choices: [QuestionField.Choice]
    let rider: QuestionField?
    @Binding var session: QuestionSession
    let settled: Bool
    let changed: @MainActor () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !field.title.isEmpty, field.title != field.name {
                HStack(spacing: 2) {
                    Text(verbatim: field.title)
                    if field.required { Text(verbatim: "*").foregroundStyle(XbinColor.toneText(.danger)) }
                }
                .font(.subheadline.weight(.medium))
                .fixedSize(horizontal: false, vertical: true)
            }
            if let d = field.description {
                Text(verbatim: d).font(.footnote).foregroundStyle(XbinColor.muted)
            }
            ForEach(choices, id: \.self) { choice in row(choice) }
            if let rider {
                QuestionOtherField(rider: rider, session: $session, settled: settled, changed: changed)
            }
        }
    }

    private func row(_ choice: QuestionField.Choice) -> some View {
        let chosen = session.isChosen(field, choice)
        return Button {
            session.choose(field.name, chosen ? nil : choice.value)
            changed()
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Image(systemName: chosen ? "largecircle.fill.circle" : "circle")
                    .foregroundStyle(chosen ? XbinColor.accentText : XbinColor.muted)
                Text(verbatim: choice.label)
                    .font(.subheadline)
                    .foregroundStyle(XbinColor.text)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .background(chosen ? XbinColor.selection : XbinColor.fill, in: RoundedRectangle.xbinPlate)
            .overlay(RoundedRectangle.xbinPlate.strokeBorder(chosen ? XbinColor.accent : XbinColor.border, lineWidth: 1))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(verbatim: choice.label))
        .accessibilityAddTraits(chosen ? .isSelected : [])
    }
}

/// The free text riding on a question's choices ("Something else"): the
/// rider property's `title` is its label, its `description` the
/// placeholder. Typing clears the choice (``QuestionSession/set(_:_:)``).
private struct QuestionOtherField: View {
    let rider: QuestionField
    @Binding var session: QuestionSession
    let settled: Bool
    let changed: @MainActor () -> Void

    var body: some View {
        let label = rider.title == rider.name ? "Something else" : rider.title
        let text = mainBinding(get: { session.text(rider.name) }, set: { v in
            session.set(rider.name, v.isEmpty ? nil : .string(v))
            changed()
        })
        // Settled with a choice: the box that wasn't used is not drawn.
        if !settled || !session.text(rider.name).isEmpty {
            TextField(text: text, prompt: Text(verbatim: rider.description ?? label), axis: .vertical) { Text(verbatim: label) }
                .lineLimit(1...4)
                .padding(10)
                .frame(minHeight: 44)
                .background(XbinColor.fill, in: RoundedRectangle.xbinPlate)
                .accessibilityLabel(Text(verbatim: label))
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
                    .background(XbinColor.fill, in: RoundedRectangle.xbinPlate)
                description
            }
        case .text(let format):
            VStack(alignment: .leading, spacing: 6) {
                title
                TextField(text: text, prompt: nil) { Text(verbatim: field.title) }
                    .keyboardType(format == "email" ? .emailAddress : format == "uri" ? .URL : .default)
                    .textInputAutocapitalization(format == nil ? .sentences : .never)
                    .padding(10)
                    .background(XbinColor.fill, in: RoundedRectangle.xbinPlate)
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
