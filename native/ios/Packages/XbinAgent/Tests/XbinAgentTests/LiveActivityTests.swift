import Foundation
import Testing
@testable import XbinAgent

@Suite struct LiveActivityTests {
    // MARK: the wire (the relay builds these; xbind computes the same)

    @Test func contentStateDecodesTheRelaysJSON() throws {
        let relay = #"{"phase":"waiting","since":1790000000,"pending":2}"#
        let s = try JSONDecoder().decode(AgentActivityState.self, from: Data(relay.utf8))
        #expect(s == AgentActivityState(phase: .waiting, since: 1_790_000_000, pending: 2))
        // lenient: an unknown phase, doubles, missing fields, extra fields
        let odd = #"{"phase":"compiling","since":1790000000.0,"extra":true}"#
        let o = try JSONDecoder().decode(AgentActivityState.self, from: Data(odd.utf8))
        #expect(o == AgentActivityState(phase: .running, since: 1_790_000_000, pending: 0))
        #expect(try JSONDecoder().decode(AgentActivityState.self, from: Data("{}".utf8)) == AgentActivityState(phase: .running))
        // round trip, keys as the relay writes them
        let enc = try JSONEncoder().encode(s)
        let back = try JSONSerialization.jsonObject(with: enc) as? [String: Any]
        #expect(Set(back?.keys ?? [:].keys) == ["phase", "since", "pending"])
        #expect(try JSONDecoder().decode(AgentActivityState.self, from: enc) == s)
        #expect(s.startDate == Date(timeIntervalSince1970: 1_790_000_000))
        #expect(AgentActivityState(phase: .running).startDate == nil)
        #expect(s.finished == AgentActivityState(phase: .idle, since: 1_790_000_000, pending: 0))
    }

    @Test func attributesDecodeAPushStart() throws {
        // what a push-to-start carries (relay/liveactivity.go activityBody)
        let start = #"{"ws":"wsPushId_1","ref":"ref-7","workspace":"","session":"","appWorkspace":"","sessionID":""}"#
        let a = try JSONDecoder().decode(AgentActivityAttributes.self, from: Data(start.utf8))
        #expect(a == AgentActivityAttributes(ws: "wsPushId_1", ref: "ref-7"))
        #expect(a.pushStarted)
        // missing keys read as ""
        #expect(try JSONDecoder().decode(AgentActivityAttributes.self, from: Data(#"{"ws":"w"}"#.utf8)) == AgentActivityAttributes(ws: "w"))
        let local = AgentActivityAttributes(ws: "w", workspace: "Acme", session: "fix login", appWorkspace: "app-1", sessionID: "s1")
        #expect(!local.pushStarted)
        #expect(try JSONDecoder().decode(AgentActivityAttributes.self, from: JSONEncoder().encode(local)) == local)
    }

    // MARK: the transcript's state

    @Test func transcriptActivityState() {
        var t = AgentTranscript()
        #expect(t.activityState == nil)
        t.receiveLive(ev(1, "message.delta", ["role": "user", "text": "fix it"], ts: 1_790_000_000_500))
        t.receiveLive(ev(2, "status", ["status": "running"], ts: 1_790_000_000_600))
        #expect(t.activityState == AgentActivityState(phase: .running, since: 1_790_000_000, pending: 0))
        t.receiveLive(ev(3, "permission.request", ["pid": "p1", "toolCall": ["title": "rm"], "options": []], ts: 1_790_000_001_000))
        #expect(t.activityState?.phase == .waiting)
        #expect(t.activityState?.pending == 1)
        t.receiveLive(ev(4, "elicitation.request", ["eid": "e1", "message": "?", "schema": ["type": "object"]], ts: 1_790_000_002_000))
        #expect(t.activityState?.pending == 2)
        t.receiveLive(ev(5, "permission.resolved", ["pid": "p1", "optionId": "allow"], ts: 1_790_000_003_000))
        t.receiveLive(ev(6, "elicitation.resolved", ["eid": "e1", "action": "decline"], ts: 1_790_000_004_000))
        #expect(t.activityState == AgentActivityState(phase: .running, since: 1_790_000_000, pending: 0))
        t.receiveLive(ev(7, "status", ["status": "waiting_permission"], ts: 1_790_000_005_000))
        #expect(t.activityState?.phase == .waiting)
        t.receiveLive(ev(8, "turn.end", ["turn": 1, "stopReason": "end_turn"], ts: 1_790_000_006_000))
        t.receiveLive(ev(9, "status", ["status": "idle"], ts: 1_790_000_006_100))
        #expect(t.activityState == nil)
    }

    /// The captured sessions: a card from a turn's start to its end (the
    /// turn.end, not the idle status after it — xbind ends it there too),
    /// and `since` never moves within a turn.
    @Test func capturedSessions() throws {
        for name in ["basic", "cancel"] {
            var t = AgentTranscript()
            var since: Int64?
            var inTurn = false
            for e in try events(name) {
                t.receiveLive(e)
                if e.type == "status", e.data["status"]?.string == "running" { inTurn = true }
                if e.type == "turn.end" { inTurn = false }
                let s = t.activityState
                #expect((s != nil) == inTurn, "\(name) seq \(e.seq)")
                if let s {
                    if let since { #expect(s.since == since, "\(name) seq \(e.seq)") }
                    since = s.since
                } else {
                    since = nil
                }
            }
        }
    }

    // MARK: the policy

    let now = Date(timeIntervalSince1970: 1_790_000_100)
    let p = AgentActivityPolicy()

    func running(_ ago: TimeInterval) -> AgentActivityState {
        AgentActivityState(phase: .running, since: Int64(now.timeIntervalSince1970 - ago))
    }

    @Test func startsLongTurnsOnly() {
        // a quick turn: not yet; decide again when it is old enough
        let d = p.decide(state: running(3), shown: nil, canStart: true, now: now)
        #expect(d.action == .none)
        #expect(d.recheckAt == now.addingTimeInterval(7))
        #expect(p.decide(state: running(10), shown: nil, canStart: true, now: now).action == .start(running(10)))
        // waiting for the user, or leaving the foreground: at once
        let w = AgentActivityState(phase: .waiting, since: running(1).since, pending: 1)
        #expect(p.decide(state: w, shown: nil, canStart: true, now: now).action == .start(w))
        #expect(p.decide(state: running(1), shown: nil, canStart: true, leaving: true, now: now).action == .start(running(1)))
        // no clock: only a wait or leaving
        #expect(p.decide(state: AgentActivityState(phase: .running), shown: nil, canStart: true, now: now) == .init(.none))
        // not allowed (background, the user said no, dismissed this turn)
        #expect(p.decide(state: w, shown: nil, canStart: false, now: now) == .init(.none))
        #expect(p.decide(state: nil, shown: nil, canStart: true, now: now) == .init(.none))
    }

    @Test func followsAndEnds() {
        let r = running(60)
        let w = AgentActivityState(phase: .waiting, since: r.since, pending: 1)
        #expect(p.decide(state: r, shown: r, canStart: true, now: now) == .init(.none))
        #expect(p.decide(state: w, shown: r, canStart: false, now: now).action == .update(w))
        #expect(p.decide(state: nil, shown: w, canStart: false, now: now).action == .end(r.finished, dismissAt: now.addingTimeInterval(600)))
    }

    // MARK: what the card says

    @Test func display() {
        let local = AgentActivityAttributes(ws: "w", workspace: "Acme", session: "fix login", appWorkspace: "0f3a-1", sessionID: "s1:2")
        let r = AgentActivityDisplay(attributes: local, state: AgentActivityState(phase: .running, since: 1_790_000_000), stale: false)
        #expect(r.title == "fix login" && r.subtitle == "Acme" && r.status == "Working" && r.symbol == "sparkles" && r.badge == "")
        #expect(r.timerStart == Date(timeIntervalSince1970: 1_790_000_000))
        #expect(r.link == "xbin://0f3a-1/agent/s1:2")
        let w = AgentActivityDisplay(attributes: local, state: AgentActivityState(phase: .waiting, since: 1, pending: 3), stale: false)
        #expect(w.status == "3 waiting for you" && w.badge == "3" && w.symbol == "hand.raised.fill")
        let w1 = AgentActivityDisplay(attributes: local, state: AgentActivityState(phase: .waiting, since: 1, pending: 0), stale: false)
        #expect(w1.status == "Waiting for you" && w1.badge == "!")
        let done = AgentActivityDisplay(attributes: local, state: AgentActivityState(phase: .idle, since: 1), stale: true)
        #expect(done.status == "Finished" && done.timerStart == nil && !done.stale)
        let stale = AgentActivityDisplay(attributes: local, state: AgentActivityState(phase: .running, since: 1), stale: true)
        #expect(stale.status == "Status unknown" && stale.timerStart == nil && stale.stale)
        // push-started: names from the app's records, else generic; the
        // workspace link without a session
        let pushed = AgentActivityAttributes(ws: "w", ref: "r")
        let named = AgentActivityDisplay(attributes: pushed, state: AgentActivityState(phase: .running), stale: false,
                                         workspaceTitle: "Acme", appWorkspace: "0f3a-1")
        #expect(named.title == "Agent" && named.subtitle == "Acme" && named.link == "xbin://0f3a-1" && named.timerStart == nil)
        let bare = AgentActivityDisplay(attributes: pushed, state: AgentActivityState(phase: .running), stale: false)
        #expect(bare.subtitle == "xbin" && bare.link == nil)
        // a link never carries more than the two ids
        #expect(AgentActivityDisplay.link(appWorkspace: "a/b", session: "x#y") == "xbin://a%2Fb/agent/x%23y")
    }
}
