import Foundation

// "All tiles" (D128): the web sidebar's tree — workspace-template/shell/
// bx-side.js (`_ownerSections`, `_sectionTemplate`, `_folderTemplate`,
// D24/D55) — built from what the app already reads: the catalog, the
// user's `layout` pref (`side.folders`, `side.sharedOpen`) and
// `GET /api/xbin/screens` (org screens, `folders[scope]`). Read-only: the
// app files nothing.
//
// 1. The user's personal folders first (`side.folders`, top level): tile
//    paths, `#screen:<id>` (a personal screen) and `#orgscreen:<id>`.
// 2. Then one section per owner (shell-kit.js `ownerKeyOf`): `mine` (the
//    user owns it), `org:<id>` (whoami's orgs first), `workspace` — each
//    holding its shared folder set (`folders["ws" | "org:<id>"]`), then the
//    tiles no folder of it files, by label, then (orgs) the org's screens.
//    A tile filed in a personal folder leaves its section; one filed in a
//    shared folder leaves the section's root. A section with no tile and
//    no screen is left out; one section alone draws without its header.
// 3. A tile's label is its basename, or its path when two tiles of the
//    section share the basename (D55); a personal folder's tiles go by
//    basename.
// 4. Skipped everywhere: the shell, blueprints (`template`), archived
//    (`offloaded*`) and hidden (`state: hidden`) tiles — TileInfo.isListed
//    (the web's show-hidden toggle has no counterpart here). A shared
//    folder with nothing this user can see is left out; a personal one
//    stays (the user made it).

public struct NavigatorModel: Sendable, Equatable {
    public enum Item: Sendable, Equatable, Identifiable {
        case folder(Folder)
        case tile(TileInfo, label: String)
        /// A personal screen, or an org's (`screen.kind`).
        case screen(ScreenInfo)

        public var id: String {
            switch self {
            case .folder(let f): return "f:" + f.id
            case .tile(let t, _): return "t:" + t.path
            case .screen(let s): return "s:" + s.id
            }
        }
    }

    public struct Folder: Sendable, Equatable, Identifiable {
        /// Unique across the tree: `<context>/<folder id>` (`top`, `ws`,
        /// `org:<id>`).
        public var id: String
        public var name: String
        /// The web's icon (an emoji); nil = a plain folder.
        public var icon: String?
        /// Open at first, as the user left it on the web: a personal folder
        /// when its `open` is set, a shared one unless `side.sharedOpen`
        /// folds it.
        public var open: Bool
        /// Child folders first, then what the folder files, in its order.
        public var items: [Item]
    }

    public struct Section: Sendable, Equatable, Identifiable {
        /// `mine`, `org:<id>`, `workspace`.
        public var id: String
        public var title: String
        /// Its shared folders, then its unfiled tiles, then its screens.
        public var items: [Item]
        /// Tiles and screens it owns (the web header's count).
        public var count: Int
    }

    /// The personal folders.
    public var folders: [Folder]
    public var sections: [Section]

    public init(folders: [Folder] = [], sections: [Section] = []) {
        self.folders = folders
        self.sections = sections
    }

    /// One section alone draws without a header (a solo workspace).
    public var showsSectionHeaders: Bool { sections.count > 1 }
    public var isEmpty: Bool { folders.isEmpty && sections.allSatisfy(\.items.isEmpty) }

    /// Which section a tile lists under (shell-kit.js `ownerKeyOf`).
    public static func ownerKey(_ t: TileInfo, user: String) -> String {
        if !user.isEmpty, t.owner == "user:\(user)" { return "mine" }
        return t.owner.hasPrefix("org:") ? t.owner : "workspace"
    }

    /// A section's shared-folder scope (shell-kit.js `scopeOf`): `ws` for
    /// the workspace, the org's own key, none for `mine`.
    public static func scope(ofSection key: String) -> String? {
        key == "workspace" ? "ws" : key.hasPrefix("org:") ? key : nil
    }

    /// The labels of a section's tiles: the basename, or the path when two
    /// share it (bx-side.js `_labelsFor`).
    public static func labels(_ tiles: [TileInfo]) -> [String: String] {
        var n: [String: Int] = [:]
        for t in tiles { n[basename(t.path), default: 0] += 1 }
        var out: [String: String] = [:]
        for t in tiles { out[t.path] = (n[basename(t.path)] ?? 0) > 1 ? t.path : basename(t.path) }
        return out
    }

    public static func basename(_ path: String) -> String {
        guard let i = path.lastIndex(of: "/") else { return path }
        return String(path[path.index(after: i)...])
    }

    public init(catalog: Catalog, layout: PersonalLayout, shared: SharedScreens, whoami: Whoami?) {
        let me = whoami?.userID ?? ""
        let tiles = catalog.tiles.filter(\.isListed) // the server's order, as the web iterates it
        let visible = Dictionary(tiles.map { ($0.path, $0) }, uniquingKeysWith: { a, _ in a })
        let personalScreens = Dictionary(layout.screens.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        let orgScreens = Dictionary(shared.org.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })

        /// A folder of `list` (a context's whole set) and what it holds;
        /// `section`: a shared folder's owner section — its tiles (others
        /// are not shown there) and their labels.
        func folder(_ f: FolderInfo, in list: [FolderInfo], context: String,
                    section: (paths: Set<String>, labels: [String: String])?, depth: Int) -> Folder? {
            guard depth < 32 else { return nil } // repeated ids in hand-edited data
            let isShared = section != nil
            var items: [Item] = []
            var seen = Set<String>()
            for it in f.items {
                var item: Item?
                if it.hasPrefix("#screen:") {
                    if !isShared, let s = personalScreens[String(it.dropFirst("#screen:".count))] { item = .screen(s) }
                } else if it.hasPrefix("#orgscreen:") {
                    if !isShared, let s = orgScreens[String(it.dropFirst("#orgscreen:".count))] { item = .screen(s) }
                } else if let t = visible[it], section.map({ $0.paths.contains(it) }) ?? true {
                    item = .tile(t, label: section?.labels[it] ?? Self.basename(it))
                }
                if let item, seen.insert(item.id).inserted { items.append(item) }
            }
            let children = list.filter { $0.parent == f.id }.compactMap {
                folder($0, in: list, context: context, section: section, depth: depth + 1)
            }
            if isShared, items.isEmpty, children.isEmpty { return nil }
            let open = isShared ? (layout.sharedOpen[f.id] ?? true) : f.open
            return Folder(id: "\(context)/\(f.id)", name: f.name, icon: f.icon, open: open,
                          items: children.map(Item.folder) + items)
        }

        // 1. Personal folders.
        folders = layout.folders.filter { $0.parent == nil }.compactMap {
            folder($0, in: layout.folders, context: "top", section: nil, depth: 0)
        }

        // 2. Owner sections.
        let filedPersonally = Set(layout.folders.flatMap(\.items))
        var keys: [String] = []
        var owned: [String: [TileInfo]] = [:]
        func section(_ key: String) {
            if owned[key] == nil { keys.append(key); owned[key] = [] }
        }
        if !me.isEmpty { section("mine") }
        for o in whoami?.orgs ?? [] { section("org:\(o.id)") }
        for t in tiles where !filedPersonally.contains(t.path) {
            let key = Self.ownerKey(t, user: me)
            section(key)
            owned[key]?.append(t)
        }
        let names = Dictionary((whoami?.orgs ?? []).map { ("org:\($0.id)", $0.displayName) }, uniquingKeysWith: { a, _ in a })
        var sections: [Section] = []
        for key in keys {
            let comps = owned[key] ?? []
            let screens = key.hasPrefix("org:") ? shared.org.filter { "org:\($0.org ?? "")" == key } : []
            guard !comps.isEmpty || !screens.isEmpty else { continue }
            let labels = Self.labels(comps)
            let sharedSet = Self.scope(ofSection: key).map { shared.folders[$0] ?? [] } ?? []
            let filed = Set(sharedSet.flatMap(\.items))
            let scoped = (paths: Set(comps.map(\.path)), labels: labels)
            let folders = sharedSet.filter { $0.parent == nil }.compactMap {
                folder($0, in: sharedSet, context: Self.scope(ofSection: key) ?? key, section: scoped, depth: 0)
            }
            let root = comps.filter { !filed.contains($0.path) }.sorted { a, b in
                let la = labels[a.path] ?? a.path, lb = labels[b.path] ?? b.path
                let c = la.compare(lb, options: [.caseInsensitive])
                return c == .orderedSame ? la < lb : c == .orderedAscending
            }
            let title = key == "mine" ? "Mine" : key == "workspace" ? "Workspace" : names[key] ?? String(key.dropFirst(4))
            sections.append(Section(id: key, title: title,
                                    items: folders.map(Item.folder) + root.map { .tile($0, label: labels[$0.path] ?? $0.path) }
                                        + screens.map(Item.screen),
                                    count: comps.count + screens.count))
        }
        self.sections = sections
    }
}
