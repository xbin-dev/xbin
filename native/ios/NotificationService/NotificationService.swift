import Foundation
import UserNotifications
import XbinCore

/// Decrypts xbin pushes (native/spec/push.md §4): the relay sends generic
/// text ("New activity") with the sealed payload under `xbin`; this
/// extension opens it with the workspace's key (shared by the app through
/// the Keychain access group) and shows the real title and body. Anything
/// it can't open stays as delivered.
///
/// The workspace agent's kinds (D241) are drawn from the request's own
/// fields by XbinCore's AgentNotice — the title xbind's words, the call
/// itself only where Allow once may be offered, the category (which actions
/// show) from those fields and the person's settings for the workspace,
/// never from text. This extension holds the push keys and those settings,
/// nothing else: it draws, it never decides.
final class NotificationService: UNNotificationServiceExtension {
    private var contentHandler: ((UNNotificationContent) -> Void)?
    private var fallback: UNNotificationContent?

    override func didReceive(_ request: UNNotificationRequest,
                             withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
        self.contentHandler = contentHandler
        fallback = request.content
        let content = (request.content.mutableCopy() as? UNMutableNotificationContent) ?? UNMutableNotificationContent()
        if let opened = Self.open(userInfo: request.content.userInfo) {
            let p = opened.payload
            let prefs = AgentNoticeStore.prefs(opened.appWorkspace, group: Self.keychainGroup)
            if let c = AgentNotice.content(p, appWorkspace: opened.appWorkspace, prefs: prefs) {
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
                    content.badge = NSNumber(value: AgentNoticeStore.setCount(b, for: opened.appWorkspace, group: Self.keychainGroup))
                }
            } else {
                content.title = p.title
                content.body = p.body
                // Per workspace; xbind's notices linking one of its pages (the
                // partitions page, D181) in a group of their own, with that
                // page's actions (PushPayload.threadID, .category).
                content.threadIdentifier = p.threadID
                content.categoryIdentifier = p.category
                var info = p.userInfo
                info["app"] = opened.appWorkspace
                content.userInfo = info
            }
            if let t = opened.title, !t.isEmpty { content.subtitle = t }
        }
        contentHandler(content)
        self.contentHandler = nil
    }

    override func serviceExtensionTimeWillExpire() {
        if let h = contentHandler, let f = fallback { h(f) }
        contentHandler = nil
    }

    struct Opened {
        var appWorkspace: String
        var payload: PushPayload
        var title: String?
    }

    static var keychainGroup: String? {
        guard let g = Bundle.main.object(forInfoDictionaryKey: "XbinKeychainGroup") as? String,
              !g.isEmpty, !g.hasPrefix("$(") else { return nil }
        return g
    }

    /// `userInfo["xbin"]` → the envelope → the first workspace key that
    /// opens it whose push id matches the payload's `ws`.
    static func open(userInfo: [AnyHashable: Any]) -> Opened? {
        guard let raw = userInfo["xbin"], JSONSerialization.isValidJSONObject(raw),
              let data = try? JSONSerialization.data(withJSONObject: raw),
              let json = try? JSONValue(parsing: data), let env = PushEnvelope(json: json) else { return nil }
        let ring = PushKeyring.load(group: keychainGroup)
        let candidates = ring.entries.map { (appWorkspace: $0.workspace, pushWorkspace: $0.pushWorkspace) }
        guard let o = PushOpener.open(env, candidates: candidates, open: { ws in
            ring[ws].flatMap { PushCrypto.open(env, privateKey: $0.privateKey) }
        }) else { return nil }
        return Opened(appWorkspace: o.appWorkspace, payload: o.payload, title: ring[o.appWorkspace]?.title)
    }
}
