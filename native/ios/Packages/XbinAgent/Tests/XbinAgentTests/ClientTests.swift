import Foundation
import Testing
@testable import XbinAgent

@Suite struct ClientTests {
    @Test func routesAndBodies() async throws {
        let f = try fixture("permissions")
        let session = f["session"]!
        let t = FakeTransport { r in
            switch (r.method, r.path) {
            case ("GET", "/api/xbin/agent/providers"): return ok(try! fixture("basic")["providers"]!)
            case ("POST", "/api/xbin/term/sessions"): return ok(session)
            case ("GET", "/api/xbin/term/sessions"): return ok(.array([session]))
            case ("GET", "/api/xbin/term/sessions/s%201"): return ok(f["snapshot"]!)
            case ("POST", "/api/xbin/term/sessions/s%201/prompt"): return ok(#"{"ok":true,"turn":2}"#)
            case ("POST", "/api/xbin/term/sessions/s%201/restart"): return ok(["session": session, "resumed": true])
            case ("GET", "/api/xbin/term/sessions/s%201/log"): return HTTPResponse(status: 200, body: Data("fakeacp: up\n".utf8))
            case ("GET", "/api/xbin/agent/history"): return ok(f["historyList"]!)
            case ("GET", "/api/xbin/agent/history/h1/events"): return ok(f["history"]!)
            case ("DELETE", _): return HTTPResponse(status: 204)
            default: return ok("{\"ok\":true}")
            }
        }
        let c = AgentClient(transport: t)
        #expect(try await c.providers().first?.id == "claude")
        let s = try await c.create(CreateAgentSession(cwd: "apps/x", provider: "fake", mode: "ask"))
        #expect(s.provider == "fake")
        #expect(try await c.sessions(cwd: "apps/x").count == 1)
        #expect(try await c.session("s 1").permissions.count == 1)
        #expect(try await c.prompt("s 1", text: "/review tests").turn == 2)
        try await c.cancel("s 1")
        try await c.answerPermission("s 1", pid: "p1", .option("once"))
        try await c.answerPermission("s 1", pid: "p2", .decision(.rejectOnce))
        try await c.answerQuestion("s 1", eid: "e1", action: .accept, content: ["question_0": "SQLite"])
        try await c.answerQuestion("s 1", eid: "e2", action: .decline)
        try await c.setOption("s 1", option: "model", value: "fake-fast")
        #expect(try await c.restart("s 1", RestartOptions(net: "none")).resumed)
        try await c.rename("s 1", name: "mine")
        try await c.end("s 1")
        #expect(try await c.log("s 1") == "fakeacp: up\n")
        #expect(try await c.history(cwd: "apps/x").first?.loadable == true)
        #expect(try await c.historyTranscript("h1").events.count > 10)
        try await c.deleteHistory("h1")

        let rs = t.requests.map { "\($0.method) \($0.target)" }
        #expect(rs == [
            "GET /api/xbin/agent/providers",
            "POST /api/xbin/term/sessions",
            "GET /api/xbin/term/sessions?cwd=apps/x",
            "GET /api/xbin/term/sessions/s%201",
            "POST /api/xbin/term/sessions/s%201/prompt",
            "POST /api/xbin/term/sessions/s%201/cancel",
            "POST /api/xbin/term/sessions/s%201/permissions/p1",
            "POST /api/xbin/term/sessions/s%201/permissions/p2",
            "POST /api/xbin/term/sessions/s%201/elicitations/e1",
            "POST /api/xbin/term/sessions/s%201/elicitations/e2",
            "POST /api/xbin/term/sessions/s%201/options",
            "POST /api/xbin/term/sessions/s%201/restart",
            "PATCH /api/xbin/term/sessions/s%201",
            "DELETE /api/xbin/term/sessions/s%201",
            "GET /api/xbin/term/sessions/s%201/log",
            "GET /api/xbin/agent/history?cwd=apps/x",
            "GET /api/xbin/agent/history/h1/events",
            "DELETE /api/xbin/agent/history/h1",
        ])
        let bodies = t.requests.map { $0.json?.compactString ?? "-" }
        #expect(bodies[1] == #"{"cwd":"apps/x","kind":"agent","provider":"fake","mode":"ask"}"#)
        #expect(bodies[4] == #"{"text":"/review tests"}"#)
        #expect(bodies[6] == #"{"optionId":"once"}"# && bodies[7] == #"{"decision":"reject_once"}"#)
        #expect(bodies[8] == #"{"action":"accept","content":{"question_0":"SQLite"}}"# && bodies[9] == #"{"action":"decline"}"#)
        #expect(bodies[10] == #"{"id":"model","value":"fake-fast"}"#)
        #expect(bodies[11] == #"{"net":"none"}"# && bodies[12] == #"{"name":"mine"}"#)
        #expect(t.requests[4].headers["Content-Type"] == "application/json")
    }

    @Test func refusals() async throws {
        let t = FakeTransport { r in
            if r.path.hasSuffix("/prompt") { return refuse(409, "a turn is running — cancel it or wait for turn.end") }
            if r.path.hasSuffix("/permissions/p1") { return refuse(404, "no such pending permission") }
            if r.path.hasSuffix("/term/sessions") { return refuse(503, "Please run /login first") }
            return HTTPResponse(status: 502, body: Data("bad gateway".utf8))
        }
        let c = AgentClient(transport: t)
        await #expect(throws: AgentAPIError(status: 409, message: "a turn is running — cancel it or wait for turn.end")) {
            _ = try await c.prompt("s", text: "hi")
        }
        do {
            try await c.answerPermission("s", pid: "p1", .option("x"))
            Issue.record("answered")
        } catch let e as AgentAPIError {
            #expect(e.isNotFound)
        }
        do {
            _ = try await c.create(CreateAgentSession(cwd: "a", provider: "claude"))
        } catch let e as AgentAPIError {
            #expect(e.status == 503 && !e.looksLikeAuth) // "/login" alone is not the web's auth pattern
        }
        #expect(AgentAPIError.looksLikeAuth("Authentication required") && AgentAPIError.looksLikeAuth("please sign in") && AgentAPIError.looksLikeAuth("rpc -32000"))
        await #expect(throws: AgentAPIError(status: 502, message: "bad gateway")) { try await c.cancel("s") }
    }

    @Test func eventsAndFollow() async throws {
        let evs = try events("basic")
        let t = FakeTransport { r in page(Array(evs.drop { $0.seq <= UInt64(r.q("since") ?? "0")! })) }
        let c = AgentClient(transport: t)
        let p = try await c.events("s", since: 20)
        #expect(p.events.first?.seq == 21 && p.next == evs.last?.seq && !p.truncated)
        // the NDJSON follow: a gap line, then events split across chunks at odd places
        var nd = Data(#"{"seq":0,"ts":0,"type":"gap","data":{"before":3}}"#.utf8 + [0x0A])
        for e in evs.suffix(4) { nd.append(e.data.isNull ? Data() : e.json.data); nd.append(0x0A) }
        let bytes = nd
        t.queueStream { r in
            #expect(r.q("follow") == "1" && r.q("since") == "3")
            return stride(from: 0, to: bytes.count, by: 37).map { bytes[$0..<min($0 + 37, bytes.count)] }
        }
        var got: [AgentEvent] = []
        for try await e in try await c.follow("s", since: 3) { got.append(e) }
        #expect(got.map(\.type) == ["gap"] + evs.suffix(4).map(\.type))
        #expect(got.dropFirst().map(\.seq) == evs.suffix(4).map(\.seq))
        #expect(got.last == evs.last)
    }
}
