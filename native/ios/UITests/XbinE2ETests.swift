import XCTest

/// The app end to end on a simulator against a real xbind (native/AGENTS.md
/// → "Mac mini"): mac-remote.sh e2e starts native/ios/scripts/e2e-xbind.sh
/// on the Linux box — a fresh workspace with the native counter
/// (examples/counter-go) and the scripted fake agent — tunnels it to the Mac
/// and runs these on an erased simulator. Each test launches the app and
/// adds the workspace itself when the app has none, so any one runs alone
/// (-only-testing); the names keep them in order. Screenshots: E2E_DIR and
/// the result bundle.
///
/// Without XBIN_E2E_URL they skip (the hosted CI only builds them).
final class XbinE2ETests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / XBIN_E2E_TOKEN unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    /// Add a workspace by URL + token; the navigator lists its tiles.
    @MainActor
    func test01AddWorkspace() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        _ = e.findTile("apps/counter")
        e.shot("01-workspace")
    }

    /// A web tile: apps/welcome's page draws in the tile's web view.
    @MainActor
    func test02WebTile() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.openTile("apps/welcome")
        let web = e.app.webViews.firstMatch
        XCTAssertTrue(web.waitForExistence(timeout: 30), "the web tile's view")
        XCTAssertTrue(web.staticTexts["the mental model"].waitForExistence(timeout: 30), "apps/welcome's page")
        e.shot("02-web-tile")
    }

    /// The native counter: its native.js draws natively, +1 reaches the
    /// backend, and the new value comes back into the row.
    @MainActor
    func test03NativeCounter() async throws {
        let e = try E2E(self)
        let before = try await e.server.counter()
        e.launch()
        e.ensureWorkspace()
        e.openTile("apps/counter")
        let plus = e.app.buttons.matching(NSPredicate(format: "label == %@", "+1")).firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 60), "the native counter's +1 button")
        e.shot("03-native-counter")
        plus.tap()
        await e.eventually("the counter's backend at \(before + 1)", timeout: 30) {
            try await e.server.counter() == before + 1
        }
        let row = e.app.descendants(matching: .any).matching(NSPredicate(
            format: "label == %@ OR (label BEGINSWITH %@ AND label ENDSWITH %@)", "Count, \(before + 1)", "Count", ", \(before + 1)"
        )).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "the row shows \(before + 1)")
        e.shot("03-native-counter-after")
    }

    /// A terminal on apps/welcome: a command typed on the simulator runs in
    /// the tile's shell, and what it prints comes back — an OSC title the
    /// screen shows in its navigation bar (the typed text itself does not
    /// contain "xbin-e2e-42"; only the shell's arithmetic makes it).
    @MainActor
    func test04TerminalEcho() async throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.tileAction("apps/welcome", "Terminal here")
        await e.eventually("a shell session on apps/welcome", timeout: 30) {
            try await e.server.sessions(cwd: "apps/welcome").contains { $0.kind != "agent" }
        }
        // Focus the terminal, then type (the pty keeps what arrives before
        // the prompt).
        e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        e.app.typeText("printf '\\033]0;xbin-e2e-%d\\007' $((40+2))\n")
        let title = e.element("xbin-e2e-42")
        XCTAssertTrue(title.waitForExistence(timeout: 30), "the shell's output (its title) on screen")
        e.shot("04-terminal")
        for s in try await e.server.sessions(cwd: "apps/welcome") where s.kind != "agent" {
            await e.server.end(s.id)
        }
    }

    /// An agent session with the fake ACP agent: a prompt from the composer
    /// gets the agent's answer in the transcript.
    @MainActor
    func test05AgentSession() async throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.tileAction("apps/welcome", "Agent here")
        // Several providers → the launcher; exactly one would start at once.
        let fake = e.app.buttons["Fake agent (tests)"]
        if fake.waitForExistence(timeout: 20) { fake.tap() }
        let composer = e.element("Message the agent", in: e.app.textViews)
        let field = e.element("Message the agent", in: e.app.textFields)
        guard let which = e.first(of: [composer, field], timeout: 30) else {
            e.shot("05-agent-no-composer")
            XCTFail("the agent's composer")
            return
        }
        let input = which == 0 ? composer : field
        input.tap()
        input.typeText("hello from the simulator")
        e.app.buttons["Send"].tap()
        let answer = e.containing("echo: hello from the simulator")
        XCTAssertTrue(answer.waitForExistence(timeout: 60), "the fake agent's answer in the transcript")
        e.shot("05-agent")
        let agents = try await e.server.sessions(cwd: "apps/welcome").filter { $0.kind == "agent" }
        XCTAssertTrue(agents.contains { $0.provider == "fake" }, "a fake-agent session on apps/welcome")
        for s in agents { await e.server.end(s.id) }
    }
}
