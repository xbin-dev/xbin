import XCTest

/// Partitioned tiles in the app (D181), end to end against the e2e xbind
/// (native/AGENTS.md → "Mac mini"), whose workspace has apps/parted — a
/// static tile declaring `"partition": ["user"]`
/// (scripts/testdata/e2e-tiles/parted): its row carries the partitioned
/// marker (said in its accessibility value; a tile that isn't partitioned
/// says nothing of the kind); Settings → Your partitions opens xbind's
/// partitions page in the app's own web view, signed in by its web ticket
/// ("Continue as …", one tap); and the link the partition pushes carry,
/// `xbin://<host:port>/xbin/partitions`, opens the same page over the place
/// the window was.
final class XbinPartitionsTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    static let markWords = "Partitioned: each person here has their own data"

    /// The marker on a partitioned tile's row, and on no other.
    @MainActor
    func test01MarkerOnTheRow() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        let row = e.findTile("apps/parted")
        XCTAssertTrue(e.until(15) { (row.value as? String ?? "").contains(Self.markWords) },
                      "apps/parted's row says it is partitioned: \(row.value ?? "nil")")
        e.shot("partitions-01-row")
        let plain = e.findTile("apps/welcome")
        XCTAssertFalse((plain.value as? String ?? "").contains("Partitioned"), "apps/welcome's row doesn't: \(plain.value ?? "nil")")
    }

    /// Settings → Your partitions: the page, signed in, in the app.
    @MainActor
    func test02SettingsOpensThePage() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let settings = e.app.buttons["Settings"]
        XCTAssertTrue(settings.waitForExistence(timeout: 30), "Home's Settings")
        settings.tap()
        let entry = e.app.buttons["Your partitions"]
        XCTAssertTrue(entry.waitForExistence(timeout: 30), "Settings links the partitions page (a partitioned tile is in sight)")
        e.shot("partitions-02-settings")
        entry.tap()
        e.expectPartitionsPage("partitions-02")
        e.goHome()
    }

    /// The pushes' link opens the page too (as a notification's tap does).
    @MainActor
    func test03LinkOpensThePage() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.app.open(URL(string: "xbin://\(e.server.authority)/xbin/partitions")!)
        e.expectPartitionsPage("partitions-03")
        XCTAssertTrue(e.backButton.exists, "back goes to where the window was")
        e.backButton.tap()
        XCTAssertTrue(e.app.buttons["Settings"].waitForExistence(timeout: 10), "…Home")
    }
}

extension E2E {
    /// xbind's partitions page in the app's web view: through the web
    /// ticket's "Continue as …" page when the view isn't signed in yet,
    /// then the page itself — never a password form, never a page-back onto
    /// the spent ticket.
    func expectPartitionsPage(_ name: String, file: StaticString = #filePath, line: UInt = #line) {
        let web = app.webViews.firstMatch
        let proceed = web.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Continue as")).firstMatch
        let page = web.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Partitioned tiles you use")).firstMatch
        guard let seen = first(of: [proceed, page], timeout: 40) else {
            shot(name + "-nothing")
            XCTFail("the web ticket's page or the partitions page", file: file, line: line)
            return
        }
        if seen == 0 {
            shot(name + "-continue")
            proceed.tap()
        }
        XCTAssertTrue(page.waitForExistence(timeout: 30), "the partitions page, signed in as the account", file: file, line: line)
        XCTAssertTrue(app.navigationBars["Your partitions"].exists || app.staticTexts["Your partitions"].exists,
                      "the panel's title", file: file, line: line)
        XCTAssertFalse(web.secureTextFields.firstMatch.exists, "no password form", file: file, line: line)
        shot(name + "-page")
        XCTAssertFalse(app.buttons["Page back"].exists, "no page-back onto the spent ticket", file: file, line: line)
    }
}
