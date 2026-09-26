// TilePty.swift — a native tile's `terminal` element (plans/native.md §8.5,
// docs/native.md "Escape hatches"): a terminal on the tile's OWN backend's
// pty WebSocket, reached as the tile (the app's transport adds the frame
// token; never the user's session), speaking the part of the /ws/term
// framing a tile backend speaks:
//
//   binary, both ways       raw PTY bytes
//   client → server         {"op":"resize","cols":C,"rows":R} (on open and on every resize)
//   server → client         {"op":"exit"} the pty ended (optional; a close does too)
//
// No session frame, replay, acks or pings (those are xbind's own sessions,
// TermSession): the socket is live once it opens. A drop reconnects with
// bx-terminal's backoff; the screen is kept (the backend decides whether a
// new socket is a new shell). A 401 is the frame token dying with the
// session behind it: the app renews the token once and calls reconnect().

import Foundation

@MainActor public protocol TilePtyDelegate: AnyObject {
    func tilePty(_ p: TilePtySession, phaseChanged phase: TilePtySession.Phase)
}

/// Opens the tile's pty socket (the app resolves the path and adds the
/// frame token; the session only sees events).
public typealias TilePtyConnect = @MainActor (_ events: @escaping @MainActor (TermTransportEvent) -> Void) -> any TermTransport

@MainActor public final class TilePtySession {
    public enum Phase: Equatable, Sendable {
        case idle
        /// A socket is opening (`attempt` 0 = the first try).
        case connecting(attempt: Int)
        /// Open: output flows, input is sent.
        case live
        /// The socket dropped or never opened; trying again after `delayMs`.
        case reconnecting(attempt: Int, delayMs: Double)
        /// The pty ended (`exit`, or the backend closed a socket that had
        /// been live after an exit frame).
        case exited
        /// Gave up; `reconnect()` tries again.
        case failed(Failure)
        /// `close()` was called.
        case closed

        public var isLive: Bool { self == .live }
    }

    public enum Failure: Equatable, Sendable {
        /// The retries ran out (offline, the backend down).
        case disconnected
        /// 401: the frame token (or the session behind it) died — renew, then `reconnect()`.
        case unauthorized
        /// The upgrade was refused for good: 403 no grant, 404 no such
        /// endpoint, 400 a bad request, another 4xx.
        case refused(status: Int)
    }

    public static let maxRetries = 6
    public static let retryBaseMs: Double = 500
    public static let retryCapMs: Double = 10000

    public weak var delegate: (any TilePtyDelegate)?
    public private(set) var phase: Phase = .idle {
        didSet { if phase != oldValue { delegate?.tilePty(self, phaseChanged: phase) } }
    }

    private let emulator: any TermEmulator
    private let clock: any TermClock
    private let connectSocket: TilePtyConnect
    private var transport: (any TermTransport)?
    private var gen = 0
    private var opened = false
    private var retries = 0
    private var retryTimer: (any TermTimer)?
    private var done = false

    public init(emulator: any TermEmulator, clock: any TermClock, connect: @escaping TilePtyConnect) {
        self.emulator = emulator
        self.clock = clock
        self.connectSocket = connect
    }

    /// Opens the first socket.
    public func start() {
        guard phase == .idle else { return }
        connect()
    }

    /// Ends this client: the socket closes, nothing reconnects.
    public func close() {
        done = true
        teardown()
        phase = .closed
    }

    /// Tries again now (after a failure, a renewed frame token, or while a
    /// retry is pending).
    public func reconnect() {
        guard phase != .closed else { return }
        done = false
        retries = 0
        teardown()
        connect()
    }

    /// Keys, pastes, the emulator's own replies. Dropped while no socket is open.
    public func send(_ bytes: [UInt8]) {
        guard !bytes.isEmpty, let t = transport, opened else { return }
        t.send(TermCodec.encode(.input(bytes)))
    }

    public func send(_ text: String) { send(Array(text.utf8)) }

    /// The emulator's size changed.
    public func resized(_ size: TermSize) {
        guard let t = transport, opened, size.cols > 0, size.rows > 0 else { return }
        t.send(TermCodec.encode(.resize(cols: size.cols, rows: size.rows)))
    }

    // MARK: the socket

    private func connect() {
        opened = false
        gen += 1
        let g = gen
        retryTimer?.cancel(); retryTimer = nil
        phase = .connecting(attempt: retries)
        let t = connectSocket { [weak self] ev in self?.handle(ev, gen: g) }
        transport = t
        t.start()
    }

    private func teardown() {
        gen += 1
        retryTimer?.cancel(); retryTimer = nil
        let t = transport
        transport = nil
        opened = false
        t?.close()
    }

    private func handle(_ ev: TermTransportEvent, gen g: Int) {
        guard g == gen else { return } // a superseded socket
        switch ev {
        case .opened:
            opened = true
            retries = 0
            phase = .live
            resized(emulator.size)
        case .message(let m):
            switch TermCodec.decode(m) {
            case .output(let b): emulator.write(b)
            case .exit:
                done = true
                teardown()
                phase = .exited
            default: break // a tile backend has no sessions, acks or pongs
            }
        case .closed(let c):
            dropped(c)
        }
    }

    private func dropped(_ c: TermCloseInfo) {
        transport = nil
        opened = false
        if done { return }
        switch c {
        case .refused(401, _):
            fail(.unauthorized); return
        case .refused(let status, _) where status < 500 && status != 429:
            fail(.refused(status: status)); return
        default:
            break // a drop, an outage, a 5xx/429: try again
        }
        guard retries < Self.maxRetries else { fail(.disconnected); return }
        let wait = min(Self.retryBaseMs * Double(1 << retries), Self.retryCapMs)
        retries += 1
        phase = .reconnecting(attempt: retries, delayMs: wait)
        let g = gen
        retryTimer = clock.schedule(after: wait) { [weak self] in
            guard let self, !self.done, g == self.gen else { return }
            self.connect()
        }
    }

    private func fail(_ f: Failure) {
        teardown()
        phase = .failed(f)
    }
}
