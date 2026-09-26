// TilePtyTests.swift — a native tile's `terminal` element: its pty socket
// against the fake socket, emulator and clock of TermSessionTests.

import Foundation
import Testing
@testable import XbinTerm

@MainActor final class PtyRecorder: TilePtyDelegate {
    var phases: [TilePtySession.Phase] = []
    func tilePty(_ p: TilePtySession, phaseChanged phase: TilePtySession.Phase) { phases.append(phase) }
}

@MainActor struct PtyRig {
    let emu = FakeEmulator(rows: 4, cols: 20)
    let clock = ManualClock()
    let rec = PtyRecorder()
    let box = SocketBox()
    let session: TilePtySession

    final class SocketBox {
        var sockets: [FakeTransport] = []
        var last: FakeTransport { sockets[sockets.count - 1] }
    }

    init() {
        let box = self.box
        session = TilePtySession(emulator: emu, clock: clock) { events in
            let t = FakeTransport("/api/t/pty", events)
            box.sockets.append(t)
            return t
        }
        session.delegate = rec
    }
}

@MainActor @Suite struct TilePtyTests {
    @Test func liveOnOpenResizeAndBytes() {
        let r = PtyRig()
        r.session.start()
        #expect(r.session.phase == .connecting(attempt: 0) && r.box.sockets.count == 1 && r.box.last.started)
        // nothing goes out before the socket opens
        r.session.send("early")
        #expect(r.box.last.sent.isEmpty)
        r.box.last.open()
        #expect(r.session.phase == .live)
        // the size first, as /ws/term expects
        #expect(r.box.last.texts == [#"{"op":"resize","cols":20,"rows":4}"#])
        r.session.send("ls\r")
        #expect(r.box.last.binaries == [Array("ls\r".utf8)])
        r.box.last.out("hello")
        #expect(r.emu.screen.lineAt(0).hasPrefix("hello"))
        r.session.resized(TermSize(cols: 80, rows: 24))
        #expect(r.box.last.texts.last == #"{"op":"resize","cols":80,"rows":24}"#)
        // a tile backend's stray control frames are ignored
        r.box.last.text(#"{"op":"session","id":"x"}"#)
        r.box.last.text("not json")
        #expect(r.session.phase == .live && r.emu.log.filter { $0 == "reset" }.isEmpty)
    }

    @Test func exitEndsIt() {
        let r = PtyRig()
        r.session.start()
        r.box.last.open()
        r.box.last.text(#"{"op":"exit"}"#)
        #expect(r.session.phase == .exited && r.box.last.closed)
        r.box.last.drop()
        r.clock.advance(60_000)
        #expect(r.box.sockets.count == 1 && r.session.phase == .exited)
    }

    @Test func dropsReconnectWithBackoffKeepingTheScreen() {
        let r = PtyRig()
        r.session.start()
        r.box.last.open()
        r.box.last.out("kept")
        r.box.last.drop()
        #expect(r.session.phase == .reconnecting(attempt: 1, delayMs: 500))
        r.clock.advance(499)
        #expect(r.box.sockets.count == 1)
        r.clock.advance(1)
        #expect(r.box.sockets.count == 2 && r.session.phase == .connecting(attempt: 1))
        r.box.last.drop(.unreachable("offline"))
        #expect(r.session.phase == .reconnecting(attempt: 2, delayMs: 1000))
        r.clock.advance(1000)
        r.box.last.open()
        #expect(r.session.phase == .live)
        #expect(r.emu.screen.lineAt(0).hasPrefix("kept") && !r.emu.log.contains("reset"))
        // the retry count starts over once a socket opened
        r.box.last.drop()
        #expect(r.session.phase == .reconnecting(attempt: 1, delayMs: 500))
    }

    @Test func givesUpAfterTheRetries() {
        let r = PtyRig()
        r.session.start()
        for _ in 0..<TilePtySession.maxRetries {
            r.box.last.drop(.failed)
            r.clock.advance(TilePtySession.retryCapMs)
        }
        r.box.last.drop(.failed)
        #expect(r.session.phase == .failed(.disconnected))
        #expect(r.box.sockets.count == TilePtySession.maxRetries + 1)
        r.session.reconnect()
        #expect(r.session.phase == .connecting(attempt: 0) && r.box.sockets.count == TilePtySession.maxRetries + 2)
    }

    @Test func refusals() {
        let r = PtyRig()
        r.session.start()
        r.box.last.drop(.refused(status: 401, body: ""))
        #expect(r.session.phase == .failed(.unauthorized))
        r.clock.advance(60_000)
        #expect(r.box.sockets.count == 1)
        // the app renewed the frame token: try again
        r.session.reconnect()
        r.box.last.drop(.refused(status: 403, body: "no grant"))
        #expect(r.session.phase == .failed(.refused(status: 403)))
        r.session.reconnect()
        r.box.last.drop(.refused(status: 404, body: ""))
        #expect(r.session.phase == .failed(.refused(status: 404)))
        // a 5xx or 429 is worth another try
        r.session.reconnect()
        r.box.last.drop(.refused(status: 502, body: ""))
        #expect(r.session.phase == .reconnecting(attempt: 1, delayMs: 500))
        r.clock.advance(500)
        r.box.last.drop(.refused(status: 429, body: ""))
        #expect(r.session.phase == .reconnecting(attempt: 2, delayMs: 1000))
    }

    @Test func closeAndStaleSockets() {
        let r = PtyRig()
        r.session.start()
        let first = r.box.last
        r.session.reconnect()
        #expect(first.closed && r.box.sockets.count == 2)
        // the superseded socket's late events drive nothing
        first.open()
        first.out("stale")
        #expect(r.session.phase == .connecting(attempt: 0) && !r.emu.screen.lineAt(0).hasPrefix("stale"))
        r.box.last.open()
        r.session.close()
        #expect(r.session.phase == .closed && r.box.last.closed)
        r.box.last.drop()
        r.clock.advance(60_000)
        #expect(r.box.sockets.count == 2)
        r.session.reconnect() // closed stays closed
        #expect(r.box.sockets.count == 2)
        #expect(r.rec.phases.last == .closed)
    }
}
