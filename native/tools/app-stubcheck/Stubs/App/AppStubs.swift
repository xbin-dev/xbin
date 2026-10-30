// Stand-ins for the app types the checked files use from files this tool
// does not compile (run.sh says which): the rest of Tiles/, Terminal/,
// Push/, Shared/, the three Model/Shell files that need Apple-only
// frameworks (AppTransport, DeviceKeys, ScannerAndSSO) and
// Model/TunnelPairing.swift (CryptoKit; app-check runs it on Linux). Signatures as
// the real declarations have them; bodies are placeholders.
import Observation
import SwiftUI
import UIKit
import WebKit
import XbinAgent
import XbinCore
import XbinRendererCheck
import XbinRendererModel
import XbinTerm

// Model/AppTransport.swift, Model/DeviceKeys.swift, Shared/Keychain.swift
final class AppTransport: APITransport, @unchecked Sendable {
    static let shared = AppTransport()
    private(set) var session: URLSession! = URLSession(configuration: .ephemeral)
    func send(_ r: APIRequest, to origin: ServerOrigin) async throws -> APIResponse { APIResponse(status: 200) }
    func stream(_ request: URLRequest, allowRedirect: @escaping @Sendable (URL) -> URL?,
                onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
                onData: @escaping @Sendable (Data) -> Void,
                onDone: @escaping @Sendable ((any Error)?) -> Void) -> StreamingLoad { StreamingLoad() }
    func stream(after prepare: @escaping @Sendable () async throws -> URLRequest,
                allowRedirect: @escaping @Sendable (URL) -> URL?,
                onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
                onData: @escaping @Sendable (Data) -> Void,
                onDone: @escaping @Sendable ((any Error)?) -> Void) -> StreamingLoad { StreamingLoad() }
}
final class StreamingLoad: @unchecked Sendable {
    func cancel() {}
}
struct AgentSessionTransport: AgentTransport {
    let auth: WorkspaceAuth
    let transport: any WorkspaceTransport
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
    enum Accessibility { case whenUnlockedThisDevice, afterFirstUnlockThisDevice }
    static func read(service: String, account: String, group: String? = nil) -> Data? { nil }
    @discardableResult
    static func write(_ d: Data, service: String, account: String, group: String? = nil,
                      accessibility: Accessibility = .whenUnlockedThisDevice) -> Bool { true }
    static func delete(service: String, account: String, group: String? = nil) {}
    static func accounts(service: String) -> [String] { [] }
}

// Model/TunnelPairing.swift (CryptoKit; compiled and run by native/tools/app-check)
protocol TunnelVault: Sendable {
    func load(workspace: String) async -> TunnelSecrets?
    func save(_ secrets: TunnelSecrets, workspace: String) async
    func delete(workspace: String) async
}
struct PairedWorkspace: Sendable {
    let record: WorkspaceRecord
    let session: SessionCredential
}
typealias TunnelSignInTransport = @Sendable (WorkspaceRecord, TunnelSecrets) async throws -> any APITransport
struct TunnelPairer: Sendable {
    let keys: any DeviceKeyStore
    let vault: any TunnelVault
    let dialer: any TunnelDialer
    let signInTransport: TunnelSignInTransport
    let clientHeader: String
    let platform: String
    var timeout: TimeInterval = 7 * 60
    func pair(_ link: TunnelLink, deviceName: String, workspaceID id: String = UUID().uuidString.lowercased(),
              onCode: @escaping @Sendable (_ code: String, _ expires: Date?) async -> Void,
              onSigningIn: @escaping @Sendable () async -> Void = {},
              onConnected: @escaping @Sendable () async -> Void = {}) async throws -> PairedWorkspace {
        throw PairingFailure.timedOut
    }
}
struct TunnelUpgrader: Sendable {
    let vault: any TunnelVault
    enum Outcome: Sendable, Equatable {
        case upgraded(TunnelInfo)
        case notNow(String)
    }
    func upgrade(_ record: WorkspaceRecord,
                 send: @Sendable (APIRequest) async throws -> APIResponse) async -> Outcome { .notNow("") }
}

// Model/LoopbackForwarder.swift (Network.framework), Model/TunnelEndpoint.swift (IrohLib) — D204
protocol ForwardUpstream: Sendable {
    func splice(with local: any ByteStream) async
}
final class LoopbackForwarder: @unchecked Sendable {
    typealias Connect = @Sendable () async throws -> any ForwardUpstream
    init(connect: @escaping Connect, onFailure: @escaping @Sendable (String) -> Void = { _ in }) {}
    func start(port: Int) async throws -> Int { port }
    func stop() {}
    var isListening: Bool { false }
    func emulateReclaim() {}
}
struct TCPUpstream: ForwardUpstream {
    static func open(host: String, port: Int) async throws -> TCPUpstream { TCPUpstream() }
    func splice(with local: any ByteStream) async {}
}
final class TunnelPathWatch: @unchecked Sendable {
    init(changed: @escaping @MainActor @Sendable (Bool) -> Void) {}
    func stop() {}
}
struct IrohTunnelDialer: TunnelDialer {
    func connect(irohSecret: Data, ticket: String, relayToken: String?, alpn: String) async throws -> any TunnelConnection {
        throw URLError(.badURL)
    }
}

// Shared/NoiseTunnel.swift (CryptoKit; app-check runs it)
struct NoiseChannel: Sendable {}
enum NoiseTunnel {
    static func openTunnel(_ stream: any ByteStream, secrets: TunnelSecrets, serverEndpoint: Data,
                           deviceEndpoint: Data) async throws -> NoiseChannel { NoiseChannel() }
    static func probe(_ stream: any ByteStream, secrets: TunnelSecrets, serverEndpoint: Data, deviceEndpoint: Data,
                      host: String) async throws -> Int { 200 }
}
enum TunnelSplice {
    static func run(plain: any ByteStream, channel: NoiseChannel) async throws {}
}

// XbinApp.swift
enum AppLock { static func unlock(reason: String) async -> Bool { true } }

// Push/
@MainActor final class PushManager {
    static let shared = PushManager()
    var relay: URL? { nil }
    func workspaceAdded(_ w: WorkspaceModel) {}
    func workspaceRemoved(_ w: WorkspaceModel) async {}
    func maintainAll() async {}
    func start() async {}
    func sendTest(_ w: WorkspaceModel) async -> String { "" }
    func clearDelivered(workspace: String, link: String) {}
}
@MainActor final class LiveActivities {
    static let shared = LiveActivities()
    static var enabled: Bool { get { true } set {} }
    static var pushToStart: Bool { get { true } set {} }
    func start() {}
    func follow(_ feed: AgentSessionFeed, in w: WorkspaceModel, name: String = "") -> Task<Void, Never> { Task {} }
    func observe(_ t: AgentWindow, session: String, workspace: String, name: String = "") {}
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
    var newSession: TermNewSession?
    var body: some View { EmptyView() }
}
struct TerminalKeyboardSettingsView: View {
    var body: some View { EmptyView() }
}

// Tiles/Widgets/RuntimePool.swift
@MainActor final class NativeRuntimePool {
    static let shared = NativeRuntimePool()
    func forget(workspace id: String) {}
    func runtime(_ workspace: WorkspaceModel, _ tile: String) -> NativeTileRuntime? { nil }
    func widgets(_ workspace: WorkspaceModel) -> WidgetCache { WidgetCache() }
    func show(_ workspace: WorkspaceModel, _ tile: TileInfo, size: CardSize) {}
    func hide(_ workspace: WorkspaceModel, _ tile: String) {}
    func resize(_ workspace: WorkspaceModel, _ tile: String, size: CardSize) {}
}
@MainActor final class WidgetCache {
    func snapshot(_ tile: String, size: CardSize) -> WidgetStore.Snapshot? { nil }
}

// Tiles/NativeTile.swift (what TileCard reads of a live runtime)
@MainActor final class NativeTileRuntime {
    let widgetModel = XbinTreeModel(store: TreeStore())
    var widgetServices: XbinServices { XbinServices() }
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
    var canGoBack = false
    var pageBackground: UIColor?
    @ObservationIgnored weak var nav: WorkspaceNav?
    convenience init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, subpath: String = "", query: String? = nil,
                     fragment: String? = nil) {
        self.init(workspace: workspace, tile: tile, canOpenLinks: canOpenLinks, url: nil)
    }
    convenience init(page: XbindPage, workspace: WorkspaceModel) {
        self.init(workspace: workspace, tile: "", canOpenLinks: true, url: nil, page: page)
    }
    init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, url: URL?, island: Bool = false, chromePath: String? = nil,
         page: XbindPage? = nil) {
        self.workspace = workspace
        self.tile = tile
        self.canOpenLinks = canOpenLinks
        webView = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
    }
    func load() {}
    func reload() {}
    func goBack() {}
    func reopen() {}
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

// Shell/ScannerAndSSO.swift (VisionKit, AuthenticationServices)
struct QRScanButton: View {
    let found: (String) -> Void
    var body: some View { EmptyView() }
}
struct SSOButton: View {
    let server: ServerOrigin
    let label: String
    let flow: OnboardingFlow
    var body: some View { EmptyView() }
}
