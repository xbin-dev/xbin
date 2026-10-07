import XCTest

/// Vocabulary rev 2's navigation in the app (D189), end to end against the
/// e2e xbind (native/AGENTS.md → "Mac mini"), whose workspace has apps/desk —
/// a static tile whose native.js is a split of tickets
/// (scripts/testdata/e2e-tiles/desk): a deep link `xbin://<host:port>/c/apps/desk#t=t-2`
/// opens the native view at that ticket (on an iPhone pushed over the list,
/// on an iPad beside it); the split's Back returns to the list — the tile's
/// own stack owns Back while it can pop, the panel stays — and a second link
/// while the view runs takes it to another ticket (`xbn.navigate`).
final class XbinNavigationTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    func test01DeepLinkIntoTheNativeView() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let pad = e.app.windows.firstMatch.frame.width >= 700 // a tablet: two columns
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/desk#t=t-2")!)
        let vat = e.app.staticTexts["Invoice shows the wrong VAT"]
        let number = e.app.staticTexts["t-2"]
        XCTAssertTrue(number.waitForExistence(timeout: 60), "the deep-linked ticket's detail (its number)")
        XCTAssertTrue(vat.exists, "…under its subject")
        e.shot("nav-01-deep-link")

        // a list row is one button whose label combines its texts
        let list = e.containing("SSO login loops")
        if pad {
            XCTAssertTrue(list.waitForExistence(timeout: 10), "a tablet shows the list beside the ticket")
        } else {
            XCTAssertFalse(list.exists && list.isHittable, "a phone shows the ticket over the list")
            // The split's own Back (titled after the list) pops its stack —
            // not the app's panel back.
            print("xbin-nav bars before Back:\n\(e.app.navigationBars.debugDescription)")
            let back = e.app.navigationBars.buttons.matching(NSPredicate(format: "label == %@ OR label == %@", "Tickets", "Back")).firstMatch
            XCTAssertTrue(back.waitForExistence(timeout: 10), "the detail's Back")
            back.tap()
            e.shot("nav-02-after-back")
            XCTAssertTrue(list.waitForExistence(timeout: 10) && e.until(10) { list.isHittable }, "Back shows the list")
            XCTAssertFalse(number.exists, "…and the ticket is gone")
            XCTAssertTrue(e.backButton.exists, "the tile's panel is still in front")
            e.shot("nav-02-list")
        }

        // A link while the view runs: xbn.navigate → hashchange.
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/desk#t=t-3")!)
        let t3 = e.app.staticTexts["t-3"]
        XCTAssertTrue(t3.waitForExistence(timeout: 30), "the second link's ticket")
        e.shot("nav-03-navigate")
    }
}
