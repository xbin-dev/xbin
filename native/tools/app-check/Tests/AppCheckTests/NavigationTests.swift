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
        #expect(a.windows.isEmpty && a.surface == .terminal(cwd: "apps/x", session: nil))
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
        let t = WindowTarget(workspace: "w1", screen: "s1", surface: .tile("apps/x", sub: "a/", fragment: "f"))
        let back = try #require(WindowTarget(encoded: t.encoded))
        #expect(back == t)
        #expect(t.restoring(afterUncleanExit: false) == t)
        #expect(t.restoring(afterUncleanExit: true) == WindowTarget(workspace: "w1", screen: "s1"))
        // What builds before D125 stored (no screen) still restores.
        let old = try #require(WindowTarget(encoded: #"{"workspace":"w1","surface":{"agent":{"cwd":"apps/x"}}}"#))
        #expect(old == WindowTarget(workspace: "w1", surface: .agent(cwd: "apps/x", session: nil)))
        let build = try #require(WindowTarget(encoded: WindowTarget(workspace: "w1", surface: .build(tile: "apps/n")).encoded))
        #expect(build.surface == .build(tile: "apps/n") && Surface.build(tile: "apps/new-thing").title == "New thing")
        #expect(WindowTarget(encoded: "") == nil && WindowTarget(encoded: "junk") == nil)
        #expect(Surface.agent(cwd: "apps/my-tile", session: nil).title.hasPrefix("Agent · "))
    }

    /// A tile (or a canvas island in a native one) pushes `xbin.window`:
    /// the tile's screen disappears but is only covered — it must keep its
    /// page/runtime and come back on the pop. A window with another pushed
    /// over it is covered too, not closed; a popped one, and a root the
    /// window replaced, are gone.
    @Test func aCoveredScreenIsNotAClosedOne() {
        let nav = WorkspaceNav(workspaceID: "w1")
        nav.open(.tile("apps/x"))
        #expect(!nav.stillStacked(window: nil)) // nothing over it: disappearing means gone
        let w1 = PushedWindow(fromTile: "apps/x", target: "apps/x/a", title: "A", replyID: "r1")
        let w2 = PushedWindow(fromTile: "apps/x", target: "apps/x/b", title: "B", replyID: "r2")
        nav.push(w1)
        #expect(nav.stillStacked(window: nil))   // the tile: covered by r1
        nav.push(w2)
        #expect(nav.stillStacked(window: "r1"))  // r1: covered by r2, not closed
        nav.windows.removeLast()                 // the back button pops r2
        #expect(!nav.stillStacked(window: "r2")) // gone: its opener hears `closed`
        #expect(nav.stillStacked(window: nil) && nav.stillStacked(window: "r1"))
        nav.close(replyID: "r1")                 // the tile closes its window
        #expect(!nav.stillStacked(window: "r1") && !nav.stillStacked(window: nil))
        nav.push(w1)
        nav.open(.tile("apps/y"))                // another surface: everything goes
        #expect(!nav.stillStacked(window: nil) && !nav.stillStacked(window: "r1"))
    }

    /// Home → Screen → tile, and back: the panel left is the forward
    /// memory — one right-edge swipe returns to it (the same panel, its
    /// view kept) — until any new navigation forgets it.
    @Test func levelsBackAndForwardMemory() {
        let nav = WorkspaceNav(workspaceID: "w1")
        #expect(nav.level == 0 && nav.current == .home && !nav.canGoBack && !nav.canGoForward && nav.screenID == nil)
        nav.openScreen("s1")
        #expect(nav.level == 1 && nav.screenID == "s1" && nav.below == .home)
        nav.open(.tile("apps/x"))                                // from the screen's card
        #expect(nav.level == 2 && nav.surface == .tile("apps/x") && nav.screenID == "s1" && nav.below == .screen("s1"))
        let tileEntry = nav.entries[2].id
        nav.push(PushedWindow(fromTile: "apps/x", target: "apps/x/a", title: "A", replyID: "r1"))

        #expect(nav.back())                                      // the left-edge swipe
        #expect(nav.level == 1 && nav.surface == nil && nav.canGoForward && nav.ahead == .surface(.tile("apps/x")))
        #expect(nav.windows.isEmpty)                             // (the inner stack pops first; nothing is left over)
        #expect(nav.back() && nav.level == 0 && nav.ahead == .screen("s1"))
        #expect(!nav.back())
        #expect(nav.forward() && nav.forward() && !nav.forward()) // right-edge swipes, twice: back on the tile
        #expect(nav.surface == .tile("apps/x") && nav.entries[2].id == tileEntry)

        // Reopening the tile a back left is the forward swipe (same view)…
        nav.back()
        nav.open(.tile("apps/x"))
        #expect(nav.surface == .tile("apps/x") && nav.entries[2].id == tileEntry && !nav.canGoForward)
        // …any other navigation forgets the forward memory.
        nav.back()
        nav.open(.tile("apps/y"))
        #expect(nav.surface == .tile("apps/y") && nav.entries[2].id != tileEntry && nav.entries.count == 3)
        nav.back()
        nav.back()
        nav.openScreen("s2")
        #expect(!nav.canGoForward && nav.entries.map(\.panel) == [.home, .screen("s2")])
        nav.back()
        nav.goHome()
        #expect(nav.level == 0 && !nav.canGoForward && nav.entries.count == 1)
    }

    /// A tile opened from search, recents or a link goes back to the screen
    /// it sits on — the one shown when it's there — or Home; a terminal or
    /// agent stays over the screen shown.
    @Test func whereBackGoesFromATile() {
        let screens = ["s1": ["apps/a"], "s2": ["apps/a", "apps/b"]]
        let containing = { (path: String, current: String?) -> String? in
            if let current, screens[current]?.contains(path) == true { return current }
            return ["s1", "s2"].first { screens[$0]!.contains(path) }
        }
        #expect(WorkspaceNav.screen(for: .tile("apps/a"), current: "s2", containing: containing) == "s2")
        #expect(WorkspaceNav.screen(for: .tile("apps/b"), current: "s1", containing: containing) == "s2")
        #expect(WorkspaceNav.screen(for: .tile("apps/z"), current: "s1", containing: containing) == nil)
        #expect(WorkspaceNav.screen(for: .build(tile: "apps/b"), current: nil, containing: containing) == "s2")
        #expect(WorkspaceNav.screen(for: .terminal(cwd: "apps/z", session: nil), current: "s1", containing: containing) == "s1")
        #expect(WorkspaceNav.screen(for: .agent(cwd: nil, session: "x"), current: nil, containing: containing) == nil)
        let nav = WorkspaceNav(workspaceID: "w1")
        nav.open(.tile("apps/z"), on: nil)                      // from Home's search: back is Home
        #expect(nav.entries.map(\.panel) == [.home, .surface(.tile("apps/z"))] && nav.below == .home)
        nav.restore(screen: "s1", surface: .agent(cwd: "apps/a", session: "x"))
        #expect(nav.entries.map(\.panel) == [.home, .screen("s1"), .surface(.agent(cwd: "apps/a", session: "x"))])
    }

    /// The build chooser hands over to the agent it started: the same
    /// panel, now the session; a launcher that started does the same.
    @Test func surfacesChangedInPlace() {
        let nav = WorkspaceNav(workspaceID: "w1")
        nav.open(.build(tile: "apps/n"), on: "s1")
        let id = nav.entries.last!.id
        nav.replace(with: .agent(cwd: "apps/n", session: "a1"))
        #expect(nav.surface == .agent(cwd: "apps/n", session: "a1") && nav.entries.last!.id == id && nav.screenID == "s1")
        nav.open(.agent(cwd: "apps/n", session: nil))
        let launcher = nav.entries.last!.id
        #expect(nav.started(session: "a2", cwd: "apps/n") && nav.entries.last!.id == launcher)
        nav.back()
        nav.replace(with: .tile("apps/q"))                      // nothing full screen: an ordinary open
        #expect(nav.surface == .tile("apps/q") && nav.screenID == "s1")
    }
}
