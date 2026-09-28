import Foundation

// The app's Home (plans/native.md §4, D125): the first level lists screens
// only, by section as the web sidebar groups them —
//
// - **Mine**: the user's own screens (the `layout` pref), inside the
//   personal folders they are filed in (`side.folders` items `#screen:<id>`,
//   and `#orgscreen:<id>` for an org screen filed there too); the rest at
//   the section's top.
// - **one per org** (named from `whoami.orgs`): the org's shared screens,
//   inside the org's shared folder set when it files them.
// - **Workspace**: the workspace default screen — only while the user has
//   no screen of their own, as the web shell seeds their first screen from
//   it (a copy they then own) — and the `ws` folder set's screens.
//
// A screen lists the tiles the user can see (the catalog), in the layout's
// reading order. Folders that hold no screen (only tiles) stay out: tiles
// are found by search and "All tiles".

public struct HomeModel: Sendable, Equatable {
    public struct Folder: Sendable, Equatable, Identifiable {
        public var id: String
        public var name: String
        public var screens: [ScreenInfo]
        public var children: [Folder]
    }

    public struct Section: Sendable, Equatable, Identifiable {
        /// `mine`, `org:<id>`, `workspace`.
        public var id: String
        public var title: String
        public var folders: [Folder]
        /// Screens not in any of the section's folders.
        public var screens: [ScreenInfo]
    }

    /// The workspace default screen's id (it has none of its own).
    public static let defaultScreenID = "default"

    public var sections: [Section]
    /// Every screen once, in Home's order (Mine, then orgs, then Workspace).
    public var screens: [ScreenInfo]

    public init(sections: [Section] = [], screens: [ScreenInfo] = []) {
        self.sections = sections
        self.screens = screens
    }

    public init(catalog: Catalog, layout: PersonalLayout, shared: SharedScreens, whoami: Whoami?) {
        let visible = Set(catalog.listed.map(\.path))
        func seen(_ s: ScreenInfo) -> ScreenInfo {
            var s = s
            s.tiles = s.tiles.filter { visible.contains($0) }
            return s
        }
        let personal = layout.screens.map(seen)
        let org = shared.org.map(seen)
        let personalByID = Dictionary(personal.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        let orgByID = Dictionary(org.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })

        /// The screen an item names, if it names one that exists.
        func resolve(_ item: String, personalItems: Bool) -> ScreenInfo? {
            if item.hasPrefix("#orgscreen:") { return orgByID[String(item.dropFirst("#orgscreen:".count))] }
            if personalItems, item.hasPrefix("#screen:") { return personalByID[String(item.dropFirst("#screen:".count))] }
            return nil
        }
        /// The folder tree under `parent`, keeping only folders that hold a
        /// screen (themselves or below); `filed` collects what they hold.
        func tree(_ folders: [FolderInfo], parent: String?, personalItems: Bool, filed: inout Set<String>,
                  depth: Int = 0) -> [Folder] {
            guard depth < 32 else { return [] } // a cycle in hand-edited data
            var out: [Folder] = []
            for f in folders where f.parent == parent {
                var seenHere = Set<String>()
                let screens = f.items.compactMap { resolve($0, personalItems: personalItems) }.filter { seenHere.insert($0.id).inserted }
                let children = tree(folders, parent: f.id, personalItems: personalItems, filed: &filed, depth: depth + 1)
                guard !screens.isEmpty || !children.isEmpty else { continue }
                for s in screens { filed.insert(s.id) }
                out.append(Folder(id: f.id, name: f.name, screens: screens, children: children))
            }
            return out
        }

        var sections: [Section] = []
        var all: [ScreenInfo] = []
        func add(_ s: ScreenInfo) { if !all.contains(where: { $0.id == s.id }) { all.append(s) } }

        // Mine.
        var filedMine = Set<String>()
        let mineFolders = tree(layout.folders, parent: nil, personalItems: true, filed: &filedMine)
        let mineRoot = personal.filter { !filedMine.contains($0.id) }
        if !mineFolders.isEmpty || !mineRoot.isEmpty {
            sections.append(Section(id: "mine", title: "Mine", folders: mineFolders, screens: mineRoot))
            Self.walk(mineFolders) { add($0) }
            mineRoot.forEach(add)
        }

        // One per org: whoami's order, then any other org with screens.
        var orgIDs = (whoami?.orgs ?? []).map(\.id)
        for s in org { if let o = s.org, !orgIDs.contains(o) { orgIDs.append(o) } }
        let names = Dictionary((whoami?.orgs ?? []).map { ($0.id, $0.displayName) }, uniquingKeysWith: { a, _ in a })
        for id in orgIDs {
            let screens = org.filter { $0.org == id }
            guard !screens.isEmpty else { continue }
            var filed = Set<String>()
            let own = Set(screens.map(\.id))
            let folders = tree(shared.folders["org:\(id)"] ?? [], parent: nil, personalItems: false, filed: &filed)
                .compactMap { Self.keeping($0, own) }
            sections.append(Section(id: "org:\(id)", title: names[id] ?? id, folders: folders,
                                    screens: screens.filter { !filed.contains($0.id) }))
            Self.walk(folders) { add($0) }
            screens.forEach(add)
        }

        // Workspace.
        var wsScreens: [ScreenInfo] = []
        if personal.isEmpty, let d = shared.workspaceDefault {
            wsScreens.append(seen(ScreenInfo(id: Self.defaultScreenID, name: "Home", kind: .workspaceDefault, tiles: d)))
        }
        var filedWS = Set<String>()
        let wsFolders = tree(shared.folders["ws"] ?? [], parent: nil, personalItems: false, filed: &filedWS)
        if !wsScreens.isEmpty || !wsFolders.isEmpty {
            sections.append(Section(id: "workspace", title: "Workspace", folders: wsFolders, screens: wsScreens))
            wsScreens.forEach(add)
            Self.walk(wsFolders) { add($0) }
        }
        self.sections = sections
        self.screens = all
    }

    private static func walk(_ folders: [Folder], _ f: (ScreenInfo) -> Void) {
        for d in folders {
            d.screens.forEach(f)
            walk(d.children, f)
        }
    }

    /// `f` with only the screens in `ids` (an org's folder set filing another
    /// org's screen shows it under that org, not here); nil when emptied.
    private static func keeping(_ f: Folder, _ ids: Set<String>) -> Folder? {
        var f = f
        f.screens = f.screens.filter { ids.contains($0.id) }
        f.children = f.children.compactMap { keeping($0, ids) }
        return f.screens.isEmpty && f.children.isEmpty ? nil : f
    }

    public func screen(_ id: String) -> ScreenInfo? { screens.first { $0.id == id } }

    /// The screen a tile sits on, for where "back" goes from it: `current`
    /// when it's there, else the first screen (Home's order) that has it;
    /// nil (Home) when none does. `tiles` gives a screen's tiles as the
    /// phone shows them (the phone arrangement may add or hide some).
    public func screenID(containing path: String, preferring current: String?,
                         tiles: (ScreenInfo) -> [String] = { $0.tiles }) -> String? {
        if let current, let s = screen(current), tiles(s).contains(path) { return current }
        return screens.first { tiles($0).contains(path) }?.id
    }
}
