@_exported import Foundation

public final class CGContext {}

public enum UIUserInterfaceStyle: Int, Sendable { case unspecified, light, dark }
public final class UITraitCollection: Sendable { public init() {}; public var userInterfaceStyle: UIUserInterfaceStyle { .light } }
public final class UIColor: NSObject, @unchecked Sendable {
    public init(red: CGFloat, green: CGFloat, blue: CGFloat, alpha: CGFloat) {}
    public init(dynamicProvider: @escaping @Sendable (UITraitCollection) -> UIColor) {}
    public static let systemGroupedBackground = UIColor(red: 0, green: 0, blue: 0, alpha: 1)
    public static let secondarySystemGroupedBackground = systemGroupedBackground
    public static let tertiarySystemGroupedBackground = systemGroupedBackground
    public static let separator = systemGroupedBackground
    public static let label = systemGroupedBackground
    public static let secondaryLabel = systemGroupedBackground
    public static let tertiarySystemFill = systemGroupedBackground
    public static let systemGreen = systemGroupedBackground
    public static let systemOrange = systemGroupedBackground
    public static let systemRed = systemGroupedBackground
    public static let systemGray = systemGroupedBackground
    public static let systemBlue = systemGroupedBackground
    public static let clear = systemGroupedBackground
    public static let placeholderText = systemGroupedBackground
}
public final class UIImage: Sendable {
    public init?(data: Data) {}
    public func pngData() -> Data? { nil }
    public var size: CGSize { .zero }
}
@MainActor public final class UIPasteboard {
    public static let general = UIPasteboard()
    public var string: String?
}
public enum UIKeyboardType: Int, Sendable { case `default`, decimalPad, emailAddress, URL, webSearch, numberPad }
public struct UITextContentType: Sendable, Hashable { public static let password = UITextContentType(), emailAddress = UITextContentType(), URL = UITextContentType() }
public struct UIContentSizeCategory: Sendable { public init() {} }
public enum UITextAutocapitalizationType: Int, Sendable { case none, words, sentences, allCharacters }
public enum UITextAutocorrectionType: Int, Sendable { case `default`, no, yes }
public enum UIReturnKeyType: Int, Sendable { case `default`, go, google, join, next, route, search, send, yahoo, done, emergencyCall, `continue` }
public enum NSTextAlignment: Int, Sendable { case left, center, right, justified, natural }

@MainActor open class UIResponder: NSObject {
    @discardableResult open func resignFirstResponder() -> Bool { true }
}
public struct UIEdgeInsets: Sendable, Equatable {
    public var top: CGFloat = 0, left: CGFloat = 0, bottom: CGFloat = 0, right: CGFloat = 0
    public init() {}
    public static let zero = UIEdgeInsets()
}
public struct UILayoutPriority: Sendable { public static let defaultLow = UILayoutPriority(), defaultHigh = UILayoutPriority(), required = UILayoutPriority() }
public enum NSLayoutConstraint { public enum Axis: Int, Sendable { case horizontal, vertical } }
@MainActor open class UIView: UIResponder {
    public override init() { super.init() }
    public init(frame: CGRect) { super.init(); self.frame = frame }
    public var frame: CGRect = CGRect(x: 0, y: 0, width: 0, height: 0)
    public var safeAreaInsets: UIEdgeInsets { UIEdgeInsets() }
    public func drawHierarchy(in rect: CGRect, afterScreenUpdates afterUpdates: Bool) -> Bool { false }
    public var bounds: CGRect { frame }
    public var layer: CALayer { CALayer() }
    public func setNeedsLayout() {}
    public func layoutIfNeeded() {}
    open func layoutSubviews() {}
    public func addSubview(_ view: UIView) {}
    public var overrideUserInterfaceStyle: UIUserInterfaceStyle = .unspecified
    public var backgroundColor: UIColor?
    public var tintColor: UIColor!
    public var isHidden = false
    public var accessibilityLabel: String?
    public var isAccessibilityElement = false
    open var intrinsicContentSize: CGSize { .zero }
    public func invalidateIntrinsicContentSize() {}
    open func sizeThatFits(_ size: CGSize) -> CGSize { size }
    public func setContentHuggingPriority(_ priority: UILayoutPriority, for axis: NSLayoutConstraint.Axis) {}
    public func setContentCompressionResistancePriority(_ priority: UILayoutPriority, for axis: NSLayoutConstraint.Axis) {}
}

// MARK: Text input (the renderer's IME-aware fields)

public final class UIFont: NSObject, @unchecked Sendable {
    public struct TextStyle: Sendable, Hashable {
        public static let body = TextStyle(), subheadline = TextStyle(), footnote = TextStyle(), caption1 = TextStyle(), headline = TextStyle()
    }
    public class func preferredFont(forTextStyle style: TextStyle, compatibleWith traitCollection: UITraitCollection? = nil) -> UIFont { UIFont() }
    public var lineHeight: CGFloat { 20 }
}
extension UITraitCollection {
    public convenience init(preferredContentSizeCategory: UIContentSizeCategory) { self.init() }
}
open class UITextRange: NSObject {}
public typealias UIActionHandler = @MainActor (UIAction) -> Void
@MainActor public final class UIAction {
    public init(title: String = "", handler: @escaping UIActionHandler) {}
}
@MainActor open class UIControl: UIView {
    public struct Event: OptionSet, Sendable {
        public let rawValue: UInt
        public init(rawValue: UInt) { self.rawValue = rawValue }
        public static let editingChanged = Event(rawValue: 1 << 17), valueChanged = Event(rawValue: 1 << 12)
    }
    public var isEnabled = true
    public func addAction(_ action: UIAction, for controlEvents: Event) {}
}
@MainActor public protocol UITextInput: AnyObject {
    var markedTextRange: UITextRange? { get }
    func unmarkText()
}
@MainActor public protocol UITextFieldDelegate: AnyObject {}
@MainActor public protocol UITextViewDelegate: AnyObject {}
@MainActor open class UITextField: UIControl, UITextInput {
    public enum BorderStyle: Int, Sendable { case none, line, bezel, roundedRect }
    public var text: String?
    public var placeholder: String?
    public var font: UIFont?
    public var textColor: UIColor?
    public var borderStyle: BorderStyle = .none
    public weak var delegate: (any UITextFieldDelegate)?
    public var keyboardType: UIKeyboardType = .default
    public var textContentType: UITextContentType!
    public var autocapitalizationType: UITextAutocapitalizationType = .sentences
    public var autocorrectionType: UITextAutocorrectionType = .default
    public var returnKeyType: UIReturnKeyType = .default
    public var isSecureTextEntry = false
    public var markedTextRange: UITextRange? { nil }
    public func unmarkText() {}
}
@MainActor open class NSTextContainer: NSObject {
    public var lineFragmentPadding: CGFloat = 5
}
@MainActor open class UIScrollView: UIView {
    public var isScrollEnabled = true
}
@MainActor open class UITextView: UIScrollView, UITextInput {
    public init(frame: CGRect, textContainer: NSTextContainer?) { super.init(frame: frame) }
    public required init?(coder: NSCoder) { nil }
    public var text: String!
    public var font: UIFont?
    public var textColor: UIColor?
    public weak var delegate: (any UITextViewDelegate)?
    public var isEditable = true
    public var keyboardType: UIKeyboardType = .default
    public var textContentType: UITextContentType!
    public var autocapitalizationType: UITextAutocapitalizationType = .sentences
    public var autocorrectionType: UITextAutocorrectionType = .default
    public var textContainerInset = UIEdgeInsets()
    public var textContainer = NSTextContainer()
    public var markedTextRange: UITextRange? { nil }
    public func unmarkText() {}
}
@MainActor open class UILabel: UIView {
    public var text: String?
    public var font: UIFont!
    public var textColor: UIColor!
    public var numberOfLines = 1
    public var textAlignment: NSTextAlignment = .natural
}
public final class CALayer { public func render(in ctx: CGContext) {} }
@MainActor open class UIScene: UIResponder {}
@MainActor open class UIWindowScene: UIScene {}
@MainActor open class UIApplication: UIResponder {
    public static let shared = UIApplication()
    public var connectedScenes: Set<UIScene> { [] }
}
@MainActor open class UIWindow: UIView {
    @available(iOS, deprecated: 26.0, message: "Use init(windowScene:) instead.")
    public override init(frame: CGRect) { super.init(frame: frame) }
    public init(windowScene: UIWindowScene) { super.init() }
    public var rootViewController: UIViewController?
}
public struct UITraitOverrides { public var preferredContentSizeCategory = UIContentSizeCategory() }
@MainActor open class UIViewController {
    public init() {}
    public var additionalSafeAreaInsets = UIEdgeInsets()
    public var view: UIView! = UIView()
    public var overrideUserInterfaceStyle: UIUserInterfaceStyle = .unspecified
    public var traitOverrides = UITraitOverrides()
}
public final class UIGraphicsImageRendererFormat { public init() {}; public var scale: CGFloat = 1; public var opaque = false }
public final class UIGraphicsImageRendererContext { public var cgContext: CGContext { CGContext() } }
public final class UIGraphicsImageRenderer {
    public init(size: CGSize, format: UIGraphicsImageRendererFormat) {}
    public func image(actions: (UIGraphicsImageRendererContext) -> Void) -> UIImage { UIImage(data: Data())! }
}
