import XCTest

/// Tile widgets on the phone's screens (D117, native/spec/tree.md §13), end
/// to end on a simulator against the e2e xbind (native/AGENTS.md → "Mac
/// mini"): the native counter (examples/counter-go) draws its widget on the
/// screen's card — the count and a +1 — and the +1 on the card increments
/// it without opening the tile.
final class XbinWidgetTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    /// The counter's card shows its widget with the backend's count; the
    /// card's +1 reaches the backend and the card shows the new count, and
    /// the screen stays (the tap was the widget's, not the card's).
    @MainActor
    func test01CounterWidget() async throws {
        let e = try E2E(self)
        try await e.server.seedScreens()
        let before = try await e.server.counter()
        e.launch()
        e.ensureWorkspace()
        e.openScreen()
        let card = e.card("apps/counter")
        XCTAssertTrue(card.waitForExistence(timeout: 20), "the counter's card")
        let count = card.staticTexts["\(before)"]
        XCTAssertTrue(count.waitForExistence(timeout: 60), "the counter's widget shows \(before)")
        let plus = card.buttons["+1"]
        XCTAssertTrue(plus.waitForExistence(timeout: 10), "the widget's +1")
        e.shot("widgets-01-counter")
        plus.tap()
        await e.eventually("the counter's backend at \(before + 1)", timeout: 30) {
            try await e.server.counter() == before + 1
        }
        XCTAssertTrue(card.staticTexts["\(before + 1)"].waitForExistence(timeout: 20), "the widget shows \(before + 1)")
        XCTAssertTrue(e.app.buttons["Edit"].exists, "still on the screen: the +1 was the widget's")
        e.shot("widgets-01-counter-after")

        // Anywhere else on the card opens the tile, its runtime already warm.
        card.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1)).tap()
        let row = e.app.descendants(matching: .any).matching(NSPredicate(
            format: "label == %@ OR (label BEGINSWITH %@ AND label ENDSWITH %@)", "Count, \(before + 1)", "Count", ", \(before + 1)"
        )).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "the counter's screen, showing \(before + 1)")
        e.shot("widgets-01-counter-tile")
    }
}
