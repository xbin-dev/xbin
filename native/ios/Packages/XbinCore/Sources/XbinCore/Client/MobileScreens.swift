import Foundation

// A screen on the phone (plans/native.md §4, D117): a 2-column grid of
// cards, small (one column) or wide (both). How a user arranges a screen on
// their phone is theirs, in a pref of its own next to the web shell's
// `layout` (the same bucket — per user, `root`), for every kind of screen
// (personal, org, the workspace default):
//
//   mobile-screens = {v:1, screens:{<screenId>:{tiles:[{path,size}], hidden:[path]}}}
//
// The merge rule: the pref's order first; then the tiles the screen gained
// since (appended, small); minus `hidden`. A screen the phone never
// arranged shows its tiles in the layout's reading order, all small. The
// web shell's layout format never changes; the one edit the phone makes to
// it is adding a tile it created to a personal screen (LayoutEdit).

/// The phone arrangement pref.
public struct MobileScreens: Sendable, Equatable {
    public static let key = "mobile-screens"
    public static let path = "/api/xbin/prefs/mobile-screens"
    public static let version: Int64 = 1

    public struct Card: Sendable, Hashable, Identifiable {
        public var path: String
        public var size: CardSize
        public var id: String { path }

        public init(path: String, size: CardSize = .small) {
            self.path = path
            self.size = size
        }
    }

    public struct Arrangement: Sendable, Equatable {
        /// The cards in order (hidden ones never among them).
        public var tiles: [Card]
        /// Tiles of the screen kept off this phone's.
        public var hidden: [String]

        public init(tiles: [Card] = [], hidden: [String] = []) {
            self.tiles = tiles
            self.hidden = hidden
        }

        var json: JSONValue {
            .object(["tiles": .array(tiles.map { .object(["path": .string($0.path), "size": .string($0.size.rawValue)]) }),
                     "hidden": .array(hidden.map { .string($0) })])
        }

        init(json v: JSONValue) {
            var seen = Set<String>()
            tiles = (v["tiles"]?.arrayValue ?? []).compactMap { t in
                guard let p = t["path"]?.stringValue ?? t.stringValue, !p.isEmpty, seen.insert(p).inserted else { return nil }
                return Card(path: p, size: t["size"]?.stringValue.flatMap(CardSize.init(rawValue:)) ?? .small)
            }
            hidden = (v["hidden"]?.arrayValue ?? []).compactMap(\.stringValue)
        }
    }

    public var screens: [String: Arrangement]

    public init(screens: [String: Arrangement] = [:]) { self.screens = screens }

    /// Lenient: a missing or odd pref is an empty one; an unknown screen
    /// entry shape reads as nothing arranged.
    public init(json: JSONValue?) {
        var s: [String: Arrangement] = [:]
        for (id, v) in json?["screens"]?.objectValue ?? [:] where v.objectValue != nil { s[id] = Arrangement(json: v) }
        screens = s
    }

    /// The cards of screen `id` whose own tiles are `screenTiles` (reading
    /// order), by the merge rule; `visible` drops what the user can't see
    /// (a tile deleted since, or no longer readable).
    public func cards(for id: String, screenTiles: [String], visible: (String) -> Bool = { _ in true }) -> [Card] {
        guard let a = screens[id] else {
            var seen = Set<String>()
            return screenTiles.filter { visible($0) && seen.insert($0).inserted }.map { Card(path: $0) }
        }
        let hidden = Set(a.hidden)
        var out = a.tiles.filter { !hidden.contains($0.path) && visible($0.path) }
        var have = Set(out.map(\.path))
        for p in screenTiles where !hidden.contains(p) && visible(p) && have.insert(p).inserted {
            out.append(Card(path: p))
        }
        return out
    }

    /// The tiles screen `id` keeps off the phone that it still has.
    public func hidden(for id: String, screenTiles: [String]) -> [String] {
        let on = Set(screenTiles)
        return (screens[id]?.hidden ?? []).filter { on.contains($0) }
    }

    /// Arranges screen `id` as `cards`, keeping `hidden` off it.
    public mutating func set(_ id: String, cards: [Card], hidden: [String]) {
        let shown = Set(cards.map(\.path))
        var seen = Set<String>()
        screens[id] = Arrangement(tiles: cards.filter { seen.insert($0.path).inserted },
                                  hidden: hidden.filter { !shown.contains($0) })
    }

    /// Adds `path` to screen `id` as the merge rule shows it now (at the
    /// end, unless it's there; no longer hidden).
    public mutating func add(_ path: String, to id: String, screenTiles: [String], size: CardSize = .small) {
        var cards = self.cards(for: id, screenTiles: screenTiles)
        if !cards.contains(where: { $0.path == path }) { cards.append(Card(path: path, size: size)) }
        set(id, cards: cards, hidden: (screens[id]?.hidden ?? []).filter { $0 != path })
    }

    /// `raw` (the pref as stored, maybe written by a newer app) with screen
    /// `id`'s entry replaced by this model's: every other key and screen as
    /// it was.
    public func merged(into raw: JSONValue?, screen id: String) -> JSONValue {
        var top = raw?.objectValue ?? [:]
        var scr = top["screens"]?.objectValue ?? [:]
        scr[id] = screens[id]?.json
        top["screens"] = .object(scr)
        if top["v"] == nil { top["v"] = .int(Self.version) }
        return .object(top)
    }
}

/// The web shell's `layout` pref: where it lives and the edits the phone
/// makes to it (a new personal screen; a tile created on the phone placed
/// on a personal screen). Edits work on the stored JSON, so every field
/// the phone doesn't know (drafts, recents, the sidebar) stays as it was.
public enum LayoutPref {
    /// `GET|PUT /api/xbin/prefs/layout`. The shell (`/` is `/c/root/`)
    /// writes the `root` bucket: the one a request without a frame token —
    /// the app's own session — reads and writes.
    public static let path = "/api/xbin/prefs/layout"
    /// Where builds before D117 looked: the `shell` bucket (a frame token
    /// for `shell`). Read only when `root` has none.
    public static let legacyComponent = "shell"
    /// The bucket the shell and the app share.
    public static let component = "root"
    /// Names who writes a pref (≤ 64 characters); xbind echoes it in the
    /// `prefs` event, so a client skips its own writes.
    public static let writerHeader = "X-Prefs-Writer"

    /// Whether a `prefs` event is another client's write of a pref Home
    /// shows (`layout`, `mobile-screens` in the shared bucket).
    public static func concernsHome(component: String, key: String, writer: String, me: String) -> Bool {
        (component == Self.component || component.isEmpty) && (key == "layout" || key == MobileScreens.key)
            && (writer.isEmpty || writer != me)
    }

    /// A write of `value` to pref `path`, naming its writer.
    public static func put(_ path: String, _ value: JSONValue, writer: String) -> APIRequest {
        var r = APIRequest.json("PUT", path, value)
        r.headers[writerHeader] = String(writer.prefix(64))
        return r
    }

    /// The web's default new-tile size (grid-layout.js `DEF_W`, `DEF_H`).
    public static let tileWidth = 576.0
    public static let tileHeight = 384.0

    /// A screen id as the web shell makes them (base-36, 7 characters).
    public static func newScreenID() -> String {
        let chars = Array("0123456789abcdefghijklmnopqrstuvwxyz")
        return String((0..<7).map { _ in chars[Int.random(in: 0..<chars.count)] })
    }

    /// "Screen N" after the screens there are, as the web's + does.
    public static func nextScreenName(_ layout: JSONValue?) -> String {
        "Screen \((layout?["screens"]?.arrayValue?.count ?? 0) + 1)"
    }

    /// `layout` with a new empty screen `{id, name, tiles:[]}` at the end.
    /// An empty layout is seeded first as the web shell would (a "Home"
    /// with `seed`, the workspace default's tiles), so the user keeps it.
    public static func addingScreen(id: String, name: String, to layout: JSONValue?, seed: JSONValue? = nil) -> JSONValue {
        var top = layout?.objectValue ?? [:]
        var screens = top["screens"]?.arrayValue ?? []
        if screens.isEmpty, let seed, let tiles = seed.arrayValue, !tiles.isEmpty {
            let home = newScreenID()
            screens.append(.object(["id": .string(home), "name": "Home", "tiles": .array(tiles)]))
            top["active"] = .string(home)
        }
        screens.append(.object(["id": .string(id), "name": .string(name), "tiles": []]))
        top["screens"] = .array(screens)
        if top["active"]?.stringValue == nil { top["active"] = .string(id) }
        return .object(top)
    }

    /// `layout` with `path` placed on screen `id` at a free spot; nil when
    /// the screen isn't there or already has the tile.
    public static func adding(tile path: String, toScreen id: String, in layout: JSONValue?) -> JSONValue? {
        guard var top = layout?.objectValue, var screens = top["screens"]?.arrayValue,
              let i = screens.firstIndex(where: { $0["id"]?.stringValue == id }),
              var screen = screens[i].objectValue else { return nil }
        var tiles = screen["tiles"]?.arrayValue ?? []
        guard !tiles.contains(where: { $0["path"]?.stringValue == path }) else { return nil }
        let spot = freeSpot(tiles)
        tiles.append(.object(["path": .string(path), "x": .double(spot.x), "y": .double(spot.y),
                              "w": .double(tileWidth), "h": .double(tileHeight)]))
        screen["tiles"] = .array(tiles)
        screens[i] = .object(screen)
        top["screens"] = .array(screens)
        return .object(top)
    }

    /// The web shell's `_freeSpot` on a two-column desktop: the first gap
    /// of a default tile's size, row by row, left to right, that overlaps
    /// no placed tile (floating ones don't count); else a new row below.
    public static func freeSpot(_ tiles: [JSONValue], columns: Int = 2) -> (x: Double, y: Double) {
        struct R { var x, y, w, h: Double }
        let placed: [R] = tiles.compactMap { t in
            if let f = t["float"], !f.isNull { return nil }
            return R(x: t["x"]?.doubleValue ?? 0, y: t["y"]?.doubleValue ?? 0,
                     w: t["w"]?.doubleValue ?? tileWidth, h: t["h"]?.doubleValue ?? tileHeight)
        }
        func overlaps(_ a: R, _ b: R) -> Bool { a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h }
        for row in 0..<100 {
            for c in 0..<max(1, columns) {
                let r = R(x: Double(c) * tileWidth, y: Double(row) * tileHeight, w: tileWidth, h: tileHeight)
                if !placed.contains(where: { overlaps(r, $0) }) { return (r.x, r.y) }
            }
        }
        let bottom = placed.map { $0.y + $0.h }.max() ?? 0
        return (0, (bottom / 48).rounded() * 48)
    }
}
