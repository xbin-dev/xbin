// The UIKit the app uses beyond swiftui-stubcheck's (the renderer's) and
// term-stubcheck's (the terminal's) stubs — declarations from memory of the
// SDK, like the rest of these stubs. Plus the Foundation types Apple
// platforms have and Linux's Foundation lacks (NSUserActivity,
// NSItemProvider), and FoundationNetworking re-exported (URLSession).
#if canImport(FoundationNetworking)
@_exported import FoundationNetworking
#endif

public final class CGImage: @unchecked Sendable {}
extension UIImage {
    public convenience init(cgImage: CGImage) { self.init(data: Data())! }
    public func jpegData(compressionQuality: CGFloat) -> Data? { nil }
    public var scale: CGFloat { 1 }
    public func draw(in rect: CGRect) {}
}
extension UIGraphicsImageRendererFormat {
    public static func `default`() -> UIGraphicsImageRendererFormat { UIGraphicsImageRendererFormat() }
}

// Scenes, windows, the application.
extension UIScene {
    public enum ActivationState: Sendable { case unattached, foregroundActive, foregroundInactive, background }
    public var activationState: ActivationState { .foregroundActive }
}
extension UIWindowScene {
    public var keyWindow: UIWindow? { nil }
    public var windows: [UIWindow] { [] }
}
extension UIApplication {
    public func open(_ url: URL, options: [String: Any] = [:], completionHandler: (@MainActor @Sendable (Bool) -> Void)? = nil) {}
    public var supportsMultipleScenes: Bool { true }
}
extension UIWindow {
    public var isKeyWindow: Bool { false }
    nonisolated public static let didBecomeKeyNotification = Notification.Name("UIWindowDidBecomeKeyNotification")
}
extension UIViewController {
    public var presentedViewController: UIViewController? { nil }
    public var isBeingDismissed: Bool { false }
    public func present(_ vc: UIViewController, animated: Bool, completion: (() -> Void)? = nil) {}
}
@MainActor open class UINavigationController: UIViewController {}
@MainActor public protocol UINavigationControllerDelegate: AnyObject {}
@MainActor public protocol UIImagePickerControllerDelegate: AnyObject {
    func imagePickerController(_ picker: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any])
    func imagePickerControllerDidCancel(_ picker: UIImagePickerController)
}
@MainActor open class UIImagePickerController: UINavigationController {
    public enum SourceType: Sendable { case photoLibrary, camera }
    public struct InfoKey: Hashable, Sendable { public static let originalImage = InfoKey() }
    public static func isSourceTypeAvailable(_ t: SourceType) -> Bool { true }
    public var sourceType: SourceType = .photoLibrary
    public var mediaTypes: [String] = []
    public weak var delegate: (any UIImagePickerControllerDelegate & UINavigationControllerDelegate)?
}
@MainActor public final class UIRefreshControl: UIControl {}
extension UIScrollView {
    public var refreshControl: UIRefreshControl? { get { nil } set {} }
}

// The device and haptics.
public enum UIUserInterfaceIdiom: Sendable { case phone, pad, mac, unspecified }
extension UIDevice {
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
@MainActor public final class UISwipeGestureRecognizer: UIGestureRecognizer {
    public struct Direction: OptionSet, Sendable { public let rawValue: UInt; public init(rawValue: UInt) { self.rawValue = rawValue }
        public static let down = Direction(rawValue: 8), up = Direction(rawValue: 4) }
    public var direction: Direction = .down
    public var numberOfTouchesRequired = 1
}
extension ProcessInfo { public var isiOSAppOnMac: Bool { false } }
extension URL {
    public func startAccessingSecurityScopedResource() -> Bool { false }
    public func stopAccessingSecurityScopedResource() {}
}

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
