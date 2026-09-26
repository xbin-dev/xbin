import Foundation
import UserNotifications
import XbinCore

/// Decrypts xbin pushes (native/spec/push.md §4): the relay sends generic
/// text ("New activity") with the sealed payload under `xbin`; this
/// extension opens it with the workspace's key (shared by the app through
/// the Keychain access group) and shows the real title and body. Anything
/// it can't open stays as delivered.
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
            content.title = p.title
            content.body = p.body
            content.threadIdentifier = p.ws
            content.categoryIdentifier = p.typedKind.category
            if let t = opened.title, !t.isEmpty { content.subtitle = t }
            var info = p.userInfo
            info["app"] = opened.appWorkspace
            content.userInfo = info
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
