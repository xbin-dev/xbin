#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// The workspace agent's approvals the app decides as the web does (D253):
/// a tile agent's start picked on the sheet — Coding agent, Model, Mode,
/// where each came from, Remember for this tile, the mode a fact and a
/// bypass mode warned; a follow-up that continues the agent's own session
/// (Send) or starts a new one; a message to a tile agent shown whole, Send
/// only once all of its text has been on screen; Send from Needs you for a
/// follow-up its row shows whole; and Try again on a start that didn't open.
/// Demo `followups` (App/Model/AgentDemo.swift: the start-words corpus, as
/// xbind would answer it — it holds an Allow to all of a message's text as
/// xbind does). Screenshots: `followups-NN-<what>.png` from the tests,
/// `followups-<light|dark>-NN-<what>.png` from the gallery.
final class XbinWorkspaceAgentFollowupsTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    private func launch(_ extra: [String] = []) throws -> E2E {
        let e = try E2E(self)
        e.wsDemo("followups")
        e.app.launchArguments += ["-XbinAgentGate", "allow"] + extra
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        return e
    }

    /// A tile agent's start with xbind's choices: what xbind preselected and
    /// where each came from; a pick of the person's own says so, ticks
    /// Remember, and changes what the sheet says of the session — a coding
    /// agent's own defaults, ITS sign-in line; a bypass mode is warned.
    @MainActor
    func test01StartPickers() throws {
        let e = try launch()
        e.fuOpen("rq_st")
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "the start's sheet")
        XCTAssertEqual(heading.label, "Start a tile agent in Eui?")
        let harness = e.fuPick("harness"), model = e.fuPick("model"), mode = e.fuPick("mode")
        e.expect(harness, 10, "the Coding agent picker")
        XCTAssertEqual(harness.label, "Coding agent")
        XCTAssertEqual(harness.value as? String, "Claude Code")
        XCTAssertEqual(e.element("approval-from-harness").label, "remembered for this tile", "where it came from, its own element")
        XCTAssertEqual(model.value as? String, "its default")
        XCTAssertEqual(e.element("approval-from-model").label, "the default")
        XCTAssertTrue(e.element("approval-note-model").label.hasPrefix("Claude Code's own list shows in its session once it runs"))
        XCTAssertEqual(mode.value as? String, "Auto, acts without asking")
        XCTAssertEqual(e.element("approval-mode-chip").label, "acts without asking", "the mode a fact, a chip")
        XCTAssertTrue(e.element("approval-mode-fact").label.hasPrefix("It edits files and runs commands in this tile's sandbox without asking you."))
        let remember = e.app.buttons.matching(identifier: "approval-remember").firstMatch
        XCTAssertTrue(remember.exists && !remember.isSelected, "Remember for this tile: unticked until the person picks")
        XCTAssertTrue(e.element("approval-signin").label.contains("allow it on the web"), "the sign-in line stays the web's (D230): \(e.element("approval-signin").label)")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertEqual(allow.label, "Start")
        // the pickers lead, the tile after them (the web's order)
        let tile = e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Tile,")).firstMatch
        XCTAssertLessThan(mode.frame.minY, tile.frame.minY, "the mode before the tile")
        XCTAssertFalse(e.element("approval-from-harness").label.contains("·") || e.element("approval-mode-chip").label.contains("·"), "no “·” in what is new")
        e.shot("followups-01-start")

        // the person's own model: theirs, and offered to remember
        e.fuChoose(model, "opus")
        XCTAssertTrue(e.until(10) { model.value as? String == "opus" }, "the model picked: \(model.value ?? "")")
        XCTAssertEqual(e.element("approval-from-model").label, "your pick")
        XCTAssertTrue(e.until(5) { remember.isSelected }, "a pick of their own ticks Remember")
        // another coding agent: its own default model and mode — the defaults, not the person's — and no sign-in line of Claude Code's
        e.fuChoose(harness, "Codex")
        XCTAssertTrue(e.until(10) { harness.value as? String == "Codex" })
        XCTAssertEqual(e.element("approval-from-harness").label, "your pick")
        XCTAssertEqual(model.value as? String, "its default")
        XCTAssertEqual(e.element("approval-from-model").label, "the default")
        XCTAssertEqual(mode.value as? String, "Approve for me, acts without asking")
        XCTAssertFalse(e.element("approval-signin").exists, "Codex runs on the workspace's model: no sign-in to offer")
        // a bypass mode the person picks: picked, and warned
        e.fuChoose(mode, "Full access, skips every check")
        XCTAssertTrue(e.until(10) { mode.value as? String == "Full access, skips every check" })
        let warn = e.element("approval-mode-warn")
        e.expect(warn, 10, "a bypass mode is warned")
        XCTAssertTrue(warn.label.hasPrefix("Full access skips every check Codex has of its own"), warn.label)
        XCTAssertTrue(remember.label == "Remember for this tile")
        XCTAssertTrue(e.containing("A mode that skips every check isn't remembered").exists, "Remember says what it keeps")
        e.shot("followups-02-start-bypass")
        // Gemini CLI: ITS sign-in line — the saved sign-in it offers, on a tile not only the person's
        e.fuChoose(harness, "Gemini CLI")
        XCTAssertTrue(e.until(10) { e.element("approval-signin").exists }, "the sign-in line of the coding agent picked")
        let signin = e.element("approval-signin").label
        XCTAssertTrue(signin.contains("Gemini CLI") && signin.contains("trust line") && signin.contains("bob"), signin)
        // Start: decided, and the next that waits shows (one at a time)
        XCTAssertTrue(e.until(15) { allow.isEnabled }, "Start arms a moment after the last pick")
        allow.tap()
        XCTAssertTrue(e.until(20) { heading.exists && heading.label != "Start a tile agent in Eui?" }, "decided: the next one shows (\(heading.label))")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
    }

    /// A follow-up into the agent's own idle session: continuing is the
    /// first choice — the session's facts as chips, the sign-in it carries
    /// named, Send — and "Start a new session instead" shows the pickers.
    @MainActor
    func test02Continue() throws {
        let e = try launch()
        e.fuOpen("rq_st_cont")
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "the follow-up's sheet")
        XCTAssertEqual(heading.label, "Continue the session in Eui?")
        let cont = e.app.buttons.matching(identifier: "approval-continue").firstMatch
        let anew = e.app.buttons.matching(identifier: "approval-new-session").firstMatch
        e.expect(cont, 10, "the session it continues")
        XCTAssertTrue(cont.isSelected, "continuing is the first choice")
        for fact in ["Continue the Claude Code session from", "Claude Code", "model opus", "acts without asking", "uses your Claude Code sign-in", "Work"] {
            XCTAssertTrue(cont.label.contains(fact), "“\(fact)”: \(cont.label)")
        }
        XCTAssertEqual(anew.label, "Start a new session instead")
        XCTAssertFalse(e.fuPick("harness").exists, "the session keeps its own: no pickers")
        XCTAssertFalse(e.element("approval-signin").exists, "no sign-in tick for a continue (D251)")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertEqual(allow.label, "Send")
        e.shot("followups-03-continue")

        anew.tap()
        XCTAssertTrue(e.until(10) { anew.isSelected && e.fuPick("harness").exists }, "a new session: the pickers")
        XCTAssertEqual(heading.label, "Start a tile agent in Eui?")
        XCTAssertEqual(allow.label, "Start")
        XCTAssertEqual(e.fuPick("harness").value as? String, "Claude Code")
        XCTAssertEqual(e.element("approval-from-harness").label, "your preference")
        e.shot("followups-04-continue-new-session")
        cont.tap()
        XCTAssertTrue(e.until(10) { cont.isSelected && allow.label == "Send" }, "back to continuing")
        XCTAssertTrue(e.until(15) { allow.isEnabled })
        allow.tap()
        XCTAssertTrue(e.until(20) { heading.exists && heading.label != "Continue the session in Eui?" }, "sent: the next one shows (\(heading.label))")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
    }

    /// A message to a tile agent: the session's facts, the whole text, Send
    /// — only once all of the text has been on screen: a short one at once,
    /// a long one once its end was in view, a clipped one after Show all and
    /// its end. The demo holds Send to all of the text as xbind does.
    @MainActor
    func test03Messages() throws {
        let e = try launch()
        let heading = e.element("approval-heading")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        let later = e.app.buttons.matching(identifier: "approval-later").firstMatch
        let note = e.element("approval-note")

        e.fuOpen("rq_m")
        e.expect(heading, 30, "a short message")
        XCTAssertEqual(heading.label, "Send this message to the OpenCode session in eui?")
        let agent = e.element("approval-message-agent")
        for fact in ["Tile agent", "OpenCode", "mode build", "acts without asking", "model xbin/small"] {
            XCTAssertTrue(agent.label.contains(fact), "“\(fact)”: \(agent.label)")
        }
        XCTAssertTrue(e.element("approval-message").label.contains("Fix the stray O in the corner."), "the whole text")
        XCTAssertEqual(allow.label, "Send")
        XCTAssertTrue(e.until(15) { allow.isEnabled }, "a short text: all of it is on screen, Send arms")
        XCTAssertFalse(note.exists)
        e.shot("followups-05-message")
        later.tap()
        XCTAssertTrue(e.until(10) { !heading.exists })

        // a long one: Send waits for its end
        e.fuOpen("rq_m_lines")
        e.expect(heading, 30, "a long message")
        let details = e.app.buttons.matching(identifier: "approval-details").firstMatch
        if e.until(4, { allow.isEnabled }) {
            // armed at once only where all of the text shows (an iPad's sheet): its end above the decision bar
            let text = e.element("approval-message")
            XCTAssertTrue(text.frame.minY >= 0 && text.frame.maxY <= allow.frame.minY + 1, "Send armed with the text's end off screen: \(text.frame) / \(allow.frame)")
        } else {
            XCTAssertEqual(note.label, "Scroll to the end of the text to allow it")
            e.shot("followups-06-message-long-top")
            e.fuScrollTo(details)
            XCTAssertTrue(e.until(15) { allow.isEnabled }, "its end seen: Send arms")
        }
        e.shot("followups-07-message-long-end")
        allow.tap()
        XCTAssertTrue(e.until(20) { !heading.exists || !heading.label.hasPrefix("Send this message") || e.app.buttons.matching(identifier: "approval-message-show-all").firstMatch.exists },
                      "sent — the demo took all of it (\(heading.label))")
        XCTAssertFalse(e.containing("not all of the text").exists, "xbind's rule held: all of it was on screen")
        if heading.exists { later.tap() }
        XCTAssertTrue(e.until(10) { !heading.exists })

        // a clipped one: Show all, then its end
        e.fuOpen("rq_m_clip")
        e.expect(heading, 30, "a clipped message")
        let showAll = e.app.buttons.matching(identifier: "approval-message-show-all").firstMatch
        e.fuScrollTo(showAll)
        XCTAssertEqual(note.label, "Show all of the text to allow it")
        XCTAssertFalse(allow.isEnabled, "never before Show all")
        e.shot("followups-08-message-clipped")
        showAll.tap()
        e.fuScrollTo(details)
        XCTAssertTrue(e.until(15) { allow.isEnabled }, "all of it, and its end: Send arms")
        allow.tap()
        XCTAssertTrue(e.until(20) { !heading.exists || !e.containing("é😀ab").exists }, "sent")
        XCTAssertFalse(e.containing("not all of the text").exists)
        if heading.exists { later.tap() }
    }

    /// Needs you: a follow-up whose text its row shows whole is sent from
    /// the row (Send armed a moment after the list shows); a longer one is
    /// Review…; a start that didn't open is Try again, its sheet with the
    /// earlier picks and Try again.
    @MainActor
    func test04RowsAndTryAgain() throws {
        let e = try launch(["-XbinAgentArmSeconds", "4"])
        let needs = e.app.buttons["Needs you"]
        e.expect(needs, 60, "Home's Needs you")
        needs.tap()
        let send = e.app.buttons.matching(identifier: "inbox-send:rq_m").firstMatch
        e.expect(send, 30, "a short message's Send on its row")
        XCTAssertFalse(send.isEnabled, "not armed when the list shows")
        XCTAssertTrue(e.element("inbox-text:rq_m").label.contains("Fix the stray O in the corner."), "all of its text on the row")
        XCTAssertTrue(e.app.buttons.matching(identifier: "inbox-send:rq_st_cont").firstMatch.exists, "a follow-up that continues a session, likewise")
        // (the oldest first: the start that didn't open leads)
        let retry = e.app.buttons.matching(identifier: "inbox-retry:rq_st_failed").firstMatch
        XCTAssertTrue(retry.exists, "the start that didn't open")
        XCTAssertTrue(retry.label.contains("Didn't start: Start a tile agent in Eui") && retry.label.contains("Try again"), retry.label)
        e.shot("followups-09-needs-you")
        XCTAssertTrue(e.until(20) { send.isEnabled }, "it arms")
        send.tap()
        XCTAssertTrue(e.until(20) { !send.exists }, "sent from the row: it no longer waits")
        // a long message: its row is Review… (the sheet), no Send
        let long = e.app.buttons.matching(identifier: "inbox-request:rq_m_lines").firstMatch
        e.fuReveal(long, up: false)
        XCTAssertFalse(e.app.buttons.matching(identifier: "inbox-send:rq_m_lines").firstMatch.exists, "no Send for a text its row can't show whole")
        e.shot("followups-10-needs-you-long")

        // Try again: the sheet, the earlier picks, never the sign-in
        e.fuReveal(retry, up: true)
        retry.tap()
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "the Try again sheet")
        XCTAssertEqual(heading.label, "Try again to start a tile agent in Eui?")
        XCTAssertEqual(e.fuPick("harness").value as? String, "Codex", "the earlier pick")
        XCTAssertEqual(e.fuPick("model").value as? String, "xbin/big")
        XCTAssertEqual(e.fuPick("mode").value as? String, "Ask for approval, asks before acting")
        XCTAssertEqual(e.element("approval-mode-chip").label, "asks before acting")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertEqual(allow.label, "Try again")
        e.shot("followups-11-try-again")
        XCTAssertTrue(e.until(20) { allow.isEnabled })
        allow.tap()
        XCTAssertTrue(e.until(20) { !heading.exists || heading.label != "Try again to start a tile agent in Eui?" }, "tried again")
    }

    /// The whole-text rule at the largest accessibility text size: a long
    /// message's Send stays unavailable until the end of its text has been
    /// in view — scrolled to — and is there once it has (where the sheet
    /// shows all of it at once, its end is above the decision bar).
    @MainActor
    func test06LargestTextLongMessage() throws {
        let e = try launch(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
        e.fuOpen("rq_m_lines")
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "a long message at the largest text size")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        let details = e.app.buttons.matching(identifier: "approval-details").firstMatch
        if e.until(4, { allow.isEnabled }) {
            let text = e.element("approval-message")
            XCTAssertTrue(text.frame.minY >= 0 && text.frame.maxY <= allow.frame.minY + 1, "Send armed with the text's end off screen: \(text.frame) / \(allow.frame)")
        } else {
            XCTAssertEqual(e.element("approval-note").label, "Scroll to the end of the text to allow it")
            e.shot("followups-12-largest-long-top")
            XCTAssertFalse(e.until(3) { allow.isEnabled }, "still not, with the end out of view")
            e.fuScrollTo(details)
            XCTAssertTrue(e.until(15) { allow.isEnabled }, "its end seen: Send arms")
        }
        e.shot("followups-13-largest-long-end")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
    }

    /// A message that fits without scrolling: Send waits only for the arming
    /// delay — its end was in view from the first moment — with no scroll.
    @MainActor
    func test07ShortMessageArmsWithoutScrolling() throws {
        let e = try launch(["-XbinAgentArmSeconds", "4"])
        e.fuOpen("rq_m")
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "a short message")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertFalse(allow.isEnabled, "not before the arming delay")
        XCTAssertTrue(e.until(15) { allow.isEnabled }, "armed, without a scroll: all of the text was on screen from the start")
        XCTAssertFalse(e.element("approval-note").exists, "nothing holds it back")
        e.shot("followups-14-short-armed")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
    }

    /// The sheets and the rows, light and dark, at the size the device has:
    /// `followups-<light|dark>-NN-<what>.png`.
    @MainActor
    func test05Gallery() throws {
        for mode in ["light", "dark"] {
            var n = 0
            let e = try launch(["-XbinAppearance", mode])
            func shot(_ name: String) {
                n += 1
                e.shot(String(format: "followups-%@-%02d-%@", mode, n, name))
            }
            let heading = e.element("approval-heading")
            let later = e.app.buttons.matching(identifier: "approval-later").firstMatch
            func close() {
                later.tap()
                XCTAssertTrue(e.until(10) { !heading.exists })
            }
            e.fuOpen("rq_st")
            e.expect(e.fuPick("harness"), 30, "the start's pickers")
            shot("start")
            e.fuChoose(e.fuPick("harness"), "Gemini CLI")
            XCTAssertTrue(e.until(10) { e.element("approval-signin").exists })
            shot("start-picked")
            close()
            e.fuOpen("rq_st_cont")
            e.expect(e.app.buttons.matching(identifier: "approval-continue").firstMatch, 30, "the continue")
            shot("continue")
            e.app.buttons.matching(identifier: "approval-new-session").firstMatch.tap()
            XCTAssertTrue(e.until(10) { e.fuPick("harness").exists })
            shot("continue-new-session")
            close()
            e.fuOpen("rq_m")
            e.expect(e.element("approval-message"), 30, "a short message")
            shot("message-short")
            close()
            e.fuOpen("rq_m_lines")
            e.expect(e.element("approval-message"), 30, "a long message")
            shot("message-long")
            close()
            e.fuOpen("rq_m_clip")
            e.expect(e.element("approval-message"), 30, "a clipped message")
            shot("message-clipped")
            close()
            e.app.buttons["Needs you"].tap()
            e.expect(e.app.buttons.matching(identifier: "inbox-send:rq_m").firstMatch, 30, "the rows")
            shot("needs-you")
            e.app.buttons["Done"].tap()
            e.app.terminate()
        }
    }
}

extension E2E {
    /// Home's Needs you → request `id`'s sheet: its row's Review… (a
    /// follow-up allowed on its row has its own Review… button).
    func fuOpen(_ id: String, file: StaticString = #filePath, line: UInt = #line) {
        let needs = app.buttons["Needs you"]
        expect(needs, 60, "Home's Needs you row", file: file, line: line)
        needs.tap()
        let row = app.buttons.matching(identifier: "inbox-request:\(id)").firstMatch
        let review = app.buttons.matching(identifier: "inbox-review:\(id)").firstMatch
        var swipes = 0
        while !(row.exists || review.exists), swipes < 10 {
            _ = row.waitForExistence(timeout: swipes == 0 ? 4 : 1)
            if row.exists || review.exists { break }
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
            from.press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35)))
            swipes += 1
        }
        let target = row.exists ? row : review
        expect(target, 5, "the inbox's \(id)", file: file, line: line)
        wsScrollTo(target)
        target.tap()
    }

    /// Scrolls the sheet in front until `element` can be tapped — far: a
    /// message of 4,000 characters is many screens on a phone. The drags
    /// stay inside the sheet's scrolling part, between its trusted bar and
    /// its decision bar (on an iPad the sheet is a card in the middle).
    func fuScrollTo(_ element: XCUIElement, file: StaticString = #filePath, line: UInt = #line) {
        let top = self.element("approval-bar"), deny = app.buttons.matching(identifier: "approval-deny").firstMatch
        let note = self.element("approval-note")
        let window = app.windows.firstMatch.frame
        var tries = 0
        while element.exists, !element.isHittable, tries < 60 {
            let x = top.exists ? top.frame.midX : window.midX
            // the decision bar's top: its note (what holds Allow back) when it has one, else Deny
            let barTop = min(deny.exists ? deny.frame.minY : window.height, note.exists ? note.frame.minY : window.height)
            var from = barTop < window.height ? barTop - 24 : window.height * 0.7
            var to = top.exists ? top.frame.maxY + 24 : window.height * 0.2
            if from - to < 40 { from = window.height * 0.7; to = window.height * 0.2 } // (no room: the old way)
            let origin = app.coordinate(withNormalizedOffset: CGVector(dx: 0, dy: 0))
            origin.withOffset(CGVector(dx: x, dy: from)).press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: x, dy: to)))
            tries += 1
        }
        XCTAssertTrue(element.exists && element.isHittable, "\(element) can be scrolled into reach", file: file, line: line)
    }

    /// Scrolls the list in front (Needs you: a lazy list) until `element`
    /// is there and can be tapped — down the list, or back `up`.
    func fuReveal(_ element: XCUIElement, up: Bool, file: StaticString = #filePath, line: UInt = #line) {
        var tries = 0
        while !(element.exists && element.isHittable), tries < 12 {
            let a = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.35 : 0.7)), b = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.7 : 0.35))
            a.press(forDuration: 0.05, thenDragTo: b)
            tries += 1
        }
        XCTAssertTrue(element.exists && element.isHittable, "\(element) in the list", file: file, line: line)
    }

    /// One of a start's pickers on the sheet.
    func fuPick(_ id: String) -> XCUIElement { app.buttons.matching(identifier: "approval-pick-\(id)").firstMatch }

    /// Opens `picker`'s menu and picks `option`.
    func fuChoose(_ picker: XCUIElement, _ option: String, file: StaticString = #filePath, line: UInt = #line) {
        wsScrollTo(picker)
        picker.tap()
        var item = app.buttons.matching(NSPredicate(format: "label == %@", option)).firstMatch
        _ = until(10) {
            // (the menu's item, not the picker that shows the same words behind it)
            if let hit = app.buttons.matching(NSPredicate(format: "label == %@", option)).allElementsBoundByIndex.last(where: { $0.isHittable }) { item = hit; return true }
            return false
        }
        expect(item, 10, "the option “\(option)”", file: file, line: line)
        item.tap()
    }
}
