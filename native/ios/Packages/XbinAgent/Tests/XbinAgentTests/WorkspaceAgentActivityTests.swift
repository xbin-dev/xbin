import Foundation
import Testing
@testable import XbinAgent

// The workspace agent's card (D241; WorkspaceAgentActivity.swift).

@Suite struct WorkspaceAgentActivityTests {
    @Test func stateDecodesTheRelaysJSON() throws {
        let relay = #"{"phase":"waiting","since":1790000000,"pending":2,"agents":3}"#
        let s = try JSONDecoder().decode(WorkspaceAgentActivityState.self, from: Data(relay.utf8))
        #expect(s == WorkspaceAgentActivityState(phase: .waiting, since: 1_790_000_000, pending: 2, agents: 3))
        // an older relay's three fields; an unknown phase; out of range
        let old = try JSONDecoder().decode(WorkspaceAgentActivityState.self, from: Data(#"{"phase":"paused","since":1,"pending":0}"#.utf8))
        #expect(old.phase == .paused && old.agents == 0)
        let odd = try JSONDecoder().decode(WorkspaceAgentActivityState.self, from: Data(#"{"phase":"compiling","pending":500,"agents":-3}"#.utf8))
        #expect(odd == WorkspaceAgentActivityState(phase: .running, pending: 99, agents: 0))
        let keys = try JSONSerialization.jsonObject(with: JSONEncoder().encode(s)) as? [String: Any]
        #expect(Set(keys?.keys ?? [:].keys) == ["phase", "since", "pending", "agents"])
        // a push-to-start's attributes: ws and ref only
        let a = try JSONDecoder().decode(WorkspaceAgentActivityAttributes.self, from: Data(#"{"ws":"w1","ref":"r1","workspace":"","appWorkspace":""}"#.utf8))
        #expect(a.pushStarted && a.workspace.isEmpty)
    }

    /// Pushed or local, the state is numbers and enumerated words only.
    @Test func stateHoldsNoText() throws {
        let s = WorkspaceAgentActivityState(phase: .waiting, since: 5, pending: 1, agents: 2)
        let o = try JSONSerialization.jsonObject(with: JSONEncoder().encode(s)) as? [String: Any] ?? [:]
        for (k, v) in o where k != "phase" { #expect(v is NSNumber, "\(k)") }
        #expect(WorkspaceAgentPhase.allCases.map(\.rawValue).contains(o["phase"] as? String ?? ""))
    }

    private let policy = WorkspaceAgentActivityPolicy()
    private let t0 = Date(timeIntervalSince1970: 1_790_000_000)

    @Test func startsAfterWorkOrAtOnceWhenSomethingWaits() {
        let working = WorkspaceAgentPicture(has: true, agents: 2)
        var d = policy.decide(picture: working, shown: nil, stretchSince: t0, idleSince: nil, canStart: true, now: t0.addingTimeInterval(3))
        #expect(d.action == .none && d.recheckAt == t0.addingTimeInterval(10))
        d = policy.decide(picture: working, shown: nil, stretchSince: t0, idleSince: nil, canStart: true, now: t0.addingTimeInterval(10))
        #expect(d.action == .start(WorkspaceAgentActivityState(phase: .running, since: 1_790_000_000, agents: 2)))
        let waits = WorkspaceAgentPicture(has: true, pending: 1)
        d = policy.decide(picture: waits, shown: nil, stretchSince: t0, idleSince: nil, canStart: true, now: t0)
        #expect(d.action == .start(WorkspaceAgentActivityState(phase: .waiting, since: 1_790_000_000, pending: 1)))
        d = policy.decide(picture: working, shown: nil, stretchSince: t0, idleSince: nil, canStart: true, leaving: true, now: t0)
        if case .start = d.action {} else { Issue.record("leaving the front mid-work starts it: \(d)") }
        d = policy.decide(picture: waits, shown: nil, stretchSince: t0, idleSince: nil, canStart: false, now: t0)
        #expect(d.action == .none, "a card swiped away, or not allowed")
    }

    @Test func followsAndEnds() {
        let shown = WorkspaceAgentActivityState(phase: .running, since: 1_790_000_000, agents: 2)
        var d = policy.decide(picture: WorkspaceAgentPicture(has: true, pending: 2, agents: 1), shown: shown, stretchSince: t0, idleSince: nil, canStart: true, now: t0)
        #expect(d.action == .update(WorkspaceAgentActivityState(phase: .waiting, since: 1_790_000_000, pending: 2, agents: 1)))
        // nothing works or waits: idle now, the end after the grace
        let idle = WorkspaceAgentPicture(has: true)
        d = policy.decide(picture: idle, shown: shown, stretchSince: t0, idleSince: t0, canStart: true, now: t0.addingTimeInterval(5))
        #expect(d.action == .update(WorkspaceAgentActivityState(phase: .idle, since: 1_790_000_000)) && d.recheckAt == t0.addingTimeInterval(120))
        d = policy.decide(picture: idle, shown: WorkspaceAgentActivityState(phase: .idle, since: 1_790_000_000), stretchSince: t0, idleSince: t0,
                          canStart: true, now: t0.addingTimeInterval(121))
        #expect(d.action == .end(WorkspaceAgentActivityState(phase: .idle, since: 1_790_000_000), dismissAt: t0.addingTimeInterval(421)))
        // paused: at once, on paused
        d = policy.decide(picture: WorkspaceAgentPicture(has: true, paused: true, pending: 1), shown: shown, stretchSince: t0, idleSince: nil, canStart: true, now: t0)
        #expect(d.action == .end(WorkspaceAgentActivityState(phase: .paused, since: 1_790_000_000, pending: 1), dismissAt: t0.addingTimeInterval(60)))
        // no agent that may act (off, taken away, held): at once
        d = policy.decide(picture: WorkspaceAgentPicture(has: false), shown: shown, stretchSince: t0, idleSince: nil, canStart: true, now: t0)
        #expect(d.action == .end(WorkspaceAgentActivityState(phase: .idle, since: 1_790_000_000), dismissAt: t0))
        #expect(policy.decide(picture: WorkspaceAgentPicture(has: false), shown: nil, stretchSince: nil, idleSince: nil, canStart: true, now: t0).action == .none)
    }

    @Test func display() {
        let a = WorkspaceAgentActivityAttributes(ws: "w", workspace: "Northwind", appWorkspace: "app-1")
        var d = WorkspaceAgentActivityDisplay(attributes: a, state: WorkspaceAgentActivityState(phase: .running, since: 9, agents: 3), stale: false)
        #expect(d.title == "Workspace agent" && d.subtitle == "Northwind" && d.status == "Working" && d.detail == "3 tile agents working")
        #expect(d.badge == "3" && d.link == "xbin://app-1/xbin/agent" && d.reviewLabel == nil && d.timerStart != nil)
        d = WorkspaceAgentActivityDisplay(attributes: a, state: WorkspaceAgentActivityState(phase: .waiting, since: 9, pending: 2, agents: 1), stale: false)
        #expect(d.status == "Needs you (2)" && d.detail == "1 tile agent working" && d.badge == "2" && d.link == "xbin://app-1/xbin/agent#first" && d.reviewLabel == "Review")
        d = WorkspaceAgentActivityDisplay(attributes: a, state: WorkspaceAgentActivityState(phase: .paused, since: 9), stale: true)
        #expect(d.status == "Paused" && !d.stale && d.timerStart == nil)
        d = WorkspaceAgentActivityDisplay(attributes: a, state: WorkspaceAgentActivityState(phase: .running, since: 9), stale: true)
        #expect(d.status == "Status unknown" && d.timerStart == nil)
        // a push-started card: the names from the app's own records
        let pushed = WorkspaceAgentActivityAttributes(ws: "w", ref: "r")
        d = WorkspaceAgentActivityDisplay(attributes: pushed, state: WorkspaceAgentActivityState(phase: .idle), stale: false, workspaceTitle: "Acme", appWorkspace: "app-2")
        #expect(d.subtitle == "Acme" && d.status == "Done" && d.link == "xbin://app-2/xbin/agent")
        #expect(WorkspaceAgentActivityDisplay(attributes: pushed, state: WorkspaceAgentActivityState(phase: .idle), stale: false).link == nil)
    }
}
