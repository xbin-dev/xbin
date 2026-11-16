import Foundation
import UserNotifications
import XbinCore

/// What the Notification Service Extension draws for an opened agent
/// payload (D241): the text, category, thread, userInfo and badge from
/// XbinCore's AgentNotice, with this workspace's settings and the device's
/// state from the shared Keychain group. Shared with the app only so its
/// Debug UI tests run this very code on xbind's real sealed pushes (a
/// simulator's `simctl push` doesn't wake the extension).
enum AgentNoticeDraw {
    /// Fills `content`; false when `p` isn't an agent kind this app draws
    /// (the caller draws xbind's own text then).
    static func agent(_ p: PushPayload, appWorkspace: String, group: String?, into content: UNMutableNotificationContent) -> Bool {
        let prefs = AgentNoticeStore.prefs(appWorkspace, group: group)
        let device = AgentNoticeStore.device(group: group)
        guard let c = AgentNotice.content(p, appWorkspace: appWorkspace, prefs: prefs, device: device) else { return false }
        content.title = c.title
        content.body = c.body
        content.threadIdentifier = c.threadID
        content.categoryIdentifier = c.category
        content.userInfo = c.userInfo
        content.interruptionLevel = c.timeSensitive ? .timeSensitive : .active
        content.relevanceScore = c.timeSensitive ? 1 : 0.5
        if let b = c.badge {
            // this workspace's count, with the others' last ones: the
            // app icon says what waits across workspaces
            content.badge = NSNumber(value: AgentNoticeStore.setCount(b, for: appWorkspace, group: group))
        }
        return true
    }
}
