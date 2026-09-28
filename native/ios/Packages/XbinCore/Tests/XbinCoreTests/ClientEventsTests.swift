import Foundation
import Testing
@testable import XbinCore

@Suite struct ClientEventsTests {
    /// Every frame shape docs/protocol.md lists for `/ws/events`.
    @Test func parsesTheDocumentedFrames() {
        #expect(AppEvent.parse(#"{"type":"reload","component":"apps/thing"}"#) == .reload(component: "apps/thing"))
        #expect(AppEvent.parse(#"{"type":"build-start","component":"apps/thing"}"#)
            == .build(component: "apps/thing", phase: .start, text: ""))
        #expect(AppEvent.parse(#"{"type":"build-error","component":"apps/thing","text":"x.go:1: boom"}"#)
            == .build(component: "apps/thing", phase: .error, text: "x.go:1: boom"))
        #expect(AppEvent.parse(#"{"type":"build-ok","component":"apps/thing"}"#)
            == .build(component: "apps/thing", phase: .ok, text: ""))
        #expect(AppEvent.parse(#"{"type":"grants"}"#) == .grants(component: nil))
        #expect(AppEvent.parse(#"{"type":"grants","component":"apps/x"}"#) == .grants(component: "apps/x"))
        #expect(AppEvent.parse(#"{"type":"branding"}"#) == .branding)
        // The workspace's native-runtime switch (an admin's PUT
        // /api/xbin/native-runtime): re-read whoami.
        #expect(AppEvent.parse(#"{"type":"native"}"#) == .nativeSwitch)
        #expect(AppEvent.parse("{\"type\":\"native\"}\n") == .nativeSwitch)
        #expect(AppEvent.parse(#"{"type":"bus","topic":"res:a/b/c","data":1}"#) == .other(type: "bus"))
        #expect(AppEvent.parse(#"{"type":"status","component":"apps/x","data":{"level":"error","message":"down","ts":1}}"#)
            == .tileStatus(component: "apps/x", level: "error", message: "down"))
        #expect(AppEvent.parse(#"{"type":"pr","component":"apps/x"}"#) == .other(type: "pr"))
        #expect(AppEvent.parse(#"{"type":"something-new","x":[1,2]}"#) == .other(type: "something-new"))
        // As xbind writes them (encoding/json: a newline after each frame).
        #expect(AppEvent.parse("{\"type\":\"reload\",\"component\":\"apps/welcome\"}\n") == .reload(component: "apps/welcome"))
    }

    @Test func refusesJunk() {
        #expect(AppEvent.parse("") == nil)
        #expect(AppEvent.parse("not json") == nil)
        #expect(AppEvent.parse("[1,2]") == nil)
        #expect(AppEvent.parse(#"{"component":"apps/x"}"#) == nil)
        #expect(AppEvent.parse(#"{"type":7}"#) == nil)
        // A reload that names nothing reloads nothing.
        #expect(AppEvent.parse(#"{"type":"reload"}"#) == .other(type: "reload"))
    }

    @Test func termEvents() {
        let open = AppEvent.parse(#"{"type":"term","component":"apps/t","data":{"op":"open","id":"s1","user":"alice"}}"#)
        #expect(open == .term(TermEvent(component: "apps/t", op: .open, id: "s1", user: "alice")))
        if case .term(let t)? = open { #expect(t.needsRelist) }
        for op in ["close", "rename"] {
            let e = AppEvent.parse(#"{"type":"term","component":"apps/t","data":{"op":"\#(op)","id":"s1"}}"#)
            if case .term(let t)? = e { #expect(t.needsRelist && t.op.rawValue == op) } else { Issue.record("\(op): \(String(describing: e))") }
        }
        let status = AppEvent.parse(#"""
        {"type":"term","component":"apps/t","data":{"op":"status","id":"s2","user":"alice",
         "status":"waiting_permission","pending":0,"questions":1,"turn":3}}
        """#)
        #expect(status == .term(TermEvent(component: "apps/t", op: .status, id: "s2", user: "alice",
                                          status: "waiting_permission", pending: 0, questions: 1, turn: 3)))
        if case .term(let t)? = status { #expect(!t.needsRelist) }
        // Fields of a status on another op are not read.
        let odd = AppEvent.parse(#"{"type":"term","data":{"op":"open","id":"s","status":"running","pending":2}}"#)
        #expect(odd == .term(TermEvent(component: "", op: .open, id: "s")))
        // Unknown op, missing id: ignored.
        #expect(AppEvent.parse(#"{"type":"term","data":{"op":"teleport","id":"s"}}"#) == .other(type: "term"))
        #expect(AppEvent.parse(#"{"type":"term","data":{"op":"open"}}"#) == .other(type: "term"))
        #expect(AppEvent.parse(#"{"type":"term"}"#) == .other(type: "term"))
    }

    /// Admins get every user's term events; the app follows only its own.
    @Test func termEventsForThisUser() {
        #expect(TermEvent.homeKey(userID: "alice") == "alice")
        #expect(TermEvent.homeKey(userID: "a.b-c_d9") == "a.b-c_d9")
        #expect(TermEvent.homeKey(userID: "ann@example.com") == "ann_example.com")
        #expect(TermEvent.homeKey(userID: "zoë/x") == "zo__x")
        #expect(TermEvent.homeKey(userID: "") == "owner")
        #expect(TermEvent.homeKey(userID: "..") == "owner")
        let e = TermEvent(component: "x", op: .status, id: "s", user: "ann_example.com")
        #expect(e.isFor(userID: "ann@example.com") && !e.isFor(userID: "bob"))
        #expect(TermEvent(component: "x", op: .open, id: "s").isFor(userID: "anyone"))
        #expect(TermEvent(component: "x", op: .open, id: "s", user: "owner").isFor(userID: ""))
    }

    @Test func sessionFramesKeepTheirText() {
        let f = #"{"type":"session","topic":"session.abc","component":"apps/t","data":{"seq":7,"ts":1,"type":"message.delta","data":{"role":"agent","text":"hi"},"user":"alice","id":"abc"}}"#
        #expect(AppEvent.parse(f) == .session(id: "abc", component: "apps/t", frame: f))
        // The id from the topic when data has none.
        let t = #"{"type":"session","topic":"session.xyz","data":{"seq":1,"type":"status","data":{}}}"#
        #expect(AppEvent.parse(t) == .session(id: "xyz", component: "", frame: t))
        #expect(AppEvent.parse(#"{"type":"session","topic":"other","data":{}}"#) == .other(type: "session"))
    }

    /// The web's longest-prefix rule (web/events-socket.js isReloadTarget).
    @Test func reloadTargetsTheMostSpecificOpenTile() {
        let open = ["apps/calendar", "apps/calendar/widgets", "apps/cal", "notes"]
        #expect(ReloadTargets.target(for: "apps/calendar", open: open) == "apps/calendar")
        #expect(ReloadTargets.target(for: "apps/calendar/widgets/x", open: open) == "apps/calendar/widgets")
        #expect(ReloadTargets.target(for: "apps/calendar/backend", open: open) == "apps/calendar")
        #expect(ReloadTargets.target(for: "apps/calendarx", open: open) == nil)
        #expect(ReloadTargets.target(for: "apps/cal", open: open) == "apps/cal")
        #expect(ReloadTargets.target(for: "apps", open: open) == nil)
        #expect(ReloadTargets.target(for: "notes/", open: open) == "notes")
        #expect(ReloadTargets.target(for: "notes", open: ["/notes/"]) == "/notes/")
        #expect(ReloadTargets.target(for: "notes", open: []) == nil)
        #expect(!ReloadTargets.covers("", "anything"))
        #expect(ReloadTargets.covers("a/b", "a/b/c/d"))
    }

    // MARK: Old apps and tile deployments

    /// covers D127h PO-5 — why xbind never sends a non-primary deployment's
    /// activity as `reload` (the server's rule: old event types carry only
    /// the primary, with the bare component). `ReloadTargets.covers` as it
    /// ships: `apps/x+dev` isn't covered by `apps/x` (`+` is no path
    /// separator), but `apps/a/b+dev` **is** covered by `apps/a`, so a
    /// shipped app would reload an open `apps/a` for it, even with `apps/a/b`
    /// itself open.
    @Test func aQualifiedComponentReloadsAnOpenAncestor() {
        #expect(!ReloadTargets.covers("apps/x", "apps/x+dev"))
        #expect(ReloadTargets.target(for: "apps/x+dev", open: ["apps/x"]) == nil)
        #expect(ReloadTargets.covers("apps/a", "apps/a/b+dev"))
        #expect(!ReloadTargets.covers("apps/a/b", "apps/a/b+dev"))
        #expect(ReloadTargets.target(for: "apps/a/b+dev", open: ["apps/a", "apps/a/b"]) == "apps/a")
        #expect(ReloadTargets.target(for: "apps/a/b+dev/widget", open: ["apps/a/b", "apps/a"]) == "apps/a")
        #expect(ReloadTargets.target(for: "/apps/a/b+dev/", open: ["apps/a/b"]) == nil)
        #expect(ReloadTargets.target(for: "apps/a+dev", open: ["apps"]) == "apps")
        // The bare component: today's, the tile's own view.
        #expect(ReloadTargets.target(for: "apps/a/b", open: ["apps/a", "apps/a/b"]) == "apps/a/b")
        // As the app routes a frame (WorkspaceEvents.receive): parse, then target.
        if case .reload(let c)? = AppEvent.parse(#"{"type":"reload","component":"apps/a/b+dev"}"#) {
            #expect(ReloadTargets.target(for: c, open: ["apps/a", "apps/a/b"]) == "apps/a")
        } else {
            Issue.record("a reload frame didn't parse as .reload")
        }
    }

    /// covers D127h PO-5 — the `deployments` type in every documented form
    /// (full and reader forms, each op, naming `dev` and `main`) parses to
    /// `.deployments` — the sessions screen's state follows it (D132) — and
    /// never to a reload, a build or a status, so none reaches reload
    /// targeting; a `bus` event of a non-main namespace stays
    /// `.other(type: "bus")`.
    @Test func deploymentsEventsNeverReload() {
        let data = [
            #"{"op":"record","seq":19,"by":"user:ana","session":"s1","what":["liveReload","primary","protectedPrimary","edges","deployments","deliveries","alwaysOn","limits"]}"#,
            #"{"op":"deploy","id":43,"deployment":"main","how":"promote","from":"dev","checkpoint":"c:3f2a1c9","result":"running","phase":"build","by":"user:ana","session":"s1"}"#,
            #"{"op":"deploy","id":44,"deployment":"dev","how":"deploy","checkpoint":"c:3f2a1c9","result":"failed","phase":"start","by":"user:ana"}"#,
            #"{"op":"reload","deployment":"dev"}"#,
            #"{"op":"build","deployment":"dev","phase":"start"}"#,
            #"{"op":"build","deployment":"dev","phase":"error","text":"compiler output"}"#,
            #"{"op":"build","deployment":"dev","phase":"ok"}"#,
            #"{"op":"work-tree","changed":3}"#,
            #"{"op":"data","deployment":"dev","busy":"","state":"seeded"}"#,
            #"{"op":"status","deployment":"dev","level":"error","message":"down","ts":1790000000,"transient":false}"#,
            #"{"op":"notify","deployment":"dev","to":"user:bob","title":"t","at":"2026-09-27T00:00:00Z"}"#,
            #"{"op":"record","what":["liveReload"]}"#,
            #"{"op":"deploy","deployment":"main","checkpoint":"c:3f2a1c9","result":"ok","phase":"swap","by":"user:ana"}"#,
        ]
        for d in data {
            for component in ["apps/crm", "apps/crm/widgets"] {
                let f = #"{"type":"deployments","component":"\#(component)","data":\#(d)}"#
                guard case .deployments(let e)? = AppEvent.parse(f) else {
                    Issue.record("not a deployments event: \(f)")
                    continue
                }
                #expect(e.component == component && e.op == (try? JSONValue(parsing: d))?["op"]?.stringValue, "\(f)")
            }
        }
        #expect(AppEvent.parse(#"{"type":"bus","topic":"res:apps/crm/events/orders","deployment":"dev","data":1}"#) == .other(type: "bus"))
        // The facts the sessions screen reads.
        #expect(AppEvent.parse(#"{"type":"deployments","component":"apps/crm","data":{"op":"work-tree","changed":3}}"#)
                == .deployments(DeploymentsEvent(component: "apps/crm", op: "work-tree", changed: 3)))
        #expect(AppEvent.parse(#"{"type":"deployments","component":"apps/crm","data":{"op":"branch","deployment":"dev","assigned":"feature","workTree":"main","related":"","paused":true}}"#)
                == .deployments(DeploymentsEvent(component: "apps/crm", op: "branch", deployment: "dev", assigned: "feature", workTree: "main", paused: true)))
        // No component, or no op: not one the app can use.
        #expect(AppEvent.parse(#"{"type":"deployments","data":{"op":"record"}}"#) == .other(type: "deployments"))
        #expect(AppEvent.parse(#"{"type":"deployments","component":"apps/crm","data":{}}"#) == .other(type: "deployments"))
    }

    /// covers D127h PO-5 — TestFailedDeployInvisible's tape
    /// (test/deployments_test.go): go, node and python tiles written, their
    /// live reload paused, then a broken build, a crash at start and a health
    /// timeout each shipped by reload now, a deploy and a rollback, every one
    /// failing, with a tile-report on the primary between. Through the app's
    /// parser and reload targeting: no frame the app acts on as `reload`,
    /// `build` or `status` names a non-primary deployment, so an open
    /// ancestor's prefix match never fires on one; the only reloads are the
    /// three first writes', each on its own tile's view; after a tile's pause
    /// nothing reloads it or reports a build of it.
    @Test func failedDeployTapeReplays() throws {
        let tape = try Self.replay("the recorded tape", Self.failedDeployTape.split(separator: "\n").map(String.init))
        #expect(tape.count == 113)
        let reloads = tape.compactMap { f -> String? in
            if case .reload(let c) = f.event { return c }
            return nil
        }
        #expect(reloads.sorted() == ["apps/fd-go", "apps/fd-node", "apps/fd-python"])
        for tile in ["apps/fd-go", "apps/fd-node", "apps/fd-python"] {
            guard let paused = tape.firstIndex(where: { $0.type == "deployments" && $0.component == tile }) else {
                Issue.record("\(tile) never paused")
                continue
            }
            var failed = Set<Int64>()
            for f in tape[paused...] where f.component == tile {
                switch f.event {
                case .reload, .build:
                    Issue.record("\(tile): \(f.event) after the pause")
                case .tileStatus(_, _, let message, _):
                    #expect(message == "watching the deploys", "\(tile): the primary's own report only")
                default:
                    if f.json["data"]?["op"]?.stringValue == "deploy", f.json["data"]?["result"]?.stringValue == "failed",
                       let id = f.json["data"]?["id"]?.intValue {
                        failed.insert(id)
                    }
                }
            }
            // go's broken build is shipped once more, by bx live-reload now.
            #expect(failed.count == (tile == "apps/fd-go" ? 10 : 9), "\(tile): \(failed.sorted())")
        }
    }

    /// covers D127h PO-5 — a fresh tape: with XBIN_DEPLOY_TAPE naming the file
    /// an integration run of TestFailedDeployInvisible wrote, it replays the
    /// same way.
    @Test(.enabled(if: ProcessInfo.processInfo.environment["XBIN_DEPLOY_TAPE"] != nil, "XBIN_DEPLOY_TAPE is not set"))
    func freshFailedDeployTapeReplays() throws {
        let path = ProcessInfo.processInfo.environment["XBIN_DEPLOY_TAPE"] ?? ""
        let text = try String(contentsOfFile: path, encoding: .utf8)
        let tape = try Self.replay(path, text.split(separator: "\n").map(String.init).filter { !$0.allSatisfy(\.isWhitespace) })
        #expect(!tape.isEmpty)
    }

    @Test func eventsURL() throws {
        #expect(EventsRoute.url(on: try ServerOrigin(string: "https://ws.example.com"))?.absoluteString == "wss://ws.example.com/ws/events")
        #expect(EventsRoute.url(on: try ServerOrigin(string: "http://127.0.0.1:9461"))?.absoluteString == "ws://127.0.0.1:9461/ws/events")
    }

    // MARK: The reconnect policy

    @Test func connectsOnlyWhileWanted() {
        var p = EventSocketPolicy()
        #expect(p.retryDue() == .none)
        #expect(p.setWanted(true) == .connect)
        #expect(p.setWanted(true) == .none)          // already connecting
        #expect(p.opened() == false)                 // the first socket: nothing missed
        #expect(p.connected)
        #expect(p.setWanted(false) == .disconnect)
        #expect(p.closed(.dropped) == .none)         // our own close: no retry
        #expect(p.setWanted(false) == .none)
        #expect(p.setWanted(true) == .connect)
        #expect(p.opened() == true)                  // a reopen: re-list, catch up
    }

    @Test func backsOffDoublingToFifteenSecondsAndResetsOnOpen() {
        var p = EventSocketPolicy()
        _ = p.setWanted(true)
        var delays: [Double] = []
        for _ in 0..<7 {
            guard case .retry(let d) = p.closed(.unreachable) else { Issue.record("no retry"); return }
            delays.append(d)
            #expect(p.retryDue() == .connect)
        }
        #expect(delays == [0.5, 1, 2, 4, 8, 15, 15])
        _ = p.opened()
        #expect(p.closed(.dropped) == .retry(after: 0.5))
        #expect(p.retryDue() == .connect)
        #expect(p.retryDue() == .none)               // already connecting
        // Jitter scales the delay only.
        #expect(p.closed(.refused(status: 502), jitter: 1.2) == .retry(after: 1.2))
        #expect(p.backoff == 2)
    }

    @Test func reauthenticatesOnceOn401() {
        var p = EventSocketPolicy()
        _ = p.setWanted(true)
        #expect(p.closed(.refused(status: 401)) == .reauthenticate)
        #expect(p.retryDue() == .connect)            // after the re-sign
        #expect(p.closed(.refused(status: 401)) == .none)
        #expect(p.parked)
        #expect(p.retryDue() == .none)
        // The next foreground tries again, from scratch.
        _ = p.setWanted(false)
        #expect(p.setWanted(true) == .connect)
        #expect(p.closed(.refused(status: 401)) == .reauthenticate)
        #expect(p.retryDue() == .connect)
        _ = p.opened()                               // a working socket resets the allowance
        #expect(p.closed(.refused(status: 401)) == .reauthenticate)
        #expect(p.reauthFailed() == .none && p.parked)
        // No session at all while connecting: parked, and the next ask tries again.
        var q = EventSocketPolicy()
        #expect(q.setWanted(true) == .connect)
        #expect(q.reauthFailed() == .none && q.parked && !q.connecting)
        #expect(q.retryDue() == .none)
        #expect(q.setWanted(true) == .connect)
    }

    @Test func parksOnForbiddenAndNotFound() {
        for status in [403, 404] {
            var p = EventSocketPolicy()
            _ = p.setWanted(true)
            #expect(p.closed(.refused(status: status)) == .none)
            #expect(p.parked && p.retryDue() == .none)
            // Asked again while still wanted (a window came forward): one more try.
            #expect(p.setWanted(true) == .connect)
        }
    }
}

// MARK: - Event tapes

extension ClientEventsTests {
    /// One replayed frame: what the app parsed, and the frame itself.
    struct TapeFrame {
        var event: AppEvent
        var json: JSONValue
        var type: String { json["type"]?.stringValue ?? "" }
        var component: String { json["component"]?.stringValue ?? "" }
    }

    /// Replays an event tape (one JSON frame per line) through the app's
    /// parser and reload targeting (WorkspaceEvents.receive): every
    /// component is a bare tile path; no frame the app acts on as `reload`,
    /// `build` or `status` carries a `deployment`, at the top or in `data`;
    /// `deployments` frames are `.deployments`; and every reload lands on its own
    /// tile's view with the tiles and a common ancestor open, or on the
    /// ancestor alone, as today.
    static func replay(_ name: String, _ lines: [String]) throws -> [TapeFrame] {
        var out: [TapeFrame] = []
        for (i, line) in lines.enumerated() {
            let at = "\(name):\(i + 1)"
            let json = try JSONValue(parsing: line)
            guard let event = AppEvent.parse(line) else {
                Issue.record("\(at) doesn't parse: \(line)")
                continue
            }
            let f = TapeFrame(event: event, json: json)
            #expect(!f.component.contains("+"), "\(at): a qualified component: \(line)")
            switch event {
            case .reload, .build, .tileStatus:
                #expect(json["deployment"] == nil && json["data"]?["deployment"] == nil, "\(at) names a deployment: \(line)")
            default:
                break
            }
            if f.type == "deployments" {
                if case .deployments = event {} else { Issue.record("\(at): \(event)") }
            }
            out.append(f)
        }
        let tiles = Set(out.map(\.component).filter { !$0.isEmpty }).sorted()
        let tops = Set(tiles.map { String($0.prefix { $0 != "/" }) })
        guard tops.count == 1, let ancestor = tops.first, tiles.allSatisfy({ $0.contains("/") }) else {
            Issue.record("\(name): no common ancestor of \(tiles)")
            return out
        }
        for open in [[ancestor] + tiles, [ancestor]] {
            for f in out {
                guard case .reload(let c) = f.event else { continue }
                #expect(ReloadTargets.target(for: c, open: open) == (open.contains(c) ? c : ancestor), "\(name): a reload of \(c) with \(open) open")
            }
        }
        return out
    }

    /// TestFailedDeployInvisible's tape, verbatim (blank lines dropped):
    /// recorded with `XBIN_DEPLOY_TAPE=<file> go test -tags=integration -run
    /// 'TestFailedDeployInvisible$' ./test/` on an isolated daemon, 2026-09-27.
    static let failedDeployTape = #"""
    {"type":"reload","component":"apps/fd-node"}
    {"type":"reload","component":"apps/fd-python"}
    {"type":"reload","component":"apps/fd-go"}
    {"type":"build-start","component":"apps/fd-node"}
    {"type":"build-start","component":"apps/fd-go"}
    {"type":"build-start","component":"apps/fd-python"}
    {"type":"build-ok","component":"apps/fd-node"}
    {"type":"build-ok","component":"apps/fd-python"}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"ok","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"ok","phase":"swap","by":"owner"}}
    {"type":"status","component":"apps/fd-python","data":{"level":"warn","message":"watching the deploys","ts":1790538841}}
    {"type":"status","component":"apps/fd-node","data":{"level":"warn","message":"watching the deploys","ts":1790538841}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:1201301","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:1201301","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:2a4908a","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:2a4908a","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"work-tree","changed":2}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"work-tree","changed":2}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:2a4908a","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:1201301","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:1201301","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:1201301","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:2a4908a","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:2a4908a","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:2a4908a","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:1201301","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:1201301","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:1201301","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:2a4908a","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:2a4908a","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:1201301","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:2a4908a","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:6d84418","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:6d84418","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:9e50223","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:9e50223","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:9e50223","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:6d84418","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:6d84418","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:6d84418","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:9e50223","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:9e50223","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:9e50223","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:6d84418","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:6d84418","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:6d84418","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:9e50223","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:9e50223","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:9e50223","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:6d84418","result":"failed","phase":"start","by":"owner"}}
    {"type":"build-ok","component":"apps/fd-go"}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:8ae17df","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:8ae17df","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:04d2d26","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:04d2d26","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"swap","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"ok","phase":"swap","by":"owner"}}
    {"type":"status","component":"apps/fd-go","data":{"level":"warn","message":"watching the deploys","ts":1790538845}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"work-tree","changed":2}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":6,"deployment":"main","how":"reload-now","checkpoint":"c:8104834","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":6,"deployment":"main","how":"reload-now","checkpoint":"c:8104834","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":6,"deployment":"main","how":"reload-now","checkpoint":"c:8104834","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":7,"deployment":"main","how":"deploy","checkpoint":"c:8104834","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":7,"deployment":"main","how":"deploy","checkpoint":"c:8104834","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":7,"deployment":"main","how":"deploy","checkpoint":"c:8104834","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":8,"deployment":"main","how":"rollback","checkpoint":"c:8104834","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":8,"deployment":"main","how":"rollback","checkpoint":"c:8104834","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":8,"deployment":"main","how":"rollback","checkpoint":"c:8104834","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:04d2d26","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:04d2d26","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:04d2d26","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":9,"deployment":"main","how":"reload-now","checkpoint":"c:1e83bee","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":9,"deployment":"main","how":"reload-now","checkpoint":"c:1e83bee","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:8ae17df","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:8ae17df","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:8ae17df","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:04d2d26","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:04d2d26","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:04d2d26","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":9,"deployment":"main","how":"reload-now","checkpoint":"c:1e83bee","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":10,"deployment":"main","how":"deploy","checkpoint":"c:1e83bee","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":10,"deployment":"main","how":"deploy","checkpoint":"c:1e83bee","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:8ae17df","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:8ae17df","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:8ae17df","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:04d2d26","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":10,"deployment":"main","how":"deploy","checkpoint":"c:1e83bee","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":11,"deployment":"main","how":"rollback","checkpoint":"c:1e83bee","result":"running","phase":"build","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":11,"deployment":"main","how":"rollback","checkpoint":"c:1e83bee","result":"running","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":11,"deployment":"main","how":"rollback","checkpoint":"c:1e83bee","result":"failed","phase":"start","by":"owner"}}
    {"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:8ae17df","result":"failed","phase":"start","by":"owner"}}
    """#
}
