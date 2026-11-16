import Foundation
import LocalAuthentication
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

    /// The app's own lock ("Require Face ID to open"), copied here for the
    /// extension (review finding 2): under it, a notification offers Review
    /// only. The app writes it whenever it changes and at every launch.
    static let deviceAccount = "@device"

    static func appLock(group: String?) -> Bool {
        guard let d = Keychain.read(service: prefsService, account: deviceAccount, group: group),
              let v = try? JSONDecoder().decode([String: Bool].self, from: d) else { return false }
        return v["appLock"] ?? false
    }

    static func setAppLock(_ on: Bool, group: String?) {
        guard let d = try? JSONEncoder().encode(["appLock": on]) else { return }
        Keychain.write(d, service: prefsService, account: deviceAccount, group: group, accessibility: .afterFirstUnlockThisDevice)
    }

    /// The device as a notification is drawn or acted on: the app's lock,
    /// whether a passcode is set (without one, device authentication asks
    /// nothing), and — the app's to say — whether protected data is there.
    static func device(group: String?, protectedData: Bool = true) -> AgentNoticeDevice {
        AgentNoticeDevice(appLock: appLock(group: group),
                          hasPasscode: LAContext().canEvaluatePolicy(.deviceOwnerAuthentication, error: nil),
                          protectedData: protectedData)
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
