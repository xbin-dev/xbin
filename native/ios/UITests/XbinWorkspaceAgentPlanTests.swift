import XCTest

/// A plan's review in the app (D238; docs/protocol/delegation.md §Plans):
/// the same sheet as an admin batch, by the rules the web's dialog follows
/// (XbinCore PlanWords). On a Debug build's `-XbinAgentDemo plan |
/// plan-user | plan-from` — what xbind would answer, from the corpus the
/// words' vectors are made of — in light and in dark:
///
/// - an admin who never opted in: the personal lines ticked, the admin's
///   line not tickable until "Let my agent propose admin changes" (offered
///   on the review, armed) is on — which ticks nothing by itself; a line
///   says what it needs and what needs it, and unticking it unticks those;
///   a start line shows the coding agent, the mode and the brief;
/// - a person who is no admin: "needs an admin", xbind's line only, never
///   tickable; Ask an admin after the rest is applied;
/// - an admin asked by someone else: who asks, a warned line unticked.
///
/// Screenshots: `wsplan-<light|dark>-NN-<state>.png`.
final class XbinWorkspaceAgentPlanTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    func test01DemoPlanReview() throws {
        for mode in ["light", "dark"] {
            var n = 0
            func launch(_ demo: String) throws -> E2E {
                let e = try E2E(self)
                e.wsDemo(demo)
                e.app.launchArguments += ["-XbinAppearance", mode, "-XbinAgentGate", "allow"]
                e.launch()
                e.ensureWorkspace()
                e.goHome()
                return e
            }
            func shot(_ e: E2E, _ name: String) {
                n += 1
                e.shot(String(format: "wsplan-%@-%02d-%@", mode, n, name))
            }
            func tick(_ e: E2E, _ i: Int) -> XCUIElement { e.app.buttons.matching(identifier: "changeset-tick-\(i)").firstMatch }

            // ---- an admin's plan ----
            var e = try launch("plan")
            e.wsInbox("inbox-changeset:pl_dice")
            let heading = e.element("changeset-heading")
            e.expect(heading, 30, "the plan's review")
            XCTAssertEqual(heading.label, "Your agent proposes 5 changes")
            XCTAssertTrue(e.containing("xbin plan, asking").exists || e.containing("XBIN · PLAN").exists, "the bar says it is xbin's sheet for a plan")
            XCTAssertTrue(tick(e, 1).label.hasPrefix("Change 1 of 5, selected. Create the tile apps/dice"), tick(e, 1).label)
            XCTAssertTrue(tick(e, 1).label.contains("Needed by line 4 and 5") && tick(e, 1).label.contains("Before: nothing at apps/dice"), tick(e, 1).label)
            XCTAssertTrue(tick(e, 3).label.hasPrefix("Change 3 of 5, can't be selected."), "an admin's line before the opt-in: \(tick(e, 3).label)")
            XCTAssertTrue(tick(e, 3).label.contains("admin") && tick(e, 3).label.contains("Warning, spend"), tick(e, 3).label)
            XCTAssertFalse(tick(e, 3).isEnabled, "…can't be ticked")
            let apply = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
            XCTAssertEqual(apply.label, "Apply 4 changes…")
            let optIn = e.app.buttons.matching(identifier: "changeset-optin").firstMatch
            XCTAssertTrue(optIn.exists, "the opt-in is offered on the review")
            XCTAssertTrue(e.containing("This plan has 1 admin change.").exists)
            shot(e, "plan-review")
            // unticking the dice tile unticks the binding and the start that need it
            tick(e, 1).tap()
            XCTAssertTrue(e.until(10) { apply.label == "Apply 1 change…" }, "unticking a line unticks the lines that need it: \(apply.label)")
            tick(e, 1).tap()
            // the opt-in: armed, the person's own tap — and it ticks nothing
            XCTAssertTrue(e.until(15) { optIn.isEnabled }, "the opt-in arms")
            optIn.tap()
            XCTAssertTrue(e.until(15) { !optIn.exists && tick(e, 3).isEnabled }, "after the opt-in the admin's line can be ticked")
            XCTAssertTrue(tick(e, 3).label.hasPrefix("Change 3 of 5, not selected."), "…and was not ticked for them: \(tick(e, 3).label)")
            // the start line: the mode xbind vouches for, the coding agent, the brief as sent
            let brief = e.element("changeset-brief-5")
            e.wsScrollTo(tick(e, 5))
            XCTAssertTrue(e.containing("asks before acting").exists && e.containing("Coding agent: Codex").exists, "the start's mode and coding agent")
            XCTAssertTrue(brief.exists && e.containing("sent exactly like this").exists, "the brief, as it will be sent")
            shot(e, "plan-start-line")
            e.app.terminate()

            // ---- a person who is no admin ----
            e = try launch("plan-user")
            e.wsInbox("inbox-changeset:pl_user")
            e.expect(e.element("changeset-heading"), 30, "the plan's review")
            XCTAssertTrue(tick(e, 3).label.contains("needs an admin") && tick(e, 3).label.contains("Not yours to apply"), tick(e, 3).label)
            XCTAssertFalse(tick(e, 3).label.contains("Before:") || tick(e, 3).label.contains("Warning"), "xbind's line and nothing of the state behind it: \(tick(e, 3).label)")
            XCTAssertFalse(tick(e, 3).isEnabled)
            XCTAssertFalse(e.app.buttons.matching(identifier: "changeset-optin").firstMatch.exists, "no opt-in for a person who is no admin")
            e.wsScrollTo(tick(e, 3))
            shot(e, "plan-needs-admin")
            let go = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
            XCTAssertTrue(e.until(15) { go.isEnabled })
            go.tap() // personal lines: no password step
            e.expect(e.element("changeset-results"), 30, "the results, with no sign-in step")
            XCTAssertTrue(e.containing("1 line needs an admin and was not applied.").exists)
            let ask = e.app.buttons.matching(identifier: "changeset-ask-admin").firstMatch
            e.wsScrollTo(ask)
            XCTAssertTrue(e.until(15) { ask.isEnabled })
            ask.tap()
            XCTAssertTrue(e.until(15) { e.containing("1 line was sent to the workspace's admins.").exists }, "Ask an admin: the line says it was sent")
            shot(e, "plan-asked")
            e.app.terminate()

            // ---- an admin, asked by someone else ----
            e = try launch("plan-from")
            e.wsInbox("inbox-changeset:pl_from")
            let asked = e.element("changeset-heading")
            e.expect(asked, 30, "the request's review")
            XCTAssertEqual(asked.label, "bob asks for 2 admin changes")
            XCTAssertTrue(tick(e, 1).label.hasPrefix("Change 1 of 2, not selected."), "a warned line starts unticked for the admin too: \(tick(e, 1).label)")
            XCTAssertEqual(e.app.buttons.matching(identifier: "changeset-deny").firstMatch.label, "Decline")
            XCTAssertFalse(e.app.buttons.matching(identifier: "changeset-optin").firstMatch.exists, "no opt-in of one's own for someone else's request")
            shot(e, "plan-request")
            e.app.terminate()
        }
    }
}
