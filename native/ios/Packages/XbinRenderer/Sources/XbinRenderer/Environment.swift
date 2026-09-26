#if canImport(UIKit)
import Observation
import SwiftUI
import UIKit
import XbinCore
import XbinRendererModel

// MARK: - What the app provides

/// App services the renderer calls — everything that needs the network, the
/// workspace session or a platform picker stays in the app (plans/native.md
/// §7.5, §8.4, §8.5). Each is optional; without it the renderer draws a
/// neutral placeholder (images, terminal, canvas) or hides the affordance
/// (attach).
public struct XbinServices {
    /// A button's `copy` and a code block's copy button (default: the
    /// general pasteboard).
    public var copy: @MainActor (String) -> Void
    /// A tapped markdown link, after the tile's `link` event. The app opens
    /// it only when the tile has `cap:open-links` (§8.2); nil: never opened.
    public var openLink: (@MainActor (URL) -> Void)?
    /// The bytes of a tile-relative image `src`, fetched with the tile's
    /// frame token (never through the bridge). `data:` sources are decoded by
    /// the renderer itself (≤ 256 KiB).
    public var imageData: (@MainActor (String) async throws -> Data)?
    /// The view for a `terminal` element: the app's terminal connected as the
    /// tile to `src` (§8.5).
    public var terminal: (@MainActor (XbinTerminalRequest) -> AnyView)?
    /// The view for a `canvas` element: a WebView island in the tile's
    /// sandbox (`src`) or a no-script static page (`html`).
    public var canvas: (@MainActor (XbinCanvasRequest) -> AnyView)?
    /// The composer's attach button: the app shows its pickers, uploads the
    /// bytes to the tile-relative `upload.path` with the frame token, and
    /// returns what the server answered per file (§8.4). Nil: no attach
    /// button.
    public var attach: (@MainActor (XbinAttachRequest) async -> [XbinUpload])?

    @MainActor
    public init(
        copy: @escaping @MainActor (String) -> Void = { UIPasteboard.general.string = $0 },
        openLink: (@MainActor (URL) -> Void)? = nil,
        imageData: (@MainActor (String) async throws -> Data)? = nil,
        terminal: (@MainActor (XbinTerminalRequest) -> AnyView)? = nil,
        canvas: (@MainActor (XbinCanvasRequest) -> AnyView)? = nil,
        attach: (@MainActor (XbinAttachRequest) async -> [XbinUpload])? = nil
    ) {
        self.copy = copy
        self.openLink = openLink
        self.imageData = imageData
        self.terminal = terminal
        self.canvas = canvas
        self.attach = attach
    }
}

/// A `terminal` element to draw.
public struct XbinTerminalRequest: Sendable, Hashable {
    /// The node key (stable while the element exists: keep one session per key).
    public let key: String
    /// Tile-relative WebSocket path of the tile's pty endpoint.
    public let src: String
    public let title: String?
}

/// A `canvas` element to draw.
public struct XbinCanvasRequest: Sendable, Hashable {
    public let key: String
    /// A tile page (tile-relative), loaded in the tile's sandbox.
    public let src: String?
    /// Static HTML, shown with a no-script CSP.
    public let html: String?
    /// Points (the `height` token; 160 by default).
    public let height: Double
}

/// The composer asked to attach files.
public struct XbinAttachRequest: Sendable, Hashable {
    public let key: String
    /// The HTTP method and tile-relative path to upload to (`upload`).
    public let method: String
    public let path: String
    /// The `accept` filter (`image/*,.pdf`), if any.
    public let accept: String?
}

/// One uploaded file: its name and the server's response, handed to the
/// tile as `uploaded {name, response}`.
public struct XbinUpload: Sendable, Equatable {
    public var name: String
    public var response: JSONValue

    public init(name: String, response: JSONValue) {
        self.name = name
        self.response = response
    }
}

/// How the tree is drawn.
public struct XbinRenderOptions: Sendable, Equatable {
    /// Draw open sheets as a card over the bottom of the view instead of
    /// presenting them — for snapshots and previews, which can't capture a
    /// modal presentation.
    public var inlineSheets: Bool

    public init(inlineSheets: Bool = false) {
        self.inlineSheets = inlineSheets
    }
}

// MARK: - Renderer context (internal)

/// Everything a primitive view needs, shared through the environment.
@MainActor
final class XbinRenderContext {
    let model: XbinTreeModel
    let services: XbinServices
    let options: XbinRenderOptions

    init(model: XbinTreeModel, services: XbinServices, options: XbinRenderOptions) {
        self.model = model
        self.services = services
        self.options = options
    }

    /// The user acted on `node` (see ``XbinTreeModel/emit(_:_:_:)``).
    func emit(_ node: XbinNode, _ type: String, _ payload: JSONValue = [:]) {
        _ = model.emit(node.key, type, payload)
    }

    /// A tapped link in `node`'s text: the tile's `link` event, then the
    /// app's policy.
    func link(_ url: URL, in node: XbinNode) {
        if node.listens(to: "link") { emit(node, "link", ["href": .string(url.absoluteString)]) }
        services.openLink?(url)
    }
}

/// Where a primitive is drawn — it decides how a button, a row or a picker
/// looks (the reference renderer's `cx.place`).
enum XbinPlacement: Sendable, Equatable {
    /// A free layout: a scroll screen, a stack, a sheet body.
    case free
    /// A row of a SwiftUI `List`/`Form` (a list or form screen).
    case list
    /// A cell of a card group outside a `List` (a section on a scroll screen).
    case card
    /// The navigation bar.
    case toolbar
    /// A transcript's children.
    case chat
    /// Inside a tool card (nested transcripts don't scroll).
    case toolcard
    /// Docked at the bottom of a screen (the composer).
    case dock
    /// A message's action buttons.
    case inlineActions
    /// A composer's suggestion chips.
    case chips
}

/// Navigation context: whether a screen is already inside a navigation
/// container, pushed, or in a sheet (title display rules).
struct XbinNavFlags: Sendable, Equatable {
    var inNavigation = false
    var pushed = false
    var inSheet = false
}

private struct ContextKey: EnvironmentKey {
    static let defaultValue: XbinRenderContext? = nil
}

private struct PlacementKey: EnvironmentKey {
    static let defaultValue: XbinPlacement = .free
}

private struct NavFlagsKey: EnvironmentKey {
    static let defaultValue = XbinNavFlags()
}

private struct ConfirmKey: EnvironmentKey {
    static let defaultValue: ConfirmHost? = nil
}

extension EnvironmentValues {
    var xbin: XbinRenderContext? {
        get { self[ContextKey.self] }
        set { self[ContextKey.self] = newValue }
    }

    var xbinPlacement: XbinPlacement {
        get { self[PlacementKey.self] }
        set { self[PlacementKey.self] = newValue }
    }

    var xbinNav: XbinNavFlags {
        get { self[NavFlagsKey.self] }
        set { self[NavFlagsKey.self] = newValue }
    }

    var xbinConfirm: ConfirmHost? {
        get { self[ConfirmKey.self] }
        set { self[ConfirmKey.self] = newValue }
    }
}

// MARK: - Confirmation dialogs

/// A button's `confirm` (§8.3): the confirmation dialog shown before its
/// tap. One host per presentation level (the tree's root and each sheet),
/// so a button in a menu, a swipe action or a toolbar can ask for one.
@MainActor
@Observable
final class ConfirmHost {
    struct Request: Identifiable {
        let id = UUID()
        let title: String
        let message: String?
        let label: String
        let destructive: Bool
        let action: @MainActor () -> Void
    }

    var pending: Request?

    func ask(_ request: Request) { pending = request }
}

/// Installs a ``ConfirmHost`` and its dialog.
struct ConfirmHostModifier: ViewModifier {
    @State private var host = ConfirmHost()

    func body(content: Content) -> some View {
        let showing = Binding<Bool>(
            get: { host.pending != nil },
            set: { if !$0 { host.pending = nil } }
        )
        let title = host.pending?.title ?? ""
        content
            .environment(\.xbinConfirm, host)
            .confirmationDialog(title, isPresented: showing, titleVisibility: title.isEmpty ? .hidden : .visible,
                                presenting: host.pending) { request in
                Button(request.label, role: request.destructive ? .destructive : nil) { request.action() }
                Button("Cancel", role: .cancel) {}
            } message: { request in
                if let m = request.message { Text(verbatim: m) }
            }
    }
}
#endif
