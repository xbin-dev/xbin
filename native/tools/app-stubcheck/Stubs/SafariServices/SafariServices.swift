@_exported import UIKit
@MainActor public final class SFSafariViewController: UIViewController {
    public enum DismissButtonStyle: Sendable { case done, close, cancel }
    public init(url: URL) { super.init() }
    public var dismissButtonStyle: DismissButtonStyle = .done
}
