// TermSessionTests.swift — the /ws/term session state machine against a fake
// socket, a fake emulator (optionally parsing asynchronously, as xterm.js
// does) and a manual clock.

import Foundation
import Testing
@testable import XbinTerm

// MARK: - fakes

@MainActor final class FakeTransport: TermTransport {
    let path: String
    let events: @MainActor (TermTransportEvent) -> Void
    var started = false, closed = false
    var sent: [TermWireMessage] = []
    init(_ path: String, _ events: @escaping @MainActor (TermTransportEvent) -> Void) { self.path = path; self.events = events }
    func start() { started = true }
    func send(_ msg: TermWireMessage) { sent.append(msg) }
    func close() { closed = true }
    // the server's side
    func open() { events(.opened) }
    func text(_ s: String) { events(.message(.text(s))) }
    func out(_ s: String) { events(.message(.binary(Array(s.utf8)))) }
    func drop(_ c: TermCloseInfo = .dropped) { events(.closed(c)) }
    func session(_ id: String = "s1", echoAck: Bool = true, netNote: String = "") {
        text(#"{"op":"session","id":"\#(id)","net":"internet","label":"internet","scopes":[{"id":"internet","label":"internet"}],"netNote":"\#(netNote)","baseOutdated":false,"vm":false,"echoAck":\#(echoAck)}"#)
    }
    var binaries: [[UInt8]] { sent.compactMap { if case .binary(let b) = $0 { return b }; return nil } }
    var texts: [String] { sent.compactMap { if case .text(let t) = $0 { return t }; return nil } }
    var pings: [String] { texts.filter { $0.contains(#""op":"ping""#) } }
}

/// A tiny emulator over FakeScreen: printable ASCII at the cursor, CR, LF.
/// With `async`, writes are parsed only on `flush()` (xterm.js's write queue).
@MainActor final class FakeEmulator: TermEmulator {
    let screen: FakeScreen
    var log: [String] = []
    var async = false
    private var queued: [[UInt8]] = []
    private var bodies: [@MainActor () -> Void] = []
    init(rows: Int = 4, cols: Int = 20) { screen = FakeScreen(rows, cols) }
    var size: TermSize { TermSize(cols: screen.cols, rows: screen.rows) }
    var framebuffer: (any TermFramebuffer)? { screen }
    func write(_ bytes: [UInt8]) {
        log.append("write:" + String(decoding: bytes, as: UTF8.self))
        if async { queued.append(bytes) } else { parse(bytes) }
    }
    func afterParsed(_ body: @escaping @MainActor () -> Void) {
        if async { bodies.append(body) } else { body() }
    }
    func reset() {
        log.append("reset")
        for r in 0..<screen.rows { screen.g[r] = FakeScreen.blankRow(screen.cols) }
        screen.cursor = TermPos(row: 0, col: 0)
    }
    func flush() {
        for b in queued { parse(b) }
        queued = []
        let bs = bodies; bodies = []
        for b in bs { b() }
    }
    private func parse(_ bytes: [UInt8]) {
        for b in bytes {
            switch b {
            case 0x0d: screen.cursor.col = 0
            case 0x0a: screen.cursor.row = min(screen.cursor.row + 1, screen.rows - 1)
            case 0x20..<0x7f:
                if screen.cursor.col < screen.cols { screen.g[screen.cursor.row][screen.cursor.col] = String(UnicodeScalar(b)) }
                screen.cursor.col = min(screen.cursor.col + 1, screen.cols - 1)
            default: break
            }
        }
    }
}

@MainActor final class ManualClock: TermClock {
    var t: Double = 1000
    private var timers: [(due: Double, n: Int, timer: T)] = []
    private var n = 0
    final class T: TermTimer {
        var cancelled = false
        let body: @MainActor () -> Void
        init(_ b: @escaping @MainActor () -> Void) { body = b }
        func cancel() { cancelled = true }
    }
    func now() -> Double { t }
    func schedule(after ms: Double, _ body: @escaping @MainActor () -> Void) -> any TermTimer {
        let x = T(body); n += 1
        timers.append((t + ms, n, x))
        return x
    }
    /// Advances time, firing due timers in order (including ones they schedule).
    func advance(_ ms: Double) {
        let end = t + ms
        while let i = timers.indices.filter({ timers[$0].due <= end && !timers[$0].timer.cancelled })
            .min(by: { (timers[$0].due, timers[$0].n) < (timers[$1].due, timers[$1].n) }) {
            let e = timers.remove(at: i)
            t = max(t, e.due)
            e.timer.body()
        }
        t = end
        timers.removeAll { $0.timer.cancelled }
    }
    var pending: Int { timers.filter { !$0.timer.cancelled }.count }
}

@MainActor final class Recorder: TermSessionDelegate {
    var phases: [TermSession.Phase] = []
    var attached: [TermSessionInfo] = []
    var netNotes: [String] = []
    var overlays: [(PredictionRender, Bool)] = []
    var rtts: [Double] = []
    var notices: [TermSession.Notice] = []
    func termSession(_ s: TermSession, phaseChanged phase: TermSession.Phase) { phases.append(phase) }
    func termSession(_ s: TermSession, attached info: TermSessionInfo) { attached.append(info) }
    func termSession(_ s: TermSession, netNote: String) { netNotes.append(netNote) }
    func termSession(_ s: TermSession, overlay: PredictionRender, lagging: Bool) { overlays.append((overlay, lagging)) }
    func termSession(_ s: TermSession, rtt: Double) { rtts.append(rtt) }
    func termSession(_ s: TermSession, notice: TermSession.Notice) { notices.append(notice) }
}

/// A session wired to fakes; `socks` are the transports it opened, in order.
@MainActor final class Harness {
    let emu: FakeEmulator
    let clock = ManualClock()
    let rec = Recorder()
    var socks: [FakeTransport] = []
    var session: TermSession!
    var sock: FakeTransport { socks.last! }
    init(target: TermTarget = .new(TermNewSession(cwd: "apps/x")), fresh: TermNewSession? = nil,
         initialInput: [UInt8]? = nil, rows: Int = 4, cols: Int = 20) {
        emu = FakeEmulator(rows: rows, cols: cols)
        session = TermSession(target: target, fresh: fresh, initialInput: initialInput, emulator: emu, clock: clock) { [unowned self] path, events in
            let t = FakeTransport(path, events)
            self.socks.append(t)
            return t
        }
        session.delegate = rec
    }
    /// start, open, session frame (and an empty replay: nothing).
    func live(_ id: String = "s1", echoAck: Bool = true) {
        session.start(); sock.open(); sock.session(id, echoAck: echoAck)
    }
}

// MARK: - tests

@MainActor @Suite("/ws/term session")
struct TermSessionTests {
    @Test("connect → session frame → replay → live")
    func connectFlow() {
        let h = Harness()
        #expect(h.session.phase == .idle)
        h.session.start()
        #expect(h.sock.path == "/ws/term?cwd=apps%2Fx&gpu=none&api=1")
        #expect(h.sock.started)
        #expect(h.session.phase == .connecting(attempt: 0))
        h.sock.open()
        #expect(h.session.phase == .handshaking)
        #expect(h.sock.texts == [#"{"op":"resize","cols":20,"rows":4}"#], "the size goes first, as bx-terminal sends it on open")
        h.sock.session("abc")
        #expect(h.session.phase == .live)
        #expect(h.session.target == .reattach(id: "abc"))
        #expect(h.session.info?.id == "abc")
        #expect(h.rec.attached.map(\.id) == ["abc"])
        #expect(h.session.echoAck)
        #expect(h.sock.pings == [#"{"op":"ping","t":1000}"#], "an echo-acking xbind is pinged at once")
        h.sock.out("$ ")
        #expect(h.emu.log == ["reset", "write:$ "])
        #expect(h.emu.screen.lineAt(0).hasPrefix("$ "))
        #expect(h.rec.phases == [.connecting(attempt: 0), .handshaking, .live])
    }

    @Test("a reattach resets the emulator before the replay, so the scrollback isn't printed twice")
    func reattachResets() {
        let h = Harness()
        h.live("s1")
        h.sock.out("hello")
        h.sock.drop()
        #expect(h.session.phase == .reconnecting(attempt: 1, delayMs: 500))
        h.clock.advance(499)
        #expect(h.socks.count == 1)
        h.clock.advance(1)
        #expect(h.socks.count == 2)
        #expect(h.sock.path == "/ws/term?session=s1")
        h.sock.open(); h.sock.session("s1")
        h.sock.out("hello world") // the server replays the whole scrollback
        #expect(h.emu.log == ["reset", "write:hello", "reset", "write:hello world"])
        #expect(h.emu.screen.lineAt(0).hasPrefix("hello world"))
        #expect(h.session.phase == .live)
    }

    @Test("the backoff is bx-terminal's: 500 ms doubling to 10 s, eight tries, then disconnected — resumable")
    func backoff() {
        let h = Harness()
        h.live("s1")
        h.sock.drop()
        var waits: [Double] = []
        while case .reconnecting(_, let d) = h.session.phase {
            waits.append(d)
            h.clock.advance(d)
            h.sock.drop(.unreachable("offline")) // never reached the server
        }
        #expect(waits == [500, 1000, 2000, 4000, 8000, 10000, 10000, 10000])
        #expect(h.session.phase == .failed(.disconnected))
        #expect(h.session.target == .reattach(id: "s1"), "the session is kept: the network was down, not the session")
        let n = h.socks.count
        h.session.reconnect()
        #expect(h.socks.count == n + 1)
        #expect(h.sock.path == "/ws/term?session=s1")
        h.sock.open(); h.sock.session("s1")
        #expect(h.session.phase == .live)
    }

    @Test("an open resets the backoff")
    func openResetsBackoff() {
        let h = Harness()
        h.live("s1")
        for _ in 0..<5 {
            h.sock.drop()
            #expect(h.session.phase == .reconnecting(attempt: 1, delayMs: 500))
            h.clock.advance(500); h.sock.open(); h.sock.session("s1")
        }
    }

    @Test("a close frame from xbind, whatever its code, is a drop: reattach")
    func serverCloseIsADrop() {
        let h = Harness()
        h.live("s1")
        for code in [1000, 1001, 1005, 1011] {
            h.sock.drop(.serverClosed(code: code))
            #expect(h.session.phase == .reconnecting(attempt: 1, delayMs: 500))
            h.clock.advance(500); h.sock.open(); h.sock.session("s1")
            #expect(h.session.target == .reattach(id: "s1"))
        }
    }

    @Test("a reattach that twice fails to open, for a reason the transport can't see, starts fresh (bx-terminal's rule)")
    func failedTwiceStartsFresh() {
        let h = Harness()
        h.live("s1")
        h.sock.drop()
        h.clock.advance(500); h.sock.drop(.failed)
        #expect(h.session.phase == .reconnecting(attempt: 2, delayMs: 1000))
        h.clock.advance(1000); h.sock.drop(.failed)
        #expect(h.rec.notices == [.previousSessionGone])
        #expect(h.sock.path == "/ws/term?cwd=apps%2Fx&gpu=none&api=1", "a fresh session on the same tile")
        #expect(h.session.phase == .connecting(attempt: 0))
        h.sock.open(); h.sock.session("s2")
        #expect(h.session.target == .reattach(id: "s2"))
    }

    @Test("404 on a reattach: the session is gone — fresh at once, or failed without a tile")
    func notFound() {
        let h = Harness()
        h.live("s1")
        h.sock.drop()
        h.clock.advance(500); h.sock.drop(.refused(status: 404, body: "no such session"))
        #expect(h.rec.notices == [.previousSessionGone])
        #expect(h.sock.path == "/ws/term?cwd=apps%2Fx&gpu=none&api=1")

        let r = Harness(target: .reattach(id: "old"))
        r.session.start()
        #expect(r.sock.path == "/ws/term?session=old")
        r.sock.drop(.refused(status: 404, body: "no such session"))
        #expect(r.session.phase == .failed(.sessionGone))

        let f = Harness(target: .reattach(id: "old"), fresh: TermNewSession(cwd: "apps/y", net: "none"))
        f.session.start(); f.sock.drop(.refused(status: 404, body: ""))
        #expect(f.sock.path == "/ws/term?cwd=apps%2Fy&net=none&gpu=none&api=1")
    }

    @Test("refusals: 401 waits for a re-sign-in, 403/409/400 fail, a reattach's 5xx retries")
    func refusals() {
        let a = Harness(); a.live("s1"); a.sock.drop()
        a.clock.advance(500); a.sock.drop(.refused(status: 401, body: "unauthorized"))
        #expect(a.session.phase == .failed(.unauthorized))
        #expect(a.clock.pending == 0, "no retry timer: signing in again is the app's job")
        a.session.reconnect()
        #expect(a.sock.path == "/ws/term?session=s1")

        let b = Harness(); b.live("s1"); b.sock.drop()
        b.clock.advance(500); b.sock.drop(.refused(status: 403, body: "revoked"))
        #expect(b.session.phase == .failed(.forbidden("revoked")))

        let c = Harness(target: .reattach(id: "agent1")); c.session.start()
        c.sock.drop(.refused(status: 409, body: "an agent session has no terminal socket"))
        #expect(c.session.phase == .failed(.refused(status: 409, reason: "an agent session has no terminal socket")))

        let d = Harness(target: .new(TermNewSession(cwd: "x", vm: true))); d.session.start()
        d.sock.drop(.refused(status: 400, body: "VM sandboxes need isolation"))
        #expect(d.session.phase == .failed(.refused(status: 400, reason: "VM sandboxes need isolation")))

        let e = Harness(); e.live("s1"); e.sock.drop()
        e.clock.advance(500); e.sock.drop(.refused(status: 502, body: "bad gateway"))
        #expect(e.session.phase == .reconnecting(attempt: 2, delayMs: 1000))

        let g = Harness(); g.session.start(); g.sock.drop(.refused(status: 500, body: "spawn failed"))
        #expect(g.session.phase == .failed(.refused(status: 500, reason: "spawn failed")), "a new session's failure isn't retried")
    }

    @Test("a new session whose socket never opens is disconnected, as in bx-terminal")
    func newSessionNeverOpens() {
        let h = Harness()
        h.session.start(); h.sock.drop(.unreachable("offline"))
        #expect(h.session.phase == .failed(.disconnected))
        #expect(h.session.target == .new(TermNewSession(cwd: "apps/x")))
    }

    @Test("exit ends the session: closed socket, no reconnect")
    func exit() {
        let h = Harness(); h.live("s1")
        h.sock.text(#"{"op":"exit"}"#)
        #expect(h.session.phase == .exited)
        #expect(h.sock.closed)
        h.sock.drop()
        #expect(h.session.phase == .exited)
        #expect(h.clock.pending == 0)
        h.session.reconnect()
        #expect(h.socks.count == 1, "an exited session can't be reattached")
    }

    @Test("close() stops everything; late events are ignored")
    func close() {
        let h = Harness(); h.live("s1")
        let s = h.sock
        h.session.close()
        #expect(h.session.phase == .closed)
        #expect(s.closed)
        s.out("late"); s.drop()
        #expect(h.emu.log == ["reset"])
        #expect(h.session.phase == .closed)
        #expect(h.clock.pending == 0)
    }

    @Test("restart opens a new session; the old socket is closed and can no longer drive the terminal")
    func restartSupersedes() {
        let h = Harness(); h.live("s1")
        let old = h.sock
        h.session.restart(TermNewSession(cwd: "apps/x", net: "none"))
        #expect(old.closed)
        #expect(h.sock.path == "/ws/term?cwd=apps%2Fx&net=none&gpu=none&api=1")
        old.out("stale output"); old.text(#"{"op":"exit"}"#); old.drop()
        #expect(h.emu.log == ["reset"])
        #expect(h.session.phase == .connecting(attempt: 0))
        h.sock.open(); h.sock.session("s2")
        #expect(h.session.info?.id == "s2")
    }

    @Test("pings every 5 s while visible; the RTT is RFC 6298-smoothed; bogus pongs are ignored")
    func pingsAndRtt() {
        let h = Harness(); h.live("s1")
        #expect(h.sock.pings.count == 1)
        h.clock.advance(120)
        h.sock.text(#"{"op":"pong","t":1000}"#)
        #expect(h.session.srtt == 120)
        h.clock.advance(4880) // 5 s after the session frame
        #expect(h.sock.pings.count == 2)
        #expect(h.sock.pings.last == #"{"op":"ping","t":6000}"#)
        h.clock.advance(200)
        h.sock.text(#"{"op":"pong","t":6000}"#)
        #expect(h.session.srtt == 130, "7/8 · 120 + 1/8 · 200")
        #expect(h.rec.rtts == [120, 130])
        h.sock.text(#"{"op":"pong","t":99999999}"#) // in the future
        h.sock.text(#"{"op":"pong","t":-70000}"#)   // over a minute
        h.sock.text(#"{"op":"pong","t":"x"}"#)
        #expect(h.session.srtt == 130)
        h.session.visible = false
        h.clock.advance(20000)
        #expect(h.sock.pings.count == 2, "no pings while hidden")
        h.session.visible = true
        h.clock.advance(5000)
        #expect(h.sock.pings.count == 3)
    }

    @Test("an xbind without echo acks: no pings, no predictions")
    func noEchoAck() {
        let h = Harness(); h.live("s1", echoAck: false)
        h.session.predictMode = .on
        h.sock.out("$ ")
        h.session.send("a")
        #expect(h.sock.pings.isEmpty)
        #expect(h.session.predictor.active() == false)
        #expect(h.sock.binaries == [[0x61]])
        h.clock.advance(60000)
        #expect(h.sock.pings.isEmpty)
    }

    @Test("input: dropped while closed, one binary frame per send, resize when open")
    func input() {
        let h = Harness()
        h.session.send("lost")
        h.session.start()
        h.session.send("lost too") // not open yet
        h.sock.open()
        h.session.send("ls\r")
        #expect(h.sock.binaries == [Array("ls\r".utf8)])
        h.session.resized(TermSize(cols: 100, rows: 30))
        #expect(h.sock.texts.last == #"{"op":"resize","cols":100,"rows":30}"#)
        h.session.send([])
        #expect(h.sock.binaries.count == 1, "nothing to send, no frame")
    }

    @Test("the initial input is typed once, into the first session, as input")
    func initialInput() {
        let h = Harness(initialInput: Array("claude /login\n".utf8))
        h.live("s1")
        #expect(h.sock.binaries == [Array("claude /login\n".utf8)])
        h.sock.drop(); h.clock.advance(500); h.sock.open(); h.sock.session("s1")
        #expect(h.sock.binaries.isEmpty, "not again on a reattach")
    }

    @Test("a clamp note is reported once per session id")
    func netNote() {
        let h = Harness()
        h.session.start(); h.sock.open(); h.sock.session("s1", netNote: "host networking is admin-only")
        h.sock.drop(); h.clock.advance(500); h.sock.open(); h.sock.session("s1", netNote: "host networking is admin-only")
        #expect(h.rec.netNotes == ["host networking is admin-only"])
    }

    @Test("predictive echo: typed text is drawn at once and released when the ack covers the echo")
    func predictiveEcho() {
        let h = Harness(); h.live("s1")
        h.session.predictMode = .on
        h.sock.out("$ ")
        h.session.send("l")
        #expect(h.session.overlay.cells == [PredictedRun(row: 0, col: 2, cells: ["l"], underline: false)])
        #expect(h.session.overlay.cursor == TermPos(row: 0, col: 3))
        #expect(h.sock.binaries == [[0x6c]])
        #expect(h.session.predictor.localFrameSent == 1)
        h.sock.out("l") // the echo
        #expect(h.session.overlay.cells.isEmpty, "the screen shows it now: nothing to draw")
        #expect(h.session.predictor.pending() > 0)
        h.sock.text(#"{"op":"ack","n":1}"#)
        #expect(h.session.predictor.pending() == 0)
        #expect(h.session.overlay == .empty)
    }

    @Test("an ack is applied only after the emulator parsed the output before it")
    func ackAfterParse() {
        let h = Harness(); h.live("s1")
        h.emu.async = true
        h.session.predictMode = .on
        h.sock.out("$ "); h.emu.flush()
        h.session.send("x")
        h.sock.out("x")                      // queued in the emulator, not parsed
        h.sock.text(#"{"op":"ack","n":1}"#)  // must wait for it
        #expect(h.session.predictor.lateAck == 0)
        #expect(h.session.predictor.pending() > 0)
        h.emu.flush()
        #expect(h.session.predictor.lateAck == 1)
        #expect(h.session.predictor.pending() == 0)
        #expect(h.session.predictor.glitchTrigger == 0)
    }

    @Test("acks from a superseded socket are never applied")
    func staleAck() {
        let h = Harness(); h.live("s1")
        h.emu.async = true
        h.sock.text(#"{"op":"ack","n":5}"#) // queued behind the parse
        h.sock.drop(); h.clock.advance(500) // a new socket renumbers frames
        h.emu.flush()
        #expect(h.session.predictor.lateAck == 0)
    }

    @Test("nothing is predicted between the session frame and the replay")
    func noPredictionBeforeReplay() {
        let h = Harness(); h.live("s1")
        h.session.predictMode = .on
        h.session.send("a")
        #expect(h.session.predictor.active() == false, "the screen was just reset: the replay hasn't arrived")
        h.sock.out("a$ ")
        h.session.send("b")
        #expect(h.session.predictor.active())
    }

    @Test("auto mode: the overlay shows once the RTT is slow, with the lag badge")
    func autoModeLag() {
        let h = Harness(); h.live("s1")
        h.sock.out("$ ")
        h.clock.advance(150); h.sock.text(#"{"op":"pong","t":1000}"#)
        #expect(h.session.srtt == 150)
        h.session.send("a")
        #expect(h.session.overlay.cells.first?.text == "a")
        #expect(h.session.lagging)
        #expect(h.rec.overlays.last?.1 == true)
    }

    @Test("the cursor-hidden state and buffer switches reach the predictor")
    func emulatorSignals() {
        let h = Harness(); h.live("s1")
        h.sock.out("$ ")
        h.session.cursorVisibilityChanged(hidden: true)
        #expect(h.session.predictor.cursorHidden)
        h.session.predictMode = .on
        h.session.cursorVisibilityChanged(hidden: false)
        h.session.send("a")
        #expect(h.session.predictor.active())
        h.session.bufferChanged()
        #expect(h.session.predictor.active() == false)
        #expect(h.session.overlay == .empty)
    }
}
