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
        #expect(AppEvent.parse(#"{"type":"bus","topic":"res:a/b/c","data":1}"#) == .other(type: "bus"))
        #expect(AppEvent.parse(#"{"type":"status","component":"apps/x","data":{"level":"error","message":"down","ts":1}}"#)
            == .tileStatus(component: "apps/x", level: "error", message: "down"))
        #expect(AppEvent.parse(#"{"type":"pr","component":"apps/x"}"#) == .other(type: "pr"))
        #expect(AppEvent.parse(#"{"type":"something-new","x":[1,2]}"#) == .other(type: "something-new"))
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
