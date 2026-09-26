import Foundation
import Testing
@testable import XbinAgent

let allFixtures = ["basic", "permissions", "plan", "ask", "subagent", "shell", "cancel", "signedout"]

@Suite struct TranscriptTests {
    /// Folding live, one event at a time, gives what a replay gives.
    @Test(arguments: allFixtures)
    func liveEqualsReplay(_ name: String) throws {
        let evs = try events(name)
        var live = AgentTranscript()
        for e in evs { #expect(live.receiveLive(e) == .none) }
        let replay = AgentTranscript(events: evs)
        #expect(live.items == replay.items)
        #expect(live.state == replay.state)
        #expect(live.lastSeq == evs.last?.seq)
        // and so does a replay in pages of three, and a shuffled one
        var paged = AgentTranscript()
        var since: UInt64 = 0
        for chunk in stride(from: 0, to: evs.count, by: 3).map({ Array(evs[$0..<min($0 + 3, evs.count)]) }) {
            paged.apply(events: chunk, since: since)
            since = paged.lastSeq
        }
        #expect(paged.items == replay.items)
        var shuffled = AgentTranscript()
        var rng = SeededRandom(seed: 7)
        let order = evs.shuffled(using: &rng)
        for k in stride(from: 0, to: order.count, by: 5) { shuffled.apply(events: Array(order[k..<min(k + 5, order.count)])) }
        #expect(shuffled.items == replay.items)
        #expect(shuffled.state == replay.state)
        // the persisted transcript is the same log
        if let h = try fixture(name)["history"].flatMap(HistoryTranscript.init(json:)) {
            #expect(AgentTranscript(events: h.events).items == replay.items)
        }
    }

    @Test func sessionStateFromStatus() throws {
        let t = AgentTranscript(events: try events("basic"))
        let s = t.state
        #expect(s.status == .idle && !s.isBusy)
        #expect(s.title == "fake: hello there")
        #expect(s.commands.map(\.name) == ["review", "compact", "init"])
        #expect(s.commands.first?.hint == "what to focus on")
        #expect(s.options.map(\.id) == ["model"] && s.options[0].currentValue == "fake-default" && s.options[0].values.count == 2)
        #expect(s.modes.map(\.id) == ["ask", "yolo"] && s.currentMode == "ask")
        #expect(s.agent?.name == "fakeacp")
        #expect(s.usage?.label == "42/1 000 tokens")
        #expect(s.lastTurn == 3 && s.lastStopReason == .endTurn)
        #expect(s.login == nil)
        // a mode picker (modes) and the model picker
        #expect(s.pickers().map(\.id) == ["mode", "model"])
        #expect(s.pickers()[1].values.map(\.label) == ["Fake (default)", "Fake fast"])
    }

    @Test func turnsAndThoughts() throws {
        let t = AgentTranscript(events: try events("basic"))
        let turns = t.turns
        #expect(turns.count == 3 && turns.allSatisfy { $0.end != nil })
        #expect(turns[0].items.map(\.id) == ["m3", "m5", "turn8"])
        guard case .thought(let th) = turns[1].items[1] else { Issue.record("no thought"); return }
        #expect(th.text.hasPrefix("**step 0**") && th.text.contains("**step 5**"))
        #expect(th.done && th.seconds == 2)
        #expect(!th.isLive(status: .running))
        guard case .message(let m) = turns[2].items[1] else { Issue.record("no message"); return }
        #expect(m.text == String(repeating: "x", count: 50)) // the burst, coalesced by the daemon
        #expect(!m.open)
        #expect(turns[2].end?.label == "turn 3 · done")
    }

    @Test func streamingMidTurn() throws {
        let evs = try events("basic")
        // up to the fourth thought chunk: a live thought
        var t = AgentTranscript(events: Array(evs.prefix(16)))
        guard case .thought(let th)? = t.items.last else { Issue.record("no thought"); return }
        #expect(!th.done && th.isLive(status: t.state.status))
        #expect(t.activity() == nil) // the thought shows itself
        t.receiveLive(evs[16])
        t.receiveLive(evs[17])
        t.receiveLive(evs[18]) // the answer: the thought ends
        guard case .message(let m)? = t.items.last, case .thought(let done) = t.items[t.items.count - 2] else { Issue.record("shape"); return }
        #expect(m.open && t.state.isBusy && done.done && m.isStreaming(status: t.state.status))
        #expect(t.activity() == "Working…")
        #expect(t.state.turnStartedAt == evs[9].ts) // the prompt of turn 2
        t.receiveLive(evs[19])
        t.receiveLive(evs[20]) // turn.end
        #expect(t.state.turnStartedAt == nil)
    }

    @Test func activityNamesTheRunningTool() throws {
        let evs = try events("shell")
        let t = AgentTranscript(events: Array(evs.prefix(5)))
        guard case .tool(let tool)? = t.items.last else { Issue.record("no tool"); return }
        #expect(tool.status == .inProgress)
        #expect(t.activity() == "Running \(tool.headline)…")
        #expect(t.activity(ended: true) == nil)
    }

    @Test func shellOutputAndSnapshotDiffs() throws {
        let t = AgentTranscript(events: try events("shell"))
        let tools = t.items.compactMap { if case .tool(let x) = $0 { return x }; return nil }
        #expect(tools.count == 2)
        #expect(tools[0].status == .failed && tools[0].failedExit == 3)
        #expect(tools[0].outputView().full.plain == "error: ok\nline2\n")
        #expect(tools[0].command.hasPrefix("printf"))
        // files.changed landed after turn.end: on the call (by id) and before the turn's divider
        #expect(tools[1].files?.changes.map(\.path) == ["made.txt", "main.go"])
        #expect(tools[1].headline.hasPrefix("echo hi > made.txt"))
        let tail = t.items.suffix(2).map(\.id)
        #expect(tail[0].hasPrefix("changes") && tail[1] == "turn18")
        guard case .changes(let c) = t.items[t.items.count - 2] else { Issue.record("no changes"); return }
        #expect(c.turn == 2 && c.files.stat == (files: 2, add: 2, del: 1))
    }

    @Test func subagentNests() throws {
        let t = AgentTranscript(events: try events("subagent"))
        guard case .tool(let task) = t.items[1] else { Issue.record("no task"); return }
        #expect(task.isSubagent && task.status == .completed)
        #expect(task.headline == "Explore the repo" && task.subagentType == "Explore" && task.subagentPrompt.contains("main"))
        #expect(task.children.map(\.id) == ["th7", "tool0:read1", "m10"])
        #expect(task.subagentSteps == 1)
        guard case .thought(let th) = task.children[0], case .message(let m) = task.children[2] else { Issue.record("children"); return }
        #expect(th.done && th.parent == "task1")
        #expect(m.text == "main starts in main.go" && !m.open)
        #expect(t.item(id: "tool0:read1") != nil)
        // the answer, after the Task card
        guard case .message(let answer) = t.items[2] else { Issue.record("answer"); return }
        #expect(answer.text == "the subagent found it")
    }

    @Test func pendingRequestsSettle() throws {
        let evs = try events("permissions")
        var t = AgentTranscript(events: Array(evs.prefix(7)))
        #expect(t.state.status == .waitingPermission)
        #expect(t.pendingPermissions.map(\.pid) == ["p1"])
        t.receiveLive(evs[7])
        #expect(t.pendingPermissions.isEmpty)
        #expect(t.lastStatus(after: 6) == .waitingPermission)
        #expect(t.lastStatus(after: 0) == .waitingPermission)
        let ask = try events("ask")
        let q = AgentTranscript(events: Array(ask.prefix(7)))
        #expect(q.pendingQuestions.map(\.eid) == ["e1"])
        #expect(q.pendingQuestions.first?.fields.map(\.key) == ["question_0", "question_1"])
        let done = AgentTranscript(events: ask)
        let cards = done.items.compactMap { if case .question(let c) = $0 { return c }; return nil }
        #expect(cards.map { $0.resolution?.action } == [.accept, .decline])
        #expect(cards[0].fields[1].answer(in: cards[0].resolution?.content) == "Metrics — Admin UI")
        #expect(cards[0].resolution?.by.displayName == "alice" && cards[1].resolution?.action.verb == "skipped")
    }

    @Test func seqGapsAskForARefetch() throws {
        let evs = try events("basic")
        var t = AgentTranscript(events: Array(evs.prefix(5)))
        #expect(t.receiveLive(evs[7]) == .refetch(since: 5)) // 6 and 7 were missed
        #expect(t.lastSeq == 5)
        #expect(t.receiveLive(evs[2]) == .none) // old: ignored
        t.apply(events: Array(evs[5...]), since: 5)
        #expect(t.items == AgentTranscript(events: evs).items)
        // a live event before any replay: refetch from 0
        var empty = AgentTranscript()
        #expect(empty.receiveLive(evs[3]) == .refetch(since: 0))
    }

    @Test func truncatedPagesAndStreamGaps() throws {
        let evs = try events("basic")
        // the replay's cursor predated the ring: a gap at the top
        var t = AgentTranscript()
        t.apply(events: Array(evs[9...]), since: 0, truncated: true)
        #expect(t.truncated)
        guard case .gap(let g) = t.items.first else { Issue.record("no gap"); return }
        #expect(g.before == 0)
        // a follow stream: a gap line, then a jump
        var s = AgentTranscript(events: Array(evs.prefix(4)))
        #expect(s.receiveStream(AgentEvent(json: j(#"{"type":"gap","data":{"before":4}}"#))!) == .none)
        #expect(s.receiveStream(evs[20]) == .none) // seq 21 after 4: allowed once, after the gap
        #expect(s.lastSeq == 21 && s.truncated)
        #expect(s.items.contains { if case .gap = $0 { return true }; return false })
        #expect(s.receiveStream(evs[26]) == .refetch(since: 21)) // a jump without a gap line
        // events that turn up behind the gap re-fold in order; the marker stays where it was seen
        s.apply(events: Array(evs[4..<20]), since: 4)
        #expect(s.items.map(\.id).prefix(4) == ["m3", "gap4", "m5", "turn8"])
    }

    @Test func loginIsStickyAcrossPartialStatuses() throws {
        let t = AgentTranscript(events: try events("signedout"))
        #expect(t.state.status == .error && t.state.isEnded)
        // the last status (commands only) carries no login — the prompt stays
        #expect(t.state.login == LoginNeeded(provider: "Fake agent (tests)", command: "the fake needs no login"))
        #expect(t.state.detail.contains("Please run /login"))
        #expect(t.state.lastStopReason == .error && t.state.lastTurnError != nil)
        guard case .turnEnd(let d)? = t.items.last else { Issue.record("no divider"); return }
        #expect(d.stopReason == .error && d.error != nil && d.label == "turn 1 · failed")

        var s = AgentTranscript()
        s.apply(events: [ev(1, "status", j(#"{"status":"idle","login":{"needed":true,"provider":"Claude Code","command":"claude /login"}}"#)),
                         ev(2, "status", j(#"{"status":"idle","usage":{"used":1,"size":2}}"#))])
        #expect(s.state.login != nil)
        s.receiveLive(ev(3, "status", j(#"{"status":"idle","currentMode":"default"}"#))) // a full status without login: signed in
        #expect(s.state.login == nil)
        s.receiveLive(ev(4, "status", j(#"{"status":"running","login":{"needed":true,"provider":"P","command":"c"}}"#)))
        s.receiveLive(ev(5, "turn.end", j(#"{"turn":1,"stopReason":"end_turn"}"#)))
        #expect(s.state.login == nil) // a turn that ran: signed in
        #expect(s.state.signIn(provider: AgentProvider(json: j(#"{"id":"claude","name":"Claude Code","login":"claude /login"}"#)),
                               lastError: "Authentication required (-32000)") == LoginNeeded(provider: "Claude Code", command: "claude /login"))
    }

    @Test func planChecklistPerTurn() {
        var t = AgentTranscript()
        t.apply(events: [
            ev(1, "message.delta", j(#"{"role":"user","text":"go"}"#)),
            ev(2, "plan", j(#"{"entries":[{"content":"a","priority":"high","status":"pending"},{"content":"b","status":"pending"}]}"#)),
            ev(3, "message.delta", j(#"{"role":"agent","text":"working","messageId":"m"}"#)),
            ev(4, "plan", j(#"{"entries":[{"content":"a","status":"completed"},{"content":"b","status":"in_progress"}]}"#)),
            ev(5, "turn.end", j(#"{"turn":1,"stopReason":"end_turn"}"#)),
            ev(6, "plan", j(#"{"entries":[{"content":"c","status":"pending"}]}"#)),
        ])
        let plans = t.items.compactMap { if case .plan(let p) = $0 { return p }; return nil }
        #expect(plans.count == 2)
        #expect(plans[0].entries.map(\.glyph) == ["✓", "▸"] && plans[0].completed == 1)
        #expect(plans[1].entries.map(\.content) == ["c"])
    }

    @Test func toolIdsAreScopedPerTurnAndMessagesByRun() {
        var t = AgentTranscript()
        t.apply(events: [
            ev(1, "message.delta", j(#"{"role":"agent","text":"a","messageId":"x"}"#)),
            ev(2, "message.delta", j(#"{"role":"agent","text":"b","messageId":"x"}"#)),
            ev(3, "message.delta", j(#"{"role":"agent","text":"c","messageId":"y"}"#)),
            ev(4, "tool.call", j(#"{"id":"t","title":"one","kind":"read","status":"completed"}"#)),
            ev(5, "turn.end", j(#"{"turn":1,"stopReason":"end_turn"}"#)),
            ev(6, "tool.call", j(#"{"id":"t","title":"two","kind":"read","status":"in_progress"}"#)),
            ev(7, "files.changed", j(#"{"toolCallId":"t","changes":[{"path":"f","status":"modified","add":1,"del":0}]}"#)),
            ev(8, "tool.update", j(#"{"id":"t","status":"completed"}"#)),
            ev(9, "future.thing", j(#"{"x":1}"#)),
        ])
        #expect(t.items.map(\.id) == ["m1", "m3", "tool0:t", "turn5", "tool1:t"])
        guard case .message(let m) = t.items[0], case .tool(let second) = t.items[4] else { Issue.record("shape"); return }
        #expect(m.text == "ab" && !m.open)
        #expect(second.title == "two" && second.status == .completed && second.files?.changes.count == 1)
        #expect(AgentEvent(json: j(#"{"seq":9,"type":"future.thing","data":{}}"#))!.payload == .unknown(type: "future.thing", data: j("{}")))
    }
}

/// A deterministic RNG for the shuffled-replay test.
struct SeededRandom: RandomNumberGenerator {
    var state: UInt64
    init(seed: UInt64) { state = seed }
    mutating func next() -> UInt64 {
        state = state &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
        return state
    }
}

@Suite struct TranscriptScaleTests {
    /// A long stream folds in linear time: a message's text, a subagent's
    /// nested message and a command's output all grow in place.
    @Test func longStreams() {
        var t = AgentTranscript()
        var seq: UInt64 = 0
        func next(_ type: String, _ d: JSONValue) { seq += 1; t.receiveLive(ev(seq, type, d)) }
        next("tool.call", j(#"{"id":"task","kind":"think","status":"in_progress","subagent":true}"#))
        next("tool.call", j(#"{"id":"sh","kind":"execute","status":"in_progress","rawInput":{"command":"yes"}}"#))
        let chunk = String(repeating: "y\n", count: 40)
        let start = Date()
        for _ in 0..<3000 {
            next("tool.update", ["id": "sh", "outputDelta": .string(chunk)])
            next("message.delta", ["role": "agent", "text": "word ", "messageId": "m", "parent": "task"])
        }
        let elapsed = Date().timeIntervalSince(start)
        guard case .tool(let task) = t.items[0], case .tool(let sh) = t.items[1], case .message(let m) = task.children[0] else {
            Issue.record("shape"); return
        }
        #expect(sh.output.utf8.count == 3000 * chunk.utf8.count)
        #expect(m.text.utf8.count == 3000 * 5)
        #expect(elapsed < 10, "folding 6000 deltas took \(elapsed)s")
    }
}
