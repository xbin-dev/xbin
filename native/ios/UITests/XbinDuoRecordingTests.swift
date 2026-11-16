#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// Screen recordings of the workspace agent (D233) for people to watch —
/// not checks. Skipped unless the run records (`XBIN_E2E_RECORD=1`:
/// ci-uitests.sh films the simulator with `simctl io recordVideo` for the
/// whole run, and these tests mark where each clip starts and ends in
/// `$E2E_DIR/clips.tsv`, wall-clock seconds; native/AGENTS.md → "Recording
/// the app"). Made for an iPhone Duo (`XBIN_DUO=1`) but run anywhere.
///
/// Each clip begins with the app already where the flow starts and ends a
/// moment after it settles: no launches, sign-ins or waits on screen.
/// The live flows need the e2e xbind's workspace agent (`XBIN_E2E_WSAGENT=1`).
final class XbinDuoRecordingTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil, "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind")
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_RECORD"] != "1",
                      "XBIN_E2E_RECORD unset: nothing records this run (recordings only, not checks)")
    }

    private var themes: [String] {
        let only = ProcessInfo.processInfo.environment["XBIN_E2E_RECORD_THEMES"] ?? ""
        return only.isEmpty ? ["light", "dark"] : only.split(separator: " ").map(String.init)
    }

    // MARK: - The live agent (the e2e xbind's scripted model)

    /// (a) Home → the agent's button → the agent → a typed request → its
    /// answer with the request's card; (b) Review… → the approval →
    /// Allow once → the card settles; (e) Pause and Resume from the shield.
    @MainActor
    func test01LiveAskApprovePause() async throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_WSAGENT"] != "1", "XBIN_E2E_WSAGENT unset: no workspace agent")
        for theme in themes {
            let e = try E2E(self)
            _ = try await e.server.wsPrepare("Planning week \(theme)")
            e.app.launchArguments += ["-XbinAppearance", theme]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.expect(e.wsHomeRow, 60, "Home's agent row")
            e.expect(e.wsButton, 60, "the bar's agent button")
            XCTAssertTrue(e.wsHittable(e.wsButton))
            e.hold(1)

            e.clip("ask-\(theme)", "Ask the workspace agent") {
                e.hold(1.5)
                e.wsButton.tap()
                e.expect(e.wsComposer, 60, "the composer")
                XCTAssertTrue(e.until(60) { e.wsComposer.isEnabled })
                e.hold(1)
                e.wsComposer.tap()
                e.wsComposer.typeText("Set the note in apps/note to \"Standup moved to 10:30\"")
                e.tapAfterTyping(e.app.buttons["Send"])
                e.expect(e.wsAnyReview, 90, "the request's card")
                e.hold(3)
            }

            e.clip("approve-\(theme)", "Approve a request") {
                e.hold(1)
                e.wsAnyReview.tap()
                let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
                e.expect(allow, 30, "the approval")
                e.hold(2.5)
                XCTAssertTrue(e.until(15) { allow.isEnabled })
                allow.tap()
                XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists })
                e.expect(e.containing("Allowed"), 30, "the card settles")
                e.hold(3)
            }

            e.clip("pause-\(theme)", "Pause and resume the agent") {
                e.hold(1)
                e.wsChromeMenu("Pause my agent")
                let resume = e.app.buttons.matching(identifier: "agent-resume").firstMatch
                e.expect(resume, 30, "paused")
                e.hold(3)
                resume.tap()
                XCTAssertTrue(e.until(30) { !resume.exists })
                e.hold(2.5)
            }
            e.app.terminate()
        }
    }

    // MARK: - The demo states (Debug's -XbinAgentDemo)

    /// (c) A plan's review: tick a line → Apply → the password step → the
    /// results in place, and the sheet closes itself.
    @MainActor
    func test02DemoPlanReview() throws {
        for theme in themes {
            let e = try launchDemo("plan", theme)
            let needs = e.app.buttons["Needs you"]
            e.expect(needs, 60, "Needs you")
            e.clip("plan-\(theme)", "Review a plan") {
                e.hold(1)
                e.wsInbox("inbox-changeset:pl_dice")
                e.expect(e.element("changeset-heading"), 30, "the plan")
                e.hold(2.5)
                let tick = e.app.buttons.matching(identifier: "changeset-tick-3").firstMatch
                e.wsScrollTo(tick)
                e.hold(1)
                tick.tap()
                e.hold(1.5)
                let apply = e.app.buttons.matching(identifier: "changeset-allow").firstMatch
                XCTAssertTrue(e.until(15) { apply.isEnabled })
                apply.tap()
                let field = e.app.secureTextFields.firstMatch
                e.expect(field, 30, "the password step")
                e.hold(1)
                field.tap()
                field.typeText("demo-password")
                e.tapAfterTyping(e.app.buttons.matching(identifier: "stepup-allow").firstMatch)
                e.expect(e.element("changeset-results"), 30, "the results")
                XCTAssertTrue(e.until(15) { !e.element("changeset-results").exists }, "the sheet closes itself")
                e.hold(1.5)
            }
            e.app.terminate()
        }
    }

    /// (d) A tile agent's start in a mode that asks, then one in a mode that
    /// never asks (the danger wording, Deny first).
    @MainActor
    func test03DemoSessionStarts() throws {
        for theme in themes {
            let e = try launchDemo("grants", theme)
            e.expect(e.app.buttons["Needs you"], 60, "Needs you")
            e.clip("start-asks-\(theme)", "Start a tile agent that asks first") {
                e.hold(1)
                e.wsInbox("inbox-request:rq_sess")
                e.expect(e.element("approval-heading"), 30, "the start")
                e.hold(3)
                e.app.swipeUp()
                e.hold(2)
                let start = e.app.buttons.matching(identifier: "approval-allow").firstMatch
                XCTAssertTrue(e.until(15) { start.isEnabled })
                start.tap()
                // the next request that waits comes up in its place
                XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists || !e.element("approval-heading").label.contains("Orders") })
                e.hold(1.5)
            }
            e.wsCloseApprovals()
            e.goHome()
            e.clip("start-never-asks-\(theme)", "A start that never asks") {
                e.hold(1)
                e.wsInbox("inbox-request:rq_never")
                e.expect(e.element("approval-danger"), 30, "the danger wording")
                e.hold(3.5)
                e.app.swipeUp()
                e.hold(2)
                e.app.buttons.matching(identifier: "approval-deny").firstMatch.tap()
                XCTAssertTrue(e.until(20) { !e.element("approval-danger").exists })
                e.hold(1.5)
            }
            e.wsCloseApprovals()
            e.app.terminate()
        }
    }

    /// (f) An admin before setup: Home's row, the agent's three steps.
    @MainActor
    func test04DemoSetup() throws {
        for theme in themes {
            let e = try launchDemo("setup", theme)
            e.expect(e.wsHomeRow, 60, "Home's setup row")
            e.clip("setup-\(theme)", "Set up the workspace agent") {
                e.hold(2)
                e.wsHomeRow.tap()
                e.expect(e.element("agent-setup"), 30, "the setup's steps")
                e.hold(3)
                e.app.swipeUp()
                e.hold(3)
                e.wsClose.tap()
                e.hold(1.5)
            }
            e.app.terminate()
        }
    }

    // MARK: - The open Duo, on simulated geometry

    /// The open layout — the agent in its half beside a tile — on Debug's
    /// `-XbinDuoOpenGeometry` (the inner screen's size, scaled into the
    /// closed Duo's outer one: no simulator can open a Duo without
    /// DeviceHub's GUI). Clips `open-sim-*`; only with XBIN_E2E_RECORD_OPEN=1.
    @MainActor
    func test05OpenGeometry() async throws {
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_RECORD_OPEN"] != "1", "XBIN_E2E_RECORD_OPEN unset")
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_WSAGENT"] != "1", "XBIN_E2E_WSAGENT unset: no workspace agent")
        for theme in themes {
            let e = try E2E(self)
            let run = try await e.server.wsPrepare("Open Duo \(theme)")
            e.app.launchArguments += ["-XbinAppearance", theme, "-XbinDuoOpenGeometry", "YES"]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.openTile("apps/note")
            e.expect(e.wsButton, 60, "the bar's agent button over the tile")
            e.hold(2)

            // (the request is sent through the agent's API, not typed: the
            // composer's text view takes no focus inside the scaled canvas)
            e.mark("open-sim-ask-\(theme)", "start", "Open Duo: the agent beside a tile")
            e.hold(1.5)
            e.wsButton.tap()
            e.expect(e.wsComposer, 60, "the composer")
            XCTAssertTrue(e.until(60) { e.wsComposer.isEnabled })
            e.hold(2)
            try await e.server.wsSay(run, "Set the note in apps/note to \"Standup moved to 10:30\"")
            e.expect(e.wsAnyReview, 90, "the request's card")
            e.hold(3)
            e.mark("open-sim-ask-\(theme)", "end", "Open Duo: the agent beside a tile")

            e.clip("open-sim-approve-\(theme)", "Open Duo: approve, the tile beside changes") {
                e.hold(1)
                e.wsAnyReview.tap()
                let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
                e.expect(allow, 30, "the approval")
                e.hold(2.5)
                XCTAssertTrue(e.until(15) { allow.isEnabled })
                allow.tap()
                XCTAssertTrue(e.until(20) { !e.element("approval-heading").exists })
                e.expect(e.containing("Allowed"), 30, "the card settles")
                e.hold(4)
            }
            e.app.terminate()

            let d = try launchDemo("plan", theme, ["-XbinDuoOpenGeometry", "YES"])
            d.expect(d.app.buttons["Needs you"], 60, "Needs you")
            d.clip("open-sim-plan-\(theme)", "Open Duo: review a plan") {
                d.hold(1)
                d.wsInbox("inbox-changeset:pl_dice")
                d.expect(d.element("changeset-heading"), 30, "the plan")
                d.hold(2.5)
                let tick = d.app.buttons.matching(identifier: "changeset-tick-3").firstMatch
                d.wsScrollTo(tick)
                tick.tap()
                d.hold(1.5)
                let apply = d.app.buttons.matching(identifier: "changeset-allow").firstMatch
                XCTAssertTrue(d.until(15) { apply.isEnabled })
                apply.tap()
                let field = d.app.secureTextFields.firstMatch
                d.expect(field, 30, "the password step")
                field.tap()
                field.typeText("demo-password")
                d.tapAfterTyping(d.app.buttons.matching(identifier: "stepup-allow").firstMatch)
                d.expect(d.element("changeset-results"), 30, "the results")
                XCTAssertTrue(d.until(15) { !d.element("changeset-results").exists })
                d.hold(1.5)
            }
            d.app.terminate()
        }
    }

    @MainActor
    private func launchDemo(_ state: String, _ theme: String, _ extra: [String] = []) throws -> E2E {
        let e = try E2E(self)
        e.wsDemo(state)
        e.app.launchArguments += ["-XbinAppearance", theme, "-XbinAgentGate", "allow"] + extra
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        return e
    }
}

extension E2E {
    /// Any request card's Review… in the conversation.
    var wsAnyReview: XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "agent-review:")).firstMatch
    }

    /// Closes the approvals that come up one after another (Not now).
    func wsCloseApprovals() {
        let later = app.buttons.matching(identifier: "approval-later").firstMatch
        for _ in 0..<6 where later.exists {
            later.tap()
            _ = until(5) { !later.exists }
        }
        app.buttons["Done"].tapIfThere()
    }

    /// Lets the screen rest for a viewer.
    func hold(_ seconds: TimeInterval) {
        _ = app.otherElements["xbin-e2e-never"].waitForExistence(timeout: seconds)
    }

    /// Marks a clip of the run's recording: `name<TAB>start|end<TAB>epoch
    /// seconds<TAB>title` lines in `$E2E_DIR/clips.tsv`.
    func clip(_ name: String, _ title: String, _ body: () throws -> Void) rethrows {
        mark(name, "start", title)
        try body()
        mark(name, "end", title)
    }

    func mark(_ name: String, _ event: String, _ title: String) {
        guard let dir = ProcessInfo.processInfo.environment["E2E_DIR"], !dir.isEmpty else { return }
        let url = URL(fileURLWithPath: dir).appendingPathComponent("clips.tsv")
        let line = String(format: "%@\t%@\t%.3f\t%@\n", name, event, Date().timeIntervalSince1970, title)
        if let h = try? FileHandle(forWritingTo: url) {
            h.seekToEndOfFile()
            h.write(Data(line.utf8))
            try? h.close()
        } else {
            try? Data(line.utf8).write(to: url)
        }
    }
}

extension XCUIElement {
    func tapIfThere() { if exists, isHittable { tap() } }
}
