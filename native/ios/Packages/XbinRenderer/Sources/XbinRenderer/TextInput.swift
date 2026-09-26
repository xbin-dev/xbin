#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinRendererModel

// IME-aware text controls for `field` and the composer (native/spec/tree.md
// §6, plans/native.md §24). SwiftUI's TextField reports every change of its
// text, marked (composing) text included, and a value set while the user
// composes ends the composition: a Japanese reading reached the tile before
// its kanji were chosen, and a tile that rewrote it broke the input. These
// wrap UITextField/UITextView, which expose the marked range, and follow
// ``TextInputGate``: nothing is reported while text is marked, the commit
// is reported once, and a value the tile sets meanwhile waits for the end
// of the composition.

/// How an IME-aware text control looks and behaves.
struct TextInputStyle {
    var placeholder = ""
    var accessibilityLabel = ""
    var keyboard: UIKeyboardType = .default
    var contentType: UITextContentType?
    var autocapitalization: UITextAutocapitalizationType = .sentences
    var autocorrection: UITextAutocorrectionType = .default
    var returnKey: UIReturnKeyType = .default
    var secure = false
    var textStyle: UIFont.TextStyle = .body
}

/// Lets the SwiftUI side act on a text control: commit the composition in
/// progress (the composer does before sending, so the marked text isn't
/// left behind).
@MainActor
final class TextInputHandle {
    fileprivate var commit: (@MainActor () -> Void)?

    /// Commits any marked text; the control reports the result as usual.
    func commitComposition() { commit?() }
}

/// The font, colours and traits both controls share.
@MainActor
private enum TextInputLook {
    static func font(_ style: UIFont.TextStyle, _ size: DynamicTypeSize) -> UIFont {
        let traits = UITraitCollection(preferredContentSizeCategory: UIContentSizeCategory(size))
        return UIFont.preferredFont(forTextStyle: style, compatibleWith: traits)
    }

    /// Applies what changed: setting a trait a first responder already has
    /// can reload the keyboard, and restyling the text mid-composition can
    /// disturb the marked text.
    static func apply(_ s: TextInputStyle, enabled: Bool, size: DynamicTypeSize, to v: UITextField) {
        let f = font(s.textStyle, size)
        if v.font != f { v.font = f }
        let placeholder: String? = s.placeholder.isEmpty ? nil : s.placeholder
        if v.placeholder != placeholder { v.placeholder = placeholder }
        if v.accessibilityLabel != s.accessibilityLabel { v.accessibilityLabel = s.accessibilityLabel }
        if v.keyboardType != s.keyboard { v.keyboardType = s.keyboard }
        if v.textContentType != s.contentType { v.textContentType = s.contentType }
        if v.autocapitalizationType != s.autocapitalization { v.autocapitalizationType = s.autocapitalization }
        if v.autocorrectionType != s.autocorrection { v.autocorrectionType = s.autocorrection }
        if v.returnKeyType != s.returnKey { v.returnKeyType = s.returnKey }
        if v.isSecureTextEntry != s.secure { v.isSecureTextEntry = s.secure }
        if v.isEnabled != enabled { v.isEnabled = enabled }
        colors(enabled: enabled, text: v.textColor, tint: v.tintColor) { v.textColor = $0 } setTint: { v.tintColor = $0 }
    }

    /// Sets the text colour (`label`, `secondaryLabel` when disabled) and
    /// the caret tint when they differ.
    private static func colors(enabled: Bool, text: UIColor?, tint: UIColor?,
                               setText: (UIColor) -> Void, setTint: (UIColor) -> Void) {
        let color: UIColor = enabled ? .label : .secondaryLabel
        if text != color { setText(color) }
        if tint != XbinColor.uiTint { setTint(XbinColor.uiTint) }
    }

    static func apply(_ s: TextInputStyle, enabled: Bool, size: DynamicTypeSize, to v: XbinTextView) {
        let f = font(s.textStyle, size)
        if v.font != f { v.font = f }
        if v.placeholder != s.placeholder { v.placeholder = s.placeholder }
        if v.accessibilityLabel != s.accessibilityLabel { v.accessibilityLabel = s.accessibilityLabel }
        if v.keyboardType != s.keyboard { v.keyboardType = s.keyboard }
        if v.textContentType != s.contentType { v.textContentType = s.contentType }
        if v.autocapitalizationType != s.autocapitalization { v.autocapitalizationType = s.autocapitalization }
        if v.autocorrectionType != s.autocorrection { v.autocorrectionType = s.autocorrection }
        if v.isEditable != enabled { v.isEditable = enabled }
        colors(enabled: enabled, text: v.textColor, tint: v.tintColor) { v.textColor = $0 } setTint: { v.tintColor = $0 }
        v.refreshPlaceholder()
    }
}

// MARK: - One line

/// A one-line text control (`field` text, secure, number, email, url,
/// search) over UITextField. `text` is the controlled value; `onInput` gets
/// each committed change, `onFocus` editing starting and ending, `onReturn`
/// the return key (then editing ends).
struct XbinTextField: UIViewRepresentable {
    let text: String
    var style = TextInputStyle()
    var onInput: @MainActor (String) -> Void
    var onFocus: @MainActor (Bool) -> Void = { _ in }
    var onReturn: (@MainActor () -> Void)?

    func makeCoordinator() -> Coordinator { Coordinator(value: text) }

    func makeUIView(context: Context) -> UITextField {
        let v = UITextField()
        v.text = text
        v.borderStyle = .none
        v.backgroundColor = .clear
        let c = context.coordinator
        v.delegate = c
        v.addAction(UIAction { [weak c, weak v] _ in
            if let c, let v { c.sync(v) }
        }, for: .editingChanged)
        v.setContentHuggingPriority(.defaultLow, for: .horizontal)
        v.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        c.parent = self
        return v
    }

    func updateUIView(_ v: UITextField, context: Context) {
        let c = context.coordinator
        c.parent = self
        TextInputLook.apply(style, enabled: context.environment.isEnabled, size: context.environment.dynamicTypeSize, to: v)
        c.tile(text, in: v)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextField, context: Context) -> CGSize? {
        let natural = uiView.intrinsicContentSize
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? max(natural.width, 120)
        return CGSize(width: width, height: natural.height)
    }

    @MainActor
    final class Coordinator: NSObject, UITextFieldDelegate {
        var parent: XbinTextField?
        private var gate: TextInputGate

        init(value: String) {
            gate = TextInputGate(value: value)
        }

        // A composition that commits without changing the text (the
        // reading kept as is) changes only the marked range: the selection.
        func textFieldDidChangeSelection(_ v: UITextField) { sync(v) }

        func textFieldDidBeginEditing(_ v: UITextField) { parent?.onFocus(true) }

        func textFieldDidEndEditing(_ v: UITextField) {
            sync(v)
            parent?.onFocus(false)
        }

        func textFieldShouldReturn(_ v: UITextField) -> Bool {
            sync(v)
            guard let onReturn = parent?.onReturn else { return true }
            onReturn()
            v.resignFirstResponder()
            return false
        }

        func sync(_ v: UITextField) {
            switch gate.changed(text: v.text ?? "", composing: v.markedTextRange != nil) {
            case .report(let s): parent?.onInput(s)
            case .replace(let s): v.text = s
            case .none: break
            }
        }

        func tile(_ value: String, in v: UITextField) {
            if case .replace(let s) = gate.tile(value, shown: v.text ?? "", composing: v.markedTextRange != nil) {
                v.text = s
            }
        }
    }
}

// MARK: - Several lines

/// A growing text control (`field kind=multiline`, the composer) over
/// UITextView: `lines` rows tall at least, then growing with the text up
/// to its upper bound, then scrolling. Same reporting as
/// ``XbinTextField``; `onComposing` hears marked text come and go (the
/// composer enables send for it), and `handle` lets the caller commit a
/// composition.
struct XbinTextArea: UIViewRepresentable {
    let text: String
    var style = TextInputStyle()
    var lines: ClosedRange<Int> = 1...6
    var handle: TextInputHandle?
    var onInput: @MainActor (String) -> Void
    var onFocus: @MainActor (Bool) -> Void = { _ in }
    var onComposing: (@MainActor (Bool) -> Void)?

    func makeCoordinator() -> Coordinator { Coordinator(value: text) }

    func makeUIView(context: Context) -> XbinTextView {
        let v = XbinTextView(frame: .zero, textContainer: nil)
        v.text = text
        v.backgroundColor = .clear
        v.textContainerInset = .zero
        v.textContainer.lineFragmentPadding = 0
        v.isScrollEnabled = false
        v.delegate = context.coordinator
        v.setContentHuggingPriority(.defaultLow, for: .horizontal)
        v.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        context.coordinator.parent = self
        context.coordinator.view = v
        return v
    }

    func updateUIView(_ v: XbinTextView, context: Context) {
        let c = context.coordinator
        c.parent = self
        c.view = v
        handle?.commit = { [weak c] in c?.commitComposition() }
        TextInputLook.apply(style, enabled: context.environment.isEnabled, size: context.environment.dynamicTypeSize, to: v)
        c.tile(text, in: v)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: XbinTextView, context: Context) -> CGSize? {
        // The proposed width, 0 included: a stack's minimum-size probe that
        // got 240 back took the view for a rigid one. 240 when unproposed.
        let proposed = proposal.width.flatMap { $0.isFinite ? max(0, $0) : nil }
        let width = proposed ?? 240
        let line = (uiView.font ?? UIFont.preferredFont(forTextStyle: .body)).lineHeight
        let content = uiView.sizeThatFits(CGSize(width: max(width, 1), height: .greatestFiniteMagnitude)).height
        let low = line * CGFloat(max(1, lines.lowerBound))
        let high = line * CGFloat(max(lines.lowerBound, lines.upperBound))
        let overflow = content > high + 0.5
        // Only a real width decides scrolling, not a probe.
        if (proposed ?? 0) > 0, uiView.isScrollEnabled != overflow { uiView.isScrollEnabled = overflow }
        return CGSize(width: width, height: ceil(min(max(content, low), high)))
    }

    @MainActor
    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: XbinTextArea?
        weak var view: XbinTextView?
        private var gate: TextInputGate
        private var composing = false

        init(value: String) {
            gate = TextInputGate(value: value)
        }

        func textViewDidChange(_ v: UITextView) { sync(v) }

        func textViewDidChangeSelection(_ v: UITextView) { sync(v) }

        func textViewDidBeginEditing(_ v: UITextView) { parent?.onFocus(true) }

        func textViewDidEndEditing(_ v: UITextView) {
            sync(v)
            parent?.onFocus(false)
        }

        func commitComposition() {
            guard let v = view, v.markedTextRange != nil else { return }
            v.unmarkText()
            sync(v)
        }

        func sync(_ v: UITextView) {
            let marked = v.markedTextRange != nil
            switch gate.changed(text: v.text ?? "", composing: marked) {
            case .report(let s): parent?.onInput(s)
            case .replace(let s): v.text = s
            case .none: break
            }
            changed(v, marked: marked)
        }

        func tile(_ value: String, in v: XbinTextView) {
            let marked = v.markedTextRange != nil
            if case .replace(let s) = gate.tile(value, shown: v.text ?? "", composing: marked) {
                v.text = s
                changed(v, marked: false)
            }
        }

        /// The text or the marked range changed: the placeholder, the
        /// height SwiftUI gives the view, and the composer's send button.
        private func changed(_ v: UITextView, marked: Bool) {
            (v as? XbinTextView)?.refreshPlaceholder()
            v.invalidateIntrinsicContentSize()
            if marked != composing {
                composing = marked
                parent?.onComposing?(marked)
            }
        }
    }
}

/// A UITextView with a placeholder (UITextView has none), hidden as soon
/// as there is any text, marked text included.
final class XbinTextView: UITextView {
    private let placeholderLabel = UILabel()

    var placeholder = "" {
        didSet {
            placeholderLabel.text = placeholder
            refreshPlaceholder()
            setNeedsLayout()
        }
    }

    override init(frame: CGRect, textContainer: NSTextContainer?) {
        super.init(frame: frame, textContainer: textContainer)
        placeholderLabel.textColor = .placeholderText
        placeholderLabel.numberOfLines = 0
        placeholderLabel.textAlignment = .natural
        placeholderLabel.isAccessibilityElement = false
        addSubview(placeholderLabel)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { nil }

    func refreshPlaceholder() {
        placeholderLabel.font = font
        placeholderLabel.isHidden = placeholder.isEmpty || !text.isEmpty
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        let inset = textContainerInset
        let pad = textContainer.lineFragmentPadding
        let width = max(0, bounds.width - inset.left - inset.right - pad * 2)
        let height = placeholderLabel.sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude)).height
        placeholderLabel.frame = CGRect(x: inset.left + pad, y: inset.top, width: width, height: height)
    }
}
#endif
