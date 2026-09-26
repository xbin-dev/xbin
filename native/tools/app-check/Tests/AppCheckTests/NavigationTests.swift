import Foundation
import Testing
import XbinCore

// A window's navigation (native/ios/App/Model/Navigation.swift, symlinked
// here). Every window has its own WorkspaceNav per workspace, and a screen
// acts on its own window's: a tile page in a background window that calls
// `xbin.window` pushes onto that window, never over the tile in front, and
// an agent session that finishes starting only moves the window it started
// in — and only if that window still shows its launcher.

@Suite @MainActor struct NavigationTests {
    /// Two windows on one workspace (Stage Manager): window A, in front,
    /// shows tile X; window B, behind, tile Y. Y's `xbin.window` (after a
    /// fetch, no gesture) and its close land in B only.
    @Test func aTilesWindowPushesOntoItsOwnWindow() {
        let a = WorkspaceNav(workspaceID: "w1")
        let b = WorkspaceNav(workspaceID: "w1")
        a.open(.tile("apps/x"))
        b.open(.tile("apps/y"))
        let pushed = PushedWindow(fromTile: "apps/y", target: "apps/y/detail", title: "Detail", replyID: "r1")
        b.push(pushed)                     // WebTileController.nav is B's
        #expect(b.windows == [pushed])
        #expect(a.windows.isEmpty && a.surface == .tile("apps/x"))
        // A's own window closes by its id without touching B's.
        a.push(PushedWindow(fromTile: "apps/x", target: "apps/x/a", title: "A", replyID: "r2"))
        a.close(replyID: "r1")
        #expect(b.windows == [pushed] && a.windows.map(\.replyID) == ["r2"])
        b.close(replyID: "r1")
        #expect(b.windows.isEmpty && a.windows.map(\.replyID) == ["r2"])
        // Opening another surface drops that window's pushed screens only.
        a.open(.terminal(cwd: "apps/x", session: nil))
        #expect(a.windows.isEmpty && !a.showNavigator)
    }

    /// The agent launcher creates its session asynchronously: the window it
    /// started in shows the new session — unless the user moved on there.
    @Test func aStartedSessionMovesOnlyItsLauncher() {
        let launcher = WorkspaceNav(workspaceID: "w1")
        let other = WorkspaceNav(workspaceID: "w1")
        launcher.open(.agent(cwd: "apps/x", session: nil))
        other.open(.agent(cwd: "apps/x", session: nil))  // same launcher in another window: untouched
        #expect(launcher.started(session: "s1", cwd: "apps/x"))
        #expect(launcher.surface == .agent(cwd: "apps/x", session: "s1"))
        #expect(other.surface == .agent(cwd: "apps/x", session: nil))
        // The user opened a tile in that window while the session started.
        let moved = WorkspaceNav(workspaceID: "w1")
        moved.open(.agent(cwd: "apps/x", session: nil))
        moved.open(.tile("apps/y"))
        #expect(!moved.started(session: "s2", cwd: "apps/x"))
        #expect(moved.surface == .tile("apps/y"))
        // …or a launcher on another tile, or one already on a session.
        let elsewhere = WorkspaceNav(workspaceID: "w1")
        elsewhere.open(.agent(cwd: "apps/z", session: nil))
        #expect(!elsewhere.started(session: "s3", cwd: "apps/x"))
        #expect(!launcher.started(session: "s4", cwd: "apps/x"))
        #expect(launcher.surface == .agent(cwd: "apps/x", session: "s1"))
    }

    /// `@SceneStorage` keeps a window's place as a string; after a run that
    /// ended in the foreground (a crash, maybe a native tile mounting on
    /// restore) the window comes back on its workspace only.
    @Test func windowTargetsRestoreCautiouslyAfterACrash() throws {
        let t = WindowTarget(workspace: "w1", surface: .tile("apps/x", sub: "a/", fragment: "f"))
        let back = try #require(WindowTarget(encoded: t.encoded))
        #expect(back == t)
        #expect(t.restoring(afterUncleanExit: false) == t)
        #expect(t.restoring(afterUncleanExit: true) == WindowTarget(workspace: "w1"))
        #expect(WindowTarget(encoded: "") == nil && WindowTarget(encoded: "junk") == nil)
        #expect(Surface.agent(cwd: "apps/my-tile", session: nil).title.hasPrefix("Agent · "))
    }
}
