import Foundation
import Testing
@testable import XbinCore

/// Partitioned tiles in the app (D181): the `/components` row's
/// `partition` (old and new xbinds), the marker's and the paused tile's
/// rules, the partitions page's feature gate, its strict web ticket and the
/// settings entry, and the `partitions` event.
@Suite struct ClientPartitionsTests {
    // MARK: The catalog

    /// An xbind older than partitions: no row carries `partition`, nothing
    /// is partitioned or paused, and every tile opens as it did.
    @Test func oldCatalogHasNoPartitions() throws {
        let c = Catalog(json: try Resources.json("server/components.json"))
        #expect(!c.tiles.isEmpty)
        #expect(c.tiles.allSatisfy { $0.partition == nil && !$0.isPartitioned && !$0.isPaused })
        #expect(!c.hasPartitionedTile)
    }

    @Test func newCatalogDecodesPartition() throws {
        let c = Catalog(json: try Resources.json("server/components-partitions.json"))
        // Rows without `partition` decode as before; every row is kept.
        #expect(c.tiles.count == 8)
        #expect(c["apps/welcome"]?.partition == nil)
        #expect(c["root"]?.partition == nil)

        let notes = try #require(c["apps/notes"]?.partition)
        #expect(notes == TilePartition(state: "partitioned", user: true))
        #expect(c["apps/notes"]?.isPartitioned == true && c["apps/notes"]?.isPaused == false)
        #expect(notes.markTitle == "Partitioned: each person here has their own data")

        let agent = try #require(c["apps/agent"]?.partition)
        #expect(agent.user && agent.global && agent.request == nil)
        #expect(agent.markTitle == "Partitioned: each person here has their own data; one shared global instance also runs, for what isn't a person's")

        // Pending: the recorded mode (unpartitioned) keeps the marker off;
        // the request and the tile's note (trimmed) come along.
        let ledger = try #require(c["apps/ledger"])
        #expect(ledger.partition?.state == "pending")
        #expect(ledger.partition?.request == TilePartition.Request(user: true, global: false, declined: false))
        #expect(ledger.partition?.note == "Each person keeps their own ledger now.")
        #expect(ledger.isPaused && !ledger.isPartitioned)
        #expect(ledger.partition?.markTitle == "")

        // A request a manager declined: the tile runs in its recorded mode.
        let board = try #require(c["apps/board"])
        #expect(board.partition?.request?.declined == true)
        #expect(board.isPartitioned && !board.isPaused)

        // Invalid: neither marked nor paused (its manifest error says why).
        let broken = try #require(c["apps/broken"])
        #expect(broken.partition?.state == "invalid" && !broken.isPartitioned && !broken.isPaused)
        #expect(broken.manifestError.hasPrefix("partition:"))

        // A newer xbind's state word and fields: kept, read leniently.
        let future = try #require(c["apps/future"])
        #expect(future.partition?.state == "sharded" && future.isPartitioned && !future.isPaused)

        #expect(c.hasPartitionedTile)
        // Search, the navigator's listing: unchanged by the new field.
        #expect(c.listed.map(\.path).contains("apps/ledger"))
    }

    @Test func partitionIsLenient() {
        #expect(TilePartition(json: .null) == nil)
        #expect(TilePartition(json: "partitioned") == nil)
        #expect(TilePartition(json: true) == nil)
        #expect(TilePartition(json: [:]) == TilePartition(state: "", user: false))
        // `request: null` is no request; `pending` without one isn't paused.
        let p = TilePartition(json: ["state": "pending", "user": false, "global": false, "request": .null])
        #expect(p?.request == nil && p?.isPaused == false)
        // A row whose `partition` isn't an object: as if it had none.
        let row = TileInfo(json: ["path": "apps/x", "partition": "user"])
        #expect(row?.partition == nil && row?.isPartitioned == false)
        // Booleans stay booleans: 1 isn't true.
        #expect(TilePartition(json: ["state": "partitioned", "user": 1])?.isPartitioned == false)
    }

    /// The marker's words and colour are the web shell's (shell/
    /// partition-mode.js MARK_TITLE/MARK_GLOBAL, shell-css.js partCss).
    @Test func markerMatchesTheWebShell() {
        #expect(TilePartition.markText == "Partitioned: each person here has their own data")
        #expect(TilePartition.markGlobalText == "one shared global instance also runs, for what isn't a person's")
        #expect(TilePartition.markColorDark == 0x3FB5A3)
        #expect(TilePartition.markColorLight == 0x1F8778)
    }

    /// A paused tile opens its page — xbind's switch page — never its
    /// native view; the native view is back once the row says it runs.
    @Test func pausedTileOpensItsPage() {
        let paused = TilePartition(state: "pending", user: false, request: .init(user: true, global: false))
        var t = TileInfo(path: "apps/ledger", nativeEntry: "native.js", partition: paused)
        #expect(TileSurface.pick(t, serverRuntime: 1) == .web)
        t.partition = TilePartition(state: "partitioned", user: true)
        #expect(TileSurface.pick(t, serverRuntime: 1) == .native)
        t.partition = TilePartition(state: "partitioned", user: true, request: .init(user: false, global: false, declined: true))
        #expect(TileSurface.pick(t, serverRuntime: 1) == .native)
        t.partition = nil
        #expect(TileSurface.pick(t, serverRuntime: 1) == .native)
        #expect(TileSurface.pick(t, serverRuntime: nil) == .web)
    }

    // MARK: The partitions page

    @Test func pageNames() {
        #expect(XbindPage.partitions.path == "/xbin/partitions")
        #expect(XbindPage.partitions.link == "xbin/partitions")
        #expect(XbindPage.partitions.feature == "partitions-page/1")
        #expect(XbindPage.partitions.featuresRequest.method == "GET")
        #expect(XbindPage.partitions.featuresRequest.path == "/api/xbin/partitions")
        #expect(XbindPage.partitions.title == "Your partitions")
        #expect(XbindPage(linkName: "partitions") == .partitions)
        #expect(XbindPage(linkName: "Partitions") == nil)
        #expect(XbindPage(linkName: "consents") == nil)
    }

    /// The feature gate: the page is there only when `GET
    /// /api/xbin/partitions` lists `partitions-page/1`.
    @Test func featureGate() {
        let page = XbindPage.partitions
        let all: JSONValue = ["features": ["partitions/1", "mode-switch/1", "consents/1", "credential-confirm/1",
                                           "partition-mail/1", "partitions-page/1"], "tiles": []]
        #expect(XbindPageAvailability.from(json(200, all), page: page) == .served)
        // Partitions without the page (an xbind between the two).
        let before: JSONValue = ["features": ["partitions/1", "mode-switch/1"], "tiles": []]
        #expect(XbindPageAvailability.from(json(200, before), page: page) == .notServed)
        // An xbind without partitions answers 404.
        #expect(XbindPageAvailability.from(json(404, ["error": "no route"]), page: page) == .notServed)
        // Not a feature list, or a feature only named alike.
        #expect(XbindPageAvailability.from(json(200, ["features": "partitions-page/1"]), page: page) == .notServed)
        #expect(XbindPageAvailability.from(json(200, ["features": ["partitions-page/2", "partitions-page"]]), page: page) == .notServed)
        #expect(XbindPageAvailability.from(APIResponse(status: 200, body: Data("<html>".utf8)), page: page) == .notServed)
        // Can't tell: ask again later, never guess.
        #expect(XbindPageAvailability.from(nil, page: page) == .unknown)
        #expect(XbindPageAvailability.from(json(401, ["error": "signed out"]), page: page) == .unknown)
        #expect(XbindPageAvailability.from(json(503, ["error": "busy"]), page: page) == .unknown)
    }

    /// The page opens signed in through its one-shot ticket, or not at all:
    /// never the plain page (a password form in the app's web view).
    @Test func ticketIsStrict() throws {
        let origin = try ServerOrigin(string: "https://ws.example.com")
        let page = XbindPage.partitions
        #expect(body(WebTicket.request(next: page.path)) == ["next": "/xbin/partitions"])
        let ok = json(200, ["url": "https://ws.example.com/login?ticket=T1&next=%2Fxbin%2Fpartitions", "expiresIn": 120])
        #expect(XbindPageTicket.outcome(ok, origin: origin, page: page)
            == .open(URL(string: "https://ws.example.com/login?ticket=T1&next=%2Fxbin%2Fpartitions")!))
        // Through the enrollment origin (the phone's own address for it).
        let tunnel = try ServerOrigin(string: "http://127.0.0.1:9871")
        #expect(XbindPageTicket.outcome(ok, origin: tunnel, signedOrigin: "https://ws.example.com", page: page)
            == .open(URL(string: "http://127.0.0.1:9871/login?ticket=T1&next=%2Fxbin%2Fpartitions")!))
        // Refused, failed, elsewhere, or unanswered: an error, no page.
        guard case .failed(let refused) = XbindPageTicket.outcome(json(403, ["error": "a token session has no web ticket"]),
                                                                  origin: origin, page: page) else {
            Issue.record("a refused ticket opened the page")
            return
        }
        #expect(refused.contains("a token session has no web ticket"))
        for r in [json(404, ["error": "no route"]), json(500, ["error": "x"]),
                  json(200, ["url": "https://evil.example.com/login?ticket=T"]), json(200, ["nope": true])] {
            guard case .failed = XbindPageTicket.outcome(r, origin: origin, page: page) else {
                Issue.record("opened the page on \(r.status)")
                continue
            }
        }
        guard case .failed = XbindPageTicket.outcome(nil, origin: origin, page: page) else {
            Issue.record("opened the page without an answer")
            return
        }
    }

    /// The settings entry (I13): the page served, a signed-in person, a
    /// partitioned tile in sight — the web shell's pageEntry.
    @Test func settingsEntry() throws {
        let withPart = Catalog(json: try Resources.json("server/components-partitions.json"))
        let without = Catalog(json: try Resources.json("server/components.json"))
        let pendingOnly = Catalog(tiles: [TileInfo(path: "apps/x", partition: TilePartition(
            state: "pending", user: false, request: .init(user: true, global: false)))])
        let person = Whoami(json: ["kind": "user", "id": "ana", "role": "user"])
        let viewAs = Whoami(json: ["kind": "user", "id": "ana", "impersonatedBy": "owner", "readOnly": true])
        let root = Whoami(json: ["kind": "root", "id": ""])
        #expect(PartitionsEntry.shown(availability: .served, catalog: withPart, whoami: person))
        #expect(!PartitionsEntry.shown(availability: .notServed, catalog: withPart, whoami: person))
        #expect(!PartitionsEntry.shown(availability: .unknown, catalog: withPart, whoami: person))
        #expect(!PartitionsEntry.shown(availability: .served, catalog: without, whoami: person))
        #expect(!PartitionsEntry.shown(availability: .served, catalog: pendingOnly, whoami: person))
        #expect(!PartitionsEntry.shown(availability: .served, catalog: withPart, whoami: viewAs))
        #expect(!PartitionsEntry.shown(availability: .served, catalog: withPart, whoami: root))
        #expect(!PartitionsEntry.shown(availability: .served, catalog: withPart, whoami: nil))
        // Probing: only where the entry could show — an older xbind's rows
        // never carry `partition`, so it is never asked.
        #expect(PartitionsEntry.worthProbing(catalog: withPart, whoami: person))
        #expect(!PartitionsEntry.worthProbing(catalog: without, whoami: person))
        #expect(!PartitionsEntry.worthProbing(catalog: withPart, whoami: root))
    }

    // MARK: Events

    @Test func partitionsEvents() {
        let mode = #"{"type":"partitions","component":"apps/x","data":{"op":"mode","tile":"apps/x","state":"pending","spec":{"user":false,"global":false},"request":{"spec":{"user":true,"global":true},"since":"2026-10-01T10:00:00Z","declined":false}}}"#
        #expect(AppEvent.parse(mode) == .partitions(component: "apps/x", op: "mode"))
        let notice = #"{"type":"partitions","component":"apps/x","data":{"op":"notice","notice":{"id":"n1","kind":"credential","text":"…"}}}"#
        #expect(AppEvent.parse(notice) == .partitions(component: "apps/x", op: "notice"))
        #expect(AppEvent.parse(#"{"type":"partitions","component":"apps/x"}"#) == .other(type: "partitions"))
        #expect(AppEvent.parse(#"{"type":"partitions","data":{"op":""}}"#) == .other(type: "partitions"))
    }
}
