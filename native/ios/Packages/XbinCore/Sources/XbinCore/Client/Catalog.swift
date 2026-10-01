import Foundation

// The tile navigator's data (plans/native.md §4 "Navigating a workspace"):
// `GET /api/xbin/components` (RBAC-filtered), `GET /api/xbin/screens` (the
// workspace default, org screens, shared folders) and the user's own layout
// (the shell's `layout` pref, in the `root` bucket the web shell writes —
// LayoutPref). Parsing is lenient: an unknown or missing field never drops
// a tile.

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
    /// May own tiles personally (`personalTiles`; an xbind that predates
    /// it: unless the workspace's `tileCreation` is `org-only`, D52/D88).
    public var personalTiles: Bool
    /// The workspace's tile-creation policy (`any` | `org-only`).
    public var tileCreation: String
    /// The caller's orgs (`orgs`, users only): names for Home's sections,
    /// and where they may create tiles.
    public var orgs: [Org]
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
        tileCreation = json["tileCreation"]?.stringValue ?? ""
        personalTiles = json["personalTiles"]?.boolValue ?? (tileCreation != "org-only")
        orgs = (json["orgs"]?.arrayValue ?? []).compactMap(Org.init(json:))
        nativeRuntime = json["native"]?["runtime"]?.intValue.map { Int($0) }
        readOnly = json["readOnly"]?.boolValue ?? false
    }

    public var displayName: String { name.isEmpty ? userID : name }

    /// One of `whoami.orgs`: `{id, name, level, create, admin, suspended?}`.
    public struct Org: Sendable, Equatable, Identifiable {
        public var id: String
        public var name: String
        /// May create tiles owned by the org.
        public var create: Bool
        public var admin: Bool

        public init(id: String, name: String = "", create: Bool = false, admin: Bool = false) {
            self.id = id
            self.name = name
            self.create = create
            self.admin = admin
        }

        public init?(json: JSONValue) {
            guard let id = json["id"]?.stringValue, !id.isEmpty else { return nil }
            self.init(id: id, name: json["name"]?.stringValue ?? "", create: json["create"]?.boolValue ?? false,
                      admin: json["admin"]?.boolValue ?? false)
        }

        public var displayName: String { name.isEmpty ? id : name }
    }
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
    /// Trusted chrome: acts as the human, so the app opens it signed in as
    /// the user (a web view of its own session, WebTicket), never under a
    /// frame token.
    public var chrome: Bool
    /// Extra sandbox tokens its grants unlock (`allow-popups` = cap:open-links).
    public var sandbox: [String]
    /// `native.entry`: the tile ships a native UI (§7.1).
    public var nativeEntry: String?
    public var manifestError: String
    /// A blueprint, not a live tile (`template`): instantiated through the
    /// Tile Manager, never opened.
    public var template: Bool
    /// `partition`: the tile keeps people's data apart, or its code asks to
    /// (partitioned tiles, D181); nil on every other row, and on every row
    /// of an xbind older than partitions.
    public var partition: TilePartition?

    public var id: String { path }

    public init(path: String, scope: String = "", runtime: String = "", hasIndex: Bool = true, state: String = "",
                owner: String = "", chrome: Bool = false, sandbox: [String] = [], nativeEntry: String? = nil,
                manifestError: String = "", template: Bool = false, partition: TilePartition? = nil) {
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
        self.template = template
        self.partition = partition
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
                  manifestError: json["manifestError"]?.stringValue ?? "", template: json["template"]?.boolValue ?? false,
                  partition: json["partition"].flatMap(TilePartition.init(json:)))
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
    /// Carries the partitioned marker: its recorded mode has user
    /// partitions (a pending switch doesn't change it).
    public var isPartitioned: Bool { partition?.isPartitioned ?? false }
    /// Paused: a partition mode switch waits for a tile manager, and
    /// nothing of the tile's primary runs — its documents are xbind's
    /// switch page.
    public var isPaused: Bool { partition?.isPaused ?? false }
    /// May open links in the browser (`cap:open-links`, ND11).
    public var canOpenLinks: Bool { sandbox.contains("allow-popups") }
    /// The shell itself, the root page, and tiles nothing can open.
    public var isShellInternal: Bool { path == "root" || path == "shell" }
    /// Archived (`offloaded`, `offloaded-full`): restored from the admin
    /// console (the web shell's `offloaded`, menus.js).
    public var isOffloaded: Bool { state == "offloaded" || state == "offloaded-full" }
    /// Hidden by an admin (`state: hidden`, D42): the web sidebar shows it
    /// only behind its show-hidden toggle; the app never lists it.
    public var isHidden: Bool { state == "hidden" }
    /// What the app lists and opens: not the shell, not a blueprint, not
    /// hidden, archived or disabled, and with a page to open (the web
    /// sidebar's rules, bx-side.js `_ownerSections`).
    public var isListed: Bool {
        !isShellInternal && hasIndex && !template && !isOffloaded && !isHidden && state != "disabled"
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

/// A `/components` row's `partition` (docs/protocol.md `GET /components`;
/// docs/partitions.md): the settled state, the recorded mode (`user`,
/// `global`) and, when the tile's code asks for another, the request — and,
/// while that request waits for a tile manager, the tile's `partitionNote`
/// (its code's own words: text, attributed to the tile). Read the way the
/// web shell reads it (shell/partition-mode.js `partitionView`): leniently,
/// a state this app doesn't know kept as it came.
public struct TilePartition: Sendable, Hashable {
    /// `partitioned` | `unpartitioned` | `pending` | `invalid` (or a newer
    /// xbind's word).
    public var state: String
    /// The recorded mode.
    public var user: Bool
    public var global: Bool
    /// What the code asks for when it differs from the recorded mode.
    public var request: Request?
    /// The tile's `partitionNote` while the switch is pending ("" when none).
    public var note: String

    public struct Request: Sendable, Hashable {
        public var user: Bool
        public var global: Bool
        /// A tile manager kept the current mode; the code still asks.
        public var declined: Bool

        public init(user: Bool, global: Bool, declined: Bool = false) {
            self.user = user
            self.global = global
            self.declined = declined
        }
    }

    public init(state: String, user: Bool, global: Bool = false, request: Request? = nil, note: String = "") {
        self.state = state
        self.user = user
        self.global = global
        self.request = request
        self.note = note
    }

    /// nil unless `json` is an object.
    public init?(json: JSONValue) {
        guard json.objectValue != nil else { return nil }
        let q = json["request"].flatMap { $0.objectValue != nil ? $0 : nil }
        self.init(state: json["state"]?.stringValue ?? "", user: json["user"]?.boolValue ?? false,
                  global: json["global"]?.boolValue ?? false,
                  request: q.map { Request(user: $0["user"]?.boolValue ?? false, global: $0["global"]?.boolValue ?? false,
                                           declined: $0["declined"]?.boolValue ?? false) },
                  note: (json["note"]?.stringValue ?? "").trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// The marker's rule: the recorded mode has user partitions.
    public var isPartitioned: Bool { user }
    /// A switch waits for a tile manager (`pending`, with a request).
    public var isPaused: Bool { state == "pending" && request != nil }

    /// The marker's words (the web shell's `markTitle`, PD-53), "" when the
    /// tile has no user partitions.
    public var markTitle: String {
        guard user else { return "" }
        return global ? "\(Self.markText); \(Self.markGlobalText)" : Self.markText
    }

    public static let markText = "Partitioned: each person here has their own data"
    public static let markGlobalText = "one shared global instance also runs, for what isn't a person's"
    /// A paused tile's badge, spoken (its page says the rest).
    public static let pausedText = "Paused: a partition mode switch waits for a tile manager"

    /// The marker's colour (`--bx-part`, shell-css.js `partCss`): a calm
    /// teal, deeper on light backgrounds so the ring keeps its contrast.
    public static let markColorDark: UInt32 = 0x3FB5A3
    public static let markColorLight: UInt32 = 0x1F8778
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

    /// A tile with the partitioned marker is among them (the web shell's
    /// `anyPartitioned`: a pending switch into partitions isn't one yet).
    public var hasPartitionedTile: Bool { tiles.contains(where: \.isPartitioned) }

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

/// A sidebar folder (`{id, name, items, parent?, open?, icon?}`), personal
/// or shared. Items are tile paths, and in a personal folder `#screen:<id>`
/// and `#orgscreen:<id>` too.
public struct FolderInfo: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var items: [String]
    public var parent: String?
    /// The folder's icon as the web sidebar shows it (an emoji; nil = 📁).
    public var icon: String?
    /// A personal folder the user left open on the web (`open`); shared
    /// folders keep that per user in `side.sharedOpen` instead.
    public var open: Bool

    public init(id: String, name: String, items: [String], parent: String? = nil, icon: String? = nil, open: Bool = false) {
        self.id = id
        self.name = name
        self.items = items
        self.parent = parent
        self.icon = icon
        self.open = open
    }

    static func list(_ v: JSONValue?) -> [FolderInfo] {
        (v?.arrayValue ?? []).compactMap { f in
            guard let id = f["id"]?.stringValue else { return nil }
            let parent = f["parent"]?.stringValue
            let icon = f["icon"]?.stringValue?.trimmingCharacters(in: .whitespaces)
            return FolderInfo(id: id, name: f["name"]?.stringValue ?? id,
                              items: (f["items"]?.arrayValue ?? []).compactMap(\.stringValue),
                              parent: (parent?.isEmpty ?? true) ? nil : parent,
                              icon: (icon?.isEmpty ?? true) ? nil : icon, open: f["open"]?.boolValue ?? false)
        }
    }
}

/// The user's own layout — the shell's `layout` pref:
/// `{screens:[{id,name,tiles}], active, side:{folders, sharedOpen}}`.
public struct PersonalLayout: Sendable, Equatable {
    public var screens: [ScreenInfo]
    public var active: String?
    public var folders: [FolderInfo]
    /// Shared folders the user folded or opened on the web
    /// (`side.sharedOpen`: folder id → open; absent = open).
    public var sharedOpen: [String: Bool]

    public init(screens: [ScreenInfo] = [], active: String? = nil, folders: [FolderInfo] = [], sharedOpen: [String: Bool] = [:]) {
        self.screens = screens
        self.active = active
        self.folders = folders
        self.sharedOpen = sharedOpen
    }

    public init(json: JSONValue?) {
        screens = (json?["screens"]?.arrayValue ?? []).compactMap { s in
            guard let id = s["id"]?.stringValue else { return nil }
            return ScreenInfo(id: id, name: s["name"]?.stringValue ?? "Screen", kind: .personal,
                              tiles: ScreenInfo.tilePaths(s["tiles"]))
        }
        active = json?["active"]?.stringValue
        folders = FolderInfo.list(json?["side"]?["folders"])
        var open: [String: Bool] = [:]
        for (id, v) in json?["side"]?["sharedOpen"]?.objectValue ?? [:] { if let b = v.boolValue { open[id] = b } }
        sharedOpen = open
    }
}

/// `GET /api/xbin/screens`.
public struct SharedScreens: Sendable, Equatable {
    public var workspaceDefault: [String]?
    /// The default screen's tiles as stored (`{path,x,y,w,h}`): what a
    /// first personal screen is seeded with (LayoutPref.addingScreen).
    public var workspaceDefaultTiles: JSONValue?
    public var org: [ScreenInfo]
    /// Scope (`ws`, `org:<id>`) → its curated folders.
    public var folders: [String: [FolderInfo]]

    public init(workspaceDefault: [String]? = nil, org: [ScreenInfo] = [], folders: [String: [FolderInfo]] = [:]) {
        self.workspaceDefault = workspaceDefault
        self.org = org
        self.folders = folders
    }

    public init(json: JSONValue?) {
        if let d = json?["default"], !d.isNull {
            workspaceDefault = ScreenInfo.tilePaths(d["tiles"])
            workspaceDefaultTiles = d["tiles"]
        }
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
