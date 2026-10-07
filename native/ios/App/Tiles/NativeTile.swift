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
///
/// The caps ask for the tile's widget too (D125): its `target:"widget"`
/// trees land in `store.widget`, drawn by a phone screen's card through
/// ``widgetModel`` and kept in the workspace's ``WidgetCache``. Runtimes
/// come from ``NativeRuntimePool``, which starts, parks and stops them.
@MainActor
@Observable
final class NativeTileRuntime: NSObject {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let store: TreeStore
    let webView: WKWebView
    /// The widget tree as the renderer draws it (a card's view).
    let widgetModel: XbinTreeModel
    /// The card size the widget is drawn at (the runtime is told).
    private(set) var widgetSize: CardSize
    var lifecycle = NativeTileLifecycle()
    var title: String?
    var shareItems: [Any]?
    var tileDialog: TileDialog?
    /// Its terminal, canvas and attach hatches (TileHatches.swift).
    let hatches: TileHatches

    @ObservationIgnored var onFallback: ((String) -> Void)?
    /// Stopped for good (its page unregistered, its handlers gone).
    @ObservationIgnored private(set) var stopped = false
    @ObservationIgnored private var caps: NativeCaps
    @ObservationIgnored private let widgets: WidgetCache
    @ObservationIgnored private var storeObservation: TreeStoreObservation?
    @ObservationIgnored private var widgetObservation: TreeStoreObservation?
    @ObservationIgnored private var timeout: Task<Void, Never>?
    @ObservationIgnored private var widgetProbe: Task<Void, Never>?
    @ObservationIgnored private var widgetRemounts = 3
    @ObservationIgnored private var limits = SpawnLimits()
    @ObservationIgnored private var visible: Bool?
    /// A deep link that arrived before the tile's first tree (D189): sent
    /// with `xbn.navigate` once the runtime is up.
    @ObservationIgnored private var pendingNavigate: String?

    /// How long after the tile's first mount a runtime that sent no widget
    /// counts as one without (its cards then stay standard for a day).
    static let widgetGrace: Duration = .seconds(3)

    init(workspace: WorkspaceModel, tile: TileInfo, widgetSize: CardSize, widgets: WidgetCache) {
        self.workspace = workspace
        self.tile = tile
        self.widgetSize = widgetSize
        self.widgets = widgets
        let saved = NativeStateFile.load(workspace: workspace.id, tile: tile.path)
        store = TreeStore(savedState: saved)
        // (Every TreeStore made with init(savedState:) has a widget store.)
        widgetModel = XbinTreeModel(store: store.widget ?? TreeStore())
        hatches = TileHatches(workspace: workspace, tile: tile)
        let config = WebTileController.configuration(for: workspace, bridge: false)
        let caps = XbinVocabulary.caps(app: AppInfo.version, renderer: "ios").withWidget(size: widgetSize)
        self.caps = caps
        Self.install(RuntimeScript.startScripts(caps: caps, state: saved), in: config.userContentController)
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
        webView.accessibilityElementsHidden = true
        workspace.schemeHandler.register(webView, tile: tile.path)
        storeObservation = store.observe { [weak self] event in
            MainActor.assumeIsolated { self?.handle(event) }
        }
        widgetObservation = store.widget?.observe { [weak self] event in
            MainActor.assumeIsolated { self?.handleWidget(event) }
        }
        widgetModel.send = { [weak self] call in self?.call(call) }
    }

    /// Loads the runtime document — with a deep link's `fragment`, which the
    /// tile reads from `location.hash` as it starts (D189).
    func start(fragment: String? = nil) {
        guard let url = TileScheme.runtimeURL(workspace: workspace.id, tile: tile.path, fragment: fragment) else {
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

    /// Caps and state before any page script (tree.md §1), then the tile ↔
    /// app bridge for xbin.dialog in the runtime too — for the next load.
    private static func install(_ scripts: [String], in ucc: WKUserContentController) {
        ucc.removeAllUserScripts()
        for s in scripts {
            ucc.addUserScript(WKUserScript(source: s, injectionTime: .atDocumentStart, forMainFrameOnly: true, in: .page))
        }
    }

    /// The end (the pool evicted it, it failed, the workspace signed out).
    func stop() {
        guard !stopped else { return }
        stopped = true
        timeout?.cancel()
        widgetProbe?.cancel()
        hatches.stopAll()
        workspace.schemeHandler.unregister(webView)
        let ucc = webView.configuration.userContentController
        ucc.removeScriptMessageHandler(forName: "xbn", contentWorld: .page)
        ucc.removeScriptMessageHandler(forName: TileBridge.handlerName, contentWorld: .page)
        webView.stopLoading()
    }

    /// Reloads the runtime (the Reload menu, live reload, a grant change),
    /// with the state the tile saved last — not the one this screen opened
    /// with, or the tile's next save would overwrite the newer one.
    func reload() {
        guard !stopped else { return }
        store.reset()
        widgetProbe?.cancel()
        widgetProbe = nil
        widgetRemounts = 3
        lifecycle = NativeTileLifecycle()
        hatches.reloadPages()
        Self.install(RuntimeScript.startScripts(caps: caps, state: store.savedState), in: webView.configuration.userContentController)
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

    /// A deep link into the running view (D189): `xbn.navigate` sets the
    /// document's hash and fires `hashchange`; before the first tree it
    /// waits for the runtime.
    func navigate(_ fragment: String) {
        guard !stopped else { return }
        #if DEBUG
        NSLog("xbin-nav runtime %@ navigate %@ live %@", tile.path, fragment, String(lifecycle.phase == .live))
        #endif
        if lifecycle.phase == .live { call(.navigate(fragment)) } else { pendingNavigate = fragment }
    }

    func setVisible(_ on: Bool) {
        guard visible != on, !stopped else { return }
        visible = on
        webView.configuration.preferences.inactiveSchedulingPolicy = on ? .none : .throttle
        call(.visibility(on ? .visible : .hidden))
    }

    /// The card showing the widget is `size` now: the runtime draws for it
    /// (and a reload starts with it).
    func setWidgetSize(_ size: CardSize) {
        guard size != widgetSize else { return }
        widgetSize = size
        caps.widgetSize = size
        call(.widgetSize(size))
    }

    // MARK: Runtime → app

    private func handle(_ event: TreeStoreEvent) {
        switch event {
        case .tree:
            lifecycle.mounted()
            hatches.prune(store.tree)
            probeWidget()
            if let f = pendingNavigate {
                pendingNavigate = nil
                call(.navigate(f))
            }
        case .meta:
            title = store.meta.title
            workspace.tileMeta.set(tile.path, store.meta)
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

    /// The widget tree changed: keep it for the card when the runtime
    /// isn't live; a patch that didn't apply asks for the whole tree again
    /// (a few times — a runtime that keeps sending bad widget trees gets
    /// the cached card).
    private func handleWidget(_ event: TreeStoreEvent) {
        guard let widget = store.widget else { return }
        switch event {
        case .tree:
            if let root = widget.tree.root { widgets.record(tile.path, root: root, version: widget.version ?? 1, size: widgetSize) }
        case .failed(let f):
            print("xbin native \(tile.path) widget: \(f)")
            guard widgetRemounts > 0 else { return }
            widgetRemounts -= 1
            call(.remount(.widget))
        default:
            break
        }
    }

    /// After the tile's first mount: a runtime that sends no widget soon
    /// after is one without (cards stop starting it for a while).
    private func probeWidget() {
        guard widgetProbe == nil, store.widget?.isMounted == false else { return }
        widgetProbe = Task { [weak self] in
            try? await Task.sleep(for: Self.widgetGrace)
            guard let self, !Task.isCancelled, !self.stopped, self.store.widget?.isMounted == false else { return }
            self.widgets.noteNoWidget(self.tile.path)
        }
    }

    /// What a widget's controls may ask of the app: the clipboard and the
    /// tile's images — no terminals, canvases or attachments on a card.
    var widgetServices: XbinServices {
        let imageData: @MainActor (String) async throws -> Data = { [weak self] src in
            guard let self else { throw URLError(.cancelled) }
            let r = try await self.tileFile(src)
            guard r.isSuccess else { throw APIError(r) }
            return r.body
        }
        let copy: @MainActor (String) -> Void = { UIPasteboard.general.string = $0 }
        return XbinServices(copy: copy, imageData: imageData)
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

    /// A file of the tile, fetched with the tile's frame token (images,
    /// shared files) — never through the bridge (§7.5). Relative to its page,
    /// or its own `/c/<self>/…` or `/api/<self>/…` (TileResource); anything
    /// else is refused.
    func tileFile(_ ref: String) async throws -> APIResponse {
        guard let path = TileResource.assetPath(ref, tile: tile.path, known: workspace.catalog.tiles.map(\.path)) else {
            throw APIError(status: 403, message: "not a file of this tile")
        }
        let tokens = workspace.frameTokens
        let token = try await tokens.token(for: tile.path)
        let r = try await getFile(path, token: token)
        guard r.status == 401 else { return r }
        // The token died with the session behind it: renew it once.
        await tokens.invalidate(tile.path, token: token)
        return try await getFile(path, token: try await tokens.renew(tile.path))
    }

    private func getFile(_ path: String, token: String) async throws -> APIResponse {
        var headers = [TileScheme.frameTokenHeader: token, TileScheme.clientHeader: AppInfo.clientHeader]
        headers["Accept"] = "*/*"
        return try await workspace.transport.send(APIRequest("GET", path, headers: headers), to: workspace.origin)
    }

    /// What the renderer asks of the app: the clipboard, links (only with
    /// cap:open-links, ND11), tile images by frame token, and the escape
    /// hatches — terminal, canvas, attach (TileHatches).
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
        let copy: @MainActor (String) -> Void = { UIPasteboard.general.string = $0 }
        return hatches.services(copy: copy, openLink: openLink, imageData: imageData)
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

/// The screen of a native tile: the renderer over the runtime's tree. The
/// runtime comes from ``NativeRuntimePool`` — warm when the tile's widget
/// ran it, kept warm after the screen goes — which parks its hidden
/// document in the window (so WebKit keeps its timers running).
struct NativeTileScreen: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    /// A deep link's fragment (`#c=42`): the runtime starts with it, or a
    /// running one navigates to it (D189).
    var fragment: String?
    let fallBack: (String) -> Void

    @Environment(WorkspaceNav.self) private var nav
    @State private var runtime: NativeTileRuntime?
    /// This screen's claim on the runtime (the pool pins it while held).
    @State private var claim: UUID?
    /// The deep link already handed to the runtime (D189): coming back from
    /// a page the tile pushed onto this stack doesn't open it again.
    @State private var handledFragment: String?

    var body: some View {
        ZStack {
            if let rt = runtime {
                if rt.lifecycle.phase == .live {
                    // In the panel's NavigationStack: a collapsing split pushes
                    // onto it rather than nesting a stack (D189).
                    XbinTreeView(store: rt.store, send: { rt.call($0) }, services: rt.services,
                                 options: XbinRenderOptions(hostNavigation: true))
                        .modifier(AttachPickers(picker: rt.hatches.attach.picker))
                        .overlay(alignment: .bottom) { AttachStatusView(flow: rt.hatches.attach) }
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
                    // The tile's sessions screen (D132): its tabs and tools.
                    Button("Sessions & tools", systemImage: "rectangle.stack") {
                        workspace.open(.sessions(tile: tile.path, show: .first), in: nav)
                    }
                    Button("New session…", systemImage: "plus.rectangle.on.rectangle") {
                        workspace.open(.sessions(tile: tile.path, show: .launcher), in: nav)
                    }
                } label: { Image(systemName: "ellipsis.circle") }
            }
        }
        .onAppear {
            let pool = NativeRuntimePool.shared
            if let rt = runtime, !rt.stopped, claim != nil {
                rt.setVisible(true) // back from under a window it pushed
                return
            }
            if let id = claim { pool.close(workspace, tile.path, screen: id) }
            let id = UUID()
            let link = NativeHostedStack.fragment(fragment, handled: handledFragment)
            handledFragment = fragment
            let rt = pool.open(workspace, tile, screen: id, fragment: link, fallBack: fallBack)
            rt.hatches.nav = nav // canvas islands push onto this window (Navigation.swift)
            claim = id
            runtime = rt
        }
        .onDisappear {
            // Covered by a page its own tree pushed onto this stack (a
            // split's detail, D189): the window still shows this tile — it
            // stays live and visible. Covered by a window this tile pushed
            // (an island's xbin.window): it shows again on the pop, islands
            // and all — keep the claim. Gone: let the pool have it.
            if case .tile(let path, _, _)? = nav.surface, path == tile.path, !nav.stillStacked(window: nil) { return }
            if nav.stillStacked(window: nil) {
                runtime?.setVisible(false)
            } else if let id = claim {
                NativeRuntimePool.shared.close(workspace, tile.path, screen: id)
                claim = nil
            }
        }
        // Another deep link to the open tile (D189).
        .onChange(of: fragment) { _, f in
            if let link = NativeHostedStack.fragment(f, handled: handledFragment) { runtime?.navigate(link) }
            handledFragment = f
        }
        // Live reload (§7.7): the tile's source changed — remount.
        .task(id: tile.path) { await workspace.events.onReload(of: tile.path) { runtime?.reload() } }
        .sheet(item: Binding(get: { runtime?.tileDialog }, set: { if $0 == nil, let d = runtime?.tileDialog { runtime?.resolveDialog(d.id, button: nil, values: [:]) } })) { d in
            TileDialogSheet(from: tile.path, dialog: d) { b, v in runtime?.resolveDialog(d.id, button: b, values: v) }
                .presentationDetents([.medium, .large])
        }
        .sheet(isPresented: Binding(get: { runtime?.shareItems != nil }, set: { if !$0 { runtime?.shareItems = nil } })) {
            ShareSheet(items: runtime?.shareItems ?? [])
        }
    }
}
