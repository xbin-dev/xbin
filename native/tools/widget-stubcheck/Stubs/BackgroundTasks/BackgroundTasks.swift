@_exported import Foundation

// What the app's background refresh uses of BackgroundTasks (D241).

public class BGTask: NSObject, @unchecked Sendable {
    public func setTaskCompleted(success: Bool) {}
}

public class BGTaskRequest: NSObject, @unchecked Sendable {
    public var earliestBeginDate: Date?
}

public final class BGAppRefreshTaskRequest: BGTaskRequest, @unchecked Sendable {
    public init(identifier: String) {}
}

public final class BGTaskScheduler: @unchecked Sendable {
    public static let shared = BGTaskScheduler()
    @discardableResult
    public func register(forTaskWithIdentifier identifier: String, using queue: DispatchQueue?,
                         launchHandler: @escaping @Sendable (BGTask) -> Void) -> Bool { true }
    public func submit(_ taskRequest: BGTaskRequest) throws {}
}
