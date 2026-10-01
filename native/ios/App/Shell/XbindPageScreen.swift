import SwiftUI
import UIKit
import WebKit
import XbinCore

/// One of xbind's own pages, full screen (D181): the partitions page — a
/// person's partitions, consents, personal binds, the credentials someone
/// else made for them (Allow / Refuse) and a tile manager's switch
/// decisions. xbind serves it top-level only (it refuses frames), with the
/// person's own sign-in, so it gets a web view of its own: the chrome
/// store, no tile bridge, signed in by a one-shot web ticket — never a
/// tile's frame, never Safari, never the plain page (WebTileController's
/// page mode). It opens from a push linking `xbin/partitions`, an `xbin://`
/// link and Settings → Your partitions, each only once the workspace lists
/// the page's feature (WorkspaceModel.openPage); a window restored onto it
/// asks again here.
struct XbindPageScreen: View {
    let workspace: WorkspaceModel
    let page: XbindPage

    @State private var controller: WebTileController?
    @State private var gate: XbindPageAvailability?
    @Environment(WorkspaceNav.self) private var nav
    @Environment(\.panelActive) private var panelActive

    var body: some View {
        ZStack(alignment: .top) {
            switch gate {
            case .served?:
                if let c = controller {
                    WebViewHost(webView: c.webView)
                        .ignoresSafeArea(.container, edges: .bottom)
                    if c.isLoading, c.progress < 1 {
                        ProgressView(value: c.progress).progressViewStyle(.linear).tint(.accentColor)
                    }
                    if let err = c.loadError {
                        ContentUnavailableView {
                            Label("Can't open \(page.title.lowercased())", systemImage: "exclamationmark.triangle")
                        } description: {
                            Text(verbatim: err)
                        } actions: {
                            Button("Try again") { c.reload() }.buttonStyle(.borderedProminent)
                        }
                        .background(.background)
                    }
                }
            case .notServed?:
                ContentUnavailableView {
                    Label("No partitions page here", systemImage: "rectangle.split.2x1")
                } description: {
                    Text("This workspace's xbind doesn't serve it — it is older than partitioned tiles, or doesn't keep people's data apart.")
                } actions: {
                    Button("Go to the workspace") { nav.goHome() }
                }
            case .unknown?:
                ContentUnavailableView {
                    Label("Can't reach the workspace", systemImage: "wifi.exclamationmark")
                } description: {
                    Text("It didn't say whether it has a partitions page.")
                } actions: {
                    Button("Try again") { Task { await check() } }.buttonStyle(.borderedProminent)
                }
            case nil:
                ProgressView().controlSize(.large).padding(.top, 80)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .navigationTitle(Text(verbatim: page.title))
        .navigationBarTitleDisplayMode(.inline)
        .background {
            if let bg = controller?.pageBackground { Color(uiColor: bg).ignoresSafeArea() }
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                if controller?.canGoBack == true {
                    Button { controller?.goBack() } label: { Image(systemName: "arrow.uturn.backward") }
                        .accessibilityLabel("Page back")
                }
                if gate == .served {
                    Button { controller?.reload() } label: { Image(systemName: "arrow.clockwise") }
                        .accessibilityLabel("Reload")
                }
            }
        }
        .task { if gate == nil { await check() } }
        // Kept for a forward swipe, it loads afresh (a new ticket) when back.
        .onAppear { controller?.reopen() }
        .onDisappear { controller?.close() }
        .onChange(of: panelActive) { _, active in controller?.webView.accessibilityElementsHidden = !active }
        // Shown, the page lists what its notifications said: those go.
        .onChange(of: controller?.isLoading) { _, loading in
            guard loading == false, let c = controller, c.loadError == nil, c.webView.url?.path == page.path else { return }
            PushManager.shared.clearDelivered(workspace: workspace.id, link: page.link)
        }
        .alert(controller?.jsDialog?.message ?? "", isPresented: Binding(get: { controller?.jsDialog != nil }, set: { _ in })) {
            JSDialogButtons(dialog: controller?.jsDialog) { ok, text in
                let d = controller?.jsDialog
                controller?.jsDialog = nil
                d?.answer(ok, text)
            }
        }
    }

    /// The feature gate, then the page.
    private func check() async {
        let a = await workspace.availability(of: page)
        gate = a
        guard a == .served, controller == nil else { return }
        let c = WebTileController(page: page, workspace: workspace)
        c.nav = nav // its "← workspace" goes to this window's Home
        c.webView.accessibilityElementsHidden = !panelActive
        controller = c
        c.load()
    }
}
