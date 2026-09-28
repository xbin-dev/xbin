import Foundation

/// The last widget each native tile drew, per workspace (D125): a phone
/// screen shows it while the tile's runtime isn't live (not started yet,
/// evicted from the runtime pool, the app just launched). One tree per card
/// size — a small card's widget isn't a wide card's — and, for a tile whose
/// runtime ran without drawing a widget, when that was seen, so screens
/// don't start runtimes for tiles that have none (``shouldProbe(_:now:)``).
///
/// Kept in a JSON file (the app's Caches: losing it only shows standard
/// cards until the tiles run again). The trees are tile-supplied, so
/// bounded: ``maxTreeBytes`` each, ``maxTiles`` tiles. Not thread-safe: use
/// it from one isolation domain (the app: the main actor).
public final class WidgetStore {
    /// A widget tree as it was last drawn.
    public struct Snapshot: Sendable, Equatable {
        /// The tree major version (`mount.v`).
        public var version: Int
        public var root: Node
        public var size: CardSize
        public var at: Date

        public init(version: Int = 1, root: Node, size: CardSize, at: Date) {
            self.version = version
            self.root = root
            self.size = size
            self.at = at
        }
    }

    /// What is known about one tile's widget.
    public struct Record: Sendable, Equatable {
        /// The last tree per card size.
        public var trees: [CardSize: Snapshot] = [:]
        /// When a runtime of the tile was seen to draw no widget (cleared by
        /// the next widget tree).
        public var noWidgetSince: Date?

        public init(trees: [CardSize: Snapshot] = [:], noWidgetSince: Date? = nil) {
            self.trees = trees
            self.noWidgetSince = noWidgetSince
        }

        var lastSeen: Date {
            ([noWidgetSince].compactMap { $0} + trees.values.map(\.at)).max() ?? .distantPast
        }
    }

    /// The largest tree kept, as JSON text.
    public static let maxTreeBytes = 64 * 1024
    /// The most tiles kept; the least recently drawn go first.
    public static let maxTiles = 200
    /// How long "draws no widget" holds before a screen starts the tile's
    /// runtime again to look (opening the tile always looks).
    public static let noWidgetTTL: TimeInterval = 24 * 3600

    /// The file, or nil (memory only).
    public let file: URL?
    public private(set) var records: [String: Record] = [:]
    /// Changed since the last ``save()``.
    public private(set) var isDirty = false

    /// A store backed by `file` (read now; a missing or unreadable file is
    /// an empty store).
    public init(file: URL?) {
        self.file = file
        if let file, let data = try? Data(contentsOf: file), let json = try? JSONValue(parsing: data) {
            records = Self.decode(json)
        }
    }

    /// The tree `tile` last drew at `size`.
    public func snapshot(_ tile: String, size: CardSize) -> Snapshot? { records[tile]?.trees[size] }

    /// Keeps `root` as `tile`'s widget at `size`. Returns false when it
    /// wasn't kept (too big) or nothing changed.
    @discardableResult
    public func record(_ tile: String, root: Node, version: Int = 1, size: CardSize, at: Date) -> Bool {
        let snap = Snapshot(version: version, root: root, size: size, at: at)
        guard Self.encode(snap).jsonString.utf8.count <= Self.maxTreeBytes else { return false }
        var r = records[tile] ?? Record()
        let unchanged = r.trees[size].map { $0.root == root && $0.version == version } ?? false
        if unchanged && r.noWidgetSince == nil { return false }
        r.trees[size] = snap
        r.noWidgetSince = nil
        records[tile] = r
        trim()
        isDirty = true
        return true
    }

    /// A runtime of `tile` mounted its UI and drew no widget.
    public func noteNoWidget(_ tile: String, at: Date) {
        var r = records[tile] ?? Record()
        r.trees = [:]
        r.noWidgetSince = at
        records[tile] = r
        trim()
        isDirty = true
    }

    /// Whether a screen should start `tile`'s runtime for its card: unless
    /// a runtime drew no widget for it within ``noWidgetTTL``.
    public func shouldProbe(_ tile: String, now: Date) -> Bool {
        guard let since = records[tile]?.noWidgetSince else { return true }
        return now.timeIntervalSince(since) >= Self.noWidgetTTL || now < since
    }

    public func remove(_ tile: String) {
        guard records.removeValue(forKey: tile) != nil else { return }
        isDirty = true
    }

    /// Forgets everything, the file included.
    public func removeAll() {
        records = [:]
        isDirty = false
        if let file { try? FileManager.default.removeItem(at: file) }
    }

    /// Writes the file when something changed.
    public func save() throws {
        guard isDirty, let file else { return }
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data(Self.encode(records).jsonString.utf8).write(to: file, options: .atomic)
        isDirty = false
    }

    private func trim() {
        guard records.count > Self.maxTiles else { return }
        let old = records.sorted { $0.value.lastSeen < $1.value.lastSeen }.prefix(records.count - Self.maxTiles)
        for (k, _) in old { records[k] = nil }
    }

    // MARK: - The file

    // {"v":1,"tiles":{"<path>":{"trees":{"small":{"v":1,"root":…,"at":<ms>}},"none":<ms>}}}
    // — through JSONValue, so the trees' booleans and integers survive.

    static func encode(_ s: Snapshot) -> JSONValue {
        ["v": .int(Int64(s.version)), "root": s.root.json, "at": .int(Int64((s.at.timeIntervalSince1970 * 1000).rounded()))]
    }

    static func encode(_ records: [String: Record]) -> JSONValue {
        var tiles: [String: JSONValue] = [:]
        for (tile, r) in records {
            var o: [String: JSONValue] = [:]
            var trees: [String: JSONValue] = [:]
            for (size, s) in r.trees { trees[size.rawValue] = encode(s) }
            if !trees.isEmpty { o["trees"] = .object(trees) }
            if let n = r.noWidgetSince { o["none"] = .int(Int64((n.timeIntervalSince1970 * 1000).rounded())) }
            tiles[tile] = .object(o)
        }
        return ["v": 1, "tiles": .object(tiles)]
    }

    static func decode(_ json: JSONValue) -> [String: Record] {
        guard json["v"]?.intValue == 1, let tiles = json["tiles"]?.objectValue else { return [:] }
        var out: [String: Record] = [:]
        for (tile, t) in tiles {
            var r = Record()
            for (name, s) in t["trees"]?.objectValue ?? [:] {
                guard let size = CardSize(rawValue: name), let rootJSON = s["root"],
                      let root = try? Node(json: rootJSON) else { continue }
                let v = s["v"]?.intValue.flatMap { Int(exactly: $0) } ?? 1
                r.trees[size] = Snapshot(version: v, root: root, size: size, at: date(s["at"]))
            }
            if let n = t["none"], !n.isNull { r.noWidgetSince = date(n) }
            if !r.trees.isEmpty || r.noWidgetSince != nil { out[tile] = r }
        }
        return out
    }

    private static func date(_ v: JSONValue?) -> Date {
        guard let ms = v?.doubleValue else { return .distantPast }
        return Date(timeIntervalSince1970: ms / 1000)
    }
}

/// Which native runtimes stay live (D125): at most ``capacity``, least
/// recently used first out, across the widgets on screen and the open
/// tiles — an open tile is never evicted, and a widget scrolled off screen
/// goes before one still showing. The app's pool keeps one of these and
/// stops the runtimes it names.
public struct RuntimePoolPolicy<Key: Hashable & Sendable>: Sendable {
    public let capacity: Int
    /// The live runtimes.
    public private(set) var live: Set<Key> = []
    private var opens: [Key: Int] = [:]
    private var shows: [Key: Int] = [:]
    private var used: [Key: Int] = [:]
    private var clock = 0

    public init(capacity: Int = 6) { self.capacity = max(1, capacity) }

    /// A tile screen shows `key`: it is live (start it when it wasn't) and
    /// pinned. Returns the runtimes to stop.
    public mutating func open(_ key: Key) -> [Key] {
        opens[key, default: 0] += 1
        return admit(key)
    }

    /// The tile screen went away: `key` stays live, unpinned.
    public mutating func close(_ key: Key) -> [Key] {
        opens[key] = max(0, (opens[key] ?? 0) - 1)
        if opens[key] == 0 { opens[key] = nil }
        touch(key)
        return enforce(keeping: nil)
    }

    /// A card drawing `key`'s widget came on screen: it is live. Returns
    /// the runtimes to stop.
    public mutating func show(_ key: Key) -> [Key] {
        shows[key, default: 0] += 1
        return admit(key)
    }

    /// The card went off screen.
    public mutating func hide(_ key: Key) {
        shows[key] = max(0, (shows[key] ?? 0) - 1)
        if shows[key] == 0 { shows[key] = nil }
    }

    /// `key`'s runtime ended on its own (it failed): no longer live.
    public mutating func drop(_ key: Key) {
        live.remove(key)
        used[key] = nil
    }

    public func isOpen(_ key: Key) -> Bool { (opens[key] ?? 0) > 0 }
    public func isShown(_ key: Key) -> Bool { (shows[key] ?? 0) > 0 }
    /// On screen one way or the other: its runtime should run as visible.
    public func isVisible(_ key: Key) -> Bool { isOpen(key) || isShown(key) }

    private mutating func touch(_ key: Key) {
        guard live.contains(key) else { return }
        clock += 1
        used[key] = clock
    }

    private mutating func admit(_ key: Key) -> [Key] {
        live.insert(key)
        touch(key)
        return enforce(keeping: key)
    }

    private mutating func enforce(keeping keep: Key?) -> [Key] {
        var out: [Key] = []
        while live.count > capacity {
            // Off screen before on screen, then least recently used; never
            // an open tile, never the runtime just asked for.
            let candidates = live.filter { $0 != keep && !isOpen($0) }
            guard let victim = candidates.min(by: { a, b in
                let (sa, sb) = (isShown(a), isShown(b))
                if sa != sb { return !sa }
                return (used[a] ?? 0) < (used[b] ?? 0)
            }) else { break }
            live.remove(victim)
            used[victim] = nil
            out.append(victim)
        }
        return out
    }
}
