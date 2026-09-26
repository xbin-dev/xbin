import Foundation
import XbinCore

/// The app's remote configuration (plans/native.md §23): the kill switch
/// for native tile runtimes, `https://xbin.dev/app/ios.json` (Info.plist
/// `XbinAppConfigURL` overrides it). Read on becoming active at most every
/// 6 hours, cached in UserDefaults, and **fail open** — XbinCore's
/// KillSwitch.swift has the rules and their tests.
enum RemoteConfig {
    private static let cacheKey = "remoteAppConfig"

    /// The configuration file's URL.
    static var url: URL? {
        let s = (Bundle.main.object(forInfoDictionaryKey: "XbinAppConfigURL") as? String ?? "")
            .trimmingCharacters(in: .whitespaces)
        return URL(string: s.isEmpty ? RemoteAppConfig.defaultURL : s)
    }

    static var cache: RemoteAppConfigCache? {
        get { RemoteAppConfigCache.decode(UserDefaults.standard.data(forKey: cacheKey)) }
        set {
            if let v = newValue { UserDefaults.standard.set(v.jsonData, forKey: cacheKey) }
            else { UserDefaults.standard.removeObject(forKey: cacheKey) }
        }
    }

    /// Why native tile UIs are off app-wide right now (nil: on).
    static func gate(now: Date = Date()) -> NativeRuntimeGate? {
        NativeRuntimeGate.app(userOff: AppSettings.nativeViewsOff, remote: cache, build: AppInfo.build, now: now)
    }

    /// Fetches the file when due. True when the cache changed.
    @discardableResult
    static func refreshIfDue(now: Date = Date()) async -> Bool {
        let before = cache
        guard RemoteAppConfigCache.due(before, at: now), let url else { return false }
        var status: Int?
        var body = Data()
        do {
            var req = URLRequest(url: url)
            req.cachePolicy = .reloadIgnoringLocalCacheData
            req.timeoutInterval = 15
            req.setValue(AppInfo.clientHeader, forHTTPHeaderField: "X-XBin-Client")
            // No workspace credential ever goes here: the app's plain
            // ephemeral session, no cookies.
            let (d, r) = try await AppTransport.shared.session.data(for: req)
            status = (r as? HTTPURLResponse)?.statusCode
            body = d
        } catch {
            status = nil
        }
        let after = RemoteAppConfigCache.after(fetch: status, body: body, previous: before, at: now)
        guard after != before else { return false }
        cache = after
        return true
    }
}
