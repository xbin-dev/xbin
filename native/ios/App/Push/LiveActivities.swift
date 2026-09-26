import ActivityKit
import Foundation
import UIKit
import XbinAgent
import XbinCore

/// Live Activities for agent turns (native/spec/push.md §7; the model and
/// its rules: XbinAgent's LiveActivity.swift). While the app runs, it
/// starts, updates and ends a turn's card from the session's transcript
/// (AgentActivityPolicy: long turns, a wait for the user, or leaving the
/// foreground mid-turn). With push set up for the workspace, it registers
/// each card's ActivityKit token — and the app's push-to-start token, which
/// PushManager keeps — with xbind through the relay, so the card follows
/// the turn while the app is suspended and xbind can start one for a long
/// turn the app never saw. What goes through the relay is generic state
/// only; the names stay in the card's attributes, on the device.
///
/// Hooks:
///  - `start()` once at launch (PushManager.start calls it);
///  - the Agent screen, for the session it shows: `follow(_:in:name:)`
///    (keep the returned task with the screen's, cancel it with them), or
///    `observe(_:session:workspace:name:)` per transcript it already has;
///  - scene changes arrive by notification (willResignActive,
///    didBecomeActive); `workspaceRemoved(_:)` from PushManager.
///
/// ActivityKit objects never leave the nonisolated statics below: they are
/// looked up by id there, so nothing non-Sendable crosses an actor.
@MainActor
final class LiveActivities {
    static let shared = LiveActivities()

    /// Live Activities at all (Settings may bind it; default on).
    static var enabled: Bool {
        get { UserDefaults.standard.object(forKey: "xbin.liveActivities") as? Bool ?? true }
        set { UserDefaults.standard.set(newValue, forKey: "xbin.liveActivities") }
    }

    /// Cards xbind starts by push for long turns (default on).
    static var pushToStart: Bool {
        get { UserDefaults.standard.object(forKey: "xbin.liveActivities.pushToStart") as? Bool ?? true }
        set {
            UserDefaults.standard.set(newValue, forKey: "xbin.liveActivities.pushToStart")
            Task { await PushManager.shared.maintainAll() }
        }
    }

    let policy = AgentActivityPolicy()

    /// One session's card.
    private struct Card {
        let workspace: String // the app's workspace id
        let session: String
        var name: String
        var activityID: String?
        var shown: AgentActivityState?
        var latest: AgentActivityState?
        /// The user took this turn's card away (by its `since`): it stays away.
        var dismissedSince: Int64?
        var recheck: Task<Void, Never>?
        var recheckAt: Date?
        /// Tokens registered with xbind already.
        var registered: Set<String> = []
    }

    /// A card as ActivityKit has it, in Sendable form.
    struct Snapshot: Sendable {
        let id: String
        let attributes: AgentActivityAttributes
        let state: AgentActivityState
        let active: Bool
    }

    private var cards: [String: Card] = [:]
    private var started = false
    private var foreground = true
    private var observers: [any NSObjectProtocol] = []

    private static func key(_ workspace: String, _ session: String) -> String { workspace + "\n" + session }

    // MARK: hooks

    func start() {
        guard !started else { return }
        started = true
        let nc = NotificationCenter.default
        observers.append(nc.addObserver(forName: UIApplication.willResignActiveNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { LiveActivities.shared.leavingForeground() }
        })
        observers.append(nc.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { LiveActivities.shared.becameActive() }
        })
        Task {
            await Self.pushToStartTokens { hex in
                await PushManager.shared.pushToStartTokenChanged(hex)
            }
        }
        Task {
            await Self.newActivities { snapshot in
                await LiveActivities.shared.adopt(snapshot)
            }
        }
        Task { await self.reconcile() }
    }

    /// Follows a session's feed for its card while the returned task runs.
    func follow(_ feed: AgentSessionFeed, in w: WorkspaceModel, name: String = "") -> Task<Void, Never> {
        let ws = w.id
        let session = feed.sessionID
        return Task { [weak self] in
            for await t in await feed.updates() {
                guard let self else { return }
                self.observe(t, session: session, workspace: ws, name: name)
            }
        }
    }

    /// One transcript of a session (what `follow` does per update).
    func observe(_ t: AgentTranscript, session: String, workspace: String, name: String = "") {
        guard Self.enabled else { return }
        let k = Self.key(workspace, session)
        var c = cards[k] ?? Card(workspace: workspace, session: session, name: "")
        let title = name.isEmpty ? (t.state.title ?? "") : name
        if !title.isEmpty { c.name = title }
        c.latest = t.activityState
        cards[k] = c
        evaluate(k, leaving: false)
    }

    /// The workspace is gone from the app: its cards go now.
    func workspaceRemoved(_ id: String) {
        for (k, c) in cards where c.workspace == id {
            c.recheck?.cancel()
            if let a = c.activityID, let s = c.shown {
                Task { await Self.end(a, s.finished, dismissAt: Date()) }
            }
            cards[k] = nil
        }
    }

    // MARK: deciding

    private func evaluate(_ k: String, leaving: Bool) {
        guard var c = cards[k] else { return }
        let now = Date()
        var canStart = foreground && Self.enabled && ActivityAuthorizationInfo().areActivitiesEnabled
        if let d = c.dismissedSince, d == c.latest?.since { canStart = false }
        let decision = policy.decide(state: c.latest, shown: c.shown, canStart: canStart, leaving: leaving, now: now)
        switch decision.action {
        case .none:
            break
        case .start(let s):
            if let id = request(c, s) {
                c.activityID = id
                c.shown = s
            }
        case .update(let s):
            if let id = c.activityID {
                let stale = now.addingTimeInterval(policy.staleAfter)
                Task { [weak self] in
                    let found = await Self.update(id, s, staleDate: stale)
                    if !found { self?.lost(k, id) }
                }
            }
            c.shown = s
        case .end(let s, let at):
            if let id = c.activityID {
                Task { await Self.end(id, s, dismissAt: at) }
            }
            c.activityID = nil
            c.shown = nil
        }
        // one pending re-decision at a time (a streaming turn calls this
        // for every delta)
        if decision.recheckAt != c.recheckAt {
            c.recheck?.cancel()
            c.recheck = nil
            c.recheckAt = decision.recheckAt
            if let at = decision.recheckAt {
                c.recheck = Task { [weak self] in
                    try? await Task.sleep(for: .seconds(max(0.1, at.timeIntervalSinceNow)))
                    if Task.isCancelled { return }
                    self?.recheck(k)
                }
            }
        }
        cards[k] = (c.activityID == nil && c.latest == nil) ? nil : c
    }

    private func recheck(_ k: String) {
        cards[k]?.recheck = nil
        cards[k]?.recheckAt = nil
        evaluate(k, leaving: false)
    }

    /// Starts a card; its id, or nil when ActivityKit refuses (not allowed,
    /// too many, not in the foreground).
    private func request(_ c: Card, _ s: AgentActivityState) -> String? {
        guard let w = AppModel.shared.workspace(c.workspace) else { return nil }
        let push = PushManager.shared.canPush(w)
        let attributes = AgentActivityAttributes(ws: PushManager.shared.state(w.id).pushWorkspace ?? "", ref: "",
                                                 workspace: w.title, session: c.name, appWorkspace: w.id, sessionID: c.session)
        let content = ActivityContent(state: s, staleDate: Date().addingTimeInterval(policy.staleAfter))
        let pushType: PushType? = push ? .token : nil
        do {
            let activity = try Activity<AgentActivityAttributes>.request(attributes: attributes, content: content, pushType: pushType)
            let id = activity.id
            if push { followTokens(id, key: Self.key(c.workspace, c.session)) }
            return id
        } catch {
            return nil
        }
    }

    /// A card went without us (the user swiped it away, the system ended
    /// it): forget it, don't bring it back this turn, and stop xbind
    /// pushing to it.
    private func lost(_ k: String, _ id: String) {
        guard var c = cards[k], c.activityID == id else { return }
        c.activityID = nil
        c.shown = nil
        c.dismissedSince = c.latest?.since
        cards[k] = c.latest == nil ? nil : c
        if let w = AppModel.shared.workspace(c.workspace), PushManager.shared.canPush(w) {
            let req = PushAPI.unregisterActivity(deviceId: PushManager.shared.deviceID(w), session: c.session)
            Task { _ = try? await w.auth.send(req) }
        }
    }

    // MARK: scene

    private func leavingForeground() {
        // ActivityKit starts cards only from the foreground: last chance
        // for a turn still running
        for k in cards.keys { evaluate(k, leaving: true) }
        foreground = false
    }

    private func becameActive() {
        foreground = true
        for k in cards.keys { evaluate(k, leaving: false) }
        Task { await reconcile() }
    }

    /// Ends cards whose turn is over (an end push that never came, a turn
    /// that ended while the app slept), and forgets cards that are gone.
    private func reconcile() async {
        let snaps = await Self.snapshots()
        for (k, c) in cards {
            if let id = c.activityID, !snaps.contains(where: { $0.id == id }) { lost(k, id) }
        }
        for s in snaps where s.active {
            let tracked = cards.first { $0.value.activityID == s.id }
            let ws = tracked?.value.workspace ?? s.attributes.appWorkspace
            let session = tracked?.value.session ?? s.attributes.sessionID
            guard !ws.isEmpty, !session.isEmpty, let w = AppModel.shared.workspace(ws) else { continue }
            let busy: Bool
            do {
                busy = try await w.agents.session(session).session.status?.isBusy ?? false
            } catch let e as AgentAPIError where e.isNotFound {
                busy = false
            } catch {
                continue // offline: leave it
            }
            if !busy {
                await Self.end(s.id, s.state.finished, dismissAt: Date().addingTimeInterval(policy.dismissAfter))
                let k = Self.key(ws, session)
                if cards[k]?.activityID == s.id { cards[k] = nil }
            }
        }
    }

    // MARK: tokens

    private func followTokens(_ id: String, key k: String) {
        Task {
            await Self.tokens(of: id) { hex in
                await LiveActivities.shared.tokenArrived(hex, activity: id, key: k)
            }
        }
    }

    private func tokenArrived(_ hex: String, activity id: String, key k: String) async {
        guard let c = cards[k], c.activityID == id, !c.registered.contains(hex) else { return }
        guard await register(token: hex, workspace: c.workspace, session: c.session, ref: nil) != nil else { return }
        cards[k]?.registered.insert(hex)
    }

    /// A card ActivityKit reports new: one xbind started by push gets its
    /// token registered by its ref, and joins the cards once xbind says
    /// which session it is.
    private func adopt(_ s: Snapshot) {
        guard s.attributes.pushStarted, !cards.values.contains(where: { $0.activityID == s.id }) else { return }
        let ws = AppModel.shared.workspaces.first { PushManager.shared.state($0.id).pushWorkspace == s.attributes.ws }?.id
        guard let ws else { return }
        let id = s.id
        let ref = s.attributes.ref
        let state = s.state
        Task {
            await Self.tokens(of: id) { hex in
                await LiveActivities.shared.pushStartedToken(hex, activity: id, workspace: ws, ref: ref, state: state)
            }
        }
    }

    private func pushStartedToken(_ hex: String, activity id: String, workspace ws: String, ref: String, state: AgentActivityState) async {
        guard let session = await register(token: hex, workspace: ws, session: nil, ref: ref) else { return }
        let k = Self.key(ws, session)
        var c = cards[k] ?? Card(workspace: ws, session: session, name: "")
        if let mine = c.activityID, mine != id {
            // the app shows its own card for the session: one is enough
            Task { await Self.end(id, state.finished, dismissAt: Date()) }
            return
        }
        c.activityID = id
        if c.shown == nil { c.shown = state }
        c.registered.insert(hex)
        cards[k] = c
    }

    /// The relay handle for an ActivityKit token, registered with xbind
    /// (by session, or by a push start's ref); the session xbind names.
    private func register(token hex: String, workspace ws: String, session: String?, ref: String?) async -> String? {
        guard let w = AppModel.shared.workspace(ws), let relay = PushManager.shared.relay,
              let parent = PushManager.shared.state(w.id).handle else { return nil }
        let newHandle = PushRelayAPI.newActivityHandle(apnsToken: hex, parent: parent, topic: AppInfo.bundleID,
                                                       production: AppInfo.apnsProduction)
        do {
            let r = try await AppTransport.shared.send(newHandle, to: relay)
            guard r.isSuccess, let h = PushRelayAPI.handle(from: try r.json()) else { return nil }
            let req = PushAPI.registerActivity(deviceId: PushManager.shared.deviceID(w), session: session, ref: ref, handle: h)
            return PushAPI.activitySession(try await w.auth.json(req))
        } catch {
            return nil
        }
    }

    // MARK: ActivityKit (nonisolated: activities are looked up by id here
    // and never handed across)

    nonisolated static func snapshots() async -> [Snapshot] {
        Activity<AgentActivityAttributes>.activities.map {
            Snapshot(id: $0.id, attributes: $0.attributes, state: $0.content.state, active: $0.activityState == .active)
        }
    }

    nonisolated static func update(_ id: String, _ s: AgentActivityState, staleDate: Date) async -> Bool {
        guard let a = Activity<AgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return false }
        await a.update(ActivityContent(state: s, staleDate: staleDate))
        return true
    }

    nonisolated static func end(_ id: String, _ s: AgentActivityState, dismissAt: Date) async {
        guard let a = Activity<AgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return }
        await a.end(ActivityContent(state: s, staleDate: nil), dismissalPolicy: .after(dismissAt))
    }

    nonisolated static func tokens(of id: String, _ each: @escaping @Sendable (String) async -> Void) async {
        guard let a = Activity<AgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return }
        for await data in a.pushTokenUpdates {
            await each(APNsToken.hex(data))
        }
    }

    nonisolated static func newActivities(_ each: @escaping @Sendable (Snapshot) async -> Void) async {
        for await a in Activity<AgentActivityAttributes>.activityUpdates {
            let s = Snapshot(id: a.id, attributes: a.attributes, state: a.content.state, active: a.activityState == .active)
            await each(s)
        }
    }

    nonisolated static func pushToStartTokens(_ each: @escaping @Sendable (String) async -> Void) async {
        for await data in Activity<AgentActivityAttributes>.pushToStartTokenUpdates {
            await each(APNsToken.hex(data))
        }
    }
}
