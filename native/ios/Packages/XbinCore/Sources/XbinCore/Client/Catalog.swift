import Foundation

// The tile navigator's data (plans/native.md §4 "Navigating a workspace"):
// `GET /api/xbin/components` (RBAC-filtered), `GET /api/xbin/screens` (the
// workspace default, org screens, shared folders) and the user's own layout
// (the shell's `layout` pref — read with a frame token for `shell`, the
// bucket the web shell writes). Parsing is lenient: an unknown or missing
// field never drops a tile.

/// `GET /api/xbin/whoami`, the parts the app uses.
public struct Whoami: Sendable, Equatable {
    public var kind: String
    public var userID: String
    public var name: String
    public var role: String
    public var admin: Bool
    /// May open terminals at all (`terminal`), pick networks (`termNet`),
    /// mint terminal tokens (`termApi`).
    public var terminal: Bool
    public var termNet: Bool
    public var termApi: Bool
    public var personalTiles: Bool
    /// `native.runtime` (this xbind serves runtime documents); nil on an
    /// xbind that predates them — every tile is then a web tile.
    public var nativeRuntime: Int?
    public var readOnly: Bool
    public var json: JSONValue

    public init(json: JSONValue) {
        self.json = json
        kind = json["kind"]?.stringValue ?? ""
        userID = json["id"]?.stringValue ?? ""
        name = json["name"]?.stringValue ?? ""
        role = json["role"]?.stringValue ?? ""
        admin = json["admin"]?.boolValue ?? (role == "admin")
        terminal = json["terminal"]?.boolValue ?? false
        termNet = json["termNet"]?.boolValue ?? false
        termApi = json["termApi"]?.boolValue ?? false
        personalTiles = json["personalTiles"]?.boolValue ?? false
        nativeRuntime = json["native"]?["runtime"]?.intValue.map { Int($0) }
        readOnly = json["readOnly"]?.boolValue ?? false
    }

    public var displayName: String { name.isEmpty ? userID : name }
}

/// One row of `GET /api/xbin/components`.
public struct TileInfo: Sendable, Hashable, Identifiable {
    public var path: String
    public var scope: String
    public var runtime: String
    public var hasIndex: Bool
    /// Lifecycle (`offloaded`, `disabled`, …); "" = enabled.
    public var state: String
    /// `user:<id>` | `org:<id>` | "".
    public var owner: String
    /// Trusted chrome: acts as the human, so the app opens it in Safari.
    public var chrome: Bool
    /// Extra sandbox tokens its grants unlock (`allow-popups` = cap:open-links).
    public var sandbox: [String]
    /// `native.entry`: the tile ships a native UI (§7.1).
    public var nativeEntry: String?
    public var manifestError: String

    public var id: String { path }

    public init(path: String, scope: String = "", runtime: String = "", hasIndex: Bool = true, state: String = "",
                owner: String = "", chrome: Bool = false, sandbox: [String] = [], nativeEntry: String? = nil,
                manifestError: String = "") {
        self.path = path
        self.scope = scope
        self.runtime = runtime
        self.hasIndex = hasIndex
        self.state = state
        self.owner = owner
        self.chrome = chrome
        self.sandbox = sandbox
        self.nativeEntry = nativeEntry
        self.manifestError = manifestError
    }

    public init?(json: JSONValue) {
        guard let p = json["path"]?.stringValue, !p.isEmpty else { return nil }
        var entry: String?
        if let n = json["native"], !n.isNull {
            entry = n["entry"]?.stringValue ?? (n.boolValue == true ? "native.js" : nil)
            if entry?.isEmpty == true { entry = "native.js" }
        }
        self.init(path: p, scope: json["scope"]?.stringValue ?? "", runtime: json["runtime"]?.stringValue ?? "",
                  hasIndex: json["hasIndex"]?.boolValue ?? true, state: json["state"]?.stringValue ?? "",
                  owner: json["owner"]?.stringValue ?? "", chrome: json["chrome"]?.boolValue ?? false,
                  sandbox: (json["sandbox"]?.arrayValue ?? []).compactMap(\.stringValue), nativeEntry: entry,
                  manifestError: json["manifestError"]?.stringValue ?? "")
    }

    /// The last path segment, made readable: `apps/egress-approver` →
    /// "Egress approver".
    public var title: String { TileInfo.humanize(path) }
    /// Everything before the last segment ("apps"), "" at the top.
    public var parent: String {
        guard let i = path.lastIndex(of: "/") else { return "" }
        return String(path[..<i])
    }

    /// Opens as a native view (it has a native entry and isn't chrome).
    public var opensNatively: Bool { nativeEntry != nil && !chrome }
    /// May open links in Safari (`cap:open-links`, ND11).
    public var canOpenLinks: Bool { sandbox.contains("allow-popups") }
    /// The shell itself, the root page, and tiles nothing can open.
    public var isShellInternal: Bool { path == "root" || path == "shell" }
    public var isListed: Bool {
        !isShellInternal && hasIndex && state != "offloaded" && state != "disabled"
    }

    public func isPersonal(of user: String) -> Bool { !user.isEmpty && owner == "user:\(user)" }
    public var orgOwner: String? { owner.hasPrefix("org:") ? String(owner.dropFirst(4)) : nil }

    public static func humanize(_ path: String) -> String {
        let last = path.split(separator: "/").last.map(String.init) ?? path
        let words = last.replacingOccurrences(of: "_", with: " ").replacingOccurrences(of: "-", with: " ")
        guard let f = words.first else { return path }
        return f.uppercased() + words.dropFirst()
    }
}

/// The components a user can see.
public struct Catalog: Sendable, Equatable {
    public var tiles: [TileInfo]

    public init(tiles: [TileInfo]) { self.tiles = tiles }

    public init(json: JSONValue) {
        tiles = (json.arrayValue ?? []).compactMap(TileInfo.init(json:))
    }

    public subscript(path: String) -> TileInfo? { tiles.first { $0.path == path } }

    /// What the navigator lists (no shell internals, nothing offloaded).
    public var listed: [TileInfo] { tiles.filter(\.isListed).sorted { $0.path < $1.path } }

    /// Search over path and title: prefix of the title, then prefix of any
    /// path segment, then substring; ties by path. Every word must match.
    public func search(_ query: String) -> [TileInfo] {
        let words = query.lowercased().split(whereSeparator: \.isWhitespace).map(String.init)
        guard !words.isEmpty else { return listed }
        var scored: [(Int, TileInfo)] = []
        for t in listed {
            let title = t.title.lowercased()
            let path = t.path.lowercased()
            let segments = path.split(whereSeparator: { $0 == "/" || $0 == "-" || $0 == "_" }).map(String.init)
            var total = 0
            var ok = true
            for w in words {
                if title.hasPrefix(w) { total += 0 }
                else if segments.contains(where: { $0.hasPrefix(w) }) { total += 1 }
                else if path.contains(w) || title.contains(w) { total += 2 }
                else { ok = false; break }
            }
            if ok { scored.append((total, t)) }
        }
        return scored.sorted { $0.0 != $1.0 ? $0.0 < $1.0 : $0.1.path < $1.1.path }.map(\.1)
    }
}

/// A screen: the user's own (from the layout pref), an org's shared one, or
/// the workspace default seed.
public struct ScreenInfo: Sendable, Hashable, Identifiable {
    public enum Kind: String, Sendable { case personal, org, workspaceDefault }
    public var id: String
    public var name: String
    public var kind: Kind
    /// The owning org (org screens).
    public var org: String?
    /// Tile paths, in the layout's reading order (top-left first).
    public var tiles: [String]

    public init(id: String, name: String, kind: Kind, org: String? = nil, tiles: [String]) {
        self.id = id
        self.name = name
        self.kind = kind
        self.org = org
        self.tiles = tiles
    }

    /// Tiles of a layout screen: `[{path, x, y, …}]`, top-to-bottom then
    /// left-to-right (floats last), duplicates dropped.
    static func tilePaths(_ v: JSONValue?) -> [String] {
        struct Placed { var path: String; var y: Double; var x: Double; var float: Bool; var i: Int }
        var out: [Placed] = []
        for (i, t) in (v?.arrayValue ?? []).enumerated() {
            let p = t["path"]?.stringValue ?? t.stringValue ?? ""
            guard !p.isEmpty else { continue }
            let fl = t["float"].map { !$0.isNull } ?? false
            out.append(Placed(path: p, y: t["y"]?.doubleValue ?? 0, x: t["x"]?.doubleValue ?? 0, float: fl, i: i))
        }
        out.sort { a, b in
            if a.float != b.float { return !a.float }
            if a.y != b.y { return a.y < b.y }
            if a.x != b.x { return a.x < b.x }
            return a.i < b.i
        }
        var seen = Set<String>()
        return out.map(\.path).filter { seen.insert($0).inserted }
    }
}

/// A sidebar folder (`{id, name, items, parent?, open?}`), personal or shared.
public struct FolderInfo: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var items: [String]
    public var parent: String?

    public init(id: String, name: String, items: [String], parent: String? = nil) {
        self.id = id
        self.name = name
        self.items = items
        self.parent = parent
    }

    static func list(_ v: JSONValue?) -> [FolderInfo] {
        (v?.arrayValue ?? []).compactMap { f in
            guard let id = f["id"]?.stringValue else { return nil }
            let parent = f["parent"]?.stringValue
            return FolderInfo(id: id, name: f["name"]?.stringValue ?? id,
                              items: (f["items"]?.arrayValue ?? []).compactMap(\.stringValue),
                              parent: (parent?.isEmpty ?? true) ? nil : parent)
        }
    }
}

/// The user's own layout — the shell's `layout` pref:
/// `{screens:[{id,name,tiles}], active, side:{folders}}`.
public struct PersonalLayout: Sendable, Equatable {
    public var screens: [ScreenInfo]
    public var active: String?
    public var folders: [FolderInfo]

    public init(screens: [ScreenInfo] = [], active: String? = nil, folders: [FolderInfo] = []) {
        self.screens = screens
        self.active = active
        self.folders = folders
    }

    public init(json: JSONValue?) {
        screens = (json?["screens"]?.arrayValue ?? []).compactMap { s in
            guard let id = s["id"]?.stringValue else { return nil }
            return ScreenInfo(id: id, name: s["name"]?.stringValue ?? "Screen", kind: .personal,
                              tiles: ScreenInfo.tilePaths(s["tiles"]))
        }
        active = json?["active"]?.stringValue
        folders = FolderInfo.list(json?["side"]?["folders"])
    }
}

/// `GET /api/xbin/screens`.
public struct SharedScreens: Sendable, Equatable {
    public var workspaceDefault: [String]?
    public var org: [ScreenInfo]
    /// Scope (`ws`, `org:<id>`) → its curated folders.
    public var folders: [String: [FolderInfo]]

    public init(workspaceDefault: [String]? = nil, org: [ScreenInfo] = [], folders: [String: [FolderInfo]] = [:]) {
        self.workspaceDefault = workspaceDefault
        self.org = org
        self.folders = folders
    }

    public init(json: JSONValue?) {
        if let d = json?["default"], !d.isNull { workspaceDefault = ScreenInfo.tilePaths(d["tiles"]) }
        org = (json?["org"]?.arrayValue ?? []).compactMap { s in
            guard let id = s["id"]?.stringValue else { return nil }
            return ScreenInfo(id: id, name: s["name"]?.stringValue ?? "Screen", kind: .org, org: s["org"]?.stringValue,
                              tiles: ScreenInfo.tilePaths(s["tiles"]))
        }
        var f: [String: [FolderInfo]] = [:]
        for (scope, v) in json?["folders"]?.objectValue ?? [:] { f[scope] = FolderInfo.list(v["folders"]) }
        folders = f
    }
}

/// What the navigator shows, built from the three sources above. Tiles the
/// user can't see (absent from the catalog) are dropped everywhere.
public struct NavigatorModel: Sendable, Equatable {
    public struct Section: Sendable, Equatable, Identifiable {
        public var id: String
        public var title: String
        public var tiles: [TileInfo]
        public var folders: [FolderNode]
    }

    public struct FolderNode: Sendable, Equatable, Identifiable {
        public var id: String
        public var name: String
        public var tiles: [TileInfo]
        public var children: [FolderNode]
    }

    public var screens: [ScreenInfo]
    public var sections: [Section]

    public init(catalog: Catalog, layout: PersonalLayout, shared: SharedScreens, user: String) {
        let visible = Dictionary(catalog.listed.map { ($0.path, $0) }, uniquingKeysWith: { a, _ in a })
        func tiles(_ paths: [String]) -> [TileInfo] { paths.compactMap { visible[$0] } }

        var screens = layout.screens
        if screens.isEmpty, let d = shared.workspaceDefault {
            screens = [ScreenInfo(id: "default", name: "Home", kind: .workspaceDefault, tiles: d)]
        }
        screens += shared.org
        self.screens = screens.map { s in
            var s = s
            s.tiles = s.tiles.filter { visible[$0] != nil }
            return s
        }

        func tree(_ folders: [FolderInfo], parent: String?) -> [FolderNode] {
            folders.filter { $0.parent == parent }.map { f in
                FolderNode(id: f.id, name: f.name, tiles: tiles(f.items), children: tree(folders, parent: f.id))
            }
        }

        var sections: [Section] = []
        let personal = catalog.listed.filter { $0.isPersonal(of: user) }
        let mine = tree(layout.folders, parent: nil)
        if !personal.isEmpty || !mine.isEmpty {
            sections.append(Section(id: "mine", title: "Mine", tiles: personal, folders: mine))
        }
        for scope in shared.folders.keys.sorted(by: { a, b in a == "ws" ? true : b == "ws" ? false : a < b }) {
            let nodes = tree(shared.folders[scope] ?? [], parent: nil)
            guard !nodes.isEmpty else { continue }
            let title = scope == "ws" ? "Workspace" : String(scope.dropFirst(scope.hasPrefix("org:") ? 4 : 0))
            sections.append(Section(id: "folders:\(scope)", title: title, tiles: [], folders: nodes))
        }
        sections.append(Section(id: "all", title: "All tiles", tiles: catalog.listed, folders: []))
        self.sections = sections
    }
}
