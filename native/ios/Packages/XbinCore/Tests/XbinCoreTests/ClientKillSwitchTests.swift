import Foundation
import Testing
@testable import XbinCore

@Suite struct ClientKillSwitchTests {
    let now = Date(timeIntervalSince1970: 1_790_000_000)

    func parse(_ s: String) -> RemoteAppConfig? { (try? JSONValue(parsing: s)).flatMap(RemoteAppConfig.init(json:)) }

    /// The file website/app/ios.json publishes, and its variants.
    @Test func readsTheConfigurationFile() throws {
        let shipped = try String(contentsOf: repoFile("website/app/ios.json"), encoding: .utf8)
        #expect(parse(shipped) == RemoteAppConfig(disabled: false, disabledBuilds: []))
        #expect(parse(#"{"nativeRuntime":{"disabled":true}}"#) == RemoteAppConfig(disabled: true))
        #expect(parse(#"{"nativeRuntime":{"disabledBuilds":[41, "42", " 43 ", "", null, 1.5]}}"#)
            == RemoteAppConfig(disabledBuilds: ["41", "42", "43"])) // 1.5 is no build number
        #expect(parse(#"{"somethingElse":1}"#) == RemoteAppConfig())
        #expect(parse(#"{"nativeRuntime":{"disabled":"yes"}}"#) == RemoteAppConfig()) // not a boolean: not disabled
        #expect(parse("[]") == nil)
        #expect(parse("true") == nil)
    }

    @Test func disablesListedBuilds() {
        let c = RemoteAppConfig(disabledBuilds: ["41", "42"])
        #expect(c.disables(build: "42") && c.disables(build: " 41 "))
        #expect(!c.disables(build: "43") && !c.disables(build: "") && !c.disables(build: "4"))
        #expect(RemoteAppConfig(disabled: true).disables(build: "1"))
    }

    /// Fail open: only a readable answer changes the cache; a 404 means
    /// nothing is published, so nothing is off.
    @Test func cacheUpdatesFailOpen() {
        let off = RemoteAppConfigCache(config: RemoteAppConfig(disabled: true), fetchedAt: now.addingTimeInterval(-100))
        let later = now
        let body = Data(#"{"nativeRuntime":{"disabled":false,"disabledBuilds":["7"]}}"#.utf8)
        #expect(RemoteAppConfigCache.after(fetch: 200, body: body, previous: off, at: later)
            == RemoteAppConfigCache(config: RemoteAppConfig(disabledBuilds: ["7"]), fetchedAt: later))
        #expect(RemoteAppConfigCache.after(fetch: 404, body: Data(), previous: off, at: later)
            == RemoteAppConfigCache(config: RemoteAppConfig(), fetchedAt: later))
        #expect(RemoteAppConfigCache.after(fetch: 410, body: Data(), previous: nil, at: later)?.config == RemoteAppConfig())
        // Errors keep what was there (never turn anything off by themselves).
        #expect(RemoteAppConfigCache.after(fetch: nil, body: Data(), previous: off, at: later) == off)
        #expect(RemoteAppConfigCache.after(fetch: 503, body: body, previous: off, at: later) == off)
        #expect(RemoteAppConfigCache.after(fetch: 200, body: Data("<html>captive portal</html>".utf8), previous: nil, at: later) == nil)
        #expect(RemoteAppConfigCache.after(fetch: 200, body: Data("[]".utf8), previous: nil, at: later) == nil)
        #expect(RemoteAppConfigCache.after(fetch: nil, body: Data(), previous: nil, at: later) == nil)
    }

    @Test func refetchesEverySixHours() {
        #expect(RemoteAppConfigCache.due(nil, at: now))
        let c = RemoteAppConfigCache(config: RemoteAppConfig(), fetchedAt: now)
        #expect(!RemoteAppConfigCache.due(c, at: now.addingTimeInterval(3600)))
        #expect(RemoteAppConfigCache.due(c, at: now.addingTimeInterval(6 * 3600)))
        #expect(RemoteAppConfigCache.due(c, at: now.addingTimeInterval(-60))) // the clock went back
        // After a crash in the foreground: now, however fresh (the file may
        // have turned this build's native views off an hour ago).
        #expect(RemoteAppConfigCache.due(c, at: now.addingTimeInterval(60), afterUncleanExit: true))
        #expect(!RemoteAppConfigCache.due(c, at: now.addingTimeInterval(60), afterUncleanExit: false))
    }

    @Test func cacheRoundTrips() {
        let c = RemoteAppConfigCache(config: RemoteAppConfig(disabled: true, disabledBuilds: ["3"]), fetchedAt: now)
        #expect(RemoteAppConfigCache.decode(c.jsonData) == c)
        #expect(RemoteAppConfigCache.decode(Data("junk".utf8)) == nil)
        #expect(RemoteAppConfigCache.decode(nil) == nil)
    }

    @Test func appGate() {
        let off = RemoteAppConfigCache(config: RemoteAppConfig(disabled: true), fetchedAt: now)
        let build = RemoteAppConfigCache(config: RemoteAppConfig(disabledBuilds: ["12"]), fetchedAt: now)
        #expect(NativeRuntimeGate.app(userOff: false, remote: nil, build: "12", now: now) == nil)
        #expect(NativeRuntimeGate.app(userOff: true, remote: nil, build: "12", now: now) == .user)
        #expect(NativeRuntimeGate.app(userOff: true, remote: off, build: "12", now: now) == .user)
        #expect(NativeRuntimeGate.app(userOff: false, remote: off, build: "12", now: now) == .remote)
        #expect(NativeRuntimeGate.app(userOff: false, remote: build, build: "12", now: now) == .remoteBuild)
        #expect(NativeRuntimeGate.app(userOff: false, remote: build, build: "13", now: now) == nil)
        // A week-old "off" no longer counts (the fetch has been failing).
        let week = now.addingTimeInterval(RemoteAppConfigCache.maxAge + 1)
        #expect(NativeRuntimeGate.app(userOff: false, remote: off, build: "12", now: week) == nil)
        #expect(NativeRuntimeGate.app(userOff: false, remote: off, build: "12", now: now.addingTimeInterval(86400)) == .remote)
    }

    /// `whoami.native.runtime` 0 = the workspace's admin turned native
    /// runtimes off; the navigator then opens every tile as a web page.
    @Test func workspaceGate() throws {
        #expect(NativeRuntimeGate.workspace(nativeRuntime: nil, loaded: false) == nil)
        #expect(NativeRuntimeGate.workspace(nativeRuntime: nil, loaded: true) == .unsupported)
        #expect(NativeRuntimeGate.workspace(nativeRuntime: 0, loaded: true) == .workspace)
        #expect(NativeRuntimeGate.workspace(nativeRuntime: 1, loaded: true) == nil)
        let tile = try #require(TileInfo(json: ["path": "apps/n", "native": ["entry": "native.js"]]))
        #expect(tile.opensNatively)
        #expect(TileSurface.pick(tile, serverRuntime: 1) == .native)
        #expect(TileSurface.pick(tile, serverRuntime: 0) == .web)
        #expect(TileSurface.pick(tile, serverRuntime: 1, runtimeOff: true) == .web)
        for g in [NativeRuntimeGate.user, .remote, .remoteBuild, .workspace, .unsupported] { #expect(!g.explanation.isEmpty) }
    }
}

/// A launch after a run that ended in the foreground (a crash — say a
/// native runtime crashing as it mounts on restore) knows it, so its
/// windows don't restore straight into the tile before the remote switch
/// is read; a run that went to the background ended cleanly.
@Suite struct ForegroundMarkTests {
    final class Flags: FlagStore {
        var values: [String: Bool] = [:]
        func bool(forKey key: String) -> Bool { values[key] ?? false }
        func set(_ value: Bool, forKey key: String) { values[key] = value }
    }

    @Test func aRunThatEndsInTheForegroundIsUnclean() {
        let flags = Flags()
        #expect(!ForegroundMark(flags).lastRunEndedInForeground)  // a first launch
        // Run 1: active, then it crashes in the foreground.
        ForegroundMark(flags).enteredForeground()
        // Run 2 reads it before marking itself.
        let run2 = ForegroundMark(flags)
        #expect(run2.lastRunEndedInForeground)
        run2.enteredForeground()
        run2.enteredBackground()                                   // then iOS ends it in the background
        #expect(!ForegroundMark(flags).lastRunEndedInForeground)
        // Active again after the background: unclean until it leaves.
        run2.enteredForeground()
        #expect(ForegroundMark(flags).lastRunEndedInForeground)
        #expect(flags.values.keys.sorted() == [ForegroundMark.key])
    }

    /// The app's store is UserDefaults.
    @Test func userDefaultsIsAFlagStore() throws {
        let name = "xbin-test-\(UUID().uuidString)"
        let d = try #require(UserDefaults(suiteName: name))
        defer { d.removePersistentDomain(forName: name) }
        let mark = ForegroundMark(d)
        mark.enteredForeground()
        #expect(ForegroundMark(d).lastRunEndedInForeground)
        mark.enteredBackground()
        #expect(!ForegroundMark(d).lastRunEndedInForeground)
    }
}

/// A file of the repository, from this test file's location.
func repoFile(_ rel: String, file: String = #filePath) -> URL {
    var u = URL(fileURLWithPath: file)
    for _ in 0..<7 { u.deleteLastPathComponent() } // <repo>/native/ios/Packages/XbinCore/Tests/XbinCoreTests/x.swift
    return u.appendingPathComponent(rel)
}
