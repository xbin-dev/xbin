@_exported import UIKit
@MainActor public final class WKWebsiteDataStore {
    public init(forIdentifier: UUID) {}
    public static func nonPersistent() -> WKWebsiteDataStore { WKWebsiteDataStore(forIdentifier: UUID()) }
    public static func remove(forIdentifier: UUID) async throws {}
}
@MainActor public final class WKWebpagePreferences { public var allowsContentJavaScript = true }
@MainActor public final class WKPreferences { public var javaScriptCanOpenWindowsAutomatically = false }
public struct WKAudiovisualMediaTypes: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let all = WKAudiovisualMediaTypes(rawValue: 7) }
@MainActor public final class WKWebViewConfiguration {
    public init() {}
    public var websiteDataStore = WKWebsiteDataStore.nonPersistent()
    public var defaultWebpagePreferences = WKWebpagePreferences()
    public var preferences = WKPreferences()
    public var allowsInlineMediaPlayback = false
    public var mediaTypesRequiringUserActionForPlayback: WKAudiovisualMediaTypes = []
    public var suppressesIncrementalRendering = false
}
@MainActor public final class WKFrameInfo { public var isMainFrame: Bool { true } }
@MainActor public final class WKNavigationAction {
    public var request: URLRequest { URLRequest(url: URL(string: "about:blank")!) }
    public var targetFrame: WKFrameInfo? { nil }
}
public enum WKNavigationActionPolicy: Sendable { case cancel, allow, download }
@MainActor public protocol WKNavigationDelegate: AnyObject {
    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy
}
@MainActor open class WKWebView: UIView {
    public init(frame: CGRect, configuration: WKWebViewConfiguration) { super.init(frame: frame) }
    public required init?(coder: NSCoder) { super.init(coder: coder) }
    public weak var navigationDelegate: (any WKNavigationDelegate)?
    public var scrollView: UIScrollView { UIScrollView() }
    public var allowsLinkPreview = true
    public var allowsBackForwardNavigationGestures = false
    public var url: URL? { nil }
    @discardableResult public func loadHTMLString(_ s: String, baseURL: URL?) -> Int { 0 }
    public func stopLoading() {}
}
