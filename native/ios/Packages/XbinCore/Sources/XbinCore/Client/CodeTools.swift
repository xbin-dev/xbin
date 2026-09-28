import Foundation

// The sessions screen's Code and Logs tools (D132, B3): a tile's files, a
// file, its git log and a commit's diff (docs/protocol.md `GET
// /code/tree`, `/code/file`, `/git/log`, `/git/diff`, `/git/activity`),
// and its backend log (`GET /logs?component=&deployment=&follow=1`), read
// only — as the web terminal window's code and logs tabs (web/bx-code.js,
// web/bx-logs.js). Queries name the tile by its path (a `+` in it escaped,
// D127j); a deployment rides its own parameter.

public enum CodeRoute {
    static func q(_ tile: String) -> String { "component=\(URLComponent.encode(tile))" }

    public static func tree(_ tile: String) -> String { "/api/xbin/code/tree?\(q(tile))" }
    public static func file(_ tile: String, _ path: String) -> String {
        "/api/xbin/code/file?\(q(tile))&file=\(URLComponent.encode(path))"
    }
    public static func log(_ tile: String, limit: Int = 50) -> String { "/api/xbin/git/log?\(q(tile))&limit=\(limit)" }
    /// `rev` "" = the uncommitted changes against HEAD.
    public static func diff(_ tile: String, rev: String) -> String {
        "/api/xbin/git/diff?\(q(tile))&rev=\(URLComponent.encode(rev))"
    }
    public static func activity(_ tile: String) -> String { "/api/xbin/git/activity?\(q(tile))" }
    /// The backend's log, its last `tail` bytes, then (follow) what is appended.
    public static func logs(_ tile: String, deployment: String? = nil, tail: Int = 65536, follow: Bool = true) -> String {
        var p = "/api/xbin/logs?\(q(tile))&tail=\(tail)"
        if let deployment, !deployment.isEmpty { p += "&deployment=\(URLComponent.encode(deployment))" }
        if follow { p += "&follow=1" }
        return p
    }
}

/// A tile's files as a tree (bx-code.js `buildTree`): folders first, by
/// name, then files.
public struct CodeTree: Equatable, Sendable {
    public struct File: Equatable, Sendable, Identifiable {
        public var path: String
        public var name: String
        public var size: Int
        public var id: String { path }
    }

    public struct Folder: Equatable, Sendable, Identifiable {
        public var path: String
        public var name: String
        public var folders: [Folder]
        public var files: [File]
        public var id: String { path }
        /// Files in it and below.
        public var count: Int { files.count + folders.reduce(0) { $0 + $1.count } }
    }

    public var root: Folder
    public var fileCount: Int { root.count }

    public init(files: [(path: String, size: Int)]) {
        final class Node {
            var folders: [String: Node] = [:]
            var files: [File] = []
        }
        let top = Node()
        for f in files where !f.path.isEmpty {
            let parts = f.path.split(separator: "/").map(String.init)
            var node = top
            for p in parts.dropLast() {
                if let n = node.folders[p] { node = n } else { let n = Node(); node.folders[p] = n; node = n }
            }
            node.files.append(File(path: f.path, name: parts.last ?? f.path, size: f.size))
        }
        func build(_ n: Node, path: String, name: String) -> Folder {
            Folder(path: path, name: name,
                   folders: n.folders.keys.sorted().map { k in build(n.folders[k]!, path: path.isEmpty ? k : "\(path)/\(k)", name: k) },
                   files: n.files.sorted { $0.name < $1.name })
        }
        root = build(top, path: "", name: "")
    }

    /// `GET /code/tree` → the tree; nil for an unreadable answer.
    public init?(json j: JSONValue) {
        guard let list = j["files"]?.arrayValue else { return nil }
        self.init(files: list.compactMap { f in
            guard let p = f["path"]?.stringValue else { return nil }
            return (p, Int(f["size"]?.intValue ?? 0))
        })
    }
}

/// `GET /code/file`: the text, or why there is none.
public enum CodeFile: Equatable, Sendable {
    case text(String, truncated: Bool)
    case binary

    public init?(json j: JSONValue) {
        if j["binary"]?.boolValue == true { self = .binary; return }
        guard let c = j["content"]?.stringValue else { return nil }
        self = .text(c, truncated: j["truncated"]?.boolValue == true)
    }
}

/// One commit of `GET /git/log`.
public struct GitCommit: Equatable, Sendable, Identifiable {
    public var hash: String
    public var short: String
    public var author: String
    /// Unix seconds.
    public var date: Int
    public var subject: String
    public var added: Int
    public var removed: Int
    public var files: Int
    public var id: String { hash }

    public init(hash: String, short: String = "", author: String = "", date: Int = 0, subject: String = "", added: Int = 0,
                removed: Int = 0, files: Int = 0) {
        self.hash = hash; self.short = short.isEmpty ? String(hash.prefix(7)) : short; self.author = author; self.date = date
        self.subject = subject; self.added = added; self.removed = removed; self.files = files
    }

    public static func list(_ j: JSONValue) -> [GitCommit] {
        (j["commits"]?.arrayValue ?? []).compactMap { c in
            guard let h = c["hash"]?.stringValue, !h.isEmpty else { return nil }
            func i(_ k: String) -> Int { Int(c[k]?.intValue ?? 0) }
            return GitCommit(hash: h, short: c["short"]?.stringValue ?? "", author: c["author"]?.stringValue ?? "",
                             date: i("date"), subject: c["subject"]?.stringValue ?? "", added: i("add"), removed: i("del"),
                             files: i("files"))
        }
    }
}

/// `GET /git/activity`: when the tile's history was written.
public struct GitActivity: Equatable, Sendable {
    /// Author dates (unix seconds) of the tile's own history.
    public var local: [Int]

    public init(local: [Int]) { self.local = local }

    public init(json j: JSONValue) {
        local = (j["local"]?.arrayValue ?? []).compactMap { $0["t"]?.intValue.map { Int($0) } }
    }

    /// Commits per day over the last `days` days, oldest first.
    public func perDay(days: Int = 30, now: Date) -> [Int] {
        let end = Int(now.timeIntervalSince1970)
        var out = Array(repeating: 0, count: max(1, days))
        for t in local {
            let back = (end - t) / 86400
            if back >= 0, back < out.count { out[out.count - 1 - back] += 1 }
        }
        return out
    }
}

/// A backend log as it streams (bx-logs.js): bytes in, whole lines out —
/// a line split across chunks (or a multibyte character) waits for its
/// end; terminal escapes are dropped; the last `limit` lines are kept.
public struct LogLines: Equatable, Sendable {
    public private(set) var lines: [String] = []
    /// Lines dropped off the top (for stable row ids).
    public private(set) var dropped = 0
    public let limit: Int
    private var partial = Data()

    public init(limit: Int = 2000) { self.limit = limit }

    /// Appends a chunk; true when whole lines were added.
    @discardableResult
    public mutating func append(_ chunk: Data) -> Bool {
        partial.append(chunk)
        var added = false
        while let nl = partial.firstIndex(of: 0x0A) {
            let lineData = partial[partial.startIndex..<nl]
            partial = Data(partial[partial.index(after: nl)...])
            var line = String(decoding: lineData, as: UTF8.self)
            if line.hasSuffix("\r") { line.removeLast() }
            lines.append(Self.plain(line))
            added = true
        }
        if lines.count > limit {
            let over = lines.count - limit
            lines.removeFirst(over)
            dropped += over
        }
        return added
    }

    /// The line not yet ended (shown dimmed, or on the next append).
    public var pending: String { Self.plain(String(decoding: partial, as: UTF8.self)) }

    /// A line without terminal escapes (CSI and OSC sequences).
    public static func plain(_ s: String) -> String {
        guard s.contains("\u{1B}") else { return s }
        var out = ""
        var it = s.unicodeScalars.makeIterator()
        while let c = it.next() {
            guard c == "\u{1B}" else { out.unicodeScalars.append(c); continue }
            guard let n = it.next() else { break }
            if n == "[" {
                while let x = it.next(), !(0x40...0x7E).contains(x.value) {}
            } else if n == "]" {
                while let x = it.next() {
                    if x == "\u{07}" { break }
                    if x == "\u{1B}" { _ = it.next(); break }
                }
            }
        }
        return out
    }
}
