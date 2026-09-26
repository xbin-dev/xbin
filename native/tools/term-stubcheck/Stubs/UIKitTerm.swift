// Extra UIKit declarations the terminal uses (the real SDK's signatures,
// as far as known), on top of ../../swiftui-stubcheck/Stubs/UIKit (which
// holds UIResponder/UIView/UIControl/UIFont and the anchors both use).

public struct NSDirectionalEdgeInsets: Sendable, Equatable {
    public init(top: CGFloat, leading: CGFloat, bottom: CGFloat, trailing: CGFloat) {}
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
@MainActor open class UIInputView: UIView {
    public enum Style: Int, Sendable { case `default`, keyboard }
    public init(frame: CGRect, inputViewStyle: UIInputView.Style) { super.init(frame: frame) }
    public required init?(coder: NSCoder) { super.init(coder: coder) }
    public var allowsSelfSizing = false
}
@MainActor public protocol UIInputViewAudioFeedback { var enableInputClicksWhenVisible: Bool { get } }
@MainActor public final class UIDevice { public static let current = UIDevice(); public func playInputClick() {} }

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

extension UIColor {
    public static let black = UIColor(red: 0, green: 0, blue: 0, alpha: 1)
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

extension NotificationCenter {
    // Objective-C-only on Linux Foundation.
    public func addObserver(_ observer: Any, selector aSelector: Selector, name aName: NSNotification.Name?, object anObject: Any?) {}
}
