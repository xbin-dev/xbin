import Foundation
import XbinCore

/// Sessions in the Keychain, one item per workspace (never synced, only
/// while unlocked). The bearer token is the user's session: app code only.
struct KeychainSessionStore: SessionStore {
    static let service = "dev.xbin.session"

    func loadSession(workspace: String) async -> SessionCredential? {
        guard let d = Keychain.read(service: Self.service, account: workspace) else { return nil }
        return try? JSONDecoder().decode(SessionCredential.self, from: d)
    }

    func saveSession(_ session: SessionCredential?, workspace: String) async {
        guard let session, let d = try? JSONEncoder().encode(session) else {
            Keychain.delete(service: Self.service, account: workspace)
            return
        }
        Keychain.write(d, service: Self.service, account: workspace)
    }
}

/// The persisted workspace list (no secrets): Application Support/workspaces.json.
enum WorkspaceListFile {
    static var url: URL {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return dir.appendingPathComponent("workspaces.json")
    }

    static func load() -> WorkspaceList {
        guard let d = try? Data(contentsOf: url) else { return WorkspaceList() }
        return (try? WorkspaceList.decode(d)) ?? WorkspaceList()
    }

    static func save(_ list: WorkspaceList) {
        guard let d = try? list.encoded() else { return }
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? d.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
}

/// Saved native-runtime state blobs (`xbin.native.saveState`), per
/// workspace and tile, ≤ 64 KiB each (Caches: losing them only loses the
/// scroll/navigation position).
enum NativeStateFile {
    static func url(workspace: String, tile: String) -> URL {
        let dir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("native-state/\(workspace)", isDirectory: true)
        let name = Data(tile.utf8).base64EncodedString().replacingOccurrences(of: "/", with: "_")
        return dir.appendingPathComponent(name + ".json")
    }

    static func load(workspace: String, tile: String) -> JSONValue? {
        guard let d = try? Data(contentsOf: url(workspace: workspace, tile: tile)) else { return nil }
        return try? JSONValue(parsing: d)
    }

    static func save(_ v: JSONValue, workspace: String, tile: String) {
        guard let v = NativeStateBlob.accept(v) else { return }
        let u = url(workspace: workspace, tile: tile)
        try? FileManager.default.createDirectory(at: u.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? v.jsonData.write(to: u, options: .atomic)
    }

    static func removeAll(workspace: String) {
        let dir = url(workspace: workspace, tile: "x").deletingLastPathComponent()
        try? FileManager.default.removeItem(at: dir)
    }
}
