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
// Coming back to the front (D211, #42) always ends with a fresh tunnel that
// has answered: nothing comes up while the app is in the background (a
// request then waits for the front — the follow loops of open screens
// retried in the background grace period and brought a tunnel up there,
// whose listener and UDP socket iOS then broke, and which the front reused
// for good); whatever did come up there (a notification's action,
// ``TunnelManager/inBackground(_:)``) goes when the app is back; a tunnel is
// "up" only once a probe through it (TunnelProbe) got xbind's answer; a
// stream or handshake that fails on a connection marks it dead and the
// request is tried again on a new one; a forwarder whose listener fails is
// replaced. What happens is kept in TunnelLog, in every build (Settings →
// Connection → Diagnostics).
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

    /// Debug builds: `-XbinTunnelEmulateSuspend YES` — coming back from the
    /// background, every forwarder that lived through it has its sockets
    /// killed, as iOS does on a device to a suspended app's (TN2277; the
    /// simulator doesn't). The UI tests' resume test runs with it (D211).
    static var emulateSuspend: Bool {
        #if DEBUG
        return UserDefaults.standard.bool(forKey: "XbinTunnelEmulateSuspend")
        #else
        return false
        #endif
    }
}

/// A line in the tunnels' diagnostics (D211): kept in ``TunnelLog`` in
/// every build — Settings → Connection shows it, "Copy diagnostics" hands it
/// over — and, in Debug builds, in the device log (`xbin-tunnel …`: `xcrun
/// simctl spawn <udid> log show --predicate 'eventMessage CONTAINS
/// "xbin-tunnel"'`). Never a key, ticket, psk or token (TunnelLog scrubs
/// what looks like one anyway).
func tunnelLog(_ s: String, workspace: String? = nil) {
    TunnelLog.shared.add(s, workspace: workspace)
    #if DEBUG
    NSLog("xbin-tunnel %@", workspace.map { "[\(TunnelLog.shortWorkspace($0))] \(s)" } ?? s)
    #endif
}

/// A per-connection detail (each request's accept, splice and end): the
/// device log in Debug builds only — the diagnostics keep what matters.
func tunnelTrace(_ s: @autoclosure () -> String) {
    #if DEBUG
    NSLog("xbin-tunnel %@", s())
    #endif
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
    /// The path watch's last report (nil: none yet since it started).
    @ObservationIgnored private var pathOK: Bool?
    /// Work running for a notification while the app is in the background
    /// (``inBackground(_:)``): tunnels may come up for it.
    @ObservationIgnored private var backgroundUses = 0
    @ObservationIgnored private var backgroundSince: Date?

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
        return TunnelTransport(publicOrigin: record.server, address: session.address,
                               offline: { [weak self] in self?.pathOK == false }) { [weak self] in
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
                              manager: self, active: isActive)
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

    /// Whether a tunnel may come up while the app is in the background: only
    /// for work a notification asked for (``inBackground(_:)``).
    var allowsBackgroundUse: Bool { backgroundUses > 0 }

    /// Work the app does for a notification while it may be in the
    /// background (an action that doesn't open the app): its requests may
    /// bring tunnels up, which go again once it is done. Outside it, a
    /// request in the background waits for the front.
    func inBackground(_ work: @MainActor () async -> Void) async {
        backgroundUses += 1
        await work()
        backgroundUses -= 1
        if backgroundUses == 0, !isActive { suspendAll(reason: "the background work is done") }
    }

    /// Foreground: sessions may run again; the ones on screen start now.
    /// Back from the background, a tunnel that came up there goes first:
    /// iOS breaks a suspended app's listening socket and iroh's UDP socket
    /// (iroh #4289), and nothing in either says so.
    func becameActive(prewarm shown: Set<String>) {
        let wasActive = isActive
        #if DEBUG
        if TunnelDebug.emulateSuspend, !wasActive { for s in sessions.values { s.emulateReclaim() } }
        #endif
        isActive = true
        if !wasActive {
            let away = backgroundSince.map { Int(Date().timeIntervalSince($0).rounded()) }
            backgroundSince = nil
            tunnelLog("app: in front again" + (away.map { " after \($0) s in the background" } ?? ""))
        }
        if pathWatch == nil, !sessions.isEmpty {
            pathWatch = TunnelPathWatch { [weak self] ok in self?.pathChanged(ok) }
        }
        for (id, s) in sessions { s.activate(fresh: !wasActive, prewarm: shown.contains(id)) }
    }

    /// Background: every listener, connection and endpoint goes, inside a
    /// background task so the closes reach the network. A pairing's session
    /// that never became a workspace (its item is gone) goes for good.
    func enteredBackground() {
        if isActive { tunnelLog("app: background") }
        isActive = false
        backgroundSince = backgroundSince ?? Date()
        pathWatch?.stop()
        pathWatch = nil
        pathOK = nil
        var closing: [any TunnelConnection] = []
        for (id, s) in sessions where s.isIroh && KeychainTunnelVault.read(id) == nil && AppModel.shared.workspace(id) == nil {
            sessions[id] = nil
            statuses[id] = nil
            if let c = s.end(reason: "the pairing was left") { closing.append(c) }
        }
        close(closing)
        suspendAll(reason: "the app went to the background")
    }

    /// Every session stops — synchronously, so a close that finishes only
    /// after the app is back can't touch what came up since (D211).
    private func suspendAll(reason: String) {
        close(sessions.values.compactMap { $0.suspend(reason: reason) })
    }

    /// Closes connections in parallel, in a background task.
    private func close(_ connections: [any TunnelConnection]) {
        guard !connections.isEmpty else { return }
        let task = BackgroundTask.begin("xbin tunnels")
        Task {
            await withTaskGroup(of: Void.self) { g in
                for c in connections { g.addTask { await c.close() } }
            }
            task.end()
        }
    }

    /// A path update. A new watch's first one only says how things are (a
    /// restart then would race the bring-up the foreground began); later
    /// ones are changes, and a satisfied one restarts a connect that hangs.
    private func pathChanged(_ ok: Bool) {
        guard isActive else { return }
        let last = pathOK
        pathOK = ok
        if ok != last, last != nil || !ok { tunnelLog("network: \(ok ? "a path again" : "no path")") }
        guard ok, last != nil else { return }
        for s in sessions.values { s.networkChanged() }
    }

    /// Settings → Connection → Reconnect.
    func reconnect(_ id: String) { sessions[id]?.reconnectNow(reason: "Reconnect was tapped") }

    /// The Connection screen's path and RTT, read now.
    func refreshPath(_ id: String) async { _ = await sessions[id]?.refreshPath() }

    /// The workspace is gone: its tunnel stops (its Keychain item goes with
    /// the workspace's other keys, WorkspaceModel.forget).
    func forget(_ id: String) async {
        if let s = sessions.removeValue(forKey: id), let c = s.end(reason: "the workspace was removed") { await c.close() }
        statuses[id] = nil
    }

    /// New secrets from the workspace (a refresh): the next connect uses them.
    func secretsChanged(_ id: String, _ s: TunnelSecrets) { sessions[id]?.update(s) }

    /// What "Copy diagnostics" hands over for workspace `id`: the app, the
    /// tunnel's state now, then the log.
    func diagnostics(for w: WorkspaceModel) -> String {
        var header = [
            "xbin \(AppInfo.version) (\(AppInfo.build)) — \(AppInfo.platform) — tunnel diagnostics \(TunnelLog.timestamp(Date()))",
            "workspace \(TunnelLog.shortWorkspace(w.id)) at \(w.origin.origin)",
            "status: \(status(of: w.id).summary(server: w.origin))",
            "forwarder: \(w.transportOrigin.authority)\(isActive ? "" : " (the app is in the background)")",
        ]
        if let ids = endpointIDs(of: w.id) {
            header.append("endpoints: device \(TunnelLog.shortID(ids.device)), workspace \(TunnelLog.shortID(ids.server))")
        }
        if let s = sessions[w.id] { header.append(s.stateLine) }
        return TunnelLog.shared.report(header: header, workspace: w.id)
    }

    // MARK: From the sessions

    func upstream(_ id: String) async throws -> any ForwardUpstream {
        guard let s = sessions[id] else { throw TunnelSession.Problem(description: "this workspace has no tunnel") }
        return try await s.upstream()
    }

    func portChanged(_ id: String, _ port: Int) {
        AppModel.shared.workspace(id)?.tunnelPortChanged(port)
    }

    /// Up and answering: the workspace refreshes its tunnel's config, and
    /// its open pages and sockets recover in place.
    func cameUp(_ id: String) {
        AppModel.shared.workspace(id)?.tunnelCameUp()
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
