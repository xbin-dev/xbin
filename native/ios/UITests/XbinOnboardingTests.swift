import Foundation
import XCTest

/// Adding a workspace end to end (App/Shell/Onboarding.swift): the Welcome's
/// levels, Log in by address and password, joining with an invite, "Sign in
/// again" replacing its workspace, and the failures told apart. Each test
/// launches the app as a fresh install (Debug builds' -XbinFreshStart: no
/// workspace, XbinE2ETests' saved one left as it was), so any one runs
/// alone. test10Gallery takes a screenshot of every onboarding screen, light
/// and dark (gallery-<light|dark>-NN-<screen>.png in E2E_DIR).
final class XbinOnboardingTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    static let welcomeButtons = ["Log in", "Join with an invite", "Run your own xbin", "What is xbin?"]

    /// The Welcome's four ways in; What is xbin? pages through to Log in,
    /// where Scan QR code is shown but disabled (a simulator can't scan).
    @MainActor
    func test01Welcome() throws {
        let e = try E2E(self)
        e.launchFresh()
        for b in Self.welcomeButtons {
            XCTAssertTrue(e.app.buttons[b].waitForExistence(timeout: 30), "the Welcome's \(b)")
        }
        e.shot("onboarding-01-welcome")
        e.app.buttons["What is xbin?"].tap()
        for (i, title) in ["An office for your agents", "Agents at work", "Yours, and private"].enumerated() {
            XCTAssertTrue(e.element(title).waitForExistence(timeout: 10), "What is xbin? page \(i + 1)")
            e.shot("onboarding-02-about-\(i + 1)")
            if i < 2 { e.app.buttons["Next"].tap() }
        }
        XCTAssertTrue(e.app.buttons["Privacy policy"].exists, "the privacy page links the policy")
        e.app.buttons["Log in"].tap()
        let scan = e.app.buttons["Scan QR code"]
        XCTAssertTrue(scan.waitForExistence(timeout: 10), "Log in's Scan QR code, always shown")
        XCTAssertTrue(e.containing("paste the link instead").exists, "a simulator can't scan: it says so")
        XCTAssertFalse(scan.isEnabled, "…and the button is off")
        XCTAssertTrue(e.app.buttons["Enter workspace address"].exists)
        e.shot("onboarding-03-login")
    }

    /// Where do I find the QR code? — three pages, back to Log in.
    @MainActor
    func test02HelpPages() throws {
        let e = try E2E(self)
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
        e.app.buttons["Log in"].tap()
        let help = e.app.buttons["Where do I find the QR code?"]
        XCTAssertTrue(help.waitForExistence(timeout: 10))
        help.tap()
        for (i, title) in ["1. Open settings", "2. Add a device", "3. Scan it"].enumerated() {
            XCTAssertTrue(e.element(title).waitForExistence(timeout: 10), "help page \(i + 1)")
            e.shot("onboarding-04-help-\(i + 1)")
            if i == 0 { XCTAssertTrue(e.containing("Click settings").exists, "the shell's own word: settings") }
            if i < 2 { e.app.buttons["Next"].tap() }
        }
        e.app.buttons["Back to Log in"].tap()
        XCTAssertTrue(e.app.buttons["Enter workspace address"].waitForExistence(timeout: 10), "back on Log in")
    }

    /// Run your own xbin: the install one-liner, copied.
    @MainActor
    func test03RunYourOwn() throws {
        let e = try E2E(self)
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Run your own xbin"].waitForExistence(timeout: 30))
        e.app.buttons["Run your own xbin"].tap()
        XCTAssertTrue(e.element("curl -fsSL https://xbin.dev/install.sh | sh").waitForExistence(timeout: 10), "the install command")
        e.app.buttons["Copy the command"].tap()
        XCTAssertTrue(e.app.buttons["Copied"].waitForExistence(timeout: 5), "the copy button says it copied")
        XCTAssertTrue(e.app.buttons["Open xbin.dev"].exists)
        e.shot("onboarding-05-run-your-own")
    }

    /// Log in → the address → the sign-in methods the workspace offers
    /// (login/methods: password here) → the password: the device enrolls and
    /// the workspace opens.
    @MainActor
    func test04SignInByAddress() async throws {
        let e = try E2E(self)
        let before = try await e.server.devices().count
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
        e.signIn(shots: "onboarding-06")
        _ = e.findTile("apps/counter")
        e.shot("onboarding-06-signed-in")
        let after = try await e.server.devices().count
        XCTAssertEqual(after, before + 1, "this simulator enrolled a device")
    }

    /// Join with an invite: the link an admin made → "You're invited to …"
    /// → a password → the device enrolls, the invite is spent.
    @MainActor
    func test05JoinWithInvite() async throws {
        let e = try E2E(self)
        let (id, link) = try await e.server.invite(name: "Invited Tester")
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Join with an invite"].waitForExistence(timeout: 30))
        e.app.buttons["Join with an invite"].tap()
        let field = e.element("Invite link", in: e.app.textFields)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the invite link field")
        field.tap()
        field.typeText(link + "\n")
        XCTAssertTrue(e.containing("You're invited to").waitForExistence(timeout: 30), "the invite, checked")
        XCTAssertTrue(e.containing("as Invited Tester").exists, "…names the account")
        e.shot("onboarding-07-invited")
        let pw = "e2e-invite-\(Int.random(in: 100_000...999_999))"
        let new = e.element("New password", in: e.app.secureTextFields)
        new.tap()
        new.typeText(pw)
        let confirm = e.element("Confirm password", in: e.app.secureTextFields)
        confirm.tap()
        confirm.typeText(pw + "\n")
        e.dismissSystemAlerts()
        guard e.app.buttons["Workspaces"].waitForExistence(timeout: 60) else {
            e.shot("onboarding-07-join-failed")
            XCTFail("the workspace did not open after joining (the page's error is in the screenshot)")
            return
        }
        e.dismissSavePassword()
        e.shot("onboarding-07-joined")
        // (GET /api/xbin/users leaves out invitePending and deviceCount when false or 0.)
        let found = try await e.server.account(id)
        let account = try XCTUnwrap(found, "the invited account")
        XCTAssertFalse(account.invitePending ?? false, "the invite is spent")
        XCTAssertEqual(account.deviceCount ?? 0, 1, "the invitee's first device is this simulator")
    }

    /// An address nothing answers at: "Can't connect", before any sign-in.
    @MainActor
    func test06UnreachableAddress() throws {
        let e = try E2E(self)
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
        e.app.buttons["Log in"].tap()
        e.enterAddress("http://127.0.0.1:1")
        XCTAssertTrue(e.containing("Can't connect").waitForExistence(timeout: 45), "the can't-connect card")
        XCTAssertTrue(e.containing("address your phone uses").exists, "…with what to try")
        e.shot("onboarding-08-cant-connect")
    }

    /// An enrollment link with a code the workspace never minted: the
    /// workspace answers (login/methods) and refuses the code — "Code refused".
    @MainActor
    func test07BadCode() throws {
        let e = try E2E(self)
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
        e.app.buttons["Log in"].tap()
        pasteBadCode(e)
        XCTAssertTrue(e.containing("Code refused").waitForExistence(timeout: 30), "the code-refused card")
        e.shot("onboarding-09-code-refused")
    }

    /// "Sign in again" on a workspace whose device was removed: Log in on
    /// its address, and the new sign-in replaces the workspace instead of
    /// adding a second one.
    @MainActor
    func test08SignInAgainReplaces() async throws {
        let e = try E2E(self)
        e.launchFresh()
        XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
        e.signIn()
        // Remove this simulator's device on the server (the newest one).
        let devices = try await e.server.devices()
        let mine = try XCTUnwrap(devices.max { ($0.created ?? 0) < ($1.created ?? 0) })
        try await e.server.removeDevice(mine.id)
        // Back to the foreground: the app refreshes, its device login fails.
        XCUIDevice.shared.press(.home)
        e.app.activate()
        let again = e.app.buttons["Sign in again"]
        XCTAssertTrue(again.waitForExistence(timeout: 45), "the sign-in problem bar")
        e.shot("onboarding-10-device-removed")
        again.tap()
        let field = e.element("Workspace address", in: e.app.textFields)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "Log in on the workspace's address")
        XCTAssertTrue(e.containing("replaced, not added twice").exists)
        e.shot("onboarding-10-sign-in-again")
        e.app.buttons["Continue"].tap()
        let user = e.element("Username", in: e.app.textFields)
        XCTAssertTrue(user.waitForExistence(timeout: 30), "the password form")
        user.tap()
        user.typeText(e.server.user)
        let password = e.element("Password", in: e.app.secureTextFields)
        password.tap()
        password.typeText(e.server.password + "\n")
        XCTAssertTrue(e.app.buttons["Sign in again"].waitForNonExistence(timeout: 60), "signed in again")
        e.dismissSavePassword()
        // One workspace in the switcher, not two.
        e.app.buttons["Workspaces"].tap()
        let rows = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@", e.server.url.host ?? "127.0.0.1"))
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 10))
        e.shot("onboarding-10-replaced")
        XCTAssertEqual(rows.count, 1, "the workspace was replaced, not added twice")
        let now = try await e.server.devices()
        XCTAssertFalse(now.contains { $0.id == mine.id }, "the removed device stays removed")
    }

    /// Every onboarding screen, light and dark, for looking at.
    @MainActor
    func test10Gallery() async throws {
        for mode in ["light", "dark"] {
            var n = 0
            func shot(_ e: E2E, _ name: String) {
                n += 1
                e.shot(String(format: "gallery-%@-%02d-%@", mode, n, name))
            }
            // The Welcome, What is xbin?, Log in, the help, the address and
            // the sign-in methods.
            var e = try E2E(self)
            e.launchFresh(appearance: mode)
            XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
            shot(e, "welcome")
            e.app.buttons["What is xbin?"].tap()
            for i in 1...3 {
                XCTAssertTrue(e.element("\(i) of 3").waitForExistence(timeout: 10))
                shot(e, "about-\(i)")
                e.app.buttons[i < 3 ? "Next" : "Log in"].tap()
            }
            XCTAssertTrue(e.app.buttons["Where do I find the QR code?"].waitForExistence(timeout: 10))
            shot(e, "login")
            e.app.buttons["Where do I find the QR code?"].tap()
            for i in 1...3 {
                XCTAssertTrue(e.element("\(i) of 3").waitForExistence(timeout: 10))
                shot(e, "help-\(i)")
                e.app.buttons[i < 3 ? "Next" : "Back to Log in"].tap()
            }
            let enter = e.app.buttons["Enter workspace address"]
            XCTAssertTrue(enter.waitForExistence(timeout: 10))
            enter.tap()
            let field = e.element("Workspace address", in: e.app.textFields)
            XCTAssertTrue(field.waitForExistence(timeout: 10))
            shot(e, "address")
            field.tap()
            field.typeText(e.server.address + "\n")
            XCTAssertTrue(e.element("Username", in: e.app.textFields).waitForExistence(timeout: 30))
            shot(e, "methods")

            // Run your own xbin.
            e = try E2E(self)
            e.launchFresh(appearance: mode)
            XCTAssertTrue(e.app.buttons["Run your own xbin"].waitForExistence(timeout: 30))
            e.app.buttons["Run your own xbin"].tap()
            XCTAssertTrue(e.app.buttons["Copy the command"].waitForExistence(timeout: 10))
            shot(e, "run-your-own")

            // Join with an invite, up to the password (the invite stays unspent).
            let (_, link) = try await e.server.invite(name: "Gallery Invitee")
            e = try E2E(self)
            e.launchFresh(appearance: mode)
            XCTAssertTrue(e.app.buttons["Join with an invite"].waitForExistence(timeout: 30))
            e.app.buttons["Join with an invite"].tap()
            let invite = e.element("Invite link", in: e.app.textFields)
            XCTAssertTrue(invite.waitForExistence(timeout: 10))
            shot(e, "invite")
            invite.tap()
            invite.typeText(link + "\n")
            XCTAssertTrue(e.containing("You're invited to").waitForExistence(timeout: 30))
            shot(e, "invite-accept")

            // The failures: can't connect, the enrollment question, code refused.
            e = try E2E(self)
            e.launchFresh(appearance: mode)
            XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
            e.app.buttons["Log in"].tap()
            e.enterAddress("http://127.0.0.1:1")
            XCTAssertTrue(e.containing("Can't connect").waitForExistence(timeout: 45))
            shot(e, "cant-connect")
            e = try E2E(self)
            e.launchFresh(appearance: mode)
            XCTAssertTrue(e.app.buttons["Log in"].waitForExistence(timeout: 30))
            e.app.buttons["Log in"].tap()
            pasteBadCode(e, shoot: { shot(e, "enroll-question") })
            XCTAssertTrue(e.containing("Code refused").waitForExistence(timeout: 30))
            shot(e, "code-refused")
        }
    }

    /// On Log in: paste an enrollment link with a code the workspace never
    /// minted, and answer "Add".
    @MainActor
    private func pasteBadCode(_ e: E2E, shoot: (() -> Void)? = nil) {
        let field = e.element("Paste the link", in: e.app.textFields)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "Log in's link field")
        field.tap()
        let u = e.server.address.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? e.server.address
        field.typeText("xbin://enroll?u=\(u)&c=\(String(repeating: "A", count: 26))\n")
        let host = (e.server.url.host ?? "") + (e.server.url.port.map { ":\($0)" } ?? "")
        let add = e.app.buttons["Add \(host)"]
        XCTAssertTrue(add.waitForExistence(timeout: 10), "the enrollment question")
        shoot?()
        add.tap()
    }
}

extension E2EServer {
    /// A row of GET /api/xbin/devices (the e2e account's devices).
    struct Device: Decodable, Sendable {
        let id: String
        let created: Int?
    }

    struct Devices: Decodable, Sendable { let devices: [Device] }

    func devices() async throws -> [Device] {
        try JSONDecoder().decode(Devices.self, from: await send("GET", "/api/xbin/devices")).devices
    }

    func removeDevice(_ id: String) async throws {
        _ = try await send("DELETE", "/api/xbin/devices/\(id)")
    }
}
