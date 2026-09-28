import Foundation
import Testing
@testable import XbinCore

/// "All tiles" = the web sidebar's tree: each case one rule of
/// workspace-template/shell/bx-side.js (and shell-kit.js / menus.js).
@Suite struct ClientNavigatorTests {
    func tile(_ path: String, owner: String = "", state: String = "", template: Bool = false, hasIndex: Bool = true) -> TileInfo {
        TileInfo(path: path, hasIndex: hasIndex, state: state, owner: owner, template: template)
    }

    func who(_ id: String, orgs: [(String, String)] = []) -> Whoami {
        Whoami(json: ["id": .string(id), "orgs": .array(orgs.map { ["id": .string($0.0), "name": .string($0.1)] })])
    }

    func ids(_ items: [NavigatorModel.Item]) -> [String] { items.map(\.id) }

    func folder(_ item: NavigatorModel.Item?) -> NavigatorModel.Folder? {
        if case .folder(let f)? = item { return f }
        return nil
    }

    /// shell-kit.js ownerKeyOf.
    @Test func ownerKeys() {
        #expect(NavigatorModel.ownerKey(tile("a", owner: "user:alice"), user: "alice") == "mine")
        #expect(NavigatorModel.ownerKey(tile("a", owner: "user:alice"), user: "bob") == "workspace")
        #expect(NavigatorModel.ownerKey(tile("a", owner: "user:"), user: "") == "workspace")   // no caller id: never mine
        #expect(NavigatorModel.ownerKey(tile("a", owner: "org:eng"), user: "alice") == "org:eng")
        #expect(NavigatorModel.ownerKey(tile("a"), user: "alice") == "workspace")
        #expect(NavigatorModel.scope(ofSection: "workspace") == "ws")
        #expect(NavigatorModel.scope(ofSection: "org:eng") == "org:eng")
        #expect(NavigatorModel.scope(ofSection: "mine") == nil)
    }

    /// Sections: mine, then whoami's orgs in its order, then any other owner
    /// in the catalog's order; a section with nothing to show is left out,
    /// an org's with only screens stays; titles from whoami's org names.
    @Test func sectionsInOrder() {
        let cat = Catalog(tiles: [tile("w/one"), tile("o/x", owner: "org:ops"), tile("e/y", owner: "org:eng"),
                                  tile("m/z", owner: "user:alice"), tile("q/v", owner: "org:qa")])
        let shared = SharedScreens(org: [ScreenInfo(id: "s9", name: "Board", kind: .org, org: "legal", tiles: [])])
        let nav = NavigatorModel(catalog: cat, layout: PersonalLayout(), shared: shared,
                                 whoami: who("alice", orgs: [("eng", "Engineering"), ("legal", ""), ("hr", "People")]))
        #expect(nav.sections.map(\.id) == ["mine", "org:eng", "org:legal", "workspace", "org:ops", "org:qa"])
        #expect(nav.sections.map(\.title) == ["Mine", "Engineering", "legal", "Workspace", "ops", "qa"])
        #expect(ids(nav.sections[2].items) == ["s:s9"] && nav.sections[2].count == 1)   // screens only
        #expect(nav.showsSectionHeaders)

        // One section alone: no header (a solo workspace).
        let solo = NavigatorModel(catalog: Catalog(tiles: [tile("a/b")]), layout: PersonalLayout(), shared: SharedScreens(),
                                  whoami: who("alice"))
        #expect(solo.sections.map(\.id) == ["workspace"] && !solo.showsSectionHeaders)
    }

    /// Skipped: the shell, blueprints, archived, hidden (menus.js offloaded/
    /// hidden), disabled and page-less tiles — everywhere, folders too.
    @Test func skipped() {
        let cat = Catalog(tiles: [tile("root"), tile("shell"), tile("a/tpl", template: true), tile("a/off", state: "offloaded"),
                                  tile("a/full", state: "offloaded-full"), tile("a/hid", state: "hidden"),
                                  tile("a/dis", state: "disabled"), tile("a/noindex", hasIndex: false), tile("a/ok")])
        let layout = PersonalLayout(folders: [FolderInfo(id: "f", name: "F", items: ["a/hid", "a/tpl", "a/ok"], open: true)])
        let nav = NavigatorModel(catalog: cat, layout: layout, shared: SharedScreens(), whoami: who("alice"))
        #expect(ids(nav.folders[0].items) == ["t:a/ok"])
        #expect(nav.sections.isEmpty)   // a/ok is filed personally; nothing else is listed
        #expect(cat.listed.map(\.path) == ["a/ok"])
        #expect(TileInfo(json: ["path": "t", "template": true])?.template == true)
        #expect(TileInfo(json: ["path": "t", "state": "hidden"])?.isListed == false)
    }

    /// Labels (D55): the basename, the path when two of a section share it
    /// — within the section only; the root sorted by label, case aside.
    @Test func labelsAndOrder() {
        let cat = Catalog(tiles: [tile("apps/notes"), tile("tools/notes"), tile("apps/Beta"), tile("apps/alpha"),
                                  tile("x/notes", owner: "org:eng")])
        let nav = NavigatorModel(catalog: cat, layout: PersonalLayout(), shared: SharedScreens(), whoami: who("alice"))
        let ws = nav.sections.first { $0.id == "workspace" }!
        let labels = ws.items.compactMap { item -> String? in
            if case .tile(_, let l) = item { return l }
            return nil
        }
        #expect(labels == ["alpha", "apps/notes", "Beta", "tools/notes"])
        let eng = nav.sections.first { $0.id == "org:eng" }!
        if case .tile(_, let l)? = eng.items.first { #expect(l == "notes") } else { Issue.record("eng's tile") }
        #expect(NavigatorModel.labels([tile("a/b"), tile("c/b"), tile("d/e")]) == ["a/b": "a/b", "c/b": "c/b", "d/e": "e"])
    }

    /// Personal folders come first and hold any listed tile, `#screen:` (a
    /// personal screen) and `#orgscreen:`; stale ones drop; a filed tile
    /// leaves its section; sub-folders before items; `open` as saved; an
    /// empty personal folder stays.
    @Test func personalFolders() {
        let cat = Catalog(tiles: [tile("apps/a"), tile("apps/b"), tile("apps/c", owner: "org:eng")])
        let layout = PersonalLayout(
            screens: [ScreenInfo(id: "s1", name: "Mine", kind: .personal, tiles: [])],
            folders: [FolderInfo(id: "f1", name: "Work", items: ["apps/c", "#screen:s1", "#orgscreen:o1", "#screen:gone", "gone/x", "apps/c"],
                                 icon: "🛠", open: true),
                      FolderInfo(id: "f2", name: "Nested", items: ["apps/b"], parent: "f1"),
                      FolderInfo(id: "f3", name: "Empty", items: [])])
        let shared = SharedScreens(org: [ScreenInfo(id: "o1", name: "Eng board", kind: .org, org: "eng", tiles: [])])
        let nav = NavigatorModel(catalog: cat, layout: layout, shared: shared, whoami: who("alice", orgs: [("eng", "Eng")]))
        #expect(nav.folders.map(\.name) == ["Work", "Empty"])
        #expect(nav.folders[0].open && !nav.folders[1].open && nav.folders[0].icon == "🛠")
        #expect(ids(nav.folders[0].items) == ["f:top/f2", "t:apps/c", "s:s1", "s:o1"])   // duplicates once
        #expect(ids(folder(nav.folders[0].items.first)?.items ?? []) == ["t:apps/b"])
        // apps/b and apps/c are filed: the workspace keeps apps/a; eng keeps its screen.
        #expect(nav.sections.map(\.id) == ["org:eng", "workspace"])
        #expect(ids(nav.sections[0].items) == ["s:o1"])
        #expect(ids(nav.sections[1].items) == ["t:apps/a"])
    }

    /// A section's shared folders (`/screens` folders[scope]): before its
    /// root, holding only the section's own tiles (never a personally filed
    /// one, another owner's or a screen ref), filed ones leave the root, a
    /// folder with nothing to show is left out, `side.sharedOpen` folds.
    @Test func sharedFolders() {
        let cat = Catalog(tiles: [tile("w/a"), tile("w/b"), tile("w/c"), tile("w/mine"), tile("e/x", owner: "org:eng")])
        let layout = PersonalLayout(folders: [FolderInfo(id: "p", name: "P", items: ["w/mine"])], sharedOpen: ["s2": false])
        let shared = SharedScreens(folders: [
            "ws": [FolderInfo(id: "s1", name: "Shared", items: ["w/a", "e/x", "w/mine", "#screen:s1"]),
                   FolderInfo(id: "s2", name: "Sub", items: ["w/b"], parent: "s1"),
                   FolderInfo(id: "s3", name: "Nothing for you", items: ["e/x", "gone"]),
                   FolderInfo(id: "s4", name: "Empty parent", items: []),
                   FolderInfo(id: "s5", name: "Empty child", items: ["gone"], parent: "s4")],
            "org:eng": [FolderInfo(id: "e1", name: "Eng", items: ["e/x"], icon: "⚙")],
        ])
        let nav = NavigatorModel(catalog: cat, layout: layout, shared: shared, whoami: who("alice", orgs: [("eng", "")]))
        let ws = nav.sections.first { $0.id == "workspace" }!
        #expect(ids(ws.items) == ["f:ws/s1", "t:w/c"])
        let s1 = folder(ws.items.first)!
        #expect(s1.open && ids(s1.items) == ["f:ws/s2", "t:w/a"])
        #expect(folder(s1.items.first)?.open == false)
        #expect(ws.count == 3)   // w/a, w/b, w/c (w/mine is filed personally)
        let eng = nav.sections.first { $0.id == "org:eng" }!
        #expect(ids(eng.items) == ["f:org:eng/e1"] && folder(eng.items.first)?.icon == "⚙")
    }

    /// Hand-edited data never loops: repeated folder ids stop at a depth.
    @Test func folderCycles() {
        let layout = PersonalLayout(folders: [FolderInfo(id: "x", name: "X", items: []),
                                              FolderInfo(id: "x", name: "X again", items: [], parent: "x")])
        let nav = NavigatorModel(catalog: Catalog(tiles: []), layout: layout, shared: SharedScreens(), whoami: nil)
        #expect(nav.folders.count == 1)
        var depth = 0
        var f = nav.folders.first
        while let cur = f { depth += 1; f = folder(cur.items.first) }
        #expect(depth <= 33)
        #expect(!nav.isEmpty)
        #expect(NavigatorModel(catalog: Catalog(tiles: []), layout: PersonalLayout(), shared: SharedScreens(), whoami: nil).isEmpty)
    }
}

@Suite struct ClientDataURITests {
    static let png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

    @Test func parses() throws {
        let p = try #require(DataURI.parse("data:image/png;base64," + Self.png))
        #expect(p.mediaType == "image/png" && p.isBitmap && !p.isSVG)
        #expect(p.data.prefix(4) == Data([0x89, 0x50, 0x4E, 0x47]))
        // Case, parameters, whitespace and missing padding, as browsers take them.
        let q = try #require(DataURI.parse("  DATA:Image/PNG;name=x.png;BASE64,\(Self.png.dropLast(2))\n "))
        #expect(q.mediaType == "image/png" && q.data == p.data)
        let svg = try #require(DataURI.parse("data:image/svg+xml;charset=utf-8,%3Csvg%20xmlns%3D%22http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%22%2F%3E"))
        #expect(svg.isSVG && String(decoding: svg.data, as: UTF8.self) == "<svg xmlns=\"http://www.w3.org/2000/svg\"/>")
        #expect(DataURI.parse("data:,hi")?.mediaType == "text/plain")
    }

    @Test func refuses() {
        #expect(DataURI.parse("") == nil)
        #expect(DataURI.parse("https://x/i.png") == nil)
        #expect(DataURI.parse("data:image/png;base64") == nil)          // no comma
        #expect(DataURI.parse("data:image/png;base64,") == nil)         // nothing
        #expect(DataURI.parse("data:image/png;base64,a") == nil)        // not base64
        #expect(DataURI.parse("data:image/png;base64,!!!!") == nil)
        #expect(DataURI.parse("data:image/png;base64," + String(repeating: "A", count: DataURI.maxLength)) == nil)
        #expect(DataURI.parse("🙂") == nil)
    }

    @Test func cachedAndBranding() {
        let cache = DataURICache(capacity: 2)
        let a = "data:image/png;base64," + Self.png
        #expect(cache.decode(a) != nil && cache.decode(a) != nil && cache.count == 1)
        #expect(cache.decode("nope") == nil && cache.count == 2)   // a miss is remembered too
        _ = cache.decode("data:,x")
        #expect(cache.count == 2)                                   // the oldest went
        let b = BrandingCache(response: ["title": "Acme", "icon": .string(a), "hasIcon": true])
        #expect(b.iconImage?.mediaType == "image/png" && b.isBranded)
        #expect(BrandingCache(title: "", icon: "data:text/plain,hi").iconImage == nil)   // not an image
        #expect(!BrandingCache(title: "").isBranded && BrandingCache(title: "", icon: "🙂").isBranded)
    }
}
