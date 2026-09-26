import SwiftUI
import UIKit
import UserNotifications
import XbinCore

@main
struct XbinApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var model = AppModel.shared
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .onOpenURL { model.open(url: $0) }
                .tint(Color.xbinAmber)
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .active:
                Task {
                    await model.unlock()
                    await model.becameActive()
                }
            case .background:
                if AppSettings.appLock { model.locked = true }
            default: break
            }
        }
    }
}

/// APNs registration and notification taps.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        if !AppModel.shared.workspaces.isEmpty { Task { await PushManager.shared.start() } }
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        PushManager.shared.didRegister(deviceToken: deviceToken)
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: any Error) {}

    /// The relay's silent token check carries no `xbin`: nothing to do.
    func application(_ application: UIApplication, didReceiveRemoteNotification userInfo: [AnyHashable: Any]) async
        -> UIBackgroundFetchResult {
        .noData
    }

    /// Foreground: no banner when the user is already looking at the very
    /// surface the notification links to (push.md §4.5).
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions {
        let info = notification.request.content.userInfo
        let app = info["app"] as? String
        let link = info["link"] as? String ?? ""
        let showing = await MainActor.run { AppDelegate.isShowing(app: app, link: link) }
        if showing { return [] }
        await MainActor.run { Task { await AppModel.shared.workspace(app ?? "")?.refreshSessions() } }
        return [.banner, .list, .sound]
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        let app = info["app"] as? String ?? ""
        let link = info["link"] as? String ?? ""
        let action = response.actionIdentifier
        await MainActor.run {
            guard let w = AppModel.shared.workspace(app) else { return }
            if action == "mute", link.hasPrefix("c/") {
                let tile = PushPayload(ws: "", kind: "tile", title: "", link: link).deepLink(appWorkspace: w.id)
                if case .tile(_, let path, _) = tile { Task { await PushManager.shared.mute(tile: path, in: w) } }
                return
            }
            let payload = PushPayload(ws: "", kind: "", title: "", link: link)
            AppModel.shared.open(link: payload.deepLink(appWorkspace: w.id))
        }
    }

    @MainActor static func isShowing(app: String?, link: String) -> Bool {
        let m = AppModel.shared
        guard let app, m.selectedID == app, let w = m.workspace(app), let s = w.surface,
              UIApplication.shared.applicationState == .active else { return false }
        switch PushPayload(ws: "", kind: "", title: "", link: link).deepLink(appWorkspace: app) {
        case .tile(_, let t, _): if case .tile(let p, _, _) = s { return p == t }
        case .agent(_, let id): if case .agent(_, let sid) = s { return sid == id }
        default: break
        }
        return false
    }
}

extension Color {
    /// xbin amber (`#f5a623`, plans/native.md §10.1: the tint).
    static let xbinAmber = Color(red: 0xF5 / 255, green: 0xA6 / 255, blue: 0x23 / 255)
}
