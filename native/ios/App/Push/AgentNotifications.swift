import BackgroundTasks
import Foundation
import UIKit
import UserNotifications
import XbinCore

/// The workspace agent's notifications in the app (D241; plans/wsagent/
/// ios-stage2.md §3–§4, §9–§11). The extension draws them (XbinCore's
/// AgentNotice: the category from the request's fields and the person's
/// settings); here are the categories' actions, what an action does, the
/// follow-ups, the background push that takes them back, the sweep on
/// foreground and background refresh, the badge, and the settings.
///
/// An action decides only through `WorkspaceAgentModel` with the device's
/// session — the sheet's own path — after the system's device
/// authentication (every action here is `.authenticationRequired`), after
/// re-reading the request (AgentNoticeDecision.verify: as shown, still
/// waiting, the setting on now). Unreachable: nothing is sent and the
/// follow-up says so, with Retry — never a queue. A reply is a message.
@MainActor
final class AgentNotifications {
    static let shared = AgentNotifications()

    var group: String? { AppInfo.sharedKeychainGroup }

    // MARK: categories

    static var categories: Set<UNNotificationCategory> {
        let auth: UNNotificationActionOptions = [.authenticationRequired]
        let allow = UNNotificationAction(identifier: AgentNotice.allowOnceAction, title: "Allow once", options: auth)
        let deny = UNNotificationAction(identifier: AgentNotice.denyAction, title: "Deny", options: [.authenticationRequired, .destructive])
        let reject = UNNotificationAction(identifier: AgentNotice.rejectAction, title: "Reject", options: [.authenticationRequired, .destructive])
        let review = UNNotificationAction(identifier: AgentNotice.reviewAction, title: "Review…", options: [.foreground, .authenticationRequired])
        let open = UNNotificationAction(identifier: AgentNotice.openAction, title: "Open", options: [.foreground, .authenticationRequired])
        let retry = UNNotificationAction(identifier: AgentNotice.retryAction, title: "Retry", options: auth)
        let reply = UNTextInputNotificationAction(identifier: AgentNotice.replyAction, title: "Reply", options: auth,
                                                  textInputButtonTitle: "Send", textInputPlaceholder: "Message your agent")
        func cat(_ id: String, _ actions: [UNNotificationAction]) -> UNNotificationCategory {
            UNNotificationCategory(identifier: id, actions: actions, intentIdentifiers: [])
        }
        return [
            // Deny left of Allow on the lock screen's menu is the system's
            // order: the actions as listed. Review… last, always there.
            cat(AgentNotice.requestAllowDeny, [allow, deny, review]),
            cat(AgentNotice.requestAllow, [allow, review]),
            cat(AgentNotice.requestDeny, [deny, review]),
            cat(AgentNotice.requestReview, [review]),
            cat(AgentNotice.changesetReject, [reject, review]),
            cat(AgentNotice.changesetReview, [review]),
            cat(AgentNotice.conversation, [reply, open]),
            cat(AgentNotice.conversationOpen, [open]),
            cat(AgentNotice.followupRetry, [retry, review]),
            cat(AgentNotice.followup, [review]),
        ]
    }

    // MARK: settings

    func prefs(_ w: WorkspaceModel) -> AgentNotifyPrefs { AgentNoticeStore.prefs(w.id, group: group) }

    /// Saves the person's settings for `w`: the extension reads them, the
    /// registration's kinds follow (xbind sends nothing they turned off),
    /// and the card goes when they turn it off.
    func setPrefs(_ p: AgentNotifyPrefs, for w: WorkspaceModel) {
        let was = prefs(w)
        AgentNoticeStore.setPrefs(p, w.id, group: group)
        if !p.liveActivity { AgentLiveActivity.shared.end(workspace: w.id) }
        if was.kinds(base: PushAPI.appKinds) != p.kinds(base: PushAPI.appKinds) || was.liveActivity != p.liveActivity {
            Task { await PushManager.shared.maintain(w) }
        }
    }

    // MARK: actions

    /// The device now: the app's lock as the app has it, a passcode, protected data.
    func deviceNow() -> AgentNoticeDevice {
        var d = AgentNoticeStore.device(group: group, protectedData: UIApplication.shared.isProtectedDataAvailable)
        d.appLock = AppSettings.appLock
        return d
    }

    /// A notification's action, or its tap (`action` the default one). True:
    /// an agent notification, handled here.
    func handle(info: [String: String], action: String, text: String?, identifier: String) async -> Bool {
        guard let kind = info["kind"], kind.hasPrefix("agent."), kind != "agent.permission",
              kind != "agent.question", kind != "agent.turn", let app = info["app"],
              let w = AppModel.shared.workspace(app) else { return false }
        let a = AgentNoticeAction.of(actionID: action, text: text)
        // the app's own lock and the device's authentication come first, whatever
        // the notification's category offered (an old one keeps its actions)
        let tried = info["tried"].map { AgentNoticeAction.of(actionID: $0, text: nil) }
        if let why = AgentNoticeGate.refusal(a, tried: tried, device: deviceNow()) {
            if case .message = a {
                followUp(w, identifier: UUID().uuidString, info: info, note: AgentNoticeWords.notSent(why))
            } else {
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(why))
            }
            return true
        }
        switch a {
        case .open:
            open(info: info, in: w)
        case .message(let t):
            await reply(t, info: info, in: w)
        case .allowOnce, .deny:
            guard let shown = info["shown"].flatMap(AgentShownRequest.init(encoded:)) else {
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(""))
                return true
            }
            await decide(a, shown: shown, info: info, in: w, identifier: identifier)
        case .reject:
            guard let id = info["changeset"], DelegateID.isRequest(id) else { return true }
            await reject(id, info: info, in: w, identifier: identifier)
        case .retry:
            // the follow-up carries what was tried: the whole flow again, re-read first
            if let shown = info["shown"].flatMap(AgentShownRequest.init(encoded:)), let tried = info["tried"] {
                await decide(AgentNoticeAction.of(actionID: tried, text: nil), shown: shown, info: info, in: w, identifier: identifier)
            } else if let id = info["changeset"], DelegateID.isRequest(id) {
                await reject(id, info: info, in: w, identifier: identifier)
            }
        }
        return true
    }

    /// Review…, Open, a tap: the router, on exactly the thing.
    func open(info: [String: String], in w: WorkspaceModel) {
        let link = info["link"] ?? ""
        let deep = PushPayload(ws: "", kind: "", title: "", link: link).deepLink(appWorkspace: w.id)
        if case .workspaceAgent = deep {
            AppModel.shared.open(link: deep)
        } else if let c = info["conversation"], AgentRoute.isConversation(c) {
            AppModel.shared.open(link: .workspaceAgent(workspace: w.id, route: .conversation(c)))
        } else {
            AppModel.shared.open(link: .page(workspace: w.id, page: .agent))
        }
    }

    private func decide(_ a: AgentNoticeAction, shown: AgentShownRequest, info: [String: String], in w: WorkspaceModel, identifier: String) async {
        let call = shown.whole ? AgentNotice.callText(method: shown.method, target: shown.target, body: shown.body) : ""
        var info = info
        info["tried"] = a == .allowOnce ? AgentNotice.allowOnceAction : AgentNotice.denyAction
        await TunnelManager.shared.inBackground {
            let read = await w.agent.readForNotice(shown.id)
            let verdict = AgentNoticeDecision.verify(a, shown: shown, current: read.request, reachable: read.reachable, prefs: prefs(w))
            switch verdict {
            case .unreachable:
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.unreachable(w.title, call: call))
            case .gone(let r):
                let place = r == nil ? "" : await placeOf(r!, in: w)
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.gone(r, where: place), passive: true)
            case .changed:
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.changed())
            case .review(let why):
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(why))
            case .send:
                let req = a == .allowOnce ? DelegateRoute.approveFromNotification(shown.id) : DelegateRoute.denyFromNotification(shown.id)
                switch await w.agent.decideForNotice(req, id: shown.id) {
                case .done:
                    let note = a == .allowOnce ? AgentNoticeWords.allowed(call: call)
                        : AgentNoticeWords.denied(call.isEmpty ? "Your agent is told it was denied." : call)
                    followUp(w, identifier: identifier, info: info, note: note, passive: true)
                case .gone:
                    let r = await w.agent.readForNotice(shown.id).request
                    let place = r == nil ? "" : await placeOf(r!, in: w)
                    followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.gone(r, where: place), passive: true)
                case .stopped(_, let message):
                    followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.stopped(message))
                case .failed where w.agent.unreachable:
                    // the answer may have been lost after xbind applied it: read again, say what is true
                    let again = await w.agent.readForNotice(shown.id)
                    switch AgentNoticeDecision.afterTransportFailure(a, reread: again.request, reachable: again.reachable) {
                    case .decided:
                        let note = a == .allowOnce ? AgentNoticeWords.allowed(call: call)
                            : AgentNoticeWords.denied(call.isEmpty ? "Your agent is told it was denied." : call)
                        followUp(w, identifier: identifier, info: info, note: note, passive: true)
                    case .notDecided:
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.unreachable(w.title, call: call))
                    case .elsewhere:
                        let place = await placeOf(again.request!, in: w)
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.gone(again.request, where: place), passive: true)
                    case .unknown:
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.unconfirmed(w.title))
                    }
                case .failed(let why), .stepUp(let why, _):
                    followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.failed(why, allow: a == .allowOnce))
                case .unseen:
                    followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(""))
                }
            }
        }
        await sweep(w)
    }

    private func reject(_ id: String, info: [String: String], in w: WorkspaceModel, identifier: String) async {
        var info = info
        info["tried"] = AgentNotice.rejectAction
        await TunnelManager.shared.inBackground {
            let read = await w.agent.readSetForNotice(id)
            switch AgentNoticeDecision.verifyReject(setID: id, current: read.set, reachable: read.reachable, prefs: prefs(w)) {
            case .unreachable:
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.unreachable(w.title))
            case .gone, .changed:
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(""), passive: true)
            case .review(let why):
                followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(why))
            case .send:
                switch await w.agent.decideForNotice(DelegateRoute.rejectFromNotification(id), id: nil) {
                case .done: followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.rejected(), passive: true)
                case .failed(let why) where !w.agent.unreachable:
                    followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.failed(why, allow: false))
                case .failed:
                    // the answer may have been lost after xbind applied it: read again, say what is true
                    let again = await w.agent.readSetForNotice(id)
                    if !again.reachable || again.set == nil {
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.unconfirmed(w.title))
                    } else if again.set?.isProposed == true {
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.unreachable(w.title))
                    } else if again.set?.state == .rejected {
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.rejected(), passive: true)
                    } else {
                        followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(again.set.map(AgentNoticeWords.setSettled) ?? ""), passive: true)
                    }
                default: followUp(w, identifier: identifier, info: info, note: AgentNoticeWords.review(""))
                }
            }
        }
        await sweep(w)
    }

    /// Reply: the text, as the person's message, to the conversation the
    /// notification named — and nothing else (it never decides anything).
    private func reply(_ text: String, info: [String: String], in w: WorkspaceModel) async {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, let conv = info["conversation"], AgentRoute.isConversation(conv) else { return }
        await TunnelManager.shared.inBackground {
            let out = await w.agent.message(conversation: conv, text: t)
            if let why = out {
                followUp(w, identifier: UUID().uuidString, info: info, note: AgentNoticeWords.replyFailed(w.title, text: t, why: why))
            }
        }
    }

    /// Where a decided request was decided, in words (§7.3).
    func placeOf(_ r: DelegateRequest, in w: WorkspaceModel) async -> String {
        var names: [String: String] = [:]
        if !r.decidedDevice.isEmpty, r.decidedDevice != w.record.deviceId,
           let j = try? await w.auth.json(APIRequest("GET", AppAuthRoute.devices)) {
            for d in DeviceInfo.list(json: j) { names[d.id] = d.name }
        }
        return AgentNoticeWords.decidedWhere(r, thisDevice: w.record.deviceId ?? "", thisDeviceName: "this \(UIDevice.current.model)",
                                             deviceNames: names)
    }

    /// A follow-up in place of the notification that was acted on (same
    /// identifier): what came of it, and — when nothing was decided and it
    /// can be tried again — Retry. It carries the request's shown fields
    /// again, so a Retry is checked exactly as the first try was.
    func followUp(_ w: WorkspaceModel, identifier: String, info: [String: String], note: AgentNoticeWords.Note, passive: Bool = false) {
        let c = UNMutableNotificationContent()
        c.title = note.title
        c.body = note.body
        c.subtitle = w.title
        c.threadIdentifier = info["ws"].map { "\($0)/xbin/agent" } ?? w.id
        c.categoryIdentifier = note.retry ? AgentNotice.followupRetry : AgentNotice.followup
        var keep: [String: String] = ["kind": "agent.followup", "app": w.id]
        for k in ["ws", "link", "shown", "changeset", "conversation", "tried", "collapse"] {
            if let v = info[k] { keep[k] = v }
        }
        c.userInfo = keep
        // what came of it is said in a banner; a sound only when nothing was decided
        c.interruptionLevel = .active
        c.sound = passive ? nil : .default
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: identifier, content: c, trigger: nil))
    }

    // MARK: background pushes, the sweep, the badge

    /// A background push: an `agent.clear` — opened with the workspace's
    /// push key, as the extension opens an alert — takes back what it names
    /// and sets the badge. Anything else (the relay's token check) is nothing.
    func backgroundPush(_ userInfo: SendableUserInfo) async -> UIBackgroundFetchResult {
        guard let opened = PushOpen.open(userInfo: userInfo.value, group: group), opened.payload.kind == "agent.clear",
              let data = AgentNoticeData(json: opened.payload.agent) else { return .noData }
        let delivered = await Self.delivered()
        let gone = AgentNoticeSweep.cleared(delivered, app: opened.appWorkspace, clear: data.clear)
        if !gone.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: gone) }
        if let b = data.badge { setBadge(AgentNoticeStore.setCount(b, for: opened.appWorkspace, group: group)) }
        if let w = AppModel.shared.workspace(opened.appWorkspace) { AgentLiveActivity.shared.observe(w) }
        return .newData
    }

    /// Takes back the delivered agent notifications of `w` whose thing doesn't
    /// wait any more (after a read of what waits), and sets the badge.
    func sweep(_ w: WorkspaceModel) async {
        let agent = w.agent
        guard agent.state != nil, !agent.unreachable else { return }
        let delivered = await Self.delivered()
        let stale = AgentNoticeSweep.stale(delivered, app: w.id, pendingRequests: Set(agent.requests.map(\.id)),
                                           openSets: Set(agent.changesets.filter(\.isProposed).map(\.id)))
        if !stale.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: stale) }
        updateBadge()
    }

    /// Takes back the conversation's own notifications (a question, a
    /// "finished") once the person looks at it.
    func seen(conversation: String, in w: WorkspaceModel) {
        Task {
            let ids = await Self.delivered().filter { $0.app == w.id && $0.collapse == "agent-conv:\(conversation)" }.map(\.identifier)
            if !ids.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids) }
        }
    }

    /// The badge: what waits for the person across workspaces.
    func updateBadge() {
        var total = 0
        for w in AppModel.shared.workspaces {
            total = AgentNoticeStore.setCount(w.needsYouCount, for: w.id, group: group)
        }
        setBadge(total)
    }

    private func setBadge(_ n: Int) {
        UNUserNotificationCenter.current().setBadgeCount(n) { _ in }
    }

    static func delivered() async -> [AgentNoticeSweep.Delivered] {
        await withCheckedContinuation { (c: CheckedContinuation<[AgentNoticeSweep.Delivered], Never>) in
            UNUserNotificationCenter.current().getDeliveredNotifications { notes in
                c.resume(returning: notes.compactMap { n in
                    let info = n.request.content.userInfo
                    guard let kind = info["kind"] as? String, kind.hasPrefix("agent.") else { return nil }
                    return AgentNoticeSweep.Delivered(identifier: n.request.identifier, app: info["app"] as? String ?? "",
                                                      kind: kind, collapse: info["collapse"] as? String ?? "")
                })
            }
        }
    }

    // MARK: background refresh (§9)

    static let refreshTask = "dev.xbin.app.refresh"

    /// At launch, before the app finishes launching (BGTaskScheduler's rule).
    func registerBackgroundRefresh() {
        BGTaskScheduler.shared.register(forTaskWithIdentifier: Self.refreshTask, using: nil) { task in
            Task { @MainActor in
                AgentNotifications.shared.scheduleRefresh()
                await AgentNotifications.shared.refreshAll()
                task.setTaskCompleted(success: true)
            }
        }
    }

    /// As often as iOS allows — about every half hour, at most.
    func scheduleRefresh() {
        let r = BGAppRefreshTaskRequest(identifier: Self.refreshTask)
        r.earliestBeginDate = Date().addingTimeInterval(30 * 60)
        try? BGTaskScheduler.shared.submit(r)
    }

    /// Each workspace's agent read again: the sweep, the badge, the card.
    func refreshAll() async {
        await TunnelManager.shared.inBackground {
            for w in AppModel.shared.workspaces {
                await w.refreshSessions()
                await w.agent.refresh()
                await sweep(w)
                AgentLiveActivity.shared.observe(w)
            }
        }
        updateBadge()
    }
}

/// A push's userInfo handed to the main actor: property-list values,
/// copied as they are (what the app reads of it is strings).
struct SendableUserInfo: @unchecked Sendable {
    let value: [AnyHashable: Any]
    init(_ v: [AnyHashable: Any]) { value = v }

    /// Its string values by string key — what an agent notification's
    /// userInfo holds (AgentNotice.Content.userInfo).
    var strings: [String: String] {
        var out: [String: String] = [:]
        for (k, v) in value { if let k = k as? String, let v = v as? String { out[k] = v } }
        return out
    }
}

/// Opening a push in the app (an `agent.clear` arrives as a background push,
/// which no extension sees): the extension's rule — the first workspace key
/// that opens it whose push id is the payload's `ws`.
enum PushOpen {
    struct Opened {
        var appWorkspace: String
        var payload: PushPayload
    }

    static func open(userInfo: [AnyHashable: Any], group: String?) -> Opened? {
        guard let raw = userInfo["xbin"], JSONSerialization.isValidJSONObject(raw),
              let data = try? JSONSerialization.data(withJSONObject: raw),
              let json = try? JSONValue(parsing: data), let env = PushEnvelope(json: json) else { return nil }
        let ring = PushKeyring.load(group: group)
        let candidates = ring.entries.map { (appWorkspace: $0.workspace, pushWorkspace: $0.pushWorkspace) }
        guard let o = PushOpener.open(env, candidates: candidates, open: { ws in
            ring[ws].flatMap { PushCrypto.open(env, privateKey: $0.privateKey) }
        }) else { return nil }
        return Opened(appWorkspace: o.appWorkspace, payload: o.payload)
    }
}
