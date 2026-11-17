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
/// - test01: a REAL sealed push (XBIN_E2E_PUSH: xbind seals it, the
///   development relay spools it, simctl push hands it over, the extension
///   opens and draws it): Allow once from the banner's actions approves the
///   request from a notification (xbind's decidedFrom), the note changed;
///   the settings first, with the explanation and its armed Turn on.
/// - test02: Deny after it was decided elsewhere: nothing sent, and the
///   sheet the notification opens says where it was decided.
/// - test03: a request too long for the notification: Deny and Review only;
///   Review… opens the sheet on that request from a cold start.
/// - test04: Reply to the agent's question: a message in the conversation,
///   never a decision — a pending request stays pending.
/// - test05Gallery: the settings, the banner, the lock screen with the
///   notification's actions, and the Live Activity — on the lock screen and
///   the Dynamic Island's compact and expanded presentations — in light and
///   dark (`wsnotify-<light|dark>-NN-<what>.png`). Minimal is drawn only
///   beside another app's Live Activity (the system shows one per app in
///   the island), which a simulator has none of; it is the compact
///   presentation's leading glyph.
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
        // the real path (review finding 8): xbind seals the push to this
        // install's key, the development relay spools it, mac-remote.sh hands
        // it to the simulator (simctl push), the app's Notification Service
        // Extension opens it and draws it — the offer xbind's, the category
        // the extension's — and the action decides on xbind
        let relay = ProcessInfo.processInfo.environment["XBIN_E2E_PUSH_RELAY"] ?? ""
        try XCTSkipIf(relay.isEmpty, "XBIN_E2E_PUSH unset: no push relay spooling to the simulator (e2e-xbind.sh)")
        let e = try E2E(self)
        let run = try await e.server.wsPrepare("E2E notify allow")
        // (the Mac may not reach Apple: a stand-in device token, which the
        // spooling relay takes as it is — the push itself is xbind's, sealed)
        e.app.launchArguments += ["-pushRelay", relay, "-XbinFakeAPNsToken", String(repeating: "ab", count: 32), "-XbinDrawPushInApp", "YES"]
        e.launch()
        e.ensureWorkspace()
        // push starts at a launch with a workspace (a fresh simulator's first has none)
        e.app.terminate()
        e.launch()
        e.notifyAllowPermission()
        await e.eventually("this install registered for push with xbind", timeout: 120) {
            try await e.server.pushDevices() > 0
        }
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

        // the agent asks; xbind's push comes — the app in front opens the sealed
        // push and draws it with the extension's code (simctl push wakes no
        // extension), its banner over the app
        let ref = try await e.server.wsFileNoteRequest(run, note: "allowed from a notification")
        // its banner, or — gone up before the request's filing returned — Notification Center's
        e.expect(e.notifyFind("Approval needed", timeout: 90), 10, "the approval's notification")
        XCTAssertTrue(e.notifyContaining("PUT /api/apps/note").exists || e.notifyContaining("/api/apps/note").exists,
                      "the call itself, in full, on an eligible request")
        e.notifyActions("Approval needed", until: "Allow once")
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
        e.expectStillRunning("after Allow once")
        e.app.activate()
    }

    @MainActor
    func test02DenyAfterItWasDecidedElsewhere() async throws {
        let e = try E2E(self)
        // the sealed path is test01's: no real push of xbind's (a clear when the
        // web decides) takes this test's notification back from under it
        try await e.server.pushForgetAll()
        let run = try await e.server.wsPrepare("E2E notify gone")
        let ref = try await e.server.wsFileNoteRequest(run, note: "decided on the web")
        e.app.launchArguments += ["-XbinAgentNotice", "request:\(ref)", "-XbinAgentNoticeDelay", "8"]
        e.launch()
        e.ensureWorkspace()
        e.notifyAllowPermission()
        XCUIDevice.shared.press(.home)
        let banner = e.notifyBanner("Approval needed")
        e.expect(banner, 60, "the approval's banner")
        // meanwhile, elsewhere (the test's own sign-in)
        try await e.server.wsApprove(ref)
        e.notifyActions("Approval needed", until: "Deny")
        let deny = e.springboard.buttons["Deny"]
        e.expect(deny, 15, "Deny among the actions")
        deny.tap()
        let follow = e.notifyFind("Already decided")
        // nothing was sent: it stays what the other sign-in made it
        let state = try await e.server.wsRequest(ref)["state"] as? String
        XCTAssertTrue(["approved", "used"].contains(state ?? ""), "the earlier decision stands: \(state ?? "")")
        e.expectStillRunning("after Deny")
        if e.isPad && !follow.exists {
            // An iPad's Springboard (the iOS 27 simulator) lists no follow-up that
            // replaces a notification acted on from Notification Center — the app
            // posts it (its log says so). The app's router opens the same sheet.
            e.app.open(URL(string: "xbin://\(e.server.authority)/xbin/agent#r=\(ref)")!)
        } else {
            e.expect(follow, 15, "the follow-up: already decided")
            e.shot("wsnotify-02-already-decided-banner")
            // its Review… opens the sheet, which says where it was decided
            e.notifyActions("Already decided", until: "Review…")
            let review = e.springboard.buttons["Review…"]
            e.expect(review, 15, "Review… on the follow-up")
            review.tap()
        }
        let settled = e.app.descendants(matching: .any).matching(identifier: "approval-settled").firstMatch
        e.expect(settled, 60, "the sheet on that request, settled")
        let place = e.app.descendants(matching: .any).matching(identifier: "approval-decided-where").firstMatch
        e.expect(place, 30, "where it was decided")
        // the test decided with its own password sign-in (an app's session, not
        // a browser's, nor this device's): xbind's record says so
        XCTAssertEqual(place.label, "Already decided on another of your devices.", "where, by xbind's record")
        e.shot("wsnotify-03-already-decided-sheet")
    }

    @MainActor
    func test03TooLongIsReviewOnly() async throws {
        let e = try E2E(self)
        // the sealed path is test01's: no real push of xbind's (a clear when the
        // web decides) takes this test's notification back from under it
        try await e.server.pushForgetAll()
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
        e.notifyActions("Approval needed", until: "Review…")
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
        // the sealed path is test01's: no real push of xbind's (a clear when the
        // web decides) takes this test's notification back from under it
        try await e.server.pushForgetAll()
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
        e.notifyActions("Question from your workspace agent", until: "Reply")
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
        e.expectStillRunning("after Reply")
        e.app.activate()
    }

    @MainActor
    func test05Gallery() async throws {
        let e = try E2E(self)
        try await e.server.pushForgetAll()
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
            // the Live Activity: the Dynamic Island, compact, on the home screen (an iPad has none)
            XCUIDevice.shared.press(.home)
            try await Task.sleep(nanoseconds: 3_000_000_000)
            if !e.isPad { e.shot("wsnotify-\(appearance)-02-island-compact") }
            let banner = e.notifyBanner("Approval needed")
            e.expect(banner, 60, "the banner")
            e.shot("wsnotify-\(appearance)-03-banner")
            try await Task.sleep(nanoseconds: 6_000_000_000) // the banner goes up
            if !e.isPad {
                // the expanded island: a long press on it
                e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.022)).press(forDuration: 1.0)
                try await Task.sleep(nanoseconds: 1_000_000_000)
                e.shot("wsnotify-\(appearance)-04-island-expanded")
                e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7)).tap()
            }
            // the lock screen: the card, and the notification in the list. It is the
            // system's (the simulator's appearance, which the app's flag doesn't change):
            // once is enough, and a second visit finds the list moved on. On an iPad
            // the simulator's lock screen keeps its first-card prompt over the list
            // and an action is taken from Notification Center (tests 01–04): the
            // iPhone's pass has the lock screen
            guard appearance == "light", !e.isPad else { e.app.terminate(); continue }
            e.lockDevice()
            // the first card on the lock screen: iOS asks whether the app may show
            // them there ("Allow"; again later: "Always Allow")
            for label in ["Allow", "Always Allow"] {
                let allowCards = e.springboard.buttons[label]
                if allowCards.waitForExistence(timeout: 3) { allowCards.tap(); try await Task.sleep(nanoseconds: 1_000_000_000); break }
            }
            // a notification seen while unlocked waits under the lock screen's list: a swipe up shows it
            if !e.notifyBanner("Approval needed").waitForExistence(timeout: 5) {
                e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.62))
                    .press(forDuration: 0.05, thenDragTo: e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
            }
            e.expect(e.notifyBanner("Approval needed"), 20, "the notification on the lock screen")
            e.shot("wsnotify-\(appearance)-05-lock-screen")
            let listed = e.notifyCell("Approval needed")
            listed.press(forDuration: 1.5)
            if !e.springboard.buttons["Allow once"].waitForExistence(timeout: 6) {
                // the lock screen's list: a swipe to the left, then View opens it with its actions
                listed.swipeLeft()
                let view = e.springboard.buttons["View"]
                if view.waitForExistence(timeout: 5) { view.tap() } else {
                    print("lock screen, buttons after a swipe: \(e.springboard.buttons.allElementsBoundByIndex.map(\.label))")
                }
            }
            e.expect(e.springboard.buttons["Allow once"], 15, "the actions on the lock screen")
            XCTAssertTrue(e.springboard.buttons["Deny"].exists && e.springboard.buttons["Review…"].exists, "Deny and Review… beside it")
            XCTAssertFalse(e.springboard.buttons["Reply"].exists, "an approval has no Reply")
            // the simulator brings its keyboard up with an expanded notification: say what has focus
            print("lock screen actions: text fields \(e.springboard.textFields.count), text views \(e.springboard.textViews.count)")
            e.dismissKeyboardTip()
            e.shot("wsnotify-\(appearance)-06-lock-screen-actions")
            e.springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.08)).tap()
            e.unlockDevice()
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

    /// The app handled a notification's action in the background and is
    /// still there: a completion handler called off the main thread is an
    /// uncaught exception in UIKit's snapshot, after the action's effect.
    func expectStillRunning(_ what: String, file: StaticString = #filePath, line: UInt = #line) {
        Thread.sleep(forTimeInterval: 3)
        XCTAssertNotEqual(app.state, .notRunning, "the app is still running \(what) (it would have crashed handing the action back)", file: file, line: line)
    }

    /// A long press on the notification whose text has `title` opens its
    /// actions; `until` is one of them. A banner that went up before the
    /// press (an iPad's go sooner) is in Notification Center: the press is
    /// made there.
    func notifyActions(_ title: String, until action: String) {
        let cell = notifyCell(title)
        if cell.exists {
            cell.press(forDuration: 1.2)
            if springboard.buttons[action].waitForExistence(timeout: 5) { return }
        }
        // gone up: Notification Center has it (under its fold when seen before)
        let found = notifyFind(title, timeout: 30)
        guard found.exists else { return }
        notifyCell(title).press(forDuration: 1.5)
        _ = springboard.buttons[action].waitForExistence(timeout: 10)
    }

    /// A notification whose text has `title`: its banner, or — a follow-up
    /// that replaces a notification acted on from Notification Center comes
    /// without one (an iPad's) — the notification in Notification Center.
    func notifyFind(_ title: String, timeout: TimeInterval = 45) -> XCUIElement {
        let found = notifyBanner(title)
        if found.waitForExistence(timeout: timeout / 2) { return found }
        springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.003))
            .press(forDuration: 0.05, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7)))
        if found.waitForExistence(timeout: timeout / 4) { return found }
        // one seen before sits under the list's fold: a drag up shows it
        springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.62))
            .press(forDuration: 0.05, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        _ = found.waitForExistence(timeout: timeout / 4)
        return found
    }

    /// The notification itself whose text has `title`: the smallest element
    /// that says it (a list or a stack around it says it too).
    func notifyCell(_ title: String) -> XCUIElement {
        let all = springboard.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", title)).allElementsBoundByIndex
        let sized = all.filter { $0.exists && $0.frame.height >= 40 }
        return sized.min { $0.frame.width * $0.frame.height < $1.frame.width * $1.frame.height } ?? notifyBanner(title)
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
        // short drags: an iPad's sheet is small, and a swipe can carry the row past its header
        let sheet = app.windows.firstMatch
        for _ in 0..<8 where !(row.exists && row.isHittable) {
            let up = !row.exists || row.frame.midY > sheet.frame.midY
            let from = sheet.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.62 : 0.4))
            from.press(forDuration: 0.05, thenDragTo: sheet.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: up ? 0.47 : 0.55)))
            _ = row.waitForExistence(timeout: 2)
        }
        expect(row, 20, "Workspace agent under Notifications", file: file, line: line)
        row.tap()
    }

    /// The simulator's keyboard shows its slide-to-type tip the first time
    /// the system's keyboard comes up (an expanded notification brings it up
    /// in Springboard): Continue, so a shot shows what is under it.
    func dismissKeyboardTip() {
        let tip = springboard.buttons["Continue"]
        if tip.waitForExistence(timeout: 2) { tip.tap() }
    }

    /// The lock screen (the simulator's side button).
    func lockDevice() {
        _ = XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        Thread.sleep(forTimeInterval: 2)
    }

    /// Back from the lock screen (a simulator has no passcode): the home
    /// button wakes it, a drag up from the bottom edge opens it.
    func unlockDevice() {
        XCUIDevice.shared.press(.home)
        Thread.sleep(forTimeInterval: 1)
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.995))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        Thread.sleep(forTimeInterval: 1.5)
        if !springboard.icons.firstMatch.isHittable { XCUIDevice.shared.press(.home); Thread.sleep(forTimeInterval: 1.5) }
    }
}

extension E2EServer {
    /// Forgets the account's push registrations (DELETE /api/xbin/devices/push/<id>):
    /// a test of the demo path gets no real push of xbind's.
    func pushForgetAll() async throws {
        let o = (try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/devices/push")) as? [String: Any]) ?? [:]
        for d in (o["devices"] as? [[String: Any]]) ?? [] {
            if let id = d["deviceId"] as? String { _ = try? await send("DELETE", "/api/xbin/devices/push/\(id)") }
        }
    }

    /// How many push registrations the account has (GET /api/xbin/devices/push).
    func pushDevices() async throws -> Int {
        let o = (try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/devices/push")) as? [String: Any]) ?? [:]
        return (o["devices"] as? [Any])?.count ?? 0
    }

    /// One request as xbind reads it (GET /api/xbin/delegate/requests/<id>).
    func wsRequest(_ id: String) async throws -> [String: Any] {
        (try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/delegate/requests/\(id)")) as? [String: Any]) ?? [:]
    }
}
