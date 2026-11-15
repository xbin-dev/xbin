#if canImport(UIKit)
import UIKit
#endif
import XCTest

// What XbinWorkspaceAgentStatesTests needs of the e2e xbind and of the app:
// the workspace put in a state through its API, and the chrome driven in it.

// MARK: - The e2e account's workspace, put in a state

extension E2EServer {
    /// The e2e account's name, as the app's bars say it.
    var userName: String { "E2E Tester" }

    func wsState() async throws -> [String: Any] {
        (try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/delegate/state")) as? [String: Any]) ?? [:]
    }

    /// Who gets an agent: "everyone" | "chosen" (the defaults' entry goes).
    func wsGive(who: String) async throws {
        _ = try await send("PUT", "/api/xbin/delegate/workspace", json: ["who": who])
    }

    /// The workspace's switch.
    func wsSwitch(on: Bool) async throws {
        _ = try await send("PUT", "/api/xbin/delegate/workspace", json: ["enabled": on])
    }

    private static let other = "e2e-ws-other"

    /// Another account is given an agent, and nobody else: the e2e admin
    /// administers it and has none of their own.
    func wsGiveOnlyAnother() async throws {
        _ = try? await send("POST", "/api/xbin/users", json: ["id": Self.other, "name": "Another Person"])
        try await wsGive(who: "chosen")
        _ = try await send("PUT", "/api/xbin/access", json: ["tile": Self.wsAgent, "kind": "user", "id": Self.other, "level": "read"])
    }

    /// That account and its entry, gone.
    func wsDropOther() async {
        _ = try? await send("PUT", "/api/xbin/access", json: ["tile": Self.wsAgent, "kind": "user", "id": Self.other, "level": ""])
        _ = try? await send("DELETE", "/api/xbin/users/\(Self.other)")
    }

    /// How many messages the session's conversation holds.
    func wsMessageCount(_ run: Int) async throws -> Int {
        (try await wsCall("GET", "/runs/\(run)/view")["messages"] as? [[String: Any]] ?? []).count
    }

    /// The turn a tile agent's session is on (0: it has taken no prompt).
    func wsSessionTurn(_ id: String) async throws -> Int {
        let rows = (try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/term/sessions?compact=1")) as? [[String: Any]]) ?? []
        return rows.first { ($0["id"] as? String) == id }?["turn"] as? Int ?? 0
    }

    /// The scripted model proposes two grants on apps/note (the admin opted
    /// in): Welcome as a reader, the Counter as a writer. The change set's
    /// id at xbind.
    func wsProposeGrants(_ run: Int) async throws -> String {
        try await wsSay(run, "Let apps/welcome and apps/counter use apps/note")
        let cards = try await wsCall("GET", "/ws/sessions/\(run)/cards")
        let list = (cards["requests"] as? [[String: Any]] ?? []).filter { ($0["kind"] as? String) == "changeset" && ($0["state"] as? String) == "proposed" }
        return try XCTUnwrap(list.compactMap { $0["ref"] as? String }.last { !$0.isEmpty }, "a proposed change set on the session's cards")
    }

    /// The grants other tiles hold on apps/note.
    func wsGrantsToNote() async throws -> [String] {
        let j = try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/grants")) as? [String: Any]
        return (j?["grants"] as? [[String: Any]] ?? []).compactMap { g in
            guard (g["target"] as? String) == "apps/note", let from = g["from"] as? String, from.hasPrefix("apps/") else { return nil }
            return "\(from) as \(g["role"] as? String ?? "")"
        }
    }

    /// Every proposed set rejected, and the test's grants on apps/note gone.
    func wsCleanChangeSets() async {
        let sets = ((try? JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/delegate/changesets"))) as? [String: Any])?["changesets"] as? [[String: Any]] ?? []
        for s in sets where (s["state"] as? String) == "proposed" {
            if let id = s["id"] as? String { _ = try? await send("POST", "/api/xbin/delegate/changesets/\(id)/reject", json: [:]) }
        }
        for (from, role) in [("apps/welcome", "reader"), ("apps/counter", "writer")] {
            _ = try? await send("DELETE", "/api/xbin/grants", json: ["from": from, "target": "apps/note", "role": role])
        }
    }
}

// MARK: - Driving the chrome in a state

extension XCUIElement {
    /// Whether the element says it is the selected one (a radio row).
    var wsSelected: Bool { isSelected }
}

extension E2E {
    /// A Debug build's `-XbinAgentDemo <state>` for this launch.
    func wsDemo(_ state: String) {
        app.launchArguments += ["-XbinAgentDemo", state]
    }

    /// A link handed to the running app, as a notification's tap does.
    func wsOpenLink(_ link: String) {
        XCUIDevice.shared.system.open(URL(string: link)!)
        let open = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["Open"]
        if open.waitForExistence(timeout: 3) { open.tap() }
    }

    /// Home's Needs you → the row `id` → its Review….
    func wsInbox(_ id: String, file: StaticString = #filePath, line: UInt = #line) {
        let needs = app.buttons["Needs you"]
        expect(needs, 60, "Home's Needs you row", file: file, line: line)
        XCTAssertTrue(wsHittable(needs), "Needs you can be tapped", file: file, line: line)
        needs.tap()
        let row = app.buttons.matching(identifier: id).firstMatch
        expect(row, 30, "the inbox's \(id)", file: file, line: line)
        wsScrollTo(row)
        row.tap()
    }

    /// Scrolls the sheet in front until `element` can be tapped.
    func wsScrollTo(_ element: XCUIElement, file: StaticString = #filePath, line: UInt = #line) {
        var tries = 0
        while element.exists, !element.isHittable, tries < 12 {
            // (a short drag well inside the sheet: never its grabber, never a button)
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.55))
            let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
            from.press(forDuration: 0.05, thenDragTo: to)
            tries += 1
        }
        XCTAssertTrue(element.exists && element.isHittable, "\(element) can be scrolled into reach", file: file, line: line)
    }

    /// The identifiers among `ids` in the order the accessibility tree has
    /// their elements.
    func wsTreeOrder(of ids: [String]) -> [String] {
        guard let snapshot = try? app.snapshot() else { return [] }
        var out: [String] = []
        var stack: [any XCUIElementSnapshot] = [snapshot]
        while let s = stack.popLast() {
            if ids.contains(s.identifier), !out.contains(s.identifier) { out.append(s.identifier) }
            stack.append(contentsOf: s.children.reversed())
        }
        return out
    }
}
