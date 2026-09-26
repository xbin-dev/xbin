import Foundation
import UIKit
import XbinAgent
import XbinCore

// The app types App/Push touches, declared as the app declares them
// (App/Model, Shared/) with bodies elided. Keep them in step with the real
// declarations: what this checks is our use of ActivityKit, the packages
// and concurrency, not these.

@MainActor final class AppModel {
    static let shared = AppModel()
    private(set) var workspaces: [WorkspaceModel] = []
    func workspace(_ id: String) -> WorkspaceModel? { workspaces.first { $0.id == id } }
}

@MainActor final class WorkspaceModel {
    let id: String
    private(set) var record: WorkspaceRecord
    let auth: WorkspaceAuth
    init() { fatalError() }
    var title: String { record.displayTitle }
    var agents: AgentClient { fatalError() }
    func describe(_ error: any Error) -> String { "" }
}

final class AppTransport: NSObject, APITransport, @unchecked Sendable {
    static let shared = AppTransport()
    func send(_ r: APIRequest, to origin: ServerOrigin) async throws -> APIResponse { fatalError() }
}

enum AppInfo {
    static var bundleID: String { "dev.xbin.app" }
    static var apnsProduction: Bool { false }
    static var sharedKeychainGroup: String? { nil }
}

enum AppSettings {
    static var effectivePushRelay: String { "" }
}

enum Keychain {
    static func read(service: String, account: String, group: String? = nil) -> Data? { nil }
    @discardableResult
    static func write(_ data: Data, service: String, account: String, group: String? = nil) -> Bool { true }
    static func delete(service: String, account: String, group: String? = nil) {}
}

struct PushKeyring: Codable, Equatable {
    struct Entry: Codable, Equatable {
        var workspace: String
        var pushWorkspace: String?
        var privateKey: Data
        var title: String?
    }
    var entries: [Entry] = []
    static func load(group: String?) -> PushKeyring { PushKeyring() }
    func save(group: String?) {}
    subscript(workspace: String) -> Entry? { entries.first { $0.workspace == workspace } }
    mutating func upsert(_ e: Entry) {}
    mutating func remove(_ workspace: String) {}
}

enum PushCrypto {
    static func newKeyPair() -> (privateKey: Data, publicKey: Data) { (Data(), Data()) }
    static func publicKey(of privateKey: Data) -> Data? { nil }
}
