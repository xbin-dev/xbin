import XCTest

/// A tile's sessions screen (D132), end to end on a simulator against the
/// e2e xbind (native/AGENTS.md → "Mac mini"): the web terminal window's
/// counterpart — the launcher, tabs across shells and agents, the tools.
final class XbinSessionsTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    /// Long press → New session… opens the tile's sessions screen on its
    /// launcher; Bash makes a shell tab, `+` shows the launcher again, the
    /// fake agent makes an agent tab beside it; a tap switches back, the
    /// shell still there; a tab's long press ends the agent's session.
    @MainActor
    func test01LauncherAndTabs() async throws {
        let e = try E2E(self)
        for s in try await e.server.sessions(cwd: "apps/welcome") { await e.server.end(s.id) }
        e.launch()
        e.ensureWorkspace()
        e.openLauncher("apps/welcome")
        let bash = e.launcherBox("Terminal")
        XCTAssertTrue(bash.waitForExistence(timeout: 30), "the launcher's Bash box")
        XCTAssertTrue(e.launcherBox("Agent").waitForExistence(timeout: 30), "the launcher's box for the fake agent")
        XCTAssertTrue(e.containing("Start a session in apps/welcome").exists, "the launcher's heading")
        e.shot("sessions-01-launcher")

        bash.tap()
        let shellTab = e.sessionTabs.matching(NSPredicate(format: "label BEGINSWITH %@", "Bash")).firstMatch
        XCTAssertTrue(shellTab.waitForExistence(timeout: 20), "a Bash tab")
        await e.eventually("a shell session on apps/welcome", timeout: 30) {
            try await e.server.sessions(cwd: "apps/welcome").contains { $0.kind != "agent" }
        }
        e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        e.app.typeText("printf '\\033]0;xbin-tab-%d\\007' $((40+2))\n")
        XCTAssertTrue(e.element("xbin-tab-42").waitForExistence(timeout: 30), "the shell's title in the bar")
        e.shot("sessions-02-shell-tab")

        let plus = e.app.buttons.matching(identifier: "sessions-plus").firstMatch
        XCTAssertTrue(plus.exists, "the strip's +")
        plus.tap()
        XCTAssertTrue(e.launcherBox("Agent").waitForExistence(timeout: 10), "+ shows the launcher again")
        e.launcherBox("Agent").tap()
        let agentTab = e.sessionTabs.matching(NSPredicate(format: "label CONTAINS %@", "agent")).firstMatch
        XCTAssertTrue(agentTab.waitForExistence(timeout: 30), "an agent tab beside the shell's")
        let composer = e.element("Message the agent", in: e.app.textViews)
        let field = e.element("Message the agent", in: e.app.textFields)
        XCTAssertTrue(e.first(of: [composer, field], timeout: 30) != nil, "the agent tab’s composer")
        XCTAssertEqual(e.sessionTabs.count, 2, "two tabs")
        e.shot("sessions-03-agent-tab")

        // Back to the shell: the same session, its title still there.
        shellTab.tap()
        XCTAssertTrue(e.element("xbin-tab-42").waitForExistence(timeout: 10), "the shell tab again, as it was")

        // The agent tab's long press: End session.
        agentTab.press(forDuration: 1.2)
        let end = e.app.buttons["End session"]
        XCTAssertTrue(end.waitForExistence(timeout: 10), "the tab's menu")
        e.shot("sessions-04-tab-menu")
        end.tap()
        await e.eventually("the agent session ended", timeout: 30) {
            try await !e.server.sessions(cwd: "apps/welcome").contains { $0.kind == "agent" }
        }
        for s in try await e.server.sessions(cwd: "apps/welcome") { await e.server.end(s.id) }
    }
}
