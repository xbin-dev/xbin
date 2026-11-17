import Foundation
import UIKit
import UserNotifications
import XbinCore

/// Push, the app side (native/spec/push.md): the APNs token, one relay
/// handle per workspace, the registration with each xbind, and keeping it
/// working on launch and foreground (§1.4). The relay URL is configuration
/// (the project's relay, https://notif-fwd.xbin.dev, by default:
/// AppInfo.defaultPushRelay); with none set, push stays off and nothing is
/// sent anywhere.
@MainActor
final class PushManager {
    static let shared = PushManager()

    /// The kinds this app asks for (registration `kinds`; none = all):
    /// PushAPI.appKinds — `account` since D181 (a registration made without
    /// it is renewed by maintain's kinds check).
    static let kinds = PushAPI.appKinds

    private(set) var apnsToken: String?
    /// ActivityKit's push-to-start token (hex), once it gives one
    /// (LiveActivities); registered per workspace as `startHandle`.
    private(set) var pushToStartToken: String?
    /// …and the workspace agent card's (its own type: AgentLiveActivity),
    /// registered as `agentStartHandle` (D241).
    private(set) var agentPushToStartToken: String?
    private var running = false
    private var rerun = false

    var relay: ServerOrigin? {
        let s = AppSettings.effectivePushRelay
        return s.isEmpty ? nil : try? ServerOrigin(string: s)
    }

    var keyGroup: String? { AppInfo.sharedKeychainGroup }

    // MARK: APNs

    /// Asks for permission (once) and registers with APNs. Called when a
    /// workspace is added and on launch when push is configured.
    func start() async {
        LiveActivities.shared.start() // local cards work without push
        AgentLiveActivity.shared.start()
        let center = UNUserNotificationCenter.current()
        // the workspace agent's categories too (D241): its follow-ups are the
        // app's own notifications, with or without push
        center.setNotificationCategories(Self.categories.union(AgentNotifications.categories))
        guard relay != nil else { return }
        let granted = (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        #if DEBUG
        // The UI tests (D241): a simulator whose Mac can't reach Apple gets no
        // APNs token; `-XbinFakeAPNsToken <hex>` stands in for it — the
        // development relay spools, and simctl push delivers, whatever it says.
        if granted, let fake = UserDefaults.standard.string(forKey: "XbinFakeAPNsToken"), !fake.isEmpty {
            apnsToken = fake
            await maintainAll()
            return
        }
        #endif
        if granted { UIApplication.shared.registerForRemoteNotifications() }
    }

    func didRegister(deviceToken: Data) {
        apnsToken = APNsToken.hex(deviceToken)
        Task { await maintainAll() }
    }

    static var categories: Set<UNNotificationCategory> {
        let open = UNNotificationAction(identifier: "open", title: "Open", options: [.foreground])
        let mute = UNNotificationAction(identifier: "mute", title: "Mute this tile", options: [.destructive])
        // xbind's notices that link its partitions page (D181): the page,
        // and no Mute — a tile's mute doesn't hold them back.
        let partitions = UNNotificationAction(identifier: "open", title: "Open your partitions", options: [.foreground])
        // A sign-in someone else made for the account: decided on the
        // partitions page, after the device is unlocked.
        let review = UNNotificationAction(identifier: "open", title: "Review…", options: [.foreground, .authenticationRequired])
        return [
            UNNotificationCategory(identifier: "xbin.agent.needs-you", actions: [open], intentIdentifiers: []),
            UNNotificationCategory(identifier: "xbin.agent.turn", actions: [open], intentIdentifiers: []),
            UNNotificationCategory(identifier: "xbin.tile", actions: [open, mute], intentIdentifiers: []),
            UNNotificationCategory(identifier: PushPayload.Kind.pageCategory(.partitions), actions: [partitions], intentIdentifiers: []),
            UNNotificationCategory(identifier: PushPayload.Kind.accountCategory, actions: [review], intentIdentifiers: []),
            UNNotificationCategory(identifier: "xbin.test", actions: [], intentIdentifiers: []),
        ]
    }

    /// The notifications of `workspace` (the app's id) that link `link`
    /// go from Notification Center: the page they link is showing what
    /// they said (D181: the partitions page lists its notices, holds and
    /// asks itself).
    nonisolated func clearDelivered(workspace: String, link: String) {
        UNUserNotificationCenter.current().getDeliveredNotifications { notes in
            let ids = notes.filter {
                let info = $0.request.content.userInfo
                return info["app"] as? String == workspace && info["link"] as? String == link
            }.map(\.request.identifier)
            if !ids.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids) }
        }
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
        guard !running else {
            rerun = true // a token arrived meanwhile: once more after this round
            return
        }
        running = true
        defer { running = false }
        repeat {
            rerun = false
            for w in AppModel.shared.workspaces { await maintain(w) }
        } while rerun
    }

    /// ActivityKit gave a (new) push-to-start token.
    func pushToStartTokenChanged(_ hex: String) async {
        guard hex != pushToStartToken else { return }
        pushToStartToken = hex
        await maintainAll()
    }

    /// …the workspace agent card's (D241).
    func agentPushToStartTokenChanged(_ hex: String) async {
        guard hex != agentPushToStartToken else { return }
        agentPushToStartToken = hex
        await maintainAll()
    }

    /// The kinds `w`'s registration asks for: every agent kind unless the
    /// person turned one off for that workspace (D241, AgentNotifyPrefs).
    func kinds(_ w: WorkspaceModel) -> [String] { AgentNotifications.shared.prefs(w).kinds(base: Self.kinds) }

    /// Push is set up for the workspace (a relay, a token, a handle):
    /// Live Activities there get push tokens.
    func canPush(_ w: WorkspaceModel) -> Bool {
        relay != nil && apnsToken != nil && state(w.id).handle != nil
    }

    /// push.md §1.4 for one workspace.
    func maintain(_ w: WorkspaceModel) async {
        guard let relay, let token = apnsToken else { return }
        let key = publicKey(for: w)
        let dev = deviceID(w)
        var st = state(w.id)
        let listed = try? await w.auth.json(APIRequest("GET", PushAPI.registrations))
        let server: PushStatus? = listed.map(PushStatus.init(json:))
        let wanted = kinds(w)
        let plan = PushMaintenance.plan(state: st, apnsToken: token, relay: relay.origin, publicKey: key, kinds: wanted,
                                        deviceId: dev, server: server)
        do {
            switch plan {
            case .none:
                if let s = server, !s.workspace.isEmpty { rememberPushWorkspace(w, s.workspace) }
                await maintainStart(w, publicKey: key, deviceId: dev, serverHasIt: listed.flatMap { PushAPI.hasPushToStart($0, deviceId: dev) })
                await maintainAgentStart(w, publicKey: key, deviceId: dev, listed: listed)
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
            let reg = try await w.auth.json(PushAPI.register(deviceId: dev, handle: handle, publicKey: key, kinds: wanted))
            let status = PushStatus(json: reg)
            st.publicKey = key
            st.kinds = wanted
            st.pushWorkspace = status.workspace
            setState(st, w.id)
            if !status.workspace.isEmpty { rememberPushWorkspace(w, status.workspace) }
            await maintainStart(w, publicKey: key, deviceId: dev, serverHasIt: PushAPI.hasPushToStart(reg, deviceId: dev))
            await maintainAgentStart(w, publicKey: key, deviceId: dev, listed: try? await w.auth.json(APIRequest("GET", PushAPI.registrations)))
        } catch {
            // Offline or refused: try again next foreground.
        }
    }

    /// Push-to-start for one workspace (LiveStartPlan): a relay handle for
    /// ActivityKit's push-to-start token under the device handle, given to
    /// xbind as the registration's `startHandle` — or taken back when the
    /// user turned it off.
    private func maintainStart(_ w: WorkspaceModel, publicKey key: String, deviceId dev: String, serverHasIt: Bool?) async {
        guard let relay, let handle = state(w.id).handle else { return }
        var ls = liveStart(w.id)
        let plan = LiveStartPlan.plan(enabled: LiveActivities.enabled && LiveActivities.pushToStart, token: pushToStartToken,
                                      state: ls, deviceHandle: handle, serverHasIt: serverHasIt)
        let start: String
        switch plan {
        case .none:
            return
        case .create(let token):
            let req = PushRelayAPI.newActivityHandle(apnsToken: token, parent: handle, topic: AppInfo.bundleID,
                                                     production: AppInfo.apnsProduction, start: true)
            guard let r = try? await AppTransport.shared.send(req, to: relay), r.isSuccess,
                  let json = try? r.json(), let h = PushRelayAPI.handle(from: json) else { return }
            ls = LiveStartState(token: token, handle: h, parent: handle)
            start = h
        case .clear:
            ls = LiveStartState()
            start = ""
        }
        let reg = PushAPI.register(deviceId: dev, handle: handle, publicKey: key, kinds: kinds(w), startHandle: start)
        guard (try? await w.auth.json(reg)) != nil else { return }
        setLiveStart(ls, w.id)
    }

    /// The workspace agent card's push-to-start (D241): a relay handle for
    /// its token, registered as `agentStartHandle` — only with an xbind
    /// that has the card (`features`), Live Activities on and the card on
    /// for that workspace.
    private func maintainAgentStart(_ w: WorkspaceModel, publicKey key: String, deviceId dev: String, listed: JSONValue?) async {
        guard let relay, let handle = state(w.id).handle else { return }
        let feature = PushAPI.features(listed).contains("agent-activity/1")
        guard feature || listed == nil else { return } // an older xbind: nothing to register (listed nil: unknown, try)
        var ls = liveStart(w.id, service: "dev.xbin.liveagentstart")
        let on = LiveActivities.enabled && LiveActivities.pushToStart && AgentNotifications.shared.prefs(w).liveActivity
        let plan = LiveStartPlan.plan(enabled: on, token: agentPushToStartToken, state: ls, deviceHandle: handle,
                                      serverHasIt: listed.flatMap { PushAPI.hasAgentPushToStart($0, deviceId: dev) })
        let start: String
        switch plan {
        case .none:
            return
        case .create(let token):
            let req = PushRelayAPI.newActivityHandle(apnsToken: token, parent: handle, topic: AppInfo.bundleID,
                                                     production: AppInfo.apnsProduction, start: true)
            guard let r = try? await AppTransport.shared.send(req, to: relay), r.isSuccess,
                  let json = try? r.json(), let h = PushRelayAPI.handle(from: json) else { return }
            ls = LiveStartState(token: token, handle: h, parent: handle)
            start = h
        case .clear:
            ls = LiveStartState()
            start = ""
        }
        let reg = PushAPI.register(deviceId: dev, handle: handle, publicKey: key, kinds: kinds(w), startHandle: nil, agentStartHandle: start)
        guard (try? await w.auth.json(reg)) != nil else { return }
        setLiveStart(ls, w.id, service: "dev.xbin.liveagentstart")
    }

    private func liveStart(_ ws: String, service: String) -> LiveStartState {
        guard let d = Keychain.read(service: service, account: ws),
              let s = try? JSONDecoder().decode(LiveStartState.self, from: d) else { return LiveStartState() }
        return s
    }

    private func setLiveStart(_ s: LiveStartState, _ ws: String, service: String) {
        if let d = try? JSONEncoder().encode(s) { Keychain.write(d, service: service, account: ws) }
    }

    private func liveStart(_ ws: String) -> LiveStartState {
        guard let d = Keychain.read(service: "dev.xbin.livestart", account: ws),
              let s = try? JSONDecoder().decode(LiveStartState.self, from: d) else { return LiveStartState() }
        return s
    }

    private func setLiveStart(_ s: LiveStartState, _ ws: String) {
        if let d = try? JSONEncoder().encode(s) { Keychain.write(d, service: "dev.xbin.livestart", account: ws) }
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
        Keychain.delete(service: "dev.xbin.livestart", account: w.id)
        Keychain.delete(service: "dev.xbin.liveagentstart", account: w.id)
        LiveActivities.shared.workspaceRemoved(w.id)
        AgentLiveActivity.shared.end(workspace: w.id)
        AgentNoticeStore.forget(w.id, group: keyGroup)
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
