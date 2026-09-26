import Foundation
import Testing
@testable import XbinAgent

@Suite struct ModelTests {
    @Test func providers() throws {
        let ps = try #require(fixture("basic")["providers"]?.list(AgentProvider.self))
        #expect(ps.map(\.id).prefix(4) == ["claude", "codex", "gemini", "opencode"])
        #expect(ps[0].modes.last?.explicit == true && ps[0].defaultMode == "default" && ps[0].login == "claude /login")
        #expect(ps.contains { $0.id == "fake" })
    }

    @Test func sessionAndSnapshot() throws {
        let f = try fixture("permissions")
        let s = try #require(f["session"].flatMap(SessionInfo.init(json:)))
        #expect(s.isAgent && s.provider == "fake" && s.status == .starting && s.cwd == "apps/x" && s.api)
        #expect(s.scopes.map(\.id) == ["internet", "host", "none"])
        #expect(s.createdDate != nil)
        let snap = try #require(f["snapshot"].flatMap(SessionSnapshot.init(json:)))
        #expect(snap.session.status == .waitingPermission && snap.session.pending == 1)
        #expect(snap.permissions.map(\.pid) == ["p1"])
        #expect(snap.permissions[0].request.rule == PermissionRule(kind: "execute", title: "run ls", scoped: true))
        let hs = try #require(f["historyList"]?.list(HistoryEntry.self))
        #expect(hs.count == 1 && hs[0].loadable && hs[0].turns == 4 && hs[0].preview == "perm one" && hs[0].name == "fake: perm one")
        let h = try #require(f["history"].flatMap(HistoryTranscript.init(json:)))
        #expect(h.meta.id == hs[0].id && h.events.count >= f["events"]?.array?.count ?? 0) // + the statuses of its end
    }

    @Test func openEnumsKeepUnknownValues() {
        #expect(SessionStatus(rawValue: "paused") == .other("paused") && SessionStatus(rawValue: "paused").rawValue == "paused")
        #expect(SessionStatus.waitingPermission.label == "waiting for you")
        #expect(ToolKind(rawValue: "browse").rawValue == "browse" && ToolKind(rawValue: "switch_mode") == .switchMode)
        #expect(StopReason(rawValue: "max_tokens").label == "hit the token limit")
        #expect(PermissionOptionKind(rawValue: "reject_always").isReject)
        #expect(FileChangeStatus(rawValue: "renamed").letter == "R")
    }

    @Test func requestBodies() {
        let c = CreateAgentSession(cwd: "apps/x", provider: "claude", mode: "plan", model: "opus", options: ["effort": "high"], net: "none", api: false, name: String(repeating: "n", count: 80), resume: "h1")
        #expect(c.body == j(#"{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"plan","model":"opus","options":{"effort":"high"},"net":"none","api":false,"name":"\#(String(repeating: "n", count: 64))","resume":"h1"}"#))
        #expect(CreateAgentSession(cwd: "a", provider: "p").body.compactString == #"{"cwd":"a","kind":"agent","provider":"p"}"#)
        #expect(RestartOptions(net: "internet", vm: true).body == j(#"{"net":"internet","vm":true}"#))
        #expect(PermissionAnswer.decision(.allowAlways).body == j(#"{"decision":"allow_always"}"#))
    }

    @Test func hubFrames() throws {
        let frame = j(#"{"type":"session","topic":"session.abc","component":"apps/x","data":{"seq":7,"ts":1789,"type":"message.delta","data":{"role":"agent","text":"hi"},"user":"alice","id":"abc"}}"#)
        let h = try #require(SessionHubEvent(json: frame))
        #expect(h.sessionID == "abc" && h.user == "alice" && h.component == "apps/x" && h.event.seq == 7)
        #expect(h.event.payload == .messageDelta(MessageDelta(role: .agent, text: "hi")))
        #expect(SessionHubEvent(json: j(#"{"type":"term","data":{"op":"open"}}"#)) == nil)
    }

    @Test func everyEventTypeReads() throws {
        var seen = Set<String>()
        for name in allFixtures {
            for e in try events(name) {
                seen.insert(e.type)
                if case .unknown = e.payload { Issue.record("\(name) seq \(e.seq): \(e.type) did not read") }
            }
        }
        // the fake sends no plan (TranscriptTests covers it); gap is a follow stream's
        #expect(seen == Set(AgentEventType.allCases.map(\.rawValue)).subtracting(["gap", "plan"]))
    }
}
