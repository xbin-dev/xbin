// Extra UIKit declarations the terminal uses (the real SDK's signatures,
// as far as known). Replaces the renderer stub's UIView/UIResponder.

public struct Selector: Sendable { public init(_ s: String) {} }

@MainActor open class UIResponder: NSObject {
    open var inputAccessoryView: UIView? { nil }
    open func becomeFirstResponder() -> Bool { true }
    open func resignFirstResponder() -> Bool { true }
    open func reloadInputViews() {}
    open func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {}
    open func copy(_ sender: Any?) {}
}

public struct UIEdgeInsets: Sendable, Equatable {
    public var top: CGFloat = 0, left: CGFloat = 0, bottom: CGFloat = 0, right: CGFloat = 0
    public init() {}
}
public struct NSDirectionalEdgeInsets: Sendable, Equatable {
    public init(top: CGFloat, leading: CGFloat, bottom: CGFloat, trailing: CGFloat) {}
}

@MainActor public class NSLayoutAnchor<T> {
    public func constraint(equalTo anchor: NSLayoutAnchor<T>) -> NSLayoutConstraint { NSLayoutConstraint() }
    public func constraint(equalTo anchor: NSLayoutAnchor<T>, constant c: CGFloat) -> NSLayoutConstraint { NSLayoutConstraint() }
}
@MainActor public class NSLayoutXAxisAnchor: NSLayoutAnchor<NSLayoutXAxisAnchor> {}
@MainActor public class NSLayoutYAxisAnchor: NSLayoutAnchor<NSLayoutYAxisAnchor> {}
@MainActor public class NSLayoutDimension: NSLayoutAnchor<NSLayoutDimension> {
    public func constraint(equalToConstant c: CGFloat) -> NSLayoutConstraint { NSLayoutConstraint() }
    public func constraint(equalTo anchor: NSLayoutDimension, multiplier m: CGFloat) -> NSLayoutConstraint { NSLayoutConstraint() }
}
@MainActor public class NSLayoutConstraint {
    public var isActive = false
    public class func activate(_ constraints: [NSLayoutConstraint]) {}
}
@MainActor public class UILayoutGuide {
    public var leadingAnchor: NSLayoutXAxisAnchor { NSLayoutXAxisAnchor() }
    public var trailingAnchor: NSLayoutXAxisAnchor { NSLayoutXAxisAnchor() }
}

@MainActor open class UIView: UIResponder {
    public init(frame: CGRect) { super.init() }
    public required init?(coder: NSCoder) { super.init() }
    public convenience override init() { self.init(frame: .zero) }
    public var frame: CGRect = .zero
    public var bounds: CGRect = .zero
    public var safeAreaInsets: UIEdgeInsets { UIEdgeInsets() }
    public func drawHierarchy(in rect: CGRect, afterScreenUpdates afterUpdates: Bool) -> Bool { false }
    public var layer: CALayer { CALayer() }
    public func setNeedsLayout() {}
    public func layoutIfNeeded() {}
    public var overrideUserInterfaceStyle: UIUserInterfaceStyle = .unspecified
    public var backgroundColor: UIColor?
    public var isHidden = false
    public var isOpaque = true
    public var contentMode: UIView.ContentMode = .scaleToFill
    public var isUserInteractionEnabled = true
    public var translatesAutoresizingMaskIntoConstraints = true
    public var tag = 0
    public var superview: UIView? { nil }
    public var window: UIView? { nil }
    public var accessibilityLabel: String?
    public var accessibilityHint: String?
    public var isAccessibilityElement = false
    public var layoutMarginsGuide: UILayoutGuide { UILayoutGuide() }
    public var leadingAnchor: NSLayoutXAxisAnchor { NSLayoutXAxisAnchor() }
    public var trailingAnchor: NSLayoutXAxisAnchor { NSLayoutXAxisAnchor() }
    public var topAnchor: NSLayoutYAxisAnchor { NSLayoutYAxisAnchor() }
    public var bottomAnchor: NSLayoutYAxisAnchor { NSLayoutYAxisAnchor() }
    public var widthAnchor: NSLayoutDimension { NSLayoutDimension() }
    public var heightAnchor: NSLayoutDimension { NSLayoutDimension() }
    public func addSubview(_ view: UIView) {}
    public func removeFromSuperview() {}
    public func setNeedsDisplay() {}
    open func draw(_ rect: CGRect) {}
    public func convert(_ point: CGPoint, from view: UIView?) -> CGPoint { point }
    public func convert(_ point: CGPoint, to view: UIView?) -> CGPoint { point }
    public func addGestureRecognizer(_ g: UIGestureRecognizer) {}
    public var gestureRecognizers: [UIGestureRecognizer]?
    public enum ContentMode: Int, Sendable { case scaleToFill, redraw }
}
@MainActor open class UIScrollView: UIView {
    public var contentOffset: CGPoint = .zero
    public var contentSize: CGSize = .zero
}
@MainActor open class UIControl: UIView {
    public struct Event: OptionSet, Sendable { public let rawValue: UInt; public init(rawValue: UInt) { self.rawValue = rawValue }; public static let touchUpInside = Event(rawValue: 64) }
    public func addAction(_ action: UIAction, for controlEvents: UIControl.Event) {}
}
@MainActor public class UIAction {
    public init(title: String = "", handler: @escaping @MainActor (UIAction) -> Void) {}
}
public struct UIConfigurationTextAttributesTransformer {
    public struct Attrs { public var font: UIFont? }
    public init(_ transform: @escaping @Sendable (Attrs) -> Attrs) {}
}
public enum NSLineBreakMode: Int, Sendable { case byWordWrapping, byCharWrapping, byClipping, byTruncatingHead, byTruncatingTail, byTruncatingMiddle }
@MainActor open class UIButton: UIControl {
    public struct Configuration {
        public static func gray() -> Configuration { Configuration() }
        public var title: String?
        public var contentInsets = NSDirectionalEdgeInsets(top: 0, leading: 0, bottom: 0, trailing: 0)
        public var titleTextAttributesTransformer: UIConfigurationTextAttributesTransformer?
        public var titleLineBreakMode: NSLineBreakMode = .byTruncatingTail
        public var baseBackgroundColor: UIColor?
        public var baseForegroundColor: UIColor?
    }
    public convenience init(configuration: Configuration, primaryAction: UIAction? = nil) { self.init(frame: .zero) }
    public var configuration: Configuration?
}
@MainActor open class UIStackView: UIView {
    public enum Distribution: Int, Sendable { case fill, fillEqually, fillProportionally }
    public var axis: NSLayoutConstraint.Axis = .horizontal
    public var distribution: Distribution = .fill
    public var spacing: CGFloat = 0
    public func addArrangedSubview(_ view: UIView) {}
}
extension NSLayoutConstraint { public enum Axis: Int, Sendable { case horizontal, vertical } }
@MainActor open class UIInputView: UIView {
    public enum Style: Int, Sendable { case `default`, keyboard }
    public init(frame: CGRect, inputViewStyle: UIInputView.Style) { super.init(frame: frame) }
    public required init?(coder: NSCoder) { super.init(coder: coder) }
    public var allowsSelfSizing = false
}
@MainActor public protocol UIInputViewAudioFeedback { var enableInputClicksWhenVisible: Bool { get } }
@MainActor public final class UIDevice { public static let current = UIDevice(); public func playInputClick() {} }

@MainActor open class UIGestureRecognizer {
    public enum State: Int, Sendable { case possible, began, changed, ended, cancelled, failed }
    public init(target: Any?, action: Selector?) {}
    public var state: State { .possible }
    public var view: UIView? { nil }
    public weak var delegate: (any UIGestureRecognizerDelegate)?
    public var cancelsTouchesInView = true
    public var isEnabled = true
    public func location(in view: UIView?) -> CGPoint { .zero }
    public var numberOfTouches: Int { 0 }
}
@MainActor public protocol UIGestureRecognizerDelegate: AnyObject {}
extension UIGestureRecognizerDelegate {
    public func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool { false }
}
@MainActor open class UILongPressGestureRecognizer: UIGestureRecognizer {
    public var minimumPressDuration: TimeInterval = 0.5
    public var allowableMovement: CGFloat = 10
}
@MainActor open class UIPanGestureRecognizer: UIGestureRecognizer {
    public var minimumNumberOfTouches = 1
    public var maximumNumberOfTouches = Int.max
    public func translation(in view: UIView?) -> CGPoint { .zero }
    public func setTranslation(_ t: CGPoint, in view: UIView?) {}
}
@MainActor open class UIPinchGestureRecognizer: UIGestureRecognizer { public var scale: CGFloat = 1 }

@MainActor public final class UITextLoupeSession {
    public class func begin(at point: CGPoint, fromSelectionWidgetView widget: UIView?, in interactionView: UIView) -> Self { fatalError() }
    public func move(to point: CGPoint, withCaretRect caretRect: CGRect, trackingCaret tracksCaret: Bool) {}
    public func invalidate() {}
}

public final class UIFont: Sendable {
    public struct Weight: Sendable { public static let regular = Weight(), medium = Weight(), bold = Weight() }
    public class func monospacedSystemFont(ofSize fontSize: CGFloat, weight: UIFont.Weight) -> UIFont { UIFont() }
}
extension UIColor {
    public static let black = UIColor(red: 0, green: 0, blue: 0, alpha: 1)
    public static let clear = black
    public func withAlphaComponent(_ a: CGFloat) -> UIColor { self }
    public var cgColor: CGColor { CGColor() }
}
public final class CGColor {}
extension CGContext {
    public func setFillColor(_ c: CGColor) {}
    public func fill(_ r: CGRect) {}
}
public func UIGraphicsGetCurrentContext() -> CGContext? { nil }
public struct NSUnderlineStyle: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let single = NSUnderlineStyle(rawValue: 1) }
extension NSAttributedString.Key {
    public static let font = NSAttributedString.Key("NSFont")
    public static let foregroundColor = NSAttributedString.Key("NSColor")
    public static let underlineStyle = NSAttributedString.Key("NSUnderline")
}
extension NSString { public func draw(at point: CGPoint, withAttributes attrs: [NSAttributedString.Key: Any]? = nil) {} }

public struct UIKeyModifierFlags: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }
    public static let shift = UIKeyModifierFlags(rawValue: 1), control = UIKeyModifierFlags(rawValue: 2), alternate = UIKeyModifierFlags(rawValue: 4), command = UIKeyModifierFlags(rawValue: 8) }
public struct UIKeyboardHIDUsage: RawRepresentable, Sendable { public var rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue } }
@MainActor public class UIKey {
    public var modifierFlags: UIKeyModifierFlags { [] }
    public var keyCode: UIKeyboardHIDUsage { UIKeyboardHIDUsage(rawValue: 0) }
    public var characters: String { "" }
    public var charactersIgnoringModifiers: String { "" }
}
@MainActor public class UIPress: Hashable {
    public var key: UIKey? { nil }
    nonisolated public static func == (a: UIPress, b: UIPress) -> Bool { a === b }
    nonisolated public func hash(into h: inout Hasher) { h.combine(ObjectIdentifier(self)) }
}
@MainActor public class UIPressesEvent {}
extension NotificationCenter {
    // Objective-C-only on Linux Foundation.
    public func addObserver(_ observer: Any, selector aSelector: Selector, name aName: NSNotification.Name?, object anObject: Any?) {}
}
