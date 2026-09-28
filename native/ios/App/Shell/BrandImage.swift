import Observation
import SwiftUI
import UIKit
import WebKit
import XbinCore

/// A workspace's branding icon as an image (D76, D128). The icon is a
/// `data:` URI — PNG, JPEG, WebP, ICO or SVG (docs/protocol.md
/// `GET /branding`): XbinCore decodes it once (DataURICache), ImageIO draws
/// the bitmaps, and an SVG is drawn once by an offscreen web view — script
/// off, no navigation, nothing fetched — then kept as a bitmap. Views that
/// asked while it rendered are redrawn when it lands.
@MainActor
@Observable
final class BrandImages {
    static let shared = BrandImages()

    /// Bumped when an SVG lands: views reading ``image(_:)`` redraw.
    private(set) var generation = 0
    @ObservationIgnored private var images: [String: UIImage] = [:]
    @ObservationIgnored private var failed: Set<String> = []
    @ObservationIgnored private var rendering: [String: SVGSnapshot] = [:]

    /// The icon `uri` as an image; nil while an SVG renders, or when it
    /// isn't an image the app can draw (the caller draws its fallback).
    func image(_ uri: String?) -> UIImage? {
        _ = generation
        guard let uri, !uri.isEmpty else { return nil }
        if let i = images[uri] { return i }
        guard !failed.contains(uri), rendering[uri] == nil else { return nil }
        guard let d = DataURICache.shared.decode(uri), d.mediaType.hasPrefix("image/") else {
            failed.insert(uri)
            return nil
        }
        if d.isSVG {
            let r = SVGSnapshot(svg: d.data) { [weak self] image in self?.landed(uri, image) }
            rendering[uri] = r
            Task { r.start() } // after this view update: it adds a view to the window
            return nil
        }
        guard let i = UIImage(data: d.data) else {
            failed.insert(uri)
            return nil
        }
        images[uri] = i
        return i
    }

    private func landed(_ uri: String, _ image: UIImage?) {
        rendering[uri] = nil
        if let image { images[uri] = image } else { failed.insert(uri) }
        generation += 1
    }
}

/// Draws an SVG once, at a few points' worth of pixels, in a web view that
/// runs no script and loads nothing but its own document; attached behind
/// the key window's content (WebKit paints only views in a window), then
/// removed.
@MainActor
private final class SVGSnapshot: NSObject, WKNavigationDelegate {
    static let side: CGFloat = 96

    private let svg: Data
    private let done: (UIImage?) -> Void
    private var web: WKWebView?
    private var admitted = false
    private var finished = false

    init(svg: Data, done: @escaping (UIImage?) -> Void) {
        self.svg = svg
        self.done = done
    }

    func start() {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.defaultWebpagePreferences.allowsContentJavaScript = false
        let web = WKWebView(frame: CGRect(x: 0, y: 0, width: Self.side, height: Self.side), configuration: config)
        web.isOpaque = false
        web.backgroundColor = .clear
        web.scrollView.backgroundColor = .clear
        // At the window's corner, under the status bar: no safe-area inset
        // may push the picture down (it did: only its top showed).
        web.scrollView.contentInsetAdjustmentBehavior = .never
        web.scrollView.isScrollEnabled = false
        web.isUserInteractionEnabled = false
        web.accessibilityElementsHidden = true
        web.navigationDelegate = self
        self.web = web
        guard let window = Self.window() else {
            finish(nil)
            return
        }
        window.insertSubview(web, at: 0)
        let src = "data:image/svg+xml;base64," + svg.base64EncodedString()
        let page = """
        <!doctype html><html><head><meta name="viewport" content="width=\(Int(Self.side))">
        <style>html,body{margin:0;padding:0;background:transparent;overflow:hidden}
        img{display:block;width:100vw;height:100vh;object-fit:contain}</style></head>
        <body><img src="\(src)" alt=""></body></html>
        """
        web.loadHTMLString(page, baseURL: nil)
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 10_000_000_000)
            self?.finish(nil)
        }
    }

    private static func window() -> UIWindow? {
        let windows = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.flatMap(\.windows)
        return windows.first(where: \.isKeyWindow) ?? windows.first
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
        // Its own document once; nothing else, ever.
        guard !admitted, navigationAction.request.url?.absoluteString == "about:blank" else { return .cancel }
        admitted = true
        return .allow
    }

    /// Loaded: the picture drawn onto a canvas by the app's own script (the
    /// page's may not run) and read back as a PNG — a snapshot of the view
    /// caught it mid-render (smeared, with stray edges). A snapshot is the
    /// fallback when the canvas can't be read.
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        Task { [weak self] in
            let px = Int(Self.side * 2)
            let js = """
            const img = document.querySelector('img'); await img.decode();
            const c = document.createElement('canvas'); c.width = c.height = \(px);
            c.getContext('2d').drawImage(img, 0, 0, \(px), \(px));
            return c.toDataURL('image/png');
            """
            let url = try? await webView.callAsyncJavaScript(js, arguments: [:], in: nil, contentWorld: .defaultClient) as? String
            if let url, let d = DataURI.parse(url), let image = UIImage(data: d.data) {
                self?.finish(image)
                return
            }
            let config = WKSnapshotConfiguration()
            config.rect = webView.bounds
            config.afterScreenUpdates = true
            webView.takeSnapshot(with: config) { image, _ in
                MainActor.assumeIsolated { self?.finish(image) }
            }
        }
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error) { finish(nil) }

    private func finish(_ image: UIImage?) {
        guard !finished else { return }
        finished = true
        web?.navigationDelegate = nil
        web?.stopLoading()
        web?.removeFromSuperview()
        web = nil
        done(image)
    }
}
