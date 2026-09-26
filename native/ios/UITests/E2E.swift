import Foundation
import XCTest

// The UI tests' two halves: E2EServer talks to the xbind over HTTP (the
// owner token) to check what the app did; E2E drives the app. native/AGENTS.md
// → "Mac mini" has the recipe (mac-remote.sh e2e); the tests skip when
// XBIN_E2E_URL is unset, which is how the hosted CI builds them without an
// xbind to talk to.

/// The xbind under test, from the test runner's environment (xcodebuild
/// passes TEST_RUNNER_XBIN_E2E_URL / _TOKEN as XBIN_E2E_URL / _TOKEN).
struct E2EServer: Sendable {
    let url: URL
    let token: String

    /// The workspace address as the app's add-workspace form takes it.
    var address: String { url.absoluteString.hasSuffix("/") ? String(url.absoluteString.dropLast()) : url.absoluteString }

    static func fromEnvironment() -> E2EServer? {
        let env = ProcessInfo.processInfo.environment
        guard let raw = env["XBIN_E2E_URL"], !raw.isEmpty, let url = URL(string: raw),
              let token = env["XBIN_E2E_TOKEN"], !token.isEmpty else { return nil }
        return E2EServer(url: url, token: token)
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

    private func request(_ method: String, _ path: String) -> URLRequest {
        var r = URLRequest(url: URL(string: address + path)!)
        r.httpMethod = method
        r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        r.timeoutInterval = 15
        return r
    }

    private func send(_ method: String, _ path: String) async throws -> Data {
        let (data, response) = try await URLSession.shared.data(for: request(method, path))
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
            throw XCTSkip("XBIN_E2E_URL / XBIN_E2E_TOKEN unset: no xbind to test against (native/AGENTS.md → Mac mini)")
        }
        self.server = server
        self.test = test
        self.app = XCUIApplication()
    }

    func launch() {
        app.launchArguments += ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
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

    /// The app shows a workspace: when it has none (a fresh simulator), adds
    /// the xbind under test by address + token (Add a workspace → Advanced).
    func ensureWorkspace(file: StaticString = #filePath, line: UInt = #line) {
        let workspaces = app.buttons["Workspaces"]
        let add = app.buttons["Add a workspace"]
        guard let seen = first(of: [workspaces, add], timeout: 30) else {
            shot("00-no-start-screen")
            XCTFail("neither a workspace nor the welcome screen after launch", file: file, line: line)
            return
        }
        if seen == 0 { return }
        addWorkspace(file: file, line: line)
    }

    func addWorkspace(file: StaticString = #filePath, line: UInt = #line) {
        app.buttons["Add a workspace"].tap()
        let address = element("Workspace address (https://…)", in: app.textFields)
        XCTAssertTrue(address.waitForExistence(timeout: 10), "the add-workspace form", file: file, line: line)
        address.tap()
        address.typeText(server.address)
        let advanced = element("Advanced: server URL and token", in: app.buttons)
        if !advanced.isHittable { app.swipeUp() }
        advanced.tap()
        let token = element("Token", in: app.secureTextFields)
        XCTAssertTrue(token.waitForExistence(timeout: 5), "the token field", file: file, line: line)
        token.tap()
        token.typeText(server.token)
        shot("01-add-workspace-form")
        app.buttons["Connect"].tap()
        dismissSystemAlerts()
        if !app.buttons["Workspaces"].waitForExistence(timeout: 30) {
            shot("01-add-workspace-failed")
            XCTFail("the workspace did not open after Connect (the form's error is in the screenshot)", file: file, line: line)
        }
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
