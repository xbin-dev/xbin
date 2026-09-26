import Foundation
import Observation
import UIKit
import WebKit
import XbinCore

/// A JavaScript `alert`/`confirm`/`prompt` waiting for the user.
struct JSDialog: Identifiable {
    enum Kind { case alert, confirm, prompt(defaultText: String) }
    let id = UUID()
    var kind: Kind
    var message: String
    var answer: (Bool, String?) -> Void
}

/// A tile's `xbin.dialog` waiting for the user.
struct TileDialog: Identifiable {
    let id: String
    var spec: DialogSpec
}

/// One tile page (plans/native.md §6): a WKWebView on the workspace's
/// scheme handler and data store, the bridge (§6.2) and the page's state
/// for the native chrome (§6.3).
@MainActor
@Observable
final class WebTileController: NSObject {
    let workspace: WorkspaceModel
    let tile: String
    let canOpenLinks: Bool
    let webView: WKWebView

    var progress: Double = 0
    var isLoading = true
    var loadError: String?
    var pageTitle: String = ""
    var jsDialog: JSDialog?
    var tileDialog: TileDialog?
    var shareItems: [Any]?

    @ObservationIgnored private var limits = SpawnLimits()
    @ObservationIgnored private var observations: [NSKeyValueObservation] = []
    @ObservationIgnored private var backgroundedAt: Date?
    @ObservationIgnored private var downloads: [ObjectIdentifier: URL] = [:]
    @ObservationIgnored private let initialURL: URL?

    convenience init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, subpath: String = "", query: String? = nil,
                     fragment: String? = nil) {
        self.init(workspace: workspace, tile: tile, canOpenLinks: canOpenLinks,
                  url: TileScheme.pageURL(workspace: workspace.id, tile: tile, subpath: subpath, query: query, fragment: fragment))
    }

    /// A page of `tile` by its scheme URL. An `island` (a native tile's
    /// `canvas src`, TileHatches) sits inside a native screen: no pull to
    /// refresh, no back/forward swipes.
    init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, url: URL?, island: Bool = false) {
        self.workspace = workspace
        self.tile = tile
        self.canOpenLinks = canOpenLinks
        initialURL = url
        webView = WKWebView(frame: .zero, configuration: Self.configuration(for: workspace, bridge: true))
        super.init()
        webView.configuration.userContentController.add(WeakScriptHandler(self), contentWorld: .page, name: TileBridge.handlerName)
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.allowsBackForwardNavigationGestures = !island
        webView.customUserAgent = nil
        #if DEBUG
        webView.isInspectable = true
        #endif
        if !island {
            let refresh = UIRefreshControl()
            refresh.addTarget(self, action: #selector(pulled(_:)), for: .valueChanged)
            webView.scrollView.refreshControl = refresh
        }
        workspace.schemeHandler.register(webView, tile: tile)
        observations = [
            webView.observe(\.estimatedProgress) { [weak self] wv, _ in
                MainActor.assumeIsolated { self?.progress = wv.estimatedProgress }
            },
            webView.observe(\.title) { [weak self] wv, _ in
                MainActor.assumeIsolated { self?.pageTitle = wv.title ?? "" }
            },
        ]
    }

    /// A configuration for this workspace: its data store (§4), its scheme
    /// handler (§6.1), and — for tile pages — the bridge script (§6.2).
    static func configuration(for ws: WorkspaceModel, bridge: Bool) -> WKWebViewConfiguration {
        let c = WKWebViewConfiguration()
        c.websiteDataStore = ws.dataStore
        c.setURLSchemeHandler(ws.schemeHandler, forURLScheme: TileScheme.scheme)
        c.allowsInlineMediaPlayback = true
        c.preferences.javaScriptCanOpenWindowsAutomatically = false
        c.defaultWebpagePreferences.preferredContentMode = .mobile
        let ucc = WKUserContentController()
        if bridge {
            ucc.addUserScript(WKUserScript(source: TileBridge.userScript, injectionTime: .atDocumentStart,
                                           forMainFrameOnly: true, in: .page))
        }
        c.userContentController = ucc
        return c
    }

    func load() {
        guard let u = initialURL else { loadError = "Bad tile path"; return }
        loadError = nil
        webView.load(URLRequest(url: u))
    }

    func reload() {
        loadError = nil
        if webView.url == nil { load() } else { webView.reload() }
    }

    @objc private func pulled(_ sender: UIRefreshControl) {
        reload()
        sender.endRefreshing()
    }

    func close() {
        workspace.schemeHandler.unregister(webView)
        webView.configuration.userContentController.removeScriptMessageHandler(forName: TileBridge.handlerName, contentWorld: .page)
        webView.stopLoading()
    }

    /// Scene phase: a page suspended longer than its frame token (15 min)
    /// reloads (its own WebSockets carry the page's token).
    func didEnterBackground() { backgroundedAt = Date() }

    func willEnterForeground() {
        if let t = backgroundedAt, Date().timeIntervalSince(t) > 14 * 60 { reload() }
        backgroundedAt = nil
    }

    /// The page on the server, for Safari.
    var safariURL: URL? { workspace.origin.url(path: "/c/\(URLComponent.encodePath(tile))/") }

    // MARK: Bridge replies

    func reply(_ id: String, _ result: JSONValue?) {
        webView.evaluateJavaScript(TileBridge.replyScript(id: id, result: result), in: nil, in: .page) { _ in }
    }

    func resolveDialog(_ id: String, button: JSONValue?, values: [String: JSONValue]) {
        tileDialog = nil
        limits.dialogClosed(id)
        reply(id, DialogSpec.result(button: button, values: values))
    }

    /// A window this page opened was closed (popped, or it asked).
    func windowClosed(_ id: String) {
        if limits.windowClosed(id) { reply(id, nil) }
    }

    fileprivate func handle(_ request: TileBridge.Request) {
        switch request {
        case .dialog(let id, let spec):
            guard limits.admitDialog(id) else {
                reply(id, DialogSpec.result(button: nil, values: [:]))
                return
            }
            tileDialog = TileDialog(id: id, spec: spec)
        case .window(let id, let spec):
            guard limits.admitWindow(id) else { reply(id, nil); return }
            let target = spec.target(from: tile)
            workspace.windows.append(PushedWindow(fromTile: tile, target: target, title: spec.displayTitle(from: tile), replyID: id))
            WindowReplies.shared.register(id) { [weak self] in self?.windowClosed(id) }
        case .windowClose(let id):
            workspace.windows.removeAll { $0.replyID == id }
            windowClosed(id)
        case .contextMenu:
            break // iOS long-press already shows WebKit's own menu
        }
    }
}

/// Who to tell when a pushed window goes away (by its request id).
@MainActor
final class WindowReplies {
    static let shared = WindowReplies()
    private var closers: [String: () -> Void] = [:]
    func register(_ id: String, _ f: @escaping () -> Void) { closers[id] = f }
    func closed(_ id: String) { closers.removeValue(forKey: id)?() }
}

// MARK: - WebKit delegates

extension WebTileController: WKNavigationDelegate, WKUIDelegate {
    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        isLoading = true
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        isLoading = false
        loadError = nil
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error) {
        isLoading = false
        if (error as NSError).code != NSURLErrorCancelled { loadError = error.localizedDescription }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: any Error) {
        isLoading = false
        if (error as NSError).code != NSURLErrorCancelled { loadError = error.localizedDescription }
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        loadError = "The page stopped (its web process ended)."
    }

    /// Navigation stays on this workspace's scheme; a link elsewhere opens
    /// in Safari only with cap:open-links (ND11), as the browser sandbox allows.
    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let url = navigationAction.request.url else { return .cancel }
        if url.scheme?.lowercased() == TileScheme.scheme {
            return url.host?.lowercased() == workspace.id ? .allow : .cancel
        }
        if url.scheme == "about" || url.scheme == "blob" || url.scheme == "data" { return .allow }
        if navigationAction.targetFrame?.isMainFrame == false { return .allow } // iframes load what they load
        if canOpenLinks, navigationAction.navigationType == .linkActivated, ["http", "https", "mailto"].contains(url.scheme ?? "") {
            _ = await UIApplication.shared.open(url)
        }
        return .cancel
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationResponse: WKNavigationResponse) async -> WKNavigationResponsePolicy {
        if let h = navigationResponse.response as? HTTPURLResponse,
           (h.value(forHTTPHeaderField: "Content-Disposition") ?? "").lowercased().hasPrefix("attachment") {
            return .download
        }
        return navigationResponse.canShowMIMEType ? .allow : .download
    }

    func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) {
        download.delegate = self
    }

    func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) {
        download.delegate = self
    }

    /// `window.open` / `target=_blank`: Safari, only with cap:open-links.
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if canOpenLinks, let url = navigationAction.request.url, ["http", "https"].contains(url.scheme ?? "") {
            UIApplication.shared.open(url)
        }
        return nil
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo) async {
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            jsDialog = JSDialog(kind: .alert, message: message) { _, _ in c.resume() }
        }
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo) async -> Bool {
        await withCheckedContinuation { (c: CheckedContinuation<Bool, Never>) in
            jsDialog = JSDialog(kind: .confirm, message: message) { ok, _ in c.resume(returning: ok) }
        }
    }

    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?,
                 initiatedByFrame frame: WKFrameInfo) async -> String? {
        await withCheckedContinuation { (c: CheckedContinuation<String?, Never>) in
            jsDialog = JSDialog(kind: .prompt(defaultText: defaultText ?? ""), message: prompt) { ok, text in
                c.resume(returning: ok ? (text ?? "") : nil)
            }
        }
    }
}

extension WebTileController: WKDownloadDelegate {
    func download(_ download: WKDownload, decideDestinationUsing response: URLResponse, suggestedFilename: String) async -> URL? {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let name = suggestedFilename.isEmpty ? "download" : (suggestedFilename as NSString).lastPathComponent
        let url = dir.appendingPathComponent(name)
        downloads[ObjectIdentifier(download)] = url
        return url
    }

    func downloadDidFinish(_ download: WKDownload) {
        // The file is handed to the share sheet (Files, AirDrop, …).
        if let url = downloads.removeValue(forKey: ObjectIdentifier(download)) { shareItems = [url] }
    }

    func download(_ download: WKDownload, didFailWithError error: any Error, resumeData: Data?) {
        loadError = "Download failed: \(error.localizedDescription)"
    }
}

extension WebTileController: WKScriptMessageHandler {
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        // Only the page itself speaks for the tile (as only a tile's own
        // iframe does in the browser), never a frame inside it.
        guard message.frameInfo.isMainFrame, let s = message.body as? String,
              let req = TileBridge.parse(.string(s)) else { return }
        handle(req)
    }
}

/// WKUserContentController retains its handlers: this breaks the cycle.
@MainActor
final class WeakScriptHandler: NSObject, WKScriptMessageHandler {
    private weak var target: (any WKScriptMessageHandler)?
    init(_ target: any WKScriptMessageHandler) { self.target = target }
    func userContentController(_ ucc: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(ucc, didReceive: message)
    }
}
