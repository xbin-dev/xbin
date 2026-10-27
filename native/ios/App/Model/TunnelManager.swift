import Foundation
import Observation
import UIKit
import XbinCore

// The app tunnels' runtime (D204; plans/app-tunnel.md Appendix B §4 Phase 3):
// one TunnelSession per tunnelled workspace — its loopback forwarder and
// its iroh connection — in the foreground only. A session starts lazily (the
// first request's `ready()`) or is pre-warmed when the app becomes active
// for the workspaces on screen; the background tears every one down, and
// coming back makes a fresh endpoint (iroh #4289: a UDP socket is broken
// after a suspend). While active a dropped tunnel is retried with backoff,
// and at once when the network changes (NWPathMonitor). Each forwarded TCP
// connection gets its own iroh stream with its own Noise IK handshake
// (NoiseTunnel.openTunnel) and is spliced through it (TunnelSplice). After
// each connect the workspace refreshes its ticket, relay and token from
// `GET /api/xbin/app-tunnel/client` (WorkspaceModel.refreshTunnel).
//
// Foundation and the app's own types only: app-stubcheck compiles this
// file, with Stubs/App/AppStubs.swift standing in for the Apple-only
// LoopbackForwarder and IrohTunnelDialer, and for NoiseTunnel (CryptoKit).

/// Debug builds: `-XbinTunnelForward YES` puts every plain-http workspace
/// that has no tunnel behind a loopback forwarder that splices to its own
/// address over TCP — the app talks to `http://127.0.0.1:<port>` exactly as
/// through a tunnel, with no iroh and no xbin layer (the spike's step 0b;
/// the UI tests run through it with XBIN_E2E_TUNNEL_FORWARD=1). Never in a
/// release build.
enum TunnelDebug {
    static var forwardAll: Bool {
        #if DEBUG
        return UserDefaults.standard.bool(forKey: "XbinTunnelForward")
        #else
        return false
        #endif
    }
}

/// What a connection can say beyond XbinCore's TunnelConnection (IrohLib's
/// does): when it closes, and its path.
protocol TunnelConnectionDetails: Sendable {
    /// Waits until the connection closes; why.
    func closed() async -> String
    /// The selected path — direct (UDP) or through the relay — and its RTT
    /// in seconds.
    func path() async -> (path: TunnelStatus.Path, rtt: Double?)?
}

@MainActor
@Observable
final class TunnelManager {
    static let shared = TunnelManager()

    /// Each tunnelled workspace's state (the Connection row and screen).
    private(set) var statuses: [String: TunnelStatus] = [:]

    @ObservationIgnored private var sessions: [String: TunnelSession] = [:]
    @ObservationIgnored private(set) var isActive = true
    @ObservationIgnored private var pathWatch: TunnelPathWatch?

    /// Sets XbinCore's seams for pairing and the device upgrade
    /// (``TunnelRuntime``): IrohLib's dialer, and HTTP through a paired
    /// workspace's tunnel for its first device login.
    func installRuntime() {
        TunnelRuntime.dialer = IrohTunnelDialer()
        let m = self
        TunnelRuntime.signInTransport = { record, secrets in try await m.signInTransport(record, secrets) }
    }

    /// The transport of a workspace with a tunnel (`secrets`, its Keychain
    /// item), or Debug's forwarder; nil: it goes direct.
    func transport(for record: WorkspaceRecord, secrets: TunnelSecrets?) -> (any WorkspaceTransport)? {
        let mode: TunnelSession.Mode
        if let secrets {
            mode = .iroh(secrets)
        } else if TunnelDebug.forwardAll, record.server.scheme == "http" {
            mode = .plainTCP(host: record.server.host, port: record.server.port ?? 80)
        } else {
            return nil
        }
        let session = self.session(for: record, mode: mode)
        let id = record.id
        return TunnelTransport(publicOrigin: record.server, address: session.address) { [weak self] in
            guard let self else { throw TunnelSession.Problem(description: "the app is shutting down") }
            try await self.ready(id)
        }
    }

    private func session(for record: WorkspaceRecord, mode: TunnelSession.Mode) -> TunnelSession {
        if let s = sessions[record.id] {
            if case .iroh(let secrets) = mode { s.update(secrets) }
            return s
        }
        let s = TunnelSession(workspace: record.id, port: record.tunnel?.port ?? TunnelAddress.freshPort(), mode: mode,
                              manager: self)
        sessions[record.id] = s
        if statuses[record.id] == nil { statuses[record.id] = .connecting }
        if pathWatch == nil, isActive { pathWatch = TunnelPathWatch { [weak self] ok in self?.pathChanged(ok) } }
        return s
    }

    /// HTTP over a just-paired workspace's tunnel (its first device login):
    /// the session the workspace keeps using once it is added.
    func signInTransport(_ record: WorkspaceRecord, _ secrets: TunnelSecrets) async throws -> any APITransport {
        guard let t = transport(for: record, secrets: secrets) else {
            throw TunnelSession.Problem(description: "no tunnel for this workspace")
        }
        _ = try await t.ready()
        return t
    }

    func ready(_ id: String) async throws {
        guard let s = sessions[id] else { throw TunnelSession.Problem(description: "this workspace has no tunnel") }
        try await s.ready()
    }

    /// `.direct` for a workspace without a tunnel.
    func status(of id: String) -> TunnelStatus {
        sessions[id] == nil ? .direct : (statuses[id] ?? .connecting)
    }

    /// The device's and the server's EndpointIds (hex), once connected.
    func endpointIDs(of id: String) -> (device: String, server: String)? { sessions[id]?.endpointIDs }

    func setStatus(_ id: String, _ s: TunnelStatus) {
        if statuses[id] != s { statuses[id] = s }
    }

    // MARK: Lifecycle

    /// Foreground: sessions may run again; the ones on screen start now.
    func becameActive(prewarm shown: Set<String>) {
        isActive = true
        if pathWatch == nil, !sessions.isEmpty {
            pathWatch = TunnelPathWatch { [weak self] ok in self?.pathChanged(ok) }
        }
        for (id, s) in sessions { s.setActive(prewarm: shown.contains(id)) }
    }

    /// Background: every listener, connection and endpoint goes, inside a
    /// background task so the closes reach the network. A pairing's session
    /// that never became a workspace (its item is gone) goes for good.
    func enteredBackground() {
        isActive = false
        pathWatch?.stop()
        pathWatch = nil
        for (id, s) in sessions where s.isIroh && KeychainTunnelVault.read(id) == nil && AppModel.shared.workspace(id) == nil {
            sessions[id] = nil
            statuses[id] = nil
            Task { await s.suspend() }
        }
        let all = Array(sessions.values)
        guard !all.isEmpty else { return }
        let task = BackgroundTask.begin("xbin tunnels")
        Task {
            for s in all { await s.suspend() }
            task.end()
        }
    }

    private func pathChanged(_ ok: Bool) {
        guard ok, isActive else { return }
        for s in sessions.values { s.networkChanged() }
    }

    /// Settings → Connection → Reconnect.
    func reconnect(_ id: String) { sessions[id]?.reconnectNow() }

    /// The Connection screen's path and RTT, read now.
    func refreshPath(_ id: String) async { await sessions[id]?.refreshPath() }

    /// The workspace is gone: its tunnel stops (its Keychain item goes with
    /// the workspace's other keys, WorkspaceModel.forget).
    func forget(_ id: String) async {
        if let s = sessions.removeValue(forKey: id) { await s.suspend() }
        statuses[id] = nil
    }

    /// New secrets from the workspace (a refresh): the next connect uses them.
    func secretsChanged(_ id: String, _ s: TunnelSecrets) { sessions[id]?.update(s) }

    // MARK: From the sessions

    func upstream(_ id: String) async throws -> any ForwardUpstream {
        guard let s = sessions[id] else { throw TunnelSession.Problem(description: "this workspace has no tunnel") }
        return try await s.upstream()
    }

    fileprivate func portChanged(_ id: String, _ port: Int) {
        AppModel.shared.workspace(id)?.tunnelPortChanged(port)
    }

    fileprivate func connected(_ id: String) {
        Task { await AppModel.shared.workspace(id)?.refreshTunnel() }
    }
}

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

    let workspace: String
    let address: TunnelAddress
    private var mode: Mode
    private weak var manager: TunnelManager?
    private var forwarder: LoopbackForwarder?
    private var connection: (any TunnelConnection)?
    private var starting: Task<Void, any Error>?
    private var watcher: Task<Void, Never>?
    private var retry: Task<Void, Never>?
    private var policy = TunnelReconnectPolicy()
    private var active = true
    /// Bumped by every teardown: work begun before it lets go of what it made.
    private var epoch = 0

    init(workspace: String, port: Int, mode: Mode, manager: TunnelManager) {
        self.workspace = workspace
        address = TunnelAddress(port: port)
        self.mode = mode
        self.manager = manager
    }

    var isIroh: Bool {
        if case .iroh = mode { return true }
        return false
    }

    private var isUp: Bool { forwarder?.isListening == true && (!isIroh || connection != nil) }

    var endpointIDs: (device: String, server: String)? {
        guard let c = connection else { return nil }
        return (TunnelUpgrade.hex(c.localId), TunnelUpgrade.hex(c.remoteId))
    }

    /// Up, or brought up (one attempt at a time; every caller waits on it).
    func ready() async throws {
        if isUp { return }
        if let t = starting { return try await t.value }
        let t = Task { @MainActor in try await self.bringUp() }
        starting = t
        defer { if starting == t { starting = nil } }
        try await t.value
    }

    private func bringUp() async throws {
        let e = epoch
        if forwarder?.isListening != true { try await listen(epoch: e) }
        guard e == epoch else { throw CancellationError() }
        guard case .iroh(let secrets) = mode else {
            manager?.setStatus(workspace, .connected(path: .direct, rtt: nil))
            return
        }
        if connection != nil { return }
        manager?.setStatus(workspace, .connecting)
        do {
            try await connect(secrets, epoch: e)
            policy.succeeded()
        } catch {
            guard e == epoch else { throw error }
            manager?.setStatus(workspace, .failed(Self.describe(error)))
            scheduleRetry()
            throw error
        }
    }

    /// The forwarder on the kept port, or — taken — on a new one, which the
    /// record keeps from now on (open pages reload there: `generation`).
    private func listen(epoch e: Int) async throws {
        let ws = workspace
        let f = LoopbackForwarder(connect: { [weak manager] in
            guard let manager else { throw Problem(description: "the app is shutting down") }
            return try await manager.upstream(ws)
        })
        let want = address.port
        let got: Int
        do {
            got = try await f.start(port: want)
        } catch {
            got = try await f.start(port: 0)
        }
        guard e == epoch else {
            f.stop()
            throw CancellationError()
        }
        forwarder?.stop()
        forwarder = f
        address.moved(to: got)
        manager?.portChanged(workspace, got)
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
            do {
                let c = try await withDeadline(30, "reaching the workspace's tunnel") {
                    try await dialer.connect(irohSecret: s.irohSecret, ticket: s.ticket, relayToken: t.token, alpn: TunnelALPN.tunnel)
                }
                guard e == epoch else {
                    await c.close()
                    throw CancellationError()
                }
                if !s.endpoint.isEmpty, TunnelUpgrade.hex(c.remoteId) != s.endpoint.lowercased() {
                    await c.close()
                    throw Problem(description: "the tunnel answered as another server than this workspace's")
                }
                connection = c
                if t.isNext, case .iroh(var cur) = mode, let n = cur.next {
                    // The rotation's relay token works: it is the token now.
                    cur.relay = n
                    cur.next = nil
                    mode = .iroh(cur)
                    await KeychainTunnelVault().save(cur, workspace: workspace)
                }
                watch(c, epoch: e)
                await refreshPath()
                manager?.connected(workspace)
                return
            } catch {
                if error is CancellationError { throw error }
                last = error
            }
        }
        throw last
    }

    /// Notices the connection closing while in front: reconnects.
    private func watch(_ c: any TunnelConnection, epoch e: Int) {
        watcher?.cancel()
        guard let d = c as? any TunnelConnectionDetails else { return }
        let gen = e
        watcher = Task { [weak self] in
            let why = await d.closed()
            guard let self, !Task.isCancelled, gen == self.epoch, self.connection != nil else { return }
            self.connection = nil
            await c.close()
            self.manager?.setStatus(self.workspace, .failed(why.isEmpty ? "the connection closed" : why))
            guard self.active else { return }
            self.scheduleRetry()
        }
    }

    private func scheduleRetry() {
        guard active, retry == nil, let delay = policy.failed() else { return }
        retry = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9))
            guard let self, !Task.isCancelled else { return }
            self.retry = nil
            try? await self.ready()
        }
    }

    /// Lets go of the connection (the forwarder stays).
    private func drop() {
        watcher?.cancel()
        watcher = nil
        if let c = connection {
            connection = nil
            Task { await c.close() }
        }
    }

    /// One accepted connection's upstream: a new iroh stream through its own
    /// IK handshake (spec §3, §4) — or, Debug, TCP to the server itself.
    func upstream() async throws -> any ForwardUpstream {
        switch mode {
        case .plainTCP(let host, let port):
            #if DEBUG
            return try await TCPUpstream.open(host: host, port: port)
            #else
            throw Problem(description: "no tunnel in this build (\(host):\(port))")
            #endif
        case .iroh(let s):
            try await ready()
            guard let c = connection else { throw Problem(description: "the tunnel isn't connected") }
            let stream = try await c.openStream()
            let channel = try await withDeadline(15, "the tunnel's handshake", onTimeout: { await stream.cancel() }) {
                try await NoiseTunnel.openTunnel(stream, secrets: s, serverEndpoint: c.remoteId, deviceEndpoint: c.localId)
            }
            return NoiseUpstream(channel: channel)
        }
    }

    func update(_ s: TunnelSecrets) {
        if case .iroh = mode { mode = .iroh(s) }
    }

    // MARK: Lifecycle

    func setActive(prewarm: Bool) {
        active = true
        policy.setActive(true)
        if prewarm { Task { try? await self.ready() } }
    }

    /// The background, or the workspace going: everything closes.
    func suspend() async {
        epoch += 1
        active = false
        policy.setActive(false)
        retry?.cancel()
        retry = nil
        starting = nil
        watcher?.cancel()
        watcher = nil
        forwarder?.stop()
        forwarder = nil
        let c = connection
        connection = nil
        await c?.close()
        manager?.setStatus(workspace, .connecting)
    }

    func networkChanged() {
        guard active, !isUp, starting == nil else { return }
        retry?.cancel()
        retry = nil
        policy.succeeded()
        Task { try? await self.ready() }
    }

    func reconnectNow() {
        retry?.cancel()
        retry = nil
        policy.succeeded()
        drop()
        Task { try? await self.ready() }
    }

    func refreshPath() async {
        guard let d = connection as? any TunnelConnectionDetails, let p = await d.path() else { return }
        manager?.setStatus(workspace, .connected(path: p.path, rtt: p.rtt))
    }

    static func describe(_ error: any Error) -> String {
        if let e = error as? LocalizedError, let d = e.errorDescription { return d }
        return String(describing: error)
    }
}

/// A forwarded connection through the xbin layer (TunnelSplice).
private struct NoiseUpstream: ForwardUpstream {
    let channel: NoiseChannel

    func splice(with local: any ByteStream) async {
        try? await TunnelSplice.run(plain: local, channel: channel)
    }
}

/// A UIKit background task: a little time to close the tunnels in.
@MainActor
final class BackgroundTask {
    private var id: UIBackgroundTaskIdentifier = .invalid

    static func begin(_ name: String) -> BackgroundTask {
        let t = BackgroundTask()
        t.id = UIApplication.shared.beginBackgroundTask(withName: name) { [weak t] in t?.end() }
        return t
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}
