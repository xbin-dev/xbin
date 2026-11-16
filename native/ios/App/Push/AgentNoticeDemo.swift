#if DEBUG
import Foundation
import UIKit
import UserNotifications
import XbinCore

/// Debug builds only (the UI tests, D241): `-XbinAgentNotice <what>` posts,
/// `-XbinAgentNoticeDelay` seconds after the person leaves the app (default 4), the
/// notification a push about `<what>` makes — drawn by XbinCore's
/// AgentNotice exactly as the extension draws a sealed push (the simulator
/// gets no APNs push sealed to this install's key, and the extension runs
/// only for those). `<what>`: `request:<id>` (read from xbind with the
/// device's session, as the extension never could: the payload is the
/// shape xbind sends, `offer` by the same rule), `changeset:<id>`,
/// `ask:<conversation>`, `done:<conversation>`. `-XbinAgentAllowOnce YES`
/// turns Allow once on for the first workspace, as the person would in
/// Settings. A Release build has none of it.
enum AgentNoticeDemo {
    @MainActor static func runIfAsked() {
        let d = UserDefaults.standard
        if d.bool(forKey: "XbinAgentAllowOnce") {
            // once there is a workspace (a first launch signs in after it starts)
            Task { @MainActor in
                for _ in 0..<600 where AppModel.shared.workspaces.isEmpty { try? await Task.sleep(nanoseconds: 200_000_000) }
                guard let w = AppModel.shared.workspaces.first else { return }
                var p = AgentNotifications.shared.prefs(w)
                p.lockAllowOnce = true
                p.allowOnceExplained = true
                AgentNoticeStore.setPrefs(p, w.id, group: AppInfo.sharedKeychainGroup)
            }
        }
        guard let what = d.string(forKey: "XbinAgentNotice"), !what.isEmpty else { return }
        // a launch argument is a string: double(forKey:) reads it
        let delay = d.object(forKey: "XbinAgentNoticeDelay") == nil ? 4 : d.double(forKey: "XbinAgentNoticeDelay")
        // a test may leave the app before xbind has answered: finish in the background
        hold = UIApplication.shared.beginBackgroundTask(withName: "xbin-agent-notice-demo")
        Task { @MainActor in
            // the app asks once whether it may notify (with push, PushManager.start asks)
            _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
            for _ in 0..<150 where AppModel.shared.workspaces.first?.agent.state == nil {
                if let w = AppModel.shared.workspaces.first { await w.agent.refresh() }
                try? await Task.sleep(nanoseconds: 200_000_000)
            }
            guard let w = AppModel.shared.workspaces.first, let payload = await payload(what, in: w) else {
                NSLog("xbin-agent notice demo: nothing for %@", what)
                release()
                return
            }
            // a push comes while the app is not open: the notification is drawn
            // when the person leaves the app — with the settings as they are then,
            // as the extension draws it with the settings at arrival
            pending = (payload, w.id, delay)
            if UIApplication.shared.applicationState == .background { post(); return }
            observer = NotificationCenter.default.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { post() }
            }
        }
    }

    @MainActor private static var pending: (payload: PushPayload, workspace: String, delay: Double)?
    @MainActor private static var observer: NSObjectProtocol?
    @MainActor private static var hold = UIBackgroundTaskIdentifier.invalid

    @MainActor private static func release() {
        if hold != .invalid { UIApplication.shared.endBackgroundTask(hold); hold = .invalid }
    }

    @MainActor private static func post() {
        guard let (payload, id, delay) = pending, let w = AppModel.shared.workspaces.first(where: { $0.id == id }) else { return }
        pending = nil
        if let o = observer { NotificationCenter.default.removeObserver(o); observer = nil }
        let prefs = AgentNotifications.shared.prefs(w)
        guard let c = AgentNotice.content(payload, appWorkspace: w.id, prefs: prefs) else {
            release()
            return
        }
        let n = UNMutableNotificationContent()
        n.title = c.title
        n.subtitle = w.title
        n.body = c.body
        n.threadIdentifier = c.threadID
        n.categoryIdentifier = c.category
        n.userInfo = c.userInfo
        n.interruptionLevel = c.timeSensitive ? .timeSensitive : .active
        n.sound = .default
        if let b = c.badge { n.badge = NSNumber(value: AgentNoticeStore.setCount(b, for: w.id, group: AppInfo.sharedKeychainGroup)) }
        let trigger = UNTimeIntervalNotificationTrigger(timeInterval: max(1, delay), repeats: false)
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: payload.collapseID, content: n, trigger: trigger)) { _ in
            DispatchQueue.main.async { release() }
        }
        NSLog("xbin-agent notice demo: %@ in %.0f s, category %@", payload.collapseID, delay, c.category)
    }

    @MainActor private static func payload(_ what: String, in w: WorkspaceModel) async -> PushPayload? {
        let parts = what.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2 else { return nil }
        let id = parts[1]
        let badge = JSONValue.int(Int64(w.needsYouCount))
        switch parts[0] {
        case "request":
            guard let r = await w.agent.readForNotice(id).request else { return nil }
            var req: [String: JSONValue] = ["id": .string(r.id), "kind": .string(r.kind.rawValue), "tile": .string(r.tilePath),
                                            "tileName": .string(w.agent.tileName(r.tilePath)), "offer": ""]
            var probe = AgentNoticeData.Request()
            probe.id = r.id; probe.kind = r.kind.rawValue; probe.tile = r.tilePath; probe.offer = "once"
            probe.method = r.method; probe.target = r.target; probe.contentType = r.contentType
            probe.body = r.body; probe.bodySize = r.bodySize; probe.bodySha256 = r.bodySha256
            if r.isPending, r.bodyShown, !r.bodyBinary, r.preview["readWeb"]?.boolValue != true, AgentNotice.eligible(probe) {
                req["offer"] = "once"
                req["method"] = .string(r.method)
                req["target"] = .string(r.target)
                req["contentType"] = .string(r.contentType)
                req["body"] = .string(r.body)
                req["bodySize"] = .int(Int64(r.bodySize))
                req["bodySha256"] = .string(r.bodySha256)
            }
            return PushPayload(ws: PushManager.shared.state(w.id).pushWorkspace ?? "demo", kind: "agent.approval",
                               title: "Approval needed", body: "", link: "xbin/agent#r=\(r.id)", collapseID: "agent-approval:\(r.id)",
                               agent: .object(["v": 1, "badge": badge, "request": .object(req)]))
        case "changeset":
            guard let c = await w.agent.readSetForNotice(id).set else { return nil }
            let set: JSONValue = .object(["id": .string(c.id), "items": .int(Int64(c.items.count)),
                                          "warned": .int(Int64(c.items.filter { !$0.warnings.isEmpty }.count)), "plan": .bool(c.plan),
                                          "asks": .bool(!c.from.isEmpty)])
            return PushPayload(ws: "demo", kind: "agent.changeset", title: c.plan ? "A plan to review" : "Admin changes proposed",
                               body: "Nothing is applied until you approve.", link: "xbin/agent#cs=\(c.id)", collapseID: "agent-changeset:\(c.id)",
                               agent: .object(["v": 1, "badge": badge, "changeset": set]))
        case "ask", "done":
            let ask = parts[0] == "ask"
            var agent: [String: JSONValue] = ["v": 1, "badge": badge, "conversation": .object(["id": .string(id)])]
            if ask { agent["ask"] = "Which day should the note say — today or tomorrow?" }
            return PushPayload(ws: "demo", kind: ask ? "agent.ask" : "agent.done",
                               title: ask ? "Question from your workspace agent" : "Workspace agent finished",
                               body: ask ? "Open the agent to answer, or reply here." : "The report is ready.",
                               link: "xbin/agent#c=\(id)", collapseID: "agent-conv:\(id)", agent: .object(agent))
        default:
            return nil
        }
    }
}
#endif
