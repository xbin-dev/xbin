@_exported import UIKit
@MainActor public final class WKWebsiteDataStore {
    public init(forIdentifier: UUID) {}
    public static func nonPersistent() -> WKWebsiteDataStore { WKWebsiteDataStore(forIdentifier: UUID()) }
    public static func remove(forIdentifier: UUID) async throws {}
}
@MainActor public final class WKWebView {}
