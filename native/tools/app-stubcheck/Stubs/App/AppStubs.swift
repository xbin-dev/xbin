// Stubs of the app types the Model and Shell use from other folders.
import SwiftUI
import UIKit
import WebKit
import XbinAgent
import XbinCore

@MainActor final class TileSchemeHandler {
    init(workspace: WorkspaceModel) {}
    func register(_ webView: WKWebView, tile: String) {}
    func unregister(_ webView: WKWebView) {}
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
@MainActor final class PushManager {
    static let shared = PushManager()
    var relay: URL? { nil }
    func workspaceAdded(_ w: WorkspaceModel) {}
    func workspaceRemoved(_ w: WorkspaceModel) async {}
    func maintainAll() async {}
    func start() async {}
    func sendTest(_ w: WorkspaceModel) async -> String { "" }
}
enum AppLock { static func unlock(reason: String) async -> Bool { true } }
extension Color { static let xbinAmber = Color(uiColor: .systemOrange) }

struct TileScreen: View {
    let workspace: WorkspaceModel
    let path: String
    var subpath: String = ""
    var fragment: String?
    var body: some View { EmptyView() }
}
struct TerminalScreen: View {
    let workspace: WorkspaceModel
    let cwd: String
    let sessionID: String?
    var body: some View { EmptyView() }
}
struct AgentScreen: View {
    let workspace: WorkspaceModel
    let cwd: String?
    let sessionID: String?
    var body: some View { EmptyView() }
}
struct WindowScreen: View {
    let workspace: WorkspaceModel
    let window: PushedWindow
    var body: some View { EmptyView() }
}
struct AddWorkspaceView: View {
    let request: AddRequest
    var body: some View { EmptyView() }
}
