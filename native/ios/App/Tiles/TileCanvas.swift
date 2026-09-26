import Foundation
import SwiftUI
import UIKit
import WebKit
import XbinCore

// A native tile's `canvas` element (plans/native.md §8.5): the one drawing
// escape. `src` is a page of the tile's own, loaded as a web tile is —
// through the workspace's scheme handler with the tile's frame token on
// every request, the tile bridge (§6.2) for its dialogs, the same
// navigation rules — in a fixed-height island. `html` is static markup:
// JavaScript off, a CSP that loads nothing but inline styles and `data:`
// images, no base URL, every navigation refused, a non-persistent store.

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
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
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

/// A `canvas html=` island: static, no scripts, no network.
struct StaticCanvas: UIViewRepresentable {
    let html: String

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
        context.coordinator.load(html, in: wv)
        return wv
    }

    func updateUIView(_ wv: WKWebView, context: Context) {
        if context.coordinator.shown != html { context.coordinator.load(html, in: wv) }
    }

    static func dismantleUIView(_ wv: WKWebView, coordinator: Coordinator) {
        wv.stopLoading()
        wv.navigationDelegate = nil
    }

    /// Admits only the document it loads itself; every other navigation —
    /// a link, a meta refresh, a form, an iframe — is refused.
    @MainActor
    final class Coordinator: NSObject, WKNavigationDelegate {
        private(set) var shown: String?
        private var admitting = 0

        func load(_ html: String, in wv: WKWebView) {
            shown = html
            admitting += 1
            wv.loadHTMLString(CanvasDocument.wrap(html), baseURL: nil)
        }

        func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
            let url = navigationAction.request.url?.absoluteString ?? ""
            if admitting > 0, navigationAction.targetFrame?.isMainFrame == true, url == "about:blank" {
                admitting -= 1
                return .allow
            }
            return .cancel
        }
    }
}

/// A canvas the app won't draw: its `src` isn't one of the tile's pages.
struct CanvasRefused: View {
    let src: String

    var body: some View {
        RoundedRectangle(cornerRadius: 10, style: .continuous)
            .strokeBorder(Color.secondary.opacity(0.4), style: StrokeStyle(lineWidth: 1, dash: [5, 4]))
            .overlay {
                Label("Not a page of this tile: \(src)", systemImage: "exclamationmark.triangle")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .padding(8)
            }
    }
}
