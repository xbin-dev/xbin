import Foundation
import Testing
@testable import XbinCore

// The phone's screens (D125): Home's tree, the phone arrangement's merge
// rule, the web layout edits, creating a tile, tile statuses.

@Suite struct ClientHomeTests {
    static let catalog = Catalog(tiles: ["apps/a", "apps/b", "apps/c", "apps/d", "tiles/admin", "gone/offloaded"].map {
        TileInfo(path: $0, state: $0.hasPrefix("gone/") ? "offloaded" : "")
    })

    /// Mine: personal screens inside the personal folders filing them
    /// (nested, `#screen:` and `#orgscreen:`), the rest on top; each org
    /// named from whoami, its screens inside its shared folder set; the
    /// workspace default only while the user has no screen.
    @Test func homeTreeFromLayoutScreensAndWhoami() {
        let layout = PersonalLayout(json: [
            "screens": [
                ["id": "s1", "name": "Home", "tiles": [["path": "apps/b", "x": 576, "y": 0], ["path": "apps/a", "x": 0, "y": 0]]],
                ["id": "s2", "name": "Ops", "tiles": [["path": "tiles/admin"], ["path": "gone/offloaded"]]],
                ["id": "s3", "name": "Deep", "tiles": []],
            ],
            "side": ["folders": [
                ["id": "f1", "name": "Work", "items": ["#screen:s2", "apps/c", "#orgscreen:o1", "#screen:nope"]],
                ["id": "f2", "name": "Nested", "parent": "f1", "items": ["#screen:s3"]],
                ["id": "f3", "name": "Tiles only", "items": ["apps/d"]],
            ]],
        ])
        let shared = SharedScreens(json: [
            "default": ["tiles": [["path": "apps/d"]]],
            "org": [["id": "o1", "org": "eng", "name": "Eng board", "tiles": [["path": "apps/c"]]],
                    ["id": "o2", "org": "eng", "name": "Eng filed", "tiles": []],
                    ["id": "o3", "org": "ops", "name": "Ops wall", "tiles": [["path": "apps/a"]]]],
            "folders": ["org:eng": ["folders": [["id": "e1", "name": "Boards", "items": ["#orgscreen:o2", "#orgscreen:o3", "apps/a"]]]]],
        ])
        let who = Whoami(json: ["id": "alice", "orgs": [["id": "ops", "name": "Operations"], ["id": "eng", "name": "Engineering"]]])
        let home = HomeModel(catalog: Self.catalog, layout: layout, shared: shared, whoami: who)
        #expect(home.sections.map(\.id) == ["mine", "org:ops", "org:eng"])
        #expect(home.sections.map(\.title) == ["Mine", "Operations", "Engineering"])
        let mine = home.sections[0]
        #expect(mine.screens.map(\.name) == ["Home"])                          // s2, s3 are filed
        #expect(mine.folders.map(\.name) == ["Work"])                          // "Tiles only" holds no screen
        #expect(mine.folders[0].screens.map(\.id) == ["s2", "o1"])              // a stale #screen dropped
        #expect(mine.folders[0].children.map(\.name) == ["Nested"] && mine.folders[0].children[0].screens.map(\.id) == ["s3"])
        #expect(home.screen("s1")?.tiles == ["apps/a", "apps/b"])              // reading order
        #expect(home.screen("s2")?.tiles == ["tiles/admin"])                   // what the user can't open: gone
        // Orgs in whoami's order; an org's folder set files its own screens only.
        let eng = home.sections[2]
        #expect(eng.folders.map(\.name) == ["Boards"] && eng.folders[0].screens.map(\.id) == ["o2"])
        #expect(eng.screens.map(\.id) == ["o1"])
        #expect(home.sections[1].screens.map(\.id) == ["o3"])
        // Every screen once, in Home's order.
        #expect(home.screens.map(\.id) == ["s2", "o1", "s3", "s1", "o3", "o2"])

        // No screen of one's own: the workspace default, as the web seeds it.
        let fresh = HomeModel(catalog: Self.catalog, layout: PersonalLayout(), shared: shared, whoami: Whoami(json: ["id": "bob"]))
        #expect(fresh.sections.map(\.id) == ["org:eng", "org:ops", "workspace"]) // orgs not in whoami: by first screen
        #expect(fresh.sections.last?.screens.map(\.id) == [HomeModel.defaultScreenID])
        #expect(fresh.screen("default")?.kind == .workspaceDefault && fresh.screen("default")?.tiles == ["apps/d"])
        #expect(HomeModel(catalog: Self.catalog, layout: PersonalLayout(), shared: SharedScreens(), whoami: nil).sections.isEmpty)
    }

    /// Where "back" goes from a tile: the screen it was opened from when it
    /// is there, else the first that has it, else Home.
    @Test func theScreenATileSitsOn() {
        let layout = PersonalLayout(json: ["screens": [["id": "s1", "name": "A", "tiles": [["path": "apps/a"]]],
                                                       ["id": "s2", "name": "B", "tiles": [["path": "apps/a"], ["path": "apps/b"]]]]])
        let home = HomeModel(catalog: Self.catalog, layout: layout, shared: SharedScreens(), whoami: nil)
        #expect(home.screenID(containing: "apps/a", preferring: "s2") == "s2")
        #expect(home.screenID(containing: "apps/a", preferring: nil) == "s1")
        #expect(home.screenID(containing: "apps/b", preferring: "s1") == "s2")
        #expect(home.screenID(containing: "apps/c", preferring: "s1") == nil)
        // The phone arrangement counts (a tile added on the phone only).
        #expect(home.screenID(containing: "apps/c", preferring: nil, tiles: { $0.id == "s2" ? $0.tiles + ["apps/c"] : $0.tiles }) == "s2")
    }

    @Test func whoamiOrgsAndPolicy() {
        let w = Whoami(json: ["id": "bob", "tileCreation": "org-only",
                              "orgs": [["id": "eng", "name": "Engineering", "create": true], ["id": "x"], ["name": "no id"]]])
        #expect(w.orgs.map(\.id) == ["eng", "x"] && w.orgs[0].create && w.orgs[1].displayName == "x")
        #expect(!w.personalTiles)                                              // older xbind: the policy decides
        #expect(Whoami(json: ["id": "bob"]).personalTiles)
        #expect(!Whoami(json: ["id": "bob", "personalTiles": false]).personalTiles)
    }
}

@Suite struct ClientMobileScreensTests {
    /// Pref order first, then what the screen gained since (small), minus
    /// hidden; a screen never arranged: the layout's order, all small.
    @Test func mergeRule() {
        let pref = MobileScreens(json: ["v": 1, "screens": [
            "s1": ["tiles": [["path": "apps/c", "size": "wide"], ["path": "apps/a"], ["path": "apps/phone-only", "size": "small"],
                             ["path": "apps/c"], ["path": "apps/b", "size": "huge"]],
                   "hidden": ["apps/d", "apps/b"]],
            "bad": "junk",
        ]])
        let cards = pref.cards(for: "s1", screenTiles: ["apps/a", "apps/b", "apps/d", "apps/e", "apps/f"])
        #expect(cards.map(\.path) == ["apps/c", "apps/a", "apps/phone-only", "apps/e", "apps/f"])
        #expect(cards.map(\.size) == [.wide, .small, .small, .small, .small])
        #expect(pref.hidden(for: "s1", screenTiles: ["apps/b", "apps/x"]) == ["apps/b"])
        // What the user can't see any more goes.
        #expect(pref.cards(for: "s1", screenTiles: [], visible: { $0 != "apps/c" }).map(\.path) == ["apps/a", "apps/phone-only"])
        // Never arranged (or an entry of a shape we don't know): reading order.
        #expect(pref.cards(for: "s2", screenTiles: ["apps/b", "apps/a", "apps/b"]).map(\.path) == ["apps/b", "apps/a"])
        #expect(pref.cards(for: "bad", screenTiles: ["apps/a"]) == [MobileScreens.Card(path: "apps/a")])
        #expect(MobileScreens(json: nil).screens.isEmpty && MobileScreens(json: "x").screens.isEmpty)
    }

    /// Edit mode's Done and "+ Add": stored as given; the pref's other
    /// screens and keys (a newer app's) stay as they were.
    @Test func arrangeAndStore() throws {
        var m = MobileScreens()
        m.set("s1", cards: [.init(path: "apps/b", size: .wide), .init(path: "apps/a"), .init(path: "apps/b")], hidden: ["apps/c", "apps/a"])
        #expect(m.screens["s1"] == .init(tiles: [.init(path: "apps/b", size: .wide), .init(path: "apps/a")], hidden: ["apps/c"]))
        m.add("apps/c", to: "s1", screenTiles: ["apps/c", "apps/d"])            // un-hides it, at the end
        #expect(m.cards(for: "s1", screenTiles: ["apps/c", "apps/d"]).map(\.path) == ["apps/b", "apps/a", "apps/d", "apps/c"])
        #expect(m.screens["s1"]?.hidden == [])
        m.add("apps/a", to: "s1", screenTiles: [])                              // already there: unchanged
        #expect(m.screens["s1"]?.tiles.map(\.path) == ["apps/b", "apps/a", "apps/d", "apps/c"])

        let raw: JSONValue = ["v": 1, "future": true, "screens": ["s9": ["tiles": [], "hidden": [], "extra": 1]]]
        let out = m.merged(into: raw, screen: "s1")
        #expect(out["future"] == true && out["screens"]?["s9"]?["extra"] == 1)
        let back = MobileScreens(json: out)
        #expect(back.screens["s1"] == m.screens["s1"])
        #expect(m.merged(into: nil, screen: "s1")["v"] == 1)
        // Through JSON text and back.
        let parsed = try JSONValue(parsing: out.jsonData)
        #expect(MobileScreens(json: parsed) == back)
    }

    /// "+ New screen": appended; an empty layout first gets the Home the
    /// web shell would have seeded (so the user keeps it).
    @Test func newScreen() {
        let one = LayoutPref.addingScreen(id: "n1", name: "Screen 2", to: ["screens": [["id": "s1", "name": "Home", "tiles": []]],
                                                                           "active": "s1", "drafts": ["org": [:]]])
        #expect(one["screens"]?.arrayValue?.map { $0["id"]?.stringValue } == ["s1", "n1"])
        #expect(one["active"] == "s1" && one["drafts"] != nil)
        let seeded = LayoutPref.addingScreen(id: "n1", name: "Screen 1", to: nil, seed: [["path": "apps/d", "x": 0, "y": 0]])
        let screens = seeded["screens"]?.arrayValue ?? []
        #expect(screens.count == 2 && screens[0]["name"] == "Home" && screens[0]["tiles"]?[0]?["path"] == "apps/d")
        #expect(seeded["active"] == screens[0]["id"])
        let bare = LayoutPref.addingScreen(id: "n1", name: "Screen 1", to: nil)
        #expect(bare["screens"]?.arrayValue?.count == 1 && bare["active"] == "n1")
        #expect(LayoutPref.nextScreenName(one) == "Screen 3" && LayoutPref.nextScreenName(nil) == "Screen 1")
        let id = LayoutPref.newScreenID()
        #expect(id.count == 7 && id.allSatisfy { $0.isLowercase || $0.isNumber })
    }

    /// A tile created on the phone lands on a personal screen's web layout
    /// at the web shell's free spot.
    @Test func placeATileOnTheWebLayout() {
        let w = LayoutPref.tileWidth, h = LayoutPref.tileHeight
        let jw = JSONValue.double(w), jh = JSONValue.double(h)
        #expect(LayoutPref.freeSpot([]) == (0, 0))
        #expect(LayoutPref.freeSpot([["path": "a", "x": 0, "y": 0, "w": jw, "h": jh]]) == (w, 0))
        #expect(LayoutPref.freeSpot([["path": "a", "x": 0, "y": 0, "w": .double(w * 2), "h": jh]]) == (0, h))
        #expect(LayoutPref.freeSpot([["path": "a", "x": 0, "y": 0, "float": ["x": 1]]]) == (0, 0))   // floats don't count
        #expect(LayoutPref.freeSpot([["path": "a", "x": 100, "y": 0, "w": 48, "h": 48]]) == (w, 0))  // a small one in the way

        let layout: JSONValue = ["screens": [["id": "s1", "name": "Home", "tiles": [["path": "apps/a", "x": 0, "y": 0, "w": jw, "h": jh]]],
                                             ["id": "s2", "name": "Two", "tiles": []]],
                                 "active": "s1", "side": ["folders": []]]
        let out = LayoutPref.adding(tile: "apps/new", toScreen: "s1", in: layout)
        let tiles = out?["screens"]?[0]?["tiles"]?.arrayValue ?? []
        #expect(tiles.count == 2 && tiles[1]["path"] == "apps/new")
        #expect(tiles[1]["x"]?.doubleValue == w && tiles[1]["y"]?.doubleValue == 0 && tiles[1]["w"]?.doubleValue == w)
        #expect(out?["side"] != nil && out?["active"] == "s1" && out?["screens"]?[1] == layout["screens"]?[1])
        #expect(LayoutPref.adding(tile: "apps/a", toScreen: "s1", in: layout) == nil)   // already there
        #expect(LayoutPref.adding(tile: "apps/x", toScreen: "nope", in: layout) == nil)
    }
}

@Suite struct ClientTileCreateTests {
    /// The web shell's _ownerOptions: me; orgs with create or admin; the
    /// workspace for an admin; "me" gone without personal tiles (an admin
    /// keeps it).
    @Test func ownerChoices() {
        let admin = Whoami(json: ["id": "root", "kind": "user", "admin": true, "personalTiles": false,
                                  "orgs": [["id": "eng", "name": "Engineering", "create": true], ["id": "ro"], ["id": "ops", "admin": true]]])
        #expect(TileCreate.owners(admin).map(\.value) == ["user:root", "org:eng", "org:ops", ""])
        #expect(TileCreate.owners(admin).map(\.label) == ["Me (personal)", "Engineering", "ops", "Workspace"])
        #expect(TileCreate.defaultOwner(admin) == "")
        let member = Whoami(json: ["id": "bob", "kind": "user", "personalTiles": true, "orgs": [["id": "eng", "create": true]]])
        #expect(TileCreate.owners(member).map(\.value) == ["user:bob", "org:eng"])
        #expect(TileCreate.defaultOwner(member) == "user:bob")
        let orgOnly = Whoami(json: ["id": "bob", "kind": "user", "personalTiles": false, "orgs": [["id": "eng", "create": true]]])
        #expect(TileCreate.owners(orgOnly).map(\.value) == ["org:eng"])
        #expect(TileCreate.defaultOwner(orgOnly) == "org:eng")
        let policy = Whoami(json: ["id": "bob", "kind": "user", "tileCreation": "org-only"])   // an xbind before personalTiles
        #expect(TileCreate.owners(policy).isEmpty && TileCreate.defaultOwner(policy) == "")
        #expect(TileCreate.owners(Whoami(json: ["kind": "element", "id": "apps/x"])).isEmpty)
    }

    @Test func namesAndRequest() throws {
        #expect(TileCreate.slug("  My Tile! v2 ") == "my-tile-v2")
        #expect(TileCreate.slug("Ünïcode—Name") == "n-code-name")
        #expect(TileCreate.tilePath(name: "Weather board") == "apps/weather-board")
        #expect(TileCreate.tilePath(name: "!!!") == nil)
        let r = try #require(TileCreate.request(name: " Weather board ", owner: "user:bob"))
        #expect(r.method == "POST" && r.path == "/api/xbin/create")
        let body = try JSONValue(parsing: try #require(r.body))
        #expect(body == ["path": "apps/weather-board", "title": "Weather board", "owner": "user:bob"])
        let ws = try JSONValue(parsing: try #require(TileCreate.request(name: "x", owner: "")?.body))
        #expect(ws["owner"] == nil)
    }

    @Test func tileStatuses() {
        var s = TileStatuses(json: ["statuses": ["apps/a": ["level": "error", "message": "down", "ts": 1],
                                                 "apps/b": ["level": "ok", "message": ""],
                                                 "apps/c": ["level": "ok", "message": "fine"]]])
        #expect(s["apps/a"] == .init(level: "error", message: "down") && s["apps/b"] == nil && s["apps/c"]?.level == "ok")
        s.apply(tile: "apps/a", level: "ok", message: "", transient: false)       // cleared
        s.apply(tile: "apps/d", level: "warn", message: "slow", transient: false)
        s.apply(tile: "apps/e", level: "error", message: "one-shot", transient: true)  // a notification, not a status
        #expect(s["apps/a"] == nil && s["apps/d"]?.level == "warn" && s["apps/e"] == nil)
        #expect(AppEvent.parse(#"{"type":"status","component":"apps/x","data":{"level":"info","message":"m","transient":true}}"#)
            == .tileStatus(component: "apps/x", level: "info", message: "m", transient: true))
    }

    /// Another client wrote `layout` or `mobile-screens` (the `prefs`
    /// event): Home reloads — never for this app's own writes, another
    /// bucket or another key.
    @Test func prefsEvents() throws {
        let e = AppEvent.parse(#"{"type":"prefs","component":"root","data":{"key":"layout","writer":"app-1"}}"#)
        #expect(e == .prefs(component: "root", key: "layout", writer: "app-1"))
        #expect(AppEvent.parse(#"{"type":"prefs","component":"root","data":{"key":"layout"}}"#) == .prefs(component: "root", key: "layout", writer: ""))
        #expect(LayoutPref.concernsHome(component: "root", key: "layout", writer: "web", me: "app-1"))
        #expect(LayoutPref.concernsHome(component: "root", key: "mobile-screens", writer: "", me: "app-1"))
        #expect(!LayoutPref.concernsHome(component: "root", key: "layout", writer: "app-1", me: "app-1"))
        #expect(!LayoutPref.concernsHome(component: "apps/x", key: "layout", writer: "web", me: "app-1"))
        #expect(!LayoutPref.concernsHome(component: "root", key: "settings", writer: "web", me: "app-1"))
        let put = LayoutPref.put(MobileScreens.path, ["v": 1], writer: String(repeating: "w", count: 80))
        #expect(put.method == "PUT" && put.header("X-Prefs-Writer")?.count == 64 && put.header("Content-Type") == "application/json")
    }
}
