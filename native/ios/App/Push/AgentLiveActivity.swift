import ActivityKit
import Foundation
import Observation
import UIKit
import XbinAgent
import XbinCore

extension WorkspaceAgentModel {
    /// The card's picture of this workspace (WorkspaceAgentModel.cardCounts).
    var picture: WorkspaceAgentPicture {
        let c = cardCounts
        return WorkspaceAgentPicture(has: c.has, paused: c.paused, pending: c.pending, agents: c.agents)
    }
}

/// The workspace agent's Live Activity (D241; plans/wsagent/ios-stage2.md
/// §5; the model and its rules: XbinAgent's WorkspaceAgentActivity.swift).
/// One card per workspace while the person's agent works or something waits
/// for them. While the app runs it starts, updates and ends the card from
/// the agent's own model (WorkspaceAgentModel.picture — counts only); with
/// push set up it registers the card's ActivityKit token with xbind through
/// the relay (`{agent: true}`), and the push-to-start token of the card's
/// type (`agentStartHandle`, PushManager), so the card follows the agent
/// while the app is suspended and xbind can start one the app never did.
///
/// A card the person swiped away stays away for its stretch (xbind is told:
/// DELETE …/activities/@agent). A card started by push is placed by its
/// ref, or ends when no workspace of the app has its `ws`.
@MainActor
final class AgentLiveActivity {
    static let shared = AgentLiveActivity()
    let policy = WorkspaceAgentActivityPolicy()

    private struct Card {
        var activityID: String?
        var shown: WorkspaceAgentActivityState?
        var stretchSince: Date?
        var idleSince: Date?
        /// The person swiped this stretch's card away.
        var dismissed = false
        var token: String?
        var registered = false
        var relayHandle: String?
        var recheck: Task<Void, Never>?
        var recheckAt: Date?
        var observing = false
    }

    private var cards: [String: Card] = [:] // app workspace id → its card
    private var foreground = true
    private var started = false
    private var observers: [any NSObjectProtocol] = []

    /// Debug builds' `-XbinAgentCardDemo running|waiting|paused|idle`: a
    /// card with that state for the workspace in front, started at launch
    /// (the UI tests' shots; no xbind behind it).
    static var demo: WorkspaceAgentActivityState? {
        #if DEBUG
        guard let s = UserDefaults.standard.string(forKey: "XbinAgentCardDemo"), let p = WorkspaceAgentPhase(rawValue: s) else { return nil }
        let since = Int64(Date().timeIntervalSince1970) - 754
        return WorkspaceAgentActivityState(phase: p, since: since, pending: p == .waiting ? 2 : 0, agents: p == .idle || p == .paused ? 0 : 3)
        #else
        return nil
        #endif
    }

    func start() {
        guard !started else { return }
        started = true
        let nc = NotificationCenter.default
        observers.append(nc.addObserver(forName: UIApplication.willResignActiveNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { AgentLiveActivity.shared.leaving() }
        })
        observers.append(nc.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { AgentLiveActivity.shared.becameActive() }
        })
        #if DEBUG
        if Self.demo != nil {
            // the UI tests' shots: the card in one state, for the first workspace, no
            // xbind behind it — the cards from before this launch go, none is adopted
            for s in Self.snapshotsNow() { Task { await Self.end(s.id, s.state, dismissAt: Date()) } }
            startDemo()
            return
        }
        #endif
        // the cards from before this launch: taken back, never started again
        for s in Self.snapshotsNow() where s.active { adoptOwn(s) }
        Task {
            await Self.pushToStartTokens { hex in await PushManager.shared.agentPushToStartTokenChanged(hex) }
        }
        Task {
            await Self.newActivities { s in await AgentLiveActivity.shared.adoptPushed(s) }
        }
    }

    /// Follows `w`'s agent: the card is decided again whenever what its
    /// picture reads changes (Observation), and now.
    func observe(_ w: WorkspaceModel) {
        start()
        let id = w.id
        if cards[id]?.observing != true {
            var c = cards[id] ?? Card()
            c.observing = true
            cards[id] = c
            track(w)
        }
        evaluate(id, leaving: false)
    }

    private func track(_ w: WorkspaceModel) {
        let id = w.id
        withObservationTracking {
            _ = w.agent.picture
        } onChange: {
            Task { @MainActor in
                guard let w = AppModel.shared.workspace(id) else { return }
                AgentLiveActivity.shared.track(w)
                AgentLiveActivity.shared.evaluate(id, leaving: false)
            }
        }
    }

    /// The card goes now (the person turned it off, the workspace went).
    func end(workspace id: String) {
        guard var c = cards[id] else { return }
        c.recheck?.cancel()
        if let a = c.activityID, let s = c.shown {
            Task { await Self.end(a, WorkspaceAgentActivityState(phase: .idle, since: s.since), dismissAt: Date()) }
        }
        retire(c.relayHandle)
        c = Card(observing: c.observing)
        cards[id] = c
    }

    // MARK: deciding

    private func evaluate(_ id: String, leaving: Bool) {
        guard let w = AppModel.shared.workspace(id), var c = cards[id] else { return }
        let now = Date()
        let prefs = AgentNotifications.shared.prefs(w)
        if Self.demo != nil { return } // the Debug demo card stands as it was started
        let pic = w.agent.picture
        let active = pic.has && !pic.paused && (pic.agents > 0 || pic.pending > 0)
        if active {
            if c.stretchSince == nil { c.stretchSince = now; c.dismissed = false }
            c.idleSince = nil
        } else {
            if c.idleSince == nil { c.idleSince = now }
            if !pic.has || pic.paused || c.shown == nil { c.stretchSince = nil }
        }
        let canStart = foreground && prefs.liveActivity && !c.dismissed && ActivityAuthorizationInfo().areActivitiesEnabled
        let decision = policy.decide(picture: pic, shown: c.shown, stretchSince: c.stretchSince, idleSince: c.idleSince,
                                     canStart: canStart, leaving: leaving, now: now)
        switch decision.action {
        case .none:
            break
        case .start(let s):
            if let a = request(w, s) {
                c.activityID = a
                c.shown = s
                c.registered = false
            }
        case .update(let s):
            if let a = c.activityID {
                let stale = now.addingTimeInterval(policy.staleAfter)
                Task { [weak self] in
                    if !(await Self.update(a, s, staleDate: stale)) { self?.lost(id, a) }
                }
            }
            c.shown = s
        case .end(let s, let at):
            if let a = c.activityID { Task { await Self.end(a, s, dismissAt: at) } }
            retire(c.relayHandle)
            c.activityID = nil
            c.shown = nil
            c.token = nil
            c.registered = false
            c.relayHandle = nil
            c.stretchSince = nil
        }
        if decision.recheckAt != c.recheckAt {
            c.recheck?.cancel()
            c.recheck = nil
            c.recheckAt = decision.recheckAt
            if let at = decision.recheckAt {
                c.recheck = Task { [weak self] in
                    try? await Task.sleep(for: .seconds(max(0.1, at.timeIntervalSinceNow)))
                    if Task.isCancelled { return }
                    self?.cards[id]?.recheckAt = nil
                    self?.evaluate(id, leaving: false)
                }
            }
        }
        cards[id] = c
    }

    /// Starts the card; its id, or nil when ActivityKit refuses.
    private func request(_ w: WorkspaceModel, _ s: WorkspaceAgentActivityState) -> String? {
        let push = PushManager.shared.canPush(w)
        let attributes = WorkspaceAgentActivityAttributes(ws: PushManager.shared.state(w.id).pushWorkspace ?? "", ref: "",
                                                          workspace: w.title, appWorkspace: w.id)
        let content = ActivityContent(state: s, staleDate: Date().addingTimeInterval(policy.staleAfter),
                                      relevanceScore: s.phase == .waiting ? 1 : 0.6)
        do {
            let a = try Activity<WorkspaceAgentActivityAttributes>.request(attributes: attributes, content: content,
                                                                           pushType: push ? .token : nil)
            let id = a.id
            if push { followTokens(id, workspace: w.id) }
            followState(id, workspace: w.id)
            return id
        } catch {
            #if DEBUG
            NSLog("xbin-agent card: not started: %@", String(describing: error))
            #endif
            return nil
        }
    }

    /// The card went without us (the person swiped it away, the system ended
    /// it): it stays away this stretch, and xbind stops pushing to it.
    private func lost(_ id: String, _ activity: String) {
        guard var c = cards[id], c.activityID == activity else { return }
        c.activityID = nil
        c.shown = nil
        c.dismissed = true
        retire(c.relayHandle)
        c.relayHandle = nil
        c.registered = false
        c.token = nil
        cards[id] = c
        if let w = AppModel.shared.workspace(id), PushManager.shared.canPush(w) {
            let req = PushAPI.unregisterActivity(deviceId: PushManager.shared.deviceID(w), session: PushAPI.agentSession)
            Task { _ = try? await w.auth.send(req) }
        }
    }

    private func leaving() {
        for id in cards.keys { evaluate(id, leaving: true) }
        foreground = false
    }

    #if DEBUG
    /// Debug's demo card, once the app is in front (ActivityKit starts cards only then).
    private func startDemo() {
        guard let d = Self.demo, let w = AppModel.shared.workspaces.first, cards[w.id]?.activityID == nil else { return }
        NSLog("xbin-agent card demo: %@, enabled %@", d.phase.rawValue, ActivityAuthorizationInfo().areActivitiesEnabled ? "yes" : "no")
        if let a = request(w, d) { cards[w.id] = Card(activityID: a, shown: d, stretchSince: d.startDate) }
    }
    #endif

    private func becameActive() {
        foreground = true
        #if DEBUG
        if Self.demo != nil { startDemo(); return }
        #endif
        for (id, c) in cards {
            evaluate(id, leaving: false)
            if c.activityID != nil, c.token != nil, !c.registered { retryRegistration(id) }
        }
    }

    // MARK: tokens

    private func followTokens(_ activity: String, workspace id: String) {
        Task {
            await Self.tokens(of: activity) { hex in
                await AgentLiveActivity.shared.tokenArrived(hex, activity: activity, workspace: id)
            }
        }
    }

    private func followState(_ activity: String, workspace id: String) {
        Task {
            await Self.states(of: activity) { dismissed in
                if dismissed { await AgentLiveActivity.shared.lost(id, activity) }
            }
        }
    }

    private func tokenArrived(_ hex: String, activity: String, workspace id: String) async {
        guard var c = cards[id], c.activityID == activity else { return }
        c.token = hex
        cards[id] = c
        await register(id)
    }

    private func retryRegistration(_ id: String) {
        Task { await register(id) }
    }

    /// The card's token, through a relay handle, to xbind (`{agent: true}`).
    private func register(_ id: String) async {
        guard let c = cards[id], let hex = c.token, !c.registered, let w = AppModel.shared.workspace(id),
              let relay = PushManager.shared.relay, let parent = PushManager.shared.state(w.id).handle else { return }
        let newHandle = PushRelayAPI.newActivityHandle(apnsToken: hex, parent: parent, topic: AppInfo.bundleID, production: AppInfo.apnsProduction)
        guard let r = try? await AppTransport.shared.send(newHandle, to: relay), r.isSuccess,
              let handle = PushRelayAPI.handle(from: (try? r.json()) ?? .null) else { return }
        let since = c.shown?.since ?? 0
        guard let answer = try? await w.auth.send(PushAPI.registerAgentActivity(deviceId: PushManager.shared.deviceID(w), handle: handle, since: since)) else {
            retire(handle)
            return
        }
        switch ActivityRegistration.of(answer) {
        case .following:
            cards[id]?.registered = true
            cards[id]?.relayHandle = handle
        case .ended:
            // xbind sees nothing running: it ends the card (and so do we)
            retire(handle)
            if let a = cards[id]?.activityID, let s = cards[id]?.shown {
                Task { await Self.end(a, WorkspaceAgentActivityState(phase: .idle, since: s.since), dismissAt: Date().addingTimeInterval(policy.dismissAfter)) }
            }
            cards[id]?.activityID = nil
            cards[id]?.shown = nil
        case .unknown, .retry:
            retire(handle)
        }
    }

    private func retire(_ handle: String?) {
        guard let handle, let relay = PushManager.shared.relay else { return }
        Task { _ = try? await AppTransport.shared.send(PushRelayAPI.deleteHandle(handle), to: relay) }
    }

    // MARK: cards from elsewhere

    /// A card the app started before this launch: its workspace's card again.
    private func adoptOwn(_ s: Snapshot) {
        guard !s.attributes.pushStarted, !s.attributes.appWorkspace.isEmpty else { return }
        let id = s.attributes.appWorkspace
        if let other = cards[id]?.activityID, other != s.id {
            Task { await Self.end(s.id, WorkspaceAgentActivityState(phase: .idle, since: s.state.since), dismissAt: Date()) }
            return
        }
        var c = cards[id] ?? Card()
        c.activityID = s.id
        c.shown = s.state
        c.stretchSince = s.state.startDate
        cards[id] = c
        followTokens(s.id, workspace: id)
        followState(s.id, workspace: id)
    }

    /// A card xbind started by push: placed by its ref once its token comes,
    /// or ended when no workspace of the app has its `ws`.
    private func adoptPushed(_ s: Snapshot) {
        guard s.attributes.pushStarted, !cards.values.contains(where: { $0.activityID == s.id }) else { return }
        let ring = PushKeyring.load(group: PushManager.shared.keyGroup)
        guard let w = AppModel.shared.workspaces.first(where: { ring[$0.id]?.pushWorkspace == s.attributes.ws
                  || PushManager.shared.state($0.id).pushWorkspace == s.attributes.ws }) else {
            if !ring.entries.isEmpty { Task { await Self.end(s.id, WorkspaceAgentActivityState(phase: .idle), dismissAt: Date()) } }
            return
        }
        let id = w.id
        if let mine = cards[id]?.activityID, mine != s.id {
            // the app shows its own: one card is enough
            Task { await Self.end(s.id, WorkspaceAgentActivityState(phase: .idle), dismissAt: Date()) }
            _ = mine
            return
        }
        let ref = s.attributes.ref
        Task {
            await Self.tokens(of: s.id) { hex in
                await AgentLiveActivity.shared.placePushed(s.id, ref: ref, token: hex, workspace: id)
            }
        }
    }

    private func placePushed(_ activity: String, ref: String, token hex: String, workspace id: String) async {
        guard let w = AppModel.shared.workspace(id), let relay = PushManager.shared.relay,
              let parent = PushManager.shared.state(w.id).handle else { return }
        let newHandle = PushRelayAPI.newActivityHandle(apnsToken: hex, parent: parent, topic: AppInfo.bundleID, production: AppInfo.apnsProduction)
        guard let r = try? await AppTransport.shared.send(newHandle, to: relay), r.isSuccess,
              let handle = PushRelayAPI.handle(from: (try? r.json()) ?? .null) else { return }
        let answer = (try? await w.auth.send(PushAPI.registerActivity(deviceId: PushManager.shared.deviceID(w), ref: ref, handle: handle)))
            .map(ActivityRegistration.of) ?? .retry
        switch answer {
        case .following:
            var c = cards[id] ?? Card()
            c.activityID = activity
            c.token = hex
            c.registered = true
            c.relayHandle = handle
            c.stretchSince = c.stretchSince ?? Date()
            cards[id] = c
            followState(activity, workspace: id)
            evaluate(id, leaving: false)
        case .ended, .unknown:
            retire(handle)
            await Self.end(activity, WorkspaceAgentActivityState(phase: .idle), dismissAt: Date())
        case .retry:
            retire(handle)
        }
    }

    // MARK: ActivityKit (nonisolated: looked up by id, never handed across)

    struct Snapshot: Sendable {
        let id: String
        let attributes: WorkspaceAgentActivityAttributes
        let state: WorkspaceAgentActivityState
        let active: Bool
    }

    nonisolated static func snapshotsNow() -> [Snapshot] {
        Activity<WorkspaceAgentActivityAttributes>.activities.map {
            Snapshot(id: $0.id, attributes: $0.attributes, state: $0.content.state, active: $0.activityState == .active)
        }
    }

    nonisolated static func update(_ id: String, _ s: WorkspaceAgentActivityState, staleDate: Date) async -> Bool {
        guard let a = Activity<WorkspaceAgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return false }
        await a.update(ActivityContent(state: s, staleDate: staleDate, relevanceScore: s.phase == .waiting ? 1 : 0.6))
        return true
    }

    nonisolated static func end(_ id: String, _ s: WorkspaceAgentActivityState, dismissAt: Date) async {
        guard let a = Activity<WorkspaceAgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return }
        await a.end(ActivityContent(state: s, staleDate: nil), dismissalPolicy: .after(dismissAt))
    }

    nonisolated static func tokens(of id: String, _ each: @escaping @Sendable (String) async -> Void) async {
        guard let a = Activity<WorkspaceAgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return }
        for await data in a.pushTokenUpdates { await each(APNsToken.hex(data)) }
    }

    /// Calls `each(true)` when the person dismissed the card (or the system
    /// ended it).
    nonisolated static func states(of id: String, _ each: @escaping @Sendable (Bool) async -> Void) async {
        guard let a = Activity<WorkspaceAgentActivityAttributes>.activities.first(where: { $0.id == id }) else { return }
        for await st in a.activityStateUpdates where st == .dismissed { await each(true) }
    }

    nonisolated static func newActivities(_ each: @escaping @Sendable (Snapshot) async -> Void) async {
        for await a in Activity<WorkspaceAgentActivityAttributes>.activityUpdates {
            await each(Snapshot(id: a.id, attributes: a.attributes, state: a.content.state, active: a.activityState == .active))
        }
    }

    nonisolated static func pushToStartTokens(_ each: @escaping @Sendable (String) async -> Void) async {
        for await data in Activity<WorkspaceAgentActivityAttributes>.pushToStartTokenUpdates { await each(APNsToken.hex(data)) }
    }
}
