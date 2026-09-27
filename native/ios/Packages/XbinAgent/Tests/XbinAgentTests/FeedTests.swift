import Foundation
import Testing
@testable import XbinAgent

/// A session log the fake serves: grows as the test appends to it.
final class Log: @unchecked Sendable {
    private let lock = NSLock()
    private var evs: [AgentEvent] = []
    var gone = false
    func add(_ e: [AgentEvent]) { lock.withLock { evs += e } }
    func since(_ s: UInt64) -> [AgentEvent] { lock.withLock { evs.filter { $0.seq > s } } }
}

@Suite struct FeedTests {
    func served(_ log: Log, prompt: (@Sendable (HTTPRequest) -> Void)? = nil) -> FakeTransport {
        FakeTransport { r in
            if log.gone { return refuse(404, "no such session") }
            if r.path.hasSuffix("/events") { return page(log.since(UInt64(r.q("since") ?? "0") ?? 0)) }
            if r.path.hasSuffix("/prompt") { prompt?(r); return ok(#"{"ok":true,"turn":1}"#) }
            return ok(#"{"ok":true}"#)
        }
    }

    @Test func catchUpAndLiveFrames() async throws {
        let evs = try events("basic")
        let log = Log()
        log.add(Array(evs.prefix(10)))
        let t = served(log)
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s")
        await feed.catchUp()
        #expect(await feed.transcript.lastSeq == 10)
        // the next frame applies directly; a skipped one triggers ?since=
        log.add(Array(evs[10..<14]))
        await feed.receive(SessionHubEvent(json: ["type": "session", "topic": "session.s", "data": evs[10].json])!)
        #expect(await feed.transcript.lastSeq == 11)
        await feed.receive(SessionHubEvent(json: ["type": "session", "topic": "session.s", "data": evs[13].json])!)
        #expect(await feed.transcript.lastSeq == 14)
        #expect(t.requests.map { $0.q("since") } == ["0", "11"])
        // another session's frame is not ours
        await feed.receive(SessionHubEvent(json: ["type": "session", "topic": "session.other", "data": evs[20].json])!)
        #expect(await feed.transcript.lastSeq == 14)
        // updates() yields the current transcript first
        var it = await feed.updates().makeAsyncIterator()
        #expect(await it.next()?.lastSeq == 14)
    }

    @Test func followsReconnectsAndEnds() async throws {
        let evs = try events("cancel")
        let log = Log()
        log.add(Array(evs.prefix(4)))
        let t = served(log)
        // the first stream drops after four events (the catch-up finds nothing
        // newer); the reconnect carries the rest; then the session is gone
        t.queueStream { _ in evs.prefix(4).map { $0.json.data + Data([0x0A]) } }
        t.queueStream { r in
            #expect(r.q("since") == "3") // one back: any xbind answers at once
            log.add(Array(evs.dropFirst(4)))
            log.gone = true
            return evs.dropFirst(4).map { $0.json.data + Data([0x0A]) }
        }
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s", sleep: { _ in })
        await feed.run()
        #expect(t.requests.map { "\($0.method) \($0.q("since") ?? "")\($0.q("follow") == "1" ? " follow" : "")" } ==
            ["GET 0 follow", "GET 4", "GET 3 follow", "GET 20"])
        #expect(await feed.ended)
        #expect(await feed.transcript.items == AgentTranscript(events: evs).items)
    }

    @Test func keepPlanningSendsTheFeedbackOnceTheTurnSettles() async throws {
        let evs = try events("plan")
        let log = Log()
        log.add(Array(evs.prefix(20))) // the second plan waits (seq 19 request, 20 waiting)
        let sent = Log()
        let t = served(log) { r in sent.add([AgentEvent(seq: 1, type: "prompt", data: r.json ?? .object(JSONObject()))]) }
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s")
        await feed.catchUp()
        let card = try #require(await feed.transcript.pendingPermissions.first)
        #expect(card.isPlanApproval)
        let reject = try #require(PermissionRules.planRejections(card.request).first)
        log.add(Array(evs[20..<23])) // resolved, running, tool failed — not settled yet
        try await feed.keepPlanning(card, choice: reject, feedback: "  use SQLite instead  ")
        #expect(await feed.followUp?.text == "use SQLite instead")
        #expect(sent.since(0).isEmpty)
        log.add(Array(evs[23...])) // turn.end cancelled, status idle
        await feed.catchUp()
        for _ in 0..<100 where sent.since(0).isEmpty { try await Task.sleep(for: .milliseconds(5)) }
        #expect(sent.since(0).first?.data == ["text": "use SQLite instead"])
        #expect(await feed.followUp == nil)
        let answer = t.requests.first { $0.path.hasSuffix("/permissions/p2") }
        #expect(answer?.json == ["optionId": "reject"])
    }

    @Test func aFollowUpComesBackWhenTheSessionEnds() async throws {
        let evs = try events("plan")
        let log = Log()
        log.add(Array(evs.prefix(20)))
        let feed = AgentSessionFeed(client: AgentClient(transport: served(log)), sessionID: "s")
        await feed.catchUp()
        let card = try #require(await feed.transcript.pendingPermissions.first)
        try await feed.keepPlanning(card, choice: PermissionRules.planRejections(card.request)[0], feedback: "nope")
        log.gone = true
        await feed.catchUp()
        #expect(await feed.ended)
        #expect(await feed.takeReturnedDraft() == "nope")
        #expect(await feed.takeReturnedDraft() == nil)
    }

    @Test func questionsCheckRequiredFields() async throws {
        let evs = try events("ask")
        let log = Log()
        log.add(Array(evs.prefix(7)))
        let t = served(log)
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s")
        await feed.catchUp()
        var card = try #require(await feed.transcript.pendingQuestions.first)
        card.fields[0].required = true
        await #expect(throws: FormError(missing: ["Database"])) { try await feed.answer(card, action: .accept, values: [:]) }
        try await feed.answer(card, action: .accept, values: ["question_0": "Postgres", "question_1": ["Metrics", "Tracing"]])
        let posted = t.requests.last { $0.path.hasSuffix("/elicitations/e1") }
        #expect(posted?.json == j(#"{"action":"accept","content":{"question_0":"Postgres","question_1":["Metrics","Tracing"]}}"#))
    }

    /// What a screen shows about the live stream: why it can't be opened
    /// (while it retries), then live, then ended.
    @Test func followStateSaysWhyTheStreamWontOpen() async throws {
        let evs = try events("cancel")
        let log = Log()
        let t = served(log)
        t.queueStream { _ in throw AgentAPIError(status: 403, message: "not yours") }
        t.queueStream { _ in
            log.add(evs)
            log.gone = true
            return evs.map { $0.json.data + Data([0x0A]) }
        }
        let seen = StateLog()
        let box = FeedBox()
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s",
                                    sleep: { _ in await seen.add(box.feed?.followState) })
        box.feed = feed
        #expect(await feed.followState == .connecting)
        await feed.run()
        #expect(await seen.states == [.failing("not yours")]) // the retry's backoff saw it
        #expect(await feed.followState == .ended)
        var it = await feed.followStates().makeAsyncIterator()
        #expect(await it.next() == .ended)
    }

    /// An open nobody answers counts as failing after `stallAfter` — it
    /// keeps waiting; an idle stream that drops is not a failure.
    @Test func anUnansweredOpenIsFailingADropIsNot() async throws {
        let hang = HangingTransport()
        let feed = AgentSessionFeed(client: AgentClient(transport: hang), sessionID: "s", stallAfter: .milliseconds(50),
                                    sleep: { _ in })
        let run = Task { await feed.run() }
        #expect(await eventually { await feed.followState == .failing("no answer from the server") })
        run.cancel()
        _ = await run.value

        let drops = DroppingTransport()
        let feed2 = AgentSessionFeed(client: AgentClient(transport: drops), sessionID: "s", sleep: { _ in })
        let run2 = Task { await feed2.run() }
        #expect(await eventually { drops.opens >= 3 })
        let state = await feed2.followState
        #expect(state == .live || state == .connecting)
        run2.cancel()
        _ = await run2.value
    }

    @Test func refusalsSurface() async throws {
        let t = FakeTransport { r in r.path.hasSuffix("/prompt") ? refuse(409, "busy") : page([]) }
        let feed = AgentSessionFeed(client: AgentClient(transport: t), sessionID: "s")
        await #expect(throws: AgentAPIError.self) { try await feed.send("hi") }
        #expect(await feed.lastError?.isConflict == true)
    }
}

actor StateLog {
    var states: [AgentSessionFeed.FollowState] = []
    func add(_ s: AgentSessionFeed.FollowState?) { if let s { states.append(s) } }
}

final class FeedBox: @unchecked Sendable {
    var feed: AgentSessionFeed?
}

/// Polls `f` for up to 2 s.
func eventually(_ f: @Sendable () async -> Bool) async -> Bool {
    for _ in 0..<200 {
        if await f() { return true }
        try? await Task.sleep(for: .milliseconds(10))
    }
    return await f()
}

/// A follow that never answers (until cancelled).
struct HangingTransport: AgentTransport {
    func send(_ request: HTTPRequest) async throws -> HTTPResponse { page([]) }
    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> {
        try await Task.sleep(for: .seconds(600))
        throw CancellationError()
    }
}

/// A follow that opens, then drops (an idle stream timing out), every time.
final class DroppingTransport: AgentTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    var opens: Int { lock.withLock { n } }
    func send(_ request: HTTPRequest) async throws -> HTTPResponse { page([]) }
    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> {
        lock.withLock { n += 1 }
        return AsyncThrowingStream { c in c.finish(throwing: URLError(.timedOut)) }
    }
}
