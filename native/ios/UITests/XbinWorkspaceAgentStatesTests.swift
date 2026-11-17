#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// The workspace agent's chrome (D233) in the states and under the rules
/// XbinWorkspaceAgentTests doesn't reach: who has an agent, what shows in
/// place of the conversation when it can't run, an admin batch with the
/// recent-sign-in step, a tile agent's start in a mode that never asks, the
/// arming of everything that widens what the agent may do, what the agent's
/// view may ask the chrome for, a draft it hands over — and the grant
/// sheets at the largest text size and as VoiceOver reads them.
///
/// Two kinds of test, named for which:
///
/// - **Demo** (`…Demo…`): a Debug build's `-XbinAgentDemo <state>`
///   (App/Model/AgentDemo.swift) answers the chrome's own reads from canned
///   xbind JSON — the corpus the delegation words' vectors are made of — for
///   states the e2e xbind can't be put in: an admin before any setup, a held
///   agent, no language model, the password step (a `--dev` xbind never asks
///   for one), a start in a mode that never asks. They need only an xbind to
///   sign in to.
/// - **Live** (`…Live…`): the e2e xbind with a workspace agent
///   (`XBIN_E2E_WSAGENT=1`), put in the state through its API: an admin who
///   gave others an agent and not themselves, nobody chosen yet (the setup's
///   third step), workspace agents turned off, an admin batch proposed by
///   the scripted model and applied.
///
/// Screenshots: `wsagent2-NN-<what>.png` from the tests, and
/// `wsagent2-<light|dark>-NN-<state>.png` from the gallery.
final class XbinWorkspaceAgentStatesTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    /// The live tests need the e2e xbind's workspace agent.
    private func requireAgent() throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_WSAGENT"] != "1",
                      "XBIN_E2E_WSAGENT unset: the e2e xbind has no workspace agent (e2e-xbind.sh)")
    }

    // MARK: Demo — in place of the conversation

    /// An admin before setup: the three steps, read-only, and the way to the
    /// web — on Home's row, on the bar's button and in the pane.
    @MainActor
    func test01DemoSetup() throws {
        let e = try E2E(self)
        e.wsDemo("setup")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.expect(e.wsHomeRow, 60, "Home's row for the agent's setup")
        XCTAssertEqual(e.wsHomeRow.label, "Set up the workspace agent")
        XCTAssertTrue(e.until(10) { (e.wsHomeRow.value as? String ?? "").contains("not set up") }, "its status: \(e.wsHomeRow.value ?? "")")
        XCTAssertEqual(e.wsButton.label, "Set up the workspace agent", "the bar's button says so too")
        XCTAssertTrue(e.wsHittable(e.wsHomeRow), "the row can be tapped")
        e.wsHomeRow.tap()
        e.expect(e.element("agent-setup"), 30, "the setup's three steps")
        for step in ["1. Connect a language model", "2. Turn workspace agents on", "3. Choose who gets one"] {
            XCTAssertTrue(e.containing(step).exists, "the step “\(step)”")
        }
        XCTAssertTrue(e.containing("None is connected").exists, "the first step is not done, and says why")
        XCTAssertTrue(e.containing("Nobody else sees it yet").exists, "what an admin is told before setup")
        XCTAssertTrue(e.containing("Setup is done on the web").exists, "…and where it is done")
        XCTAssertTrue(e.app.buttons.matching(identifier: "agent-setup-web").firstMatch.exists, "Open the setup on the web")
        XCTAssertFalse(e.wsComposer.exists, "no conversation before setup")
        e.shot("wsagent2-01-setup")
    }

    /// What xbin says in place of the conversation — which switch is on, who
    /// changes it and where — with nothing of a conversation behind it.
    @MainActor
    func test02DemoOffAndHeldStates() throws {
        let states: [(demo: String, title: String, says: String)] = [
            ("off", "Turned off for this workspace.", "An admin turned workspace agents off"),
            ("nomodel", "Not available right now.", "needs a language model"),
            ("locked", "Locked after a restart.", "until an admin unlocks the workspace"),
            ("older", "Waiting for a newer xbin.", "until xbin is updated again"),
            ("held", "Not available on this xbin.", "tiles run without sandboxes here"),
        ]
        for (i, s) in states.enumerated() {
            let e = try E2E(self)
            e.wsDemo(s.demo)
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.wsOpen()
            let title = e.element("agent-state")
            e.expect(title, 30, "what xbin says for \(s.demo)")
            XCTAssertEqual(title.label, s.title, s.demo)
            XCTAssertTrue(e.containing(s.says).exists, "\(s.demo): “\(s.says)”")
            XCTAssertTrue(e.app.buttons["Check again"].exists, "\(s.demo): Check again")
            XCTAssertFalse(e.wsComposer.exists, "\(s.demo): no composer — nothing can be sent to an agent that is off")
            XCTAssertFalse(e.app.buttons.matching(identifier: "agent-resume").firstMatch.exists, "\(s.demo): not the person's to resume")
            // the shield has nothing that would act on an agent that can't run
            e.app.buttons.matching(identifier: "agent-chrome-menu").firstMatch.tap()
            XCTAssertTrue(e.app.buttons["Check again"].waitForExistence(timeout: 10))
            XCTAssertFalse(e.app.buttons["Pause my agent"].exists || e.app.buttons["Preferences"].exists, "\(s.demo): no Pause, no Preferences")
            e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6)).tap()
            e.shot(String(format: "wsagent2-%02d-%@", 2 + i, s.demo))
            e.app.terminate()
        }
    }

    /// An admin who gave others an agent and not themselves has none:
    /// nothing is shown, and a link to the agent lands on Home.
    @MainActor
    func test03DemoAdminWithoutAnAgent() throws {
        let e = try E2E(self)
        e.wsDemo("none")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let needs = e.app.buttons["Needs you"]
        e.expect(needs, 60, "Home")
        XCTAssertFalse(e.until(6) { e.wsHomeRow.exists || e.wsButton.exists }, "no row and no button for a person without an agent")
        // a link to the agent's component, and to xbin's page for it: Home, never an empty pane
        for link in ["xbin://\(e.server.authority)/c/xbin/wsagent", "xbin://\(e.server.authority)/xbin/agent"] {
            e.wsOpenLink(link)
            XCTAssertFalse(e.until(6) { e.wsAcceptOpenPrompt(); return e.wsClose.exists }, "\(link) opens no agent")
            let landed = e.until(20) { e.wsAcceptOpenPrompt(); e.wsDismissSavePassword(); return needs.exists && needs.isHittable }
            if !landed { e.shot("fail-link") }
            XCTAssertTrue(landed, "\(link) lands on Home")
            XCTAssertFalse(e.containing("isn't in this workspace").exists || e.containing("No such").exists, "…and on no error screen")
        }
        e.shot("wsagent2-08-no-agent")
    }

    // MARK: Demo — the grants

    /// An admin batch: a warned change starts unticked, a blocked one can't
    /// be ticked; applying asks the app's gate and then xbind's own
    /// recent-sign-in step (the password); a wrong password says so and
    /// keeps the ticks; the results name what was applied, what failed and
    /// what was skipped; Undo proposes a new set to review.
    @MainActor
    func test04DemoChangeSetWithStepUp() throws {
        let e = try E2E(self)
        e.wsDemo("grants")
        e.app.launchArguments += ["-XbinAgentGate", "allow"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsInbox("inbox-changeset:cs_1")
        let heading = e.element("changeset-heading")
        e.expect(heading, 30, "the batch's review")
        XCTAssertEqual(heading.label, "Your agent proposes 5 admin changes")
        XCTAssertTrue(e.containing("xbin admin changes, asking").exists, "the bar says whose sheet it is")
        XCTAssertTrue(e.containing("The agent's title, not checked by xbin: Wire up the export").exists, "the agent's title, quoted as unchecked")
        XCTAssertTrue(e.containing("Nothing has been applied").exists)

        // each tick says its change: its number, whether it is ticked, xbind's line, its warnings, before and after
        let tick: (Int) -> XCUIElement = { e.app.buttons.matching(identifier: "changeset-tick-\($0)").firstMatch }
        XCTAssertTrue(tick(1).label.hasPrefix("Change 1 of 5, selected. Give Invoices read access to Orders (1)"), tick(1).label)
        XCTAssertTrue(tick(1).label.contains("Before: no access") && tick(1).label.contains("After: read"), tick(1).label)
        XCTAssertTrue(tick(2).label.hasPrefix("Change 2 of 5, not selected."), "a change with a warning starts unticked: \(tick(2).label)")
        XCTAssertTrue(tick(2).label.contains("Warning, public: anyone on the internet can reach /export"), tick(2).label)
        XCTAssertTrue(tick(2).label.contains("Warning, open port:"), tick(2).label)
        XCTAssertTrue(tick(3).label.hasPrefix("Change 3 of 5, can't be applied."), tick(3).label)
        XCTAssertTrue(tick(3).label.contains("above Finance's ceiling"), "…with xbind's reason: \(tick(3).label)")
        XCTAssertFalse(tick(3).isEnabled, "a blocked change can't be ticked")
        XCTAssertTrue(tick(4).label.contains("not selected") && tick(4).label.contains("Warning, egress"), tick(4).label)
        let apply = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
        let reject = e.app.buttons.matching(identifier: "changeset-deny").firstMatch
        XCTAssertEqual(apply.label, "Apply 1 change…", "the button counts the ticks")
        XCTAssertEqual(reject.label, "Reject all")
        XCTAssertLessThan(reject.frame.midX, apply.frame.midX, "Reject left, Apply right")
        e.shot("wsagent2-09-changeset-review")

        // tick the two warned ones too
        for n in [4, 5] {
            e.wsScrollTo(tick(n))
            tick(n).tap()
        }
        XCTAssertTrue(e.until(10) { apply.label == "Apply 3 changes…" }, "the count follows the ticks: \(apply.label)")
        XCTAssertTrue(e.containing("3 of 5 ticked").exists)
        XCTAssertTrue(e.until(15) { apply.isEnabled }, "Apply arms again after a tick")
        apply.tap()

        // xbind asks for a recent sign-in: the password step, the ticks kept
        let step = e.element("changeset-stepup")
        e.expect(step, 30, "xbind's recent-sign-in step")
        XCTAssertEqual(step.label, "Confirm it's you")
        XCTAssertTrue(e.containing("needs your password, or a sign-in from the last ten minutes").exists)
        XCTAssertTrue(e.containing("Your ticks are kept").exists)
        let field = e.app.secureTextFields.firstMatch
        e.expect(field, 10, "the password field")
        XCTAssertEqual(field.label, "Your password", "the field says what it wants")
        let go = e.app.buttons.matching(identifier: "stepup-allow").firstMatch
        XCTAssertEqual(go.label, "Continue")
        XCTAssertFalse(go.isEnabled, "Continue waits for a password")
        XCTAssertEqual(e.app.buttons.matching(identifier: "stepup-deny").firstMatch.label, "Not now")
        e.shot("wsagent2-10-changeset-stepup")
        field.tap()
        field.typeText("not-the-password")
        e.tapAfterTyping(go)
        e.expect(e.containing("That password was not accepted"), 20, "a wrong password says so")
        XCTAssertTrue(step.exists, "…and the step stays")
        e.shot("wsagent2-11-changeset-wrong-password")
        let again = e.app.secureTextFields.firstMatch
        again.tap()
        again.typeText("demo-password")
        e.tapAfterTyping(go)

        // the results: what was applied, what failed, what was skipped, what was left out
        let results = e.element("changeset-results")
        e.expect(results, 30, "the results")
        XCTAssertEqual(results.label, "1 of 3 changes applied")
        XCTAssertTrue(e.containing("One change failed, so the one after it was skipped. The one that was applied stays.").exists, "the partial outcome, said")
        for word in ["applied", "failed", "skipped", "left out by you", "couldn't be applied"] {
            XCTAssertTrue(e.containing(word).exists, "a result reads “\(word)”")
        }
        XCTAssertTrue(e.containing("xbin said: “the tile was changed since the agent proposed this”").exists, "the failure in xbind's words")
        let undo = e.app.buttons.matching(identifier: "changeset-undo").firstMatch
        XCTAssertEqual(undo.label, "Undo the 1 applied…")
        XCTAssertEqual(e.app.buttons.matching(identifier: "changeset-done").firstMatch.label, "Done")
        e.shot("wsagent2-12-changeset-results")

        // Undo undoes nothing by itself: a new set, to review like any other
        undo.tap()
        XCTAssertTrue(e.until(20) { e.element("changeset-heading").exists && e.element("changeset-heading").label == "Your agent proposes 1 admin change" },
                      "Undo proposes a new set of one change")
        XCTAssertTrue(e.app.buttons.matching(identifier: "changeset-tick-1").firstMatch.label.contains("Undo: Give Invoices read access"), "…that undoes what was applied")
        e.shot("wsagent2-13-changeset-undo")
        e.app.buttons.matching(identifier: "changeset-deny").firstMatch.tap()
        e.expect(e.containing("Rejected by you. Nothing was applied."), 20, "Reject all ends it rejected")
        e.app.buttons.matching(identifier: "changeset-done").firstMatch.tap()
    }

    /// A tile agent's start: only xbind's word that the mode asks earns the
    /// green tag. A mode that never asks — and one xbind doesn't vouch for —
    /// leads with the danger wording, and the button reads "Start without
    /// asking".
    @MainActor
    func test05DemoStartsThatNeverAsk() throws {
        let e = try E2E(self)
        e.wsDemo("grants")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let heading = e.element("approval-heading")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        let deny = e.app.buttons.matching(identifier: "approval-deny").firstMatch
        let danger = e.element("approval-danger")

        for (n, id) in ["rq_never", "rq_unknown_mode"].enumerated() {
            e.wsInbox("inbox-request:\(id)")
            e.expect(heading, 30, "the start's approval (\(id))")
            XCTAssertEqual(heading.label, "Start a tile agent in Orders?")
            e.expect(danger, 10, "\(id): the danger wording leads")
            XCTAssertTrue(danger.label.contains("a mode that never asks") && danger.label.contains("stop a single edit or command"), danger.label)
            XCTAssertTrue(e.containing("acts without asking").exists, "\(id): the mode's tag")
            XCTAssertFalse(e.containing("asks before acting").exists, "\(id): never the green tag")
            XCTAssertTrue(e.containing("It edits files and runs commands in Orders without asking you.").exists)
            XCTAssertEqual(allow.label, "Start without asking", id)
            XCTAssertEqual(deny.label, "Deny")
            // the trusted bar's Not now is a whole target too (it was 28 pt high)
            e.wsAssertWholeTarget(e.app.buttons.matching(identifier: "approval-later").firstMatch, "\(id): Not now")
            // the mode's row comes before the tile's: it is what stands between the brief and a change
            let mode = e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Mode")).firstMatch
            let tile = e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Tile")).firstMatch
            XCTAssertTrue(mode.exists && tile.exists, "the Mode and Tile rows")
            XCTAssertLessThan(mode.frame.minY, tile.frame.minY, "\(id): the mode leads the rows")
            XCTAssertLessThan(danger.frame.minY, mode.frame.minY, "\(id): the warning comes first of all")
            if id == "rq_unknown_mode" {
                // its brief and the agent's words hold characters that reorder or hide text: each is shown as its escape
                let noted = e.app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "contains hidden characters — shown as ⟨U+…⟩"))
                XCTAssertGreaterThanOrEqual(noted.count, 2, "the line that says so, with the brief and with the agent's words")
                XCTAssertTrue(e.containing("export the ⟨U+202E⟩invoices⟨U+202C⟩.").exists, "the brief, its bidi controls as escapes")
                XCTAssertTrue(e.containing("⟨U+2066⟩in a mode⟨U+2069⟩").exists, "the agent's words, likewise")
                XCTAssertFalse(e.containing("\u{202E}").exists, "no bidi control is drawn as it is")
            }
            e.shot("wsagent2-\(14 + n)-start-\(id == "rq_never" ? "never-asks" : "unknown-mode")")
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !heading.exists })
        }

        // …and one xbind says asks: the green tag, Start, no warning
        e.wsInbox("inbox-request:rq_sess")
        e.expect(heading, 30, "a start in a mode that asks")
        XCTAssertTrue(e.containing("asks before acting").exists)
        XCTAssertFalse(danger.exists, "no danger wording for a mode that asks")
        XCTAssertEqual(allow.label, "Start")
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
    }

    /// What widens what the agent may do is armed like Allow: a standing
    /// choice, "until I take it back", "Start without asking", the admin
    /// opt-in — inert at first, and narrowing never held.
    @MainActor
    func test06DemoWideningIsArmed() throws {
        let e = try E2E(self)
        e.wsDemo("grants")
        e.app.launchArguments += ["-XbinAgentArmSeconds", "8", "-XbinAgentGate", "allow"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsInbox("inbox-request:rq_call")
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        e.expect(allow, 30, "the approval")
        let once = e.app.buttons.matching(identifier: "approval-choice-once").firstMatch
        let call = e.app.buttons.matching(identifier: "approval-choice-call").firstMatch
        let tile = e.app.buttons.matching(identifier: "approval-choice-tile").firstMatch
        e.wsScrollTo(tile)
        XCTAssertTrue(once.isEnabled, "Once narrows: never held")
        XCTAssertFalse(call.isEnabled, "“Always, this exact action” is not armed when the sheet appears")
        XCTAssertFalse(tile.isEnabled, "“Always, every change” is not armed when the sheet appears")
        tile.tap()
        call.tap()
        XCTAssertEqual(allow.label, "Allow once", "a tap on an unarmed standing choice chose nothing")
        e.shot("wsagent2-16-widening-unarmed")
        XCTAssertTrue(e.until(20) { call.isEnabled && tile.isEnabled }, "the standing choices arm a moment later")
        call.tap()
        // (spoken, the button says which "always": the visible words are "Allow always")
        XCTAssertTrue(e.until(10) { allow.label == "Allow always, this exact action" }, "armed, the choice is taken: \(allow.label)")
        // the duration: the exact action stands "until I take it back"; one hour narrows, and back widens
        let hour = e.app.buttons.matching(identifier: "approval-duration-hour").firstMatch
        let always = e.app.buttons.matching(identifier: "approval-duration-always").firstMatch
        e.wsScrollTo(hour)
        XCTAssertTrue(hour.exists && always.exists, "the two durations")
        XCTAssertTrue(always.wsSelected && !hour.wsSelected, "“Until I take it back” for the exact action")
        hour.tap()
        XCTAssertTrue(e.until(5) { hour.wsSelected }, "one hour narrows: taken at once")
        once.tap()
        XCTAssertTrue(e.until(5) { allow.label == "Allow once" }, "back to Once: taken at once")

        // the wider grant's own sheet: one hour, and "until I take it back" armed like Allow
        tile.tap()
        XCTAssertTrue(e.until(10) { allow.label.hasPrefix("Allow every change to") })
        XCTAssertTrue(e.until(20) { allow.isEnabled }, "Allow arms after the choice")
        allow.tap()
        e.expect(e.element("wider-heading"), 15, "the wider grant's second sheet")
        let wHour = e.app.buttons.matching(identifier: "wider-duration-hour").firstMatch
        let wAlways = e.app.buttons.matching(identifier: "wider-duration-always").firstMatch
        XCTAssertTrue(wHour.wsSelected, "every change: for one hour unless the person widens it")
        XCTAssertFalse(wAlways.isEnabled, "“Until I take it back” is not armed when the sheet appears")
        wAlways.tap()
        XCTAssertTrue(wHour.wsSelected, "a tap on it unarmed changes nothing")
        XCTAssertTrue(e.until(20) { wAlways.isEnabled }, "…it arms a moment later")
        e.app.buttons.matching(identifier: "wider-deny").firstMatch.tap()
        XCTAssertTrue(e.until(10) { !e.element("wider-heading").exists })
        e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
        XCTAssertTrue(e.until(10) { !allow.exists })

        // Preferences (from the shield alone): "Start without asking" and the admin opt-in
        e.wsOpen()
        e.wsChromeMenu("Preferences")
        let ask = e.app.buttons.matching(identifier: "prefs-start-ask").firstMatch
        let start = e.app.buttons.matching(identifier: "prefs-start-allow").firstMatch
        e.expect(start, 30, "Preferences")
        XCTAssertTrue(e.until(10) { ask.wsSelected }, "it asks each time, as xbind has it")
        XCTAssertFalse(start.isEnabled, "“Start without asking” is not armed when the panel appears")
        start.tap()
        XCTAssertTrue(ask.wsSelected, "a tap on it unarmed changes nothing")
        e.shot("wsagent2-17-prefs-unarmed")
        XCTAssertTrue(e.until(20) { start.isEnabled }, "it arms a moment later")
        start.tap()
        XCTAssertTrue(e.until(10) { start.wsSelected }, "armed, it is taken")
        ask.tap()
        XCTAssertTrue(e.until(5) { ask.wsSelected }, "back to asking: taken at once")
        e.app.buttons["Done"].tap()
    }

    /// The grant sheets as VoiceOver reads them: every control says what it
    /// does, and the order is the one a person decides in.
    @MainActor
    func test07DemoSpokenLabelsAndOrder() throws {
        let e = try E2E(self)
        e.wsDemo("grants")
        e.app.launchArguments += ["-XbinAgentGate", "allow"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        // the entry: the bar's item and Home's row say what waits — the row with no badge of its own
        XCTAssertTrue(e.until(60) { e.wsButton.exists && e.wsButton.label == "Workspace agent, 5 need you" }, "the bar's item: \(e.wsButton.label)")
        XCTAssertEqual(e.wsHomeRow.label, "Workspace agent, 5 need you")
        XCTAssertEqual(e.wsHomeRow.value as? String, "idle · 5 need you", "the row keeps its status in words")
        XCTAssertFalse(e.wsHomeRow.staticTexts["5"].exists, "…and has no count badge of its own")
        XCTAssertEqual(e.app.buttons["Needs you"].value as? String, "5", "Needs you carries the count")

        e.wsInbox("inbox-request:rq_call")
        let heading = e.element("approval-heading")
        e.expect(heading, 30, "the approval")
        XCTAssertEqual(heading.label, "Your agent wants to change something in Invoices")
        let bar = e.element("approval-bar")
        XCTAssertEqual(bar.label, "xbin approval, asking \(e.server.userName)", "the bar, as it is read")
        let later = e.app.buttons.matching(identifier: "approval-later").firstMatch
        XCTAssertEqual(later.label, "Not now")
        let deny = e.app.buttons.matching(identifier: "approval-deny").firstMatch
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        XCTAssertEqual(deny.label, "Deny")
        XCTAssertEqual(allow.label, "Allow once")
        let row: (String) -> XCUIElement = { name in
            e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", name + ",")).firstMatch
        }
        XCTAssertTrue(row("Tile").label.contains("Invoices") && row("Tile").label.contains("apps/invoices") && row("Tile").label.contains("you can write here"), row("Tile").label)
        XCTAssertTrue(row("Action").label.contains("POST apps/invoices/links/rebuild?dry=0"), "the method and path, in full: \(row("Action").label)")
        XCTAssertTrue(row("Sends").label.contains("\"customer\":\"acme\"") && row("Sends").label.contains("shown in full"), row("Sends").label)
        XCTAssertTrue(row("Undo").label.contains("xbin can't undo this"), row("Undo").label)
        let quote = e.containing("The agent's own words, not checked by xbin:")
        XCTAssertTrue(quote.label.hasSuffix("Rebuild the customer links so the overdue ones show."), quote.label)
        let once = e.app.buttons.matching(identifier: "approval-choice-once").firstMatch
        let call = e.app.buttons.matching(identifier: "approval-choice-call").firstMatch
        let tile = e.app.buttons.matching(identifier: "approval-choice-tile").firstMatch
        XCTAssertEqual(once.label, "Once. This request only. The agent has 15 minutes to use it.")
        XCTAssertTrue(once.wsSelected, "Once is the choice the sheet opens with, and says so")
        XCTAssertTrue(call.label.hasPrefix("Always, this exact action. POST to apps/invoices/links/rebuild on Invoices"), call.label)
        XCTAssertTrue(tile.label.hasPrefix("Always, every change to Invoices, wider. "), "the third says it is wider: \(tile.label)")

        // the order a person decides in: heading → Tile → Action → Sends → undo → the agent's words → the choices → Deny → Allow
        let order: [(String, XCUIElement)] = [("heading", heading), ("Tile", row("Tile")), ("Action", row("Action")), ("Sends", row("Sends")),
                                              ("Undo", row("Undo")), ("the agent's words", quote), ("Once", once), ("this exact action", call),
                                              ("every change", tile)]
        for (a, b) in zip(order, order.dropFirst()) {
            XCTAssertLessThan(a.1.frame.minY, b.1.frame.minY, "\(a.0) comes before \(b.0)")
        }
        XCTAssertLessThan(bar.frame.minY, heading.frame.minY, "the bar that says whose sheet it is comes first")
        XCTAssertLessThan(deny.frame.midX, allow.frame.midX, "Deny, then Allow")
        XCTAssertTrue(e.wsAtSheetBottom(deny) && e.wsAtSheetBottom(e.app.buttons.matching(identifier: "approval-allow").firstMatch), "both at the bottom: \(deny.frame)")
        // …and the same order in the accessibility tree, where XCUITest exposes it
        let ids = ["approval-bar", "approval-heading", "approval-choice-once", "approval-choice-call", "approval-choice-tile", "approval-deny", "approval-allow"]
        XCTAssertEqual(e.wsTreeOrder(of: ids), ids, "the tree's order")

        // Allow's words follow the choice
        XCTAssertTrue(e.until(15) { call.isEnabled })
        e.wsScrollTo(tile)
        call.tap()
        XCTAssertTrue(e.until(10) { allow.label == "Allow always, this exact action" }, "Allow, spoken: \(allow.label)")
        XCTAssertEqual(e.app.buttons.matching(identifier: "approval-duration-hour").firstMatch.label, "For one hour")
        XCTAssertEqual(e.app.buttons.matching(identifier: "approval-duration-always").firstMatch.label, "Until I take it back")
        tile.tap()
        XCTAssertTrue(e.until(10) { allow.label == "Allow every change to Invoices" }, "Allow, spoken: \(allow.label)")
        XCTAssertTrue(e.until(15) { allow.isEnabled })
        allow.tap()
        e.expect(e.element("wider-heading"), 15, "the wider sheet")
        XCTAssertEqual(e.element("wider-heading").label, "Allow every change to Invoices?")
        let back = e.app.buttons.matching(identifier: "wider-deny").firstMatch, every = e.app.buttons.matching(identifier: "wider-allow").firstMatch
        XCTAssertEqual(back.label, "Go back")
        XCTAssertEqual(every.label, "Allow every change to Invoices", "the button says what it allows, not “button”")
        XCTAssertEqual(e.app.buttons.matching(identifier: "wider-duration-hour").firstMatch.label, "For one hour")
        back.tap()
        XCTAssertTrue(e.until(10) { !e.element("wider-heading").exists })
        later.tap()
        XCTAssertTrue(e.until(10) { !heading.exists })

        // the shield and its items
        e.wsOpen()
        let menu = e.app.buttons.matching(identifier: "agent-chrome-menu").firstMatch
        XCTAssertEqual(menu.label, "xbin's controls for your agent")
        XCTAssertEqual(e.wsClose.label, "Close the workspace agent")
        menu.tap()
        for item in ["Needs you (5)", "Approvals", "What my agent did", "Preferences", "Pause my agent"] {
            XCTAssertTrue(e.app.buttons[item].waitForExistence(timeout: 10), "the shield's “\(item)”")
        }
        e.shot("wsagent2-18-shield-menu")
        e.app.buttons["Approvals"].tap()
        e.expect(e.app.buttons.matching(identifier: "approval-takeback:ap_1").firstMatch, 20, "the standing approvals")
        XCTAssertEqual(e.app.buttons.matching(identifier: "approval-takeback:ap_1").firstMatch.label, "Take back")
        XCTAssertTrue(e.containing("Always · this action: POST apps/invoices/links/rebuild").exists)
        e.app.buttons["Done"].tap()
        e.wsChromeMenu("What my agent did")
        e.expect(e.containing("You allowed a request"), 20, "the log in sentences")
        // (the list's closing note: a lazy list has it once it is scrolled to)
        let note = e.containing("can't read or change this list")
        // drag inside the list (on an iPad it is a form sheet in the middle of the screen,
        // whose bottom is above the screen's lower quarter)
        let listRow = e.containing("You allowed a request").frame
        let middle = CGPoint(x: listRow.midX, y: listRow.midY)
        let list = e.app.collectionViews.allElementsBoundByIndex.last { $0.frame.contains(middle) } ?? e.app
        var swipes = 0
        while !note.exists, swipes < 12 {
            list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)).press(forDuration: 0.05, thenDragTo: list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)))
            swipes += 1
        }
        XCTAssertTrue(note.exists, "…and whose it is")
        e.shot("wsagent2-19-activity")
        e.app.buttons["Done"].tap()
    }

    /// The largest text size: nothing of a grant is cut, the rows stack,
    /// Deny and Allow stack with Allow below, and both can be reached.
    @MainActor
    func test08DemoLargestText() throws {
        let e = try E2E(self)
        e.wsDemo("grants")
        e.app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL", "-XbinAgentGate", "allow"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let window = e.app.windows.firstMatch.frame
        let deny = e.app.buttons.matching(identifier: "approval-deny").firstMatch
        let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
        let heading = e.element("approval-heading")
        let later = e.app.buttons.matching(identifier: "approval-later").firstMatch

        func checkButtons(_ what: String, deny: XCUIElement, allow: XCUIElement) {
            XCTAssertTrue(deny.exists && allow.exists, "\(what): both buttons")
            XCTAssertGreaterThanOrEqual(allow.frame.minY, deny.frame.maxY - 1, "\(what): stacked, Allow below Deny (\(deny.frame) / \(allow.frame))")
            XCTAssertTrue(deny.isHittable && allow.isHittable, "\(what): both can be reached")
            XCTAssertLessThanOrEqual(allow.frame.maxY, window.maxY, "\(what): Allow is on the screen")
            // (the sheet's whole width — a phone's screen, an iPad's centred sheet: the same margin on both sides)
            XCTAssertLessThan(abs(deny.frame.minX - (window.maxX - deny.frame.maxX)), 2, "\(what): full-width buttons (\(deny.frame) in \(window))")
            if !e.isPad { XCTAssertGreaterThanOrEqual(deny.frame.width, window.width * 0.8, "\(what): full-width buttons") }
            XCTAssertGreaterThanOrEqual(deny.frame.height, 44, "\(what): a full touch target")
        }

        // a tile call
        e.wsInbox("inbox-request:rq_call")
        e.expect(heading, 30, "the approval at the largest text size")
        XCTAssertTrue(heading.isHittable, "the heading shows")
        XCTAssertGreaterThan(heading.frame.height, 60, "…wrapped onto several lines, not cut: \(heading.frame)")
        XCTAssertLessThanOrEqual(heading.frame.maxX, window.maxX, "…within the sheet")
        XCTAssertTrue(later.isHittable, "Not now can be reached")
        XCTAssertLessThan(e.element("approval-bar").frame.maxY, later.frame.maxY + 1, "the bar's words sit above its Not now: stacked")
        checkButtons("a tile call", deny: deny, allow: allow)
        e.shot("wsagent2-20-large-approval-top")
        // the rows stack label above value; the choices are reachable by scrolling
        let action = e.app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "Action,")).firstMatch
        XCTAssertTrue(action.exists)
        let tile = e.app.buttons.matching(identifier: "approval-choice-tile").firstMatch
        e.wsScrollTo(tile)
        XCTAssertTrue(tile.isHittable, "the last choice can be scrolled to")
        XCTAssertLessThanOrEqual(tile.frame.maxX, window.maxX, "…and fits the width")
        e.shot("wsagent2-21-large-approval-choices")
        XCTAssertTrue(e.until(15) { tile.isEnabled })
        tile.tap()
        XCTAssertTrue(e.until(15) { allow.isEnabled && allow.label.hasPrefix("Allow every change") })
        allow.tap()
        let wHeading = e.element("wider-heading")
        e.expect(wHeading, 15, "the wider sheet at the largest text size")
        let back = e.app.buttons.matching(identifier: "wider-deny").firstMatch, every = e.app.buttons.matching(identifier: "wider-allow").firstMatch
        checkButtons("the wider sheet", deny: back, allow: every)
        e.shot("wsagent2-22-large-wider")
        back.tap()
        XCTAssertTrue(e.until(10) { !wHeading.exists })
        later.tap()
        XCTAssertTrue(e.until(10) { !heading.exists })

        // a tile agent's start, in a mode that never asks
        e.wsInbox("inbox-request:rq_never")
        e.expect(heading, 30, "the start's approval at the largest text size")
        let danger = e.element("approval-danger")
        XCTAssertTrue(danger.exists && danger.isHittable, "the danger wording shows first")
        checkButtons("a tile agent's start", deny: deny, allow: allow)
        XCTAssertEqual(allow.label, "Start without asking")
        e.shot("wsagent2-23-large-start-top")
        let told = e.containing("sent exactly like this")
        e.wsScrollTo(told)
        XCTAssertTrue(told.isHittable, "what it will be told can be scrolled to")
        e.shot("wsagent2-24-large-start-brief")
        later.tap()
        XCTAssertTrue(e.until(10) { !heading.exists })

        // an admin batch
        e.wsInbox("inbox-changeset:cs_1")
        e.expect(e.element("changeset-heading"), 30, "the batch at the largest text size")
        checkButtons("an admin batch", deny: e.app.buttons.matching(identifier: "changeset-deny").firstMatch,
                     allow: e.app.buttons.matching(identifier: "changeset-allow").firstMatch)
        e.shot("wsagent2-25-large-changeset")
        let second = e.app.buttons.matching(identifier: "changeset-tick-2").firstMatch
        e.wsScrollTo(second)
        XCTAssertTrue(second.isHittable, "a change's tick can be scrolled to")
        e.shot("wsagent2-26-large-changeset-item")
        later.tap()
    }

    // MARK: Live — who has an agent, and its switches

    /// xbind's answers decide what shows: an admin who gave others an agent
    /// and not themselves sees nothing of it (and a link to it lands on
    /// Home); with nobody chosen yet they see the setup's third step; given
    /// one again, the agent.
    @MainActor
    func test11LiveWhoHasAnAgent() async throws {
        try requireAgent()
        let e = try E2E(self)
        let server = e.server
        _ = try await server.wsPrepare("E2E who")
        addTeardownBlock { try? await server.wsGive(who: "everyone"); await server.wsDropOther() }

        // 1. others have one, the admin doesn't
        try await server.wsGiveOnlyAnother()
        let state = try await server.wsState()
        XCTAssertEqual(state["access"] as? Bool, false, "xbind: the admin role alone gives no agent")
        XCTAssertNil(state["agent"], "…and names no component to someone not given one")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        let needs = e.app.buttons["Needs you"]
        e.expect(needs, 60, "Home")
        XCTAssertFalse(e.until(8) { e.wsHomeRow.exists || e.wsButton.exists }, "no row, no button: administering it isn't having one")
        e.wsOpenLink("xbin://\(server.authority)/c/xbin/wsagent")
        XCTAssertFalse(e.until(6) { e.wsAcceptOpenPrompt(); return e.wsClose.exists }, "a link to the agent opens nothing")
        // (Home's row is there throughout; what can cover it is a system prompt that came late — answered by what is up)
        let landed = e.until(20) { e.wsAcceptOpenPrompt(); e.wsDismissSavePassword(); return needs.exists && needs.isHittable }
        if !landed { e.shot("fail-who-link") }
        XCTAssertTrue(landed, "…it lands on Home")
        e.shot("wsagent2-27-live-admin-without-one")
        e.app.terminate()

        // 2. nobody was given one yet: the setup's third step, for the admin
        await server.wsDropOther()
        let chosen = try await server.wsState()
        XCTAssertEqual((chosen["who"] as? [String: Any])?["chosen"] as? Bool, false)
        let e2 = try E2E(self)
        e2.launch()
        e2.ensureWorkspace()
        e2.goHome()
        e2.expect(e2.wsHomeRow, 60, "Home's row: the setup")
        XCTAssertEqual(e2.wsHomeRow.label, "Set up the workspace agent")
        XCTAssertTrue(e2.wsHittable(e2.wsHomeRow), "the row can be tapped")
        e2.wsHomeRow.tap()
        e2.expect(e2.element("agent-setup"), 30, "the three steps")
        XCTAssertTrue(e2.containing("Connected.").exists, "a model is connected: the first step is done")
        XCTAssertTrue(e2.containing("Nobody has one yet").exists, "the third is not")
        e2.shot("wsagent2-28-live-setup-who")
        e2.app.terminate()

        // 3. everyone again: the agent
        try await server.wsGive(who: "everyone")
        let e3 = try E2E(self)
        e3.launch()
        e3.ensureWorkspace()
        e3.goHome()
        e3.expect(e3.wsHomeRow, 60, "Home's row for the agent")
        XCTAssertEqual(e3.wsHomeRow.label, "Workspace agent")
    }

    /// Workspace agents turned off while the agent is open: the pane says
    /// which switch it is and whose; back on, the conversation returns.
    @MainActor
    func test12LiveTurnedOffForTheWorkspace() async throws {
        try requireAgent()
        let e = try E2E(self)
        let server = e.server
        _ = try await server.wsPrepare("E2E off")
        addTeardownBlock { try? await server.wsSwitch(on: true) }
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        e.expect(e.wsComposer, 90, "the conversation, before the switch")
        try await server.wsSwitch(on: false)
        let title = e.element("agent-state")
        e.expect(title, 30, "the pane follows the switch")
        XCTAssertEqual(title.label, "Turned off for this workspace.")
        XCTAssertTrue(e.containing("An admin turned workspace agents off at").exists, "whose switch, and when")
        XCTAssertTrue(e.containing("kept for when it is turned on again").exists, "nothing is lost")
        XCTAssertFalse(e.wsComposer.exists, "no composer: nothing can be sent")
        e.shot("wsagent2-29-live-turned-off")
        try await server.wsSwitch(on: true)
        e.expect(e.wsComposer, 60, "turned on again: the conversation is back")
    }

    /// Words typed and not sent stay in the composer through a pause: in
    /// view, read-only, above the app's paused bar — the one place that
    /// says it is paused, covering nothing — and editable again after
    /// Resume. Nothing was sent, nothing queued.
    @MainActor
    func test13LiveDraftSurvivesAPause() async throws {
        try requireAgent()
        for mode in ["light", "dark"] {
            let e = try E2E(self)
            let run = try await e.server.wsPrepare("E2E draft")
            let before = try await e.server.wsMessageCount(run)
            e.app.launchArguments += ["-XbinAppearance", mode]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.wsOpen()
            e.expect(e.wsComposer, 90, "the composer")
            // (the round before resumed the agent: the view takes words once its backend has heard so)
            XCTAssertTrue(e.until(60) { e.wsComposer.isEnabled }, "the composer takes words")
            e.wsComposer.tap()
            e.wsComposer.typeText("Tell me what changed in Note")
            _ = try await e.server.send("PUT", "/api/xbin/delegate/state", json: ["paused": true])
            let resume = e.app.buttons.matching(identifier: "agent-resume").firstMatch
            e.expect(resume, 30, "paused from elsewhere: the app's Resume")
            XCTAssertEqual(resume.label, "Resume my agent")
            let kept = e.app.descendants(matching: .any).matching(NSPredicate(format: "value CONTAINS %@", "Tell me what changed in Note")).firstMatch
            e.expect(kept, 30, "paused: the words are still in view")
            XCTAssertTrue(e.until(20) { !kept.isEnabled }, "…read-only while paused")
            let bar = e.element("agent-paused")
            XCTAssertTrue(kept.frame.maxY <= bar.frame.minY + 1, "the draft sits above the bar, not under it: \(kept.frame) \(bar.frame)")
            let said = e.app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Paused by you")).count
            XCTAssertEqual(said, 1, "one place says it is paused")
            XCTAssertTrue(e.containing("can't read or change anything").exists || e.containing("can’t read or change anything").exists, "…and what that means")
            e.shot("wsagent2-30-live-paused-with-draft-\(mode)")
            resume.tap()
            XCTAssertTrue(e.until(60) { kept.exists && kept.isEnabled }, "resumed: the words are still in the composer, editable")
            let after = try await e.server.wsMessageCount(run)
            XCTAssertEqual(after, before, "nothing was sent meanwhile")
            e.app.terminate()
        }
    }

    /// An admin batch, live: proposed by the scripted model, reviewed and
    /// applied in the app (a `--dev` xbind asks for no recent sign-in), its
    /// card settled, and undone through a new set.
    @MainActor
    func test14LiveChangeSet() async throws {
        try requireAgent()
        let e = try E2E(self)
        let server = e.server
        let run = try await server.wsPrepare("E2E admin changes")
        _ = try await server.send("PUT", "/api/xbin/delegate/state", json: ["adminChanges": true])
        addTeardownBlock { await server.wsCleanChangeSets(); _ = try? await server.send("PUT", "/api/xbin/delegate/state", json: ["adminChanges": false]) }
        let ref = try await server.wsProposeGrants(run)
        e.app.launchArguments += ["-XbinAgentGate", "allow"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the batch's card, drawn by the app")
        XCTAssertTrue(e.containing("2 admin changes proposed").exists, "what waits, in xbind's count")
        XCTAssertEqual(review.label, "Review: 2 admin changes proposed", "the button says what it reviews")
        e.shot("wsagent2-31-live-changeset-card")
        review.tap()
        let heading = e.element("changeset-heading")
        e.expect(heading, 30, "the review")
        XCTAssertEqual(heading.label, "Your agent proposes 2 admin changes")
        XCTAssertTrue(e.containing("Let apps/welcome and apps/counter use apps/note").exists, "the agent's title, quoted")
        let first = e.app.buttons.matching(identifier: "changeset-tick-1").firstMatch
        XCTAssertTrue(first.label.hasPrefix("Change 1 of 2, selected."), first.label)
        e.shot("wsagent2-32-live-changeset-review")
        let apply = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
        XCTAssertEqual(apply.label, "Apply 2 changes…")
        XCTAssertTrue(e.until(15) { apply.isEnabled }, "Apply arms")
        apply.tap()
        let results = e.element("changeset-results")
        e.expect(results, 30, "the results")
        XCTAssertEqual(results.label, "All 2 changes applied")
        await e.eventually("both grants exist at xbind", timeout: 15) { try await server.wsGrantsToNote().count == 2 }
        e.shot("wsagent2-33-live-changeset-results")
        let undo = e.app.buttons.matching(identifier: "changeset-undo").firstMatch
        XCTAssertEqual(undo.label, "Undo the 2 applied…")
        undo.tap()
        XCTAssertTrue(e.until(20) { heading.exists && heading.label == "Your agent proposes 2 admin changes" }, "Undo opens a new set to review")
        let still = try await server.wsGrantsToNote()
        XCTAssertEqual(still.count, 2, "Undo undid nothing by itself")
        let again = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
        XCTAssertTrue(e.until(15) { again.isEnabled })
        again.tap()
        e.expect(results, 30, "the undo's results")
        await e.eventually("the grants are gone again", timeout: 15) { try await server.wsGrantsToNote().isEmpty }
        e.app.buttons.matching(identifier: "changeset-done").firstMatch.tap()
        // settled, the card's one button reads what came of it — never Review…
        XCTAssertTrue(e.until(20) { review.exists && review.label.hasPrefix("Results: ") }, "the card has no Review… any more: \(review.label)")
    }

    /// xbind out of reach, with a conversation loaded: it stays readable
    /// under xbin's own line, Review… waits for the connection, and back in
    /// reach it works again. (Demo `offline`: the chrome's requests fail
    /// once the app has been to the background; the conversation is live.)
    @MainActor
    func test15LiveOfflineKeepsTheConversation() async throws {
        try requireAgent()
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E offline")
        let ref = try await e.server.wsFileNoteRequest(run, note: "while offline")
        e.wsDemo("offline")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card, loaded while in reach")
        XCTAssertTrue(review.isEnabled)
        XCUIDevice.shared.press(.home)
        _ = e.app.otherElements["xbin-e2e-never"].waitForExistence(timeout: 2)
        e.app.activate()
        let line = e.element("agent-offline")
        e.expect(line, 30, "xbin's line: it can't be reached")
        XCTAssertEqual(line.label, "Can't reach xbin.")
        XCTAssertTrue(e.containing("Showing what was loaded before. Approvals need a connection.").exists)
        XCTAssertTrue(review.exists, "the conversation and its card stay")
        XCTAssertTrue(e.until(10) { !review.isEnabled }, "Review… waits for the connection")
        XCTAssertTrue(e.containing("Approvals need a connection.").exists)
        XCTAssertTrue(e.app.buttons.matching(identifier: "agent-offline-retry").firstMatch.exists, "Try now")
        e.shot("wsagent2-34-live-offline")
        let state = try await e.server.wsRequestState(ref)
        XCTAssertEqual(state, "pending", "nothing was decided")
    }

    // MARK: Live — what the agent's view may ask, and a draft it hands over

    /// The view may ask the chrome only for views that change nothing: an
    /// ask for Approvals opens them; an ask for Preferences opens nothing.
    /// (`-XbinAgentDebugAsk`: a message as if the view had sent it, through
    /// the same decision and host.)
    @MainActor
    func test16LiveTheViewCannotOpenPreferences() async throws {
        try requireAgent()
        for ask in ["approvals", "prefs"] {
            let e = try E2E(self)
            _ = try await e.server.wsPrepare("E2E ask \(ask)")
            e.app.launchArguments += ["-XbinAgentDebugAsk", ask]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.wsOpen()
            e.expect(e.wsComposer, 90, "the conversation")
            if ask == "approvals" {
                e.expect(e.containing("asked every time"), 30, "an ask for Approvals opens them")
                XCTAssertTrue(e.app.navigationBars["Approvals"].exists)
                e.app.buttons["Done"].tap()
            } else {
                XCTAssertFalse(e.until(8) { e.app.navigationBars["Preferences"].exists || e.app.buttons.matching(identifier: "prefs-start-allow").firstMatch.exists },
                               "an ask for Preferences opens nothing")
                // …they open from the app's own menu alone
                e.wsChromeMenu("Preferences")
                e.expect(e.app.buttons.matching(identifier: "prefs-start-allow").firstMatch, 20, "Preferences, from the shield")
                e.app.buttons["Done"].tap()
            }
            e.app.terminate()
        }
    }

    /// A draft the agent's view hands to a tile's own agent is shown as the
    /// page's suggestion — never as the person's words — is cleaned to plain
    /// text, waits unsent, and is theirs once they edit it.
    @MainActor
    func test17LiveASuggestedDraftIsLabelled() async throws {
        try requireAgent()
        let e = try E2E(self)
        _ = try await e.server.wsPrepare("E2E draft handed over")
        e.app.launchArguments += ["-XbinAgentDebugAsk", "draft"]
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        // the tile's own agent, behind the sheet: its launcher, with what waits for the session picked
        let label = e.element("agent-draft-suggested")
        e.expect(label, 90, "the suggested draft, labelled, on the launcher")
        XCTAssertTrue(label.label.contains("Suggested by your agent’s page — not your words"), label.label)
        XCTAssertTrue(label.label.contains("Rename the note’s heading to “Shopping”.\nKeep the rest as it is."), "the text, line ends made plain: \(label.label)")
        XCTAssertFalse(label.label.contains("\u{202E}"), "…and nothing invisible left in it")
        XCTAssertTrue(label.label.contains("nothing is sent until you press Send"), label.label)
        e.shot("wsagent2-35-live-draft-launcher")
        // the view asked to SHOW a launcher: nothing was started in the tile by that
        let started = try await e.server.sessions(cwd: "apps/note").filter { $0.kind == "agent" }
        XCTAssertTrue(started.isEmpty, "the view's ask created no session: \(started.map(\.id))")
        if e.wsClose.exists, !e.isPad { e.wsClose.tap() }
        // (the launcher's "Fake agent (tests)" — not the agent column's model, "fake-chat", beside it on an iPad)
        let fake = e.app.buttons.matching(NSPredicate(format: "label CONTAINS[c] %@", "fake agent")).firstMatch
        e.expect(fake, 30, "the launcher's coding agents")
        fake.tap()
        var session = ""
        await e.eventually("the session the person started", timeout: 60) {
            session = try await e.server.sessions(cwd: "apps/note").first { $0.kind == "agent" }?.id ?? ""
            return !session.isEmpty
        }
        let composer = e.app.descendants(matching: .any).matching(NSPredicate(format: "value CONTAINS %@", "Rename the note’s heading")).firstMatch
        e.expect(composer, 60, "the draft waits in that session's composer")
        XCTAssertTrue(label.exists, "…still labelled as the page's suggestion")
        e.shot("wsagent2-36-live-draft-composer")
        let turn = try await e.server.wsSessionTurn(session)
        XCTAssertEqual(turn, 0, "handing it over sent nothing: the session has taken no prompt")
        // the person edits it: the words are theirs now
        composer.tap()
        composer.typeText(" Thanks.")
        XCTAssertTrue(e.until(10) { !label.exists }, "edited, it is no longer labelled as suggested")
        await e.server.end(session)
    }

    /// Paused, nothing can be allowed: the approval says why in the place
    /// of Allow, and Deny still works (xbind refuses an Allow then with 409
    /// {refusal: paused}; a pause keeps the request pending).
    @MainActor
    func test18LiveNothingIsAllowedWhilePaused() async throws {
        try requireAgent()
        let e = try E2E(self)
        let server = e.server
        let run = try await server.wsPrepare("E2E stopped")
        let ref = try await server.wsFileNoteRequest(run, note: "while paused")
        addTeardownBlock { _ = try? await server.send("PUT", "/api/xbin/delegate/state", json: ["paused": false]) }
        _ = try await server.send("PUT", "/api/xbin/delegate/state", json: ["paused": true])
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.wsOpen()
        let review = e.wsReview(ref)
        e.expect(review, 90, "the request's card, while paused")
        review.tap()
        let stopped = e.element("approval-stopped")
        e.expect(stopped, 30, "why, where Allow would be")
        XCTAssertTrue(stopped.label.contains("Your agent is paused. Resume it first"), stopped.label)
        XCTAssertTrue(stopped.label.contains("You can still deny it."), stopped.label)
        XCTAssertFalse(e.app.buttons.matching(identifier: "approval-allow").firstMatch.exists, "no Allow while paused")
        e.shot("wsagent2-37-live-paused-approval")
        let pending = try await server.wsRequestState(ref)
        XCTAssertEqual(pending, "pending", "a pause keeps the request")
        let deny = e.app.buttons.matching(identifier: "approval-deny").firstMatch
        XCTAssertTrue(deny.isEnabled, "Deny works while paused")
        deny.tap()
        await e.eventually("denied at xbind", timeout: 15) { try await server.wsRequestState(ref) == "denied" }
    }

    // MARK: The bar's button, everywhere

    /// The agent's button on the bar in each state a person meets it in — a
    /// count, paused, not set up — on Home and over a tile: there, tappable,
    /// the size of the bar's other items, inside the window. On an iPhone Duo (D200) the
    /// bar is the system's vertical bar at the screen's side, which the
    /// button must never leave (the owner: "always visible on the duo status
    /// sidebar"). Shots: `wsagent3-<light|dark>-NN-<state>-<where>.png`.
    @MainActor
    func test21DemoBarButtonEverywhere() throws {
        for mode in ["light", "dark"] {
            var n = 0
            for (state, spoken) in [("grants", "Workspace agent, 5 need you"), ("paused", "Workspace agent, paused"),
                                    ("setup", "Set up the workspace agent")] {
                let e = try E2E(self)
                e.wsDemo(state)
                e.app.launchArguments += ["-XbinAppearance", mode]
                e.launch()
                e.ensureWorkspace()
                e.goHome()
                for place in ["home", "tile"] {
                    if place == "tile" {
                        e.openTile("apps/counter")
                        e.expect(e.app.buttons["+1"], 60, "the counter")
                    }
                    e.expect(e.wsButton, 60, "the bar's agent button (\(state), \(place))")
                    XCTAssertTrue(e.until(20) { e.wsButton.label == spoken }, "\(state), \(place): \(e.wsButton.label)")
                    XCTAssertTrue(e.wsHittable(e.wsButton), "\(state), \(place): the button can be tapped")
                    e.wsAssertBarItem(e.wsButton, "\(state), \(place): the agent's button")
                    if place == "tile" {
                        // Back fits its item: in a Duo's vertical bar, a glyph
                        // (its "‹ Home" spilled out of the bar's capsule)
                        let back = e.app.buttons.matching(identifier: "panel-back").firstMatch
                        e.wsAssertBarItem(back, "\(state): Back")
                        if e.wsVerticalBar {
                            XCTAssertLessThanOrEqual(back.frame.width, e.app.buttons["Workspaces"].frame.width + 0.5,
                                                     "\(state): Back as narrow as the vertical bar's other items: \(back.frame)")
                        }
                    }
                    n += 1
                    e.shot(String(format: "wsagent3-%@-%02d-%@-%@", mode, n, state, place))
                }
                // …and it opens the agent from the tile
                e.wsButton.tap()
                e.expect(e.wsClose, 30, "the agent's container from the tile (\(state))")
                e.app.terminate()
            }
        }
    }

    // MARK: The gallery

    /// The states and the sheets in light and dark:
    /// `wsagent2-<light|dark>-NN-<state>.png`.
    @MainActor
    func test20Gallery() throws {
        for mode in ["light", "dark"] {
            var n = 0
            func launch(_ demo: String, _ extra: [String] = []) throws -> E2E {
                let e = try E2E(self)
                e.wsDemo(demo)
                e.app.launchArguments += ["-XbinAppearance", mode, "-XbinAgentGate", "allow"] + extra
                e.launch()
                e.ensureWorkspace()
                e.goHome()
                return e
            }
            func shot(_ e: E2E, _ name: String) {
                n += 1
                e.shot(String(format: "wsagent2-%@-%02d-%@", mode, n, name))
            }
            // in place of the conversation
            for state in ["setup", "setup-who", "off", "nomodel", "locked", "older"] {
                let e = try launch(state)
                if state == "setup" {
                    e.expect(e.wsHomeRow, 60, "Home's row")
                    shot(e, "home-setup")
                }
                e.wsOpen()
                e.expect(state.hasPrefix("setup") ? e.element("agent-setup") : e.element("agent-state"), 30, "the \(state) state")
                shot(e, state)
                e.app.terminate()
            }
            // the grants
            let e = try launch("grants")
            XCTAssertTrue(e.until(60) { e.wsButton.exists && e.wsButton.label.contains("5 need you") })
            shot(e, "home-one-badge")
            e.app.buttons["Needs you"].tap()
            e.expect(e.app.buttons.matching(identifier: "inbox-changeset:cs_1").firstMatch, 30, "the inbox")
            shot(e, "inbox")
            e.app.buttons.matching(identifier: "inbox-request:rq_never").firstMatch.tap()
            e.expect(e.element("approval-danger"), 30, "a start that never asks")
            shot(e, "start-never-asks")
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists })
            e.wsInbox("inbox-request:rq_unknown_mode")
            e.expect(e.containing("contains hidden characters"), 30, "text with hidden characters")
            shot(e, "start-hidden-characters")
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists })
            e.wsInbox("inbox-changeset:cs_1")
            e.expect(e.element("changeset-heading"), 30, "the batch")
            shot(e, "changeset-review")
            let fourth = e.app.buttons.matching(identifier: "changeset-tick-4").firstMatch
            e.wsScrollTo(fourth)
            shot(e, "changeset-warnings")
            fourth.tap()
            let apply = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
            XCTAssertTrue(e.until(15) { apply.isEnabled })
            apply.tap()
            e.expect(e.element("changeset-stepup"), 30, "the password step")
            shot(e, "changeset-stepup")
            let field = e.app.secureTextFields.firstMatch
            field.tap()
            field.typeText("demo-password")
            e.tapAfterTyping(e.app.buttons.matching(identifier: "stepup-allow").firstMatch)
            e.expect(e.element("changeset-results"), 30, "the results")
            shot(e, "changeset-results")
            e.app.buttons.matching(identifier: "changeset-done").firstMatch.tap()
            e.app.terminate()
            // the largest text size
            let big = try launch("grants", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
            big.wsInbox("inbox-request:rq_call")
            big.expect(big.element("approval-heading"), 30, "the approval, large")
            shot(big, "large-approval")
            big.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(big.until(10) { !big.element("approval-heading").exists })
            big.wsInbox("inbox-request:rq_never")
            big.expect(big.element("approval-danger"), 30, "the start, large")
            shot(big, "large-start-never-asks")
            big.app.terminate()
        }
    }
}
