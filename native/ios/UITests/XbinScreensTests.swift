import XCTest

/// The phone's screens and panels (plans/native.md §4, D117), end to end on
/// a simulator against the e2e xbind (native/AGENTS.md → "Mac mini"): Home
/// lists the account's screens (a layout seeded as the web shell writes
/// it), a screen shows its tiles as cards, a tile opens as the next panel;
/// edge swipes go back and forward, a short one only peeks; Edit reorders,
/// resizes and hides into the account's own `mobile-screens` pref; + Create
/// tile makes a tile and opens it on "What should this tile be?", whose
/// agent (the fake one) answers the prompt; a chrome tile opens in the app's
/// web view, signed in by a web ticket; the terminal's keyboard follows a
/// drag down; the switcher says "Used recently". test09Gallery shoots every
/// new screen, light and dark.
final class XbinScreensTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    /// Home → the screen → a tile; a left-edge swipe back to the screen, a
    /// right-edge one forward to the tile again; a short slow left-edge
    /// drag only peeks (the tile stays); the bar's ‹ and ▦ go back too.
    @MainActor
    func test01BackAndForwardSwipes() async throws {
        let e = try E2E(self)
        try await e.server.seedScreens()
        e.launch()
        e.ensureWorkspace()
        e.openScreen()
        let welcome = e.card("apps/welcome")
        XCTAssertTrue(welcome.waitForExistence(timeout: 20), "the screen's cards")
        XCTAssertTrue(e.card("apps/counter").exists && e.card("apps/wide").exists, "every tile of the screen as a card")
        e.shot("screens-01-screen")
        welcome.tap()
        let heading = e.app.webViews.firstMatch.staticTexts["the mental model"]
        XCTAssertTrue(heading.waitForExistence(timeout: 30), "apps/welcome's page, full screen")
        XCTAssertEqual(e.backButton.label, E2EServer.screenName, "the bar's way back names the screen")

        // Back: the screen again, its Edit in the bar.
        e.edgeSwipe(fromLeft: true, fraction: 0.75)
        let edit = e.app.buttons["Edit"]
        XCTAssertTrue(e.until(10) { edit.exists && edit.isHittable && welcome.isHittable }, "back on the screen after a left-edge swipe")
        XCTAssertFalse(heading.isHittable, "the tile left the screen (kept aside for forward)")
        e.shot("screens-01-back")

        // Forward: the same page, not reloaded from nothing.
        e.edgeSwipe(fromLeft: false, fraction: 0.75)
        XCTAssertTrue(heading.waitForExistence(timeout: 10), "forward to the tile after a right-edge swipe")
        XCTAssertTrue(e.until(5) { e.backButton.isHittable }, "the tile's bar")

        // A peek: a short slow drag from the left edge, then let go.
        e.edgeSwipe(fromLeft: true, fraction: 0.18, speed: 250)
        XCTAssertTrue(e.until(5) { heading.exists && e.backButton.isHittable && !edit.exists }, "a short drag only peeks: still on the tile")
        e.shot("screens-01-after-peek")

        // No forward without a back first.
        e.edgeSwipe(fromLeft: false, fraction: 0.75)
        XCTAssertTrue(e.until(3) { heading.exists && e.backButton.isHittable }, "nothing to go forward to")

        // The bar: ‹ E2E screen, then ▦ Home.
        e.backButton.tap()
        XCTAssertTrue(edit.waitForExistence(timeout: 10), "‹ the screen")
        e.homeButton.tap()
        XCTAssertTrue(e.screenRow().waitForExistence(timeout: 10), "▦ Home")
        e.shot("screens-01-home")
    }

    /// Edit: the third card to the top, one card wide, one hidden; Done
    /// saves it in the account's mobile-screens pref, and a new launch
    /// shows the screen that way.
    @MainActor
    func test02EditReorderSavedAndRestored() async throws {
        let e = try E2E(self)
        try await e.server.seedScreens()
        e.launch()
        e.ensureWorkspace()
        e.openScreen()
        XCTAssertTrue(e.card("apps/welcome").waitForExistence(timeout: 20))
        e.app.buttons["Edit"].tap()
        let wideRow = e.app.cells.containing(.staticText, identifier: "Wide").firstMatch
        let welcomeRow = e.app.cells.containing(.staticText, identifier: "Welcome").firstMatch
        XCTAssertTrue(wideRow.waitForExistence(timeout: 10) && welcomeRow.exists, "the editor's rows")
        e.shot("screens-02-edit")

        // Reorder: apps/wide's handle dragged onto the first row.
        let handle = wideRow.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Reorder")).firstMatch
        let grip = handle.exists ? handle.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
                                 : wideRow.coordinate(withNormalizedOffset: CGVector(dx: 0.96, dy: 0.5))
        grip.press(forDuration: 0.6, thenDragTo: welcomeRow.coordinate(withNormalizedOffset: CGVector(dx: 0.96, dy: 0.2)))
        // The counter wide; the welcome page hidden from this phone.
        let counterRow = e.app.cells.containing(.staticText, identifier: "Counter").firstMatch
        counterRow.buttons["Wide"].tap()
        e.app.buttons["Hide Welcome"].tap()
        XCTAssertTrue(e.app.staticTexts["Hidden on this phone"].waitForExistence(timeout: 5), "the hidden tile listed to show again")
        e.shot("screens-02-edited")
        e.app.buttons["Done"].tap()

        await e.eventually("the arrangement in the mobile-screens pref", timeout: 20) {
            let a = try await e.server.arrangement(E2EServer.screenID)
            return a.map(\.path) == ["apps/wide", "apps/counter"] && a.map(\.size) == ["small", "wide"]
        }
        XCTAssertFalse(e.card("apps/welcome").exists, "the hidden tile's card is gone")
        e.shot("screens-02-saved")

        // A new launch: the same arrangement.
        e.app.terminate()
        e.launch()
        e.openScreen()
        let wide = e.card("apps/wide"), counter = e.card("apps/counter")
        XCTAssertTrue(wide.waitForExistence(timeout: 30) && counter.exists, "the arranged cards after a relaunch")
        XCTAssertFalse(e.card("apps/welcome").exists, "still hidden")
        XCTAssertTrue(wide.frame.minY < counter.frame.minY, "apps/wide first: \(wide.frame) vs \(counter.frame)")
        XCTAssertTrue(counter.frame.width > wide.frame.width * 1.6, "the counter wide: \(counter.frame) vs \(wide.frame)")
        e.shot("screens-02-restored")
    }

    /// + Create tile: a name → the tile exists, on the screen (the web
    /// layout's and the phone's) → "What should this tile be?" → a prompt
    /// to the fake agent, which answers it in the new tile's session.
    @MainActor
    func test03CreateTileAndBuildIt() async throws {
        let e = try E2E(self)
        try await e.server.seedScreens()
        let name = "Phone made \(Int.random(in: 1000...9999))"
        let path = "apps/" + name.lowercased().replacingOccurrences(of: " ", with: "-")
        e.launch()
        e.ensureWorkspace()
        e.openScreen()
        XCTAssertTrue(e.card("apps/welcome").waitForExistence(timeout: 20))
        e.app.buttons["Edit"].tap()
        e.app.buttons["Create tile"].tap()
        let field = e.element("Tile name", in: e.app.textFields)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the create sheet")
        field.tap()
        field.typeText(name)
        e.shot("screens-03-create")
        e.tapAfterTyping(e.app.buttons["Create"])

        let chooser = e.app.staticTexts["What should this tile be?"]
        XCTAssertTrue(chooser.waitForExistence(timeout: 30), "the new tile opens on the build chooser")
        let components = try await e.server.components()
        XCTAssertTrue(components.contains(path), "\(path) created: \(components)")
        let layout = try await e.server.pref("layout") as? [String: Any]
        let screen = (layout?["screens"] as? [[String: Any]])?.first { $0["id"] as? String == E2EServer.screenID }
        XCTAssertTrue(((screen?["tiles"] as? [[String: Any]]) ?? []).contains { $0["path"] as? String == path },
                      "on the personal screen's web layout too")
        await e.eventually("on the phone's arrangement", timeout: 15) {
            try await e.server.arrangement(E2EServer.screenID).contains { $0.path == path }
        }

        let prompt = e.element("Describe what it should do and show…", in: e.app.textViews)
        let promptField = e.element("Describe what it should do and show…", in: e.app.textFields)
        guard let which = e.first(of: [prompt, promptField], timeout: 10) else {
            e.shot("screens-03-no-prompt")
            XCTFail("the chooser's prompt")
            return
        }
        let input = which == 0 ? prompt : promptField
        input.tap()
        input.typeText("show the time")
        let fake = e.app.buttons["Fake agent (tests)"]
        XCTAssertTrue(fake.waitForExistence(timeout: 20), "the fake agent among the providers")
        e.tapAfterTyping(fake)
        e.shot("screens-03-chooser")
        e.app.buttons["Start building"].tap()

        let answer = e.containing("echo: show the time")
        let answered = answer.waitForExistence(timeout: 60)
        e.shot("screens-03-agent")
        XCTAssertTrue(answered, "the fake agent answered the chooser's prompt (screens-03-agent.png)")
        let agents = try await e.server.sessions(cwd: path).filter { $0.kind == "agent" }
        XCTAssertTrue(agents.contains { $0.provider == "fake" }, "a fake-agent session in \(path)")
        for s in agents { await e.server.end(s.id) }
    }

    /// A chrome tile (tiles/organisations: it acts as the user) opens in
    /// the app's web view on the workspace's own origin: the web ticket's
    /// "Continue as …" page, one tap, then the page signed in.
    @MainActor
    func test04ChromeTileSignedIn() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.openTile("tiles/organisations")
        let web = e.app.webViews.firstMatch
        let proceed = web.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Continue as")).firstMatch
        let signedIn = web.staticTexts["my organisations"]
        guard let seen = e.first(of: [proceed, signedIn], timeout: 30) else {
            e.shot("screens-04-chrome-nothing")
            XCTFail("the web ticket's page or the tile")
            return
        }
        if seen == 0 {
            e.shot("screens-04-continue")
            proceed.tap()
        }
        XCTAssertTrue(signedIn.waitForExistence(timeout: 30), "tiles/organisations, signed in as the account")
        e.shot("screens-04-chrome-signed-in")
        XCTAssertFalse(e.app.buttons["Page back"].exists, "no page-back onto the spent ticket")
    }

    /// The terminal: the keyboard up, a drag from the output down into the
    /// keyboard takes it down.
    @MainActor
    func test05TerminalKeyboardFollowsADragDown() async throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.tileAction("apps/welcome", "Terminal here")
        await e.eventually("a shell session on apps/welcome", timeout: 30) {
            try await e.server.sessions(cwd: "apps/welcome").contains { $0.kind != "agent" }
        }
        e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).tap()
        XCTAssertTrue(e.until(15) { e.softKeyboard() != nil }, "the software keyboard")
        e.shot("screens-05-keyboard")
        let from = e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3))
        let to = e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.97))
        from.press(forDuration: 0.05, thenDragTo: to, withVelocity: XCUIGestureVelocity(600), thenHoldForDuration: 0.1)
        let hidden = e.until(8) { e.softKeyboard() == nil }
        e.shot("screens-05-keyboard-dragged")
        XCTAssertTrue(hidden, "the keyboard went down with the drag")
        for s in try await e.server.sessions(cwd: "apps/welcome") where s.kind != "agent" {
            await e.server.end(s.id)
        }
    }

    /// The switcher lists what was used recently under "Used recently".
    @MainActor
    func test06SwitcherSaysUsedRecently() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.openTile("apps/welcome")
        XCTAssertTrue(e.app.webViews.firstMatch.waitForExistence(timeout: 30))
        e.app.buttons["Workspaces"].tap()
        XCTAssertTrue(e.app.staticTexts["Used recently"].waitForExistence(timeout: 10), "the switcher's Used recently")
        XCTAssertTrue(e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Welcome")).firstMatch.exists, "the tile in it")
        e.shot("screens-06-switcher")
    }

    /// Every new screen, light and dark, for looking at: Home, a screen,
    /// edit mode, the create sheet, the build chooser.
    @MainActor
    func test09Gallery() async throws {
        for appearance in ["light", "dark"] {
            let e = try E2E(self)
            try await e.server.seedScreens()
            e.app.launchArguments += ["-XbinAppearance", appearance]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            XCTAssertTrue(e.screenRow().waitForExistence(timeout: 30))
            e.shot("gallery-\(appearance)-01-home")
            e.screenRow().tap()
            XCTAssertTrue(e.card("apps/welcome").waitForExistence(timeout: 20))
            e.shot("gallery-\(appearance)-02-screen")
            e.app.buttons["Edit"].tap()
            XCTAssertTrue(e.app.buttons["Create tile"].waitForExistence(timeout: 10))
            e.shot("gallery-\(appearance)-03-edit")
            e.app.buttons["Create tile"].tap()
            let field = e.element("Tile name", in: e.app.textFields)
            XCTAssertTrue(field.waitForExistence(timeout: 10))
            field.typeText("Weather \(appearance) \(Int.random(in: 100...999))")
            _ = e.until(3) { e.softKeyboard() != nil }
            e.shot("gallery-\(appearance)-04-create")
            e.tapAfterTyping(e.app.buttons["Create"])
            XCTAssertTrue(e.app.staticTexts["What should this tile be?"].waitForExistence(timeout: 30))
            _ = e.app.buttons["Fake agent (tests)"].waitForExistence(timeout: 10)
            e.shot("gallery-\(appearance)-05-chooser")
            e.app.terminate()
        }
    }
}
