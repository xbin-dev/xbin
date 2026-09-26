@_exported import Foundation

// ActivityKit as the xbin app uses it (iOS 26/27 SDK, from its documented
// API). Deliberately strict where the SDK is unclear: Activity is NOT
// Sendable, and its async sequences don't implement next(isolation:), so
// code that type-checks here holds whichever way the SDK went.

public protocol ActivityAttributes: Decodable, Encodable {
    associatedtype ContentState: Decodable, Encodable, Hashable
}

public struct ActivityContent<State: Decodable & Encodable & Hashable>: Equatable, Hashable {
    public var state: State
    public var staleDate: Date?
    public var relevanceScore: Double
    public init(state: State, staleDate: Date?, relevanceScore: Double = 0) {
        self.state = state
        self.staleDate = staleDate
        self.relevanceScore = relevanceScore
    }
}
extension ActivityContent: Sendable where State: Sendable {}

public enum PushType: Equatable, Sendable {
    case token
    case channel(String)
}

public enum ActivityState: Equatable, Hashable, Sendable {
    case active, ended, dismissed, stale, pending
}

public struct ActivityUIDismissalPolicy: Sendable {
    public static let `default` = ActivityUIDismissalPolicy()
    public static let immediate = ActivityUIDismissalPolicy()
    public static func after(_ date: Date) -> ActivityUIDismissalPolicy { ActivityUIDismissalPolicy() }
}

public struct ActivityAuthorizationInfo {
    public init() {}
    public var areActivitiesEnabled: Bool { true }
    public var frequentPushesEnabled: Bool { false }
}

public enum ActivityAuthorizationError: Error {
    case attributesTooLarge, denied, globalMaximumExceeded, malformedActivityIdentifier, missingProcessIdentifier,
         persistenceFailure, reconnectNotPermitted, targetMaximumExceeded, unentitled, unsupported, unsupportedTarget, visibility
}

public struct _ActivityAsyncSequence<Element>: AsyncSequence {
    public struct AsyncIterator: AsyncIteratorProtocol {
        public mutating func next() async -> Element? { nil }
    }
    public func makeAsyncIterator() -> AsyncIterator { AsyncIterator() }
}

public final class Activity<Attributes: ActivityAttributes>: Identifiable {
    public let id: String = ""
    public var attributes: Attributes { fatalError() }
    public var content: ActivityContent<Attributes.ContentState> { fatalError() }
    public var activityState: ActivityState { .active }
    public var pushToken: Data? { nil }
    public var pushTokenUpdates: _ActivityAsyncSequence<Data> { .init() }
    public var activityStateUpdates: _ActivityAsyncSequence<ActivityState> { .init() }

    public static var activities: [Activity<Attributes>] { [] }
    public static var activityUpdates: _ActivityAsyncSequence<Activity<Attributes>> { .init() }
    public static var pushToStartToken: Data? { nil }
    public static var pushToStartTokenUpdates: _ActivityAsyncSequence<Data> { .init() }

    public static func request(attributes: Attributes, content: ActivityContent<Attributes.ContentState>,
                               pushType: PushType? = nil) throws -> Activity<Attributes> { fatalError() }
    public func update(_ content: ActivityContent<Attributes.ContentState>) async {}
    public func end(_ content: ActivityContent<Attributes.ContentState>?, dismissalPolicy: ActivityUIDismissalPolicy = .default) async {}
}
