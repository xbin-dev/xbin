import Foundation
import Testing
@testable import XbinTerm

@Suite struct TermDirectoryTests {
    /// Captured from a real xbind (GET /api/xbin/term/sessions, one shell open).
    static let captured = #"""
[{"id":"766d985801b967a2","cwd":"apps/welcome","net":"internet","label":"internet","scopes":[{"id":"internet","label":"internet","desc":"public internet through the egress relay (no LAN)"},{"id":"host","label":"host net","desc":"the host's own network stack — LAN and host services, no relay, no metering"},{"id":"none","label":"offline","desc":"no network at all (xbind unreachable)"}],"gpu":"none","api":true,"name":"","created":"2026-09-26T07:51:01Z","lastActive":"2026-09-26T07:51:01Z","clients":1,"envHeld":false,"kind":"shell","vm":false}]
"""#

    @Test func decodesTheServersList() throws {
        let list = TermDirectory.decode(Data(Self.captured.utf8))
        #expect(list.count == 1)
        let s = try #require(list.first)
        #expect(s.cwd == "apps/welcome" && s.kind == .shell && s.net == "internet" && s.clients == 1)
        #expect(s.scopes.map(\.id) == ["internet", "host", "none"] && s.scopes[2].label == "offline")
        #expect(s.api && !s.vm && s.gpu == "none" && s.title == "shell")
        #expect(s.created == ISO8601DateFormatter().date(from: "2026-09-26T07:51:01Z"))
        #expect(!s.needsYou)
    }

    @Test func agentRowsAndTolerance() {
        let json = #"""
        [{"id":"a1","cwd":"apps/x","kind":"agent","provider":"claude","status":"waiting_permission","pending":2,"name":"fix bug",
          "lastActive":"2026-09-26T08:00:00Z","scopes":[{"id":"org"}]},
         {"id":"a2","cwd":"apps/x","kind":"agent","provider":"codex","status":"running","lastActive":"2026-09-26T09:00:00Z"},
         {"cwd":"no id"},
         {"id":"s1","cwd":"apps/x","kind":"shell","lastActive":"2026-09-26T07:00:00Z","api":"yes","clients":"3"},
         {"id":"s2","cwd":"apps/x","lastActive":"2026-09-26T10:00:00Z","future":{"x":1}}]
        """#
        let list = TermDirectory.decode(Data(json.utf8))
        #expect(list.map(\.id) == ["a1", "a2", "s1", "s2"])
        #expect(list[0].needsYou && list[0].title == "fix bug" && list[0].scopes == [TermScope(id: "org", label: "org")])
        #expect(list[1].isAgentBusy && !list[1].needsYou && list[1].title == "codex")
        #expect(list[2].api && list[2].clients == 0)                    // wrong types read as defaults
        #expect(TermDirectory.forTile(list, cwd: "apps/x").map(\.id) == ["s2", "s1", "a2", "a1"])
        #expect(TermDirectory.decode(Data("{}".utf8)).isEmpty)
    }

    /// A session waiting only on a question (an elicitation) needs the user
    /// too: the server's row carries `questions` (internal/term/sessions.go).
    @Test func questionsCountAsNeedingYou() throws {
        let json = #"""
        [{"id":"q1","cwd":"apps/x","kind":"agent","provider":"claude","status":"running","pending":0,"questions":1},
         {"id":"q2","cwd":"apps/x","kind":"agent","provider":"claude","status":"idle","questions":"2"},
         {"id":"p1","cwd":"apps/x","kind":"agent","status":"running","pending":2,"questions":1},
         {"id":"s1","cwd":"apps/x","kind":"shell","questions":4}]
        """#
        let list = TermDirectory.decode(Data(json.utf8))
        #expect(list.map(\.questions) == [1, 0, 1, 4])
        #expect(list[0].needsYou && list[0].waitingFor == "waiting: 1 question")
        #expect(!list[1].needsYou && list[1].waitingFor == "")          // a string isn't a count
        #expect(list[2].needsYou && list[2].waitingFor == "waiting: 2 permissions, 1 question")
        #expect(!list[3].needsYou)                                      // shells never wait on the user
        let waiting = TermDirectoryEntry(id: "w", cwd: "a", kind: .agent, status: "waiting_permission")
        #expect(waiting.needsYou && waiting.waitingFor == "waiting for you")
        #expect(TermDirectoryEntry(id: "q", cwd: "a", kind: .agent, questions: 3).waitingFor == "waiting: 3 questions")
    }

    /// `/ws/events` `term` `status` events update a row in place (the inbox
    /// follows them without re-listing); an unknown id means re-list.
    @Test func statusEventsApplyInPlace() throws {
        let list = [TermDirectoryEntry(id: "a", cwd: "apps/x", kind: .agent, status: "running"),
                    TermDirectoryEntry(id: "s", cwd: "apps/x")]
        let asked = try #require(TermDirectory.apply(statusOf: "a", status: "waiting_permission", pending: 0, questions: 1, to: list))
        #expect(asked[0].needsYou && asked[0].questions == 1 && asked[0].status == "waiting_permission")
        #expect(asked[1] == list[1])
        let answered = try #require(TermDirectory.apply(statusOf: "a", status: "running", pending: 0, questions: 0, to: asked))
        #expect(!answered[0].needsYou && answered[0].isAgentBusy)
        // Fields the event leaves out stay as they were; counts never go negative.
        let partial = try #require(TermDirectory.apply(statusOf: "a", status: nil, pending: -3, questions: nil, to: asked))
        #expect(partial[0].status == "waiting_permission" && partial[0].pending == 0 && partial[0].questions == 1)
        // A final status drops the row.
        #expect(TermDirectory.apply(statusOf: "a", status: "exited", pending: nil, questions: nil, to: list)?.map(\.id) == ["s"])
        #expect(TermDirectory.apply(statusOf: "a", status: "error", pending: nil, questions: nil, to: list)?.map(\.id) == ["s"])
        #expect(TermDirectory.apply(statusOf: "zz", status: "idle", pending: 0, questions: 0, to: list) == nil)
    }

    @Test func paths() throws {
        #expect(TermDirectory.listPath() == "/api/xbin/term/sessions")
        #expect(TermDirectory.listPath(cwd: "apps/a b") == "/api/xbin/term/sessions?cwd=apps%2Fa%20b")
        #expect(TermDirectory.sessionPath("x/y") == "/api/xbin/term/sessions/x%2Fy")
        let body = try JSONSerialization.jsonObject(with: TermDirectory.renameBody("  build  ")) as? [String: String]
        #expect(body == ["name": "build"])
    }

    @Test func pickers() {
        let info = TermSessionInfo(id: "s", net: "internet", label: "internet",
                                   scopes: [TermScope(id: "internet", label: "internet"), TermScope(id: "none", label: "offline")],
                                   netNote: "", baseOutdated: false, vm: false, echoAck: true)
        #expect(TermNetChoice.choices(info).map(\.current) == [true, false])
        #expect(TermNetChoice.choices(nil).isEmpty)
        let off = TermVMChoice(env: TermEnvState(vm: TermVMStatus(available: false, reason: "no qemu")), session: info)
        #expect(!off.available && off.note == "no qemu")
        let emu = TermVMChoice(env: TermEnvState(vm: TermVMStatus(available: true, emulated: true)), session: info)
        #expect(emu.available && emu.note.contains("Emulated"))
        var host = info
        host.net = "host"
        #expect(!TermVMChoice(env: TermEnvState(vm: TermVMStatus(available: true)), session: host).available)
    }
}
