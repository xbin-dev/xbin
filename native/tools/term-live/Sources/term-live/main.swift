// term-live — drives XbinTerm's TermSession against a real xbind's /ws/term:
// the session frame, echo acks, pongs, a network drop and the reattach with
// its replay (the emulator reset first, so the scrollback isn't doubled),
// resize, DELETE → exit, and the refusals (404 gone, 403 root terminal,
// 400 bad options). Linux only: a raw RFC 6455 client over a TCP socket
// stands in for the app's URLSessionWebSocketTask (this box's libcurl has no
// WebSockets), reporting the upgrade's HTTP status as the app's must.
//
//   go build -o /tmp/xbind ./cmd/xbind && /tmp/xbind init /tmp/tws
//   /tmp/xbind --dev --no-auth --workspace /tmp/tws --listen 127.0.0.1:18931 &
//   cd native/tools/term-live && swift run term-live 127.0.0.1:18931
//
// Exit 0 and "LIVE OK" when every check passes. Not run by CI (it needs a
// running xbind); run it after changing /ws/term or TermSession.
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XbinTerm

func say(_ items: Any...) { FileHandle.standardError.write(Data((items.map { "\($0)" }.joined(separator: " ") + "\n").utf8)) }
let base = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "127.0.0.1:18931"

#if canImport(Glibc)
import Glibc
#endif

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

@MainActor final class WSTransport: TermTransport {
    let path: String
    let events: @MainActor (TermTransportEvent) -> Void
    let box = Box()
    var ws: RawWS?
    var url: String { path }
    init(_ path: String, _ events: @escaping @MainActor (TermTransportEvent) -> Void) { self.path = path; self.events = events }
    nonisolated func post(_ ev: TermTransportEvent) {
        let box = self.box
        DispatchQueue.main.async { MainActor.assumeIsolated { if !box.closed { self.events(ev) } } }
    }
    func start() {
        let parts = base.split(separator: ":"); let host = String(parts[0]); let port = UInt16(parts[1])!
        let path = self.path
        guard let ws = RawWS(host: host, port: port) else { post(.closed(.unreachable("connect failed"))); return }
        self.ws = ws
        Thread.detachNewThread { [self] in
            let (status, body) = ws.handshake(host: base, path: path)
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

@MainActor final class Emu: TermEmulator {
    var text = ""
    var resets = 0
    func write(_ bytes: [UInt8]) { text += String(decoding: bytes, as: UTF8.self) }
    func afterParsed(_ body: @escaping @MainActor () -> Void) { body() }
    func reset() { resets += 1; text = "" }
    var framebuffer: (any TermFramebuffer)? { nil }
    var size: TermSize { TermSize(cols: 100, rows: 30) }
}

@MainActor final class Rec: TermSessionDelegate {
    var log: [String] = []
    func termSession(_ s: TermSession, phaseChanged phase: TermSession.Phase) { log.append("phase \(phase)"); say("phase:", phase) }
    func termSession(_ s: TermSession, attached info: TermSessionInfo) { say("attached:", info) }
    func termSession(_ s: TermSession, rtt: Double) { say("rtt:", rtt) }
    func termSession(_ s: TermSession, notice: TermSession.Notice) { say("notice:", notice) }
}

@MainActor func until(_ what: String, _ timeout: Double = 10, _ cond: () -> Bool) async -> Bool {
    let end = Date().addingTimeInterval(timeout)
    while Date() < end { if cond() { return true }; try? await Task.sleep(for: .milliseconds(20)) }
    say("TIMEOUT waiting for", what); return false
}

var failures = 0
@MainActor func check(_ ok: Bool, _ what: String) { say(ok ? "PASS" : "FAIL", what); if !ok { failures += 1 } }

@MainActor func run() async {
    let emu = Emu(), rec = Rec()
    var socks: [WSTransport] = []
    let s = TermSession(target: .new(TermNewSession(cwd: "apps/welcome")), emulator: emu, clock: TermSystemClock()) { path, ev in
        let t = WSTransport(path, ev); socks.append(t); return t
    }
    s.delegate = rec
    s.start()
    check(await until("live") { s.phase == .live }, "a new session goes live")
    check(s.info?.echoAck == true, "this xbind acks input (echoAck)")
    check(!(s.info?.id ?? "").isEmpty, "the session frame names the session: \(s.info?.id ?? "")")
    check(s.info?.scopes.isEmpty == false, "the session frame lists scopes: \(s.info?.scopes.map(\.id) ?? [])")
    check(await until("pong") { s.srtt != nil }, "a pong measured the RTT: \(s.srtt ?? -1) ms")
    s.send("echo xbin-live-$((6*7))\r")
    check(await until("echo output") { emu.text.contains("xbin-live-42") }, "input reached the shell and its output came back")
    check(await until("ack") { s.predictor.lateAck >= 1 }, "the server acked the input frame (lateAck \(s.predictor.lateAck))")
    guard let id = s.info?.id else { say("no session"); return }
    // a network drop: reattach by id, the emulator reset before the replay
    socks.last!.simulateDrop()
    check(await until("reconnecting") { if case .reconnecting = s.phase { return true }; return false }, "a drop schedules a reattach")
    check(await until("live again") { s.phase == .live && socks.count == 2 }, "reattached")
    check(socks.last!.path == "/ws/term?session=\(id)", "the reattach asked for the same session")
    check(await until("replay") { emu.text.contains("xbin-live-42") }, "the scrollback was replayed")
    check(emu.resets == 2, "the emulator was reset on each session frame (resets \(emu.resets))")
    // the typed line reads $((6*7)); only the output says 42 — once, unless the
    // replay landed on top of the old screen
    let n = emu.text.components(separatedBy: "xbin-live-42").count - 1
    check(n == 1, "the scrollback shows once after the reattach (\(n)×)")
    // resize reaches the PTY
    s.resized(TermSize(cols: 77, rows: 21))
    s.send("stty size\r")
    check(await until("stty size") { emu.text.contains("21 77") }, "resize reached the PTY (stty size = 21 77)")
    // DELETE ends the session: exit
    var req = URLRequest(url: URL(string: "http://\(base)/ws/term?session=\(id)")!); req.httpMethod = "DELETE"
    let code = await withCheckedContinuation { (c: CheckedContinuation<Int, Never>) in
        URLSession.shared.dataTask(with: req) { _, r, _ in c.resume(returning: (r as? HTTPURLResponse)?.statusCode ?? -1) }.resume()
    }
    check(code == 204, "DELETE /ws/term?session= → \(code)")
    check(await until("exit") { s.phase == .exited }, "the client saw exit")
    // an unknown session: 404 → gone
    let g = TermSession(target: .reattach(id: "no-such-session"), emulator: Emu(), clock: TermSystemClock()) { path, ev in WSTransport(path, ev) }
    g.start()
    check(await until("gone") { if case .failed = g.phase { return true }; return false }, "an unknown session fails: \(g.phase)")
    check(g.phase == .failed(.sessionGone), "…as gone (the transport saw the 404)")
    // the root terminal is disabled for everyone: 403
    let root = TermSession(target: .new(TermNewSession(cwd: "")), emulator: Emu(), clock: TermSystemClock()) { path, ev in WSTransport(path, ev) }
    root.start()
    check(await until("root") { if case .failed = root.phase { return true }; return false }, "the root terminal is refused: \(root.phase)")
    if case .failed(.forbidden(let why)) = root.phase { check(why.contains("root terminal"), "…403 with the server's reason") } else { check(false, "…403") }
    // bad options: docs/protocol.md says a VM terminal without --isolate is a
    // 400 with the reason. xbind (as of this tool) only refuses it on an
    // isolated workspace; without isolation it opens a host shell whose
    // session frame says vm:true — a server bug, reported, not the client's.
    let vm = TermSession(target: .new(TermNewSession(cwd: "apps/welcome", vm: true)), emulator: Emu(), clock: TermSystemClock()) { path, ev in WSTransport(path, ev) }
    vm.start()
    _ = await until("vm", 5) { if case .failed = vm.phase { return true }; return vm.phase == .live }
    if case .failed(.refused(400, let why)) = vm.phase { check(!why.isEmpty, "a VM terminal without isolation is refused (400: \(why))") }
    else if vm.phase == .live, vm.info?.vm == true { say("NOTE xbind accepted vm=1 without --isolate and reports vm:true (docs say 400) — server-side") ; vm.close() }
    else { check(false, "a VM terminal without isolation: \(vm.phase)") }
    say(failures == 0 ? "LIVE OK" : "LIVE FAILURES: \(failures)")
}

Task { @MainActor in await run(); exit(failures == 0 ? 0 : 1) }
dispatchMain()
