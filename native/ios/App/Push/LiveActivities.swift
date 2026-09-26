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
/// A card started by push joins the cards once xbind names its session
/// (registered by its `ref`); one xbind says is over (`ended`), knows
/// nothing of (404: its turn ended long ago, xbind restarted, or another
/// workspace sent the start under this one's id), or whose `ws` no
/// workspace of the app has, ends at once — it never shows a turn nobody
/// follows. A card that ends or goes takes its relay handles along.
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
        set {
            UserDefaults.standard.set(newValue, forKey: "xbin.liveActivities")
            if !newValue { shared.endAll() }
            Task { await PushManager.shared.maintainAll() } // and the push-to-start handle
        }
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
        /// Their relay handles (deleted when the card ends or goes).
        var relayHandles: Set<String> = []
    }

    /// A push-started card not placed yet: the workspaces it may be of
    /// (those with its `ws` — two of the app's may share a server — tried in
    /// turn), its ref, the card's state, and its token once ActivityKit gave
    /// one (`reconcile` retries a registration a network failure left).
    private struct Pending {
        var workspaces: [String]
        let ref: String
        let state: AgentActivityState
        var token: String?
    }

    /// A card as ActivityKit has it, in Sendable form.
    struct Snapshot: Sendable {
        let id: String
        let attributes: AgentActivityAttributes
        let state: AgentActivityState
        let active: Bool
    }

    private var cards: [String: Card] = [:]
    /// Push-started cards being placed, by activity id.
    private var pending: [String: Pending] = [:]
    /// Those with a registration in flight.
    private var placing: Set<String> = []
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

    /// Live Activities turned off: every card goes now.
    func endAll() {
        for (k, c) in cards {
            c.recheck?.cancel()
            if let a = c.activityID, let s = c.shown {
                Task { await Self.end(a, s.finished, dismissAt: Date()) }
            }
            retire(c.relayHandles)
            cards[k] = nil
        }
        for (id, p) in pending {
            let final = p.state.finished
            Task { await Self.end(id, final, dismissAt: Date()) }
        }
        pending = [:]
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
        for (a, p) in pending where p.workspaces.contains(id) {
            pending[a]?.workspaces.removeAll { $0 == id }
            if pending[a]?.workspaces.isEmpty ?? true {
                let final = p.state.finished
                Task { await Self.end(a, final, dismissAt: Date()) }
                pending[a] = nil
            }
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
            retire(c.relayHandles)
            c.relayHandles = []
            c.registered = []
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
        retire(c.relayHandles)
        c.relayHandles = []
        c.registered = []
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
    /// that ended while the app slept), forgets cards that are gone, and
    /// places push-started cards not placed yet (or ends them).
    private func reconcile() async {
        let snaps = await Self.snapshots()
        for (k, c) in cards {
            if let id = c.activityID, !snaps.contains(where: { $0.id == id }) { lost(k, id) }
        }
        for id in pending.keys where !snaps.contains(where: { $0.id == id && $0.active }) {
            pending[id] = nil
        }
        for s in snaps where s.active {
            let tracked = cards.first { $0.value.activityID == s.id }
            if tracked == nil, s.attributes.pushStarted {
                await placeAgain(s)
                continue
            }
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
                if let c = cards[k], c.activityID == s.id {
                    retire(c.relayHandles)
                    cards[k] = nil
                }
            }
        }
    }

    /// A push-started card `reconcile` found unplaced: one being placed is
    /// tried again once its token is known; any other (from before this
    /// launch) is adopted anew.
    private func placeAgain(_ s: Snapshot) async {
        guard var p = pending[s.id] else {
            adopt(s)
            return
        }
        if p.token == nil {
            p.token = await Self.pushToken(of: s.id)
            pending[s.id] = p
        }
        await place(s.id)
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
        let since = c.shown?.since ?? 0
        guard let got = await register(token: hex, workspace: c.workspace, session: c.session, ref: nil, since: since) else { return }
        switch got.answer {
        case .following, .ended:
            // (ended: the transcript ends the card too)
            guard cards[k]?.activityID == id else {
                retire([got.handle]) // the card ended meanwhile
                return
            }
            cards[k]?.registered.insert(hex)
            cards[k]?.relayHandles.insert(got.handle)
        case .unknown, .retry:
            break
        }
    }

    /// A card ActivityKit reports new: one xbind started by push is placed
    /// by its ref once its token comes — or ends now when no workspace of
    /// the app has its `ws` (removed, or a start another workspace sent
    /// under this one's id).
    private func adopt(_ s: Snapshot) {
        guard s.attributes.pushStarted, pending[s.id] == nil, !cards.values.contains(where: { $0.activityID == s.id }) else { return }
        let id = s.id
        let candidates = workspaceIDs(push: s.attributes.ws)
        guard !candidates.isEmpty else {
            // (only when the app can read its push records: while they are
            // locked away it can't tell, and reconcile looks again)
            if !PushKeyring.load(group: PushManager.shared.keyGroup).entries.isEmpty {
                let final = s.state.finished
                Task { await Self.end(id, final, dismissAt: Date()) }
            }
            return
        }
        pending[id] = Pending(workspaces: candidates, ref: s.attributes.ref, state: s.state, token: nil)
        Task {
            await Self.tokens(of: id) { hex in
                await LiveActivities.shared.pushStartedToken(hex, activity: id)
            }
        }
    }

    /// The app's workspaces whose xbind push id is `ws`.
    private func workspaceIDs(push ws: String) -> [String] {
        guard !ws.isEmpty else { return [] }
        let ring = PushKeyring.load(group: PushManager.shared.keyGroup)
        var ids: [String] = []
        for w in AppModel.shared.workspaces {
            if ring[w.id]?.pushWorkspace == ws || PushManager.shared.state(w.id).pushWorkspace == ws { ids.append(w.id) }
        }
        return ids
    }

    private func pushStartedToken(_ hex: String, activity id: String) async {
        guard var p = pending[id], p.token != hex else { return } // placed or ended already
        p.token = hex
        pending[id] = p
        await place(id)
    }

    /// Registers a push-started card's token by its ref: the card joins the
    /// cards (xbind named its session), ends (the turn is over, or no
    /// candidate workspace's xbind knows it), or waits for the next try
    /// (offline, locked).
    private func place(_ id: String) async {
        guard !placing.contains(id) else { return }
        placing.insert(id)
        defer { placing.remove(id) }
        while let p = pending[id], let hex = p.token, let ws = p.workspaces.first {
            guard let got = await register(token: hex, workspace: ws, session: nil, ref: p.ref) else { return }
            guard pending[id] != nil else {
                // ended meanwhile (the handle is no card's: one token, one card)
                if !cards.values.contains(where: { $0.relayHandles.contains(got.handle) }) { retire([got.handle]) }
                return
            }
            switch got.answer {
            case .retry:
                return
            case .unknown:
                // not this workspace's: the next candidate's, or nobody's
                pending[id]?.workspaces.removeFirst()
                if pending[id]?.workspaces.isEmpty ?? true {
                    pending[id] = nil
                    await Self.end(id, p.state.finished, dismissAt: Date())
                }
            case .ended:
                pending[id] = nil
                await Self.end(id, p.state.finished, dismissAt: Date().addingTimeInterval(policy.dismissAfter))
            case .following(let session):
                pending[id] = nil
                join(id, session: session, workspace: ws, state: p.state, token: hex, handle: got.handle)
            }
        }
    }

    private func join(_ id: String, session: String, workspace ws: String, state: AgentActivityState, token hex: String, handle: String) {
        let k = Self.key(ws, session)
        var c = cards[k] ?? Card(workspace: ws, session: session, name: "")
        if let mine = c.activityID, mine != id {
            // the app shows its own card for the session: one is enough
            retire([handle])
            Task { await Self.end(id, state.finished, dismissAt: Date()) }
            return
        }
        c.activityID = id
        if c.shown == nil { c.shown = state }
        c.registered.insert(hex)
        c.relayHandles.insert(handle)
        cards[k] = c
    }

    /// The relay handle for an ActivityKit token, registered with xbind (by
    /// session, or by a push start's ref), and what xbind answered; nil
    /// when push isn't set up or readable (locked) or the relay failed. A
    /// handle xbind has no use for is deleted again.
    private func register(token hex: String, workspace ws: String, session: String?, ref: String?,
                          since: Int64 = 0) async -> (handle: String, answer: ActivityRegistration)? {
        guard let w = AppModel.shared.workspace(ws), let relay = PushManager.shared.relay,
              let parent = PushManager.shared.state(w.id).handle else { return nil }
        let newHandle = PushRelayAPI.newActivityHandle(apnsToken: hex, parent: parent, topic: AppInfo.bundleID,
                                                       production: AppInfo.apnsProduction)
        let handle: String
        do {
            let r = try await AppTransport.shared.send(newHandle, to: relay)
            guard r.isSuccess, let h = PushRelayAPI.handle(from: try r.json()) else { return nil }
            handle = h
        } catch {
            return nil
        }
        let req = PushAPI.registerActivity(deviceId: PushManager.shared.deviceID(w), session: session, ref: ref, handle: handle,
                                           since: since)
        var answer = ActivityRegistration.retry
        if let r = try? await w.auth.send(req) {
            answer = ActivityRegistration.of(r)
        }
        if answer == .unknown { retire([handle]) }
        return (handle, answer)
    }

    /// Deletes relay handles of cards that ended or went: nothing more is
    /// pushed to them, and they stop taking a place under the device handle.
    private func retire(_ handles: Set<String>) {
        guard !handles.isEmpty, let relay = PushManager.shared.relay else { return }
        let list = Array(handles)
        Task {
            for h in list {
                _ = try? await AppTransport.shared.send(PushRelayAPI.deleteHandle(h), to: relay)
            }
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

    nonisolated static func pushToken(of id: String) async -> String? {
        guard let a = Activity<AgentActivityAttributes>.activities.first(where: { $0.id == id }), let data = a.pushToken else { return nil }
        return APNsToken.hex(data)
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
