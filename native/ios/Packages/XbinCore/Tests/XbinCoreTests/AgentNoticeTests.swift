import Foundation
import Testing
@testable import XbinCore

// The workspace agent's notifications (D241; AgentNotice.swift): the action
// matrix, what the extension draws, and the check an action runs. The
// security rules of plans/wsagent/ios-stage2.md §13 are named in the tests.

private let rid = "0123456789abcdef01234567"
private let sid = "89abcdef0123456789abcdef"

private func json(_ s: String) -> JSONValue { try! JSONValue(parsing: Data(s.utf8)) }

/// An `agent.approval` payload for a request of `kind` (offer and the call as given).
private func approval(kind: String = "call", offer: String = "once", method: String = "POST", target: String = "/api/apps/notes/note",
                      contentType: String = "application/json", body: String = #"{"text":"hi"}"#, bodySize: Int? = nil,
                      title: String = "Approval needed — Notes", text: String = "Your workspace agent wants to change something in Notes. Open to see exactly what.") -> PushPayload {
    var req: [String: JSONValue] = ["id": .string(rid), "kind": .string(kind), "tile": "apps/notes", "tileName": "Notes",
                                    "expires": 1_763_400_000, "offer": .string(offer)]
    if offer == "once" {
        req["method"] = .string(method)
        req["target"] = .string(target)
        req["contentType"] = .string(contentType)
        req["body"] = .string(body)
        req["bodySize"] = .int(Int64(bodySize ?? body.utf8.count))
        req["bodySha256"] = "c0ffee"
    }
    return PushPayload(ws: "wspush", kind: "agent.approval", title: title, body: text, link: "xbin/agent#r=\(rid)",
                       collapseID: "agent-approval:\(rid)", agent: .object(["v": 1, "badge": 2, "request": .object(req)]))
}

private func allowOn() -> AgentNotifyPrefs {
    var p = AgentNotifyPrefs()
    p.lockAllowOnce = true
    return p
}

private func category(_ p: PushPayload, _ prefs: AgentNotifyPrefs = allowOn(), device: AgentNoticeDevice = AgentNoticeDevice()) -> String {
    AgentNotice.content(p, appWorkspace: "app-ws", prefs: prefs, device: device)?.category ?? "(none)"
}

@Suite struct AgentNoticeTests {
    @Test func defaults() {
        let p = AgentNotifyPrefs()
        #expect(p.needsYou && p.finished && p.liveActivity && p.lockDeny)
        #expect(!p.lockAllowOnce, "Allow once is off until the person turns it on")
        // a stored record from before a field: the default for it
        let old = try! JSONDecoder().decode(AgentNotifyPrefs.self, from: Data(#"{"lockDeny":false}"#.utf8))
        #expect(!old.lockDeny && old.needsYou && !old.lockAllowOnce)
    }

    @Test func kinds() {
        let base = PushAPI.appKinds
        #expect(AgentNotifyPrefs().kinds(base: base) == base)
        var p = AgentNotifyPrefs()
        p.needsYou = false
        let k = p.kinds(base: base)
        #expect(!k.contains("agent") && !k.contains("agent.approval") && !k.contains("agent.ask") && !k.contains("agent.changeset"))
        #expect(k.contains("agent.done") && k.contains("agent.permission") && k.contains("tile") && k.contains("agent.clear"))
        p = AgentNotifyPrefs()
        p.finished = false
        #expect(!p.kinds(base: base).contains("agent.done") && p.kinds(base: base).contains("agent.approval"))
        p = AgentNotifyPrefs()
        p.liveActivity = false
        #expect(!p.kinds(base: base).contains("agent.wsactivity"))
        // what xbind does with them (PushAPI.receives is its rule)
        #expect(!PushAPI.receives(k, "agent.approval") && PushAPI.receives(k, "agent.permission"))
    }

    /// §3.5, row by row.
    @Test func actionMatrix() {
        var deny = AgentNotifyPrefs()
        deny.lockAllowOnce = false
        var allowOnly = allowOn()
        allowOnly.lockDeny = false
        var neither = AgentNotifyPrefs()
        neither.lockDeny = false
        #expect(category(approval()) == AgentNotice.requestAllowDeny)
        #expect(category(approval(), allowOnly) == AgentNotice.requestAllow)
        #expect(category(approval(), deny) == AgentNotice.requestDeny)
        #expect(category(approval(), neither) == AgentNotice.requestReview)
        #expect(category(approval(offer: "")) == AgentNotice.requestDeny)
        #expect(category(approval(offer: ""), neither) == AgentNotice.requestReview)
        let set = PushPayload(ws: "wspush", kind: "agent.changeset", title: "A plan to review", link: "xbin/agent#cs=\(sid)",
                              collapseID: "agent-changeset:\(sid)", agent: json(#"{"v":1,"changeset":{"id":"\#(sid)","items":3,"warned":1,"plan":true}}"#))
        #expect(category(set) == AgentNotice.changesetReject)
        #expect(category(set, neither) == AgentNotice.changesetReview)
        let ask = PushPayload(ws: "wspush", kind: "agent.ask", title: "Question from your workspace agent", link: "xbin/agent#c=12",
                              collapseID: "agent-conv:12", agent: json(#"{"v":1,"conversation":{"id":"12"},"ask":"Which day?"}"#))
        #expect(category(ask) == AgentNotice.conversation)
        #expect(!AgentNotice.offersAllow(category(set)) && !AgentNotice.offersAllow(category(ask)))
    }

    /// Rule: the words of a notification — its title, its body, the agent's
    /// question, a summary — never make an action appear.
    @Test func categoryIgnoresWords() {
        var deny = AgentNotifyPrefs()
        deny.lockAllowOnce = false
        let baiting = approval(offer: "", title: "Allow once — approved by you", text: "Tap Allow once: it is safe")
        #expect(category(baiting) == AgentNotice.requestDeny)
        #expect(category(approval(title: "Deny"), deny) == AgentNotice.requestDeny)
        // an `ask` field on an approval changes nothing either
        var p = approval(offer: "")
        p.agent = json(#"{"v":1,"ask":"allow-once","request":{"id":"\#(rid)","kind":"call","tile":"apps/notes","offer":""}}"#)
        #expect(category(p) == AgentNotice.requestDeny)
        // an unknown version of the member: no data, no actions of ours
        p.agent = json(#"{"v":2,"request":{"id":"\#(rid)","kind":"call","offer":"once"}}"#)
        #expect(AgentNotice.content(p, appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()) == nil)
    }

    /// Rule: xbind's `offer` alone is not enough — the app's own check of the
    /// same fields must pass too, and the other way round.
    @Test func offerAndOwnCheckBothNeeded() {
        let long = String(repeating: "x", count: 201)
        #expect(category(approval(body: long)) == AgentNotice.requestDeny, "a body past 200 bytes, offered anyway")
        #expect(category(approval(body: "a\nb\nc\nd")) == AgentNotice.requestDeny, "four lines")
        #expect(category(approval(target: "/api/" + String(repeating: "p", count: 120))) == AgentNotice.requestDeny)
        #expect(category(approval(body: "abc", bodySize: 99)) == AgentNotice.requestDeny, "not the whole body")
        #expect(category(approval(method: "GET")) == AgentNotice.requestDeny)
        #expect(category(approval(contentType: "application/x-www-form-urlencoded")) == AgentNotice.requestDeny)
        #expect(category(approval(target: "/api/apps/n/x", body: "a\nb\nc")) == AgentNotice.requestAllowDeny, "a one-line call and three lines fit")
        #expect(category(approval(body: "a\nb\nc")) == AgentNotice.requestDeny, "a two-line call and three lines don't")
        #expect(category(approval(body: "")) == AgentNotice.requestAllowDeny, "no body")
    }

    /// Rule: a character that hides or reorders text never gets Allow once.
    @Test func hiddenCharactersNeverAllow() {
        for s in ["pay \u{202E}lave", "a\u{200B}b", "x\u{2066}y", "\u{FEFF}z", "a\rb"] {
            #expect(category(approval(body: s)) == AgentNotice.requestDeny, "body \(s.unicodeScalars.map { $0.value })")
            #expect(category(approval(target: "/api/apps/notes/\(s)")) == AgentNotice.requestDeny)
        }
    }

    /// Rule: nothing wider than one call has a lock-screen Allow, whatever
    /// the payload claims.
    @Test func widerShapesReviewOnly() {
        for kind in ["session", "template", "web", "mcp", "unknown"] {
            #expect(!AgentNotice.offersAllow(category(approval(kind: kind))), "\(kind) offered Allow once")
        }
    }

    @Test func contentDrawsTheCall() throws {
        let c = try #require(AgentNotice.content(approval(), appWorkspace: "app-ws", prefs: allowOn(), device: AgentNoticeDevice()))
        #expect(c.title == "Approval needed", "the title is xbind's alone: no tile's name (review finding 5)")
        #expect(c.body == "POST /api/apps/notes/note\n" + #"{"text":"hi"}"#)
        #expect(c.threadID == "wspush/xbin/agent" && c.timeSensitive && c.badge == 2)
        #expect(c.userInfo["app"] == "app-ws" && c.userInfo["collapse"] == "agent-approval:\(rid)" && c.userInfo["link"] == "xbin/agent#r=\(rid)")
        let shown = try #require(c.userInfo["shown"].flatMap(AgentShownRequest.init(encoded:)))
        #expect(shown == AgentShownRequest(id: rid, kind: "call", tile: "apps/notes", method: "POST", target: "/api/apps/notes/note",
                                           contentType: "application/json", body: #"{"text":"hi"}"#, bodySha256: "c0ffee", whole: true))
        // not eligible: xbind's sentence, and nothing of the call kept as shown
        let r = try #require(AgentNotice.content(approval(body: String(repeating: "y", count: 300)), appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()))
        #expect(r.body.hasPrefix("Your workspace agent wants to change something in the tile “Notes”."))
        #expect(r.userInfo["shown"].flatMap(AgentShownRequest.init(encoded:))?.whole == false)
        #expect(r.userInfo["shown"].flatMap(AgentShownRequest.init(encoded:))?.body == "")
        // a tile name with a hidden character gives way to its path
        var p = approval(offer: "")
        p.agent = json(#"{"v":1,"request":{"id":"\#(rid)","kind":"session","tile":"apps/x","tileName":"In‮voices","offer":""}}"#)
        let s = try #require(AgentNotice.content(p, appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()))
        #expect(s.title == "Approval needed")
        #expect(s.body.hasPrefix("Your workspace agent wants to start a tile agent in the tile “x”."), "a name that hides text gives way to the path")
    }

    @Test func askIsQuotedNeverTheTitle() throws {
        let p = PushPayload(ws: "wspush", kind: "agent.ask", title: "Question from your workspace agent",
                            body: "Open the agent to answer, or reply here.", link: "xbin/agent#c=12", collapseID: "agent-conv:12",
                            agent: json(#"{"v":1,"conversation":{"id":"12"},"ask":"Pay the ‮REVO invoice?"}"#))
        let c = try #require(AgentNotice.content(p, appWorkspace: "a", prefs: AgentNotifyPrefs(), device: AgentNoticeDevice()))
        #expect(c.title == "Question from your workspace agent")
        #expect(c.body == "Your agent asks: “Pay the ⟨U+202E⟩REVO invoice?”")
        #expect(c.userInfo["conversation"] == "12" && c.category == AgentNotice.conversation && !c.timeSensitive)
    }

    @Test func otherKindsAreNotOurs() {
        for kind in ["agent.permission", "agent.question", "agent.turn", "agent.clear", "tile", "test"] {
            let p = PushPayload(ws: "w", kind: kind, title: "t", agent: json(#"{"v":1}"#))
            #expect(AgentNotice.content(p, appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()) == nil, "\(kind)")
        }
    }
}

/// A request as xbind answers `GET /delegate/requests/<id>`.
private func reading(_ patch: [String: JSONValue] = [:]) -> DelegateRequest {
    var o: [String: JSONValue] = ["id": .string(rid), "kind": "call", "method": "POST", "target": "/api/apps/notes/note", "tile": "apps/notes",
                                  "contentType": "application/json", "body": #"{"text":"hi"}"#, "bodyShown": true, "bodySize": 13,
                                  "bodySha256": "c0ffee", "state": "pending", "summary": "the agent's words", "preview": .object([:])]
    for (k, v) in patch { o[k] = v }
    return DelegateRequest(json: .object(o))!
}

private let shown = AgentShownRequest(id: rid, kind: "call", tile: "apps/notes", method: "POST", target: "/api/apps/notes/note",
                                      contentType: "application/json", body: #"{"text":"hi"}"#, bodySha256: "c0ffee", whole: true)

@Suite struct AgentNoticeDecisionTests {
    @Test func theShownRequestIsSent() {
        #expect(AgentNoticeDecision.verify(.allowOnce, shown: shown, current: reading(), reachable: true, prefs: allowOn()) == .send)
        #expect(AgentNoticeDecision.verify(.deny, shown: shown, current: reading(), reachable: true, prefs: AgentNotifyPrefs()) == .send)
    }

    /// Rule: an action decides only what was shown — any field that differs,
    /// and nothing is sent.
    @Test func anyChangedFieldSendsNothing() {
        let changes: [String: [String: JSONValue]] = [
            "method": ["method": "PUT"], "target": ["target": "/api/apps/notes/other"], "content type": ["contentType": "text/plain"],
            "body": ["body": #"{"text":"ho"}"#], "hash": ["bodySha256": "beef"], "size": ["bodySize": 99],
            "binary": ["bodyBinary": true], "tile": ["tile": "apps/other"], "kind": ["kind": "session"],
        ]
        for (name, patch) in changes {
            let v = AgentNoticeDecision.verify(.allowOnce, shown: shown, current: reading(patch), reachable: true, prefs: allowOn())
            #expect(v == .changed || name == "kind" && v == .changed, "\(name): \(v)")
        }
        // the tile or kind changed: not even a Deny
        #expect(AgentNoticeDecision.verify(.deny, shown: shown, current: reading(["tile": "apps/other"]), reachable: true, prefs: AgentNotifyPrefs()) == .changed)
    }

    @Test func goneAndUnreachable() {
        #expect(AgentNoticeDecision.verify(.allowOnce, shown: shown, current: nil, reachable: false, prefs: allowOn()) == .unreachable)
        #expect(AgentNoticeDecision.verify(.allowOnce, shown: shown, current: nil, reachable: true, prefs: allowOn()) == .gone(nil))
        let decided = reading(["state": "denied", "decidedVia": "session"])
        #expect(AgentNoticeDecision.verify(.allowOnce, shown: shown, current: decided, reachable: true, prefs: allowOn()) == .gone(decided))
        let other = reading(["id": .string(sid)])
        #expect(AgentNoticeDecision.verify(.deny, shown: shown, current: other, reachable: true, prefs: AgentNotifyPrefs()) == .gone(nil))
        let newer = reading(["state": "reconsidered"])
        if case .review = AgentNoticeDecision.verify(.allowOnce, shown: shown, current: newer, reachable: true, prefs: allowOn()) {} else {
            Issue.record("a state this app doesn't know was decided")
        }
    }

    /// Rule: the setting is read when the action runs — a notification
    /// delivered while Allow once was on decides nothing after it went off.
    @Test func settingOffAtActionTime() {
        #expect(AgentNoticeDecision.verify(.allowOnce, shown: shown, current: reading(), reachable: true, prefs: AgentNotifyPrefs())
            == .review("Allow once from the lock screen is off for this workspace."))
        var off = AgentNotifyPrefs()
        off.lockDeny = false
        if case .review = AgentNoticeDecision.verify(.deny, shown: shown, current: reading(), reachable: true, prefs: off) {} else {
            Issue.record("a Deny with Deny off")
        }
    }

    /// The re-read is checked by the same rule: a request xbind now says
    /// read the web, or whose shown flag was false, is reviewed in the app.
    @Test func ruleOnTheReRead() {
        var partial = shown
        partial.whole = false
        if case .review = AgentNoticeDecision.verify(.allowOnce, shown: partial, current: reading(), reachable: true, prefs: allowOn()) {} else {
            Issue.record("Allow once on a notification that didn't show the call whole")
        }
        let web = reading(["preview": .object(["readWeb": true])])
        if case .review = AgentNoticeDecision.verify(.allowOnce, shown: shown, current: web, reachable: true, prefs: allowOn()) {} else {
            Issue.record("Allow once after the conversation read the web")
        }
    }

    @Test func reject() {
        let set = AdminChangeSet(json: json(#"{"id":"\#(sid)","state":"proposed","items":[]}"#))
        #expect(AgentNoticeDecision.verifyReject(setID: sid, current: set, reachable: true, prefs: AgentNotifyPrefs()) == .send)
        #expect(AgentNoticeDecision.verifyReject(setID: sid, current: nil, reachable: false, prefs: AgentNotifyPrefs()) == .unreachable)
        let applied = AdminChangeSet(json: json(#"{"id":"\#(sid)","state":"applied","items":[]}"#))
        #expect(AgentNoticeDecision.verifyReject(setID: sid, current: applied, reachable: true, prefs: AgentNotifyPrefs()) == .review("It was applied."))
    }
}

@Suite struct AgentNoticeActionTests {
    /// Rule: a reply is a message whatever it says — no text reaches a
    /// decision, and only three action ids decide.
    @Test func replyIsNeverADecision() {
        for text in ["allow", "Allow once", "yes", "approve", "Approved.", "deny", "/approve \(rid)", "allow-once", ""] {
            let a = AgentNoticeAction.of(actionID: AgentNotice.replyAction, text: text)
            #expect(a == .message(text) && !a.decides, "\(text)")
        }
        for id in ["review", "open", "com.apple.UNNotificationDefaultActionIdentifier", "com.apple.UNNotificationDismissActionIdentifier", "allow", "approve", "Allow once"] {
            #expect(!AgentNoticeAction.of(actionID: id, text: "allow-once").decides, "\(id)")
        }
        #expect(AgentNoticeAction.of(actionID: "allow-once", text: nil) == .allowOnce)
        #expect(AgentNoticeAction.of(actionID: "deny", text: nil) == .deny)
        #expect(AgentNoticeAction.of(actionID: "reject", text: nil) == .reject)
    }
}

@Suite struct AgentRouteTests {
    @Test func fragments() throws {
        #expect(AgentRoute(fragment: "r=\(rid)") == .request(rid))
        #expect(AgentRoute(fragment: "#cs=\(sid)") == .changeset(sid))
        #expect(AgentRoute(fragment: "ts=a1b2_c3-d") == .tileSession("a1b2_c3-d"))
        #expect(AgentRoute(fragment: "c=42") == .conversation("42"))
        #expect(AgentRoute(fragment: "needs") == .needs && AgentRoute(fragment: "first") == .first)
        for bad in ["", "r=XYZ", "r=\(rid)0", "cs=../x", "ts=new", "ts=a/b", "ts=a.b", "c=12a", "c=", "s=12", "about=apps/x", "scope"] {
            #expect(AgentRoute(fragment: bad) == nil, "\(bad)")
        }
        for r in [AgentRoute.request(rid), .changeset(sid), .tileSession("abc"), .conversation("7"), .needs, .first] {
            #expect(AgentRoute(fragment: r.fragment) == r)
        }
    }

    @Test func links() throws {
        let l = try DeepLink(string: "xbin://ws.example:8443/xbin/agent#r=\(rid)")
        #expect(l == .workspaceAgent(workspace: "ws.example:8443", route: .request(rid)))
        #expect(try DeepLink(string: l.string) == l)
        // what an app before D241 made of it, and still makes of a fragment that isn't a route
        #expect(try DeepLink(string: "xbin://ws/xbin/agent") == .page(workspace: "ws", page: .agent))
        #expect(try DeepLink(string: "xbin://ws/xbin/agent#s=12") == .page(workspace: "ws", page: .agent))
        #expect(l.workspace == "ws.example:8443")
        // a push's link
        let p = PushPayload(ws: "w", kind: "agent.approval", title: "t", link: "xbin/agent#cs=\(sid)")
        #expect(p.deepLink(appWorkspace: "app") == .workspaceAgent(workspace: "app", route: .changeset(sid)))
        #expect(AgentRoute.of(pushLink: "xbin/agent#first") == .first)
        #expect(AgentRoute.of(pushLink: "c/apps/notes/#r=\(rid)") == nil, "only xbind's page link is a route")
        #expect(HandoffLink.link(fromUserInfo: HandoffLink.userInfo(l)) == l)
    }
}

@Suite struct AgentNoticeWordsTests {
    @Test func decidedWhere() {
        let mine = "dev-phone"
        func w(_ patch: [String: JSONValue]) -> String {
            AgentNoticeWords.decidedWhere(reading(patch), thisDevice: mine, thisDeviceName: "this iPhone", deviceNames: ["dev-pad": "Ana's iPad"])
        }
        #expect(w(["state": "approved", "decidedVia": "session"]) == "on the web")
        #expect(w(["state": "denied", "decidedVia": "device", "decidedDevice": "dev-phone", "decidedFrom": "notification"]) == "from a notification on this iPhone")
        #expect(w(["state": "approved", "decidedVia": "device", "decidedDevice": "dev-pad"]) == "on Ana's iPad")
        #expect(w(["state": "used", "decidedVia": "app"]) == "on another of your devices")
        #expect(w(["state": "expired"]) == "" && w(["state": "withdrawn"]) == "")
        #expect(w(["state": "approved"]) == "", "an xbind that doesn't say")
        let n = AgentNoticeWords.gone(reading(["state": "approved", "grant": "once", "decidedVia": "session"]), where: "on the web")
        #expect(n.title == "Already decided" && n.body.hasPrefix("Allowed once by you") && n.body.hasSuffix(" — on the web"))
        #expect(AgentNoticeWords.gone(nil, where: "").body == AgentNoticeWords.gone404)
        #expect(AgentNoticeWords.unreachable("Northwind").title == "Couldn't reach Northwind — nothing was decided")
        #expect(AgentNoticeWords.unreachable("Northwind").retry)
    }

    @Test func sweep() {
        let d: [AgentNoticeSweep.Delivered] = [
            .init(identifier: "1", app: "a", kind: "agent.approval", collapse: "agent-approval:\(rid)"),
            .init(identifier: "2", app: "a", kind: "agent.changeset", collapse: "agent-changeset:\(sid)"),
            .init(identifier: "3", app: "b", kind: "agent.approval", collapse: "agent-approval:\(rid)"),
            .init(identifier: "4", app: "a", kind: "agent.ask", collapse: "agent-conv:12"),
            .init(identifier: "5", app: "a", kind: "agent.approval", collapse: "agent-approval"),
            .init(identifier: "6", app: "a", kind: "tile", collapse: ""),
        ]
        #expect(AgentNoticeSweep.cleared(d, app: "a", clear: ["agent-approval:\(rid)", "agent-conv:12"]) == ["1", "4"])
        #expect(AgentNoticeSweep.stale(d, app: "a", pendingRequests: [], openSets: [sid]) == ["1"])
        #expect(AgentNoticeSweep.stale(d, app: "a", pendingRequests: [], openSets: []) == ["1", "2", "5"])
        #expect(AgentNoticeSweep.stale(d, app: "a", pendingRequests: [rid], openSets: [sid]).isEmpty)
        #expect(AgentBadge.total(["a": 2, "b": 3, "c": -1]) == 5)
    }

    @Test func payloadMember() throws {
        let d = try #require(AgentNoticeData(json: json(#"{"v":1,"badge":4,"clear":["agent-approval:x"],"request":{"id":"nothex","kind":"call"}}"#)))
        #expect(d.badge == 4 && d.clear == ["agent-approval:x"] && d.request == nil, "an id that isn't xbind's shape is no request")
        #expect(AgentNoticeData(json: json(#"{"badge":4}"#)) == nil)
        let plain = Data(#"{"v":1,"ws":"w","kind":"agent.approval","title":"t","body":"b","link":"xbin/agent#r=\#(rid)","collapseId":"c","agent":{"v":1,"badge":1}}"#.utf8)
        #expect(PushPayload(plaintext: plain)?.agent?["badge"]?.intValue == 1)
    }
}

@Suite struct AgentHandoffTests {
    /// Handoff of the agent's conversation, and a web URL handed to the app.
    @Test func agentPageAndWebLinks() throws {
        let o = try ServerOrigin(string: "https://ws.example.com")
        let p = HandoffLink.agentPage(origin: o)
        #expect(p.page?.absoluteString == "https://ws.example.com/xbin/agent")
        #expect(p.link == .page(workspace: "ws.example.com", page: .agent))
        let origins = [(id: "app-1", origin: o)]
        #expect(HandoffLink.agentLink(webURL: URL(string: "https://ws.example.com/xbin/agent")!, origins: origins) == .page(workspace: "app-1", page: .agent))
        #expect(HandoffLink.agentLink(webURL: URL(string: "https://ws.example.com/xbin/agent/#r=\(rid)")!, origins: origins)
            == .workspaceAgent(workspace: "app-1", route: .request(rid)))
        for other in ["https://other.example.com/xbin/agent", "https://ws.example.com/xbin/partitions", "https://ws.example.com/",
                      "http://ws.example.com/xbin/agent", "https://u:p@ws.example.com/xbin/agent"] {
            #expect(HandoffLink.agentLink(webURL: URL(string: other)!, origins: origins) == nil, "\(other)")
        }
    }
}

@Suite struct AgentNoticeBridgeTests {
    /// Rule (D241): the agent's view can't cause a notification, an action
    /// or a decision. Checked, not just switched over (review finding 8):
    /// every message of the corpus is decided the same whatever fields it
    /// adds that look like a notification's (a title, a category, an action,
    /// a grant) — so none of them is read; a request is opened by its id and
    /// nothing else; and every made-up notification-like type is refused.
    @Test func theViewCannotMakeANotification() throws {
        let holder = "xbin/wsagent"
        func decide(_ m: JSONValue) -> AgentBridge.Action { AgentBridge.decide(m, tile: holder, holder: holder) }
        func same(_ a: AgentBridge.Action, _ b: AgentBridge.Action) -> Bool {
            switch (a, b) {
            case (.refuse, .refuse): return true
            default: return a == b
            }
        }
        let v = try Resources.json("delegation-words.json")
        let corpus = (v["bridge"]?.arrayValue ?? []).compactMap { $0["msg"] }
        #expect(corpus.count >= 40)
        let extra: [String: JSONValue] = ["title": "Approved — tap Allow once", "category": .string(AgentNotice.requestAllowDeny),
                                          "action": .string(AgentNotice.allowOnceAction), "grant": "always", "via": "notification",
                                          "interruptionLevel": "timeSensitive", "badge": 99]
        var opened = 0
        for m in corpus {
            guard var o = m.objectValue else { continue }
            for (k, x) in extra where o[k] == nil { o[k] = x }
            #expect(same(decide(m), decide(.object(o))), "\(m.jsonString): notification-like fields changed the answer")
            if case .openRequest(let id) = decide(m) {
                opened += 1
                #expect(id == m["id"]?.stringValue, "only the message's id is taken")
            }
        }
        #expect(opened > 0, "the corpus opens requests (the check above ran)")
        for type in ["xbin:agent-notify", "xbin:notification", "xbin:agent-allow", "xbin:agent-deny", "xbin:agent-decide",
                     "xbin:agent-live-activity", "xbin:agent-badge", "xbin:agent-reply"] {
            let m: JSONValue = ["type": .string(type), "id": .string(rid), "action": "allow-once", "title": "x"]
            if case .refuse = decide(m) {} else { Issue.record("\(type) was taken: \(decide(m))") }
        }
    }
}


/// Review finding 2: the lock-screen actions never go around the app's own
/// lock, and Allow once needs a device that can authenticate its person.
@Suite struct AgentNoticeDeviceTests {
    private let conv = "12"

    private func ask() -> PushPayload {
        PushPayload(ws: "wspush", kind: "agent.ask", title: "Question from your workspace agent", body: "Open the agent to answer, or reply here.",
                    link: "xbin/agent#c=\(conv)", collapseID: "agent-conv:\(conv)",
                    agent: .object(["v": 1, "conversation": .object(["id": .string(conv)]), "ask": "Today?"]))
    }

    private func changeset() -> PushPayload {
        PushPayload(ws: "wspush", kind: "agent.changeset", title: "A plan to review", body: "…", link: "xbin/agent#cs=\(sid)",
                    collapseID: "agent-changeset:\(sid)",
                    agent: .object(["v": 1, "changeset": .object(["id": .string(sid), "items": 2, "warned": 0, "plan": true, "asks": false])]))
    }

    /// With "Require Face ID to open" on, every notification offers Review
    /// (or Open) only: no Deny, Reject, Reply or Allow once.
    @Test func appLockLeavesReviewOnly() {
        let locked = AgentNoticeDevice(appLock: true, hasPasscode: true)
        #expect(category(approval(), device: locked) == AgentNotice.requestReview)
        #expect(category(approval(offer: ""), device: locked) == AgentNotice.requestReview)
        #expect(category(changeset(), device: locked) == AgentNotice.changesetReview)
        #expect(category(ask(), device: locked) == AgentNotice.conversationOpen)
        #expect(category(ask()) == AgentNotice.conversation, "unlocked: Reply is there")
        #expect(category(changeset()) == AgentNotice.changesetReject)
    }

    /// Without a passcode, iOS's device authentication asks nothing: Allow
    /// once is never offered; Deny stays.
    @Test func noPasscodeNoAllowOnce() {
        let open = AgentNoticeDevice(appLock: false, hasPasscode: false)
        #expect(category(approval(), device: open) == AgentNotice.requestDeny)
        #expect(category(approval()) == AgentNotice.requestAllowDeny)
    }

    /// What the app honours when an action arrives — an old category may
    /// still deliver one: under the app lock no decision and no reply; Allow
    /// once only with a passcode and protected data available (a Watch or a
    /// locked phone has none).
    @Test func gateAtActionTime() {
        let fine = AgentNoticeDevice(appLock: false, hasPasscode: true, protectedData: true)
        for a in [AgentNoticeAction.allowOnce, .deny, .reject, .message("hi")] {
            #expect(AgentNoticeGate.refusal(a, device: fine) == nil)
            #expect(AgentNoticeGate.refusal(a, device: AgentNoticeDevice(appLock: true, hasPasscode: true, protectedData: true)) != nil, "\(a) under the app lock")
        }
        #expect(AgentNoticeGate.refusal(.open, device: AgentNoticeDevice(appLock: true, hasPasscode: true)) == nil, "opening the app is its own lock's business")
        #expect(AgentNoticeGate.refusal(.allowOnce, device: AgentNoticeDevice(appLock: false, hasPasscode: false, protectedData: true)) != nil)
        #expect(AgentNoticeGate.refusal(.allowOnce, device: AgentNoticeDevice(appLock: false, hasPasscode: true, protectedData: false)) != nil)
        #expect(AgentNoticeGate.refusal(.deny, device: AgentNoticeDevice(appLock: false, hasPasscode: false, protectedData: false)) == nil, "Deny stays")
        // a Retry is judged as what it retries
        #expect(AgentNoticeGate.refusal(.retry, tried: .allowOnce, device: AgentNoticeDevice(appLock: false, hasPasscode: false)) != nil)
        #expect(AgentNoticeGate.refusal(.retry, tried: .deny, device: AgentNoticeDevice(appLock: true, hasPasscode: true)) != nil)
    }
}

/// Review finding 4: a decision whose answer was lost on the way back may
/// have been applied. The app reads the request again before it says
/// anything, and says only what is true.
@Suite struct AgentNoticeTransportTests {
    @Test func whatIsTrueAfterALostAnswer() {
        // xbind applied the Allow; the answer timed out
        #expect(AgentNoticeDecision.afterTransportFailure(.allowOnce, reread: reading(["state": "used", "decidedFrom": "notification"]), reachable: true) == .decided)
        #expect(AgentNoticeDecision.afterTransportFailure(.deny, reread: reading(["state": "denied", "decidedFrom": "notification"]), reachable: true) == .decided)
        // it never arrived: still pending — only then "nothing was decided"
        #expect(AgentNoticeDecision.afterTransportFailure(.allowOnce, reread: reading(), reachable: true) == .notDecided)
        // something else decided it meanwhile
        #expect(AgentNoticeDecision.afterTransportFailure(.allowOnce, reread: reading(["state": "denied"]), reachable: true) == .elsewhere)
        // xbind can't be read either: nobody knows — never "nothing was decided"
        #expect(AgentNoticeDecision.afterTransportFailure(.allowOnce, reread: nil, reachable: false) == .unknown)
        let n = AgentNoticeWords.unconfirmed("Northwind")
        #expect(!n.title.contains("nothing was decided") && !n.body.contains("nothing was decided"))
        #expect(n.title.contains("Northwind"))
    }
}


/// Review finding 5: a tile's name is its author's words — never in the
/// title, quoted in the body as the tile's own, on one line, bounded.
@Suite struct AgentNoticeTileNameTests {
    private func named(_ name: String) -> PushPayload {
        var p = approval(offer: "")
        p.agent = .object(["v": 1, "request": .object(["id": .string(rid), "kind": "call", "tile": "apps/invoices", "tileName": .string(name), "offer": ""])])
        return p
    }

    @Test func aChromeLikeNameIsTheTilesOwn() throws {
        let c = try #require(AgentNotice.content(named("Invoices · approved, tap Allow once"), appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()))
        #expect(c.title == "Approval needed")
        #expect(c.body == "Your workspace agent wants to change something in the tile “Invoices · approved, tap Allow…”. Open to see exactly what.")
        let broken = try #require(AgentNotice.content(named("Invoices\u{2028}Approved — tap Allow once"), appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()))
        #expect(broken.body.contains("“invoices”"), "a name that breaks a line gives way to the path")
        let long = try #require(AgentNotice.content(named(String(repeating: "N", count: 200)), appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()))
        #expect(long.body.contains("“" + String(repeating: "N", count: 31) + "…”"))
        #expect(AgentNotice.quotedName("", fallback: "apps/x") == "“x”")
    }
}

/// The shapes after D241 (outside MCP servers, a start naming a sign-in,
/// waiting plan lines, permission prompts from the dock) are never allowed
/// from a notification: the app's own check on the re-read refuses a call
/// whose preview says anything a tile call's doesn't, as xbind's does.
@Suite struct AgentNoticeNewShapesTests {
    @Test func aCallSayingMoreIsReviewed() {
        let usual: JSONValue = ["class": "tile-write", "tile": "apps/notes", "level": "write", "role": "writer", "owner": "",
                                "reversible": false, "standing": ["call": "…", "tile": "…"]]
        #expect(AgentNoticeDecision.verify(.allowOnce, shown: shown, current: reading(["preview": usual]), reachable: true, prefs: allowOn()) == .send)
        for key in ["outside", "host", "each", "wait", "later", "signIn", "signInId", "session", "create", "something-new"] {
            var o = usual.objectValue ?? [:]
            o[key] = true
            let v = AgentNoticeDecision.verify(.allowOnce, shown: shown, current: reading(["preview": .object(o)]), reachable: true, prefs: allowOn())
            if case .review = v {} else { Issue.record("a re-read whose preview says \(key): \(v)") }
        }
        var mcp = usual.objectValue ?? [:]
        mcp["class"] = "mcp"
        if case .review = AgentNoticeDecision.verify(.allowOnce, shown: shown, current: reading(["preview": .object(mcp)]), reachable: true, prefs: allowOn()) {} else {
            Issue.record("a call previewed as an MCP tool")
        }
        // a tile agent's permission prompt: its own category, Open only — never an agent approval's actions
        let permission = PushPayload(ws: "w", kind: "agent.permission", title: "t", body: "b", link: "c/apps/note")
        #expect(permission.category == "xbin.agent.needs-you")
        #expect(AgentNotice.content(permission, appWorkspace: "a", prefs: allowOn(), device: AgentNoticeDevice()) == nil)
    }
}
