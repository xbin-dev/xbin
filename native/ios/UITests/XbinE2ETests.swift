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

    /// A web tile: apps/welcome's page draws in the tile's web view — at
    /// the device's width, readable: it sets no viewport (a card's page), so
    /// WebKit alone would lay it out 980 px wide and shrink its 13 px
    /// heading to about 5 pt (plans/native.md §6.3, XbinCore's TileViewport).
    @MainActor
    func test02WebTile() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        e.openTile("apps/welcome")
        let web = e.app.webViews.firstMatch
        XCTAssertTrue(web.waitForExistence(timeout: 30), "the web tile's view")
        let heading = web.staticTexts["the mental model"]
        XCTAssertTrue(heading.waitForExistence(timeout: 30), "apps/welcome's page")
        let width = e.app.windows.firstMatch.frame.width
        let readable = e.until(10) { heading.frame.height >= 12 }
        e.shot("02-web-tile")
        XCTAssertTrue(readable, "the 13 px heading at about 1:1 (\(heading.frame) in a \(width) pt window)")
        XCTAssertTrue(heading.frame.minX >= 0 && heading.frame.maxX <= width, "on screen: \(heading.frame)")
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

        // The key row sits on top of the software keyboard, visible and
        // working: another title, then ↑ ↑ from the row recalls the first
        // printf, and Return runs it again.
        let up = e.app.buttons["Up arrow"]
        XCTAssertTrue(e.app.keyboards.firstMatch.waitForExistence(timeout: 10), "the software keyboard")
        XCTAssertTrue(up.waitForExistence(timeout: 10), "the key row's ↑")
        e.keyRowAboveKeyboard()
        e.shot("04-terminal-keyboard")
        e.app.typeText("printf '\\033]0;xbin-e2e-other\\007'\n")
        XCTAssertTrue(e.element("xbin-e2e-other").waitForExistence(timeout: 30), "the second title")
        XCTAssertTrue(e.until(10) { !title.exists }, "the first title gone")
        up.tap()
        up.tap()
        e.app.typeText("\n")
        XCTAssertTrue(title.waitForExistence(timeout: 30), "↑ ↑ from the key row recalled the first printf")
        e.shot("04-terminal-keys")
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
        // Where Send rests once the keyboard is back: tapped while typing had
        // hidden the keyboard it pressed a key instead (test04 → test05
        // failed so, 2026-09-27; E2E.tapAfterTyping).
        e.tapAfterTyping(e.app.buttons["Send"])
        // Sent: the composer empties (the screen clears its draft).
        let emptied = e.until(10) { (input.value as? String ?? "").isEmpty }
        if !emptied { e.shot("05-agent-not-sent") }
        XCTAssertTrue(emptied, "Send sent the message: the composer still holds \(input.value ?? "nil")")
        let answer = e.containing("echo: hello from the simulator")
        let answered = answer.waitForExistence(timeout: 60)
        e.shot("05-agent")
        XCTAssertTrue(answered, "the fake agent's answer in the transcript (what the screen says instead: 05-agent.png)")
        let agents = try await e.server.sessions(cwd: "apps/welcome").filter { $0.kind == "agent" }
        XCTAssertTrue(agents.contains { $0.provider == "fake" }, "a fake-agent session on apps/welcome")
        for s in agents { await e.server.end(s.id) }
    }

    /// Viewports (plans/native.md §6.3): a desktop-first page wider than the
    /// screen (apps/wide, e2e-xbind.sh's fixture: no viewport meta, a
    /// 1200 px strip) is laid out at its width and fitted to the screen,
    /// with pinch zoom; a page with a mobile viewport of its own
    /// (apps/phone) keeps it, untouched. Each page reports what it sees.
    @MainActor
    func test06WebViewports() throws {
        let e = try E2E(self)
        e.launch()
        e.ensureWorkspace()
        let width = e.app.windows.firstMatch.frame.width
        e.openTile("apps/wide")
        let web = e.app.webViews.firstMatch
        XCTAssertTrue(web.waitForExistence(timeout: 30), "the web tile's view")
        let report = web.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "layout ")).firstMatch
        XCTAssertTrue(report.waitForExistence(timeout: 30), "apps/wide's page")
        let laidOut = e.until(10) { report.label == "layout 1200 metas 1" }
        let left = web.staticTexts["wide-left-edge"], right = web.staticTexts["wide-right-edge"]
        XCTAssertTrue(left.exists && right.exists, "the strip's two ends")
        e.shot("06-wide-fit")
        XCTAssertTrue(laidOut, "laid out at its own width: \(report.label)")
        XCTAssertTrue(left.frame.minX >= -1 && right.frame.maxX <= width + 1,
                      "both ends on screen (fit to width): \(left.frame) … \(right.frame) in \(width) pt")
        let before = left.frame.height
        // About the view's middle (a web text can't be pinched: no hit point),
        // so the zoomed shot shows the page's empty middle; the strip's
        // growth below is the check.
        web.pinch(withScale: 3, velocity: 2)
        let zoomed = e.until(5) { left.frame.height > before * 1.8 }
        e.shot("06-wide-zoomed")
        XCTAssertTrue(zoomed, "pinch zooms in: \(before) → \(left.frame.height) pt")

        e.openTile("apps/phone")
        let phone = e.app.webViews.firstMatch
        let own = phone.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "viewport ")).firstMatch
        XCTAssertTrue(own.waitForExistence(timeout: 30), "apps/phone's page")
        let expected = "viewport 1 width=device-width, initial-scale=1 layout \(Int(width))"
        let kept = e.until(10) { own.label == expected }
        e.shot("06-phone")
        XCTAssertTrue(kept, "its own viewport, alone and as written: \(own.label), want \(expected)")
    }

    /// With a hardware keyboard the key row stays: docked at the bottom of
    /// the screen, tappable, and typing and its keys work. A headless
    /// simulator can't attach one (XCUITest brings up the software
    /// keyboard), so the app's -XbinNoSoftKeyboard (Debug builds) stands in:
    /// the terminal gets an empty input view and UIKit docks the row where a
    /// hardware keyboard docks it. Its keys overlap the home indicator's
    /// strip there, as the row always has (KeyRowBar): not asserted yet.
    @MainActor
    func test07TerminalHardwareKeyboard() async throws {
        let e = try E2E(self)
        e.app.launchArguments += ["-XbinNoSoftKeyboard", "YES"]
        e.launch()
        e.ensureWorkspace()
        e.tileAction("apps/welcome", "Terminal here")
        await e.eventually("a shell session on apps/welcome", timeout: 30) {
            try await e.server.sessions(cwd: "apps/welcome").contains { $0.kind != "agent" }
        }
        e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        let up = e.app.buttons["Up arrow"]
        XCTAssertTrue(up.waitForExistence(timeout: 10), "the key row")
        let window = e.app.windows.firstMatch.frame
        let atBottom = { (f: CGRect) in f.maxY <= window.maxY && f.minY >= window.maxY - 100 }
        let docked = e.until(10) { atBottom(up.frame) }
        e.shot("07-terminal-hardware-keyboard")
        XCTAssertTrue(e.softKeyboard() == nil, "no software keyboard")
        XCTAssertTrue(docked, "the row docked at the bottom: \(up.frame) in \(window)")
        for label in E2E.rowKeys {
            let key = e.app.buttons[label]
            print("xbin-e2e: key \(label) \(key.frame), window \(window)")
            XCTAssertTrue(key.exists && key.isHittable, "the key row's \(label)")
            XCTAssertTrue(atBottom(key.frame), "\(label) at the bottom: \(key.frame) in \(window)")
        }
        e.app.typeText("printf '\\033]0;xbin-e2e-hw-%d\\007' $((40+2))\n")
        XCTAssertTrue(e.element("xbin-e2e-hw-42").waitForExistence(timeout: 30), "typed keys reach the shell")
        e.app.typeText("printf '\\033]0;xbin-e2e-other\\007'\n")
        XCTAssertTrue(e.element("xbin-e2e-other").waitForExistence(timeout: 30), "the second title")
        XCTAssertTrue(e.until(10) { !e.element("xbin-e2e-hw-42").exists }, "the first title gone")
        up.tap()
        up.tap()
        e.app.typeText("\n")
        XCTAssertTrue(e.element("xbin-e2e-hw-42").waitForExistence(timeout: 30), "↑ ↑ from the key row recalled the first printf")
        e.shot("07-terminal-hardware-keyboard-keys")
        for s in try await e.server.sessions(cwd: "apps/welcome") where s.kind != "agent" {
            await e.server.end(s.id)
        }
    }
}
