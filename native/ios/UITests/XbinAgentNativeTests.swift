#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// The agent template's native view (D190) end to end: the e2e xbind's
/// apps/agent — a copy of the agent builtin template answering through
/// apps/llm-gw from hack/fakeopenai's scripted model (e2e-xbind.sh; it is
/// left out, and these tests skip, where xbind has no gocryptfs) — in the
/// app. The model's script is chosen by words of the last message: `hello`
/// → "Hello from the fake model.", `quick` → "Quick answer.", `delegate` →
/// a subagent "counter" answering "one, two, three" → "The helper
/// counted.", `long N` → N units, "done: N units".
///
/// - test01: the view opens on the conversation list; a conversation opens
///   pushed over it and Back returns to the list (an iPhone); on an iPad
///   the list stays beside it and the sidebar button hides and shows it.
/// - test02: a deep link `…/c/apps/agent#c=<id>` opens [list, chat] from a
///   cold start, and a second link reaches the running view.
/// - test03: New chat → a first message → the conversation takes the new
///   chat screen's place (Back goes to the list, not to New chat).
/// - test04: a subagent's card opens it pushed over its parent, Back
///   returns to the parent; the list's More → Automations → a schedule →
///   its run, pushed, and Back returns to the schedule.
/// - test05: the jump-to-latest pill of a long conversation when news
///   arrives while the reader is up the transcript.
/// - test09: a sheet's dismissal over a pushed conversation keeps it
///   (fails on v0.3.69: the conversation closes).
/// - test06Gallery: the screens above in light and dark,
///   `agent-<light|dark>-NN-<screen>.png`.
///
/// Every test starts from an empty agent: the e2e account's conversations
/// and schedules are deleted first.
final class XbinAgentNativeTests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        try XCTSkipIf(E2EServer.fromEnvironment() == nil,
                      "XBIN_E2E_URL / _USER / _PASSWORD unset: no xbind to test against (native/AGENTS.md → Mac mini)")
    }

    @MainActor
    func test01ListThenChatAndBack() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        let id = try await e.server.ask("hello", title: "E2E hello")
        try await e.server.waitForAnswer(id, "Hello from the fake model.")
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.openAgent()
        let row = e.agentRow("E2E hello")
        e.backToList(row)
        e.expect(row, 60, "the native view opens on the conversation list")
        XCTAssertTrue(e.app.buttons["New chat"].exists, "…with New chat in its bar")
        if !e.isPad { e.expectBarTitle("Agent", "the list's title in a phone's bar") }
        e.shot("agent-01-list")

        row.tap()
        let answer = e.containing("Hello from the fake model.")
        e.expect(answer, 30, "the conversation opens")
        e.shot("agent-02-chat")
        // a phone's bar keeps the conversation's title; an iPad names it once (the app's bar names the tile)
        if e.isPad { e.expectBarTitle("E2E hello", "the conversation's title, once", count: 1) } else { e.expectBarTitle("E2E hello", "the conversation's title in a phone's bar") }
        if e.isPad {
            XCTAssertTrue(e.until(10) { row.isHittable }, "an iPad keeps the list beside the conversation")
            e.toggleSidebar(hides: row)
            e.shot("agent-03-detail-only")
            e.toggleSidebar(shows: row)
        } else {
            XCTAssertFalse(row.isHittable, "a phone pushes the conversation over the list")
            e.tapNavBack(listShows: row, "Back returns to the list")
            XCTAssertFalse(answer.exists, "…and the conversation is gone")
            XCTAssertTrue(e.backButton.exists, "the tile's panel is still in front")
            e.shot("agent-03-list-again")
        }
    }

    @MainActor
    func test02DeepLinks() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        let first = try await e.server.ask("hello", title: "E2E linked")
        try await e.server.waitForAnswer(first, "Hello from the fake model.")
        let second = try await e.server.ask("quick", title: "E2E second link")
        try await e.server.waitForAnswer(second, "Quick answer.")
        e.launch()
        e.ensureWorkspace()
        e.goHome()

        // a cold start with the link (XCUIApplication.open relaunches)
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/agent#c=\(first)")!)
        let answer = e.containing("Hello from the fake model.")
        e.expect(answer, 60, "the linked conversation")
        e.shot("agent-04-deep-link")
        let row = e.agentRow("E2E linked")
        if e.isPad {
            XCTAssertTrue(e.until(10) { row.isHittable }, "an iPad shows the list beside it")
        } else {
            XCTAssertFalse(row.exists && row.isHittable, "a phone shows it over the list")
            e.tapNavBack(listShows: row, "Back from a linked conversation returns to the list")
        }

        // a link while the view runs: xbn.navigate → hashchange
        e.openInRunningApp("xbin://\(e.server.authority)/c/apps/agent#c=\(second)")
        let quick = e.containing("Quick answer.")
        e.expect(quick, 30, "the second link's conversation")
        e.shot("agent-05-link-running")
        if !e.isPad {
            e.tapNavBack(listShows: e.agentRow("E2E second link"), "…and Back returns to the list")
        }
    }

    @MainActor
    func test03NewChat() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.openAgent()
        let newChat = e.app.buttons.matching(NSPredicate(format: "label == %@", "New chat")).firstMatch
        e.backToList(newChat)
        e.expect(newChat, 60, "the list's New chat")
        newChat.tap()
        let hi = e.containing("What do you need?")
        e.expect(hi, 20, "the new chat screen")
        if !e.isPad { e.expectBarTitle("New chat", "the new chat screen's title in a phone's bar") }
        e.shot("agent-06-new-chat")
        let input = e.composer("ask anything…")
        XCTAssertTrue(input.exists, "the new chat's composer")
        input.tap()
        input.typeText("quick")
        e.tapAfterTyping(e.app.buttons["Send"])
        let answer = e.containing("Quick answer.")
        e.expect(answer, 60, "the first message started a conversation that answers")
        XCTAssertTrue(e.until(10) { !hi.exists }, "…in the new chat screen's place")
        let started = Date()
        let working = e.containing("Working")
        let settled = e.until(45) { !working.exists }
        print("xbin-e2e: the started conversation settled \(settled) after \(Date().timeIntervalSince(started)) s")
        XCTAssertTrue(settled, "the started conversation settles (its Working… goes)")
        let pill = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "jump to latest")).firstMatch
        XCTAssertFalse(e.until(2) { pill.exists }, "no jump-to-latest pill at the end of a conversation just started")
        e.shot("agent-07-new-chat-started")
        // (the agent titles it from the message: "Titled quick" from the fake model)
        var title = ""
        await e.eventually("the conversation on the server", timeout: 20) {
            title = try await e.server.conversations().compactMap(\.title).first { $0.contains("quick") } ?? ""
            return !title.isEmpty
        }
        if !e.isPad {
            let row = e.agentRow(title)
            e.tapNavBack(listShows: row, "Back from the started conversation goes to the list")
            XCTAssertFalse(hi.exists, "…not to New chat")
        }
    }

    @MainActor
    func test04SubagentAndAutomationRun() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        let parent = try await e.server.ask("delegate", title: "E2E delegate")
        try await e.server.waitForAnswer(parent, "The helper counted.")
        let schedule = try await e.server.schedule(name: "E2E digest", goal: "quick")
        try await e.server.trigger(schedule)
        await e.eventually("the schedule's run answered", timeout: 30) {
            try await e.server.automationRuns(schedule).count == 1
        }
        e.launch()
        e.ensureWorkspace()
        e.goHome()

        // the subagent, opened from its card, over its parent
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/agent#c=\(parent)")!)
        let counted = e.containing("The helper counted.")
        e.expect(counted, 60, "the parent conversation")
        let open = e.app.buttons["Open"]
        e.expect(open, 20, "the subagent's card opens it")
        e.shot("agent-08-subagent-card")
        open.tap()
        let child = e.containing("one, two, three")
        e.expect(child, 30, "the subagent's conversation")
        XCTAssertFalse(counted.exists && counted.isHittable, "…pushed over its parent")
        e.shot("agent-09-subagent")
        e.tapNavBack(listShows: counted, "Back from the subagent returns to its parent")

        // the list's More → Automations → the schedule → its run
        if !e.isPad { e.tapNavBack(listShows: e.agentRow("E2E delegate"), "Back to the list") }
        e.agentMenu("Automations")
        let item = e.agentRow("E2E digest")
        e.expect(item, 30, "the Automations screen lists the schedule")
        e.shot("agent-10-automations")
        item.tap()
        let run = e.agentRow("⏱ E2E digest")
        e.expect(run, 30, "the schedule's runs")
        run.tap()
        let answer = e.containing("Quick answer.")
        e.expect(answer, 30, "the run's conversation, pushed")
        e.shot("agent-11-automation-run")
        e.tapNavBack(listShows: run, "Back from the run returns to the schedule")
    }

    @MainActor
    func test05JumpToLatest() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        let id = try await e.server.ask("long 40", title: "E2E long")
        try await e.server.waitForAnswer(id, "done: 40 units", timeout: 120)
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/agent#c=\(id)")!)
        e.expect(e.containing("done: 40 units"), 60, "the long conversation's end")
        // up the transcript, then news from the server
        for _ in 0..<4 { e.drag(by: e.app.windows.firstMatch.frame.height * 0.5) }
        _ = e.until(1) { false }
        try await e.server.message(id, "quick")
        let pill = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "jump to latest")).firstMatch
        e.expect(pill, 30, "the jump-to-latest pill counts the news")
        e.shot("agent-12-jump-pill")
        pill.tap()
        let answer = e.containing("Quick answer.")
        XCTAssertTrue(answer.waitForExistence(timeout: 30) && e.until(10) { answer.isHittable }, "the newest rows after the jump")
        XCTAssertTrue(pill.waitForNonExistence(timeout: 10), "the pill goes at the bottom")
    }

    /// An iPad (or an open Duo): the list hides behind the bar's sidebar
    /// button — the conversation goes full width —, stays hidden when a link
    /// opens another conversation and across a relaunch (the view's saved
    /// state), and comes back with the same button.
    @MainActor
    func test07SidebarStaysHidden() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        let first = try await e.server.ask("hello", title: "E2E hello")
        try await e.server.waitForAnswer(first, "Hello from the fake model.")
        let second = try await e.server.ask("quick", title: "E2E second")
        try await e.server.waitForAnswer(second, "Quick answer.")
        e.launch()
        e.ensureWorkspace()
        try XCTSkipUnless(e.isPad, "the sidebar is an iPad's (a phone's list is the stack's root)")
        e.goHome()
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/agent#c=\(first)")!)
        e.expect(e.containing("Hello from the fake model."), 60, "the linked conversation")
        let row = e.agentRow("E2E second")
        XCTAssertTrue(e.until(10) { row.isHittable }, "the list beside it")
        e.toggleSidebar(hides: row)
        e.shot("agent-13-sidebar-hidden")

        e.openInRunningApp("xbin://\(e.server.authority)/c/apps/agent#c=\(second)")
        e.expect(e.containing("Quick answer."), 30, "a link opens another conversation")
        XCTAssertFalse(e.until(3) { row.isHittable }, "…with the list still hidden")

        e.app.terminate()
        e.launch()
        e.ensureWorkspace()
        e.goHome()
        e.openAgent()
        e.expect(e.containing("Quick answer."), 60, "a relaunch comes back to the conversation")
        XCTAssertFalse(e.until(3) { row.isHittable }, "…with the list still hidden")
        e.toggleSidebar(shows: row)
        e.shot("agent-14-sidebar-shown")
    }

    /// A sheet over a conversation pushed over the list (a phone) leaves the
    /// conversation where it was when it goes: More → Share, swiped away,
    /// and the conversation is still in front. KNOWN TO FAIL on v0.3.69
    /// (QA, 2026-10-08): the tree's sheets are presented from its root —
    /// the list, under the app stack's destination — and their dismissal
    /// sets that destination's isPresented to false ("split … hosted set
    /// false shown true" in the xbin-nav log), so the conversation closes.
    @MainActor
    func test09SheetKeepsThePushedScreen() async throws {
        let e = try E2E(self)
        try await e.server.prepareAgent()
        let id = try await e.server.ask("hello", title: "E2E sheet")
        try await e.server.waitForAnswer(id, "Hello from the fake model.")
        e.launch()
        e.ensureWorkspace()
        try XCTSkipIf(e.isPad, "an iPad shows the conversation beside the list, not pushed")
        e.goHome()
        e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/agent#c=\(id)")!)
        let answer = e.containing("Hello from the fake model.")
        e.expect(answer, 60, "the conversation")
        e.agentMenu("Share")
        let sheet = e.containing("Who can see it")
        e.expect(sheet, 15, "the share sheet")
        // swiped away by its grabber area, as a person does
        e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.12))
            .press(forDuration: 0.05, thenDragTo: e.app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.95)))
        XCTAssertTrue(sheet.waitForNonExistence(timeout: 10), "the sheet goes")
        _ = e.until(3) { false }
        e.shot("agent-16-after-sheet")
        XCTAssertTrue(answer.exists && answer.isHittable, "the conversation is still in front")
        XCTAssertFalse(e.agentRow("E2E sheet").isHittable, "…not the list")
    }

    /// The screens of the tests above, light and dark.
    @MainActor
    func test06Gallery() async throws {
        let server = try XCTUnwrap(E2EServer.fromEnvironment())
        try await server.prepareAgent()
        let hello = try await server.ask("hello", title: "E2E hello")
        try await server.waitForAnswer(hello, "Hello from the fake model.")
        let parent = try await server.ask("delegate", title: "E2E delegate")
        try await server.waitForAnswer(parent, "The helper counted.")
        let long = try await server.ask("long 40", title: "E2E long")
        try await server.waitForAnswer(long, "done: 40 units", timeout: 120)
        let schedule = try await server.schedule(name: "E2E digest", goal: "quick")
        try await server.trigger(schedule)
        for mode in ["light", "dark"] {
            var n = 0
            let e = try E2E(self)
            func shot(_ name: String) {
                n += 1
                e.shot(String(format: "agent-%@-%02d-%@", mode, n, name))
            }
            e.app.launchArguments += ["-XbinAppearance", mode]
            e.launch()
            e.ensureWorkspace()
            e.goHome()
            e.openAgent()
            let row = e.agentRow("E2E hello")
            e.backToList(row)
            e.expect(row, 60, "the list")
            shot(e.isPad ? "split-list" : "list")
            row.tap()
            e.expect(e.containing("Hello from the fake model."), 30, "a conversation")
            shot(e.isPad ? "split-chat" : "chat-pushed")
            if e.isPad {
                e.toggleSidebar(hides: row)
                shot("sidebar-hidden")
                e.toggleSidebar(shows: row)
            }
            let newChat = e.app.buttons.matching(NSPredicate(format: "label == %@", "New chat")).firstMatch
            if !e.isPad { e.tapNavBack(listShows: row, "back to the list") }
            e.expect(newChat, 10, "New chat")
            newChat.tap()
            e.expect(e.containing("What do you need?"), 20, "the new chat screen")
            shot("new-chat")

            e.app.open(URL(string: "xbin://\(e.server.authority)/c/apps/agent#c=\(parent)")!)
            e.expect(e.containing("The helper counted."), 60, "the linked conversation")
            shot("deep-link")
            let open = e.app.buttons["Open"]
            if open.waitForExistence(timeout: 20) {
                open.tap()
                _ = e.containing("one, two, three").waitForExistence(timeout: 30)
                shot("subagent")
            }

            e.openInRunningApp("xbin://\(e.server.authority)/c/apps/agent#auto=schedule:\(schedule)")
            if e.agentRow("⏱ E2E digest").waitForExistence(timeout: 30) { shot("automation") }

            e.openInRunningApp("xbin://\(e.server.authority)/c/apps/agent#c=\(long)")
            if e.containing("done: 40 units").waitForExistence(timeout: 30) {
                for _ in 0..<4 { e.drag(by: e.app.windows.firstMatch.frame.height * 0.5) }
                _ = e.until(1) { false }
                try await server.message(long, "quick")
                let pill = e.app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "jump to latest")).firstMatch
                if pill.waitForExistence(timeout: 30) { shot("jump-pill") }
                try await server.waitForAnswer(long, "Quick answer.")
            }
            e.app.terminate()
        }
    }
}

// MARK: - The agent's API, as the e2e account

extension E2EServer {
    struct Conversation: Decodable, Sendable {
        let id: Int
        let title: String?
    }

    struct Conversations: Decodable, Sendable {
        let items: [Conversation]
        let pinned: [Conversation]?
    }

    /// The account's conversations (GET /api/apps/agent/conversations), archived ones with `archived`.
    func conversations(archived: Bool = false) async throws -> [Conversation] {
        let d = try JSONDecoder().decode(Conversations.self, from: await send("GET", "/api/apps/agent/conversations?limit=200\(archived ? "&archived=1" : "")"))
        return d.items + (d.pinned ?? [])
    }

    /// Skips without apps/agent; else empties it for the account.
    func prepareAgent() async throws {
        guard try await components().contains("apps/agent") else {
            throw XCTSkip("the e2e xbind has no apps/agent (no gocryptfs there, or XBIN_E2E_AGENT=0: e2e-xbind.sh)")
        }
        try await resetAgent()
    }

    /// An empty agent for the account: its conversations (and their
    /// subagents) and its schedules deleted.
    func resetAgent() async throws {
        for c in try await conversations() + conversations(archived: true) {
            _ = try? await send("DELETE", "/api/apps/agent/runs/\(c.id)")
        }
        let list = try JSONSerialization.jsonObject(with: await send("GET", "/api/apps/agent/schedules"))
        let items = (list as? [[String: Any]]) ?? ((list as? [String: Any])?["items"] as? [[String: Any]]) ?? []
        for s in items {
            if let id = s["id"] as? Int { _ = try? await send("DELETE", "/api/apps/agent/schedules/\(id)") }
        }
    }

    /// A new conversation from its first message (POST /api/apps/agent/ask); its id.
    func ask(_ text: String, title: String) async throws -> Int {
        let o = try JSONSerialization.jsonObject(with: await send("POST", "/api/apps/agent/ask", json: ["text": text, "title": title])) as? [String: Any]
        return try XCTUnwrap(o?["id"] as? Int, "the new conversation's id")
    }

    /// Another message into a conversation (POST /api/apps/agent/runs/<id>/message).
    func message(_ id: Int, _ text: String) async throws {
        _ = try await send("POST", "/api/apps/agent/runs/\(id)/message", json: ["text": text])
    }

    /// Waits until the conversation has said `text` and its turn is over.
    func waitForAnswer(_ id: Int, _ text: String, timeout: TimeInterval = 60) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let o = try JSONSerialization.jsonObject(with: await send("GET", "/api/apps/agent/runs/\(id)")) as? [String: Any]
            let status = (o?["run"] as? [String: Any])?["status"] as? String ?? ""
            let said = (o?["messages"] as? [[String: Any]] ?? []).contains { ($0["content"] as? String ?? "").contains(text) }
            if said, status != "running" { return }
            try await Task.sleep(for: .milliseconds(500))
        }
        throw Failure(description: "conversation \(id) did not answer \"\(text)\" within \(Int(timeout)) s")
    }

    /// A schedule of the account's (POST /api/apps/agent/schedules); its id.
    func schedule(name: String, goal: String) async throws -> Int {
        let o = try JSONSerialization.jsonObject(with: await send("POST", "/api/apps/agent/schedules",
                                                                  json: ["name": name, "cron": "0 3 * * *", "goal": goal])) as? [String: Any]
        return try XCTUnwrap(o?["id"] as? Int, "the new schedule's id")
    }

    /// Runs a schedule now (POST /api/apps/agent/schedules/<id>/trigger).
    func trigger(_ id: Int) async throws {
        _ = try await send("POST", "/api/apps/agent/schedules/\(id)/trigger")
    }

    /// A schedule's runs that answered.
    func automationRuns(_ id: Int) async throws -> [Conversation] {
        let d = try JSONSerialization.jsonObject(with: await send("GET", "/api/apps/agent/automations/schedule/\(id)/runs")) as? [String: Any]
        let items = d?["items"] as? [[String: Any]] ?? []
        return items.filter { ($0["status"] as? String) == "idle" }.compactMap { r in
            (r["id"] as? Int).map { Conversation(id: $0, title: r["title"] as? String) }
        }
    }
}

// MARK: - Driving the agent's native view

extension E2E {
    /// Asserts `element` appears within `timeout`; on a failure the screen
    /// is kept as `fail-<n>.png` (and the tree goes to the log).
    func expect(_ element: XCUIElement, _ timeout: TimeInterval, _ what: String, file: StaticString = #filePath, line: UInt = #line) {
        if element.waitForExistence(timeout: timeout) { return }
        shot("fail-\(line)")
        print("xbin-e2e: \(what) — not there:\n\(app.debugDescription)")
        XCTFail(what, file: file, line: line)
    }

    /// `title` is in the navigation bars (`count` times, when given): a
    /// crowded bar drops its title.
    func expectBarTitle(_ title: String, _ what: String, count: Int? = nil, file: StaticString = #filePath, line: UInt = #line) {
        let q = app.navigationBars.staticTexts.matching(NSPredicate(format: "label == %@", title))
        let ok = until(10) { count.map { q.count == $0 } ?? (q.count > 0) }
        if !ok { print("xbin-e2e: bar titles \(q.count) × \(title):\n\(app.navigationBars.debugDescription)") }
        XCTAssertTrue(ok, "\(what) (\(q.count) in the bars)", file: file, line: line)
    }

    /// A tablet: the agent's split shows two columns.
    var isPad: Bool {
        #if canImport(UIKit)
        return UIDevice.current.userInterfaceIdiom == .pad // (the window's frame can read 0 right after a launch)
        #else
        return app.windows.firstMatch.frame.width >= 700
        #endif
    }

    /// Opens apps/agent's native view (a link: a cold start on its list).
    func openAgent() {
        app.open(URL(string: "xbin://\(server.authority)/c/apps/agent")!)
    }

    /// A row of the agent's lists (a conversation, an automation, a run):
    /// one button whose label combines its texts.
    func agentRow(_ title: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", title)).firstMatch
    }

    /// Back to the conversation list (a relaunch restores the stack it
    /// left), until `row` shows.
    func backToList(_ row: XCUIElement) {
        for _ in 0..<5 {
            if row.waitForExistence(timeout: 10), row.isHittable { return }
            let back = app.navigationBars.buttons.matching(identifier: "BackButton").firstMatch
            guard back.exists, back.isHittable else { return }
            back.tap()
        }
    }

    /// The composer by its placeholder (a text view, or a field).
    func composer(_ placeholder: String) -> XCUIElement {
        let view = element(placeholder, in: app.textViews)
        let field = element(placeholder, in: app.textFields)
        return first(of: [view, field], timeout: 20) == 1 ? field : view
    }

    /// An item of the conversation list's More menu. The tile's own panel
    /// has a More too: the one whose menu holds `item` is the list's.
    func agentMenu(_ item: String, file: StaticString = #filePath, line: UInt = #line) {
        let entry = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", item)).firstMatch
        let mores = app.buttons.matching(NSPredicate(format: "label == %@", "More"))
        for i in 0..<mores.count {
            let more = mores.element(boundBy: i)
            guard more.exists, more.isHittable else { continue }
            more.tap()
            if entry.waitForExistence(timeout: 3) {
                entry.tap()
                return
            }
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.04)).tap() // close that menu
        }
        XCTFail("a More menu with \(item)", file: file, line: line)
    }

    /// The split's sidebar button (an iPad) hides `list`, or shows it.
    func toggleSidebar(hides hidden: XCUIElement? = nil, shows shown: XCUIElement? = nil, file: StaticString = #filePath, line: UInt = #line) {
        let label = hidden != nil ? "Hide Sidebar" : "Show Sidebar"
        let button = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "ToggleSidebar", label)).firstMatch
        guard button.waitForExistence(timeout: 10) else {
            print("xbin-nav no sidebar button:\n\(app.debugDescription)")
            XCTFail("the split's sidebar button", file: file, line: line)
            return
        }
        button.tap()
        if let list = hidden {
            XCTAssertTrue(until(10) { !list.isHittable }, "the sidebar button hides the list", file: file, line: line)
        }
        if let list = shown {
            XCTAssertTrue(until(10) { list.isHittable }, "the sidebar button shows the list again", file: file, line: line)
        }
    }
}
