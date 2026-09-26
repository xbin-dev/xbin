import Foundation
import Observation
import XbinCore
import XbinRendererModel

/// What a native tile says about itself (`xbin.native.meta({title, icon,
/// badge})`, native/spec/tree.md): the navigator and the switcher show its
/// icon and badge next to the tile. Kept per workspace, written by the
/// native tile screen whenever the runtime sends a `meta`, and cached on
/// disk (Caches: losing it only hides a badge until the tile runs again).
struct TileMeta: Codable, Equatable, Sendable {
    var title: String?
    /// An icon name of the vocabulary (plans/native.md §10.4) —
    /// `XbinIcons.symbol(_:)` maps it to an SF Symbol.
    var icon: String?
    var badge: String?

    init(title: String? = nil, icon: String? = nil, badge: String? = nil) {
        self.title = title
        self.icon = icon
        self.badge = badge
    }

    /// Tile-supplied, so bounded: a badge is a count or a word.
    init(_ m: NativeMeta) {
        title = m.title.map { String($0.prefix(80)) }
        icon = m.icon.map { String($0.prefix(40)) }
        badge = m.badge.flatMap { $0.isEmpty ? nil : String($0.prefix(8)) }
    }

    /// The SF Symbol of a known icon name (unknown names show nothing here).
    var symbol: String? { icon.flatMap { XbinIcons.symbols[$0] } }

    var isEmpty: Bool { title == nil && icon == nil && badge == nil }
}

/// One workspace's tile metas. The native tile screen writes:
///
///   workspace.tileMeta.set(tile.path, store.meta)   // on every TreeStoreEvent.meta
@MainActor
@Observable
final class TileMetaStore {
    let workspace: String
    private(set) var metas: [String: TileMeta] = [:]

    init(workspace: String) {
        self.workspace = workspace
        if let d = try? Data(contentsOf: Self.url(workspace)),
           let m = try? JSONDecoder().decode([String: TileMeta].self, from: d) { metas = m }
    }

    subscript(tile: String) -> TileMeta? { metas[tile] }

    /// The runtime's merged meta for `tile` (TreeStore.meta).
    func set(_ tile: String, _ meta: NativeMeta) {
        let m = TileMeta(meta)
        guard metas[tile] != m else { return }
        metas[tile] = m.isEmpty ? nil : m
        save()
    }

    func removeAll() {
        metas = [:]
        try? FileManager.default.removeItem(at: Self.url(workspace))
    }

    private func save() {
        let u = Self.url(workspace)
        try? FileManager.default.createDirectory(at: u.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let d = try? JSONEncoder().encode(metas) { try? d.write(to: u, options: .atomic) }
    }

    private static func url(_ workspace: String) -> URL {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("tile-meta", isDirectory: true)
            .appendingPathComponent(workspace.replacingOccurrences(of: "/", with: "_") + ".json")
    }
}
