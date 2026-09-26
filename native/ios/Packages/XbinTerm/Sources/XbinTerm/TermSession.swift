// TermSession.swift — one terminal's connection to xbind's /ws/term, the
// native twin of web/bx-terminal.js's socket half (plans/native.md §12).
//
// connect → (socket open: send the size) → `session` frame → scrollback
// replay → live. The session frame names the session; from then on a drop
// reattaches by id with bx-terminal's backoff (500 ms doubling to 10 s, eight
// tries). A gone session (404, or — when the transport can't tell why — a
// reattach that twice fails to open) starts a fresh one on the same tile.
// Unlike the browser, every session frame RESETS the emulator first — the
// server replays the whole scrollback on every attach (no offset), so a
// reattach would otherwise print it twice.
//
// Predictive echo (D70/D71): typed input is predicted before it is sent
// (frames are numbered per socket, as the server counts them); an `ack` is
// applied only once the emulator has parsed everything received before it;
// the RTT comes from app-level pings every 5 s while visible, smoothed per
// RFC 6298. The overlay to draw goes to the delegate whenever it changes.
//
// Everything is injected — the socket, the emulator, the clock — so the state
// machine runs under `swift test` on Linux. It lives on the main actor, like
// the emulator view it drives.

import Foundation

// MARK: - What the session drives

/// The terminal emulator (SwiftTerm behind the app's adapter, or a test double).
@MainActor public protocol TermEmulator: AnyObject {
    /// PTY output, in order.
    func write(_ bytes: [UInt8])
    /// Runs `body` once everything written so far has been parsed. An emulator
    /// that parses synchronously in `write` (SwiftTerm's `feed`) calls it at once.
    func afterParsed(_ body: @escaping @MainActor () -> Void)
    /// A full reset — screen, modes and scrollback — before a replay.
    func reset()
    /// The visible screen, for prediction (nil while there is none).
    var framebuffer: (any TermFramebuffer)? { get }
    /// The terminal's size in cells.
    var size: TermSize { get }
}

public struct TermSize: Equatable, Sendable {
    public var cols: Int
    public var rows: Int
    public init(cols: Int, rows: Int) { self.cols = cols; self.rows = rows }
}

/// One WebSocket. The session calls `start()` once, right after creating it;
/// the transport delivers its events on the main actor, never from inside
/// `start()`, `send` or `close()`, and none after `close()`.
@MainActor public protocol TermTransport: AnyObject {
    func start()
    func send(_ msg: TermWireMessage)
    func close()
}

public enum TermTransportEvent: Equatable, Sendable {
    /// The upgrade succeeded.
    case opened
    case message(TermWireMessage)
    /// The socket closed, or never opened.
    case closed(TermCloseInfo)
}

/// Why a socket closed — as precisely as the transport can tell. A browser
/// can't see the upgrade's HTTP status, so bx-terminal guesses (a reattach
/// that twice fails to open is "gone"); a native transport can, so the
/// session knows 404 "no such session" from a network outage.
public enum TermCloseInfo: Equatable, Sendable {
    /// An open socket closed (network drop, xbind shutting down, the server
    /// ending the socket).
    case dropped
    /// An open socket the server closed with a close frame, and its close
    /// code (1000 normal closure, 1001 going away, 1005 none given, 1011 an
    /// error…). Transports that can't see the code report `.dropped`; the
    /// /ws/term session treats both alike, a tile's pty (TilePtySession)
    /// takes a normal closure as the pty's end.
    case serverClosed(code: Int)
    /// The upgrade was answered with this HTTP status instead of 101: 401
    /// signed out, 403 no terminal level / revoked / another user's session,
    /// 404 no such session, 409 an agent session, 400 bad options (an
    /// unavailable VM, net=host with vm=1).
    case refused(status: Int, body: String)
    /// The server was never reached (offline, DNS, TLS, a timeout).
    case unreachable(String)
    /// The socket never opened and the transport can't tell why.
    case failed
}

/// Opens a socket to `path` (TermPaths.socket: path and query on the
/// workspace origin; the app adds scheme, host and credentials).
public typealias TermConnect = @MainActor (_ path: String, _ events: @escaping @MainActor (TermTransportEvent) -> Void) -> any TermTransport

/// Time for the session: monotonic milliseconds and one-shot timers.
@MainActor public protocol TermClock: AnyObject {
    func now() -> Double
    func schedule(after ms: Double, _ body: @escaping @MainActor () -> Void) -> any TermTimer
}

@MainActor public protocol TermTimer: AnyObject {
    func cancel()
}

/// The real clock: ContinuousClock and main-actor tasks.
@MainActor public final class TermSystemClock: TermClock {
    private let origin = ContinuousClock.now
    public init() {}
    public func now() -> Double {
        let d = origin.duration(to: .now).components
        return Double(d.seconds) * 1000 + Double(d.attoseconds) / 1e15
    }
    public func schedule(after ms: Double, _ body: @escaping @MainActor () -> Void) -> any TermTimer {
        let timer = TaskTimer()
        timer.task = Task { @MainActor [weak timer] in
            try? await Task.sleep(for: .milliseconds(max(ms, 0)))
            guard let timer, !timer.cancelled else { return }
            body()
        }
        return timer
    }
    private final class TaskTimer: TermTimer {
        var task: Task<Void, Never>?
        var cancelled = false
        func cancel() { cancelled = true; task?.cancel() }
    }
}

// MARK: - What the session reports

@MainActor public protocol TermSessionDelegate: AnyObject {
    func termSession(_ s: TermSession, phaseChanged phase: TermSession.Phase)
    /// Every `session` frame: the id to reattach with, the effective scope,
    /// the pickable scopes, VM, base-image state.
    func termSession(_ s: TermSession, attached info: TermSessionInfo)
    /// A clamp note ("host networking is admin-only — using the org network"),
    /// once per session id. bx-terminal prints it as one gray line.
    func termSession(_ s: TermSession, netNote: String)
    /// The prediction overlay changed: draw `overlay` over the screen (hide it
    /// while scrolled back). `lagging`: predictions show because the link is
    /// slow or glitchy — bx-terminal's ⚡ RTT badge.
    func termSession(_ s: TermSession, overlay: PredictionRender, lagging: Bool)
    /// The smoothed round-trip time changed (ms).
    func termSession(_ s: TermSession, rtt: Double)
    /// Something worth a line in the terminal or a toast (bx-terminal prints
    /// these in gray).
    func termSession(_ s: TermSession, notice: TermSession.Notice)
}

public extension TermSessionDelegate {
    func termSession(_ s: TermSession, phaseChanged phase: TermSession.Phase) {}
    func termSession(_ s: TermSession, attached info: TermSessionInfo) {}
    func termSession(_ s: TermSession, netNote: String) {}
    func termSession(_ s: TermSession, overlay: PredictionRender, lagging: Bool) {}
    func termSession(_ s: TermSession, rtt: Double) {}
    func termSession(_ s: TermSession, notice: TermSession.Notice) {}
}

// MARK: - The session

@MainActor public final class TermSession {
    public enum Phase: Equatable, Sendable {
        /// Created, not started.
        case idle
        /// A socket is opening (`attempt` 0 = the first try).
        case connecting(attempt: Int)
        /// Open, waiting for the `session` frame.
        case handshaking
        /// Attached: output flows, input is sent.
        case live
        /// The socket dropped; reattaching after `delayMs`.
        case reconnecting(attempt: Int, delayMs: Double)
        /// The shell ended (`exit`). Terminal: the app closes the surface.
        case exited
        /// Gave up; `reconnect()` tries again (after re-signing in, for `.unauthorized`).
        case failed(Failure)
        /// `close()` was called.
        case closed
    }

    public enum Notice: Equatable, Sendable {
        /// The session to reattach is gone: a fresh one is starting on the
        /// same tile ("previous session gone — starting fresh…").
        case previousSessionGone
    }

    public enum Failure: Equatable, Sendable {
        /// The socket dropped and the retries ran out (or a new session's
        /// socket never opened) — bx-terminal's "[disconnected]". A reattach
        /// target is kept: `reconnect()` resumes the same session (call it
        /// when the network comes back).
        case disconnected
        /// 401: the device session expired; sign in again, then `reconnect()`.
        case unauthorized
        /// 403: no terminal level on this tile, or it was revoked.
        case forbidden(String)
        /// The session is gone (404 / reattach never opened) and there is no
        /// tile to start a fresh one on.
        case sessionGone
        /// Any other refusal (400: e.g. no VM terminals here; 409: an agent
        /// session; a new session's 5xx).
        case refused(status: Int, reason: String)
    }

    // bx-terminal's constants
    public static let pingIntervalMs: Double = 5000
    public static let maxRetries = 8
    public static let retryBaseMs: Double = 500
    public static let retryCapMs: Double = 10000
    public static let reattachFailsToRestart = 2

    public weak var delegate: (any TermSessionDelegate)?
    public private(set) var phase: Phase = .idle { didSet { if phase != oldValue { delegate?.termSession(self, phaseChanged: phase) } } }
    /// The last `session` frame.
    public private(set) var info: TermSessionInfo?
    /// What the next socket asks for (a reattach once a session frame named the session).
    public private(set) var target: TermTarget
    /// The smoothed RTT in ms (nil until the first pong).
    public private(set) var srtt: Double?
    /// This xbind acks input and answers pings.
    public private(set) var echoAck = false
    /// The predictive echo engine (read its state; set the mode with `predictMode`).
    public let predictor = Predictor()
    public var predictMode: PredictMode {
        get { predictor.mode }
        set { predictor.setMode(newValue); redraw() }
    }
    /// Pings run only while visible (bx-terminal: document.visibilityState).
    public var visible = true
    /// The last overlay handed to the delegate.
    public private(set) var overlay = PredictionRender.empty
    public private(set) var lagging = false

    private let emulator: any TermEmulator
    private let connectSocket: TermConnect
    private let clock: any TermClock
    private var fresh: TermNewSession?      // where a fresh session starts if the old one is gone
    private var initialInput: [UInt8]?      // typed once into the first session (a sign-in command)
    private var transport: (any TermTransport)?
    private var gen = 0                     // connection epoch: only the latest socket drives the terminal
    private var opened = false
    private var retries = 0
    private var reattachFails = 0
    private var seq: UInt64 = 0             // binary input frames sent on this socket
    private var awaitingReplay = false      // session frame seen, replay not yet
    private var notedSession: String?
    private var pingTimer: (any TermTimer)?
    private var retryTimer: (any TermTimer)?
    private var done = false                // exited or closed: never reconnect

    /// - Parameters:
    ///   - target: a new session on a tile, or a reattach by id.
    ///   - fresh: for a reattach, the tile to start a fresh session on if the
    ///     old one is gone (a `.new` target is its own).
    ///   - initialInput: typed into the first session once it is attached
    ///     (e.g. an agent's sign-in command, newline included).
    public init(target: TermTarget, fresh: TermNewSession? = nil, initialInput: [UInt8]? = nil,
                emulator: any TermEmulator, clock: any TermClock, connect: @escaping TermConnect) {
        self.target = target
        if case .new(let o) = target { self.fresh = o } else { self.fresh = fresh }
        self.initialInput = initialInput
        self.emulator = emulator
        self.clock = clock
        self.connectSocket = connect
    }

    /// Opens the first socket.
    public func start() {
        guard phase == .idle else { return }
        connect()
    }

    /// Ends this client (the session lives on server-side; `DELETE
    /// TermPaths.kill(session:)` ends it). No reconnect follows.
    public func close() {
        done = true
        teardown()
        phase = .closed
    }

    /// Tries again after a failure, or reattaches now while a retry is pending.
    public func reconnect() {
        guard !done || phase == .closed else { return }
        done = false
        retries = 0; reattachFails = 0
        teardown()
        connect()
    }

    /// Starts a brand-new session with these options — a network scope, GPU,
    /// API or VM change, or after the tile's persistent layer was reset. The
    /// caller ends the old session first (`DELETE TermPaths.kill(session:)`).
    public func restart(_ options: TermNewSession) {
        done = false
        fresh = options
        target = .new(options)
        retries = 0; reattachFails = 0
        teardown()
        connect()
    }

    // MARK: input from the app

    /// Bytes for the PTY: typed keys, pastes, the emulator's own replies.
    /// Dropped while no socket is open (as bx-terminal does).
    public func send(_ bytes: [UInt8]) {
        guard !bytes.isEmpty, let t = transport, opened else { return }
        let fb = (echoAck && !awaitingReplay) ? emulator.framebuffer : nil
        if let fb { predictor.newUserData(bytes: bytes, fb, now: clock.now()) } // predict first: it expires with this frame
        seq += 1
        t.send(TermCodec.encode(.input(bytes)))
        predictor.setLocalFrameSent(seq)
        if fb != nil { redraw() }
    }

    public func send(_ text: String) { send(Array(text.utf8)) }

    /// The emulator's size changed.
    public func resized(_ size: TermSize) {
        if let t = transport, opened { t.send(TermCodec.encode(.resize(cols: size.cols, rows: size.rows))) }
        redraw()
    }

    /// The application hid (DECTCEM off) or showed the cursor; a full reset shows it.
    public func cursorVisibilityChanged(hidden: Bool) { predictor.setCursorHidden(hidden) }

    /// The emulator switched buffers (the alternate screen): predictions are void.
    public func bufferChanged() { predictor.reset(); redraw() }

    /// Sends a ping now (the timer does this every 5 s while visible).
    public func ping() {
        guard let t = transport, opened, echoAck else { return }
        t.send(TermCodec.encode(.ping(t: clock.now())))
    }

    /// Re-judges the predictions against the screen and hands the delegate the
    /// overlay if it changed. The session calls it after output is parsed;
    /// the app calls it after anything else that moves the screen.
    public func redraw() {
        var r = PredictionRender.empty
        if let fb = emulator.framebuffer {
            predictor.cull(fb, now: clock.now())
            r = predictor.render(fb)
        }
        let lag = predictor.shown() && (predictor.srttTrigger || predictor.glitchTrigger > 0)
        guard r != overlay || lag != lagging else { return }
        overlay = r; lagging = lag
        delegate?.termSession(self, overlay: r, lagging: lag)
    }

    // MARK: the socket

    private func connect() {
        opened = false
        gen += 1
        let g = gen
        // input frames are numbered per socket (the server counts what it receives)
        seq = 0; echoAck = false; awaitingReplay = false
        predictor.reset(); predictor.setLocalFrameSent(0); predictor.setLateAck(0)
        pingTimer?.cancel(); pingTimer = nil
        retryTimer?.cancel(); retryTimer = nil
        phase = .connecting(attempt: retries)
        let t = connectSocket(TermPaths.socket(target)) { [weak self] ev in self?.handle(ev, gen: g) }
        transport = t
        t.start()
    }

    /// Closes the current socket without a reconnect (its late events are stale).
    private func teardown() {
        gen += 1
        pingTimer?.cancel(); pingTimer = nil
        retryTimer?.cancel(); retryTimer = nil
        let t = transport
        transport = nil; opened = false
        t?.close()
    }

    private func handle(_ ev: TermTransportEvent, gen g: Int) {
        guard g == gen else { return } // a superseded socket must not drive the terminal
        switch ev {
        case .opened:
            retries = 0; opened = true; reattachFails = 0
            phase = .handshaking
            let s = emulator.size
            transport?.send(TermCodec.encode(.resize(cols: s.cols, rows: s.rows)))
        case .message(let m):
            switch TermCodec.decode(m) {
            case .session(let info): attached(info, gen: g)
            case .output(let b): output(b, gen: g)
            case .ack(let n): awaitingReplay = false; ack(n, gen: g)
            case .pong(let t): awaitingReplay = false; pong(t)
            case .exit:
                done = true
                teardown()
                phase = .exited
            case .ignored: break
            }
        case .closed(let c):
            dropped(c)
        }
    }

    private func attached(_ info: TermSessionInfo, gen g: Int) {
        target = .reattach(id: info.id)
        self.info = info
        // The server replays the whole scrollback on every attach: start from a
        // clean screen so a reattach doesn't print it twice.
        emulator.reset()
        predictor.reset(); predictor.setCursorHidden(false)
        awaitingReplay = true
        echoAck = info.echoAck
        phase = .live
        if !info.netNote.isEmpty && info.id != notedSession {
            notedSession = info.id
            delegate?.termSession(self, netNote: info.netNote)
        }
        delegate?.termSession(self, attached: info)
        guard g == gen else { return } // the delegate restarted or closed us
        if echoAck {
            ping()
            schedulePing(gen: g)
        }
        if let run = initialInput {
            initialInput = nil
            send(run)
        }
        redraw()
    }

    private func schedulePing(gen g: Int) {
        pingTimer = clock.schedule(after: Self.pingIntervalMs) { [weak self] in
            guard let self, g == self.gen else { return }
            if self.visible { self.ping() }
            self.schedulePing(gen: g)
        }
    }

    private func output(_ b: [UInt8], gen g: Int) {
        awaitingReplay = false
        emulator.write(b)
        emulator.afterParsed { [weak self] in
            guard let self, g == self.gen else { return }
            self.redraw()
        }
    }

    /// An ack is judged against a screen that includes every output frame
    /// that preceded it on the wire: apply it once the emulator caught up.
    private func ack(_ n: UInt64, gen g: Int) {
        emulator.afterParsed { [weak self] in
            guard let self, g == self.gen else { return } // a newer socket renumbered everything
            self.predictor.setLateAck(n)
            self.redraw()
        }
    }

    private func pong(_ t: Double?) {
        guard let t else { return }
        let r = clock.now() - t
        guard r >= 0 && r < 60000 else { return }
        let s = srttUpdate(srtt, r)
        srtt = s
        predictor.setSrtt(s)
        delegate?.termSession(self, rtt: s)
        redraw()
    }

    private func dropped(_ c: TermCloseInfo) {
        transport = nil
        pingTimer?.cancel(); pingTimer = nil
        if done { return }
        let reattaching: Bool
        if case .reattach = target { reattaching = true } else { reattaching = false }
        switch c {
        case .refused(401, _): fail(.unauthorized); return
        case .refused(403, let body): fail(.forbidden(body)); return
        case .refused(404, _) where reattaching: sessionGone(); return
        case .refused(let status, let body) where status < 500 && status != 429 || !reattaching:
            fail(.refused(status: status, reason: body)); return
        case .failed:
            // A reattach whose handshake never succeeded, for a reason we can't
            // see, almost always means the session is gone (a stale id, an
            // xbind restart): after a couple of tries, start a fresh session
            // instead of retrying a dead id forever (bx-terminal's rule).
            if reattaching && !opened {
                reattachFails += 1
                if reattachFails >= Self.reattachFailsToRestart { sessionGone(); return }
            }
        case .dropped, .serverClosed, .unreachable, .refused:
            break // a drop, an outage, a 5xx/429 on a reattach: retry
        }
        // Reattach by session id with backoff (xbind keeps the PTY).
        if reattaching && retries < Self.maxRetries {
            let wait = min(Self.retryBaseMs * Double(1 << retries), Self.retryCapMs)
            retries += 1
            phase = .reconnecting(attempt: retries, delayMs: wait)
            let g = gen
            retryTimer = clock.schedule(after: wait) { [weak self] in
                guard let self, !self.done, g == self.gen else { return }
                self.connect()
            }
        } else {
            fail(.disconnected)
        }
    }

    private func sessionGone() {
        guard let o = fresh else { fail(.sessionGone); return }
        delegate?.termSession(self, notice: .previousSessionGone)
        restart(o)
    }

    private func fail(_ f: Failure) {
        teardown()
        phase = .failed(f)
    }
}
