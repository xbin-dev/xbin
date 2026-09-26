import Foundation
#if canImport(UIKit)
import UIKit
#endif
import XbinCore

/// Build facts and app-wide settings.
enum AppInfo {
    static var version: String {
        let v = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0"
        let b = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "0"
        return "\(v) (\(b))"
    }

    static var shortVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0"
    }

    /// `CFBundleVersion` — what the remote kill switch's `disabledBuilds` lists.
    static var build: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "0"
    }

    /// `X-XBin-Client` on every request the app makes (xbind injects the
    /// `xbin-ws-origin` meta into tile HTML when it sees it).
    static var clientHeader: String { TileScheme.clientValue(version: shortVersion) }

    static var bundleID: String { Bundle.main.bundleIdentifier ?? "dev.xbin.app" }

    /// The Keychain access group the app shares with its Notification
    /// Service Extension (push keys). Set in Info.plist from the build
    /// settings (`$(AppIdentifierPrefix)dev.xbin.shared`); nil on builds
    /// without signing (CI), where the default group is used.
    static var sharedKeychainGroup: String? {
        guard let g = Bundle.main.object(forInfoDictionaryKey: "XbinKeychainGroup") as? String,
              !g.isEmpty, !g.hasPrefix("$(") else { return nil }
        return g
    }

    /// `platform` for `POST /api/xbin/devices/enroll`.
    @MainActor static var platform: String {
        if ProcessInfo.processInfo.isiOSAppOnMac { return "macos" }
        return isPad ? "ipados" : "ios"
    }

    @MainActor static var isPad: Bool { UIDevice.current.userInterfaceIdiom == .pad }

    /// The device's name for the Devices list (users rename it there).
    @MainActor static var deviceName: String { UIDevice.current.name }

    /// The push relay: configuration only — nothing is deployed and the
    /// operator is undecided (plans/native.md decision 13). Info.plist
    /// `XbinPushRelay`, overridable in Settings; empty = push off.
    static var defaultPushRelay: String {
        (Bundle.main.object(forInfoDictionaryKey: "XbinPushRelay") as? String ?? "")
            .trimmingCharacters(in: .whitespaces)
    }

    /// `aps-environment` of this build (the relay picks the APNs host).
    static var apnsProduction: Bool {
        #if DEBUG
        return false
        #else
        return true
        #endif
    }
}

/// Small user settings (UserDefaults — nothing secret here).
enum AppSettings {
    private static var d: UserDefaults { .standard }

    /// "Require Face ID when opening the app" (plans/native.md §5), off by default.
    static var appLock: Bool {
        get { d.bool(forKey: "appLock") }
        set { d.set(newValue, forKey: "appLock") }
    }

    /// Predictive echo: auto (default), on, off.
    static var predictMode: String {
        get { d.string(forKey: "predictMode") ?? "auto" }
        set { d.set(newValue, forKey: "predictMode") }
    }

    /// The relay URL override ("" = the build's default).
    static var pushRelay: String {
        get { d.string(forKey: "pushRelay") ?? "" }
        set { d.set(newValue, forKey: "pushRelay") }
    }

    static var effectivePushRelay: String {
        let o = pushRelay.trimmingCharacters(in: .whitespaces)
        return o.isEmpty ? AppInfo.defaultPushRelay : o
    }

    /// Terminal font size (points).
    static var terminalFontSize: Double {
        get { let v = d.double(forKey: "termFont"); return v > 0 ? v : 13 }
        set { d.set(newValue, forKey: "termFont") }
    }

    /// Tiles the user opens as web pages even though they have a native UI
    /// ("workspace-id|tile" keys).
    static func forcesWeb(_ ws: String, _ tile: String) -> Bool {
        (d.stringArray(forKey: "forceWeb") ?? []).contains("\(ws)|\(tile)")
    }

    static func setForcesWeb(_ ws: String, _ tile: String, _ on: Bool) {
        var s = Set(d.stringArray(forKey: "forceWeb") ?? [])
        if on { s.insert("\(ws)|\(tile)") } else { s.remove("\(ws)|\(tile)") }
        d.set(Array(s), forKey: "forceWeb")
    }

    /// "Native views" off in Settings: every tile opens as its web page.
    /// (The key predates the remote switch, which now lives in RemoteConfig.)
    static var nativeViewsOff: Bool {
        get { d.bool(forKey: "nativeRuntimeOff") }
        set { d.set(newValue, forKey: "nativeRuntimeOff") }
    }

    /// Native tile runtimes are off app-wide: the user's switch or the
    /// remote kill switch (plans/native.md §23; RemoteConfig). The
    /// workspace's own switch is `whoami.native.runtime`.
    static var nativeRuntimeOff: Bool { RemoteConfig.gate() != nil }

    /// Haptic taps when a turn settles, a permission is approved, a prompt
    /// is sent (on by default; the system's own switch still applies).
    static var haptics: Bool {
        get { d.object(forKey: "haptics") as? Bool ?? true }
        set { d.set(newValue, forKey: "haptics") }
    }

    /// Advertise the current tile or session for Handoff ("open on
    /// desktop"): on by default.
    static var handoff: Bool {
        get { d.object(forKey: "handoff") as? Bool ?? true }
        set { d.set(newValue, forKey: "handoff") }
    }
}
