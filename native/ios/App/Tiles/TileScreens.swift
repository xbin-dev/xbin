import SwiftUI
import XbinRenderer
import UIKit
import WebKit
import XbinCore

/// A tile, full screen: its native view or its web page — for trusted
/// chrome, its page on the workspace's origin, signed in (§6.3).
struct TileScreen: View {
    let workspace: WorkspaceModel
    let path: String
    var subpath: String = ""
    var fragment: String?

    @State private var forcedWeb: String?

    var body: some View {
        let info = Self.info(workspace, path)
        switch forcedWeb != nil ? TileSurface.web : workspace.surfaceKind(for: info) {
        case .native:
            NativeTileScreen(workspace: workspace, tile: info) { reason in forcedWeb = reason }
        case .web:
            WebTileScreen(workspace: workspace, tile: info, subpath: subpath, fragment: fragment, banner: forcedWeb)
        }
    }

    /// The tile at `path`; a deployment's URL (`<tile>+<name>`) and the web
    /// shell open signed in, as chrome does (D132).
    static func info(_ workspace: WorkspaceModel, _ path: String) -> TileInfo {
        var info = workspace.tile(path) ?? TileInfo(path: path)
        if !info.chrome, DeploymentsRoute.opensSignedIn(path, known: { workspace.tile($0) != nil }) { info.chrome = true }
        return info
    }
}

/// Hosts a controller's WKWebView.
struct WebViewHost: UIViewRepresentable {
    let webView: WKWebView
    func makeUIView(context: Context) -> WKWebView { webView }
    func updateUIView(_ uiView: WKWebView, context: Context) {}
}

/// A tile page with its native chrome (§6.3): progress, errors with retry,
/// pull to refresh, dialogs, downloads, the page's own back.
struct WebTileScreen: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    var subpath: String = ""
    var fragment: String?
    /// Why a native view fell back here (a quiet banner, §7.6).
    var banner: String?
    /// The pushed window this page is (its replyID); nil: the window's
    /// surface.
    var window: String?

    @State private var controller: WebTileController?
    /// This window's navigation (the tile's menu and its `xbin.window`).
    @Environment(WorkspaceNav.self) private var nav
    /// The panel is in front (PanelStack): a page kept aside for forward is
    /// out of VoiceOver's reach (SwiftUI's accessibilityHidden stops at the
    /// UIKit view).
    @Environment(\.panelActive) private var panelActive
    @Environment(\.scenePhase) private var phase
    @Environment(\.openURL) private var openURL

    var body: some View {
        ZStack(alignment: .top) {
            if let c = controller {
                WebViewHost(webView: c.webView)
                    .ignoresSafeArea(.container, edges: .bottom)
                if c.isLoading, c.progress < 1 {
                    ProgressView(value: c.progress).progressViewStyle(.linear).tint(.accentColor)
                }
                if let err = c.loadError {
                    ContentUnavailableView {
                        Label("Can't load \(tile.title)", systemImage: "exclamationmark.triangle")
                    } description: {
                        Text(verbatim: err)
                    } actions: {
                        Button("Try again") { c.reload() }.xbinPrimary()
                    }
                    .background(.background)
                }
            }
            if let banner {
                Text(verbatim: banner)
                    .font(.footnote)
                    .padding(.horizontal, 12).padding(.vertical, 6)
                    .background(XbinColor.surface, in: .xbinPlate).overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
                    .padding(.top, 8)
                    .transition(.opacity)
            }
        }
        // The page starts right under the toolbar: an inline bar (the
        // navigator's large title would leave its empty title area as a
        // band over the page), with the page's own background color
        // behind the bar, as the terminal's black runs under its bar.
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
                Menu {
                    Button("Reload", systemImage: "arrow.clockwise") { controller?.reload() }
                    if tile.opensNatively {
                        Button("Show native view", systemImage: "rectangle.stack") {
                            AppSettings.setForcesWeb(workspace.id, tile.path, false)
                            workspace.open(.tile(tile.path), in: nav)
                        }
                    }
                    // The tile's sessions screen (D132): its tabs and tools.
                    Button("Sessions & tools", systemImage: "rectangle.stack") {
                        workspace.open(.sessions(tile: tile.path, show: .first), in: nav)
                    }
                    Button("New session…", systemImage: "plus.rectangle.on.rectangle") {
                        workspace.open(.sessions(tile: tile.path, show: .launcher), in: nav)
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .onAppear {
            if controller == nil {
                let c = tile.chrome
                    ? WebTileController(chromeTile: tile, workspace: workspace, subpath: subpath)
                    : WebTileController(workspace: workspace, tile: tile.path, canOpenLinks: tile.canOpenLinks,
                                        subpath: subpath, fragment: fragment)
                c.nav = nav
                controller = c
                c.webView.accessibilityElementsHidden = !panelActive
                c.load()
            } else {
                controller?.reopen() // (after a close that wasn't the end: load afresh)
            }
        }
        // Covered by a window pushed over it (its own xbin.window) it shows
        // again on the pop — keep the page; gone: close it.
        .onDisappear { if !nav.stillStacked(window: window) { controller?.close() } }
        .onChange(of: panelActive) { _, active in controller?.webView.accessibilityElementsHidden = !active }
        // Live reload (§7.7): the tile's source changed — reload the page.
        .task(id: tile.path) { await workspace.events.onReload(of: tile.path) { controller?.reload() } }
        .onChange(of: phase) { _, p in
            if p == .background { controller?.didEnterBackground() }
            if p == .active { controller?.willEnterForeground() }
        }
        .sheet(item: Binding(get: { controller?.tileDialog }, set: { if $0 == nil, let d = controller?.tileDialog { controller?.resolveDialog(d.id, button: nil, values: [:]) } })) { d in
            TileDialogSheet(from: tile.path, dialog: d) { button, values in
                controller?.resolveDialog(d.id, button: button, values: values)
            }
            .presentationDetents([.medium, .large])
        }
        .alert(controller?.jsDialog?.message ?? "", isPresented: Binding(get: { controller?.jsDialog != nil }, set: { _ in })) {
            JSDialogButtons(dialog: controller?.jsDialog) { ok, text in
                let d = controller?.jsDialog
                controller?.jsDialog = nil
                d?.answer(ok, text)
            }
        }
        .sheet(isPresented: Binding(get: { controller?.shareItems != nil }, set: { if !$0 { controller?.shareItems = nil } })) {
            ShareSheet(items: controller?.shareItems ?? [])
        }
    }
}

/// alert/confirm/prompt buttons (a web tile's page and a native tile's
/// canvas island): confirm has Cancel, prompt a text field.
struct JSDialogButtons: View {
    let dialog: JSDialog?
    let done: (Bool, String?) -> Void
    @State private var text = ""

    var body: some View {
        switch dialog?.kind {
        case .prompt(let def)?:
            TextField("", text: $text).onAppear { text = def }
            Button("Cancel", role: .cancel) { done(false, nil) }
            Button("OK") { done(true, text) }
        case .confirm?:
            Button("Cancel", role: .cancel) { done(false, nil) }
            Button("OK") { done(true, nil) }
        default:
            Button("OK") { done(true, nil) }
        }
    }
}

/// `xbin.dialog(spec)` as a native form: the same data `<bx-dialog>`
/// renders — plain text only — attributed to the tile that asked.
struct TileDialogSheet: View {
    let from: String
    let dialog: TileDialog
    let resolve: (JSONValue?, [String: JSONValue]) -> Void

    @State private var values: [String: JSONValue] = [:]

    var spec: DialogSpec { dialog.spec }

    var body: some View {
        NavigationStack {
            Form {
                if !spec.message.isEmpty { Section { Text(verbatim: spec.message) } }
                if !spec.error.isEmpty {
                    Section { Label { Text(verbatim: spec.error) } icon: { Image(systemName: XbinGlyphs.symbol("error")) }.foregroundStyle(XbinColor.danger) }
                }
                if !spec.fields.isEmpty {
                    Section {
                        ForEach(spec.fields) { f in field(f) }
                    }
                }
                Section {
                    ForEach(Array(spec.resolvedButtons.enumerated()), id: \.offset) { _, b in
                        Button(role: b.danger ? .destructive : (b.value.isNull ? .cancel : nil)) {
                            resolve(b.value, values)
                        } label: {
                            Text(verbatim: b.label).fontWeight(b.primary ? .semibold : .regular)
                        }
                    }
                } footer: {
                    Text(verbatim: "Asked by \(from)").font(.caption.monospaced())
                }
            }
            .concreteBackground()
            .navigationTitle(Text(verbatim: spec.title))
            .navigationBarTitleDisplayMode(.inline)
            .onAppear { if values.isEmpty { values = spec.initialValues } }
            .onSubmit { if let b = spec.submitButton { resolve(b.value, values) } }
        }
    }

    @ViewBuilder private func field(_ f: DialogSpec.Field) -> some View {
        let text = Binding<String>(get: { values[f.name]?.stringValue ?? "" }, set: { values[f.name] = .string($0) })
        switch f.kind {
        case .checkbox:
            Toggle(isOn: Binding(get: { values[f.name]?.boolValue ?? false }, set: { values[f.name] = .bool($0) })) {
                Text(verbatim: f.label)
            }
        case .password:
            SecureField(f.label, text: text, prompt: Text(verbatim: f.placeholder)).privacySensitive()
        case .textarea:
            VStack(alignment: .leading) {
                Text(verbatim: f.label).font(.caption).foregroundStyle(.secondary)
                TextEditor(text: text).frame(minHeight: 90)
            }
        case .select:
            Picker(selection: text) {
                ForEach(Array(f.options.enumerated()), id: \.offset) { _, o in Text(verbatim: o.label).tag(o.value) }
            } label: { Text(verbatim: f.label) }
        case .number:
            TextField(f.label, text: text, prompt: Text(verbatim: f.placeholder)).keyboardType(.decimalPad)
        case .email:
            TextField(f.label, text: text, prompt: Text(verbatim: f.placeholder)).keyboardType(.emailAddress)
                .textInputAutocapitalization(.never)
        case .url:
            TextField(f.label, text: text, prompt: Text(verbatim: f.placeholder)).keyboardType(.URL)
                .textInputAutocapitalization(.never)
        case .text:
            TextField(f.label, text: text, prompt: Text(verbatim: f.placeholder))
        }
    }
}

/// An `xbin.window` as a pushed screen (compact) — a sub-path of the tile
/// or another tile, framed with *its* tile's frame token. Closing it tells
/// the opener (`handle.closed`).
struct WindowScreen: View {
    let workspace: WorkspaceModel
    let window: PushedWindow
    @Environment(WorkspaceNav.self) private var nav

    var body: some View {
        let known = workspace.catalog.tiles.map(\.path)
        let owner = TileScheme.tile(forPath: "/c/\(window.target)", known: known) ?? window.fromTile
        let sub = window.target.hasPrefix(owner + "/") ? String(window.target.dropFirst(owner.count + 1)) : ""
        let info = workspace.tile(owner) ?? TileInfo(path: owner)
        WebTileScreen(workspace: workspace, tile: info, subpath: sub.isEmpty ? "" : sub + "/", window: window.replyID)
            .navigationTitle(Text(verbatim: window.title))
            .navigationBarTitleDisplayMode(.inline)
            // closed once popped — not while another window covers it
            .onDisappear { if !nav.stillStacked(window: window.replyID) { WindowReplies.shared.closed(window.replyID) } }
    }
}

struct ShareSheet: UIViewControllerRepresentable {
    let items: [Any]
    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }
    func updateUIViewController(_ vc: UIActivityViewController, context: Context) {}
}
