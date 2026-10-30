import Foundation
import SwiftUI
import UIKit
import XbinRendererModel
import WebKit
import XbinCore

// A native tile's `canvas` element (plans/native.md §8.5): the one drawing
// escape. `src` is a page of the tile's own, loaded as a web tile is —
// through the workspace's scheme handler with the tile's frame token on
// every request, the tile bridge (§6.2) for its dialogs, the same
// navigation rules — in a fixed-height island. `html` is static markup:
// JavaScript off, a CSP that loads nothing but inline styles and `data:`
// images, an address nothing serves, every navigation refused (a tapped
// link reported to the tile, rev 2), a non-persistent store.

/// A `canvas src=` island: a WebTileController on one of the tile's pages.
struct CanvasIsland: View {
    let controller: WebTileController
    let tile: String

    var body: some View {
        let c = controller
        ZStack {
            WebViewHost(webView: c.webView)
            if let err = c.loadError {
                VStack(spacing: 6) {
                    Label("Can't load this view", systemImage: "exclamationmark.triangle").font(.footnote)
                    Text(verbatim: err).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                    Button("Try again") { c.reload() }.font(.footnote)
                }
                .padding(8)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(.background)
            } else if c.isLoading, c.webView.url == nil || c.progress < 0.3 {
                ProgressView()
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: XbinShapes.radius))
        .onAppear { if c.webView.url == nil { c.load() } }
        .sheet(item: Binding(get: { c.tileDialog },
                             set: { if $0 == nil, let d = c.tileDialog { c.resolveDialog(d.id, button: nil, values: [:]) } })) { d in
            TileDialogSheet(from: tile, dialog: d) { button, values in c.resolveDialog(d.id, button: button, values: values) }
                .presentationDetents([.medium, .large])
        }
        // The page's alert/confirm/prompt as on a web tile: confirm() can be
        // declined, prompt() returns what was typed.
        .alert(c.jsDialog?.message ?? "", isPresented: Binding(get: { c.jsDialog != nil }, set: { _ in })) {
            JSDialogButtons(dialog: c.jsDialog) { ok, text in
                let d = c.jsDialog
                c.jsDialog = nil
                d?.answer(ok, text)
            }
        }
        // A download from the page: the share sheet, as on a web tile.
        .sheet(isPresented: Binding(get: { c.shareItems != nil }, set: { if !$0 { c.shareItems = nil } })) {
            ShareSheet(items: c.shareItems ?? [])
        }
    }
}

/// A `canvas html=` island: static, no scripts, no network. Rev 2 (D208):
/// `zoom` is a CSS zoom on the page (``CanvasDocument/wrap(_:zoom:)``: it
/// reflows; `pageZoom` didn't on an iPhone), the reader kept at the same
/// place in the page across the reload it takes; pinch zoom magnifies on
/// top. A tapped link — which never navigates — reaches the tile as `link
/// {href}` when it listens (`onLink`). The page sits at an address nothing
/// serves (``CanvasDocument/base``), so a relative href resolves at all and
/// comes back relative.
struct StaticCanvas: UIViewRepresentable {
    let html: String
    var zoom: Double = 1
    var onLink: (@MainActor @Sendable (String) -> Void)?

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let c = WKWebViewConfiguration()
        c.websiteDataStore = .nonPersistent()
        c.defaultWebpagePreferences.allowsContentJavaScript = false
        c.preferences.javaScriptCanOpenWindowsAutomatically = false
        c.allowsInlineMediaPlayback = false
        c.mediaTypesRequiringUserActionForPlayback = .all
        c.suppressesIncrementalRendering = true
        let wv = WKWebView(frame: .zero, configuration: c)
        wv.navigationDelegate = context.coordinator
        wv.isOpaque = false
        wv.backgroundColor = .clear
        wv.scrollView.backgroundColor = .clear
        wv.allowsLinkPreview = false
        wv.allowsBackForwardNavigationGestures = false
        context.coordinator.onLink = onLink
        context.coordinator.load(html, zoom: zoom, in: wv)
        return wv
    }

    func updateUIView(_ wv: WKWebView, context: Context) {
        let c = context.coordinator
        c.onLink = onLink
        if c.shown != html {
            c.load(html, zoom: zoom, in: wv)
        } else if c.zoom != zoom {
            // the same page at another zoom: back where the reader was
            let sv = wv.scrollView
            let height = sv.contentSize.height
            c.restore = height > 0 ? Double((sv.contentOffset.y + sv.adjustedContentInset.top) / height) : nil
            c.load(html, zoom: zoom, in: wv)
        }
    }

    static func dismantleUIView(_ wv: WKWebView, coordinator: Coordinator) {
        wv.stopLoading()
        wv.navigationDelegate = nil
    }

    /// Admits only the document it loads itself; every other navigation —
    /// a link, a meta refresh, a form, an iframe — is refused, a tapped
    /// link handed to the tile when it listens (``CanvasDocument/Navigation``).
    @MainActor
    final class Coordinator: NSObject, WKNavigationDelegate {
        private(set) var shown: String?
        private(set) var zoom: Double = 1
        private var admitting = 0
        var onLink: (@MainActor @Sendable (String) -> Void)?
        /// Where the reader was (a fraction of the page's height) before a
        /// zoom reloaded it.
        var restore: Double?

        func load(_ html: String, zoom: Double, in wv: WKWebView) {
            shown = html
            self.zoom = zoom
            admitting += 1
            wv.loadHTMLString(CanvasDocument.wrap(html, zoom: zoom), baseURL: CanvasDocument.baseURL)
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            guard let f = restore else { return }
            restore = nil
            let sv = webView.scrollView
            let top = -sv.adjustedContentInset.top
            let maxY = max(top, sv.contentSize.height - sv.bounds.height + sv.adjustedContentInset.bottom)
            sv.setContentOffset(CGPoint(x: sv.contentOffset.x, y: min(maxY, max(top, CGFloat(f) * sv.contentSize.height + top))), animated: false)
        }

        func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
            let url = navigationAction.request.url?.absoluteString ?? ""
            let decision = CanvasDocument.Navigation.decide(
                url: url, admitting: admitting > 0, mainFrame: navigationAction.targetFrame?.isMainFrame == true,
                linkActivated: navigationAction.navigationType == .linkActivated, listens: onLink != nil)
            switch decision {
            case .allow:
                admitting -= 1
                return .allow
            case .link(let href):
                onLink?(href)
                return .cancel
            case .cancel:
                return .cancel
            }
        }
    }
}

/// A canvas the app won't draw: its `src` isn't one of the tile's pages.
struct CanvasRefused: View {
    let src: String

    var body: some View {
        RoundedRectangle(cornerRadius: XbinShapes.radius)
            .strokeBorder(Color.secondary.opacity(0.4), style: StrokeStyle(lineWidth: 1, dash: [5, 4]))
            .overlay {
                Label("Not a page of this tile: \(src)", systemImage: "exclamationmark.triangle")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .padding(8)
            }
    }
}
