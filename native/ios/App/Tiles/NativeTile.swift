import Observation
import SwiftUI
import UIKit
import WebKit
import XbinCore
import XbinRenderer

/// A native tile's runtime (plans/native.md §7.2; native/spec/tree.md): a
/// hidden WKWebView loads the xbind-generated runtime document
/// `/c/<tile>/?native=1` through the workspace's scheme handler — the same
/// frame token, sandbox and `window.xbin` as the tile's web page — with the
/// app's caps and the saved state injected at document start. The runtime
/// posts JSON strings to the `xbn` handler; TreeStore applies them; the
/// renderer draws the tree and sends events back with callAsyncJavaScript.
/// No tree within 5 s, a crash, a bad patch or an unsupported primitive
/// falls back to the web tile (§7.6).
@MainActor
@Observable
final class NativeTileRuntime: NSObject {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let store: TreeStore
    let webView: WKWebView
    var lifecycle = NativeTileLifecycle()
    var title: String?
    var shareItems: [Any]?
    var tileDialog: TileDialog?

    @ObservationIgnored var onFallback: ((String) -> Void)?
    @ObservationIgnored private var storeObservation: TreeStoreObservation?
    @ObservationIgnored private var timeout: Task<Void, Never>?
    @ObservationIgnored private var limits = SpawnLimits()

    init(workspace: WorkspaceModel, tile: TileInfo) {
        self.workspace = workspace
        self.tile = tile
        let saved = NativeStateFile.load(workspace: workspace.id, tile: tile.path)
        store = TreeStore(savedState: saved)
        let config = WebTileController.configuration(for: workspace, bridge: false)
        let caps = XbinVocabulary.caps(app: AppInfo.version, renderer: "ios")
        // Caps and state before any page script (tree.md §1), then the
        // tile ↔ app bridge for xbin.dialog in the runtime too.
        config.userContentController.addUserScript(WKUserScript(
            source: RuntimeScript.documentStart(caps: caps, state: saved), injectionTime: .atDocumentStart,
            forMainFrameOnly: true, in: .page))
        config.userContentController.addUserScript(WKUserScript(
            source: TileBridge.userScript, injectionTime: .atDocumentStart, forMainFrameOnly: true, in: .page))
        config.preferences.inactiveSchedulingPolicy = .none
        webView = WKWebView(frame: CGRect(x: 0, y: 0, width: 1, height: 1), configuration: config)
        super.init()
        let ucc = webView.configuration.userContentController
        ucc.add(WeakScriptHandler(self), contentWorld: .page, name: "xbn")
        ucc.add(WeakScriptHandler(self), contentWorld: .page, name: TileBridge.handlerName)
        webView.navigationDelegate = self
        webView.isUserInteractionEnabled = false
        webView.alpha = 0.01
        #if DEBUG
        webView.isInspectable = true
        #endif
        workspace.schemeHandler.register(webView, tile: tile.path)
        storeObservation = store.observe { [weak self] event in
            MainActor.assumeIsolated { self?.handle(event) }
        }
    }

    func start() {
        guard let url = TileScheme.runtimeURL(workspace: workspace.id, tile: tile.path) else {
            fail(.loadFailed("bad tile path"))
            return
        }
        lifecycle.start(at: Date())
        webView.load(URLRequest(url: url))
        timeout?.cancel()
        timeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(NativeTileLifecycle.mountTimeout))
            guard let self, !Task.isCancelled else { return }
            if self.lifecycle.check(at: Date()) { self.fallBack() }
        }
    }

    func stop() {
        timeout?.cancel()
        workspace.schemeHandler.unregister(webView)
        let ucc = webView.configuration.userContentController
        ucc.removeScriptMessageHandler(forName: "xbn", contentWorld: .page)
        ucc.removeScriptMessageHandler(forName: TileBridge.handlerName, contentWorld: .page)
        webView.stopLoading()
    }

    /// Reloads the runtime (pull to refresh, the `reload` event).
    func reload() {
        store.reset()
        lifecycle = NativeTileLifecycle()
        start()
    }

    // MARK: App → runtime

    func call(_ c: RuntimeCall) {
        Task {
            _ = try? await webView.callAsyncJavaScript(c.functionBody, arguments: [:], in: nil, contentWorld: .page)
            // Flush the render the event caused now, not on the throttled
            // hidden document's next animation frame.
            if case .event = c {
                _ = try? await webView.callAsyncJavaScript(RuntimeCall.frame.functionBody, arguments: [:], in: nil, contentWorld: .page)
            }
        }
    }

    func setVisible(_ on: Bool) {
        webView.configuration.preferences.inactiveSchedulingPolicy = on ? .none : .throttle
        call(.visibility(on ? .visible : .hidden))
    }

    // MARK: Runtime → app

    private func handle(_ event: TreeStoreEvent) {
        switch event {
        case .tree:
            lifecycle.mounted()
        case .meta:
            title = store.meta.title
        case .failed(let f):
            fail(.store(f))
        case .state(let v):
            NativeStateFile.save(v, workspace: workspace.id, tile: tile.path)
        case .call(let c):
            perform(c)
        default:
            break
        }
    }

    private func perform(_ c: BridgeCall) {
        switch NativeCallAction.decide(c, canOpenLinks: tile.canOpenLinks) {
        case .copy(let text):
            UIPasteboard.general.string = text
            call(.resolve(id: c.id, value: true))
        case .open(let url):
            UIApplication.shared.open(url)
            call(.resolve(id: c.id, value: true))
        case .share(let text, let url, let file):
            Task { await share(c.id, text: text, url: url, file: file) }
        case .refuse(let why):
            call(.resolve(id: c.id, value: .null, error: why))
        }
    }

    /// The share sheet; a tile file is downloaded with the tile's frame token.
    private func share(_ id: JSONValue, text: String?, url: URL?, file: String?) async {
        var items: [Any] = []
        if let text { items.append(text) }
        if let url { items.append(url) }
        if let file, let r = try? await tileFile(file), r.isSuccess {
            let dest = FileManager.default.temporaryDirectory.appendingPathComponent((file as NSString).lastPathComponent)
            if (try? r.body.write(to: dest)) != nil { items.append(dest) }
        }
        if items.isEmpty {
            call(.resolve(id: id, value: .null, error: "share: nothing could be shared"))
            return
        }
        shareItems = items
        call(.resolve(id: id, value: true))
    }

    /// A tile-relative file, fetched with the tile's frame token (images,
    /// shared files) — never through the bridge (§7.5).
    func tileFile(_ relative: String) async throws -> APIResponse {
        let clean = relative.hasPrefix("./") ? String(relative.dropFirst(2)) : relative
        let path = "/c/\(URLComponent.encodePath(tile.path))/\(clean)"
        let token = try await workspace.frameTokens.token(for: tile.path)
        var headers = [TileScheme.frameTokenHeader: token, TileScheme.clientHeader: AppInfo.clientHeader]
        headers["Accept"] = "*/*"
        return try await workspace.transport.send(APIRequest("GET", path, headers: headers), to: workspace.origin)
    }

    /// What the renderer asks of the app: the clipboard, links (only with
    /// cap:open-links, ND11), tile images by frame token.
    var services: XbinServices {
        // Typed locals: the closures inline in one call (one of them behind
        // a ternary) crashed the type checker's diagnostics on Xcode 27.
        var openLink: (@MainActor (URL) -> Void)?
        if tile.canOpenLinks { openLink = { url in Self.openExternally(url) } }
        let imageData: @MainActor (String) async throws -> Data = { [weak self] src in
            guard let self else { throw URLError(.cancelled) }
            let r = try await self.tileFile(src)
            guard r.isSuccess else { throw APIError(r) }
            return r.body
        }
        return XbinServices(copy: { UIPasteboard.general.string = $0 }, openLink: openLink, imageData: imageData)
    }

    /// A link the tile may open (cap:open-links): http(s) and mailto only.
    private static func openExternally(_ url: URL) {
        guard ["https", "http", "mailto"].contains(url.scheme?.lowercased() ?? "") else { return }
        UIApplication.shared.open(url, options: [:], completionHandler: nil)
    }

    private func fail(_ why: NativeTileLifecycle.Fallback) {
        guard !lifecycle.isFallback else { return }
        lifecycle.fail(why)
        fallBack()
    }

    private func fallBack() {
        timeout?.cancel()
        let banner = lifecycle.fallback?.banner ?? "Showing the web page."
        if case .store(let f)? = lifecycle.fallback { print("xbin native \(tile.path): \(f)") }
        onFallback?(banner)
    }
}

extension NativeTileRuntime: WKNavigationDelegate, WKScriptMessageHandler {
    func userContentController(_ ucc: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame else { return }
        if message.name == "xbn" {
            _ = store.receive(body: message.body)
            return
        }
        guard let s = message.body as? String, let req = TileBridge.parse(.string(s)) else { return }
        switch req {
        case .dialog(let id, let spec):
            if limits.admitDialog(id) { tileDialog = TileDialog(id: id, spec: spec) }
            else { reply(id, DialogSpec.result(button: nil, values: [:])) }
        default:
            // Windows and menus belong to web pages; a native UI uses screens.
            if case .window(let id, _) = req { reply(id, nil) }
        }
    }

    func reply(_ id: String, _ result: JSONValue?) {
        webView.evaluateJavaScript(TileBridge.replyScript(id: id, result: result), in: nil, in: .page) { _ in }
    }

    func resolveDialog(_ id: String, button: JSONValue?, values: [String: JSONValue]) {
        tileDialog = nil
        limits.dialogClosed(id)
        reply(id, DialogSpec.result(button: button, values: values))
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: any Error) {
        fail(.loadFailed(error.localizedDescription))
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error) {
        fail(.loadFailed(error.localizedDescription))
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        fail(.crashed)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationResponse: WKNavigationResponse) async -> WKNavigationResponsePolicy {
        if let h = navigationResponse.response as? HTTPURLResponse, !(200..<300).contains(h.statusCode) {
            fail(.loadFailed("HTTP \(h.statusCode)"))
            return .cancel
        }
        return .allow
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
        // The runtime document never navigates away.
        guard let u = navigationAction.request.url, u.scheme?.lowercased() == TileScheme.scheme,
              u.host?.lowercased() == workspace.id else { return .cancel }
        return .allow
    }
}

/// The screen of a native tile: the renderer over the runtime's tree, the
/// hidden runtime document kept in the view hierarchy (so WebKit keeps its
/// timers running while the tile is on screen).
struct NativeTileScreen: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let fallBack: (String) -> Void

    @State private var runtime: NativeTileRuntime?

    var body: some View {
        ZStack {
            if let rt = runtime {
                WebViewHost(webView: rt.webView)
                    .frame(width: 1, height: 1)
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
                if rt.lifecycle.phase == .live {
                    XbinTreeView(store: rt.store, send: { rt.call($0) }, services: rt.services)
                } else {
                    ProgressView().controlSize(.large)
                }
            }
        }
        .navigationTitle(Text(verbatim: runtime?.title ?? tile.title))
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button("Reload", systemImage: "arrow.clockwise") { runtime?.reload() }
                    Button("Open as web page", systemImage: "globe") {
                        AppSettings.setForcesWeb(workspace.id, tile.path, true)
                        fallBack("Showing the web page — the native view is off for this tile.")
                    }
                } label: { Image(systemName: "ellipsis.circle") }
            }
        }
        .onAppear {
            if runtime == nil {
                let rt = NativeTileRuntime(workspace: workspace, tile: tile)
                rt.onFallback = fallBack
                runtime = rt
                rt.start()
            }
            runtime?.setVisible(true)
        }
        .onDisappear {
            runtime?.setVisible(false)
            runtime?.stop()
        }
        .sheet(item: Binding(get: { runtime?.tileDialog }, set: { if $0 == nil, let d = runtime?.tileDialog { runtime?.resolveDialog(d.id, button: nil, values: [:]) } })) { d in
            TileDialogSheet(from: tile.path, dialog: d) { b, v in runtime?.resolveDialog(d.id, button: b, values: v) }
                .presentationDetents([.medium, .large])
        }
        .sheet(isPresented: Binding(get: { runtime?.shareItems != nil }, set: { if !$0 { runtime?.shareItems = nil } })) {
            ShareSheet(items: runtime?.shareItems ?? [])
        }
    }
}
