import Foundation
import Testing
@testable import XbinAgent

/// Messages while a turn runs (D197): a steered message is a bubble of its
/// own, the `queue` event and the tail page's `state.queue` are the held
/// messages, and the client's POST/DELETE …/queue — an older xbind's 404
/// included.
@Suite struct QueueTests {
    @Test func aSteeredMessageIsItsOwnBubble() {
        let evs = [
            ev(1, "message.delta", j(#"{"role":"user","text":"refactor the parser"}"#)),
            ev(2, "status", j(#"{"status":"running"}"#)),
            ev(3, "message.delta", j(#"{"role":"user","text":"and keep the old API","steered":true}"#)),
            ev(4, "message.delta", j(#"{"role":"user","text":" too","steered":true}"#)),
            ev(5, "message.delta", j(#"{"role":"agent","text":"On it."}"#)),
        ]
        let t = AgentTranscript(events: evs)
        let msgs = t.items.compactMap { if case .message(let m) = $0 { return m }; return nil }
        #expect(msgs.map(\.text) == ["refactor the parser", "and keep the old API", " too", "On it."],
                "never merged into the prompt before it, nor another steered one")
        #expect(msgs.map(\.steered) == [false, true, true, false])
        #expect(t.state.turnStartedAt == evs[0].ts, "a steered message joins the running turn: its start stays")
        // a plain user delta still merges into its run, as before
        let plain = AgentTranscript(events: [ev(1, "message.delta", j(#"{"role":"user","text":"a"}"#)),
                                             ev(2, "message.delta", j(#"{"role":"user","text":"b"}"#))])
        #expect(plain.items.count == 1)
    }

    @Test func theQueueIsStateNotARow() {
        let evs = [
            ev(1, "thought.delta", j(#"{"text":"thinking"}"#)),
            ev(2, "queue", j(#"{"items":[{"id":"q1","text":"then the tests","files":["a.png"],"at":1700}]}"#)),
        ]
        let t = AgentTranscript(events: evs)
        #expect(t.state.queue == [QueuedMessage(id: "q1", text: "then the tests", files: ["a.png"], at: 1700)])
        #expect(t.items.count == 1, "no row for it")
        guard case .thought(let th)? = t.items.first else { Issue.record("thought"); return }
        #expect(!th.done, "a queue change doesn't end the thought (as on the web)")
        let sent = AgentTranscript(events: evs + [ev(3, "queue", j(#"{"items":[],"sent":["q1"]}"#))])
        #expect(sent.state.queue.isEmpty)
        if case .queue(let q) = ev(3, "queue", j(#"{"items":[],"dropped":2,"reason":"the session ended"}"#)).payload {
            #expect(q.dropped == 2 && q.reason == "the session ended")
        } else {
            Issue.record("payload")
        }
        // the digest a window keeps apart from its folds follows it too
        var st = AgentSessionState()
        st.absorb(evs[1])
        #expect(st.queue.map(\.id) == ["q1"])
    }

    @Test func theTailPageSaysWhatIsHeldNow() {
        let page = PageState(json: j(#"{"status":{"status":"running"},"queue":[{"id":"q7","text":"next"}]}"#))
        #expect(page.queue?.map(\.id) == ["q7"])
        #expect(PageState(json: j(#"{"status":{"status":"running"}}"#)).queue == nil, "nothing held, or an older xbind")
        let t = AgentTranscript(seed: page, events: [ev(9, "queue", j(#"{"items":[]}"#))])
        #expect(t.state.queue.isEmpty, "the page's events are newer than its header")
        #expect(AgentTranscript(seed: page).state.queue.map(\.text) == ["next"])
    }

    @Test func queueAndUnqueueRoutes() async throws {
        let t = FakeTransport { r in
            switch (r.method, r.path) {
            case ("POST", "/api/xbin/term/sessions/s%201/queue"): return ok(#"{"outcome":"queued","id":"q3"}"#)
            case ("DELETE", "/api/xbin/term/sessions/s%201/queue/q3"): return ok(#"{"items":[{"id":"q3","text":"the whole text","at":5}]}"#)
            default: return refuse(404, "not found")
            }
        }
        let c = AgentClient(transport: t)
        let a = try await c.queue("s 1", text: "the whole text")
        #expect(a.outcome == .queued && a.id == "q3")
        #expect(try await c.unqueue("s 1", qid: "q3").map(\.text) == ["the whole text"])
        let first = try #require(t.requests.first)
        #expect(first.method == "POST" && first.path == "/api/xbin/term/sessions/s%201/queue")
        #expect(first.json?["text"]?.string == "the whole text")
        // an xbind before D197: 404, which the app takes as "between turns only"
        let old = AgentClient(transport: FakeTransport { _ in refuse(404, "404 page not found") })
        do {
            _ = try await old.queue("s", text: "hi")
            Issue.record("queued on an old xbind")
        } catch let e as AgentAPIError {
            #expect(e.isNotFound)
        }
    }
}
