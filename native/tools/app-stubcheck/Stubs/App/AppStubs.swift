// Stand-ins for the app types the checked files use from files this tool
// does not compile (run.sh says which): the rest of Tiles/, Terminal/,
// Push/, Shared/, and the three Model/Shell files that need Apple-only
// frameworks (AppTransport, DeviceKeys, AddWorkspaceView). Signatures as
// the real declarations have them; bodies are placeholders.
import Observation
import SwiftUI
import UIKit
import WebKit
import XbinAgent
import XbinCore

// Model/AppTransport.swift, Model/DeviceKeys.swift, Shared/Keychain.swift
final class AppTransport: APITransport, @unchecked Sendable {
    static let shared = AppTransport()
    private(set) var session: URLSession! = URLSession(configuration: .ephemeral)
    func send(_ r: APIRequest, to origin: ServerOrigin) async throws -> APIResponse { APIResponse(status: 200) }
}
struct AgentSessionTransport: AgentTransport {
    let auth: WorkspaceAuth
    let transport: AppTransport
    func send(_ request: HTTPRequest) async throws -> HTTPResponse { HTTPResponse(status: 200) }
    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> { throw URLError(.badURL) }
}
struct EnclaveKeyStore: DeviceKeyStore {
    func createKey(workspace: String) async throws -> Data { Data() }
    func hasKey(workspace: String) async -> Bool { false }
    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data { Data() }
    func deleteKey(workspace: String) async {}
}
enum Keychain {
    static func read(service: String, account: String) -> Data? { nil }
    static func write(_ d: Data, service: String, account: String) {}
    static func delete(service: String, account: String) {}
}

// XbinApp.swift
enum AppLock { static func unlock(reason: String) async -> Bool { true } }
extension Color { static let xbinAmber = Color(uiColor: .systemOrange) }

// Push/
@MainActor final class PushManager {
    static let shared = PushManager()
    var relay: URL? { nil }
    func workspaceAdded(_ w: WorkspaceModel) {}
    func workspaceRemoved(_ w: WorkspaceModel) async {}
    func maintainAll() async {}
    func start() async {}
    func sendTest(_ w: WorkspaceModel) async -> String { "" }
}
@MainActor final class LiveActivities {
    static let shared = LiveActivities()
    static var enabled: Bool { get { true } set {} }
    static var pushToStart: Bool { get { true } set {} }
    func start() {}
    func follow(_ feed: AgentSessionFeed, in w: WorkspaceModel, name: String = "") -> Task<Void, Never> { Task {} }
    func observe(_ t: AgentTranscript, session: String, workspace: String, name: String = "") {}
    func endAll() {}
    func workspaceRemoved(_ id: String) {}
}

// Terminal/
struct TerminalScreen: View {
    let workspace: WorkspaceModel
    let cwd: String
    var sessionID: String?
    var initialInput: String?
    var onExit: (() -> Void)?
    var body: some View { EmptyView() }
}
struct TerminalKeyboardSettingsView: View {
    var body: some View { EmptyView() }
}

// Tiles/TileSchemeHandler.swift
@MainActor final class TileSchemeHandler {
    init(workspace: WorkspaceModel) {}
    func register(_ webView: WKWebView, tile: String) {}
    func unregister(_ webView: WKWebView) {}
}

// Tiles/WebTileController.swift
struct JSDialog: Identifiable {
    enum Kind { case alert, confirm, prompt(defaultText: String) }
    let id = UUID()
    var kind: Kind
    var message: String
    var answer: (Bool, String?) -> Void
}
struct TileDialog: Identifiable {
    let id: String
    var spec: DialogSpec
}
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
    @ObservationIgnored weak var nav: WorkspaceNav?
    convenience init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, subpath: String = "", query: String? = nil,
                     fragment: String? = nil) {
        self.init(workspace: workspace, tile: tile, canOpenLinks: canOpenLinks, url: nil)
    }
    init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, url: URL?, island: Bool = false) {
        self.workspace = workspace
        self.tile = tile
        self.canOpenLinks = canOpenLinks
        webView = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
    }
    func load() {}
    func reload() {}
    func close() {}
    func resolveDialog(_ id: String, button: XbinCore.JSONValue?, values: [String: XbinCore.JSONValue]) {}
}

// Tiles/TileScreens.swift
struct TileScreen: View {
    let workspace: WorkspaceModel
    let path: String
    var subpath: String = ""
    var fragment: String?
    var body: some View { EmptyView() }
}
struct WindowScreen: View {
    let workspace: WorkspaceModel
    let window: PushedWindow
    var body: some View { EmptyView() }
}
struct WebViewHost: View {
    let webView: WKWebView
    var body: some View { EmptyView() }
}
struct JSDialogButtons: View {
    let dialog: JSDialog?
    let done: (Bool, String?) -> Void
    var body: some View { EmptyView() }
}
struct ShareSheet: View {
    let items: [Any]
    var body: some View { EmptyView() }
}
struct TileDialogSheet: View {
    let from: String
    let dialog: TileDialog
    let resolve: (XbinCore.JSONValue?, [String: XbinCore.JSONValue]) -> Void
    var body: some View { EmptyView() }
}

// Shell/AddWorkspaceView.swift (VisionKit, AuthenticationServices)
struct AddWorkspaceView: View {
    let request: AddRequest
    var body: some View { EmptyView() }
}
