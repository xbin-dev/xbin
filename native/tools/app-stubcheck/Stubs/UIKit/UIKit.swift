@_exported import Foundation
#if canImport(FoundationNetworking)
@_exported import FoundationNetworking
#endif

public final class CGContext {}

public enum UIUserInterfaceStyle: Int, Sendable { case unspecified, light, dark }
public final class UITraitCollection: Sendable { public var userInterfaceStyle: UIUserInterfaceStyle { .light } }
public final class UIColor: Sendable {
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
}
public final class CGImage: @unchecked Sendable {}
public final class UIImage: Sendable {
    public init?(data: Data) {}
    public init(cgImage: CGImage) {}
    public func pngData() -> Data? { nil }
}
@MainActor public final class UIPasteboard {
    public static let general = UIPasteboard()
    public var string: String?
}
public struct UIKeyboardType: Sendable { public static let `default` = UIKeyboardType(), decimalPad = UIKeyboardType(), emailAddress = UIKeyboardType(), URL = UIKeyboardType(), webSearch = UIKeyboardType(), numberPad = UIKeyboardType() }
public struct UITextContentType: Sendable { public static let password = UITextContentType(), emailAddress = UITextContentType(), URL = UITextContentType() }
public struct UIContentSizeCategory: Sendable { public init() {} }

@MainActor open class UIResponder: NSObject {}
public struct UIEdgeInsets: Sendable, Equatable {
    public var top: CGFloat = 0, left: CGFloat = 0, bottom: CGFloat = 0, right: CGFloat = 0
    public init() {}
}
@MainActor open class UIView: UIResponder {
    public override init() { super.init() }
    public init(frame: CGRect) { super.init() }
    public required init?(coder: NSCoder) { super.init() }
    public var window: UIWindow? { nil }
    public var isUserInteractionEnabled = true
    open func didMoveToWindow() {}
    public func addGestureRecognizer(_ g: UIGestureRecognizer) {}
    public func removeGestureRecognizer(_ g: UIGestureRecognizer) {}
    public var frame: CGRect = CGRect(x: 0, y: 0, width: 0, height: 0)
    public var safeAreaInsets: UIEdgeInsets { UIEdgeInsets() }
    public func drawHierarchy(in rect: CGRect, afterScreenUpdates afterUpdates: Bool) -> Bool { false }
    public var bounds: CGRect { frame }
    public var layer: CALayer { CALayer() }
    public func setNeedsLayout() {}
    public func layoutIfNeeded() {}
    public var overrideUserInterfaceStyle: UIUserInterfaceStyle = .unspecified
}
public final class CALayer { public func render(in ctx: CGContext) {} }
@MainActor open class UIScene: UIResponder {
    public enum ActivationState: Sendable { case unattached, foregroundActive, foregroundInactive, background }
    public var activationState: ActivationState { .foregroundActive }
}
@MainActor open class UIWindowScene: UIScene {
    public var keyWindow: UIWindow? { nil }
    public var windows: [UIWindow] { [] }
}
@MainActor open class UIApplication: UIResponder {
    public static let shared = UIApplication()
    public func open(_ url: URL, options: [String: Any] = [:], completionHandler: (@MainActor @Sendable (Bool) -> Void)? = nil) {}
    public var supportsMultipleScenes: Bool { true }
    public var connectedScenes: Set<UIScene> { [] }
}
@MainActor open class UIWindow: UIView {
    public override init(frame: CGRect) { super.init(); self.frame = frame }
    public required init?(coder: NSCoder) { super.init() }
    public var isKeyWindow: Bool { false }
    nonisolated public static let didBecomeKeyNotification = Notification.Name("UIWindowDidBecomeKeyNotification")
    public init(windowScene: UIWindowScene) { super.init() }
    public var rootViewController: UIViewController?
    public var isHidden = true
}
public struct UITraitOverrides { public var preferredContentSizeCategory = UIContentSizeCategory() }
@MainActor open class UIViewController: UIResponder {
    public override init() {}
    public var presentedViewController: UIViewController? { nil }
    public var isBeingDismissed: Bool { false }
    public func present(_ vc: UIViewController, animated: Bool, completion: (() -> Void)? = nil) {}
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

public enum UIUserInterfaceIdiom: Sendable { case phone, pad, mac, unspecified }
@MainActor public final class UIDevice {
    public static let current = UIDevice()
    public var userInterfaceIdiom: UIUserInterfaceIdiom { .phone }
    public var name: String { "" }
}
@MainActor open class UIFeedbackGenerator { public init() {} }
@MainActor public final class UIImpactFeedbackGenerator: UIFeedbackGenerator {
    public enum FeedbackStyle: Sendable { case light, medium, heavy, soft, rigid }
    public init(style: FeedbackStyle) { super.init() }
    public func impactOccurred() {}
}
@MainActor public final class UINotificationFeedbackGenerator: UIFeedbackGenerator {
    public enum FeedbackType: Sendable { case success, warning, error }
    public func notificationOccurred(_ t: FeedbackType) {}
}
@MainActor public final class UISelectionFeedbackGenerator: UIFeedbackGenerator {
    public func selectionChanged() {}
}
@MainActor open class UIGestureRecognizer: NSObject {
    public init(target: Any?, action: Selector?) {}
    public var view: UIView? { nil }
    public var cancelsTouchesInView = true
    public weak var delegate: (any UIGestureRecognizerDelegate)?
}
@MainActor public protocol UIGestureRecognizerDelegate: NSObjectProtocol {}
@MainActor public final class UISwipeGestureRecognizer: UIGestureRecognizer {
    public struct Direction: OptionSet, Sendable { public let rawValue: UInt; public init(rawValue: UInt) { self.rawValue = rawValue }
        public static let down = Direction(rawValue: 8), up = Direction(rawValue: 4) }
    public var direction: Direction = .down
    public var numberOfTouchesRequired = 1
}
extension ProcessInfo { public var isiOSAppOnMac: Bool { false } }

/// Foundation's on Apple platforms; absent on Linux.
open class NSUserActivity: NSObject, @unchecked Sendable {
    public init(activityType: String) {}
    open var activityType: String { "" }
    open var title: String?
    open var webpageURL: URL?
    open var userInfo: [AnyHashable: Any]?
    open var requiredUserInfoKeys: Set<String>?
    open var isEligibleForHandoff = true
    open var isEligibleForSearch = false
    open var isEligibleForPublicIndexing = false
    open var targetContentIdentifier: String?
}
public protocol NSItemProviderWriting: NSObjectProtocol {}
extension NSUserActivity: NSItemProviderWriting {}
public enum NSItemProviderRepresentationVisibility: Int, Sendable { case all, team, group, ownProcess }
open class NSItemProvider: NSObject, @unchecked Sendable {
    public override init() {}
    open func registerObject(_ object: any NSItemProviderWriting, visibility: NSItemProviderRepresentationVisibility) {}
    open var suggestedName: String?
}

/// Objective-C selectors don't exist on Linux: `#selector(x)` becomes `Selector("x")` here.
public struct Selector: Sendable { public init(_ s: String) {} }
extension NotificationCenter {
    public func addObserver(_ observer: Any, selector: Selector, name: NSNotification.Name?, object: Any?) {}
}
