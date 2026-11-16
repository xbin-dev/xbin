#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// The workspace agent when the app is not open (D241; plans/wsagent/
/// ios-stage2.md): the notifications, their lock-screen actions, the
/// router, the Live Activity, the settings — against the e2e xbind with a
/// workspace agent (XBIN_E2E_WSAGENT=1, as XbinWorkspaceAgentTests).
///
/// The simulator gets no APNs push sealed to this install's key, so a test
/// launches the app with Debug's `-XbinAgentNotice <what>`: the app posts,
/// a few seconds after the test leaves it, the notification such a push makes — drawn by the
/// same XbinCore AgentNotice the extension uses, from xbind's own reading of
/// the request — and the test acts on it in Springboard as a person does.
/// What the app then sends, it sends to the real xbind, which is checked.
///
/// - test01: Allow once from the banner's actions: the request is approved
///   from a notification (xbind's decidedFrom), the note changed; the
///   settings first, with the explanation and its armed Turn on.
/// - test02: Deny after it was decided on the web: nothing sent, and the
///   sheet the notification opens says where it was decided.
/// - test03: a request too long for the notification: Deny and Review only;
///   Review… opens the sheet on that request from a cold start.
/// - test04: Reply to the agent's question: a message in the conversation,
///   never a decision — a pending request stays pending.
/// - test05Gallery: the banner, its actions, the lock screen, the settings,
///   the explanation and the three presentations of the Live Activity, in
///   light and dark (`wsnotify-<light|dark>-NN-<what>.png`).
final class XbinWorkspaceAgentNotifyTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
        try XCTSkipIf(ProcessInfo.processInfo.environment["XBIN_E2E_WSAGENT"] != "1",
                      "XBIN_E2E_WSAGENT unset: the e2e xbind has no workspace agent (e2e-xbind.sh)")
    }

    @MainActor
    func test01AllowOnceFromTheBanner() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E notify allow")
        let ref = try await e.server.wsFileNoteRequest(run, note: "allowed from a notification")
        e.app.launchArguments += ["-XbinAgentNotice", "request:\(ref)", "-XbinAgentNoticeDelay", "3"]
        e.launch()
        e.ensureWorkspace()
        e.notifyAllowPermission()
        e.goHome()

        // the person turns Allow once on: Settings → the workspace → Workspace agent
        e.notifyOpenSettings()
        let allow = e.app.switches.matching(identifier: "agent-notify-allow-once").firstMatch
        e.expect(allow, 20, "Allow once from a notification")
        XCTAssertEqual(allow.value as? String, "0", "off until the person turns it on")
        XCTAssertEqual(e.app.switches.matching(identifier: "agent-notify-deny").firstMatch.value as? String, "1", "Deny on by default")
        e.flip(allow)
        let turnOn = e.app.buttons.matching(identifier: "allow-once-turn-on").firstMatch
        e.expect(turnOn, 15, "the explanation before Allow once is on")
        XCTAssertTrue(e.containing("Never for a plan or admin changes").exists, "it says what it never covers")
        e.shot("wsnotify-01-allow-once-explained")
        XCTAssertTrue(e.until(10) { turnOn.isEnabled }, "Turn on is armed a moment after it shows")
        turnOn.tap()
        XCTAssertTrue(e.until(10) { allow.value as? String == "1" }, "on, after Turn on")

        // leave the app (Settings open as it is); the notification comes
        XCUIDevice.shared.press(.home)
        let banner = e.notifyBanner("Approval needed")
        e.expect(banner, 60, "the approval's banner")
        XCTAssertTrue(e.notifyContaining("PUT /api/apps/note").exists || e.notifyContaining("/api/apps/note").exists,
                      "the call itself, in full, on an eligible request")
        banner.press(forDuration: 1.2)
        let allowOnce = e.springboard.buttons["Allow once"]
        e.expect(allowOnce, 15, "Allow once among the actions")
        XCTAssertTrue(e.springboard.buttons["Deny"].exists && e.springboard.buttons["Review…"].exists, "Deny and Review… beside it")
        allowOnce.tap()

        // the app decided it in the background, with the person's session, as one from a notification
        await e.eventually("the request approved from the notification", timeout: 60) {
            let r = try await e.server.wsRequest(ref)
            return ["approved", "used"].contains(r["state"] as? String ?? "") && r["decidedFrom"] as? String == "notification"
        }
        await e.eventually("the note changed", timeout: 60) {
            e.server.wsNoteValue(try await e.server.wsNote()).contains("allowed from a notification")
        }
        e.app.activate()
    }

    @MainActor
    func test02DenyAfterTheWebDecided() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E notify gone")
        let ref = try await e.server.wsFileNoteRequest(run, note: "decided on the web")
        e.app.launchArguments += ["-XbinAgentNotice", "request:\(ref)", "-XbinAgentNoticeDelay", "8"]
        e.launch()
        e.ensureWorkspace()
        e.notifyAllowPermission()
        XCUIDevice.shared.press(.home)
        let banner = e.notifyBanner("Approval needed")
        e.expect(banner, 60, "the approval's banner")
        // meanwhile, on the web
        try await e.server.wsApprove(ref)
        banner.press(forDuration: 1.2)
        let deny = e.springboard.buttons["Deny"]
        e.expect(deny, 15, "Deny among the actions")
        deny.tap()
        // nothing was sent: it stays what the web made it
        let follow = e.notifyBanner("Already decided")
        e.expect(follow, 45, "the follow-up: already decided")
        let state = try await e.server.wsRequest(ref)["state"] as? String
        XCTAssertTrue(["approved", "used"].contains(state ?? ""), "the web's decision stands: \(state ?? "")")
        e.shot("wsnotify-02-already-decided-banner")
        // its Review… opens the sheet, which says where it was decided
        follow.press(forDuration: 1.2)
        let review = e.springboard.buttons["Review…"]
        e.expect(review, 15, "Review… on the follow-up")
        review.tap()
        let settled = e.app.descendants(matching: .any).matching(identifier: "approval-settled").firstMatch
        e.expect(settled, 60, "the sheet on that request, settled")
        e.expect(e.app.descendants(matching: .any).matching(identifier: "approval-decided-where").firstMatch, 30, "where it was decided")
        XCTAssertTrue(e.containing("on the web").exists, "…on the web")
        e.shot("wsnotify-03-already-decided-sheet")
    }

    @MainActor
    func test03TooLongIsReviewOnly() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E notify long")
        let long = String(repeating: "A long note that no lock screen shows whole. ", count: 8)
        let ref = try await e.server.wsFileNoteRequest(run, note: long)
        e.app.launchArguments += ["-XbinAgentAllowOnce", "YES", "-XbinAgentNotice", "request:\(ref)", "-XbinAgentNoticeDelay", "8"]
        e.launch()
        e.ensureWorkspace()
        e.notifyAllowPermission()
        XCUIDevice.shared.press(.home)
        let banner = e.notifyBanner("Approval needed")
        e.expect(banner, 60, "the approval's banner")
        XCTAssertTrue(e.notifyContaining("Open to see exactly what").exists, "xbind's sentence, not the call")
        banner.press(forDuration: 1.2)
        e.expect(e.springboard.buttons["Review…"], 15, "Review…")
        XCTAssertFalse(e.springboard.buttons["Allow once"].exists, "no Allow once for what doesn't fit")
        XCTAssertTrue(e.springboard.buttons["Deny"].exists, "Deny is there")
        // a cold start from it: the app was not running
        e.app.terminate()
        e.springboard.buttons["Review…"].tap()
        e.expect(e.app.descendants(matching: .any).matching(identifier: "approval-heading").firstMatch, 90, "the approval sheet on that request")
        let after = try await e.server.wsRequestState(ref)
        XCTAssertEqual(after, "pending", "opening decides nothing")
        e.shot("wsnotify-04-review-cold-start")
    }

    @MainActor
    func test04ReplyIsAMessage() async throws {
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E notify reply")
        let ref = try await e.server.wsFileNoteRequest(run, note: "still waiting")
        e.app.launchArguments += ["-XbinAgentNotice", "ask:\(run)", "-XbinAgentNoticeDelay", "8"]
        e.launch()
        e.ensureWorkspace()
        e.notifyAllowPermission()
        XCUIDevice.shared.press(.home)
        let banner = e.notifyBanner("Question from your workspace agent")
        e.expect(banner, 60, "the question's banner")
        XCTAssertTrue(e.notifyContaining("Your agent asks: “").exists, "the question, quoted as the agent's")
        banner.press(forDuration: 1.2)
        let reply = e.springboard.buttons["Reply"]
        e.expect(reply, 15, "Reply")
        reply.tap()
        e.springboard.typeText("Allow once")
        let send = e.springboard.buttons["Send"]
        if send.waitForExistence(timeout: 5) { send.tap() } else { e.springboard.typeText("\n") }
        await e.eventually("the reply in the conversation", timeout: 60) {
            let msgs = try await e.server.wsCall("GET", "/runs/\(run)/view")["messages"] as? [[String: Any]] ?? []
            return msgs.contains { ($0["role"] as? String) == "user" && (($0["content"] as? String) ?? "").contains("Allow once") }
        }
        let still = try await e.server.wsRequestState(ref)
        XCTAssertEqual(still, "pending", "a reply that says “Allow once” is a message, never a decision")
        e.app.activate()
    }

    @MainActor
    func test05Gallery() async throws {
        let e = try E2E(self)
        for appearance in ["light", "dark"] {
            let run = try await e.server.wsPrepare("E2E notify gallery \(appearance)")
            let ref = try await e.server.wsFileNoteRequest(run, note: "the gallery's request")
            e.app.launchArguments = ["-XbinAppearance", appearance, "-XbinAgentAllowOnce", "YES", "-XbinAgentNotice", "request:\(ref)",
                                     "-XbinAgentNoticeDelay", "12", "-XbinAgentCardDemo", "waiting"]
            e.launch()
            e.ensureWorkspace()
            e.notifyAllowPermission()
            e.goHome()
            e.notifyOpenSettings()
            e.expect(e.app.switches.matching(identifier: "agent-notify-allow-once").firstMatch, 20, "the settings")
            e.shot("wsnotify-\(appearance)-01-settings")
            // the Live Activity: the Dynamic Island, compact, on the home screen
            XCUIDevice.shared.press(.home)
            e.shot("wsnotify-\(appearance)-02-island-compact")
            let banner = e.notifyBanner("Approval needed")
            e.expect(banner, 60, "the banner")
            e.shot("wsnotify-\(appearance)-03-banner")
            banner.press(forDuration: 1.2)
            e.expect(e.springboard.buttons["Allow once"], 15, "the actions")
            e.shot("wsnotify-\(appearance)-04-actions")
            // dismiss the expanded view: tap outside it
            e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.06)).tap()
            // the lock screen: the card, and the notification in the list
            e.lockDevice()
            e.shot("wsnotify-\(appearance)-05-lock-screen")
            e.unlockDevice()
            // the expanded island: a long press on it
            e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.025)).press(forDuration: 1.0)
            e.shot("wsnotify-\(appearance)-06-island-expanded")
            e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7)).tap()
            e.app.terminate()
        }
    }
}

// MARK: - Notifications in Springboard

extension E2E {
    var springboard: XCUIApplication { XCUIApplication(bundleIdentifier: "com.apple.springboard") }

    /// Flip a SwiftUI Toggle in a list: the element spans the row, and a tap
    /// at its middle lands on the label, which doesn't flip it — tap the
    /// switch itself, at the row's trailing end.
    func flip(_ toggle: XCUIElement) {
        let inner = toggle.switches.firstMatch
        if inner.exists, inner.frame.width < toggle.frame.width { inner.tap(); return }
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
    }

    /// A banner (or a notification in the list) whose text has `title`.
    func notifyBanner(_ title: String) -> XCUIElement {
        springboard.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", title)).firstMatch
    }

    func notifyContaining(_ text: String) -> XCUIElement {
        springboard.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    /// iOS asks once whether the app may notify: Allow.
    func notifyAllowPermission() {
        let alert = springboard.alerts.firstMatch
        if alert.waitForExistence(timeout: 12) {
            let allow = alert.buttons["Allow"]
            if allow.exists { allow.tap() } else { dismissSystemAlerts() }
        }
        dismissSavePassword()
    }

    /// Home's Settings → the workspace's Notifications → Workspace agent.
    func notifyOpenSettings(file: StaticString = #filePath, line: UInt = #line) {
        goHome()
        let settings = app.buttons["Settings"]
        expect(settings, 30, "Home's Settings", file: file, line: line)
        if !settings.isHittable { app.swipeUp() }
        settings.tap()
        let row = app.buttons.matching(identifier: "settings-agent-notifications").firstMatch
        for _ in 0..<4 where !(row.exists && row.isHittable) {
            app.swipeUp()
            _ = row.waitForExistence(timeout: 2)
        }
        expect(row, 20, "Workspace agent under Notifications", file: file, line: line)
        row.tap()
    }

    /// The lock screen (the simulator's side button).
    func lockDevice() {
        _ = XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        Thread.sleep(forTimeInterval: 2)
    }

    /// Back from the lock screen (a simulator has no passcode: a swipe up).
    func unlockDevice() {
        XCUIDevice.shared.press(.home)
        Thread.sleep(forTimeInterval: 1)
        springboard.swipeUp()
        Thread.sleep(forTimeInterval: 1)
    }
}

extension E2EServer {
    /// One request as xbind reads it (GET /api/xbin/delegate/requests/<id>).
    func wsRequest(_ id: String) async throws -> [String: Any] {
        (try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/delegate/requests/\(id)")) as? [String: Any]) ?? [:]
    }
}
