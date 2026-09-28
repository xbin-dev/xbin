import XCTest

/// A tile's sessions screen (D132), end to end on a simulator against the
/// e2e xbind (native/AGENTS.md → "Mac mini"): the web terminal window's
/// counterpart — the launcher, tabs across shells and agents, the tools.
final class XbinSessionsTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    /// Long press → New session… opens the tile's sessions screen on its
    /// launcher; Bash makes a shell tab, `+` shows the launcher again, the
    /// fake agent makes an agent tab beside it; a tap switches back, the
    /// shell still there; a tab's long press ends the agent's session.
    @MainActor
    func test01LauncherAndTabs() async throws {
        let e = try E2E(self)
        for s in try await e.server.sessions(cwd: "apps/welcome") { await e.server.end(s.id) }
        e.launch()
        e.ensureWorkspace()
        e.openLauncher("apps/welcome")
        let bash = e.launcherBox("Terminal")
        XCTAssertTrue(bash.waitForExistence(timeout: 30), "the launcher's Bash box")
        XCTAssertTrue(e.launcherBox("Agent").waitForExistence(timeout: 30), "the launcher's box for the fake agent")
        XCTAssertTrue(e.containing("Start a session in apps/welcome").exists, "the launcher's heading")
        e.shot("sessions-01-launcher")

        bash.tap()
        let shellTab = e.sessionTabs.matching(NSPredicate(format: "label BEGINSWITH %@", "Bash")).firstMatch
        XCTAssertTrue(shellTab.waitForExistence(timeout: 20), "a Bash tab")
        await e.eventually("a shell session on apps/welcome", timeout: 30) {
            try await e.server.sessions(cwd: "apps/welcome").contains { $0.kind != "agent" }
        }
        e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        e.app.typeText("printf '\\033]0;xbin-tab-%d\\007' $((40+2))\n")
        XCTAssertTrue(e.element("xbin-tab-42").waitForExistence(timeout: 30), "the shell's title in the bar")
        e.shot("sessions-02-shell-tab")

        let plus = e.app.buttons.matching(identifier: "sessions-plus").firstMatch
        XCTAssertTrue(plus.exists, "the strip's +")
        plus.tap()
        XCTAssertTrue(e.launcherBox("Agent").waitForExistence(timeout: 10), "+ shows the launcher again")
        e.launcherBox("Agent").tap()
        let agentTab = e.sessionTabs.matching(NSPredicate(format: "label CONTAINS %@", "agent")).firstMatch
        XCTAssertTrue(agentTab.waitForExistence(timeout: 30), "an agent tab beside the shell's")
        let composer = e.element("Message the agent", in: e.app.textViews)
        let field = e.element("Message the agent", in: e.app.textFields)
        XCTAssertTrue(e.first(of: [composer, field], timeout: 30) != nil, "the agent tab’s composer")
        XCTAssertEqual(e.sessionTabs.count, 2, "two tabs")
        e.shot("sessions-03-agent-tab")

        // Back to the shell: the same session, its title still there.
        shellTab.tap()
        XCTAssertTrue(e.element("xbin-tab-42").waitForExistence(timeout: 10), "the shell tab again, as it was")

        // The agent tab's long press: End session.
        agentTab.press(forDuration: 1.2)
        let end = e.app.buttons["End session"]
        XCTAssertTrue(end.waitForExistence(timeout: 10), "the tab's menu")
        e.shot("sessions-04-tab-menu")
        end.tap()
        await e.eventually("the agent session ended", timeout: 30) {
            try await !e.server.sessions(cwd: "apps/welcome").contains { $0.kind == "agent" }
        }
        for s in try await e.server.sessions(cwd: "apps/welcome") { await e.server.end(s.id) }
    }

    /// Tools → Live reload & deployments on apps/wide (a static tile: the
    /// e2e xbind runs without --isolate, which a backend tile's deployments
    /// need): the state, the deployments with their tags, then — dev
    /// assigned a branch the work tree isn't on, live reload paused by the
    /// server's hand — the offer "Keep dev on main this time", confirmed
    /// from the dry run's impact, reaches the server as the override.
    @MainActor
    func test02LiveReloadAndDeployments() async throws {
        let e = try E2E(self)
        let tile = "apps/wide"
        try await e.server.resetDeployments(tile)
        try await e.server.deploy("add", ["tile": tile, "deployment": "dev"])
        e.launch()
        e.ensureWorkspace()
        e.openLauncher(tile)
        XCTAssertTrue(e.launcherBox("Terminal").waitForExistence(timeout: 30), "the launcher")
        let tools = e.app.buttons.matching(identifier: "sessions-tools").firstMatch
        XCTAssertTrue(tools.waitForExistence(timeout: 10), "the tools menu")
        tools.tap()
        let item = e.app.buttons["Live reload & deployments"]
        XCTAssertTrue(item.waitForExistence(timeout: 10), "Live reload & deployments in the tools menu")
        item.tap()
        let header = e.app.descendants(matching: .any).matching(identifier: "live-reload-header").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 30), "live reload's sentence")
        XCTAssertTrue(e.until(10) { header.label.hasPrefix("Live reload: main (primary)") }, "live reload on main: \(header.label)")
        let dev = e.app.buttons.matching(identifier: "deployment:dev").firstMatch
        XCTAssertTrue(dev.waitForExistence(timeout: 10), "dev's row")
        XCTAssertTrue(e.app.buttons.matching(identifier: "deployment:main").firstMatch.exists, "main's row")
        e.shot("sessions-05-deployments")

        // The server moves: live reload onto dev, dev assigned a branch the
        // work tree isn't on, then paused — the screen follows the events.
        let state = try await e.server.deployments(tile)
        let features = state["features"] as? [String] ?? []
        guard features.contains("branches/1") else {
            print("xbin-e2e: this xbind has no branches/1: the branch offer is skipped")
            try await e.server.resetDeployments(tile)
            return
        }
        let wt = ((state["workTree"] as? [String: Any])?["branch"] as? String) ?? ""
        XCTAssertFalse(wt.isEmpty, "the work tree's branch (apps/wide is a git repository)")
        try await e.server.deploy("live-reload/attach", ["tile": tile, "deployment": "dev"])
        try await e.server.deploy("branch", ["tile": tile, "deployment": "dev", "branch": "feature/e2e"])
        try await e.server.deploy("live-reload/pause", ["tile": tile])
        let keep = e.app.buttons["Keep dev on \(wt) this time"]
        XCTAssertTrue(keep.waitForExistence(timeout: 30), "the offer to keep dev on \(wt) this time")
        XCTAssertTrue(e.app.buttons["Add a deployment for \(wt)…"].exists, "the offer to add a deployment for \(wt)")
        e.shot("sessions-06-branch-offer")
        keep.tap()
        let ok = e.app.alerts.buttons["Resume live reload"]
        XCTAssertTrue(ok.waitForExistence(timeout: 20), "the confirmation, from the dry run")
        XCTAssertTrue(e.containing("takes the work tree's \(wt) this time").exists, "its Branch line")
        e.shot("sessions-07-confirm")
        ok.tap()
        await e.eventually("live reload on dev, taking \(wt) this time", timeout: 30) {
            let s = try await e.server.deployments(tile)
            let d = (s["deployments"] as? [[String: Any]])?.first { $0["name"] as? String == "dev" }
            return s["liveReload"] as? String == "dev" && d?["branchOverride"] as? String == wt
        }
        dev.tap()
        let branchRow = e.app.descendants(matching: .any).matching(identifier: "branch-row").firstMatch
        XCTAssertTrue(branchRow.waitForExistence(timeout: 10), "dev's Branch row")
        XCTAssertTrue(e.until(10) { branchRow.label.contains("feature/e2e · takes \(wt) this time") }, "the Branch row: \(branchRow.label)")
        e.shot("sessions-08-branch-row")
        try await e.server.resetDeployments(tile)
    }
}

extension E2EServer {
    /// `GET /api/xbin/deployments?tile=` (docs/protocol.md §Tile deployments).
    func deployments(_ tile: String) async throws -> [String: Any] {
        let q = tile.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? tile
        return try JSONSerialization.jsonObject(with: await send("GET", "/api/xbin/deployments?tile=\(q)")) as? [String: Any] ?? [:]
    }

    /// `POST /api/xbin/deployments/<op>`.
    func deploy(_ op: String, _ body: [String: Any]) async throws {
        _ = try await send("POST", "/api/xbin/deployments/\(op)", json: body)
    }

    /// Back to the zero state: every deployment but main removed, live
    /// reload on main.
    func resetDeployments(_ tile: String) async throws {
        let s = try await deployments(tile)
        for d in s["deployments"] as? [[String: Any]] ?? [] {
            guard let name = d["name"] as? String, name != "main" else { continue }
            _ = try? await send("POST", "/api/xbin/deployments/remove", json: ["tile": tile, "deployment": name, "confirm": "erase"])
        }
        let now = try await deployments(tile)
        if now["record"] as? Bool == true, (now["liveReload"] as? String ?? "") != "main" {
            _ = try? await send("POST", "/api/xbin/deployments/live-reload/resume", json: ["tile": tile, "deployment": "main"])
        }
    }
}
