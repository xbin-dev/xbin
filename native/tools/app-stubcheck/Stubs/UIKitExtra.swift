// More UIKit for native/tools/app-stubcheck (what the covered app files use).
import Foundation

extension UIColor {
    public static let clear = UIColor(red: 0, green: 0, blue: 0, alpha: 0)
    public static let black = UIColor(red: 0, green: 0, blue: 0, alpha: 1)
    public func withAlphaComponent(_ a: CGFloat) -> UIColor { self }
}
extension UIImage {
    public func jpegData(compressionQuality: CGFloat) -> Data? { nil }
    public var size: CGSize { .zero }
    public var scale: CGFloat { 1 }
    public func draw(in rect: CGRect) {}
}
extension UIGraphicsImageRendererFormat {
    public static func `default`() -> UIGraphicsImageRendererFormat { UIGraphicsImageRendererFormat() }
}
public final class UIFont: @unchecked Sendable {
    public init() {}
    public struct Weight: Sendable { public static let regular = Weight(), medium = Weight() }
    public static func monospacedSystemFont(ofSize s: CGFloat, weight: Weight) -> UIFont { UIFont() }
}
@MainActor public final class NSLayoutConstraint {
    public static func activate(_ c: [NSLayoutConstraint]) {}
}
@MainActor public final class NSLayoutAnchorStub {
    public func constraint(equalTo other: NSLayoutAnchorStub) -> NSLayoutConstraint { NSLayoutConstraint() }
    public func constraint(equalTo other: NSLayoutAnchorStub, constant: CGFloat) -> NSLayoutConstraint { NSLayoutConstraint() }
    public func constraint(equalToConstant c: CGFloat) -> NSLayoutConstraint { NSLayoutConstraint() }
}
@MainActor public final class UILayoutGuide {
    public var leadingAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
    public var trailingAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
}
extension UIResponder {
    @discardableResult public func becomeFirstResponder() -> Bool { true }
    @discardableResult public func resignFirstResponder() -> Bool { true }
}
extension UIView {
    public var superview: UIView? { nil }
    public func addSubview(_ v: UIView) {}
    public func removeFromSuperview() {}
    public var backgroundColor: UIColor? { get { nil } set {} }
    public var isOpaque: Bool { get { true } set {} }
    public var translatesAutoresizingMaskIntoConstraints: Bool { get { true } set {} }
    public var leadingAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
    public var trailingAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
    public var topAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
    public var bottomAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
    public var heightAnchor: NSLayoutAnchorStub { NSLayoutAnchorStub() }
    public var layoutMarginsGuide: UILayoutGuide { UILayoutGuide() }
    public var inputAccessoryView: UIView? { get { nil } set {} }
}
public enum UIInputViewStyle: Sendable { case `default`, keyboard }
@MainActor open class UIInputView: UIView {
    public init(frame: CGRect, inputViewStyle: UIInputViewStyle) { super.init() }
    public required init?(coder: NSCoder) { super.init() }
    public var allowsSelfSizing = false
}
@MainActor public protocol UIInputViewAudioFeedback { var enableInputClicksWhenVisible: Bool { get } }
@MainActor public final class UIDevice {
    public static let current = UIDevice()
    public func playInputClick() {}
}
@MainActor open class UIStackView: UIView {
    public enum Distribution { case fill, fillEqually }
    public var axis: Axis = .horizontal
    public var distribution: Distribution = .fill
    public var spacing: CGFloat = 0
    public func addArrangedSubview(_ v: UIView) {}
    public enum Axis { case horizontal, vertical }
}
public struct NSDirectionalEdgeInsets: Sendable { public init(top: CGFloat, leading: CGFloat, bottom: CGFloat, trailing: CGFloat) {} }
@MainActor public final class UIAction {
    public init(handler: @escaping @MainActor (UIAction) -> Void) {}
}
@MainActor open class UIControl: UIView {
    public struct Event: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let touchUpInside = Event(rawValue: 1) }
    public func addAction(_ a: UIAction, for e: Event) {}
}
@MainActor public final class UIButton: UIControl {
    public struct Configuration {
        public static func gray() -> Configuration { Configuration() }
        public var title: String?
        public var contentInsets = NSDirectionalEdgeInsets(top: 0, leading: 0, bottom: 0, trailing: 0)
        public var baseBackgroundColor: UIColor?
        public var baseForegroundColor: UIColor?
    }
    public init(configuration: Configuration) { super.init() }
    public var configuration: Configuration? { get { nil } set {} }
}
extension UIView {
    public var accessibilityLabel: String? { get { nil } set {} }
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
extension UIApplication {
    public func open(_ url: URL, options: [String: Any] = [:], completionHandler: ((Bool) -> Void)? = nil) {}
}
@MainActor open class UIScrollView: UIView {
    public var refreshControl: UIRefreshControl?
}
@MainActor public final class UIRefreshControl: UIControl {}
extension URL {
    public func startAccessingSecurityScopedResource() -> Bool { false }
    public func stopAccessingSecurityScopedResource() {}
}
