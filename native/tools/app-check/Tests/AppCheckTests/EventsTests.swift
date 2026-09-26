import Foundation
import Testing
import XbinAgent
import XbinCore

// The app's events socket driver (native/ios/App/Model/WorkspaceEvents.swift,
// symlinked here) with a scripted socket: connecting with the workspace
// session, reconnecting, re-signing on a 401, and routing frames to the
// screens that follow them. URLSessionWebSocketTask can't run on Linux
// (libcurl here has no WebSocket support); the frames are the shapes
// docs/protocol.md documents, the session ones from XbinAgent's captured
// sessions.

@MainActor
final class ScriptedSockets {
    final class Socket: EventSocket {
        let url: URL
        let bearer: String
        let signal: @MainActor (EventSocketSignal) -> Void
        var closed = false
        init(url: URL, bearer: String, signal: @escaping @MainActor (EventSocketSignal) -> Void) {
            self.url = url
            self.bearer = bearer
            self.signal = signal
        }
        func close() { closed = true }
    }

    var opened: [Socket] = []
    var last: Socket? { opened.last }

    var factory: EventSocketFactory {
        { [unowned self] url, bearer, signal in
            let s = Socket(url: url, bearer: bearer, signal: signal)
            self.opened.append(s)
            return s
        }
    }
}

/// Only what the driver asks of a workspace: nothing (it never sends HTTP
/// itself — the session comes from the auth actor's store).
struct NoHTTP: APITransport {
    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        APIResponse(status: 404)
    }
}

actor Delays {
    var values: [Double] = []
    func add(_ d: Double) { values.append(d) }
}

@MainActor
func until(_ what: String, _ cond: @MainActor () -> Bool) async {
    for _ in 0..<500 {
        if cond() { return }
        try? await Task.sleep(nanoseconds: 2_000_000)
    }
    Issue.record("timed out waiting for \(what)")
}

@MainActor
func makeEvents(token: String = "tok-1") async -> (WorkspaceEvents, ScriptedSockets, WorkspaceAuth, Delays) {
    let origin = try! ServerOrigin(string: "https://ws.example.com")
    let record = WorkspaceRecord(id: "w1", server: origin, user: WorkspaceUser(id: "alice"))
    let auth = WorkspaceAuth(record: record, transport: NoHTTP(), keys: NoKeys(), sessions: MemorySessionStore(),
                             clientHeader: "app/test")
    await auth.adopt(SessionCredential(token: token, kind: .token, userID: "alice"))
    let sockets = ScriptedSockets()
    let delays = Delays()
    let events = WorkspaceEvents(auth: auth, makeSocket: sockets.factory, sleep: { d in await delays.add(d) }, jitter: { 1 })
    events.userID = { "alice" }
    return (events, sockets, auth, delays)
}

struct NoKeys: DeviceKeyStore {
    func createKey(workspace: String) async throws -> Data { throw SignInError.keyUnavailable("test") }
    func hasKey(workspace: String) async -> Bool { false }
    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data { throw SignInError.keyUnavailable("test") }
    func deleteKey(workspace: String) async {}
}

@Suite @MainActor struct EventsDriverTests {
    @Test func connectsWithTheWorkspaceSessionOnlyWhileWanted() async {
        let (events, sockets, _, _) = await makeEvents()
        #expect(sockets.opened.isEmpty)
        events.setWanted(true)
        await until("a socket") { sockets.opened.count == 1 }
        let s = sockets.opened[0]
        #expect(s.url.absoluteString == "wss://ws.example.com/ws/events")
        #expect(s.bearer == "tok-1")
        s.signal(.opened)
        #expect(events.isConnected)
        events.setWanted(false)
        #expect(s.closed && !events.isConnected)
        // A late signal from the closed socket changes nothing.
        s.signal(.closed(.dropped))
        try? await Task.sleep(nanoseconds: 20_000_000)
        #expect(sockets.opened.count == 1)
    }

    @Test func reconnectsWithBackoffAndResyncsAfterAGap() async {
        let (events, sockets, _, delays) = await makeEvents()
        var resyncs = 0
        events.onResync = { resyncs += 1 }
        events.setWanted(true)
        await until("first socket") { sockets.opened.count == 1 }
        sockets.opened[0].signal(.opened)
        #expect(resyncs == 0)                       // the first socket: nothing missed
        sockets.opened[0].signal(.closed(.dropped))
        await until("second socket") { sockets.opened.count == 2 }
        sockets.opened[1].signal(.closed(.unreachable))
        await until("third socket") { sockets.opened.count == 3 }
        sockets.opened[2].signal(.opened)
        #expect(resyncs == 1)                       // a reopen: re-list, catch up
        #expect(await delays.values == [0.5, 1])
        events.setWanted(false)
    }

    /// A 401 on the upgrade: one re-sign, then connect with the new bearer.
    @Test func reauthenticatesOnce() async {
        let (events, sockets, auth, _) = await makeEvents(token: "old")
        events.setWanted(true)
        await until("first socket") { sockets.opened.count == 1 }
        // Someone else re-signed meanwhile (the workspace's refresh): the
        // driver's re-sign finds the new session.
        await auth.adopt(SessionCredential(token: "new", kind: .token, userID: "alice"))
        sockets.opened[0].signal(.closed(.refused(status: 401)))
        await until("second socket") { sockets.opened.count == 2 }
        #expect(sockets.opened[1].bearer == "new")
        // A second 401 in a row: a token session can't re-sign — parked.
        sockets.opened[1].signal(.closed(.refused(status: 401)))
        await until("parked") { events.policy.parked }
        try? await Task.sleep(nanoseconds: 20_000_000)
        #expect(sockets.opened.count == 2)
        // The next foreground tries again.
        events.setWanted(false)
        events.setWanted(true)
        await until("third socket") { sockets.opened.count == 3 }
        events.setWanted(false)
    }

    @Test func noSessionParks() async {
        let (events, sockets, auth, _) = await makeEvents()
        await auth.adopt(nil)
        events.setWanted(true)
        await until("parked") { events.policy.parked }
        #expect(sockets.opened.isEmpty)
    }

    /// The web shell's rule: the most specific open tile reloads; a grant
    /// change reloads the tile it names.
    @Test func reloadsTheMostSpecificOpenTile() async {
        let (events, _, _, _) = await makeEvents()
        var counts: [String: Int] = [:]
        var tasks: [Task<Void, Never>] = []
        for tile in ["apps/calendar", "apps/calendar/widgets", "notes"] {
            let stream = events.reloads(of: tile)
            tasks.append(Task { @MainActor in for await _ in stream { counts[tile, default: 0] += 1 } })
        }
        // A second screen on the same tile (another window) reloads too.
        tasks.append(Task { @MainActor in await events.onReload(of: "notes") { counts["notes#2", default: 0] += 1 } })
        await until("four followers") { events.followedTiles.count == 4 }
        events.receive(#"{"type":"reload","component":"apps/calendar/widgets/week"}"#)
        events.receive(#"{"type":"reload","component":"apps/calendar/backend"}"#)
        events.receive(#"{"type":"reload","component":"apps/other"}"#)
        await until("reloads") { counts["apps/calendar/widgets"] == 1 && counts["apps/calendar"] == 1 }
        events.receive(#"{"type":"reload","component":"notes"}"#)
        events.receive(#"{"type":"grants","component":"apps/calendar"}"#)
        events.receive(#"{"type":"grants"}"#)
        await until("notes") { counts["notes"] == 1 && counts["notes#2"] == 1 && counts["apps/calendar"] == 2 }
        #expect(counts["apps/calendar/widgets"] == 1)
        // A screen that goes away stops following.
        tasks.forEach { $0.cancel() }
        await until("unsubscribed") { events.followedTiles.isEmpty }
    }

    @Test func termEventsOfThisUserOnly() async {
        let (events, _, _, _) = await makeEvents()
        var seen: [TermEvent] = []
        var branding = 0
        events.onTerm = { seen.append($0) }
        events.onBranding = { branding += 1 }
        var streamed: [String] = []
        let stream = events.termEvents()
        let follower = Task { @MainActor in for await t in stream { streamed.append(t.id) } }
        events.receive(#"{"type":"term","component":"apps/x","data":{"op":"status","id":"a","user":"alice","status":"waiting_permission","pending":0,"questions":1,"turn":2}}"#)
        events.receive(#"{"type":"term","component":"apps/x","data":{"op":"open","id":"b","user":"bob"}}"#) // an admin sees bob's too
        events.receive(#"{"type":"term","component":"apps/x","data":{"op":"close","id":"c"}}"#)
        events.receive(#"{"type":"branding"}"#)
        events.receive("not json")
        #expect(seen.map(\.id) == ["a", "c"])
        #expect(seen.first?.questions == 1)
        #expect(branding == 1)
        await until("streamed") { streamed == ["a", "c"] }
        follower.cancel()
    }

    /// An admin's workspace switch (`native`, plat's PUT
    /// /api/xbin/native-runtime) reaches the workspace's hook, which
    /// re-reads whoami; nothing else fires for it.
    @Test func nativeSwitchReachesItsHook() async {
        let (events, _, _, _) = await makeEvents()
        var switches = 0
        var others = 0
        events.onNativeSwitch = { switches += 1 }
        events.onBranding = { others += 1 }
        events.onTerm = { _ in others += 1 }
        events.receive("{\"type\":\"native\"}\n") // as xbind writes it
        events.receive(#"{"type":"native","component":""}"#)
        #expect(switches == 2 && others == 0)
    }

    /// `session` frames reach the open screen's feed; a reconnect makes it
    /// catch up. Frames are XbinAgent's captured `ask` session wrapped as
    /// the hub sends them.
    @Test func sessionFramesReachTheFeed() async throws {
        let (events, sockets, _, _) = await makeEvents()
        let fixture = try XbinAgent.JSONValue.parse(Data(contentsOf: repo.appendingPathComponent(
            "native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures/ask.json")))
        let id = try #require(fixture["session"]?["id"]?.string)
        let logged = try #require(fixture["events"]?.array)
        #expect(logged.count > 4)
        let agent = ScriptedAgent()
        let feed = AgentSessionFeed(client: AgentClient(transport: agent), sessionID: id)
        let delivery = events.deliver(to: feed)
        try? await Task.sleep(nanoseconds: 10_000_000)
        for e in logged.prefix(4) {
            let frame: XbinAgent.JSONValue = ["type": "session", "topic": .string("session." + id), "component": "apps/x",
                                              "data": Self.withID(e, id)]
            events.receive(frame.compactString + "\n") // as xbind writes them
        }
        // Another session's frame is not this feed's.
        events.receive(#"{"type":"session","topic":"session.zzz","data":{"seq":99,"ts":1,"type":"status","data":{"status":"idle"},"id":"zzz"}}"#)
        var last: UInt64 = 0
        for _ in 0..<500 {
            last = await feed.transcript.lastSeq
            if last == 4 { break }
            try? await Task.sleep(nanoseconds: 2_000_000)
        }
        #expect(last == 4)
        // A reopen after a gap: the feed re-reads the log since its cursor.
        events.setWanted(true)
        await until("socket") { sockets.opened.count == 1 }
        sockets.opened[0].signal(.opened)
        sockets.opened[0].signal(.closed(.dropped))
        await until("socket 2") { sockets.opened.count == 2 }
        sockets.opened[1].signal(.opened)
        for _ in 0..<500 {
            if await agent.catchUps.contains("since=4") { break }
            try? await Task.sleep(nanoseconds: 2_000_000)
        }
        #expect(await agent.catchUps.contains("since=4"))
        delivery.cancel()
        events.setWanted(false)
    }

    static func withID(_ e: XbinAgent.JSONValue, _ id: String) -> XbinAgent.JSONValue {
        guard case .object(var o) = e else { return e }
        o["id"] = .string(id)
        o["user"] = "alice"
        return .object(o)
    }
}

/// The agent routes a feed calls on a catch-up: an empty page.
actor ScriptedAgent: AgentTransport {
    var catchUps: [String] = []

    nonisolated func send(_ request: HTTPRequest) async throws -> HTTPResponse {
        await record(request)
        return HTTPResponse(status: 200, body: Data(#"{"events":[],"next":0}"#.utf8))
    }

    nonisolated func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> {
        throw AgentAPIError(status: 404, message: "not in this test")
    }

    private func record(_ r: HTTPRequest) {
        if r.path.hasSuffix("/events") { catchUps.append(r.query.map { "\($0.0)=\($0.1)" }.joined(separator: "&")) }
    }
}
