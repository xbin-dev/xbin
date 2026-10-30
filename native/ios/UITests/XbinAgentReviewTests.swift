import XCTest

/// A turn's changes reviewed a file at a time (D210, #45) on a simulator
/// against the e2e xbind. The fake agent's `run:` writes 30 files of 300
/// lines and one of 3000 into apps/welcome — far past the 192 KiB patch a
/// turn's event carries. The transcript shows the turn's card folded; open,
/// its first files and "All 31 files"; a file opens the review at its diff
/// (numbered, the first 400 lines, then "Show all"), next and previous
/// files at the bottom, the list behind Back; the shell call's own card
/// reviews the same way. Swipes through the diff are timed (XCUITest waits
/// for the app to idle after each: a busy main thread shows as a slow
/// swipe) and logged as `xbin-e2e: swipe …`.
final class XbinAgentReviewTests: XCTestCase {
    // (Back in the review: XbinNavigationTests' tapNavBack, the bar's BackButton)
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    static let gen = "review-gen"
    static let write = "run: mkdir -p \(gen) && for i in $(seq -w 1 30); do seq -f \"line %g of file $i: the quick brown fox jumps over the lazy dog\" 1 300 > \(gen)/f$i.txt; done; "
        + "seq -f \"big line %g: the quick brown fox jumps over the lazy dog\" 1 3000 > \(gen)/zz-big.txt; echo wrote"

    @MainActor
    func testTurnChangesFileByFile() async throws {
        let e = try E2E(self)
        // A snapshot diff needs the tile to be a git repo when its session
        // starts (internal/term/agentdiff.go): one session makes it one.
        let prep = try await e.server.startAgent(cwd: "apps/welcome")
        try await e.server.prompt(prep, "run: test -d .git || git init -q .; echo ready")
        _ = try await e.server.waitForTurnEnd(prep, timeout: 60)
        await e.server.end(prep)
        let id = try await e.server.startAgent(cwd: "apps/welcome")
        try await e.server.prompt(id, Self.write)
        let done = try await e.server.waitForTurnEnd(id, timeout: 120)
        XCTAssertTrue(done, "the fake agent wrote the files")

        e.launch()
        e.ensureWorkspace()
        e.app.open(URL(string: "xbin://\(e.server.authority)/agent/\(id)")!)
        let card = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "This turn changed 31 files")).firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 40), "the turn's changes card")
        XCTAssertEqual(card.value as? String, "collapsed", "folded by default")
        let first = e.app.buttons["review-file:\(Self.gen)/f01.txt"]
        XCTAssertFalse(first.exists, "folded: no file rows, no diff")
        e.shot("review-01-folded")

        card.tap()
        XCTAssertTrue(first.waitForExistence(timeout: 10), "open: the files")
        XCTAssertTrue(e.app.buttons["review-all-files"].exists, "…the first of them, and All 31 files")
        XCTAssertFalse(e.app.buttons["review-file:\(Self.gen)/zz-big.txt"].exists, "…not all 31 in the transcript")
        e.shot("review-02-files")

        // a file the turn's patch holds: its diff, numbered
        first.tap()
        let position = e.app.staticTexts["review-position"]
        XCTAssertTrue(position.waitForExistence(timeout: 20), "the review, at the file")
        XCTAssertEqual(position.label, "1 of 31")
        XCTAssertTrue(e.containing("line 1 of file 01").waitForExistence(timeout: 20), "its lines")
        e.shot("review-03-file")
        e.app.buttons["review-next"].tap()
        XCTAssertTrue(e.until(10) { position.label == "2 of 31" }, "Next: the next file (\(position.label))")
        XCTAssertTrue(e.containing("line 1 of file 02").waitForExistence(timeout: 20), "…its lines")
        e.app.buttons["review-prev"].tap()
        XCTAssertTrue(e.until(10) { position.label == "1 of 31" }, "Previous: back (\(position.label))")

        // Back: every file; the last one, past the patch's cap, from the route
        e.tapNavBack(listShows: e.app.buttons["review-list:\(Self.gen)/f01.txt"], "Back: the change set's files")
        let big = e.app.buttons["review-list:\(Self.gen)/zz-big.txt"]
        for _ in 0..<12 where !big.isHittable { e.app.swipeUp() }
        XCTAssertTrue(big.isHittable, "the list holds every file")
        e.shot("review-04-list")
        big.tap()
        XCTAssertTrue(e.until(10) { position.exists && position.label == "31 of 31" }, "the last file (\(position.label))")
        XCTAssertTrue(e.containing("big line 1:").waitForExistence(timeout: 30), "its diff, loaded from the route")
        XCTAssertFalse(e.app.buttons["review-next"].isEnabled, "no next file after the last")
        e.shot("review-05-big")

        // through its first 400 lines: each swipe returns as the app idles
        let diff = e.app.descendants(matching: .any)["review-diff"]
        let showAll = e.app.buttons["review-show-all"]
        var times: [TimeInterval] = []
        for _ in 0..<40 where !showAll.isHittable {
            let t0 = Date()
            diff.swipeUp(velocity: .fast)
            times.append(Date().timeIntervalSince(t0))
        }
        print("xbin-e2e: swipe times through 400 lines: \(times.map { String(format: "%.2f", $0) }.joined(separator: " "))")
        XCTAssertTrue(showAll.isHittable, "Show all after the first 400 lines")
        XCTAssertTrue(["Show all 3001 lines", "Show all 3,001 lines"].contains(showAll.label), "it counts every line (\(showAll.label))")
        e.shot("review-06-show-all")
        showAll.tap()
        XCTAssertTrue(showAll.waitForNonExistence(timeout: 10), "every line shown")
        let t0 = Date()
        for _ in 0..<10 { diff.swipeUp(velocity: .fast) }
        print("xbin-e2e: 10 swipes past line 400: \(String(format: "%.2f", Date().timeIntervalSince(t0))) s")
        let row = e.app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "big line ")).firstMatch
        let at = row.exists ? Int(row.label.dropFirst(9).prefix { $0.isNumber }) ?? 0 : 0
        XCTAssertTrue(at > 400, "the lines past 400 (on screen: \(row.exists ? row.label : "none"))")
        e.shot("review-07-past-400")

        // wrapped lines
        e.app.buttons["review-menu"].tap()
        e.app.buttons["Wrap lines"].tap()
        e.shot("review-08-wrapped")
        e.app.buttons["review-menu"].tap()
        e.app.buttons["Wrap lines"].tap() // back as it was (the choice is kept)

        // Done: the transcript; the shell call's own card reviews the same way
        e.app.buttons["Done"].tap()
        XCTAssertTrue(card.waitForExistence(timeout: 10), "back in the transcript")
        // (its headline: "Create review-gen && for i in …")
        let call = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@ AND value == %@", "Create review-gen", "collapsed")).firstMatch
        XCTAssertTrue(call.waitForExistence(timeout: 10), "the shell call's card, folded")
        call.tap()
        let rows = e.app.buttons.matching(identifier: "review-file:\(Self.gen)/f12.txt")
        XCTAssertTrue(e.until(10) { rows.count >= 1 && rows.element(boundBy: 0).isHittable }, "the call's files")
        e.shot("review-09-call")
        rows.element(boundBy: 0).tap() // the call's (above the turn's): past its 64 KiB, by toolCallId
        XCTAssertTrue(e.until(20) { position.exists && position.label == "12 of 31" }, "the call's review, at the file (\(position.label))")
        XCTAssertTrue(e.containing("line 1 of file 12").waitForExistence(timeout: 30), "its lines")
        e.shot("review-10-call-file")
        e.app.buttons["Done"].tap()

        try await e.server.prompt(id, "run: rm -rf \(Self.gen) .git; echo gone")
        _ = try await e.server.waitForText(id, "run: gone", timeout: 60)
        await e.server.end(id)
    }
}

extension E2EServer {
    /// Waits until the session's latest events mention `text`.
    func waitForText(_ id: String, _ text: String, timeout: TimeInterval) async throws -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let d = try await send("GET", "/api/xbin/term/sessions/\(id)/events?limit=20")
            if String(decoding: d, as: UTF8.self).contains(text) { return true }
            try await Task.sleep(for: .milliseconds(500))
        }
        return false
    }
}
