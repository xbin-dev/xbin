import Foundation
import XCTest

// The UI tests' two halves: E2EServer talks to the xbind over HTTP (the
// e2e account's own session) to check what the app did; E2E drives the app.
// native/AGENTS.md → "Mac mini" has the recipe (mac-remote.sh e2e); the
// tests skip when XBIN_E2E_URL is unset, which is how the hosted CI builds
// them without an xbind to talk to.

/// The xbind under test, from the test runner's environment (xcodebuild
/// passes TEST_RUNNER_XBIN_E2E_URL / _USER / _PASSWORD as XBIN_E2E_*): its
/// address and an admin account, which the app signs in with through Log
/// in (there is no token login) and the checks use too.
struct E2EServer: Sendable {
    let url: URL
    let user: String
    let password: String
    private let sessionBox = SessionBox()

    /// The workspace address as the app's address field takes it.
    var address: String { url.absoluteString.hasSuffix("/") ? String(url.absoluteString.dropLast()) : url.absoluteString }

    static func fromEnvironment() -> E2EServer? {
        let env = ProcessInfo.processInfo.environment
        guard let raw = env["XBIN_E2E_URL"], !raw.isEmpty, let url = URL(string: raw),
              let user = env["XBIN_E2E_USER"], !user.isEmpty,
              let password = env["XBIN_E2E_PASSWORD"], !password.isEmpty else { return nil }
        return E2EServer(url: url, user: user, password: password)
    }

    struct Failure: Error, CustomStringConvertible {
        let description: String
    }

    struct Count: Decodable, Sendable {
        let count: Int
    }

    /// A row of GET /api/xbin/term/sessions (docs/protocol.md).
    struct Session: Decodable, Sendable {
        let id: String
        let cwd: String?
        let kind: String?
        let provider: String?
    }

    /// The checks' session: one POST /api/xbin/login per test process.
    final class SessionBox: @unchecked Sendable {
        private let lock = NSLock()
        private var token: String?
        var value: String? {
            get { lock.lock(); defer { lock.unlock() }; return token }
            set { lock.lock(); token = newValue; lock.unlock() }
        }
    }

    struct Login: Decodable, Sendable { let token: String }

    private func session() async throws -> String {
        if let t = sessionBox.value { return t }
        var r = URLRequest(url: URL(string: address + "/api/xbin/login")!)
        r.httpMethod = "POST"
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONSerialization.data(withJSONObject: ["username": user, "password": password])
        r.timeoutInterval = 15
        let (data, response) = try await URLSession.shared.data(for: r)
        guard (response as? HTTPURLResponse)?.statusCode == 200 else {
            throw Failure(description: "POST /api/xbin/login as \(user) → \((response as? HTTPURLResponse)?.statusCode ?? 0)")
        }
        let t = try JSONDecoder().decode(Login.self, from: data).token
        sessionBox.value = t
        return t
    }

    func send(_ method: String, _ path: String, json: [String: Any]? = nil) async throws -> Data {
        var r = URLRequest(url: URL(string: address + path)!)
        r.httpMethod = method
        r.setValue("Bearer \(try await session())", forHTTPHeaderField: "Authorization")
        if let json {
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
            r.httpBody = try JSONSerialization.data(withJSONObject: json)
        }
        r.timeoutInterval = 15
        let (data, response) = try await URLSession.shared.data(for: r)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            throw Failure(description: "\(method) \(path) → \(status): \(String(decoding: data.prefix(200), as: UTF8.self))")
        }
        return data
    }

    /// examples/counter-go's value (GET /api/apps/counter/count).
    func counter() async throws -> Int {
        try JSONDecoder().decode(Count.self, from: await send("GET", "/api/apps/counter/count")).count
    }

    /// The owner's live terminal and agent sessions on a tile.
    func sessions(cwd: String) async throws -> [Session] {
        let q = cwd.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? cwd
        return try JSONDecoder().decode([Session].self, from: await send("GET", "/api/xbin/term/sessions?cwd=\(q)"))
    }

    /// Ends a session of either kind (DELETE /api/xbin/term/sessions/<id>).
    func end(_ id: String) async {
        _ = try? await send("DELETE", "/api/xbin/term/sessions/\(id)")
    }

    struct Invited: Decodable, Sendable {
        let inviteUrl: String?
        let inviteLink: String?
    }

    /// A new account with an invite and no password (POST /api/xbin/users):
    /// its id and the invite link as the admin console shows it.
    func invite(name: String) async throws -> (id: String, link: String) {
        let id = "invitee-\(Int(Date().timeIntervalSince1970))-\(Int.random(in: 100...999))"
        let d = try JSONDecoder().decode(Invited.self, from: await send("POST", "/api/xbin/users", json: ["id": id, "name": name]))
        guard let link = d.inviteLink ?? d.inviteUrl.map({ address + $0 }) else {
            throw Failure(description: "POST /api/xbin/users: no invite link")
        }
        return (id, link)
    }

    struct User: Decodable, Sendable {
        let id: String
        let invitePending: Bool?
        let deviceCount: Int?
    }

    struct Users: Decodable, Sendable { let users: [User] }

    /// An account as the admin console sees it (GET /api/xbin/users).
    func account(_ id: String) async throws -> User? {
        try JSONDecoder().decode(Users.self, from: await send("GET", "/api/xbin/users")).users.first { $0.id == id }
    }
}

/// Drives the app. Every query is by what a person sees (labels, the
/// placeholder of a field), so the tests need nothing from the app's code.
@MainActor
final class E2E {
    let app: XCUIApplication
    let server: E2EServer
    private let test: XCTestCase

    init(_ test: XCTestCase) throws {
        guard let server = E2EServer.fromEnvironment() else {
            throw XCTSkip("XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
        }
        self.server = server
        self.test = test
        self.app = XCUIApplication()
    }

    func launch() {
        app.launchArguments += ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
    }

    /// Launches as a fresh install would start (Debug builds'
    /// -XbinFreshStart: no workspace, the saved list untouched), in `appearance`
    /// (light or dark; nil = the simulator's).
    func launchFresh(appearance: String? = nil) {
        app.launchArguments += ["-XbinFreshStart", "YES"]
        if let appearance { app.launchArguments += ["-XbinAppearance", appearance] }
        launch()
    }

    // MARK: Screenshots

    /// A screenshot, kept in the result bundle and, when the run names one,
    /// written to E2E_DIR as <name>.png (mac-remote.sh pulls that back).
    func shot(_ name: String) {
        let screenshot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        test.add(attachment)
        if let dir = ProcessInfo.processInfo.environment["E2E_DIR"], !dir.isEmpty {
            let file = URL(fileURLWithPath: dir).appendingPathComponent(name + ".png")
            try? screenshot.pngRepresentation.write(to: file)
        }
    }

    // MARK: Finding things

    /// Any element whose label, identifier or placeholder is `text`.
    func element(_ text: String, in query: XCUIElementQuery? = nil) -> XCUIElement {
        let predicate = NSPredicate(format: "label == %@ OR identifier == %@ OR placeholderValue == %@", text, text, text)
        return (query ?? app.descendants(matching: .any)).matching(predicate).firstMatch
    }

    /// Any element whose label contains `text`.
    func containing(_ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    /// The first of `elements` to exist within `timeout` seconds, or nil.
    func first(of elements: [XCUIElement], timeout: TimeInterval) -> Int? {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            for (i, e) in elements.enumerated() where e.exists { return i }
            // waitForExistence polls without blocking the app.
            _ = elements[0].waitForExistence(timeout: 0.5)
        } while Date() < deadline
        return nil
    }

    /// Polls `condition` (an on-screen check) until it holds or `timeout`
    /// seconds pass; whether it held.
    func until(_ timeout: TimeInterval, _ condition: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if condition() { return true }
            // waitForExistence polls without blocking the app.
            _ = app.otherElements["xbin-e2e-never"].waitForExistence(timeout: 0.3)
        } while Date() < deadline
        return condition()
    }

    // MARK: The terminal

    /// The key row's keys, by their accessibility labels (AccessorySlot).
    static let rowKeys = ["Escape", "Control, sticky", "Tab", "Left arrow", "Down arrow", "Up arrow", "Right arrow"]

    /// The software keyboard's frame while one is on screen — nil while it
    /// is hidden or still sliding in (its element exists then, off screen).
    /// Its element is the key plane (226 pt on an iPhone 18 Pro).
    func softKeyboard() -> CGRect? {
        let kb = app.keyboards.firstMatch
        guard kb.exists else { return nil }
        let f = kb.frame, window = app.windows.firstMatch.frame
        return f.height > 120 && f.minY < window.maxY - 120 && f.maxY <= window.maxY + 1 ? f : nil
    }

    /// Taps `element` right after typing. XCUITest types with key presses:
    /// the software keyboard hides while typeText types and slides back
    /// 1–3.5 s later, moving what sits above it — the composer's Send, a
    /// form scrolled to its field. A tap resolved while it was away lands
    /// where the keyboard then is: on a key (test01's Connect and test05's
    /// Send, 2026-09-27; the simulator's log has each tap's point). So: wait
    /// (up to 12 s) until the keyboard is back and it and the element have
    /// stayed put for a second, scroll the element out from under it if it
    /// is covered, then tap; with no keyboard coming back, tap where the
    /// element is.
    func tapAfterTyping(_ element: XCUIElement) {
        var seen: (element: CGRect, keyboard: CGRect?)?
        var since = Date()
        let settled = until(12) {
            let now = (element: element.frame, keyboard: softKeyboard())
            guard let s = seen, s.element == now.element, s.keyboard == now.keyboard else {
                seen = now
                since = Date()
                return false
            }
            return now.keyboard != nil && Date().timeIntervalSince(since) >= 1
        }
        if !element.isHittable {
            app.swipeUp() // out from under the keyboard: a form or list scrolls it into view
            _ = until(5) { element.isHittable }
        }
        print("xbin-e2e: tap \(element.frame) after typing (settled \(settled)), keyboard \(softKeyboard().map { "\($0)" } ?? "none")")
        element.tap()
    }

    /// A key at `f` sits on the keyboard at `kb`: not overlapping it, and no
    /// further above it than the row's own height. The keyboard's element
    /// is its key plane, a little under the glass's top edge — a 46 pt row
    /// on the glass starts about 60 pt above it (iPhone 18 Pro, iOS 27.0).
    static func onTop(_ f: CGRect, of kb: CGRect) -> Bool { f.maxY <= kb.minY + 1 && f.minY >= kb.minY - 80 }

    /// The terminal's key row is on top of the software keyboard: every key
    /// of it on screen, hittable, just above the keyboard's top edge (not
    /// behind it, not floating away from it). It waits for the keyboard to
    /// come to rest first: it slides in, and XCUITest's typing can hide it
    /// for a moment. The frames go to the log.
    func keyRowAboveKeyboard(file: StaticString = #filePath, line: UInt = #line) {
        var seen = ""
        let ends = [Self.rowKeys[0], Self.rowKeys[Self.rowKeys.count - 1]]
        let above = until(20) {
            guard let kb = softKeyboard() else { seen = "no keyboard on screen"; return false }
            seen = "keyboard \(kb)"
            for label in ends {
                let f = app.buttons[label].frame
                seen += ", \(label) \(f)"
                if !Self.onTop(f, of: kb) { return false }
            }
            return true
        }
        print("xbin-e2e: key row: \(seen)")
        XCTAssertTrue(above, "the key row sits right above the keyboard: \(seen)", file: file, line: line)
        guard above, let kb = softKeyboard() else { return }
        for label in Self.rowKeys {
            let key = app.buttons[label]
            XCTAssertTrue(key.exists && key.isHittable, "the key row's \(label)", file: file, line: line)
            let f = key.frame
            print("xbin-e2e: key \(label) \(f), keyboard \(kb)")
            XCTAssertTrue(Self.onTop(f, of: kb), "\(label) sits right above the keyboard: \(f), keyboard \(kb)", file: file, line: line)
        }
    }

    /// Taps through a system alert (Save Password?, a permission) if one is up.
    func dismissSystemAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let alert = springboard.alerts.firstMatch
        guard alert.exists else { return }
        for title in ["Not Now", "Don’t Allow", "Don't Allow", "OK", "Allow"] {
            let button = alert.buttons[title]
            if button.exists {
                button.tap()
                return
            }
        }
    }

    // MARK: The workspace

    /// The app shows a workspace: when it has none (a fresh simulator), signs
    /// in to the xbind under test as a person does — Log in → Enter workspace
    /// address → the password — which enrolls this device too.
    func ensureWorkspace(file: StaticString = #filePath, line: UInt = #line) {
        let workspaces = app.buttons["Workspaces"]
        let login = app.buttons["Log in"]
        guard let seen = first(of: [workspaces, login], timeout: 30) else {
            shot("00-no-start-screen")
            XCTFail("neither a workspace nor the Welcome after launch", file: file, line: line)
            return
        }
        if seen == 0 { return }
        signIn(file: file, line: line)
    }

    /// From the Welcome: Log in → Enter workspace address → Continue →
    /// the account's name and password → the workspace opens.
    func signIn(shots prefix: String? = nil, file: StaticString = #filePath, line: UInt = #line) {
        app.buttons["Log in"].tap()
        enterAddress(server.address, file: file, line: line)
        let user = element("Username", in: app.textFields)
        guard user.waitForExistence(timeout: 30) else {
            shot("01-sign-in-no-form")
            XCTFail("the password form after the address (the page's error is in the screenshot)", file: file, line: line)
            return
        }
        if let prefix { shot(prefix + "-methods") }
        user.tap()
        user.typeText(server.user)
        let password = element("Password", in: app.secureTextFields)
        password.tap()
        password.typeText(server.password + "\n")
        dismissSystemAlerts()
        if !app.buttons["Workspaces"].waitForExistence(timeout: 60) {
            shot("01-sign-in-failed")
            XCTFail("the workspace did not open after signing in (the page's error is in the screenshot)", file: file, line: line)
            return
        }
        dismissSavePassword()
    }

    /// iOS offers to keep a password just typed in its Passwords app ("Save
    /// Password?", over the workspace): Not Now.
    func dismissSavePassword() {
        let inApp = app.buttons["Not Now"]
        let onBoard = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["Not Now"]
        guard let i = first(of: [inApp, onBoard], timeout: 8) else { return }
        (i == 0 ? inApp : onBoard).tap()
    }

    /// On Log in: Enter workspace address → `address` → Continue.
    func enterAddress(_ address: String, file: StaticString = #filePath, line: UInt = #line) {
        let enter = app.buttons["Enter workspace address"]
        XCTAssertTrue(enter.waitForExistence(timeout: 10), "Log in's Enter workspace address", file: file, line: line)
        enter.tap()
        let field = element("Workspace address", in: app.textFields)
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the address field", file: file, line: line)
        field.tap()
        field.typeText(address + "\n")
    }

    // MARK: Tiles

    /// The navigator's row for a tile: a button whose label holds its path.
    func tileRow(_ path: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", path)).firstMatch
    }

    /// Finds a tile's row in the navigator — the home screen, or the Tiles
    /// sheet over a surface — searching for it when it isn't in view.
    func findTile(_ path: String, file: StaticString = #filePath, line: UInt = #line) -> XCUIElement {
        let tiles = app.buttons["Tiles"]
        if tiles.exists { tiles.tap() }
        let row = tileRow(path)
        if row.waitForExistence(timeout: 5), row.isHittable { return row }
        var search = app.searchFields["Search tiles"]
        if !search.exists {
            app.swipeDown() // the navigation-bar drawer hides the field until pulled
            search = app.searchFields["Search tiles"]
        }
        if search.waitForExistence(timeout: 5) {
            search.tap()
            search.typeText(path)
        }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the navigator lists \(path)", file: file, line: line)
        return row
    }

    func openTile(_ path: String, file: StaticString = #filePath, line: UInt = #line) {
        findTile(path, file: file, line: line).tap()
    }

    /// A tile's context-menu action in the navigator ("Terminal here",
    /// "Agent here"); the tile screen's ⋯ menu when the long press shows none.
    func tileAction(_ path: String, _ action: String, file: StaticString = #filePath, line: UInt = #line) {
        let row = findTile(path, file: file, line: line)
        row.press(forDuration: 1.2)
        let item = app.buttons[action]
        if item.waitForExistence(timeout: 5) {
            item.tap()
            return
        }
        // Fallback: close whatever the press opened (a tap by the status
        // bar), open the tile, then its ⋯ menu.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.05, dy: 0.02)).tap()
        openTile(path, file: file, line: line)
        let more = element("More", in: app.buttons)
        XCTAssertTrue(more.waitForExistence(timeout: 20), "the tile's ⋯ menu", file: file, line: line)
        more.tap()
        XCTAssertTrue(item.waitForExistence(timeout: 5), "\(action) in the tile's menu", file: file, line: line)
        item.tap()
    }

    // MARK: Waiting on the server

    /// Polls `condition` (a server check) until it holds or `timeout` passes.
    func eventually(_ what: String, timeout: TimeInterval, file: StaticString = #filePath, line: UInt = #line,
                    _ condition: () async throws -> Bool) async {
        let deadline = Date().addingTimeInterval(timeout)
        var last: (any Error)?
        while Date() < deadline {
            do {
                if try await condition() { return }
            } catch {
                last = error
            }
            try? await Task.sleep(nanoseconds: 500_000_000)
        }
        XCTFail("\(what): not within \(Int(timeout)) s\(last.map { " (\($0))" } ?? "")", file: file, line: line)
    }
}
