import Foundation
import UIKit
import UserNotifications
import XbinCore

/// Push, the app side (native/spec/push.md): the APNs token, one relay
/// handle per workspace, the registration with each xbind, and keeping it
/// working on launch and foreground (§1.4). The relay URL is configuration —
/// no relay is deployed yet (plans/native.md decision 13) — so with none
/// set, push stays off and nothing is sent anywhere.
@MainActor
final class PushManager {
    static let shared = PushManager()

    /// The kinds this app asks for (registration `kinds`; none = all).
    static let kinds = ["agent", "tile", "test"]

    private(set) var apnsToken: String?
    private var running = false

    var relay: ServerOrigin? {
        let s = AppSettings.effectivePushRelay
        return s.isEmpty ? nil : try? ServerOrigin(string: s)
    }

    var keyGroup: String? { AppInfo.sharedKeychainGroup }

    // MARK: APNs

    /// Asks for permission (once) and registers with APNs. Called when a
    /// workspace is added and on launch when push is configured.
    func start() async {
        guard relay != nil else { return }
        let center = UNUserNotificationCenter.current()
        center.setNotificationCategories(Self.categories)
        let granted = (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        if granted { UIApplication.shared.registerForRemoteNotifications() }
    }

    func didRegister(deviceToken: Data) {
        apnsToken = APNsToken.hex(deviceToken)
        Task { await maintainAll() }
    }

    static var categories: Set<UNNotificationCategory> {
        let open = UNNotificationAction(identifier: "open", title: "Open", options: [.foreground])
        let mute = UNNotificationAction(identifier: "mute", title: "Mute this tile", options: [.destructive])
        return [
            UNNotificationCategory(identifier: "xbin.agent.needs-you", actions: [open], intentIdentifiers: []),
            UNNotificationCategory(identifier: "xbin.agent.turn", actions: [open], intentIdentifiers: []),
            UNNotificationCategory(identifier: "xbin.tile", actions: [open, mute], intentIdentifiers: []),
            UNNotificationCategory(identifier: "xbin.test", actions: [], intentIdentifiers: []),
        ]
    }

    // MARK: Per workspace

    private func stateKey(_ ws: String) -> String { ws }

    func state(_ ws: String) -> PushState {
        guard let d = Keychain.read(service: "dev.xbin.pushstate", account: ws),
              let s = try? JSONDecoder().decode(PushState.self, from: d) else { return PushState() }
        return s
    }

    func setState(_ s: PushState, _ ws: String) {
        if let d = try? JSONEncoder().encode(s) { Keychain.write(d, service: "dev.xbin.pushstate", account: ws) }
    }

    /// The push device id: the device-login id, or a stable app id for a
    /// workspace signed in without a device (development tokens).
    func deviceID(_ w: WorkspaceModel) -> String { w.record.deviceId ?? "app-" + String(w.id.prefix(18)) }

    /// The workspace's X25519 key, made on first use and shared with the
    /// extension (Keychain group, after-first-unlock).
    private func publicKey(for w: WorkspaceModel) -> String {
        var ring = PushKeyring.load(group: keyGroup)
        if let e = ring[w.id], let pub = PushCrypto.publicKey(of: e.privateKey) {
            if e.title != w.title {
                var e2 = e
                e2.title = w.title
                ring.upsert(e2)
                ring.save(group: keyGroup)
            }
            return Base64URL.encode(pub)
        }
        let pair = PushCrypto.newKeyPair()
        ring.upsert(.init(workspace: w.id, pushWorkspace: nil, privateKey: pair.privateKey, title: w.title))
        ring.save(group: keyGroup)
        return Base64URL.encode(pair.publicKey)
    }

    private func rememberPushWorkspace(_ w: WorkspaceModel, _ pushWS: String) {
        var ring = PushKeyring.load(group: keyGroup)
        guard var e = ring[w.id], e.pushWorkspace != pushWS else { return }
        e.pushWorkspace = pushWS
        ring.upsert(e)
        ring.save(group: keyGroup)
    }

    func workspaceAdded(_ w: WorkspaceModel) {
        Task {
            await start()
            await maintain(w)
        }
    }

    func maintainAll() async {
        guard !running else { return }
        running = true
        defer { running = false }
        for w in AppModel.shared.workspaces { await maintain(w) }
    }

    /// push.md §1.4 for one workspace.
    func maintain(_ w: WorkspaceModel) async {
        guard let relay, let token = apnsToken else { return }
        let key = publicKey(for: w)
        let dev = deviceID(w)
        var st = state(w.id)
        let listed = try? await w.auth.json(APIRequest("GET", PushAPI.registrations))
        let server: PushStatus? = listed.map(PushStatus.init(json:))
        let plan = PushMaintenance.plan(state: st, apnsToken: token, relay: relay.origin, publicKey: key, kinds: Self.kinds,
                                        deviceId: dev, server: server)
        do {
            switch plan {
            case .none:
                if let s = server, !s.workspace.isEmpty { rememberPushWorkspace(w, s.workspace) }
                return
            case .newHandle:
                st.handle = try await newHandle(relay, token)
                st.relay = relay.origin
            case .replaceHandle(let old):
                _ = try? await AppTransport.shared.send(PushRelayAPI.deleteHandle(old), to: relay)
                st.handle = try await newHandle(relay, token)
                st.relay = relay.origin
            case .updateToken(let h):
                let r = try await AppTransport.shared.send(
                    PushRelayAPI.updateHandle(h, apnsToken: token, topic: AppInfo.bundleID, production: AppInfo.apnsProduction), to: relay)
                if r.status == 404 { st.handle = try await newHandle(relay, token) } // the relay forgot it
                else if !r.isSuccess { throw APIError(r) }
            case .register:
                break
            }
            st.apnsToken = token
            guard let handle = st.handle else { return }
            let reg = try await w.auth.json(PushAPI.register(deviceId: dev, handle: handle, publicKey: key, kinds: Self.kinds))
            let status = PushStatus(json: reg)
            st.publicKey = key
            st.kinds = Self.kinds
            st.pushWorkspace = status.workspace
            setState(st, w.id)
            if !status.workspace.isEmpty { rememberPushWorkspace(w, status.workspace) }
        } catch {
            // Offline or refused: try again next foreground.
        }
    }

    private func newHandle(_ relay: ServerOrigin, _ token: String) async throws -> String {
        let r = try await AppTransport.shared.send(
            PushRelayAPI.newHandle(apnsToken: token, topic: AppInfo.bundleID, production: AppInfo.apnsProduction), to: relay)
        guard r.isSuccess, let h = PushRelayAPI.handle(from: try r.json()) else { throw APIError(r) }
        return h
    }

    /// Removing a workspace cuts it off at the relay and on xbind.
    func workspaceRemoved(_ w: WorkspaceModel) async {
        let st = state(w.id)
        if let relay, let h = st.handle { _ = try? await AppTransport.shared.send(PushRelayAPI.deleteHandle(h), to: relay) }
        _ = try? await w.auth.send(PushAPI.unregister(deviceId: deviceID(w)))
        Keychain.delete(service: "dev.xbin.pushstate", account: w.id)
        var ring = PushKeyring.load(group: keyGroup)
        ring.remove(w.id)
        ring.save(group: keyGroup)
    }

    /// "Mute this tile" (a notification action): adds the tile to the
    /// user's muted list on that workspace.
    func mute(tile: String, in w: WorkspaceModel) async {
        let cur = (try? await w.auth.json(APIRequest("GET", PushAPI.prefs))).map(PushAPI.mutedTiles) ?? []
        guard !cur.contains(tile) else { return }
        _ = try? await w.auth.send(PushAPI.setMuted(cur + [tile]))
    }

    func sendTest(_ w: WorkspaceModel) async -> String {
        do {
            let r = try await w.auth.send(APIRequest("POST", PushAPI.test))
            switch r.status {
            case 202: return "Sent — it should arrive in a few seconds."
            case 409: return "Push isn't on for this workspace yet (an admin turns it on), or this device isn't registered."
            case 429: return "Too many test pushes — wait a minute."
            default: return APIError(r).description
            }
        } catch { return w.describe(error) }
    }
}
