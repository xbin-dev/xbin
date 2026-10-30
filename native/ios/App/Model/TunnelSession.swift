import Foundation
import XbinCore

// One tunnelled workspace's live tunnel (D204, D211): its loopback
// forwarder and its iroh connection, brought up on demand and proven by a
// probe before use. TunnelManager.swift has the lifecycle's rules.
//
// Foundation and the app's own types only: app-stubcheck compiles this
// file (Stubs/App/AppStubs.swift stands in for LoopbackForwarder,
// TCPUpstream and NoiseTunnel).

/// One tunnelled workspace's live tunnel.
@MainActor
final class TunnelSession {
    enum Mode {
        /// The real thing: iroh to the workspace's sidecar, Noise inside.
        case iroh(TunnelSecrets)
        /// Debug builds' forwarder (TunnelDebug): TCP to the server itself.
        case plainTCP(host: String, port: Int)
    }

    struct Problem: Error, CustomStringConvertible, Equatable {
        var description: String
    }

    /// One iroh connection, numbered for the diagnostics, and whether a
    /// probe through it got xbind's answer.
    private struct Live {
        let id: Int
        let connection: any TunnelConnection
        var verified = false
    }

    let workspace: String
    let address: TunnelAddress
    private var mode: Mode
    private weak var manager: TunnelManager?
    private var forwarder: LoopbackForwarder?
    /// Forwarders are numbered so a failure report names its own: the
    /// last number given, and the one in use.
    private var forwarderID = 0
    private var currentForwarder = 0
    private var live: Live?
    private var connections = 0
    private var starting: Task<Void, any Error>?
    private var startingID = 0
    /// The epoch ``starting`` was made in.
    private var startingEpoch = 0
    private var watcher: Task<Void, Never>?
    private var retry: Task<Void, Never>?
    private var policy = TunnelReconnectPolicy()
    private var active: Bool
    /// Bumped by every teardown: work begun before it lets go of what it made.
    private var epoch = 0
    /// Requests made in the background, waiting for the front (D211).
    private var waiters: [Int: CheckedContinuation<Void, any Error>] = [:]
    private var nextWaiter = 0
    private var ended = false

    init(workspace: String, port: Int, mode: Mode, manager: TunnelManager, active: Bool = true) {
        self.workspace = workspace
        address = TunnelAddress(port: port)
        self.mode = mode
        self.manager = manager
        self.active = active
        policy.setActive(active)
    }

    var isIroh: Bool {
        if case .iroh = mode { return true }
        return false
    }

    /// The forwarder listens and, through iroh, a connection has answered.
    private var isUp: Bool { forwarder?.isListening == true && (!isIroh || live?.verified == true) }

    var endpointIDs: (device: String, server: String)? {
        guard let c = live?.connection else { return nil }
        return (TunnelUpgrade.hex(c.localId), TunnelUpgrade.hex(c.remoteId))
    }

    /// One line of state for the diagnostics' header.
    var stateLine: String {
        var parts = ["session: \(active ? "front" : "background")"]
        parts.append(forwarder?.isListening == true ? "forwarder listening" : "no forwarder")
        if let l = live { parts.append("connection #\(l.id)\(l.verified ? " (answered)" : " (not yet answered)")") } else { parts.append("no connection") }
        if starting != nil { parts.append("coming up") }
        if retry != nil { parts.append("a retry is due") }
        if !waiters.isEmpty { parts.append("\(waiters.count) request(s) waiting for the front") }
        return parts.joined(separator: ", ")
    }

    private func log(_ s: String) { tunnelLog(s, workspace: workspace) }

    /// Up, or brought up (one attempt at a time; every caller waits on it).
    /// In the background it waits for the front (unless a notification's
    /// work allows it, TunnelManager.inBackground).
    func ready() async throws {
        var rounds = 0
        while true {
            try Task.checkCancellation()
            if ended { throw Problem(description: "this workspace's tunnel was closed") }
            if !active, manager?.allowsBackgroundUse != true {
                try await untilActive()
                continue
            }
            if isUp { return }
            rounds += 1
            if rounds > 3 { throw Problem(description: "the tunnel went down as it came up") }
            let t: Task<Void, any Error>
            let te: Int
            if let s = starting {
                t = s
                te = startingEpoch
            } else {
                startingID += 1
                let id = startingID
                // The epoch as of now, not as of when the task first runs: a
                // teardown or restart in between makes this attempt stale
                // (else two attempts ran at once — two forwarders, two
                // connections — when a restart came first).
                let e = epoch
                t = Task { @MainActor in
                    defer { if self.startingID == id { self.starting = nil } }
                    try await self.bringUp(epoch: e)
                }
                starting = t
                startingEpoch = e
                te = e
            }
            do {
                try await t.value
            } catch {
                if Task.isCancelled { throw error }
                // An attempt the background or a restart made stale (it may
                // fail as what it held was closed): again, as things are now
                // — in the background, that is waiting for the front.
                guard te != epoch else { throw error }
                rounds -= 1
                continue
            }
        }
    }

    private func untilActive() async throws {
        nextWaiter += 1
        let id = nextWaiter
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, any Error>) in
                if active || ended || manager?.allowsBackgroundUse == true {
                    k.resume()
                } else {
                    waiters[id] = k
                }
            }
        } onCancel: {
            Task { @MainActor in self.waiters.removeValue(forKey: id)?.resume(throwing: CancellationError()) }
        }
    }

    private func releaseWaiters() {
        let ws = Array(waiters.values)
        waiters = [:]
        for k in ws { k.resume() }
    }

    private func bringUp(epoch e: Int) async throws {
        guard e == epoch else { throw CancellationError() }
        let started = Date()
        if forwarder?.isListening != true { try await listen(epoch: e) }
        guard e == epoch else { throw CancellationError() }
        guard case .iroh(let secrets) = mode else {
            manager?.setStatus(workspace, .connected(path: .direct, rtt: nil))
            manager?.cameUp(workspace)
            return
        }
        if live?.verified == true { return }
        if live == nil {
            manager?.setStatus(workspace, .connecting)
            do {
                try await connect(secrets, epoch: e)
            } catch {
                guard e == epoch, !(error is CancellationError) else { throw error }
                failed("no connection after \(Self.ms(since: started)) ms: \(Self.describe(error))", error)
                throw error
            }
        }
        guard e == epoch, let l = live else { throw CancellationError() }
        do {
            let status = try await probe(l, secrets)
            guard e == epoch, live?.id == l.id else { throw CancellationError() }
            live?.verified = true
            policy.succeeded()
            log("tunnel up: connection #\(l.id) answered the probe (HTTP \(status)), \(Self.ms(since: started)) ms after the bring-up began")
            if await refreshPath() == false, e == epoch {
                manager?.setStatus(workspace, .connected(path: .relay(host: nil), rtt: nil))
            }
            guard e == epoch, live?.id == l.id else { throw CancellationError() }
            manager?.cameUp(workspace)
        } catch {
            guard e == epoch, !(error is CancellationError) else { throw error }
            markDead(l.id, "it didn't answer the probe: \(Self.describe(error))")
            failed("the tunnel didn't answer after \(Self.ms(since: started)) ms", error)
            throw error
        }
    }

    private func failed(_ line: String, _ error: any Error) {
        log(line)
        manager?.setStatus(workspace, .failed(Self.describe(error)))
        scheduleRetry()
    }

    /// The forwarder on the kept port, or — taken — on a new one, which the
    /// record keeps from now on (open pages reload there: `generation`).
    private func listen(epoch e: Int) async throws {
        let ws = workspace
        forwarderID += 1
        let fid = forwarderID
        let f = LoopbackForwarder(connect: { [weak manager] in
            guard let manager else { throw Problem(description: "the app is shutting down") }
            return try await manager.upstream(ws)
        }, onFailure: { [weak self] why in
            Task { @MainActor in self?.forwarderFailed(fid, why) }
        })
        let want = address.port
        let got: Int
        do {
            got = try await f.start(port: want)
        } catch {
            log("forwarder: port \(want) can't be had (\(Self.describe(error))): another one")
            got = try await f.start(port: 0)
        }
        guard e == epoch else {
            f.stop()
            throw CancellationError()
        }
        forwarder?.stop()
        forwarder = f
        currentForwarder = fid
        log("forwarder: listening on 127.0.0.1:\(got)")
        address.moved(to: got)
        manager?.portChanged(workspace, got)
    }

    /// The forwarder's listener failed under it (iOS reclaimed it, say):
    /// it goes, and a new one comes up when the app is in front.
    private func forwarderFailed(_ id: Int, _ why: String) {
        guard id == currentForwarder, let f = forwarder else { return }
        log("forwarder: its listener failed (\(why)); a new one")
        f.stop()
        forwarder = nil
        if active { Task { try? await self.ready() } }
    }

    /// A fresh endpoint and connection (`tunnel/1`): the relay's token, then
    /// — when that one is refused — the `next` one of a rotation (§7).
    private func connect(_ s: TunnelSecrets, epoch e: Int) async throws {
        drop()
        guard let dialer = TunnelRuntime.dialer else { throw Problem(description: "this build can't open tunnels") }
        var tokens: [(token: String?, isNext: Bool)] = [(s.relay.token, false)]
        if let n = s.next, n.token != s.relay.token { tokens.append((n.token, true)) }
        var last: any Error = Problem(description: "no relay to reach the workspace through")
        for t in tokens {
            let started = Date()
            log("connecting\(t.isNext ? " with the rotation's next relay token" : "")")
            do {
                let box = DialBox()
                let c = try await withDeadline(30, "reaching the workspace's tunnel", onTimeout: { await box.abandon() }) {
                    let c = try await dialer.connect(irohSecret: s.irohSecret, ticket: s.ticket, relayToken: t.token, alpn: TunnelALPN.tunnel)
                    guard await box.keep(c) else { throw CancellationError() }
                    return c
                }
                guard e == epoch else {
                    await c.close()
                    throw CancellationError()
                }
                if !s.endpoint.isEmpty, TunnelUpgrade.hex(c.remoteId) != s.endpoint.lowercased() {
                    await c.close()
                    throw Problem(description: "the tunnel answered as another server than this workspace's")
                }
                connections += 1
                let l = Live(id: connections, connection: c)
                live = l
                log("connection #\(l.id): connected in \(Self.ms(since: started)) ms (device \(TunnelLog.shortID(TunnelUpgrade.hex(c.localId))))")
                if t.isNext, case .iroh(var cur) = mode, let n = cur.next {
                    // The rotation's relay token works: it is the token now.
                    cur.relay = n
                    cur.next = nil
                    mode = .iroh(cur)
                    await KeychainTunnelVault().save(cur, workspace: workspace)
                }
                watch(l)
                return
            } catch {
                if error is CancellationError { throw error }
                log("connect failed after \(Self.ms(since: started)) ms: \(Self.describe(error))")
                last = error
            }
        }
        throw last
    }

    /// A quick request through the new connection (TunnelProbe): a stream,
    /// its handshake and xbind's answer, within the probe's deadline.
    private func probe(_ l: Live, _ s: TunnelSecrets) async throws -> Int {
        let c = l.connection
        let host = address.origin.authority
        let stream = try await c.openStream()   // local: a new connection has room for streams
        return try await withDeadline(TunnelProbe.deadline, "the tunnel's probe", onTimeout: { await stream.cancel() }) {
            try await NoiseTunnel.probe(stream, secrets: s, serverEndpoint: c.remoteId, deviceEndpoint: c.localId, host: host)
        }
    }

    /// Notices the connection closing while in front: reconnects.
    private func watch(_ l: Live) {
        watcher?.cancel()
        watcher = nil
        guard let d = l.connection as? any TunnelConnectionDetails else { return }
        let id = l.id
        watcher = Task { [weak self] in
            let why = await d.closed()
            guard let self, !Task.isCancelled else { return }
            let reason = why.isEmpty ? "the connection closed" : why
            guard self.markDead(id, "it closed: \(reason)") else { return }
            self.manager?.setStatus(self.workspace, .failed(reason))
            self.scheduleRetry()
        }
    }

    /// Connection `id`, if it is still the one in use, goes (closed in the
    /// background); true when it was.
    @discardableResult
    private func markDead(_ id: Int, _ why: String) -> Bool {
        guard let l = live, l.id == id else { return false }
        log("connection #\(id) goes: \(why)")
        live = nil
        watcher?.cancel()
        watcher = nil
        let c = l.connection
        Task { await c.close() }
        return true
    }

    private func scheduleRetry() {
        guard active, retry == nil, let delay = policy.failed() else { return }
        log("trying again in \(Self.seconds(delay))")
        retry = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9))
            guard let self, !Task.isCancelled else { return }
            self.retry = nil
            try? await self.ready()
        }
    }

    /// Lets go of the connection (the forwarder stays).
    private func drop() {
        if let l = live { markDead(l.id, "a new connection replaces it") }
        watcher?.cancel()
        watcher = nil
    }

    /// One accepted connection's upstream: a new iroh stream through its own
    /// IK handshake (spec §3, §4) — or, Debug, TCP to the server itself. A
    /// stream or handshake that fails marks the connection dead; the request
    /// gets one more try, on a new one.
    func upstream() async throws -> any ForwardUpstream {
        if case .plainTCP(let host, let port) = mode {
            #if DEBUG
            return try await TCPUpstream.open(host: host, port: port)
            #else
            throw Problem(description: "no tunnel in this build (\(host):\(port))")
            #endif
        }
        var last: any Error = Problem(description: "the tunnel isn't connected")
        for attempt in 1...2 {
            try await ready()
            guard let l = live, l.verified, case .iroh(let s) = mode else { continue }
            do {
                return try await open(l, s)
            } catch {
                last = error
                if markDead(l.id, "a stream through it failed (\(Self.describe(error)))") {
                    manager?.setStatus(workspace, .connecting)
                } else if attempt == 1 {
                    log("a stream failed on connection #\(l.id), already replaced: \(Self.describe(error))")
                }
            }
        }
        throw last
    }

    private func open(_ l: Live, _ s: TunnelSecrets) async throws -> any ForwardUpstream {
        let c = l.connection
        // Opening a stream is local while the peer allows more (it waits at
        // the 128 a connection may have, §3); the handshake is the round trip.
        let stream = try await c.openStream()
        let channel = try await withDeadline(10, "the tunnel's handshake", onTimeout: { await stream.cancel() }) {
            try await NoiseTunnel.openTunnel(stream, secrets: s, serverEndpoint: c.remoteId, deviceEndpoint: c.localId)
        }
        return NoiseUpstream(channel: channel)
    }

    func update(_ s: TunnelSecrets) {
        if case .iroh = mode { mode = .iroh(s) }
    }

    // MARK: Lifecycle

    #if DEBUG
    func emulateReclaim() { forwarder?.emulateReclaim() }
    #endif

    /// The front again. `fresh` (back from the background): whatever came
    /// up while there goes first — iOS broke its sockets (D211).
    func activate(fresh: Bool, prewarm: Bool) {
        if fresh, forwarder != nil || live != nil {
            log("back in front: the tunnel made while in the background goes")
            if let c = teardown() { Task { await c.close() } }
        }
        active = true
        policy.setActive(true)
        let waited = !waiters.isEmpty
        if waited { log("\(waiters.count) request(s) made in the background go on") }
        releaseWaiters()
        if prewarm || waited { Task { try? await self.ready() } }
    }

    /// The background: everything stops at once; the connection is the
    /// caller's to close (TunnelManager closes them in parallel).
    func suspend(reason: String) -> (any TunnelConnection)? {
        let had = forwarder != nil || live != nil || starting != nil
        active = false
        policy.setActive(false)
        let c = teardown()
        if had { log("down: \(reason)") }
        manager?.setStatus(workspace, .connecting)
        return c
    }

    /// For good (the workspace is gone): what waits for it fails.
    func end(reason: String) -> (any TunnelConnection)? {
        ended = true
        let c = suspend(reason: reason)
        let ws = Array(waiters.values)
        waiters = [:]
        for k in ws { k.resume(throwing: Problem(description: "this workspace's tunnel was closed")) }
        return c
    }

    private func teardown() -> (any TunnelConnection)? {
        epoch += 1
        retry?.cancel()
        retry = nil
        starting = nil
        watcher?.cancel()
        watcher = nil
        forwarder?.stop()
        forwarder = nil
        let c = live?.connection
        live = nil
        return c
    }

    /// The network came back or changed: a connect that hangs (begun with
    /// no network) starts over now rather than at its deadline.
    func networkChanged() {
        guard active, !isUp else { return }
        retry?.cancel()
        retry = nil
        policy.succeeded()
        if starting != nil {
            log("network changed while coming up: starting over")
            restart()
        }
        Task { try? await self.ready() }
    }

    func reconnectNow(reason: String) {
        log("reconnecting: \(reason)")
        retry?.cancel()
        retry = nil
        policy.succeeded()
        if let l = live { markDead(l.id, reason) }
        if starting != nil { restart() }
        Task { try? await self.ready() }
    }

    /// The attempt in flight is let go of (it closes what it makes); the
    /// forwarder stays.
    private func restart() {
        epoch += 1
        starting = nil
        if let l = live, !l.verified { markDead(l.id, "a new attempt replaces it") }
    }

    /// Reads the path and RTT into the status; false when there is no
    /// verified connection that can tell.
    func refreshPath() async -> Bool {
        guard let l = live, l.verified, let d = l.connection as? any TunnelConnectionDetails else { return false }
        guard let p = await d.path(), live?.id == l.id else { return false }
        manager?.setStatus(workspace, .connected(path: p.path, rtt: p.rtt))
        return true
    }

    nonisolated static func describe(_ error: any Error) -> String {
        if let e = error as? LocalizedError, let d = e.errorDescription { return d }
        return String(describing: error)
    }

    nonisolated static func ms(since d: Date) -> Int { Int(Date().timeIntervalSince(d) * 1000) }

    nonisolated static func seconds(_ s: Double) -> String {
        s < 10 ? "\((s * 10).rounded() / 10) s" : "\(Int(s.rounded())) s"
    }
}

/// A dial whose deadline may pass before it returns: a connection that
/// arrives after nobody waits for it any more is closed, not leaked.
private actor DialBox {
    private var abandoned = false
    private var connection: (any TunnelConnection)?

    /// The dial returned `c`: false when the deadline passed (then it closes).
    func keep(_ c: any TunnelConnection) async -> Bool {
        if abandoned {
            await c.close()
            return false
        }
        connection = c
        return true
    }

    func abandon() async {
        abandoned = true
        if let c = connection {
            connection = nil
            await c.close()
        }
    }
}

/// A forwarded connection through the xbin layer (TunnelSplice).
private struct NoiseUpstream: ForwardUpstream {
    let channel: NoiseChannel

    func splice(with local: any ByteStream) async {
        do {
            try await TunnelSplice.run(plain: local, channel: channel)
        } catch {
            tunnelTrace("splice ended: \(TunnelSession.describe(error))")
        }
    }
}

