import Foundation
import Observation
import UIKit
import XbinCore

// The app tunnels' runtime (D204; plans/app-tunnel.md Appendix B §4 Phase 3):
// one TunnelSession per tunnelled workspace — its loopback forwarder and
// its iroh endpoint — in the foreground only. A session starts lazily (the
// first request's `ready()`) or is pre-warmed when the app becomes active
// for the workspaces on screen; the background tears every one down, and
// coming back makes a fresh endpoint (iroh #4289: a UDP socket is broken
// after a suspend). While active a dropped tunnel is retried with backoff,
// and at once when the network changes (NWPathMonitor). After each connect
// the workspace refreshes its ticket, relay and token from
// `GET /api/xbin/app-tunnel/client` (WorkspaceModel.refreshTunnel).
//
// Foundation and the app's own types only: app-stubcheck compiles this
// file, with Stubs/App/AppStubs.swift standing in for the Apple-only
// LoopbackForwarder and TunnelEndpoint.

/// Debug builds: `-XbinTunnelForward YES` puts every plain-http workspace
/// behind a loopback forwarder that splices to its own address over TCP —
/// the app talks to `http://127.0.0.1:<port>` exactly as through a tunnel,
/// with no iroh and no xbin layer (the spike's step 0b; the UI tests run
/// through it). Never in a release build.
enum TunnelDebug {
    static var forwardAll: Bool {
        #if DEBUG
        return UserDefaults.standard.bool(forKey: "XbinTunnelForward") || dumbpipeTicket != nil
        #else
        return false
        #endif
    }

    /// `-XbinTunnelDumbpipe <ticket>` (Debug, the spike): the forwarder's
    /// upstream is a real iroh endpoint as the tunnel makes it (Minimal, the
    /// ticket's relay only) dialing n0's dumbpipe (`dumbpipe listen-tcp
    /// --host <xbind>`), which splices to the xbind — iroh end to end, with
    /// no xbin layer.
    static var dumbpipeTicket: String? {
        #if DEBUG
        guard let t = UserDefaults.standard.string(forKey: "XbinTunnelDumbpipe"), !t.isEmpty else { return nil }
        return t
        #else
        return nil
        #endif
    }
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

    /// The transport of a workspace with a tunnel — a Keychain item
    /// (`secrets`), or the Debug forwarder — else nil (it goes direct).
    func transport(for record: WorkspaceRecord, secrets: TunnelSecrets?) -> (any WorkspaceTransport)? {
        let mode: TunnelSession.Mode
        if let secrets {
            mode = .iroh(secrets)
        } else if let t = TunnelDebug.dumbpipeTicket, record.server.scheme == "http" {
            mode = .dumbpipe(ticket: t)
        } else if TunnelDebug.forwardAll, record.server.scheme == "http" {
            mode = .plainTCP(host: record.server.host, port: record.server.port ?? 80)
        } else {
            return nil
        }
        let session: TunnelSession
        if let s = sessions[record.id] {
            session = s
        } else {
            session = TunnelSession(workspace: record.id, port: record.tunnel?.port ?? TunnelAddress.freshPort(), mode: mode,
                                    manager: self)
            sessions[record.id] = session
        }
        if statuses[record.id] == nil { statuses[record.id] = .connecting }
        let id = record.id
        return TunnelTransport(publicOrigin: record.server, address: session.address) { [weak self] in
            guard let self else { throw TunnelSession.Problem(description: "the app is shutting down") }
            try await self.ready(id)
        }
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
    /// background task so the closes reach the network.
    func enteredBackground() {
        isActive = false
        pathWatch?.stop()
        pathWatch = nil
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

    /// The workspace is gone: its tunnel stops and its Keychain item goes.
    func forget(_ id: String) async {
        if let s = sessions.removeValue(forKey: id) { await s.suspend() }
        statuses[id] = nil
        TunnelKeyStore.delete(id)
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

    // MARK: Launch

    /// Drops the Keychain items of workspaces the saved list doesn't have
    /// (a reinstall, a crash mid-pairing) — only when the list read cleanly:
    /// never on a Debug fresh start (its list is a scratch one), never when
    /// the saved list couldn't be decoded.
    static func collectGarbage(keeping ids: Set<String>, listReadCleanly: Bool) {
        guard listReadCleanly, !WorkspaceListFile.isFreshStart else { return }
        TunnelKeyStore.collectGarbage(keeping: ids)
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
        /// Debug builds' spike (TunnelDebug.dumbpipeTicket): iroh to n0's
        /// dumbpipe in front of the server, a connection per stream.
        case dumbpipe(ticket: String)
    }

    struct Problem: Error, CustomStringConvertible, Equatable {
        var description: String
    }

    let workspace: String
    let address: TunnelAddress
    private var mode: Mode
    private weak var manager: TunnelManager?
    private var forwarder: LoopbackForwarder?
    private var endpoint: TunnelEndpoint?
    private var serverID: Data?
    private var connected = false
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

    private var isPlain: Bool {
        if case .plainTCP = mode { return true }
        return false
    }

    private var isUp: Bool { forwarder?.isListening == true && (isPlain || (connected && endpoint != nil)) }

    var endpointIDs: (device: String, server: String)? {
        guard let ep = endpoint, let sid = serverID else { return nil }
        return (TunnelEndpoint.hex(ep.deviceID), TunnelEndpoint.hex(sid))
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
        if case .dumbpipe(let ticket) = mode {
            if connected, endpoint != nil { return }
            manager?.setStatus(workspace, .connecting)
            do {
                try await connectDumbpipe(ticket, epoch: e)
            } catch {
                guard e == epoch else { throw error }
                manager?.setStatus(workspace, .failed(Self.describe(error)))
                scheduleRetry()
                throw error
            }
            return
        }
        guard case .iroh(let secrets) = mode else {
            manager?.setStatus(workspace, .connected(path: .direct, rtt: nil))
            return
        }
        if connected, endpoint != nil { return }
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

    /// A fresh endpoint and connection: the ticket's relay with its token,
    /// then — when that one refuses — the `next` relay of a rotation.
    private func connect(_ s: TunnelSecrets, epoch e: Int) async throws {
        drop()
        let primary = try TunnelEndpoint.relay(of: s.ticket, fallback: s.relay.url)
        var tries: [(url: String, token: String?, isNext: Bool)] = [(primary, s.relay.token, false)]
        if let n = s.next, let u = n.url ?? s.relay.url { tries.append((u, n.token, true)) }
        var last: any Error = Problem(description: "no relay to reach the workspace through")
        for t in tries {
            var made: TunnelEndpoint?
            do {
                let ep = try await withDeadline(15, "starting the tunnel") {
                    try await TunnelEndpoint.bind(secret: s.irohSecret, relayURL: t.url, relayToken: t.token)
                }
                made = ep
                let sid = try await withDeadline(25, "reaching the workspace's tunnel", onTimeout: { await ep.close() }) {
                    try await ep.connect(ticket: s.ticket, expect: s.endpoint)
                }
                guard e == epoch else {
                    await ep.close()
                    throw CancellationError()
                }
                endpoint = ep
                serverID = sid
                connected = true
                if t.isNext, case .iroh(var cur) = mode, let n = cur.next {
                    // The rotation's relay works: it is the relay now.
                    cur.relay = n
                    cur.next = nil
                    mode = .iroh(cur)
                    TunnelKeyStore.write(cur, workspace: workspace)
                }
                watch(ep, epoch: e)
                await refreshPath()
                manager?.connected(workspace)
                return
            } catch {
                await made?.close()
                if error is CancellationError { throw error }
                last = error
            }
        }
        throw last
    }

    /// Debug's spike: a fresh endpoint (a random key each time) on the
    /// ticket's relay, and one connection to see that dumbpipe answers.
    private func connectDumbpipe(_ ticket: String, epoch e: Int) async throws {
        drop()
        let relay = try TunnelEndpoint.relay(of: ticket, fallback: nil)
        let started = Date()
        let ep = try await withDeadline(15, "starting the tunnel") {
            try await TunnelEndpoint.bind(secret: TunnelEndpoint.newSecret(), relayURL: relay, relayToken: nil)
        }
        do {
            let sid = try await withDeadline(25, "reaching dumbpipe", onTimeout: { await ep.close() }) {
                try await ep.connect(ticket: ticket, alpn: Data("DUMBPIPEV0".utf8))
            }
            guard e == epoch else { throw CancellationError() }
            endpoint = ep
            serverID = sid
            connected = true
            NSLog("xbin-tunnel dumbpipe connected in %.0f ms", Date().timeIntervalSince(started) * 1000)
            watch(ep, epoch: e)
            await refreshPath()
        } catch {
            await ep.close()
            throw error
        }
    }

    /// Notices the connection closing while in front: reconnects.
    private func watch(_ ep: TunnelEndpoint, epoch e: Int) {
        watcher?.cancel()
        watcher = Task { [weak self] in
            let why = await ep.closed()
            guard let self, !Task.isCancelled, e == self.epoch, self.endpoint === ep else { return }
            self.connected = false
            self.endpoint = nil
            await ep.close()
            self.manager?.setStatus(self.workspace, .failed(why ?? "the connection closed"))
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

    /// Lets go of the endpoint (the forwarder stays).
    private func drop() {
        watcher?.cancel()
        watcher = nil
        connected = false
        if let ep = endpoint {
            endpoint = nil
            Task { await ep.close() }
        }
    }

    /// One accepted connection's upstream: a new bi-stream through its own
    /// IK handshake (spec §3, §4) — or, Debug, TCP to the server itself.
    func upstream() async throws -> any ForwardUpstream {
        switch mode {
        case .plainTCP(let host, let port):
            #if DEBUG
            return try await TCPUpstream.open(host: host, port: port)
            #else
            throw Problem(description: "no tunnel in this build (\(host):\(port))")
            #endif
        case .dumbpipe(let ticket):
            try await ready()
            guard let ep = endpoint else { throw Problem(description: "the tunnel isn't connected") }
            let started = Date()
            let s = try await ep.openDedicated(ticket: ticket, alpn: Data("DUMBPIPEV0".utf8), hello: Data("hello".utf8))
            NSLog("xbin-tunnel dumbpipe stream in %.0f ms", Date().timeIntervalSince(started) * 1000)
            return StreamUpstream(remote: s)
        case .iroh(let s):
            try await ready()
            guard let ep = endpoint, let sid = serverID else { throw Problem(description: "the tunnel isn't connected") }
            let stream = try await ep.openBi()
            let prologue = Data("xbin-tunnel-v1".utf8) + sid + ep.deviceID
            do {
                let session = try await withDeadline(10, "the tunnel's handshake") {
                    try await TunnelXbinLayer.handshake(stream, deviceStatic: s.noiseStatic, serverStatic: s.serverKey, prologue: prologue)
                }
                return NoiseUpstream(remote: stream, session: session)
            } catch {
                await stream.cancel()
                throw error
            }
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
        connected = false
        let ep = endpoint
        endpoint = nil
        await ep?.close()
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
        if !isPlain { drop() }
        Task { try? await self.ready() }
    }

    func refreshPath() async {
        guard let ep = endpoint, connected, let p = await ep.path() else { return }
        manager?.setStatus(workspace, .connected(path: p.path, rtt: p.rtt))
    }

    static func describe(_ error: any Error) -> String {
        if let e = error as? LocalizedError, let d = e.errorDescription { return d }
        return String(describing: error)
    }
}

/// A forwarded connection through the xbin layer.
private struct NoiseUpstream: ForwardUpstream {
    let remote: any ByteStream
    let session: TunnelXbinLayer.Session

    func splice(with local: any ByteStream) async {
        try? await TunnelXbinLayer.splice(local: local, remote: remote, session: session)
        await remote.cancel()
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
