#if canImport(UIKit)
import UIKit
#endif
import XCTest

/// The web and MCP tools of the workspace agent (D236) in the app's chrome:
/// a web page to approve (its whole address, asked every time), an MCP
/// tool (once, or always this tool — armed like any standing choice), and
/// the person's two settings in Preferences, turned on only armed. Demo
/// `net` (App/Model/AgentDemo.swift): the workspace's policy has both on.
/// Screenshots: `wsagent2-<light|dark>-net-<what>.png`. Split from
/// XbinWorkspaceAgentStatesTests.swift for its size.
extension XbinWorkspaceAgentStatesTests {
    @MainActor
    func test09DemoWebAndMCP() throws {
        for mode in ["light", "dark"] {
            let e = try E2E(self)
            e.wsDemo("net")
            e.app.launchArguments += ["-XbinAppearance", mode, "-XbinAgentGate", "allow"] + (mode == "light" ? ["-XbinAgentArmSeconds", "8"] : [])
            e.launch()
            e.ensureWorkspace()
            e.goHome()

            // a web page: the address, why xbin asks, "has read the web"; no standing choice
            e.wsInbox("inbox-request:rq_web")
            e.expect(e.element("approval-url"), 30, "the page's address")
            XCTAssertTrue(e.containing("https://status.example.com/incidents?since=today").exists, "the whole address")
            XCTAssertTrue(e.element("approval-read-web").exists, "the conversation has read the web: said above")
            XCTAssertTrue(e.containing("holds data of Sales").exists, "why xbin asks")
            XCTAssertFalse(e.app.buttons.matching(identifier: "approval-choice-once").firstMatch.exists, "a page is asked every time: no standing choice")
            let allow = e.app.buttons.matching(identifier: "approval-allow").firstMatch
            XCTAssertEqual(allow.label, "Read this page")
            e.shot("wsagent2-\(mode)-net-web")
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !e.element("approval-heading").exists })

            // an MCP tool: the server, the tool, its mark, the arguments as sent; once or always this tool
            e.wsInbox("inbox-request:rq_mcp")
            e.expect(e.element("approval-tool"), 30, "the tool")
            XCTAssertTrue(e.containing("doesn’t mark it read-only").exists, "its server's mark, in words")
            let tool = e.app.buttons.matching(identifier: "approval-choice-tool").firstMatch
            e.wsScrollTo(tool)
            if mode == "light" {
                XCTAssertFalse(tool.isEnabled, "“Always, this tool” is not armed when the sheet appears")
                tool.tap()
                XCTAssertEqual(allow.label, "Allow once", "a tap on it unarmed chose nothing")
                XCTAssertTrue(e.until(20) { tool.isEnabled }, "it arms a moment later")
            }
            tool.tap()
            XCTAssertTrue(e.until(10) { allow.label == "Allow always, this tool" }, "armed, the choice is taken: \(allow.label)")
            e.shot("wsagent2-\(mode)-net-mcp")
            e.app.buttons.matching(identifier: "approval-later").firstMatch.tap()
            XCTAssertTrue(e.until(10) { !allow.exists })

            // Preferences: the web and MCP tools are the person's to turn on — armed, as they widen
            e.wsOpen()
            e.wsChromeMenu("Preferences")
            let web = e.app.switches.matching(identifier: "prefs-net-web").firstMatch
            e.expect(web, 30, "the web's setting")
            XCTAssertEqual(web.value as? String, "0", "off until the person turns it on")
            if mode == "light" {
                XCTAssertFalse(web.isEnabled, "turning the web on is not armed when the panel appears")
                XCTAssertTrue(e.until(20) { web.isEnabled }, "it arms a moment later")
            }
            e.wsScrollTo(web)
            // (the switch itself, at the row's trailing edge: a tap in the middle of a Form's toggle may land on its label)
            web.coordinate(withNormalizedOffset: CGVector(dx: 0.93, dy: 0.5)).tap()
            XCTAssertTrue(e.until(10) { web.value as? String == "1" }, "armed, it is turned on")
            XCTAssertTrue(e.app.switches.matching(identifier: "prefs-net-mcp").firstMatch.exists, "MCP tools beside it")
            e.shot("wsagent2-\(mode)-net-prefs")
            e.app.buttons["Done"].tap()
            e.app.terminate()
        }
    }
}
