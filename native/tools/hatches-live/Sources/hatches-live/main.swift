// hatches-live — a native tile's escape hatches and the Agent tab's prompt
// attachments against a real xbind, minus the UI (which only Xcode builds):
//
//   - the composer's upload: TileResource.apiPath (tile-relative, {name}),
//     PUT with the tile's frame token only → the backend sees the tile and
//     its user; without the token → refused;
//   - a `terminal` element: TileResource.socketURL (?frame=) through
//     xbind's proxy to the tile's own pty socket, driven by XbinTerm's
//     TilePtySession (size first, bytes both ways, resize, exit; no token or
//     a dead one → unauthorized) — a raw RFC 6455 client stands in for
//     URLSessionWebSocketTask (this box's libcurl has no WebSockets);
//   - AgentClient.prompt(_:text:attachments:) against hack/fakeacp: the log
//     lists the files, the model gets the image inline, 11 files are refused
//     before any request.
//
//   go build -o bin/xbind ./cmd/xbind && CGO_ENABLED=0 go build -o bin/bx ./cmd/bx && go build -o bin/fakeacp ./hack/fakeacp
//   bin/xbind init /tmp/hws && cp -R native/tools/hatches-live/tile /tmp/hws/apps/ptytest
//   XBIN_AGENT_FAKE=$PWD/bin/fakeacp XBIN_BIN=$PWD/bin XBIN_SDK_PATH=$PWD/sdk \
//     bin/xbind --dev --workspace /tmp/hws --listen 127.0.0.1:9731 --external-url http://127.0.0.1:9731 &
//   cd native/tools/hatches-live && swift run hatches-live 127.0.0.1:9731
//
// (--dev seeds admin/admin.) Exit 0 and "LIVE OK" when every check passes.
// Not run by CI — it needs a running xbind.
import Foundation
import FoundationNetworking
import XbinAgent
import XbinCore
import XbinTerm

let host = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "127.0.0.1:9731"
let origin = try ServerOrigin(string: "http://" + host)
let tile = "apps/ptytest"
nonisolated(unsafe) var failures = 0
func check(_ ok: Bool, _ what: String) {
    print((ok ? "  ok   " : "  FAIL ") + what)
    if !ok { failures += 1 }
}

final class NoRedirect: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}
let cfg = URLSessionConfiguration.ephemeral
cfg.httpShouldSetCookies = false
cfg.httpCookieAcceptPolicy = .never
let http = URLSession(configuration: cfg, delegate: NoRedirect(), delegateQueue: nil)

func send(_ method: String, _ path: String, headers: [String: String] = [:], body: Data? = nil) async throws -> (Int, Data, [AnyHashable: Any]) {
    var r = URLRequest(url: origin.url(path: path)!)
    r.httpMethod = method
    for (k, v) in headers { r.setValue(v, forHTTPHeaderField: k) }
    r.httpBody = body
    let (d, resp) = try await http.data(for: r)
    let h = resp as! HTTPURLResponse
    return (h.statusCode, d, h.allHeaderFields)
}

// 1. sign in (cookie), mint the tile's frame token
let (ls, _, lh) = try await send("POST", "/login", headers: ["Content-Type": "application/x-www-form-urlencoded"], body: Data("username=admin&password=admin".utf8))
let setCookie = (lh["Set-Cookie"] as? String) ?? ""
let cookie = setCookie.split(separator: ";").first.map(String.init) ?? ""
check(ls == 302 && !cookie.isEmpty, "password sign-in → a session cookie (\(ls))")
let (fs, fb, _) = try await send("GET", FrameTokenRoute.path(component: tile), headers: ["Cookie": cookie])
let token = (try? JSONValue(parsing: fb)).flatMap(FrameTokenRoute.token(from:)) ?? ""
check(fs == 200 && !token.isEmpty, "frame token for \(tile)")

// 2. the composer's upload: tile-relative path, {name}, frame token only
let name = TileResource.fileName("/var/mobile/tmp/My photo.png", fallback: "file")
let up = TileResource.apiPath("upload?name={name}", tile: tile, name: name)!
check(up == "/api/apps/ptytest/upload?name=My%20photo.png", "upload path resolved: \(up)")
let png = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A] + [UInt8](repeating: 7, count: 100))
let (us, ub, _) = try await send("PUT", up, headers: [TileScheme.frameTokenHeader: token, TileScheme.clientHeader: "app/hatches-live",
                                                    "Content-Type": "image/png"], body: png)
let resp = TileUpload.response(body: ub)
check(us == 200 && resp["bytes"]?.intValue == 108 && resp["path"]?.stringValue == "uploads/My photo.png"
      && resp["from"]?.stringValue == tile && resp["user"]?.stringValue == "admin", "upload as the tile: \(resp.jsonString)")
let (bad, _, _) = try await send("PUT", up, headers: ["Content-Type": "image/png"], body: png)
check(bad == 401 || bad == 403, "the same upload without the frame token is refused (\(bad))")
check(TileResource.apiPath("/api/xbin/whoami", tile: tile) == nil, "xbind's API is never an upload target")

// 3. the tile pty socket
@MainActor final class Screen: TermEmulator {
    var out = ""
    func write(_ bytes: [UInt8]) { out += String(decoding: bytes, as: UTF8.self) }
    func afterParsed(_ body: @escaping @MainActor () -> Void) { body() }
    func reset() { out = "" }
    var framebuffer: (any TermFramebuffer)? { nil }
    var size: TermSize { TermSize(cols: 80, rows: 24) }
}
@MainActor final class Phases: TilePtyDelegate {
    var all: [TilePtySession.Phase] = []
    func tilePty(_ p: TilePtySession, phaseChanged phase: TilePtySession.Phase) { all.append(phase) }
}
import Glibc
/// A minimal RFC 6455 client over a blocking TCP socket (libcurl here lacks
/// WebSockets): the upgrade's HTTP status on refusal, masked client frames,
/// unfragmented server frames (what gorilla/websocket writes for us).
final class RawWS: @unchecked Sendable {
    let fd: Int32
    var buf: [UInt8] = []
    init?(host: String, port: UInt16) {
        fd = socket(AF_INET, Int32(SOCK_STREAM.rawValue), 0)
        var a = sockaddr_in(); a.sin_family = sa_family_t(AF_INET); a.sin_port = port.bigEndian
        inet_pton(AF_INET, host, &a.sin_addr)
        let r = withUnsafePointer(to: &a) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) } }
        if r != 0 { return nil }
    }
    func write(_ b: [UInt8]) { var off = 0; while off < b.count { let n = b[off...].withUnsafeBytes { Glibc.write(fd, $0.baseAddress, $0.count) }; if n <= 0 { return }; off += n } }
    func fill() -> Bool { var tmp = [UInt8](repeating: 0, count: 65536); let n = read(fd, &tmp, tmp.count); if n <= 0 { return false }; buf += tmp[0..<n]; return true }
    func take(_ n: Int) -> [UInt8]? { while buf.count < n { if !fill() { return nil } }; let r = Array(buf[0..<n]); buf.removeFirst(n); return r }
    /// Sends the upgrade; returns the HTTP status and body (body only on refusal).
    func handshake(host: String, path: String) -> (Int, String) {
        write(Array("GET \(path) HTTP/1.1\r\nHost: \(host)\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n".utf8))
        while true {
            if let r = buf.firstRange(of: Array("\r\n\r\n".utf8)) {
                let head = String(decoding: buf[0..<r.lowerBound], as: UTF8.self)
                buf.removeFirst(r.upperBound)
                let status = Int(head.split(separator: " ").dropFirst().first ?? "") ?? -1
                if status != 101 { return (status, String(decoding: buf, as: UTF8.self)) }
                return (101, "")
            }
            if !fill() { return (-1, "") }
        }
    }
    /// One message: (opcode, payload), nil at EOF.
    func readFrame() -> (UInt8, [UInt8])? {
        guard let h = take(2) else { return nil }
        var len = Int(h[1] & 0x7f)
        if len == 126 { guard let e = take(2) else { return nil }; len = Int(e[0]) << 8 | Int(e[1]) }
        else if len == 127 { guard let e = take(8) else { return nil }; len = e.reduce(0) { $0 << 8 | Int($1) } }
        guard let p = take(len) else { return nil }
        return (h[0] & 0x0f, p)
    }
    func sendFrame(_ op: UInt8, _ p: [UInt8]) {
        var f: [UInt8] = [0x80 | op]
        if p.count < 126 { f.append(0x80 | UInt8(p.count)) }
        else if p.count < 65536 { f += [0x80 | 126, UInt8(p.count >> 8), UInt8(p.count & 0xff)] }
        else { f.append(0x80 | 127); for i in (0..<8).reversed() { f.append(UInt8((p.count >> (8 * i)) & 0xff)) } }
        let mask: [UInt8] = [1, 2, 3, 4]
        f += mask
        f += p.enumerated().map { $1 ^ mask[$0 % 4] }
        write(f)
    }
    func shut() { shutdown(fd, Int32(SHUT_RDWR)) }
}

final class Box: @unchecked Sendable { var closed = false }

@MainActor final class Socket: TermTransport {
    let path: String
    let events: @MainActor (TermTransportEvent) -> Void
    let box = Box()
    var ws: RawWS?
    var url: String { path }
    init(path: String, events: @escaping @MainActor (TermTransportEvent) -> Void) { self.path = path; self.events = events }
    nonisolated func post(_ ev: TermTransportEvent) {
        let box = self.box
        DispatchQueue.main.async { MainActor.assumeIsolated { if !box.closed { self.events(ev) } } }
    }
    func start() {
        let parts = host.split(separator: ":"); let host = String(parts[0]); let port = UInt16(parts[1])!
        let path = self.path
        guard let ws = RawWS(host: host, port: port) else { post(.closed(.unreachable("connect failed"))); return }
        self.ws = ws
        Thread.detachNewThread { [self] in
            let (status, body) = ws.handshake(host: host, path: path)
            if status != 101 { self.post(status < 0 ? .closed(.failed) : .closed(.refused(status: status, body: body.trimmingCharacters(in: .whitespacesAndNewlines)))); return }
            self.post(.opened)
            while let (op, p) = ws.readFrame() {
                switch op {
                case 1: self.post(.message(.text(String(decoding: p, as: UTF8.self))))
                case 2: self.post(.message(.binary(p)))
                case 8: self.post(.closed(.dropped)); return
                default: break
                }
            }
            self.post(.closed(.dropped))
        }
    }
    func send(_ msg: TermWireMessage) {
        switch msg {
        case .text(let s): ws?.sendFrame(1, Array(s.utf8))
        case .binary(let b): ws?.sendFrame(2, b)
        }
    }
    func close() { box.closed = true; ws?.sendFrame(8, []); ws?.shut() }
    /// A network drop, as the session would see it.
    func simulateDrop() { let e = events; box.closed = true; ws?.shut(); DispatchQueue.main.async { MainActor.assumeIsolated { e(.closed(.dropped)) } } }
}

@MainActor func until(_ what: String, timeout: Double = 10, _ cond: () -> Bool) async -> Bool {
    let end = Date().addingTimeInterval(timeout)
    while Date() < end { if cond() { return true }; try? await Task.sleep(for: .milliseconds(50)) }
    return false
}

@MainActor func ptyCheck() async {
    let path = TileResource.apiPath("pty", tile: tile)!
    let url = TileResource.socketURL(origin: origin, path: path, frameToken: token)!
    check(url.absoluteString.hasPrefix("ws://\(host)/api/apps/ptytest/pty?frame="), "socket URL: ws://…/api/apps/ptytest/pty?frame=…")
    let screen = Screen(), phases = Phases()
    let wsPath = String(url.absoluteString.dropFirst("ws://\(host)".count))
    let s = TilePtySession(emulator: screen, clock: TermSystemClock()) { events in Socket(path: wsPath, events: events) }
    s.delegate = phases
    s.start()
    check(await until("live") { s.phase == .live }, "the pty socket goes live as the tile (\(s.phase))")
    check(await until("hello") { screen.out.contains("hello from=apps/ptytest user=admin") }, "the backend sees the tile and its user")
    check(await until("resize") { screen.out.contains("resized 80x24") }, "the size goes first ({\"op\":\"resize\"})")
    s.send("ls -la\r")
    check(await until("echo") { screen.out.contains("echo: LS -LA") }, "typed bytes reach the pty and its output comes back")
    s.resized(TermSize(cols: 100, rows: 30))
    check(await until("resize2") { screen.out.contains("resized 100x30") }, "a resize is a text frame")
    s.send("exit\r")
    check(await until("exit") { s.phase == .exited }, "an exit frame ends it (\(s.phase))")

    // without a frame token: refused, and the session says unauthorized
    let s2 = TilePtySession(emulator: Screen(), clock: TermSystemClock()) { events in Socket(path: path, events: events) }
    s2.start()
    check(await until("refused") { if case .failed = s2.phase { return true }; return false },
          "without the frame token the upgrade is refused (\(s2.phase))")
    // a stale token
    let s3 = TilePtySession(emulator: Screen(), clock: TermSystemClock()) { events in
        Socket(path: path + "?frame=bogus", events: events)
    }
    s3.start()
    check(await until("401") { s3.phase == .failed(.unauthorized) }, "a dead frame token is a 401 → unauthorized (\(s3.phase))")
}
await ptyCheck()

// 4. ACP prompt attachments (fakeacp)
struct CookieTransport: AgentTransport {
    let cookie: String
    func send(_ r: HTTPRequest) async throws -> HTTPResponse {
        var h = r.headers
        h["Cookie"] = cookie
        let (s, d, hh) = try await hatches_live.send(r.method, r.target, headers: h, body: r.body)
        var out: [String: String] = [:]
        for (k, v) in hh { out["\(k)"] = "\(v)" }
        return HTTPResponse(status: s, headers: out, body: d)
    }
    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> { throw AgentAPIError(status: 501, message: "n/a") }
}
let agents = AgentClient(transport: CookieTransport(cookie: cookie))
let providers = try await agents.providers()
check(providers.contains { $0.id == "fake" }, "the fake provider is offered")
let session = try await agents.create(CreateAgentSession(cwd: tile, provider: "fake"))
var idle = false
for _ in 0..<100 {
    let snap = try await agents.session(session.id)
    if snap.session.status == .idle { idle = true; break }
    try await Task.sleep(for: .milliseconds(100))
}
check(idle, "the agent session is up")
let jpegish = Data([0xFF, 0xD8, 0xFF, 0xE0] + [UInt8](repeating: 1, count: 60))
let atts = [PromptAttachment(name: "shot.png", mime: "image/png", data: png),
            PromptAttachment(name: "notes.txt", data: Data("hello notes\n".utf8)),
            PromptAttachment(name: "photo.jpg", mime: "image/jpeg", data: jpegish)]
let accepted = try await agents.prompt(session.id, text: "what are these?", attachments: atts)
check(accepted.turn >= 1, "the prompt with three files is taken (turn \(accepted.turn))")
var evs: [AgentEvent] = []
for _ in 0..<100 {
    evs = try await agents.events(session.id).events
    if evs.contains(where: { $0.type == "turn.end" }) { break }
    try await Task.sleep(for: .milliseconds(100))
}
let user = evs.first { $0.type == "message.delta" && $0.json["data"]?["role"]?.string == "user" }
let names = user?.json["data"]?["attachments"]?.array?.compactMap { $0["name"]?.string } ?? []
check(names == ["shot.png", "notes.txt", "photo.jpg"], "the user's message lists the files: \(names)")
let inline = user?.json["data"]?["attachments"]?.array?.map { $0["inline"]?.bool ?? false } ?? []
print("       inline: \(inline)")
let agentText = evs.filter { $0.type == "message.delta" && $0.json["data"]?["role"]?.string == "agent" }
    .compactMap { $0.json["data"]?["text"]?.string }.joined()
print("       agent: \(agentText.prefix(300))")
check(agentText.contains("[image image/png 108B]"), "the agent got the PNG inline")
do {
    _ = try await agents.prompt(session.id, text: "", attachments: Array(repeating: atts[1], count: 11))
    check(false, "11 files refused")
} catch let e as AgentAPIError {
    check(e.status == 400, "11 files are refused before a request: \(e)")
}
try? await agents.end(session.id)

print(failures == 0 ? "LIVE OK" : "LIVE FAILED: \(failures)")
exit(failures == 0 ? 0 : 1)
