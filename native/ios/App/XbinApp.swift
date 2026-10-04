import SwiftUI
import UIKit
import UserNotifications
import XbinCore
import XbinRenderer

@main
struct XbinApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var model = AppModel.shared
    @Environment(\.scenePhase) private var scenePhase

    /// One window group; each window is a RootView with its own workspace
    /// and navigation (SceneModel). `openWindow(value: WindowTarget(…))`
    /// opens another on a workspace or a tile (iPad, Stage Manager, Mac).
    var body: some Scene {
        WindowGroup(for: WindowTarget.self) { $target in
            // The accent (cobalt, periwinkle in dark: Base Two, D185); each
            // window's appearance is RootView's (the person's theme).
            RootView(target: $target)
                .environment(model)
                .tint(XbinColor.accent)
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .active:
                model.enteredForeground()
                Task {
                    await model.unlock()
                    await model.becameActive()
                }
            case .background:
                if AppSettings.appLock { model.locked = true }
                model.enteredBackground()
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
        BaseTwoChrome.apply()
        #if DEBUG
        // The UI tests (-XbinUITesting): UIKit's animations off, so the app
        // is idle between steps — XCUITest waited 60 s for them after every
        // long press and menu. Never in a release build.
        if TerminalController.uiTesting { UIView.setAnimationsEnabled(false) }
        #endif
        // The remote kill switch before anything else (§23): a build whose
        // native views crash learns it's off even if a window restores
        // straight into one.
        AppModel.shared.refreshRemoteSwitch()
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
        Task { @MainActor in await AppModel.shared.workspace(app ?? "")?.refreshSessions() }
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

    /// Whether a foreground window already shows what the notification is
    /// about (any window: each has its own workspace and surface).
    @MainActor static func isShowing(app: String?, link: String) -> Bool {
        guard UIApplication.shared.applicationState == .active else { return false }
        return AppModel.shared.isShowing(app: app, link: link)
    }
}

/// What SwiftUI can't style from a view (D185): navigation bars' large
/// titles in Bricolage Grotesque 800, scaled with Dynamic Type (the inline
/// titles, bar items and body stay the system font).
enum BaseTwoChrome {
    @MainActor static func apply() {
        update()
        NotificationCenter.default.addObserver(forName: UIContentSizeCategory.didChangeNotification, object: nil,
                                               queue: .main) { _ in MainActor.assumeIsolated { update() } }
    }

    @MainActor private static func update() {
        let base = XbinFont.uiFont(XbinFaces.display, size: 34, fallbackWeight: .heavy)
        let large = UIFontMetrics(forTextStyle: .largeTitle).scaledFont(for: base, maximumPointSize: 64)
        UINavigationBar.appearance().largeTitleTextAttributes = [.font: large]
    }
}
