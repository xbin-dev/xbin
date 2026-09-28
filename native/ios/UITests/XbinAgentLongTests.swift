import XCTest

/// A long agent conversation (D130, E3) on a simulator against the e2e
/// xbind: the Agent screen opens on the tail page, loads older pages as the
/// reader scrolls up without moving what they read, unloads what is far
/// below, and the "↓ N new — jump to latest" pill brings them back to the
/// newest. The fake agent's `long N` writes N units (a markdown message and
/// an edit card each, a thought every tenth): 300 units ≈ 630 rows. Small
/// pages and a small unload margin (Debug launch arguments) make it cross
/// pages and unload within a few screens.
final class XbinAgentLongTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    func testLongTranscriptHoldsItsPlace() async throws {
        let e = try E2E(self)
        let id = try await e.server.startAgent(cwd: "apps/welcome")
        try await e.server.prompt(id, "long 300")
        let done = try await e.server.waitForTurnEnd(id, timeout: 120)
        XCTAssertTrue(done, "the fake agent wrote its 300 units")

        // pages of 30 events (about ten units), 30 rows kept beyond the screen
        e.app.launchArguments += ["-XbinAgentPageLimit", "30", "-XbinAgentKeepMargin", "30"]
        e.launch()
        e.ensureWorkspace()
        e.app.open(URL(string: "xbin://\(e.server.authority)/agent/\(id)")!)
        let last = e.containing("done: 300 units")
        XCTAssertTrue(last.waitForExistence(timeout: 40), "the tail: the agent's last line")
        e.shot("agent-long-tail")

        // Up, a drag at a time with no fling: every drag moves the text under
        // the finger by the drag and no more, pages loading above or not.
        let window = e.app.windows.firstMatch.frame
        let step = (window.height * 0.4).rounded()
        var lowest = 300
        var drags = 0
        var jumps: [String] = []
        while lowest > 270, drags < 45 {
            guard let anchor = e.topUnit(in: window) else {
                // a tall card fills the screen: move on
                e.drag(by: step)
                drags += 1
                continue
            }
            let el = e.unit(anchor.number)
            let before = anchor.minY
            e.drag(by: step)
            drags += 1
            _ = e.until(2) { e.settled(el) }
            let after = el.exists ? el.frame.minY : .infinity
            if abs(after - before - step) > 40 { jumps.append("Unit \(anchor.number): moved \(after - before) for a \(step) pt drag") }
            // a page that lands a moment later must not move it either
            _ = e.until(0.8) { false }
            if el.exists, abs(el.frame.minY - after) > 2 { jumps.append("Unit \(anchor.number): moved \(el.frame.minY - after) at rest") }
            lowest = min(lowest, e.topUnit(in: window)?.number ?? lowest)
            if drags == 6 { e.shot("agent-long-mid-scroll") }
        }
        e.shot("agent-long-older")
        XCTAssertTrue(jumps.isEmpty, "the transcript jumped:\n" + jumps.joined(separator: "\n"))
        XCTAssertTrue(lowest <= 270, "older pages loaded as the reader scrolled up (\(drags) drags reached unit \(lowest))")

        // News while the reader is up there: the pill counts it, and it
        // brings the reader down to it.
        try await e.server.prompt(id, "hello pill")
        let pill = e.app.buttons["transcript-jump"]
        XCTAssertTrue(e.until(30) { pill.exists && pill.label.contains("new") }, "the jump-to-latest pill counts the news (\(pill.exists ? pill.label : "none"))")
        e.shot("agent-long-pill")
        pill.tap()
        let echo = e.containing("echo: hello pill")
        XCTAssertTrue(echo.waitForExistence(timeout: 30), "the newest rows after the jump")
        XCTAssertTrue(e.until(10) { echo.isHittable || echo.frame.maxY <= window.maxY }, "…on screen: \(echo.frame)")
        XCTAssertTrue(pill.waitForNonExistence(timeout: 10), "the pill goes at the bottom")
        e.shot("agent-long-latest")
        await e.server.end(id)
    }
}

extension E2EServer {
    /// `host:port` — how an xbin:// link names this workspace.
    var authority: String {
        let u = URL(string: address)
        return (u?.host ?? "localhost") + (u?.port.map { ":\($0)" } ?? "")
    }

    /// A fake-agent session on a tile (POST /api/xbin/term/sessions); its id.
    func startAgent(cwd: String) async throws -> String {
        let d = try await send("POST", "/api/xbin/term/sessions", json: ["kind": "agent", "cwd": cwd, "provider": "fake"])
        let o = try JSONSerialization.jsonObject(with: d) as? [String: Any]
        return try XCTUnwrap(o?["id"] as? String, "the new session's id")
    }

    /// Sends a prompt (the server waits for the agent's handshake).
    func prompt(_ id: String, _ text: String) async throws {
        _ = try await send("POST", "/api/xbin/term/sessions/\(id)/prompt", json: ["text": text])
    }

    /// Waits until the session's log holds a turn.end (the tail page's last events).
    func waitForTurnEnd(_ id: String, timeout: TimeInterval) async throws -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let d = try await send("GET", "/api/xbin/term/sessions/\(id)/events?limit=5")
            let evs = (try JSONSerialization.jsonObject(with: d) as? [String: Any])?["events"] as? [[String: Any]] ?? []
            if evs.contains(where: { $0["type"] as? String == "turn.end" }) { return true }
            try await Task.sleep(for: .milliseconds(500))
        }
        return false
    }
}

extension E2E {
    /// A `long` unit's heading ("Unit N").
    func unit(_ n: Int) -> XCUIElement { app.staticTexts["Unit \(n)"] }

    /// The topmost unit heading in the upper part of `window` (below the
    /// bars): its number and where it is. One snapshot of the whole screen.
    func topUnit(in window: CGRect) -> (number: Int, minY: CGFloat)? {
        guard let snap = try? app.snapshot() else { return nil }
        var best: (number: Int, minY: CGFloat)?
        func walk(_ s: any XCUIElementSnapshot) {
            if s.elementType == .staticText, s.label.hasPrefix("Unit "), let n = Int(s.label.dropFirst(5)) {
                let f = s.frame
                if f.minY > window.minY + 120, f.maxY < window.midY, best == nil || f.minY < best!.minY { best = (n, f.minY) }
            }
            for c in s.children { walk(c) }
        }
        walk(snap)
        return best
    }

    /// Drags the transcript down by `points` (scrolling up), holding still
    /// before lifting: no fling.
    func drag(by points: CGFloat) {
        let w = app.windows.firstMatch
        let h = w.frame.height
        let start = w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
        let end = start.withOffset(CGVector(dx: 0, dy: min(points, h * 0.6)))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.3)
    }

    /// Whether an element's frame holds still (two reads 0.2 s apart).
    func settled(_ el: XCUIElement) -> Bool {
        guard el.exists else { return true }
        let a = el.frame
        _ = app.otherElements["xbin-e2e-never"].waitForExistence(timeout: 0.2)
        return el.exists && el.frame == a
    }
}
