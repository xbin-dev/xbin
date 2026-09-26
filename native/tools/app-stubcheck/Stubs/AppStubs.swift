// Stand-ins for the app types the covered files use (the real ones need WebKit, the Keychain, …).
import Foundation
import FoundationNetworking
import Observation
import SwiftUI
import UIKit
import WebKit
import XbinAgent
import XbinCore

enum Surface: Hashable {
    case tile(String, sub: String = "", fragment: String? = nil)
    case terminal(cwd: String, session: String?)
    case agent(cwd: String?, session: String?)
}

@MainActor
@Observable
final class WorkspaceModel {
    let id = "w"
    var origin: ServerOrigin { try! ServerOrigin(string: "https://x") }
    let frameTokens = FrameTokenCache { _ in "t" }
    var catalog = Catalog(tiles: [])
    var windows: [String] = []
    var surface: Surface?
    var agents: AgentClient { fatalError() }
    func describe(_ e: any Error) -> String { "\(e)" }
    func open(_ s: Surface) {}
}

enum AppInfo {
    static var clientHeader: String { "app/0" }
}

final class AppTransport: @unchecked Sendable {
    static let shared = AppTransport()
    private(set) var session: URLSession! = URLSession(configuration: .ephemeral)
}

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
    let webView: WKWebView
    var progress: Double = 0
    var isLoading = true
    var loadError: String?
    var jsDialog: JSDialog?
    var tileDialog: TileDialog?
    init(workspace: WorkspaceModel, tile: String, canOpenLinks: Bool, url: URL?, island: Bool = false) {
        webView = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
    }
    func load() {}
    func reload() {}
    func close() {}
    func resolveDialog(_ id: String, button: XbinCore.JSONValue?, values: [String: XbinCore.JSONValue]) {}
}

struct WebViewHost: View {
    let webView: WKWebView
    var body: some View { EmptyView() }
}

struct TileDialogSheet: View {
    let from: String
    let dialog: TileDialog
    let resolve: (XbinCore.JSONValue?, [String: XbinCore.JSONValue]) -> Void
    var body: some View { EmptyView() }
}

struct TerminalScreen: View {
    let workspace: WorkspaceModel
    let cwd: String
    var sessionID: String? = nil
    var initialInput: String? = nil
    var body: some View { EmptyView() }
}
