import XCTest

/// Native navigation in the app, end to end against the e2e xbind
/// (native/AGENTS.md → "Mac mini"). A tile's own stack sits in the app's
/// panel, whose NavigationStack is the one on screen: while the tile's stack
/// can pop, its Back goes first and the panel stays (D189).
///
/// - apps/desk (scripts/testdata/e2e-tiles/desk) — vocabulary rev 2: a
///   `split` of tickets opened from a deep link
///   `xbin://<host:port>/c/apps/desk#t=t-2` — on an iPhone the ticket pushed
///   over the list, Back returns to it; on an iPad both columns, the
///   sidebar hidden and shown again (`columns`) — and a second link while
///   the view runs opens another ticket (`xbn.navigate`).
/// - apps/stack (…/stack) — rev 1: a `nav` whose root lists runbooks; a tap
///   pushes one, Back pops it (the tile drops it on `pop`), the panel stays.
final class XbinNavigationTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    func test01DeepLinkIntoACollapsingSplit() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let pad = e.app.windows.firstMatch.frame.width >= 700 // a tablet: two columns
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/desk#t=t-2")!)
        let number = e.app.staticTexts["t-2"]
        XCTAssertTrue(number.waitForExistence(timeout: 60), "the deep-linked ticket's detail (its number)")
        XCTAssertTrue(e.containing("Invoice shows the wrong VAT").exists, "…under its subject")
        e.shot("nav-01-deep-link")

        // a list row is one button whose label combines its texts
        let list = e.containing("SSO login loops")
        if pad {
            XCTAssertTrue(list.waitForExistence(timeout: 10) && list.isHittable, "a tablet shows the list beside the ticket")
            let sidebar = e.app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "ToggleSidebar", "Hide Sidebar")).firstMatch
            if sidebar.waitForExistence(timeout: 5) {
                sidebar.tap()
                XCTAssertTrue(e.until(10) { !list.isHittable }, "the sidebar button hides the list")
                e.shot("nav-02-detail-only")
                let show = e.app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "ToggleSidebar", "Show Sidebar")).firstMatch
                show.tap()
                XCTAssertTrue(e.until(10) { list.isHittable }, "…and shows it again")
            } else {
                print("xbin-nav no sidebar button:\n\(e.app.debugDescription)")
            }
        } else {
            XCTAssertFalse(list.exists && list.isHittable, "a phone shows the ticket over the list")
            e.tapNavBack(listShows: list, "Back shows the list")
            XCTAssertFalse(number.exists, "…and the ticket is gone")
            XCTAssertTrue(e.backButton.exists, "the tile's panel is still in front")
            e.shot("nav-02-list")
        }

        // A link while the view runs (the system opens it in the running
        // app — XCUIApplication.open would relaunch it): xbn.navigate →
        // hashchange.
        e.openInRunningApp("xbin://\(e.server.authority)/c/apps/desk#t=t-3")
        XCTAssertTrue(e.app.staticTexts["t-3"].waitForExistence(timeout: 30), "the second link's ticket")
        e.shot("nav-03-navigate")
    }

    @MainActor
    func test02Rev1NavPushAndBack() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/stack")!)
        let row = e.containing("Restore a backup to staging")
        XCTAssertTrue(row.waitForExistence(timeout: 60), "the stack's root list")
        e.shot("nav-04-stack-root")
        row.tap()
        let id = e.app.staticTexts["rb-2"]
        XCTAssertTrue(id.waitForExistence(timeout: 20), "the pushed runbook")
        e.shot("nav-05-stack-pushed")
        e.tapNavBack(listShows: row, "Back pops the runbook")
        XCTAssertFalse(id.exists, "…and the runbook is gone")
        XCTAssertTrue(e.backButton.exists, "the tile's panel is still in front")
        e.shot("nav-06-stack-popped")
        // and again: a second push and pop
        row.tap()
        XCTAssertTrue(id.waitForExistence(timeout: 20), "pushed again")
        e.tapNavBack(listShows: row, "popped again")
    }
}

extension E2E {
    /// Opens `link` through the system, in the app as it runs (a
    /// notification's tap does the same); confirms the system's "Open in
    /// xbin?" when it asks.
    func openInRunningApp(_ link: String) {
        XCUIDevice.shared.system.open(URL(string: link)!)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let open = springboard.buttons["Open"]
        if open.waitForExistence(timeout: 3) { open.tap() }
    }

    /// Taps the navigation bar's Back (the system's, not the panel's) and
    /// expects `shown` to be hittable after it; dumps the tree when not.
    func tapNavBack(listShows shown: XCUIElement, _ what: String, file: StaticString = #filePath, line: UInt = #line) {
        let back = app.navigationBars.buttons.matching(identifier: "BackButton").firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 10), "the bar's Back", file: file, line: line)
        back.tap()
        if !(shown.waitForExistence(timeout: 10) && until(10) { shown.isHittable }) {
            print("xbin-nav after Back:\n\(app.debugDescription)")
            shot("nav-fail-after-back")
            XCTFail(what, file: file, line: line)
        }
    }
}
