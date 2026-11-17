@_exported import Foundation
import UIKit

// What PushManager uses of UserNotifications, and the UIKit members the
// SwiftUI stub check's UIKit lacks.

public struct UNAuthorizationOptions: OptionSet, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let alert = UNAuthorizationOptions(rawValue: 1), sound = UNAuthorizationOptions(rawValue: 2), badge = UNAuthorizationOptions(rawValue: 4)
}

public struct UNNotificationActionOptions: OptionSet, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let foreground = UNNotificationActionOptions(rawValue: 1), destructive = UNNotificationActionOptions(rawValue: 2)
    public static let authenticationRequired = UNNotificationActionOptions(rawValue: 4)
}

public enum UNNotificationInterruptionLevel: UInt, Sendable { case passive, active, timeSensitive, critical }

public final class UNNotificationSound: NSObject, @unchecked Sendable {
    public static let `default` = UNNotificationSound()
}

public class UNNotificationContent: NSObject, @unchecked Sendable {
    public var userInfo: [AnyHashable: Any] { [:] }
    public var threadIdentifier: String { "" }
}

public final class UNMutableNotificationContent: UNNotificationContent, @unchecked Sendable {
    public var title = ""
    public var subtitle = ""
    public var body = ""
    public var badge: NSNumber?
    public var sound: UNNotificationSound?
    public var categoryIdentifier = ""
    public var interruptionLevel: UNNotificationInterruptionLevel = .active
    public var relevanceScore: Double = 0
    public var mutableUserInfo: [AnyHashable: Any] = [:]
    override public var userInfo: [AnyHashable: Any] { get { mutableUserInfo } set { mutableUserInfo = newValue } }
    public var mutableThreadIdentifier = ""
    override public var threadIdentifier: String { get { mutableThreadIdentifier } set { mutableThreadIdentifier = newValue } }
}

public class UNNotificationTrigger: NSObject, @unchecked Sendable {}
public final class UNTimeIntervalNotificationTrigger: UNNotificationTrigger, @unchecked Sendable {
    public init(timeInterval: TimeInterval, repeats: Bool) {}
}

public final class UNNotificationRequest: NSObject, @unchecked Sendable {
    public var identifier: String { "" }
    public var content: UNNotificationContent { UNNotificationContent() }
    public init(identifier: String, content: UNNotificationContent, trigger: UNNotificationTrigger?) {}
    override init() {}
}

public final class UNNotification: NSObject, @unchecked Sendable {
    public var request: UNNotificationRequest { UNNotificationRequest() }
}

public class UNNotificationAction: NSObject, @unchecked Sendable {
    public init(identifier: String, title: String, options: UNNotificationActionOptions = []) {}
}

public final class UNTextInputNotificationAction: UNNotificationAction, @unchecked Sendable {
    public init(identifier: String, title: String, options: UNNotificationActionOptions = [], textInputButtonTitle: String,
                textInputPlaceholder: String) { super.init(identifier: identifier, title: title, options: options) }
}

public final class UNNotificationCategory: NSObject, @unchecked Sendable {
    public init(identifier: String, actions: [UNNotificationAction], intentIdentifiers: [String]) {}
}

public final class UNUserNotificationCenter: @unchecked Sendable {
    public static func current() -> UNUserNotificationCenter { UNUserNotificationCenter() }
    public func setNotificationCategories(_ categories: Set<UNNotificationCategory>) {}
    public func requestAuthorization(options: UNAuthorizationOptions = []) async throws -> Bool { true }
    public func getDeliveredNotifications(completionHandler: @escaping @Sendable ([UNNotification]) -> Void) {}
    public func removeDeliveredNotifications(withIdentifiers identifiers: [String]) {}
    public func add(_ request: UNNotificationRequest, withCompletionHandler completionHandler: (@Sendable (Error?) -> Void)? = nil) {}
    public func setBadgeCount(_ newBadgeCount: Int, withCompletionHandler completionHandler: (@Sendable (Error?) -> Void)? = nil) {}
}

public enum UIBackgroundFetchResult: UInt, Sendable { case newData, noData, failed }

public final class UIDevice: @unchecked Sendable {
    public static let current = UIDevice()
    public var model: String { "iPhone" }
}

extension UIApplication {
    public func registerForRemoteNotifications() {}
    nonisolated public static let willResignActiveNotification = Notification.Name("UIApplicationWillResignActiveNotification")
    nonisolated public static let didBecomeActiveNotification = Notification.Name("UIApplicationDidBecomeActiveNotification")
    nonisolated public static let didEnterBackgroundNotification = Notification.Name("UIApplicationDidEnterBackgroundNotification")
    public enum State: Int, Sendable { case active, inactive, background }
    public var applicationState: State { .active }
    public var isProtectedDataAvailable: Bool { true }
    // Debug's notification demo (D241) finishes in the background
    public func beginBackgroundTask(withName taskName: String?,
                                    expirationHandler handler: (@MainActor @Sendable () -> Void)? = nil) -> UIBackgroundTaskIdentifier {
        .invalid
    }
    public func endBackgroundTask(_ identifier: UIBackgroundTaskIdentifier) {}
}
public struct UIBackgroundTaskIdentifier: RawRepresentable, Hashable, Sendable {
    public var rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let invalid = UIBackgroundTaskIdentifier(rawValue: 0)
}
