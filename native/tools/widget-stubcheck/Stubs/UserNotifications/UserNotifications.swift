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
}

public final class UNNotificationAction: NSObject, @unchecked Sendable {
    public init(identifier: String, title: String, options: UNNotificationActionOptions = []) {}
}

public final class UNNotificationCategory: NSObject, @unchecked Sendable {
    public init(identifier: String, actions: [UNNotificationAction], intentIdentifiers: [String]) {}
}

public final class UNUserNotificationCenter: @unchecked Sendable {
    public static func current() -> UNUserNotificationCenter { UNUserNotificationCenter() }
    public func setNotificationCategories(_ categories: Set<UNNotificationCategory>) {}
    public func requestAuthorization(options: UNAuthorizationOptions = []) async throws -> Bool { true }
}

extension UIApplication {
    public func registerForRemoteNotifications() {}
    nonisolated public static let willResignActiveNotification = Notification.Name("UIApplicationWillResignActiveNotification")
    nonisolated public static let didBecomeActiveNotification = Notification.Name("UIApplicationDidBecomeActiveNotification")
}
