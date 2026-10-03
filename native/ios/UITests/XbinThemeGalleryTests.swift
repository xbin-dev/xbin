import XCTest

/// The app's own surfaces in light and dark, for looking at after a change
/// to its look (the theme, the mark): Home, a screen's cards, a native and a
/// web tile, live reload & deployments, Settings, then a tile's sessions
/// screen with its launcher, a terminal and an agent session as tabs, as
/// `theme-<light|dark>-NN-<screen>.png` in E2E_DIR. The onboarding's own
/// gallery is XbinOnboardingTests.test10Gallery, the screens'
/// XbinScreensTests.test09Gallery.
final class XbinThemeGalleryTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    func test01Gallery() async throws {
        for mode in ["light", "dark"] {
            var n = 0
            func shot(_ e: E2E, _ name: String) {
                n += 1
                e.shot(String(format: "theme-%@-%02d-%@", mode, n, name))
            }
            let e = try E2E(self)
            try await e.server.seedScreens()
            for s in try await e.server.sessions(cwd: "apps/welcome") { await e.server.end(s.id) }
            try await e.server.resetDeployments("apps/wide")
            try await e.server.deploy("add", ["tile": "apps/wide", "deployment": "dev"])
            e.app.launchArguments += ["-XbinAppearance", mode]
            e.launch()
            e.ensureWorkspace()

            // Home, then Settings (before any search: a search in progress
            // hides Home's bar), in a launch of their own.
            e.goHome()
            XCTAssertTrue(e.screenRow().waitForExistence(timeout: 30), "Home lists the screen")
            shot(e, "home")
            let settings = e.app.buttons["Settings"]
            XCTAssertTrue(settings.waitForExistence(timeout: 30), "Home's Settings")
            settings.tap()
            _ = e.until(3) { false }
            shot(e, "settings")
            e.app.terminate()

            // A screen's cards.
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            XCTAssertTrue(e.screenRow().waitForExistence(timeout: 30), "Home lists the screen")
            e.screenRow().tap()
            XCTAssertTrue(e.card("apps/welcome").waitForExistence(timeout: 20), "the screen's cards")
            _ = e.until(5) { e.card("apps/counter").exists }
            shot(e, "screen")

            // A native tile and a web tile.
            e.openTile("apps/counter")
            // The tile's own row (a screen's card may show the widget's +1).
            let count = e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Count,")).firstMatch
            XCTAssertTrue(count.waitForExistence(timeout: 60), "the native counter")
            shot(e, "native-tile")
            e.openTile("apps/welcome")
            XCTAssertTrue(e.app.webViews.firstMatch.staticTexts["the mental model"].waitForExistence(timeout: 30), "apps/welcome's page")
            shot(e, "web-tile")
            e.app.terminate()

            // Live reload & deployments (apps/wide, with a dev deployment),
            // in a fresh launch.
            e.launch()
            e.ensureWorkspace()
            e.openLauncher("apps/wide")
            XCTAssertTrue(e.launcherBox("Terminal").waitForExistence(timeout: 30), "apps/wide's launcher")
            shot(e, "launcher")
            let tools = e.app.buttons.matching(identifier: "sessions-tools").firstMatch
            XCTAssertTrue(tools.waitForExistence(timeout: 10), "the tools menu")
            tools.tap()
            let item = e.app.buttons["Live reload & deployments"]
            XCTAssertTrue(item.waitForExistence(timeout: 10), "Live reload & deployments")
            item.tap()
            XCTAssertTrue(e.app.buttons.matching(identifier: "deployment:dev").firstMatch.waitForExistence(timeout: 30), "dev's row")
            shot(e, "deployments")
            e.app.terminate()

            // The sessions screen: a shell, an agent beside it (the
            // keyboard's screens last, in a fresh launch).
            e.launch()
            e.ensureWorkspace()
            e.newSession("apps/welcome", "Terminal")
            await e.eventually("a shell session on apps/welcome", timeout: 30) {
                try await e.server.sessions(cwd: "apps/welcome").contains { $0.kind != "agent" }
            }
            e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
            // The ANSI colours (normal, then bright) and a title to wait on.
            e.app.typeText("clear; for c in 1 2 3 4 5 6; do printf '\\033[3%dm normal%d \\033[9%dm bright%d\\033[0m\\n' $c $c $c $c; done;"
                + " printf '\\033[1mbold\\033[0m plain\\n'; printf '\\033]0;gallery-%d\\007' $((40+2))\n")
            XCTAssertTrue(e.element("gallery-42").waitForExistence(timeout: 30), "the shell's output")
            shot(e, "terminal")
            let plus = e.app.buttons.matching(identifier: "sessions-plus").firstMatch
            XCTAssertTrue(plus.waitForExistence(timeout: 10), "the strip's +")
            plus.tap()
            XCTAssertTrue(e.launcherBox("Agent").waitForExistence(timeout: 10), "the launcher again")
            e.launcherBox("Agent").tap()
            let composer = e.element("Message the agent", in: e.app.textViews)
            let field = e.element("Message the agent", in: e.app.textFields)
            if let which = e.first(of: [composer, field], timeout: 30) {
                let input = which == 0 ? composer : field
                input.tap()
                input.typeText("show me the theme")
                e.tapAfterTyping(e.app.buttons["Send"])
                _ = e.containing("echo: show me the theme").waitForExistence(timeout: 60)
            }
            shot(e, "agent")

            for s in try await e.server.sessions(cwd: "apps/welcome") { await e.server.end(s.id) }
            try await e.server.resetDeployments("apps/wide")
            e.app.terminate()
        }
    }
}
