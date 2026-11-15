import Foundation
import Testing
@testable import XbinCore

/// The workspace agent's view asks the app's chrome by id (D233): the
/// bridge's `agent` call and the app's reading of it, held to the web
/// chrome's `pageMessage()` (web/agent-chrome-words.js).
@Suite struct AgentBridgeTests {
    let holder = "xbin/wsagent"

    private func decide(_ msg: JSONValue, tile: String? = nil) -> AgentBridge.Action {
        AgentBridge.decide(msg, tile: tile ?? holder, holder: holder)
    }

    private func refused(_ a: AgentBridge.Action) -> Bool {
        if case .refuse = a { return true }
        return false
    }

    // MARK: The wire

    @Test func theCallCarriesAMessage() throws {
        let m = try BridgeMessage(parsing: #"{"op":"call","id":"c1","what":"agent","args":{"msg":{"type":"xbin:agent-open-request","id":"req_1"}}}"#)
        guard case .call(let c) = m else { Issue.record("not a call"); return }
        #expect(c.what == BridgeCall.agentWhat)
        #expect(c.agentMessage == ["type": "xbin:agent-open-request", "id": "req_1"])
        #expect(AgentBridge.decide(c, tile: holder, holder: holder) == .openRequest("req_1"))
        // any other call, or an agent call without a message with a type, has none
        #expect(BridgeCall(id: 1, what: "copy", args: ["msg": ["type": "x"]]).agentMessage == nil)
        #expect(BridgeCall(id: 1, what: "agent", args: ["msg": "open"]).agentMessage == nil)
        #expect(BridgeCall(id: 1, what: "agent", args: ["msg": ["id": "a"]]).agentMessage == nil)
        #expect(BridgeCall(id: 1, what: "agent", args: [["msg": ["type": "x"]]]).agentMessage == nil)
        #expect(refused(AgentBridge.decide(BridgeCall(id: 1, what: "agent"), tile: holder, holder: holder)))
        // the app's ordinary calls know nothing of it: an app that never routes it refuses it
        #expect(refused2(NativeCallAction.decide(c, canOpenLinks: true)))
    }

    private func refused2(_ a: NativeCallAction) -> Bool {
        if case .refuse = a { return true }
        return false
    }

    @Test func theAppTellsTheView() {
        let ctx = AgentBridge.context(tile: "apps/orders", screen: nil)
        #expect(ctx == ["type": "xbin:agent-context", "tile": "apps/orders"])
        #expect(RuntimeCall.agent(ctx).javaScript == #"xbn.agent?.({"tile":"apps/orders","type":"xbin:agent-context"})"#)
        #expect(RuntimeCall.agent(ctx).functionBody.hasPrefix("return xbn.agent?.("))
        // a tile wins over a screen; slashes at the ends go; a path that isn't one is no tile
        #expect(AgentBridge.context(tile: "/apps/orders/", screen: "Sales desk") == ["type": "xbin:agent-context", "tile": "apps/orders"])
        #expect(AgentBridge.context(tile: "apps/../x", screen: "Sales desk") == ["type": "xbin:agent-context", "screen": "Sales desk"])
        #expect(AgentBridge.context(tile: nil, screen: "Sales desk") == ["type": "xbin:agent-context", "screen": "Sales desk"])
        // about nothing: the bare type (the view clears its chip)
        #expect(AgentBridge.context(tile: nil, screen: nil) == ["type": "xbin:agent-context"])
        #expect(AgentBridge.context(tile: "", screen: "") == ["type": "xbin:agent-context"])
        #expect(AgentBridge.context(tile: nil, screen: String(repeating: "s", count: 201)) == ["type": "xbin:agent-context"])
        // text a tile or a screen's name carries can't leave its argument
        let hostile = AgentBridge.context(tile: nil, screen: "\"});alert(1);({\"")
        #expect(RuntimeCall.agent(hostile).javaScript == #"xbn.agent?.({"screen":"\"});alert(1);({\"","type":"xbin:agent-context"})"#)
    }

    @Test func capsListTheFeatureOnlyWhenAsked() throws {
        let base = NativeCaps(renderer: "ios", app: "1.0", prims: ["screen": 2], features: ["chart.area"])
        #expect(!base.isAgent && !base.supports("agent"))
        #expect(base.json["features"] == ["chart.area"])
        let agent = base.withAgent()
        #expect(agent.isAgent && agent.supports("agent"))
        #expect(agent.json["features"] == ["agent", "chart.area"])
        #expect(agent.withAgent().features == ["chart.area", "agent"]) // not twice
        #expect(try NativeCaps(json: agent.json).isAgent)
        // beside the widget: both wire features
        #expect(agent.withWidget(size: .small).json["features"] == ["agent", "chart.area", "widget"])
        #expect(RuntimeScript.documentStart(caps: agent, state: nil).contains(#""features":["agent","chart.area"]"#))
    }

    // MARK: Every message

    @Test func requestsAndChangeSetsById() {
        #expect(decide(["type": "xbin:agent-open-request", "id": "r-1.a:b_C"]) == .openRequest("r-1.a:b_C"))
        #expect(decide(["type": "xbin:agent-open-changeset", "id": "cs42"]) == .openChangeset("cs42"))
        // no other field is read
        #expect(decide(["type": "xbin:agent-open-request", "id": "r1", "approve": true, "grant": "always"]) == .openRequest("r1"))
    }

    @Test func badIdsAreRefused() {
        for bad: JSONValue in ["", "a b", "a/b", "../x", "a\nb", "é", "a?b", .string(String(repeating: "a", count: 81)), 7, .null, ["r1"]] {
            #expect(refused(decide(["type": "xbin:agent-open-request", "id": bad])), "\(bad)")
            #expect(refused(decide(["type": "xbin:agent-open-changeset", "id": bad])), "\(bad)")
        }
        #expect(decide(["type": "xbin:agent-open-request", "id": .string(String(repeating: "a", count: 80))])
            == .openRequest(String(repeating: "a", count: 80)))
        #expect(refused(decide(["type": "xbin:agent-open-request"])))
    }

    @Test func sessions() {
        #expect(decide(["type": "xbin:open-session", "session": "s_1-A", "tile": "apps/orders"])
            == .openSession(session: "s_1-A", tile: "apps/orders", draft: nil))
        // a tile's path is cleaned like the web's tilePath
        #expect(decide(["type": "xbin:open-session", "session": "s1", "tile": "/apps/orders//"])
            == .openSession(session: "s1", tile: "apps/orders", draft: nil))
        // an existing session never carries a draft
        #expect(decide(["type": "xbin:open-session", "session": "s1", "tile": "apps/orders", "draft": "run rm -rf"])
            == .openSession(session: "s1", tile: "apps/orders", draft: nil))
        for badTile: JSONValue in ["", "/", "apps//orders", "apps/./orders", "apps/../admin", "..", 3, .null, .string(String(repeating: "a", count: 513))] {
            #expect(refused(decide(["type": "xbin:open-session", "session": "s1", "tile": badTile])), "\(badTile)")
        }
        for badSession: JSONValue in ["", "a.b", "a:b", "a b", "a/b", .string(String(repeating: "s", count: 65)), 1, .null] {
            #expect(refused(decide(["type": "xbin:open-session", "session": badSession, "tile": "apps/orders"])), "\(badSession)")
        }
        #expect(refused(decide(["type": "xbin:open-session", "tile": "apps/orders"])))
        #expect(refused(decide(["type": "xbin:open-session", "session": "s1"])))
    }

    @Test func aNewSessionsDraftIsPlainText() {
        func draft(_ d: JSONValue?) -> String? {
            var m: [String: JSONValue] = ["type": "xbin:open-session", "session": "new", "tile": "apps/orders"]
            if let d { m["draft"] = d }
            guard case .openSession("new", "apps/orders", let out) = decide(.object(m)) else { return nil }
            return out
        }
        #expect(draft("change line 40 in export.go") == "change line 40 in export.go")
        // line ends made \n; tabs and newlines stay
        #expect(draft("a\r\nb\rc\nd\te") == "a\nb\nc\nd\te")
        #expect(draft("a\r\n\r\nb\r\r\nc") == "a\n\nb\n\nc")
        // control characters and the invisible ones that reorder or hide text are dropped
        #expect(draft("a\u{0}b\u{7}c\u{1B}[31md\u{7F}e\u{9F}f") == "abc[31mdef")
        #expect(draft("safe\u{202E}evil\u{2066}x\u{2069}\u{200B}\u{200F}\u{FEFF}y") == "safeevilxy")
        #expect(draft("\u{0B}\u{0C}x") == "x")
        // trimmed
        #expect(draft("  \n\t words \u{A0}\n") == "words")
        // capped at 8000 characters (UTF-16 units), never half a pair
        #expect(draft(.string(String(repeating: "a", count: 9000)))?.utf16.count == 8000)
        let pairs = String(repeating: "a", count: 7999) + "😀😀"
        #expect(draft(.string(pairs)) == String(repeating: "a", count: 7999))
        // anything that isn't a string: no words
        #expect(draft(nil) == "")
        #expect(draft(42) == "")
        #expect(draft(["a"]) == "")
        #expect(draft("\u{200B}\u{200B}") == "")
    }

    @Test func views() {
        #expect(decide(["type": "xbin:agent-open", "view": "approvals"]) == .openView(.approvals))
        #expect(decide(["type": "xbin:agent-open", "view": "activity"]) == .openView(.activity))
        #expect(decide(["type": "xbin:agent-open", "view": "prefs"]) == .openView(.prefs))
        for bad: JSONValue in ["setup", "Approvals", "", 1, .null] {
            #expect(refused(decide(["type": "xbin:agent-open", "view": bad])), "\(bad)")
        }
        #expect(refused(decide(["type": "xbin:agent-open"])))
    }

    @Test func nothingElseIsUnderstood() {
        // nothing decides: no message approves, denies, resumes or changes a preference
        for type in ["xbin:agent-approve", "xbin:agent-deny", "xbin:agent-resume", "xbin:agent-context", "approve", ""] {
            #expect(refused(decide(["type": .string(type), "id": "r1"])), "\(type)")
        }
        #expect(refused(decide("xbin:agent-open-request")))
        #expect(refused(decide(["id": "r1"])))
        #expect(refused(decide(["type": 3, "id": "r1"])))
        #expect(refused(decide(.null)))
    }

    // MARK: Whose view

    @Test func onlyThePersonsAgentMayAsk() {
        let open: JSONValue = ["type": "xbin:agent-open-request", "id": "r1"]
        // a native tile that is not the person's agent
        #expect(refused(AgentBridge.decide(open, tile: "apps/orders", holder: holder)))
        #expect(refused(AgentBridge.decide(open, tile: "xbin/wsagent2", holder: holder)))
        #expect(refused(AgentBridge.decide(open, tile: "apps/xbin/wsagent", holder: holder)))
        #expect(refused(AgentBridge.decide(["type": "xbin:agent-open", "view": "prefs"], tile: "apps/orders", holder: holder)))
        #expect(refused(AgentBridge.decide(["type": "xbin:open-session", "session": "new", "tile": "apps/orders", "draft": "x"],
                                           tile: "apps/orders", holder: holder)))
        // a person with no agent (an older xbind, no access): nobody may
        #expect(refused(AgentBridge.decide(open, tile: holder, holder: nil)))
        #expect(refused(AgentBridge.decide(open, tile: "", holder: "")))
        // the holder itself
        #expect(AgentBridge.decide(open, tile: holder, holder: holder) == .openRequest("r1"))
    }
}
