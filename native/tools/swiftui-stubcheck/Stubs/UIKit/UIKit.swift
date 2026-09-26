@_exported import Foundation

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
public final class UIImage: Sendable {
    public init?(data: Data) {}
    public func pngData() -> Data? { nil }
}
@MainActor public final class UIPasteboard {
    public static let general = UIPasteboard()
    public var string: String?
}
public struct UIKeyboardType: Sendable { public static let `default` = UIKeyboardType(), decimalPad = UIKeyboardType(), emailAddress = UIKeyboardType(), URL = UIKeyboardType(), webSearch = UIKeyboardType(), numberPad = UIKeyboardType() }
public struct UITextContentType: Sendable { public static let password = UITextContentType(), emailAddress = UITextContentType(), URL = UITextContentType() }
public struct UIContentSizeCategory: Sendable { public init() {} }

@MainActor open class UIResponder {}
public struct UIEdgeInsets: Sendable, Equatable {
    public var top: CGFloat = 0, left: CGFloat = 0, bottom: CGFloat = 0, right: CGFloat = 0
    public init() {}
}
@MainActor open class UIView: UIResponder {
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
@MainActor open class UIWindow: UIView {
    public init(frame: CGRect) { super.init(); self.frame = frame }
    public var rootViewController: UIViewController?
    public var isHidden = true
}
public struct UITraitOverrides { public var preferredContentSizeCategory = UIContentSizeCategory() }
@MainActor open class UIViewController {
    public init() {}
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
