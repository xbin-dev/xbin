import SwiftUI
import UIKit
import WebKit
import XbinCore

/// A tile, full screen: its native view, its web page, or — for trusted
/// chrome — a hand-off to Safari (§6.3).
struct TileScreen: View {
    let workspace: WorkspaceModel
    let path: String
    var subpath: String = ""
    var fragment: String?

    @State private var forcedWeb: String?

    var body: some View {
        let info = workspace.tile(path) ?? TileInfo(path: path)
        switch forcedWeb != nil ? TileSurface.web : workspace.surfaceKind(for: info) {
        case .native:
            NativeTileScreen(workspace: workspace, tile: info) { reason in forcedWeb = reason }
        case .web:
            WebTileScreen(workspace: workspace, tile: info, subpath: subpath, fragment: fragment, banner: forcedWeb)
        case .safari:
            SafariHandoff(workspace: workspace, tile: info)
        }
    }
}

/// Hosts a controller's WKWebView.
struct WebViewHost: UIViewRepresentable {
    let webView: WKWebView
    func makeUIView(context: Context) -> WKWebView { webView }
    func updateUIView(_ uiView: WKWebView, context: Context) {}
}

/// A tile page with its native chrome (§6.3): progress, errors with retry,
/// pull to refresh, dialogs, downloads, "open in Safari".
struct WebTileScreen: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    var subpath: String = ""
    var fragment: String?
    /// Why a native view fell back here (a quiet banner, §7.6).
    var banner: String?

    @State private var controller: WebTileController?
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
                        Button("Try again") { c.reload() }.buttonStyle(.borderedProminent)
                    }
                    .background(.background)
                }
            }
            if let banner {
                Text(verbatim: banner)
                    .font(.footnote)
                    .padding(.horizontal, 12).padding(.vertical, 6)
                    .background(.thinMaterial, in: Capsule())
                    .padding(.top, 8)
                    .transition(.opacity)
            }
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                Menu {
                    Button("Reload", systemImage: "arrow.clockwise") { controller?.reload() }
                    if let u = controller?.safariURL {
                        Button("Open in Safari", systemImage: "safari") { openURL(u) }
                    }
                    if tile.opensNatively {
                        Button("Show native view", systemImage: "rectangle.stack") {
                            AppSettings.setForcesWeb(workspace.id, tile.path, false)
                            workspace.open(.tile(tile.path))
                        }
                    }
                    Button("Terminal here", systemImage: "apple.terminal") {
                        workspace.open(.terminal(cwd: tile.path, session: nil))
                    }
                    Button("Agent here", systemImage: "sparkles") {
                        workspace.open(.agent(cwd: tile.path, session: nil))
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .onAppear {
            if controller == nil {
                let c = WebTileController(workspace: workspace, tile: tile.path, canOpenLinks: tile.canOpenLinks,
                                          subpath: subpath, fragment: fragment)
                controller = c
                c.load()
            }
        }
        .onDisappear { controller?.close() }
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
                    Section { Label { Text(verbatim: spec.error) } icon: { Image(systemName: "exclamationmark.triangle") }.foregroundStyle(.red) }
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

    var body: some View {
        let known = workspace.catalog.tiles.map(\.path)
        let owner = TileScheme.tile(forPath: "/c/\(window.target)", known: known) ?? window.fromTile
        let sub = window.target.hasPrefix(owner + "/") ? String(window.target.dropFirst(owner.count + 1)) : ""
        let info = workspace.tile(owner) ?? TileInfo(path: owner)
        WebTileScreen(workspace: workspace, tile: info, subpath: sub.isEmpty ? "" : sub + "/")
            .navigationTitle(Text(verbatim: window.title))
            .navigationBarTitleDisplayMode(.inline)
            .onDisappear { WindowReplies.shared.closed(window.replyID) }
    }
}

/// Chrome tiles act as the human: they open in Safari, never under a
/// frame token (§6.3).
struct SafariHandoff: View {
    let workspace: WorkspaceModel
    let tile: TileInfo
    @Environment(\.openURL) private var openURL

    var body: some View {
        ContentUnavailableView {
            Label(tile.title, systemImage: "safari")
        } description: {
            Text("This tile is part of the workspace's own chrome — it acts as you, so it opens in Safari.")
        } actions: {
            if let u = workspace.safariURL(path: "/c/\(URLComponent.encodePath(tile.path))/") {
                Button("Open in Safari") { openURL(u) }.buttonStyle(.borderedProminent)
            }
        }
    }
}

struct ShareSheet: UIViewControllerRepresentable {
    let items: [Any]
    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }
    func updateUIViewController(_ vc: UIActivityViewController, context: Context) {}
}
