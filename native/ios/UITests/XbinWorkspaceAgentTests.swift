#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// The workspace agent in the app (D233) end to end: the e2e xbind with a
/// workspace agent set up (`XBIN_E2E_WSAGENT=1`: e2e-xbind.sh — isolated,
/// hack/fakeopenai as the LLM connector's upstream, the scripted coding
/// agent, apps/note with a backend an agent may write), the app's own chrome
/// for it and the agent's native view inside that chrome.
///
/// The model is scripted by the message itself: a sentence of
/// hack/fakeopenai's phrasebook (wsphrases.go — `Set the note in apps/note
/// to "…"`, `Start a tile agent in apps/note to …`) makes the tool calls a
/// model would and ends with an answer a person reads, so the conversation
/// on screen is one a person would see. The tests send such messages
/// through the agent's own API with its page's credential — as its view
/// would — rather than typing on the software keyboard, and check what
/// happened with the e2e account's own session, as the chrome reads it.
///
/// - test01: Home's row and the bar's button (the trailing-most item on
///   Home, on a screen and on a tile); the sheet; opened from a tile, the
///   composer's "About …" chip.
/// - test02: a request's card (drawn by the app) → Review… → the native
///   approval: xbind's reading, the agent's words labelled unchecked, Allow
///   not armed at first, Allow once → the card settles and the note changed.
/// - test03: Deny → "Denied by you", nothing changed.
/// - test04: the wider grant's second sheet — Go back approves nothing; the
///   Face ID gate refusing approves nothing; allowing makes a standing
///   approval, listed under the shield → Approvals, taken back there.
/// - test05: a tile agent's start: the mode leads, Start → the session's
///   card → Open → the app's own agent screen for that session.
/// - test06: Needs you counts the request and lists it with Review….
/// - test07: the connection line → the scope panel.
/// - test08: Pause from the shield menu → the paused bar with Resume, no
///   composer, the button says paused → Resume.
/// - test09: an iPad: the agent as a trailing column beside the panels, an
///   approval over it.
/// - test11: after a decision the next request shows as a sheet of its
///   own: Once, Allow unarmed.
/// - test12: a screen the view pushes (the notebook) and back: the cards
///   are still the app's and Review… still opens the approval.
/// - test10Gallery: the screens in light and dark,
///   `wsagent-<light|dark>-NN-<screen>.png`.
///
/// Every test starts from a quiet agent: nothing pending, not paused, no
/// standing approvals, no tile agents it started, and a session of its own.
final class XbinWorkspaceAgentTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_WSAGENT"] != "1",
                      "XBIN_E2E_WSAGENT unset: the e2e xbind has no workspace agent (e2e-xbind.sh)")
    }

    @MainActor
    func test01HomeRowButtonAndContext() async throws {
        let e = try E2E(self)
        _ = try await e.server.wsPrepare("E2E entry")
        try await e.server.seedScreens()
        e.launch()
        e.ensureWorkspace()
        e.goHome()

        // Home: the row with the agent's status, and the bar's button
        let row = e.wsHomeRow
        e.expect(row, 60, "Home's Workspace agent row")
        XCTAssertTrue(e.until(20) { (row.value as? String ?? "").contains("idle") }, "its status, idle: \(row.value ?? "")")
        e.wsExpectButtonTrailingMost("on Home")
        e.shot("wsagent-01-home")

        // the row opens the agent as a sheet, large: its bar by the top of the screen
        XCTAssertTrue(e.wsHittable(row), "the row can be tapped")
        row.tap()
        e.expect(e.wsClose, 30, "the agent's sheet")
        e.expect(e.wsComposer, 60, "the agent's conversation with its composer")
        let window = e.app.windows.firstMatch.frame
        if !e.isPad {
            XCTAssertLessThan(e.wsClose.frame.minY, window.height * 0.2, "the sheet opens large")
        }
        XCTAssertFalse(e.containing("About ").exists, "opened from Home: no context chip")
        e.shot("wsagent-02-sheet-large")
        e.wsClose.tap()
        XCTAssertTrue(e.until(10) { !e.wsClose.exists }, "the close button closes it")

        // the button is on a screen's bar too, where the agent is offered that screen
        e.openScreen()
        e.expect(e.card("apps/counter"), 30, "the screen's cards")
        e.wsExpectButtonTrailingMost("on a screen")
        e.wsButton.tap()
        e.expect(e.wsClose, 30, "the agent's sheet over the screen")
        e.expect(e.containing("About the \(E2EServer.screenName)"), 60, "the composer's context chip names the screen")
        e.wsClose.tap()
        XCTAssertTrue(e.until(10) { !e.wsClose.exists })

        // opened from Home again: nothing is offered (the screen's chip is not kept)
        e.goHome()
        XCTAssertTrue(e.wsHittable(row), "Home's row again")
        row.tap()
        e.expect(e.wsComposer, 60, "the agent again")
        XCTAssertFalse(e.until(3) { e.containing("About ").exists }, "opened from Home: no chip left over")
        e.wsClose.tap()
        XCTAssertTrue(e.until(10) { !e.wsClose.exists })

        // …and on a tile's bar, where the agent is offered that tile
        e.openTile("apps/counter")
        e.expect(e.backButton, 30, "the counter's panel")
        e.expect(e.app.buttons["+1"], 60, "the counter's native view")
        e.wsExpectButtonTrailingMost("on a tile")
        e.wsButton.tap()
        e.expect(e.wsClose, 30, "the agent's sheet over the tile")
        let chip = e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@ AND label CONTAINS[c] %@", "About", "counter")).firstMatch
        e.expect(chip, 60, "the composer's context chip names the tile")
        XCTAssertFalse(e.containing("About the \(E2EServer.screenName)").exists, "…and no longer the screen")
        e.shot("wsagent-03-context-chip")
        e.wsClose.tap()
    }

    @MainActor
    func test02RequestAllowOnce() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E allow once")
        let before = try await e.server.wsNote()
        let ref = try await e.server.wsFileNoteRequest(run, note: "allowed once")
        // Allow's arming stretched, so the test sees it unarmed for certain
        e.app.launchArguments += ["-XbinAgentArmSeconds", "8"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()

        // the card in the conversation is the app's, from xbind's reading of the id
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card with Review…")
        XCTAssertTrue(e.containing("Waiting for your approval").exists, "the card says it waits")
        XCTAssertTrue(e.containing("Approve a change to Note").exists, "…and for what")
        e.shot("wsagent-04-request-card")
        review.tap()

        // the app's sheet: what xbind read of the request
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "the approval sheet")
        let appeared = Date()
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        e.expect(allow, 10, "its Allow button")
        XCTAssertFalse(allow.isEnabled, "Allow is not armed when the sheet appears (\(Date().timeIntervalSince(appeared)) s in)")
        XCTAssertTrue(heading.label.contains("change something in Note"), "the heading: \(heading.label)")
        XCTAssertTrue(e.containing("xbin approval, asking").exists, "the bar says whose sheet it is")
        XCTAssertTrue(e.containing("PUT /api/apps/note/note").exists, "the exact call")
        XCTAssertTrue(e.containing("\"allowed once\"").exists, "what is sent")
        XCTAssertTrue(e.containing("xbin can't undo this").exists, "what xbin says about undoing it")
        XCTAssertTrue(e.containing("not checked by xbin").exists, "the agent's words are labelled unchecked")
        XCTAssertTrue(e.containing("Update the note").exists, "…and quoted")
        // every control says what it does
        XCTAssertEqual(review.label, "Review: Approve a change to Note", "the card's button says what it reviews")
        XCTAssertEqual(e.app.buttons.matching(identifier: "approval-deny").firstMatch.label, "Deny")
        // what widens the grant is no more armed than Allow; Once is never held
        let onceChoice = e.app.buttons.matching(identifier: "approval-choice-once").firstMatch
        let callChoice = e.app.buttons.matching(identifier: "approval-choice-call").firstMatch
        XCTAssertTrue(onceChoice.isEnabled && !callChoice.isEnabled, "the standing choices are not armed when the sheet appears")
        XCTAssertEqual(allow.label, "Allow once", "Once is the choice the sheet opens with")
        // Deny left, Allow right, at the bottom
        let deny = e.app.buttons.matching(identifier: "approval-deny").firstMatch
        XCTAssertLessThan(deny.frame.midX, allow.frame.midX, "Deny left, Allow right")
        XCTAssertGreaterThan(allow.frame.midY, e.app.windows.firstMatch.frame.height * 0.75, "the buttons are at the bottom")
        e.shot("wsagent-05-approval")
        // a tap before it is armed allows nothing
        allow.tap()
        let pendingAfterEarlyTap = try await e.server.wsRequestState(ref)
        XCTAssertEqual(pendingAfterEarlyTap, "pending", "a tap on the unarmed Allow approved nothing")
        XCTAssertTrue(heading.exists, "…and the sheet is still there")
        XCTAssertTrue(e.until(15) { allow.isEnabled }, "Allow arms a moment later")
        XCTAssertGreaterThanOrEqual(Date().timeIntervalSince(appeared), 0.6, "never before 600 ms")
        allow.tap()

        XCTAssertTrue(e.until(20) { !heading.exists }, "the sheet goes after the decision")
        await e.eventually("the note changed, as the person's agent", timeout: 30) {
            let seen = try await e.server.wsNote()
            return seen.contains("note=\"allowed once\"") && seen.contains("agent=xbin/wsagent")
        }
        XCTAssertFalse(before.contains("allowed once"))
        e.expect(e.containing("Allowed"), 30, "the card settles to what came of it")
        XCTAssertTrue(e.until(10) { !review.exists }, "…with no Review… any more")
        e.shot("wsagent-06-request-settled")
    }

    @MainActor
    func test03Deny() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E deny")
        let before = try await e.server.wsNote()
        let ref = try await e.server.wsFileNoteRequest(run, note: "never written")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card with Review…")
        review.tap()
        let deny = e.app.buttons.matching(identifier: "approval-deny").firstMatch
        e.expect(deny, 30, "the approval's Deny")
        XCTAssertTrue(deny.isEnabled, "Deny needs no arming")
        deny.tap()
        XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists }, "the sheet goes")
        e.expect(e.containing("Denied by you"), 30, "the card says it was denied")
        await e.eventually("the request is denied at xbind", timeout: 15) { try await e.server.wsRequestState(ref) == "denied" }
        let after = try await e.server.wsNote()
        XCTAssertEqual(e.server.wsNoteValue(after), e.server.wsNoteValue(before), "nothing was written")
        e.shot("wsagent-07-denied")
    }

    @MainActor
    func test04WiderGrant() async throws {
        let server = try XCTUnwrap(E2EServer.fromEnvironment())
        let run = try await server.wsPrepare("E2E wider")
        let ref = try await server.wsFileNoteRequest(run, note: "under a standing approval")

        // 1. the gate (Face ID) says no: Go back approves nothing, and neither does a refused gate
        do {
            let e = try E2E(self)
            e.app.launchArguments += ["-XbinAgentGate", "deny"]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.wsOpen()
            let review = e.wsReview(ref)
            e.expect(review, 90, "the request's card with Review…")
            review.tap()
            let wider = e.wsChooseWider()
            XCTAssertTrue(e.containing("WIDER").exists, "the choice is marked wider")
            e.shot("wsagent-08-wider-choice")
            wider.allow.tap()
            let second = e.element("wider-heading")
            e.expect(second, 15, "the wider grant's second sheet")
            XCTAssertTrue(second.label.contains("Allow every change to Note?"), "its heading: \(second.label)")
            let back = e.app.buttons.matching(identifier: "wider-deny").firstMatch, every = e.app.buttons.matching(identifier: "wider-allow").firstMatch
            XCTAssertTrue(back.exists && every.exists, "Go back and Allow every change")
            XCTAssertEqual(back.label, "Go back")
            XCTAssertTrue(e.containing("Allow every change").exists)
            XCTAssertLessThan(back.frame.midX, every.frame.midX, "Go back is the first button")
            e.shot("wsagent-09-wider-sheet")
            back.tap()
            XCTAssertTrue(e.until(10) { !second.exists }, "Go back returns to the approval")
            let stateAfterBack = try await server.wsRequestState(ref)
            XCTAssertEqual(stateAfterBack, "pending", "Go back approved nothing")

            // again, to Allow every change — and the gate refuses
            XCTAssertTrue(e.until(15) { wider.allow.isEnabled }, "Allow arms again")
            wider.allow.tap()
            e.expect(second, 15, "the second sheet again")
            XCTAssertTrue(e.until(15) { every.isEnabled }, "Allow every change arms")
            every.tap()
            e.expect(e.containing("It wasn't you"), 15, "the gate refused, and the sheet says so")
            let stateAfterGate = try await server.wsRequestState(ref)
            XCTAssertEqual(stateAfterGate, "pending", "a refused gate approved nothing")
            let approvalsAfterGate = try await server.wsApprovals()
            XCTAssertTrue(approvalsAfterGate.isEmpty, "…and made no standing approval")
            e.app.terminate()
        }

        // 2. the gate says yes: a standing approval for the whole tile, listed and taken back
        let e = try E2E(self)
        e.app.launchArguments += ["-XbinAgentGate", "allow"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card with Review…")
        review.tap()
        let wider = e.wsChooseWider()
        wider.allow.tap()
        let every = e.app.buttons.matching(identifier: "wider-allow").firstMatch
        e.expect(every, 15, "the second sheet")
        XCTAssertTrue(e.until(15) { every.isEnabled }, "Allow every change arms")
        every.tap()
        XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists }, "both sheets go")
        var approval = ""
        await e.eventually("a standing approval of every change to apps/note", timeout: 20) {
            let list = try await server.wsApprovals()
            approval = list.first { $0.tile == "apps/note" && $0.scope == "tile" }?.id ?? ""
            return !approval.isEmpty
        }
        await e.eventually("the note changed under it", timeout: 30) { try await server.wsNote().contains("note=\"under a standing approval\"") }

        // the shield's Approvals lists it, with Take back
        e.wsChromeMenu("Approvals")
        let takeBack = e.app.buttons.matching(identifier: "approval-takeback:\(approval)").firstMatch
        e.expect(takeBack, 30, "the standing approval with Take back")
        XCTAssertTrue(e.containing("Always · every change").exists, "what it allows")
        XCTAssertTrue(e.containing("WIDER").exists, "…marked wider")
        e.shot("wsagent-10-approvals")
        takeBack.tap()
        await e.eventually("taken back at xbind", timeout: 15) { try await server.wsApprovals().isEmpty }
        XCTAssertTrue(e.until(15) { !takeBack.exists }, "the row goes")
        e.app.buttons["Done"].tap()
    }

    @MainActor
    func test05SessionStart() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E tile agent")
        let ref = try await e.server.wsFileSessionStart(run)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the start's card with Review…")
        review.tap()
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "the start's approval")
        XCTAssertTrue(heading.label.contains("Start a tile agent in Note?"), "the heading: \(heading.label)")
        // the mode leads the rows
        let mode = e.containing("asks before acting"), tile = e.app.staticTexts["Coding agent"]
        XCTAssertTrue(mode.exists, "the mode: it asks before acting")
        XCTAssertTrue(tile.exists, "the coding agent's row")
        XCTAssertLessThan(mode.frame.minY, tile.frame.minY, "the mode's row comes first")
        XCTAssertTrue(e.containing("List what is in the tile").exists, "what it will be told, as sent")
        XCTAssertTrue(e.containing("sent exactly like this").exists)
        XCTAssertFalse(e.element("approval-danger").exists, "a mode that asks: no danger banner")
        let start = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertEqual(start.label, "Start")
        XCTAssertFalse(e.element("approval-choice-call").exists, "no standing choice for a start: asked every time")
        e.shot("wsagent-11-session-start")
        XCTAssertTrue(e.until(15) { start.isEnabled }, "Start arms")
        start.tap()
        XCTAssertTrue(e.until(20) { !heading.exists }, "the sheet goes")

        // the tile agent: the person's own session, started by their agent
        var session = ""
        await e.eventually("the tile agent runs on apps/note", timeout: 60) {
            session = try await e.server.sessions(cwd: "apps/note").first { $0.kind == "agent" }?.id ?? ""
            return !session.isEmpty
        }
        let open = e.app.buttons.matching(identifier: "agent-session-open:\(session)").firstMatch
        e.expect(open, 60, "the tile agent's card with its button")
        XCTAssertTrue(e.containing("Note").exists)
        XCTAssertTrue(["Open Note's tile agent", "Answer Note's tile agent"].contains(open.label), "the button says what it opens: \(open.label)")
        e.shot("wsagent-12-session-card")
        open.tap()
        // the app's own agent screen for that session, behind the sheet (now at medium) or beside the column
        let screen = e.app.navigationBars.matching(NSPredicate(format: "identifier CONTAINS %@", "Agent")).firstMatch
        XCTAssertTrue(e.until(30) { screen.exists || e.containing("Agent · Note").exists }, "the app's agent screen for the session")
        if !e.isPad {
            XCTAssertTrue(e.until(10) { e.wsClose.exists && e.wsClose.frame.minY > e.app.windows.firstMatch.frame.height * 0.3 },
                          "the sheet drops to medium so both show")
        }
        XCTAssertTrue(e.until(15) { e.backButton.exists && e.backButton.isHittable }, "the session's panel is in front, usable behind the sheet")
        e.shot("wsagent-13-session-opened")
        await e.server.end(session)
    }

    @MainActor
    func test06NeedsYou() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E needs you")
        let ref = try await e.server.wsFileNoteRequest(run, note: "from the inbox")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let needs = e.app.buttons["Needs you"]
        e.expect(needs, 60, "Home's Needs you row")
        XCTAssertTrue(e.until(30) { (needs.value as? String) == "1" }, "it counts the request: \(needs.value ?? "")")
        XCTAssertTrue(e.until(10) { e.wsButton.label.contains("1 needs you") }, "the agent's button counts it: \(e.wsButton.label)")
        XCTAssertTrue(e.until(10) { (e.wsHomeRow.value as? String ?? "").contains("1 needs you") }, "…and so does its Home row, in words")
        XCTAssertFalse(e.wsHomeRow.staticTexts["1"].exists, "…with no second badge: Needs you and the bar's item carry the count")
        needs.tap()
        let row = e.app.buttons.matching(identifier: "inbox-request:\(ref)").firstMatch
        e.expect(row, 30, "the inbox lists the request")
        XCTAssertTrue(row.label.contains("Approve a change to Note"), "by what it is: \(row.label)")
        XCTAssertTrue(row.label.contains("Review"), "with Review…")
        e.shot("wsagent-14-inbox")
        row.tap()
        // the inbox goes, the app's approval shows (never an Allow from a list)
        e.expect(e.element("approval-heading"), 30, "Review… opens the approval")
        XCTAssertFalse(row.exists, "the inbox went first")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
        XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists }, "Not now leaves it for later")
        let state = try await e.server.wsRequestState(ref)
        XCTAssertEqual(state, "pending", "…still waiting")

        // the same list from the agent's own chrome (the shield), over its sheet
        e.wsOpen()
        e.wsChromeMenu("Needs you (1)")
        e.expect(row, 30, "the shield's Needs you lists it, over the agent's sheet")
        row.tap()
        e.expect(e.element("approval-heading"), 30, "…and Review… opens the approval there too")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
        XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists })
        XCTAssertTrue(e.wsClose.exists, "the agent's sheet is still up")
    }

    @MainActor
    func test07ScopePanel() async throws {
        let e = try E2E(self)
        _ = try await e.server.wsPrepare("E2E scope")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        // the connection line: xbind's sentence, one element, a button
        let chip = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", "tiles", "connected")).firstMatch
        e.expect(chip, 90, "the connection line under the bar")
        e.shot("wsagent-15-connection")
        chip.tap()
        e.expect(e.containing("Hidden by you"), 30, "the scope panel's totals")
        for word in ["Connected", "Blocked", "No access", "Model"] {
            XCTAssertTrue(e.containing(word).exists, "the panel's \(word)")
        }
        e.expect(e.containing("It is not a permission"), 30, "the sentence that scope is focus, not permission")
        XCTAssertTrue(e.app.buttons["Apply"].exists && !e.app.buttons["Apply"].isEnabled, "Apply waits for a staged change")
        e.shot("wsagent-16-scope-panel")
        e.app.buttons["Cancel"].tap()
        XCTAssertTrue(e.until(10) { !e.containing("Hidden by you").exists }, "Cancel closes it")
    }

    @MainActor
    func test08PauseAndResume() async throws {
        let e = try E2E(self)
        _ = try await e.server.wsPrepare("E2E pause")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        e.expect(e.wsComposer, 90, "the composer, before pausing")
        e.wsChromeMenu("Pause my agent")
        let resume = e.app.buttons.matching(identifier: "agent-resume").firstMatch
        e.expect(resume, 30, "the paused bar's Resume")
        XCTAssertEqual(resume.label, "Resume my agent", "the button says what it resumes")
        XCTAssertTrue(e.containing("Paused by you").exists, "what happened")
        XCTAssertTrue(e.containing("read or change anything in the workspace").exists, "…and the conversation's banner says what it means")
        XCTAssertTrue(e.until(20) { !e.wsComposer.exists }, "no composer while paused")
        let paused = try await e.server.wsPaused()
        XCTAssertTrue(paused, "paused at xbind")
        e.shot("wsagent-17-paused")
        // the bar's button says so in place of a count
        e.wsClose.tap()
        XCTAssertTrue(e.until(15) { e.wsButton.label == "Workspace agent, paused" }, "the button: \(e.wsButton.label)")
        XCTAssertTrue(e.until(10) { (e.wsHomeRow.value as? String ?? "") == "paused" }, "Home's row: \(e.wsHomeRow.value ?? "")")
        e.wsOpen()
        e.expect(resume, 30, "Resume, again")
        resume.tap()
        XCTAssertTrue(e.until(20) { !resume.exists }, "resumed: the bar goes")
        await e.eventually("resumed at xbind", timeout: 15) { try await !e.server.wsPaused() }
        e.expect(e.wsComposer, 60, "the composer is back")
    }

    @MainActor
    func test09PadColumn() async throws {
        let e = try E2E(self)
        try XCTSkipUnless(e.isPad, "an iPad's layout: run on an iPad simulator")
        let run = try await e.server.wsPrepare("E2E iPad")
        let ref = try await e.server.wsFileNoteRequest(run, note: "from an iPad")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.expect(e.wsButton, 60, "the agent's button")
        e.wsButton.tap()
        e.expect(e.wsClose, 30, "the agent's column")
        let window = e.app.windows.firstMatch.frame
        XCTAssertGreaterThan(e.wsClose.frame.minX, window.width * 0.45, "a trailing column: \(e.wsClose.frame) in \(window)")
        XCTAssertLessThan(e.wsClose.frame.minY, 120, "…the window's height, not a sheet")
        let needs = e.app.buttons["Needs you"]
        XCTAssertTrue(needs.exists && needs.isHittable, "the panels stay usable beside it")
        e.shot("wsagent-18-ipad-column")
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card in the column")
        review.tap()
        e.expect(e.element("approval-heading"), 30, "the approval, over the window")
        e.shot("wsagent-19-ipad-approval")
        e.app.buttons.matching(identifier: "approval-deny").firstMatch.tap()
        XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists })
        // the button closes the column again
        e.wsButton.tap()
        XCTAssertTrue(e.until(10) { !e.wsClose.exists }, "the button toggles the column")
    }

    /// One request at a time: after a decision the next that waits shows —
    /// as a sheet of its own, on Once, with Allow unarmed, whatever was
    /// chosen for the one before.
    @MainActor
    func test11NextRequestStartsFresh() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E two requests")
        let first = try await e.server.wsFileNoteRequest(run, note: "the first of two")
        let second = try await e.server.wsFileNoteRequest(run, note: "the second of two", after: first)
        e.app.launchArguments += ["-XbinAgentArmSeconds", "8"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(first)
        e.expect(review, 90, "the first request's card")
        if !review.isHittable { e.app.swipeDown() }
        review.tap()
        e.expect(e.containing("\"the first of two\""), 30, "the first request's sheet")
        XCTAssertTrue(e.containing("1 of 2").exists, "its place among what waits")
        // choose "always, this exact action" for the first, and allow it
        let call = e.app.buttons.matching(identifier: "approval-choice-call").firstMatch
        if !call.isHittable { e.app.swipeUp() }
        XCTAssertTrue(e.until(20) { call.isEnabled }, "the standing choice arms (it widens the grant)")
        call.tap()
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertTrue(e.until(10) { allow.label.hasPrefix("Allow always") }, "Allow follows the choice: \(allow.label)")
        XCTAssertTrue(e.until(20) { allow.isEnabled }, "Allow arms")
        allow.tap()
        // the second shows by itself: its own body, Once, Allow unarmed
        e.expect(e.containing("\"the second of two\""), 30, "the next request's sheet")
        let next = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        e.expect(next, 10, "its Allow")
        XCTAssertEqual(next.label, "Allow once", "the next request opens on Once, not on what was chosen before")
        XCTAssertFalse(next.isEnabled, "…and with Allow unarmed")
        let stillPending = try await e.server.wsRequestState(second)
        XCTAssertEqual(stillPending, "pending", "nothing was decided for it")
        e.app.buttons.matching(identifier: "approval-deny").firstMatch.tap()
        XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists }, "no more requests: the sheet goes")
        await e.eventually("the second is denied, the first allowed as a standing approval", timeout: 20) {
            let denied = try await e.server.wsRequestState(second) == "denied"
            let standing = try await e.server.wsApprovals().contains { $0.tile == "apps/note" && $0.scope == "call" }
            return denied && standing
        }
    }

    /// The conversation's own screens push over it (the notebook); coming
    /// back, the app-drawn cards still work — the runtime and its host are
    /// the container's, not the first screen's.
    @MainActor
    func test12PushedScreenKeepsTheCards() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E pushed")
        let ref = try await e.server.wsFileNoteRequest(run, note: "after the notebook")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card")
        e.agentMenu("Notebook")
        e.expect(e.containing("what the agent remembers"), 30, "the notebook, pushed over the conversation")
        XCTAssertTrue(e.until(5) { !review.isHittable }, "…covering it")
        e.shot("wsagent-20-notebook")
        let back = e.app.navigationBars.buttons.matching(identifier: "BackButton").firstMatch
        e.expect(back, 10, "Back from the notebook")
        back.tap()
        e.expect(review, 30, "the conversation again, its card still the app's")
        XCTAssertTrue(e.until(10) { review.isHittable && review.isEnabled }, "Review… can be tapped")
        review.tap()
        e.expect(e.element("approval-heading"), 30, "…and still opens the approval")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
    }

    @MainActor
    func test10Gallery() async throws {
        let server = try XCTUnwrap(E2EServer.fromEnvironment())
        for mode in ["light", "dark"] {
            // each pass its own conversation: a settled request, a tile agent, a request that waits
            let empty = try await server.wsPrepare("E2E gallery empty \(mode)")
            let run = try await server.wsNewSession("E2E gallery \(mode)")
            let done = try await server.wsFileNoteRequest(run, note: "Standup moved to 10:30")
            try await server.wsApprove(done)
            let start = try await server.wsFileSessionStart(run)
            let pending = try await server.wsFileNoteRequest(run, note: "Invoices are due on Friday")
            try await server.wsShow(empty)

            var n = 0
            let e = try E2E(self)
            func shot(_ name: String) {
                n += 1
                e.shot(String(format: "wsagent-%@-%02d-%@", mode, n, name))
            }
            e.app.launchArguments += ["-XbinAppearance", mode, "-XbinAgentGate", "allow"]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.expect(e.wsHomeRow, 60, "Home's agent row")
            XCTAssertTrue(e.until(20) { e.wsButton.label.contains("need") }, "the button counts what waits")
            shot("home")

            // the empty state: what it can and can't do, starters
            e.wsOpen()
            e.expect(e.containing("Ask for an outcome"), 90, "the empty state")
            shot(e.isPad ? "ipad-empty" : "empty")
            e.wsClose.tap()

            // over a tile: the other session, picked from Sessions; large, then medium with the tile usable behind
            e.openTile("apps/counter")
            e.expect(e.app.buttons["+1"], 60, "the counter")
            e.wsButton.tap()
            e.expect(e.wsClose, 30, "the agent over the tile")
            let sessions = e.app.buttons.matching(NSPredicate(format: "label == %@", "Sessions")).firstMatch
            e.expect(sessions, 30, "the view's Sessions button")
            sessions.tap()
            let other = e.app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "E2E gallery \(mode)")).firstMatch
            e.expect(other, 30, "the sessions, the conversation's among them")
            shot("sessions")
            other.tap()
            e.expect(e.wsReview(pending), 90, "the conversation with its cards")
            shot(e.isPad ? "ipad-column" : "sheet-large")
            if !e.isPad {
                e.wsToggleSheetDetent()
                XCTAssertTrue(e.until(10) { e.wsClose.frame.minY > e.app.windows.firstMatch.frame.height * 0.3 }, "the sheet at medium")
                shot("sheet-medium")
                // the tile behind stays usable
                let before = try await server.counter()
                // (the screen under the tile's panel has the counter's card, with a +1 of its own: the one in front)
                let plus = e.firstHittable(e.app.buttons.matching(NSPredicate(format: "label == %@", "+1")), timeout: 10)
                XCTAssertNotNil(plus, "the counter's +1 can be tapped behind the sheet at medium")
                plus?.tap()
                await e.eventually("the counter behind the sheet took the tap", timeout: 15) { try await server.counter() == before + 1 }
                e.wsToggleSheetDetent()
            }

            // the approval, its wider second sheet
            e.wsReview(pending).tap()
            e.expect(e.element("approval-heading"), 30, "the approval")
            shot("approval")
            let wider = e.wsChooseWider()
            shot("approval-wider-choice")
            wider.allow.tap()
            e.expect(e.element("wider-heading"), 15, "the wider sheet")
            shot("wider-sheet")
            e.app.buttons.matching(identifier: "wider-deny").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !e.element("wider-heading").exists })
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists })

            // a tile agent's start
            e.wsReview(start).tap()
            e.expect(e.element("approval-heading"), 30, "the start's approval")
            shot("session-start")
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists })

            // the scope panel
            let chip = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", "tiles", "connected")).firstMatch
            if chip.waitForExistence(timeout: 20) {
                chip.tap()
                e.expect(e.containing("Hidden by you"), 30, "the scope panel")
                shot("scope")
                e.app.buttons["Cancel"].tap()
                XCTAssertTrue(e.until(10) { !e.containing("Hidden by you").exists })
            }

            // paused
            e.wsChromeMenu("Pause my agent")
            e.expect(e.app.buttons.matching(identifier: "agent-resume").firstMatch, 30, "paused")
            shot("paused")
            e.app.buttons.matching(identifier: "agent-resume").firstMatch.tap()
            XCTAssertTrue(e.until(20) { !e.app.buttons.matching(identifier: "agent-resume").firstMatch.exists })

            // the inbox, from the shield (Home is in its search here)
            let menu = e.app.buttons.matching(identifier: "agent-chrome-menu").firstMatch
            menu.tap()
            let needs = e.app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Needs you (")).firstMatch
            e.expect(needs, 10, "Needs you in the shield menu")
            shot("shield-menu")
            needs.tap()
            e.expect(e.app.buttons.matching(identifier: "inbox-request:\(pending)").firstMatch, 30, "the inbox")
            shot("inbox")
            e.app.buttons["Done"].tap()
            e.wsClose.tap()
            e.app.terminate()
        }
    }
}

// MARK: - The workspace agent's API, as the e2e account

extension E2EServer {
    static let wsAgent = "xbin/wsagent"
    private static let wsAPI = "/api/xbin/wsagent"

    /// A standing approval (GET /api/xbin/delegate/approvals).
    struct StandingApproval: Sendable {
        let id: String
        let tile: String
        let scope: String
    }

    /// The agent's own routes with its page's credential — the e2e account's
    /// frame token of the component, all a native tile's runtime holds.
    func wsCall(_ method: String, _ path: String, json: [String: Any]? = nil) async throws -> [String: Any] {
        let frame = try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/frame-token?component=\(Self.wsAgent)")) as? [String: Any]
        let token = try XCTUnwrap(frame?["token"] as? String, "a frame token of the workspace agent")
        var r = URLRequest(url: URL(string: address + Self.wsAPI + path)!)
        r.httpMethod = method
        r.setValue(token, forHTTPHeaderField: "X-XBin-Frame-Token")
        if let json {
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
            r.httpBody = try JSONSerialization.data(withJSONObject: json)
        }
        r.timeoutInterval = 30
        let (data, response) = try await URLSession.shared.data(for: r)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            throw Failure(description: "\(method) \(Self.wsAPI)\(path) → \(status): \(String(decoding: data.prefix(200), as: UTF8.self))")
        }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
    }

    private func wsJSON(_ method: String, _ path: String, json: [String: Any]? = nil) async throws -> Any {
        try JSONSerialization.jsonObject(with: await send(method, path, json: json))
    }

    /// A quiet agent for the account, and a session of the test's own in
    /// view: resumed, nothing pending (denied), no standing approval, no
    /// tile agent it started, earlier tests' sessions gone. The new
    /// session's id.
    func wsPrepare(_ name: String) async throws -> Int {
        _ = try await send("PUT", "/api/xbin/delegate/state", json: ["paused": false])
        let pending = (try await wsJSON("GET", "/api/xbin/delegate/requests") as? [String: Any])?["requests"] as? [[String: Any]] ?? []
        for r in pending { if let id = r["id"] as? String { _ = try? await send("POST", "/api/xbin/delegate/requests/\(id)/deny", json: [:]) } }
        for a in try await wsApprovals() { _ = try? await send("DELETE", "/api/xbin/delegate/approvals/\(a.id)") }
        let live = (try await wsJSON("GET", "/api/xbin/term/sessions") as? [[String: Any]]) ?? []
        for s in live where (s["origin"] as? String) == "agent" { if let id = s["id"] as? String { await end(id) } }
        let sessions = try await wsCall("GET", "/ws/sessions")["sessions"] as? [[String: Any]] ?? []
        for s in sessions where (s["main"] as? Bool) != true && ((s["name"] as? String) ?? "").hasPrefix("E2E") {
            if let id = s["id"] as? Int { _ = try? await wsCall("DELETE", "/ws/sessions/\(id)") }
        }
        return try await wsNewSession(name)
    }

    /// A new session, shown when the agent opens next (its `lastSession`).
    func wsNewSession(_ name: String) async throws -> Int {
        let made = try await wsCall("POST", "/ws/sessions", json: ["name": name])
        let id = try XCTUnwrap((made["session"] as? [String: Any])?["id"] as? Int, "the new session's id")
        try await wsShow(id)
        return id
    }

    /// The session the agent's view opens on.
    func wsShow(_ run: Int) async throws {
        _ = try await wsCall("PUT", "/ws/prefs", json: ["lastSession": run])
    }

    /// Sends the person's message and waits for the agent's answer to it —
    /// `words` a sentence of the scripted model's phrasebook, or any words
    /// with a `ws:` script line after them.
    func wsSay(_ run: Int, _ words: String, script: String? = nil, timeout: TimeInterval = 90) async throws {
        func messages() async throws -> [[String: Any]] { try await wsCall("GET", "/runs/\(run)/view")["messages"] as? [[String: Any]] ?? [] }
        let last = try await messages().compactMap { $0["id"] as? Int }.max() ?? 0
        _ = try await wsCall("POST", "/runs/\(run)/message", json: ["text": script.map { "\(words)\nws: \($0)" } ?? words])
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let done = try await messages().contains { m in
                (m["id"] as? Int ?? 0) > last && (m["role"] as? String) == "assistant"
                    && !((m["content"] as? String) ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            }
            if done { return }
            try await Task.sleep(for: .milliseconds(400))
        }
        throw Failure(description: "the agent did not answer “\(words.prefix(60))” within \(Int(timeout)) s")
    }

    private func wsArgs(_ o: [String: Any]) throws -> String {
        String(decoding: try JSONSerialization.data(withJSONObject: o, options: [.sortedKeys, .withoutEscapingSlashes]), as: UTF8.self)
    }

    /// The session's pending request of `kind` at xbind (its `ref`), newest.
    private func wsPendingRef(_ run: Int, kind: String, not earlier: String? = nil) async throws -> String {
        let cards = try await wsCall("GET", "/ws/sessions/\(run)/cards")
        let list = (cards["requests"] as? [[String: Any]] ?? []).filter { ($0["kind"] as? String) == kind && ($0["state"] as? String) == "pending" }
        return try XCTUnwrap(list.compactMap { $0["ref"] as? String }.last { !$0.isEmpty && $0 != earlier }, "a pending \(kind) request on the session's cards")
    }

    /// The agent asks to write apps/note's note (a PUT: it needs the
    /// person's approval; its reason, in its own words, is "Update the
    /// note"). The request's id at xbind.
    func wsFileNoteRequest(_ run: Int, note: String, after earlier: String? = nil) async throws -> String {
        try await wsSay(run, "Set the note in apps/note to \"\(note)\"")
        return try await wsPendingRef(run, kind: "call", not: earlier)
    }

    /// The agent writes a brief for apps/note, the person picks the scripted
    /// coding agent on it and presses the brief's "Review and start…" (the
    /// route the button calls): xbind files the start. Its request id.
    func wsFileSessionStart(_ run: Int) async throws -> String {
        try await wsSay(run, "Start a tile agent in apps/note to list what is in the tile")
        let cards = try await wsCall("GET", "/ws/sessions/\(run)/cards")
        let brief = try XCTUnwrap((cards["briefs"] as? [[String: Any]] ?? []).last?["id"] as? Int, "the brief's id")
        _ = try await wsCall("PUT", "/ws/sessions/\(run)/briefs/\(brief)", json: ["harness": "fake"])
        _ = try await wsCall("POST", "/ws/sessions/\(run)/briefs/\(brief)/start", json: [:])
        return try await wsPendingRef(run, kind: "session")
    }

    func wsRequestState(_ id: String) async throws -> String {
        (try await wsJSON("GET", "/api/xbin/delegate/requests/\(id)") as? [String: Any])?["state"] as? String ?? ""
    }

    /// Approves once, as the person (the gallery's settled card).
    func wsApprove(_ id: String) async throws {
        _ = try await send("POST", "/api/xbin/delegate/requests/\(id)/approve", json: ["grant": "once"])
        let deadline = Date().addingTimeInterval(30)
        while Date() < deadline, try await wsRequestState(id) != "used" { try await Task.sleep(for: .milliseconds(400)) }
    }

    func wsApprovals() async throws -> [StandingApproval] {
        let list = (try await wsJSON("GET", "/api/xbin/delegate/approvals") as? [String: Any])?["approvals"] as? [[String: Any]] ?? []
        return list.compactMap { a in
            guard let id = a["id"] as? String else { return nil }
            return StandingApproval(id: id, tile: a["tile"] as? String ?? "", scope: a["scope"] as? String ?? "")
        }
    }

    func wsPaused() async throws -> Bool {
        (try await wsJSON("GET", "/api/xbin/delegate/state") as? [String: Any])?["paused"] as? Bool ?? false
    }

    /// What happened to apps/note's note (GET /api/apps/note/seen):
    /// `note=… reads=… writes=… last write by user=… agent=… role=…`.
    func wsNote() async throws -> String {
        String(decoding: try await send("GET", "/api/apps/note/seen"), as: UTF8.self)
    }

    /// The note's own text out of ``wsNote()``'s line.
    func wsNoteValue(_ seen: String) -> String {
        guard let r = seen.range(of: " reads=") else { return seen }
        return String(seen[..<r.lowerBound])
    }
}

// MARK: - Driving the agent's chrome

extension E2E {
    /// The bar's agent button (the trailing-most item of the panel in front).
    var wsButton: XCUIElement { app.buttons.matching(identifier: "panel-agent").firstMatch }
    /// Home's row for the agent.
    var wsHomeRow: XCUIElement { app.buttons.matching(identifier: "home-agent").firstMatch }
    /// The agent container's close button.
    var wsClose: XCUIElement { app.buttons.matching(identifier: "agent-close").firstMatch }
    /// The agent's composer, by either placeholder.
    var wsComposer: XCUIElement {
        // (exactly: the empty state's heading is "Ask for an outcome", without the ellipsis)
        let first = "Ask for an outcome…", later = "Reply, or ask for something else…"
        return app.descendants(matching: .any).matching(NSPredicate(format: "placeholderValue == %@ OR placeholderValue == %@ OR label == %@ OR label == %@",
                                                                    first, later, first, later)).firstMatch
    }

    /// A request card's Review… (the card is the app's).
    func wsReview(_ id: String) -> XCUIElement { app.buttons.matching(identifier: "agent-review:\(id)").firstMatch }

    /// iOS's "Save Password?" comes up some seconds after a first sign-in,
    /// over whatever the test is on by then: Not Now, if it is up now.
    func wsDismissSavePassword() {
        let inApp = app.buttons["Not Now"]
        if inApp.exists { inApp.tap(); return }
        let onBoard = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["Not Now"]
        if onBoard.exists { onBoard.tap() }
    }

    /// `element` can be tapped — once nothing of the system's covers it.
    func wsHittable(_ element: XCUIElement, _ timeout: TimeInterval = 20) -> Bool {
        until(timeout) {
            if element.exists, element.isHittable { return true }
            wsDismissSavePassword()
            return false
        }
    }

    /// Opens the agent from the bar's button.
    func wsOpen(file: StaticString = #filePath, line: UInt = #line) {
        expect(wsButton, 60, "the bar's Workspace agent button", file: file, line: line)
        XCTAssertTrue(wsHittable(wsButton), "the agent's button can be tapped", file: file, line: line)
        wsButton.tap()
        expect(wsClose, 30, "the agent's container", file: file, line: line)
    }

    /// The agent's button is the trailing-most item of the bar in front.
    func wsExpectButtonTrailingMost(_ where_: String, file: StaticString = #filePath, line: UInt = #line) {
        expect(wsButton, 60, "the Workspace agent button \(where_)", file: file, line: line)
        XCTAssertTrue(wsHittable(wsButton), "the agent's button \(where_) can be tapped", file: file, line: line)
        let mine = wsButton.frame
        var others: [String] = []
        let buttons = app.navigationBars.buttons
        for i in 0..<buttons.count {
            let b = buttons.element(boundBy: i)
            guard b.exists, b.isHittable, b.identifier != "panel-agent" else { continue }
            if b.frame.midX > mine.midX, abs(b.frame.midY - mine.midY) < 30 { others.append("\(b.label) \(b.frame)") }
        }
        XCTAssertTrue(others.isEmpty, "the agent's button \(where_) is the bar's trailing-most item (\(mine)); after it: \(others)", file: file, line: line)
    }

    /// The shield menu's `item` (xbin's own controls for the agent).
    func wsChromeMenu(_ item: String, file: StaticString = #filePath, line: UInt = #line) {
        let menu = app.buttons.matching(identifier: "agent-chrome-menu").firstMatch
        expect(menu, 30, "the shield menu", file: file, line: line)
        menu.tap()
        let entry = app.buttons[item]
        expect(entry, 10, "\(item) in the shield menu", file: file, line: line)
        entry.tap()
    }

    /// On an approval: picks "Always, every change to …", the wider choice;
    /// the Allow button, whose words follow it.
    func wsChooseWider(file: StaticString = #filePath, line: UInt = #line) -> (choice: XCUIElement, allow: XCUIElement) {
        let choice = app.buttons.matching(identifier: "approval-choice-tile").firstMatch
        expect(choice, 30, "the approval's wider choice", file: file, line: line)
        if !choice.isHittable { app.swipeUp() }
        // it widens the grant: armed like Allow, a moment after the sheet shows
        XCTAssertTrue(until(20) { choice.isEnabled }, "the wider choice arms", file: file, line: line)
        choice.tap()
        let allow = app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertTrue(until(10) { allow.label.hasPrefix("Allow every change to") }, "Allow's words follow the choice: \(allow.label)", file: file, line: line)
        XCTAssertTrue(until(15) { allow.isEnabled }, "Allow arms after the choice", file: file, line: line)
        return (choice, allow)
    }

    /// Moves the agent's sheet to its other detent — large ↔ medium — by
    /// its grabber, as a tap on it does (a synthesized drag is read as a
    /// flick down and dismisses the sheet).
    func wsToggleSheetDetent(file: StaticString = #filePath, line: UInt = #line) {
        let grabber = app.buttons["Sheet Grabber"]
        expect(grabber, 10, "the sheet's grabber", file: file, line: line)
        grabber.tap()
        _ = app.otherElements["xbin-e2e-never"].waitForExistence(timeout: 0.8) // the sheet settles
    }
}
