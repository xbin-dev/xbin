import Foundation

// The native runtime's kill switches (plans/native.md §23): native tile UIs
// can be turned off without an app update, falling back to each tile's web
// page (which always works). Four independent switches, any one turns them
// off:
//
// 1. the app's remote configuration, `https://xbin.dev/app/ios.json`:
//    `{"nativeRuntime": {"disabled": bool, "disabledBuilds": [build, …]}}` —
//    every build, or the listed builds (`CFBundleVersion`);
// 2. the workspace: `whoami.native.runtime` is 0 when its admin turned native
//    runtimes off (absent on an xbind that predates them: web only);
// 3. the user, in Settings;
// 4. (per tile) "open as web page" and a failed mount (TileSurface.pick).
//
// The remote file is cached and **fails open**: a network error, a bad
// answer or a cache older than a week never turns anything off; only a
// successfully read `disabled` does, and only for as long as it is fresh.

/// The app configuration file's `nativeRuntime` part.
public struct RemoteAppConfig: Sendable, Equatable, Codable {
    /// Native runtimes off in every build.
    public var disabled: Bool
    /// Native runtimes off in these builds only (`CFBundleVersion`, as
    /// written: numbers and strings both accepted).
    public var disabledBuilds: [String]

    public init(disabled: Bool = false, disabledBuilds: [String] = []) {
        self.disabled = disabled
        self.disabledBuilds = disabledBuilds
    }

    /// Where the file lives (Info.plist `XbinAppConfigURL` overrides it).
    public static let defaultURL = "https://xbin.dev/app/ios.json"

    /// Lenient: unknown keys ignored, a missing `nativeRuntime` is "nothing
    /// disabled"; nil when the body isn't a JSON object (a failed fetch).
    public init?(json: JSONValue) {
        guard json.objectValue != nil else { return nil }
        let n = json["nativeRuntime"]
        disabled = n?["disabled"]?.boolValue ?? false
        disabledBuilds = (n?["disabledBuilds"]?.arrayValue ?? []).compactMap(Self.build)
    }

    static func build(_ v: JSONValue) -> String? {
        if let s = v.stringValue { let t = s.trimmingCharacters(in: .whitespaces); return t.isEmpty ? nil : t }
        if let i = v.intValue { return String(i) }
        return nil
    }

    /// Whether this configuration turns native runtimes off in `build`.
    public func disables(build: String) -> Bool {
        let b = build.trimmingCharacters(in: .whitespaces)
        return disabled || (!b.isEmpty && disabledBuilds.contains(b))
    }
}

/// The last successful read of the configuration.
public struct RemoteAppConfigCache: Sendable, Equatable, Codable {
    public var config: RemoteAppConfig
    public var fetchedAt: Date

    public init(config: RemoteAppConfig, fetchedAt: Date) {
        self.config = config
        self.fetchedAt = fetchedAt
    }

    /// How often the app asks again (on becoming active).
    public static let refreshInterval: TimeInterval = 6 * 3600
    /// How long an answer counts: older than this it is ignored (fail open).
    public static let maxAge: TimeInterval = 7 * 24 * 3600

    public func isFresh(at now: Date) -> Bool { now.timeIntervalSince(fetchedAt) < Self.maxAge }

    /// Whether to fetch again now (nil cache: yes).
    public static func due(_ cache: RemoteAppConfigCache?, at now: Date) -> Bool {
        guard let c = cache else { return true }
        let age = now.timeIntervalSince(c.fetchedAt)
        return age >= refreshInterval || age < 0
    }

    /// What a fetch leaves in the cache: a readable answer replaces it, a
    /// 404/410 (no file published) means nothing is disabled, anything else
    /// (network error, 5xx, junk) keeps what was there.
    public static func after(fetch status: Int?, body: Data, previous: RemoteAppConfigCache?,
                             at now: Date) -> RemoteAppConfigCache? {
        guard let status else { return previous }
        if status == 404 || status == 410 { return RemoteAppConfigCache(config: RemoteAppConfig(), fetchedAt: now) }
        guard (200..<300).contains(status), let j = try? JSONValue(parsing: body),
              let c = RemoteAppConfig(json: j) else { return previous }
        return RemoteAppConfigCache(config: c, fetchedAt: now)
    }

    public var jsonData: Data { (try? JSONEncoder().encode(self)) ?? Data() }

    public static func decode(_ data: Data?) -> RemoteAppConfigCache? {
        guard let data else { return nil }
        return try? JSONDecoder().decode(RemoteAppConfigCache.self, from: data)
    }
}

/// Why native tile UIs are off (nil: they're on).
public enum NativeRuntimeGate: Sendable, Equatable {
    /// The user turned native views off in Settings.
    case user
    /// The app's remote configuration turned them off for every build.
    case remote
    /// …for this build.
    case remoteBuild
    /// The workspace's admin turned them off (`whoami.native.runtime` 0).
    case workspace
    /// The workspace's xbind predates native runtimes.
    case unsupported

    /// The app-wide switches (Settings, the remote file), in that order.
    public static func app(userOff: Bool, remote: RemoteAppConfigCache?, build: String, now: Date) -> NativeRuntimeGate? {
        if userOff { return .user }
        guard let r = remote, r.isFresh(at: now) else { return nil }
        if r.config.disabled { return .remote }
        if r.config.disables(build: build) { return .remoteBuild }
        return nil
    }

    /// The workspace's switch (`whoami.native.runtime`); nil while unknown
    /// (whoami not loaded yet) is `.unsupported` only once loaded.
    public static func workspace(nativeRuntime: Int?, loaded: Bool) -> NativeRuntimeGate? {
        guard loaded else { return nil }
        guard let v = nativeRuntime else { return .unsupported }
        return v >= 1 ? nil : .workspace
    }

    /// What Settings says.
    public var explanation: String {
        switch self {
        case .user: return "Off in Settings: tiles open as web pages."
        case .remote: return "Native views are turned off for this app version while an issue is fixed; tiles open as web pages."
        case .remoteBuild: return "Native views are turned off for this build while an issue is fixed; tiles open as web pages."
        case .workspace: return "This workspace's admin turned native views off; tiles open as web pages."
        case .unsupported: return "This workspace's server predates native views; tiles open as web pages."
        }
    }
}
