import Foundation
import Testing
@testable import XbinAgent

// The windowed log (D130, E3): pages folded one by one give the whole log's
// items and ids; segments unload and come back with the same ids; the live
// tail detaches and counts what it missed; the feed opens on the tail,
// loads older pages, re-reads the tail on a resume's `replayed`, and
// publishes nothing for an event it already had.

/// A long session: `turns` turns of a prompt, a status, a thought, a
/// two-delta message, a tool call (the same agent id every turn) with
/// output and completion, a closing message, the turn's end and an idle.
func longLog(turns: Int) -> [AgentEvent] {
    var out: [AgentEvent] = []
    var seq: UInt64 = 0
    func add(_ type: String, _ data: String) {
        seq += 1
        out.append(ev(seq, type, j(data)))
    }
    add("status", #"{"status":"idle","agent":{"name":"Fake","version":"1"},"commands":[{"name":"review"}]}"#)
    for k in 1...turns {
        add("message.delta", #"{"role":"user","text":"do \#(k)"}"#)
        add("status", #"{"status":"running"}"#)
        add("thought.delta", #"{"text":"thinking \#(k)"}"#)
        add("message.delta", #"{"role":"agent","text":"part 1 ","messageId":"a\#(k)"}"#)
        add("message.delta", #"{"role":"agent","text":"part 2","messageId":"a\#(k)"}"#)
        add("tool.call", #"{"id":"t","title":"Edit f\#(k).go","kind":"edit","status":"in_progress"}"#)
        add("tool.update", #"{"id":"t","outputDelta":"out \#(k)\n"}"#)
        add("tool.update", #"{"id":"t","status":"completed"}"#)
        add("message.delta", #"{"role":"agent","text":"done \#(k)","messageId":"b\#(k)"}"#)
        add("turn.end", #"{"turn":\#(k),"stopReason":"end_turn","usage":{"used":\#(k * 100),"size":1000}}"#)
        add("status", #"{"status":"idle"}"#)
    }
    return out
}

/// A paging xbind over a log: pages start only at a prompt or a tool call
/// (where nothing before is still open), each with its state header.
struct Pager: Sendable {
    var events: [AgentEvent]
    /// Waiting requests to report on the tail page.
    var waiting: [String] = []

    var cuts: [UInt64] {
        events.filter { e in
            (e.type == "message.delta" && e.data["role"]?.string == "user") || e.type == "tool.call"
        }.map(\.seq)
    }

    func page(before: UInt64 = 0, limit: Int) -> JSONValue {
        let upTo = before > 0 ? before : (events.last?.seq ?? 0) + 1
        let first = events.first?.seq ?? 1
        var cut = first
        for c in cuts.reversed() where c < upTo {
            if events.filter({ $0.seq >= c && $0.seq < upTo }).count >= limit { cut = c; break }
        }
        let evs = events.filter { $0.seq >= cut && $0.seq < upTo }
        var st = JSONObject()
        var status = JSONObject()
        var turn = 0
        var usage: JSONValue?
        for e in events where e.seq < cut {
            if e.type == "status", let o = e.data.object { for (k, v) in o { status[k] = v } }
            if e.type == "turn.end" { turn = e.data["turn"]?.int ?? turn; usage = e.data["usage"] }
        }
        if !status.isEmpty { st["status"] = .object(status) }
        if let usage { st["usage"] = usage }
        st["turn"] = .number(Double(turn))
        if before == 0, !waiting.isEmpty { st["permissions"] = .array(waiting.map { ["pid": .string($0)] }) }
        return ["events": .array(evs.map(\.json)), "hasOlder": .bool(cut > first), "nextBefore": .number(Double(cut > first ? cut : 0)),
                "truncated": false, "next": .number(Double(evs.last?.seq ?? 0)), "last": .number(Double(events.last?.seq ?? 0)),
                "state": .object(st)]
    }

    func eventsPage(before: UInt64 = 0, limit: Int) -> EventsPage { EventsPage(json: page(before: before, limit: limit))! }
}

@Suite struct WindowTests {
    /// The tail, then every older page: the whole log's items, ids included
    /// (the tool's id is the seq that opened it, not a per-fold count).
    @Test func pagesFoldToTheWholeLog() throws {
        let all = longLog(turns: 30)
        let pager = Pager(events: all)
        let whole = AgentTranscript(events: all)
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: 25))
        #expect(w.paged && w.hasOlder && w.segmentCount == 1)
        #expect(w.lastSeq == all.last!.seq)
        #expect(w.state.status == .idle && w.state.agent?.name == "Fake" && w.state.lastTurn == 30)
        var n = 0
        while w.hasOlder {
            let before = w.firstSeq
            let okprepend = w.prepend(pager.eventsPage(before: before, limit: 25), before: before)
            #expect(okprepend)
            n += 1
        }
        #expect(n > 5)
        #expect(w.items.map(\.id) == whole.items.map(\.id))
        #expect(w.items == whole.items)
        // a page asked for before the window moved does not land
        var x = AgentWindow()
        x.open(tail: pager.eventsPage(limit: 25))
        let okprepend = x.prepend(pager.eventsPage(before: 5, limit: 25), before: 5)
        #expect(!okprepend)
    }

    /// A page cut between a message and a tool call: the older page's last
    /// message is not "writing" any more (sealed), as in the whole log.
    @Test func olderPagesAreSealed() throws {
        let all = longLog(turns: 3)
        let pager = Pager(events: all)
        let cut = all.first { $0.type == "tool.call" && $0.seq > 20 }!.seq
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: all.count(where: { $0.seq >= cut })))
        #expect(w.firstSeq == cut)
        _ = w.prepend(pager.eventsPage(before: cut, limit: 1000), before: cut)
        let open = w.items.compactMap { if case .message(let m) = $0, m.open { return m.id }; return nil }
        #expect(open.isEmpty, "open runs: \(open)")
        #expect(w.items == AgentTranscript(events: all).items)
    }

    /// Unload far pages either way, fetch them back: same ids, same items;
    /// the live tail goes only while the reader is away from the bottom.
    @Test func unloadAndReloadKeepIds() throws {
        let all = longLog(turns: 40)
        let pager = Pager(events: all)
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: 30))
        for _ in 0..<4 { _ = w.prepend(pager.eventsPage(before: w.firstSeq, limit: 30), before: w.firstSeq) }
        let loaded = w.items
        #expect(w.segmentCount == 5)
        // the reader looks at the middle: segments far above go, and the
        // tail only once the reader is away from the bottom
        let mid = loaded.count / 2
        var following = w
        following.keep(mid - 5, mid + 5, canDetach: false)
        #expect(!following.detached, "following: the tail stays")
        w.setFollowing(false)
        let okkeep = w.keep(visible: loaded[mid].id, loaded[mid + 1].id, margin: 5, canDetach: !w.following)
        #expect(okkeep)
        #expect(w.detached && w.hasNewer && w.hasOlder)
        #expect(w.segmentCount < 5)
        #expect(w.items.contains { $0.id == loaded[mid].id })
        // fetch back above and below
        while w.hasOlder, w.items.first?.id != loaded.first?.id {
            _ = w.prepend(pager.eventsPage(before: w.firstSeq, limit: 30), before: w.firstSeq)
        }
        while let req = w.newerRequest {
            switch req {
            case .page(let before, let limit): w.appendNewer(pager.eventsPage(before: before, limit: limit).events, for: req)
            case .since(let s): w.appendNewer(all.filter { $0.seq > s }, for: req)
            }
        }
        #expect(!w.detached && !w.hasNewer)
        #expect(w.items == loaded, "the same items, ids and values")
    }

    /// Detached: live events move the digest and the count, not the items;
    /// reading the tail back brings them.
    @Test func detachedTailCountsWhatItMisses() throws {
        var all = longLog(turns: 20)
        let pager = Pager(events: all)
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: 30))
        _ = w.prepend(pager.eventsPage(before: w.firstSeq, limit: 30), before: w.firstSeq)
        w.setFollowing(false)
        let okkeep = w.keep(0, 3, canDetach: true)
        #expect(okkeep)
        #expect(w.detached)
        let items = w.items
        // a new turn, live
        let base = all.last!.seq
        let live = [
            ev(base + 1, "message.delta", j(#"{"role":"user","text":"more"}"#)),
            ev(base + 2, "status", j(#"{"status":"running"}"#)),
            ev(base + 3, "message.delta", j(#"{"role":"agent","text":"x","messageId":"z"}"#)),
            ev(base + 4, "message.delta", j(#"{"role":"agent","text":"y","messageId":"z"}"#)),
            ev(base + 5, "permission.request", j(#"{"pid":"p9","toolCall":{"id":"q","title":"rm"},"options":[{"optionId":"a","name":"Allow","kind":"allow_once"}]}"#)),
        ]
        for e in live { let a = w.receiveLive(e); #expect(a == .none) }
        #expect(w.items == items)
        #expect(w.fresh == 3, "a prompt, a message run, a request")
        #expect(w.state.status == .running && w.pendingCount == 1 && w.lastSeq == base + 5)
        #expect(w.activityState?.phase == .waiting)
        // the reader comes back down: the tail is live again with them
        all += live
        while let req = w.newerRequest {
            switch req {
            case .page(let before, let limit): w.appendNewer(Pager(events: all).eventsPage(before: before, limit: limit).events, for: req)
            case .since(let s): w.appendNewer(all.filter { $0.seq > s }, for: req)
            }
        }
        #expect(!w.detached)
        #expect(w.items.suffix(3).map(\.id) == ["m\(base + 1)", "m\(base + 3)", "perm:p9"])
        w.setFollowing(true)
        #expect(w.fresh == 0)
    }

    /// Splitting the live tail at the server's cut moves no id.
    @Test func splitKeepsIds() throws {
        let all = longLog(turns: 12)
        let pager = Pager(events: all)
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: 1000))
        let before = w.items
        let tail = pager.eventsPage(limit: 25)
        let oksplit = w.split(at: tail.nextBefore, state: tail.state)
        #expect(oksplit)
        #expect(w.segmentCount == 2)
        #expect(w.items == before)
        // live events still fold into the (new) tail
        let e = ev(all.last!.seq + 1, "message.delta", j(#"{"role":"user","text":"again"}"#))
        w.receiveLive(e)
        #expect(w.items.last?.id == "m\(e.seq)")
    }

    /// An unseen older event refolds only its segment; a duplicate changes nothing.
    @Test func lateAndDuplicateEvents() throws {
        var all = longLog(turns: 6)
        let hole = all.remove(at: all.count - 4) // the last turn's tool completion
        #expect(hole.type == "tool.update")
        var w = AgentWindow()
        w.open(tail: Pager(events: all).eventsPage(limit: 20))
        func lastTool() -> ToolCall? {
            for it in w.items.reversed() { if case .tool(let t) = it { return t } }
            return nil
        }
        #expect(lastTool()?.status == .inProgress)
        let v = w.version
        w.receiveLive(all.last!)
        #expect(w.version == v, "a duplicate: no change")
        w.apply(events: [hole], since: hole.seq - 1)
        #expect(w.version != v)
        #expect(lastTool()?.status == .completed)
    }

    /// An xbind that does not page: one segment, nothing unloads.
    @Test func oldXbindAnswersEverything() throws {
        let all = longLog(turns: 10)
        var w = AgentWindow()
        w.open(tail: EventsPage(json: ["events": .array(all.map(\.json)), "next": .number(Double(all.last!.seq)), "truncated": false])!)
        #expect(!w.paged && !w.hasOlder && w.segmentCount == 1)
        #expect(w.items == AgentTranscript(events: all).items)
        let okkeep = w.keep(0, 1, canDetach: true)
        #expect(!okkeep)
    }

    /// The state header: a running turn's waiting request from the tail
    /// page, the status digest from before it.
    @Test func stateHeaderSeedsTheDigest() throws {
        var all = longLog(turns: 5)
        let base = all.last!.seq
        all += [
            ev(base + 1, "message.delta", j(#"{"role":"user","text":"go"}"#)),
            ev(base + 2, "status", j(#"{"status":"running"}"#)),
            ev(base + 3, "tool.call", j(#"{"id":"k","title":"ls","kind":"execute","status":"in_progress"}"#)),
            ev(base + 4, "permission.request", j(#"{"pid":"p1","toolCall":{"id":"k","title":"ls"},"options":[{"optionId":"a","name":"Allow","kind":"allow_once"}]}"#)),
        ]
        var pager = Pager(events: all)
        pager.waiting = ["p1"]
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: 2))
        #expect(w.firstSeq == base + 3)
        #expect(w.state.status == .running && w.state.agent?.name == "Fake" && w.state.commands.map(\.name) == ["review"])
        #expect(w.state.lastTurn == 5 && w.state.usage?.used == 500)
        #expect(w.pendingCount == 1 && w.pendingPermissions.map(\.pid) == ["p1"])
        #expect(w.activity()?.hasPrefix("Running") == true)
        #expect(w.state.turnStartedAt != nil)
    }
}

/// A paging fake xbind for the feed.
final class PagedLog: @unchecked Sendable {
    private let lock = NSLock()
    private var evs: [AgentEvent]
    init(_ e: [AgentEvent]) { evs = e }
    func add(_ e: [AgentEvent]) { lock.withLock { evs += e } }
    var all: [AgentEvent] { lock.withLock { evs } }

    func transport() -> FakeTransport {
        FakeTransport { r in
            guard r.path.hasSuffix("/events") else { return ok(#"{"ok":true}"#) }
            let all = self.all
            if let s = r.q("since") { return page(all.filter { $0.seq > UInt64(s) ?? 0 }) }
            return ok(Pager(events: all).page(before: UInt64(r.q("before") ?? "0") ?? 0, limit: Int(r.q("limit") ?? "200") ?? 200))
        }
    }
}

@Suite struct PagedFeedTests {
    func frame(_ e: AgentEvent) -> SessionHubEvent {
        SessionHubEvent(json: ["type": "session", "topic": "session.s", "data": e.json])!
    }

    /// Opens on the tail page, loads older pages on demand, publishes
    /// nothing for an event the other channel already delivered.
    @Test func opensOnTheTailAndLoadsOlder() async throws {
        let log = PagedLog(longLog(turns: 30))
        let t = log.transport()
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s", pageLimit: 40)
        await feed.catchUp()
        #expect(t.requests.map { $0.q("limit") } == ["40"])
        let w0 = await feed.window
        #expect(w0.paged && w0.hasOlder && w0.events.count < 60)
        #expect(await feed.loadOlder())
        #expect(t.requests.last?.q("before") == String(w0.firstSeq))
        #expect(await feed.window.items.count > w0.items.count)
        // a live event, then the same one again (follow + /ws/events)
        let e = ev(log.all.last!.seq + 1, "message.delta", j(#"{"role":"user","text":"hi"}"#))
        log.add([e])
        await feed.receive(frame(e))
        let v = await feed.window.version
        await feed.receive(frame(e))
        #expect(await feed.window.version == v)
    }

    /// A resume's replay arrives as one `replayed` frame: the tail is read again.
    @Test func replayedReReadsTheTail() async throws {
        let log = PagedLog(Array(longLog(turns: 3)))
        let t = log.transport()
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s")
        await feed.catchUp()
        let n = t.requests.count
        // the replay logged a whole earlier conversation; the hub says so once
        let base = log.all.last!.seq
        log.add(longLog(turns: 4).map { ev(base + $0.seq, $0.type, $0.data) })
        let replayed = SessionHubEvent(json: ["type": "session", "topic": "session.s",
                                              "data": ["seq": 0, "type": "replayed", "data": ["first": .number(Double(base + 1)), "last": .number(Double(log.all.last!.seq))]]])
        #expect(replayed?.isReplayed == true)
        await feed.receive(replayed!)
        #expect(t.requests.count == n + 1 && t.requests.last?.q("limit") != nil)
        #expect(await feed.window.lastSeq == log.all.last!.seq)
    }

    /// Scrolled far up with the tail unloaded, the pill re-reads the tail.
    @Test func jumpToLatestReadsTheTail() async throws {
        let log = PagedLog(longLog(turns: 30))
        let t = log.transport()
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s", pageLimit: 30)
        await feed.catchUp()
        await feed.loadOlder()
        await feed.loadOlder()
        await feed.setAtBottom(false)
        let top = await feed.window.items
        await feed.keep(visible: top[0].id, top[1].id, margin: 2)
        #expect(await feed.window.detached)
        let e = ev(log.all.last!.seq + 1, "message.delta", j(#"{"role":"user","text":"new"}"#))
        log.add([e])
        await feed.receive(frame(e))
        #expect(await feed.window.fresh == 1)
        await feed.jumpToLatest()
        let w = await feed.window
        #expect(!w.detached && w.following && w.fresh == 0 && w.items.last?.id == "m\(e.seq)")
    }
}

@MainActor
@Suite struct RowsTests {
    /// Rows keep their objects by id; a row whose item didn't change is not
    /// touched; open state survives an unload and a reload.
    @Test func rowsKeepIdentity() throws {
        let all = longLog(turns: 20)
        let pager = Pager(events: all)
        var w = AgentWindow()
        w.open(tail: pager.eventsPage(limit: 30))
        let rows = AgentRows()
        rows.apply(w)
        let first = rows.rows
        #expect(first.map(\.id) == w.items.map(\.id))
        #expect(rows.status == .idle && rows.agentName == "Fake" && rows.hasOlder)
        // a streaming delta touches one row
        let base = all.last!.seq
        w.receiveLive(ev(base + 1, "message.delta", j(#"{"role":"agent","text":"x","messageId":"q"}"#)))
        rows.apply(w)
        w.receiveLive(ev(base + 2, "message.delta", j(#"{"role":"agent","text":"y","messageId":"q"}"#)))
        let before = rows.rows
        rows.apply(w)
        #expect(zip(before, rows.rows).allSatisfy { $0 === $1 })
        guard case .message(let m) = rows.rows.last!.item else { Issue.record("last"); return }
        #expect(m.text == "xy")
        // prepend: the old rows are the same objects
        let tool = try #require(rows.rows.first { if case .tool = $0.item { return true }; return false })
        rows.setOpen(tool, true)
        _ = w.prepend(pager.eventsPage(before: w.firstSeq, limit: 30), before: w.firstSeq)
        rows.apply(w)
        #expect(rows.rows.contains { $0 === first[0] })
        #expect(rows.index(of: first[0].id)! > 0)
        // unload the tool's segment and bring it back: open again, new object
        w.setFollowing(false)
        let top = rows.rows[0].id
        w.keep(visible: top, top, margin: 0, canDetach: true)
        rows.apply(w)
        #expect(!rows.rows.contains { $0.id == tool.id })
        #expect(rows.hasNewer)
        while let req = w.newerRequest {
            switch req {
            case .page(let b, let l): w.appendNewer(pager.eventsPage(before: b, limit: l).events, for: req)
            case .since(let s): w.appendNewer((all + [ev(base + 1, "message.delta", j(#"{"role":"agent","text":"x","messageId":"q"}"#)),
                                                     ev(base + 2, "message.delta", j(#"{"role":"agent","text":"y","messageId":"q"}"#))])
                                                .filter { $0.seq > s }, for: req)
            }
        }
        rows.apply(w)
        let back = try #require(rows.rows.first { $0.id == tool.id })
        #expect(back.isOpen && back !== tool)
    }
}
