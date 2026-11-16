import Foundation
import XbinCore

/// What the app and its Notification Service Extension share about the
/// workspace agent's notifications (D241; plans/wsagent/ios-stage2.md §9,
/// §11), in the Keychain access group, readable after the first unlock (the
/// extension runs while the phone is locked): per app workspace, the
/// person's settings — which lock-screen actions it offers — and its last
/// count of what waits, which the badge sums. Nothing here is a credential:
/// the extension can draw a notification, never decide one.
enum AgentNoticeStore {
    static let prefsService = "dev.xbin.agentnotify"
    static let badgeService = "dev.xbin.badges"
    static let badgeAccount = "counts"

    static func prefs(_ workspace: String, group: String?) -> AgentNotifyPrefs {
        guard let d = Keychain.read(service: prefsService, account: workspace, group: group),
              let p = try? JSONDecoder().decode(AgentNotifyPrefs.self, from: d) else { return AgentNotifyPrefs() }
        return p
    }

    static func setPrefs(_ p: AgentNotifyPrefs, _ workspace: String, group: String?) {
        guard let d = try? JSONEncoder().encode(p) else { return }
        Keychain.write(d, service: prefsService, account: workspace, group: group, accessibility: .afterFirstUnlockThisDevice)
    }

    static func counts(group: String?) -> [String: Int] {
        guard let d = Keychain.read(service: badgeService, account: badgeAccount, group: group),
              let c = try? JSONDecoder().decode([String: Int].self, from: d) else { return [:] }
        return c
    }

    /// Records `workspace`'s count and answers the total across workspaces.
    @discardableResult
    static func setCount(_ n: Int, for workspace: String, group: String?) -> Int {
        var c = counts(group: group)
        c[workspace] = max(0, n)
        if let d = try? JSONEncoder().encode(c) {
            Keychain.write(d, service: badgeService, account: badgeAccount, group: group, accessibility: .afterFirstUnlockThisDevice)
        }
        return AgentBadge.total(c)
    }

    static func forget(_ workspace: String, group: String?) {
        Keychain.delete(service: prefsService, account: workspace, group: group)
        var c = counts(group: group)
        c[workspace] = nil
        if let d = try? JSONEncoder().encode(c) {
            Keychain.write(d, service: badgeService, account: badgeAccount, group: group, accessibility: .afterFirstUnlockThisDevice)
        }
    }
}
