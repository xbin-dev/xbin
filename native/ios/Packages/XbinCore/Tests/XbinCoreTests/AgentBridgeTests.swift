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
        let m = try BridgeMessage(parsing: #"{"op":"call","id":"c1","what":"agent","args":{"msg":{"type":"xbin:agent-open-request","id":"0123456789abcdef01234567"}}}"#)
        guard case .call(let c) = m else { Issue.record("not a call"); return }
        #expect(c.what == BridgeCall.agentWhat)
        #expect(c.agentMessage == ["type": "xbin:agent-open-request", "id": "0123456789abcdef01234567"])
        #expect(AgentBridge.decide(c, tile: holder, holder: holder) == .openRequest("0123456789abcdef01234567"))
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

    /// Every message shape — ids, sessions, a new session's draft — is
    /// decided as the web chrome's own `pageMessage()` decides it: the
    /// vectors are that function's output over a corpus of messages
    /// (native/tools/delegation-words.mjs), not expectations written here.
    @Test func everyMessageAsTheWebDecidesIt() throws {
        let v = try Resources.json("delegation-words.json")
        let cases = try #require(v["bridge"]?.arrayValue)
        #expect(cases.count >= 40)
        for c in cases {
            let msg = c["msg"] ?? .null
            let want = c["out"] ?? .null
            let got = decide(msg)
            switch got {
            case .refuse:
                #expect(want.isNull, "the web takes \(msg.jsonString): \(want.jsonString)")
            case .openRequest(let id):
                #expect(want == ["type": "review", "kind": "request", "id": .string(id)], "\(msg.jsonString)")
            case .openChangeset(let id):
                #expect(want == ["type": "review", "kind": "changeset", "id": .string(id)], "\(msg.jsonString)")
            case .openSession(let session, let tile, let draft):
                if let draft {
                    #expect(want == ["type": "new-session", "tile": .string(tile), "draft": .string(draft)], "\(msg.jsonString)")
                } else {
                    #expect(want == ["type": "session", "tile": .string(tile), "session": .string(session)], "\(msg.jsonString)")
                }
            case .openView(let view):
                #expect(want == ["type": "open", "view": .string(view.rawValue)], "\(msg.jsonString)")
            }
        }
    }

    @Test func views() {
        #expect(decide(["type": "xbin:agent-open", "view": "approvals"]) == .openView(.approvals))
        #expect(decide(["type": "xbin:agent-open", "view": "activity"]) == .openView(.activity))
        #expect(decide(["type": "xbin:agent-open", "view": "needs"]) == .openView(.needs))
        // Preferences — where the agent's reach widens — never opens for a message
        #expect(refused(decide(["type": "xbin:agent-open", "view": "prefs"])))
        #expect(refused(decide(["type": "xbin:agent-open", "view": "setup"])))
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
        let open: JSONValue = ["type": "xbin:agent-open-request", "id": "0123456789abcdef01234567"]
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
        #expect(AgentBridge.decide(open, tile: holder, holder: holder) == .openRequest("0123456789abcdef01234567"))
    }
}
