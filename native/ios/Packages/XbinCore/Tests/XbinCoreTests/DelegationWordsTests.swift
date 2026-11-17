import Foundation
import Testing
@testable import XbinCore

/// DelegationWords is a port of the web chrome's words
/// (web/agent-chrome-words.js): the vectors are that module's own output
/// over a corpus (native/tools/delegation-words.mjs; `make js-test` fails
/// while they are stale), and the port must say exactly the same.
@Suite struct DelegationWordsTests {
    let v: JSONValue
    let now: Date
    let utc = TimeZone(identifier: "UTC")!

    init() throws {
        v = try Resources.json("delegation-words.json")
        now = try #require(DelegateTime.date(v["now"]))
    }

    private func s(_ j: JSONValue?, _ k: String) -> String { j?[k]?.stringValue ?? "" }

    @Test func requests() throws {
        let cases = try #require(v["requests"]?.arrayValue)
        #expect(cases.count >= 17)
        for c in cases {
            let r = try #require(DelegateRequest(json: c["json"]))
            let id = r.id
            let t = DelegationWords.tile(r)
            #expect(t.name == s(c["tile"], "name"), "\(id)")
            #expect(t.path == s(c["tile"], "path"), "\(id)")
            #expect(t.org == s(c["tile"], "org"), "\(id)")
            #expect(DelegationWords.levelWords(r.preview["level"]?.stringValue ?? "") == (c["level"]?.stringValue ?? ""), "\(id)")
            #expect(DelegationWords.needTitle(r) == c["needTitle"]?.stringValue, "\(id)")
            let h = DelegationWords.heading(r)
            #expect(h.before == s(c["heading"], "before") && h.strong == s(c["heading"], "strong") && h.after == s(c["heading"], "after"), "\(id)")
            let sends = DelegationWords.sends(r)
            #expect(sends.text == s(c["sends"], "text"), "\(id)")
            #expect(sends.note == s(c["sends"], "note"), "\(id)")
            #expect(sends.binary == c["sends"]?["binary"]?.boolValue, "\(id)")
            #expect(sends.clipped == (c["sends"]?["clipped"]?.boolValue ?? false), "\(id)")
            let all = DelegationWords.sends(r, all: true)
            #expect(all.text == s(c["sendsAll"], "text") && all.note == s(c["sendsAll"], "note") && !all.clipped, "\(id): Show all")
            #expect(DelegationWords.undoWords(r) == c["undo"]?.stringValue, "\(id)")
            let choices = DelegationWords.grantChoices(r)
            let want = c["choices"]?.arrayValue ?? []
            #expect(choices.count == want.count, "\(id)")
            for (a, b) in zip(choices, want) {
                #expect(a.id.rawValue == s(b, "id") && a.label == s(b, "label") && a.text == s(b, "text") && a.wider == b["wider"]?.boolValue, "\(id)")
            }
            let ids: [GrantChoice.ID] = [.once, .call, .tile]
            for (i, ch) in ids.enumerated() {
                #expect(DelegationWords.allowLabel(r, choice: ch) == c["allow"]?[i]?[0]?.stringValue, "\(id) \(ch)")
                #expect(DelegationWords.allowSpoken(r, choice: ch) == c["allow"]?[i]?[1]?.stringValue, "\(id) \(ch)")
            }
            let bodies: [JSONValue] = [
                DelegationWords.approveBody(r, choice: .once), DelegationWords.approveBody(r, choice: .call),
                DelegationWords.approveBody(r, choice: .tile), DelegationWords.approveBody(r, choice: .call, duration: "hour"),
                DelegationWords.approveBody(r, choice: .tile, duration: "always"),
            ]
            #expect(JSONValue.array(bodies) == c["bodies"], "\(id)")
            // an MCP tool's own choice (D239): always, this tool — and what every other kind makes of it
            #expect(DelegationWords.allowLabel(r, choice: .tool) == c["tool"]?[0]?.stringValue, "\(id) tool")
            #expect(DelegationWords.allowSpoken(r, choice: .tool) == c["tool"]?[1]?.stringValue, "\(id) tool")
            #expect(DelegationWords.approveBody(r, choice: .tool) == c["tool"]?[2], "\(id) tool")
            #expect(DelegationWords.approveBody(r, choice: .tool, duration: "hour") == c["tool"]?[3], "\(id) tool, an hour")
            if let want = c["net"], !want.isNull {
                if r.kind == .web {
                    let w = DelegationWords.webRows(r)
                    #expect(w.url == s(want, "url") && w.host == s(want, "host") && w.why == s(want, "why") && w.once == s(want, "once"), "\(id)")
                } else {
                    let m = DelegationWords.mcpRows(r)
                    #expect(m.call.server == s(want, "server") && m.call.path == s(want, "path") && m.call.tool == s(want, "tool") && m.call.title == s(want, "title"), "\(id)")
                    #expect(m.call.description == s(want, "description") && m.call.readOnly == want["readOnly"]?.boolValue && m.call.arguments == s(want, "arguments"), "\(id)")
                    #expect(m.call.standing == s(want, "standing") && m.marked == s(want, "marked") && m.undo == s(want, "undo"), "\(id)")
                    #expect(m.changedLine == s(want, "changedLine") && m.call.changed == (want["changed"]?.arrayValue ?? []).compactMap(\.stringValue), "\(id)")
                    // an outside server (D240): outside this workspace and its host; once only while each call asks
                    #expect(m.outsideLine == s(want, "outsideLine") && m.eachLine == s(want, "eachLine") && m.call.outside == (want["outside"]?.boolValue ?? false)
                        && m.call.host == s(want, "host") && m.call.each == (want["each"]?.boolValue ?? false) && m.call.trusted == (want["trusted"]?.boolValue ?? false), "\(id) outside")
                }
            }
            let w = DelegationWords.widerWords(r)
            #expect(w.heading == s(c["wider"], "heading") && w.means == s(c["wider"], "means") && w.ends == s(c["wider"], "ends")
                && w.back == s(c["wider"], "back") && w.allow == s(c["wider"], "allow"), "\(id)")
            if let want = c["session"], !want.isNull {
                let rows = DelegationWords.sessionRows(r)
                #expect(rows.agent == s(want, "agent"), "\(id)")
                #expect(rows.modeTag == s(want["mode"], "tag") && rows.modeText == s(want["mode"], "text") && rows.modeName == s(want["mode"], "name"), "\(id)")
                #expect(rows.modeAsks == (s(want["mode"], "tone") == "ok"), "\(id)")
                // only xbind's `asks: true` earns the green tag; anything else is the danger wording, Deny first
                #expect(rows.modeAsks == (r.session["asks"]?.boolValue == true), "\(id)")
                #expect(r.neverAsks == !rows.modeAsks && rows.danger.isEmpty == rows.modeAsks, "\(id)")
                #expect(rows.network == s(want, "network") && rows.others == s(want, "others"), "\(id)")
                #expect(rows.brief == s(want, "brief") && rows.danger == s(want, "danger") && rows.comes == s(want, "comes"), "\(id)")
                // the sign-in a start would use is said before it is allowed, with who will hold it (D230)
                // a saved sign-in is only offered — a line ticked on the web: the app says what the session runs on (the
                // web's words) and where to go; whose tile it is, in the web's words, where the tile's trust is asked too
                let whose = s(want, "trustWhose")
                #expect(rows.signInTrustWhose == whose, "\(id)")
                if want["signin"]?["offer"]?.boolValue == true {
                    #expect(rows.signIn == s(want, "runs") && rows.signInHolds.isEmpty, "\(id)")
                    #expect(rows.signInTrust.contains("on the web") && rows.signInTrust.contains("has no saved sign-in"), "\(id)")
                    #expect(rows.signInTrust.contains("trust line") == !whose.isEmpty, "\(id)")
                } else {
                    #expect(rows.signIn == s(want["signin"], "text") && rows.signInHolds.isEmpty && rows.signInTrust.isEmpty, "\(id)")
                }
            }
            if r.state == .unknown {
                // a newer xbind's state: the web says reload, the app — which can be older — the web
                #expect(c["settled"]?.stringValue == v["small"]?["unknown"]?["page"]?.stringValue, "\(id)")
                #expect(DelegationWords.settled(r, zone: utc) == v["small"]?["unknown"]?["app"]?.stringValue, "\(id)")
                #expect(!r.isPending)
            } else {
                #expect(DelegationWords.settled(r, zone: utc) == c["settled"]?.stringValue, "\(id)")
            }
            #expect(DelegationWords.asked(created: r.created, expires: r.expires, now: now, zone: utc) == c["asked"]?.stringValue, "\(id)")
        }
    }

    @Test func changeSets() throws {
        let cases = try #require(v["changesets"]?.arrayValue)
        for c in cases {
            let cs = try #require(AdminChangeSet(json: c["json"]))
            let ticks = DelegationWords.initialTicks(cs)
            #expect(ticks.map { JSONValue.int(Int64($0)) } == c["ticks"]?.arrayValue, "\(cs.id)")
            let review = DelegationWords.reviewWords(cs, ticked: ticks.count)
            #expect(review.heading == s(c["review"], "heading") && review.intro == s(c["review"], "intro")
                && review.footer == s(c["review"], "footer") && review.apply == s(c["review"], "apply"), "\(cs.id)")
            #expect(DelegationWords.reviewWords(cs, ticked: 0).apply == "Apply none")
            let need = DelegationWords.needChangeTitle(cs)
            #expect(need.title == s(c["need"], "title") && need.note == s(c["need"], "note"), "\(cs.id)")
            if cs.state == .unknown {
                #expect(s(c, "ended") == v["small"]?["unknown"]?["page"]?.stringValue && !cs.isProposed, "\(cs.id)")
                #expect(DelegationWords.endedWords(cs) == v["small"]?["unknown"]?["app"]?.stringValue, "\(cs.id)")
                #expect(DelegationWords.unknownState.title == v["small"]?["unknown"]?["title"]?.stringValue)
            } else {
                #expect(DelegationWords.endedWords(cs) == s(c, "ended"), "\(cs.id)")
            }
            let res = DelegationWords.resultsWords(cs)
            #expect(res.title == s(c["results"], "title"), "\(cs.id)")
            #expect(res.banner == s(c["results"], "banner"), "\(cs.id)")
            #expect(res.undo == s(c["results"], "undo"), "\(cs.id)")
            let wantItems = c["items"]?.arrayValue ?? []
            #expect(cs.items.count == wantItems.count, "\(cs.id): \(cs.items.count) items, the web read \(wantItems.count)")
            for (it, want) in zip(cs.items, wantItems) {
                #expect(DelegationWords.kindTag(it.kind) == s(want, "tag"), "\(cs.id) \(it.n)")
                #expect(DelegationWords.tickable(it) == want["tickable"]?.boolValue, "\(cs.id) \(it.n)")
                let wantWarnings = want["warnings"]?.arrayValue ?? []
                #expect(it.warnings.count == wantWarnings.count, "\(cs.id) \(it.n): warnings")
                for (w, ww) in zip(it.warnings, wantWarnings) {
                    let got = DelegationWords.warning(w)
                    #expect(got.tag == s(ww, "tag") && got.text == s(ww, "text"), "\(cs.id) \(it.n)")
                    #expect(got.icon == s(ww, "icon"), "\(cs.id) \(it.n)")
                }
                let r = DelegationWords.resultOf(it)
                #expect(r.word == s(want["result"], "word") && r.text == s(want["result"], "text"), "\(cs.id) \(it.n)")
            }
        }
        // before/after (the web's stateText): xbind's words when it says them, a string as it is, a value's
        // fields plainly — never a dump — and null absent
        let first = try #require(AdminChangeSet(json: cases[0]["json"]))
        #expect(first.items[0].before == "no access")
        #expect(first.items[1].before == nil)
        #expect(first.items[1].after == "host: \"export.example\"")
        let said = try #require(AdminChangeSet(json: try JSONValue(parsing: #"{"id":"x","items":[{"n":1,"kind":"binding.set","before":{"binding":[]},"beforeText":"not bound","after":{}}]}"#)))
        #expect(said.items[0].before == "not bound" && said.items[0].after == "(nothing)")
    }

    @Test func approvalsAndAudit() throws {
        for c in try #require(v["approvals"]?.arrayValue) {
            let a = try #require(StandingApproval(json: c["json"]))
            let row = DelegationWords.standingRow(a, now: now)
            #expect(row.tile == s(c, "tile") && row.text == s(c, "text") && row.wider == c["wider"]?.boolValue && row.until == s(c, "until"), "\(a.id)")
        }
        for c in try #require(v["audit"]?.arrayValue) {
            let d = DelegationWords.did(AuditRow(json: c["json"] ?? .null))
            #expect(d.text == s(c, "text"), "\(s(c["json"], "path"))")
            #expect(d.strong == s(c, "strong"), "\(s(c["json"], "path"))")
            #expect(d.detail == s(c, "detail"), "\(s(c["json"], "path"))")
            #expect(d.icon == s(c, "icon") && d.group == s(c, "group"), "\(s(c["json"], "path"))")
        }
        #expect(DelegationWords.standingNote == v["small"]?["standingNote"]?.stringValue)
        #expect(DelegationWords.readWeb == v["small"]?["readWeb"]?.stringValue)
        // the Preferences of the web and MCP tools (D239): what the person set, what the policy lets them
        let prefs = try #require(v["small"]?["netPrefs"]?.arrayValue)
        #expect(prefs.count >= 5)
        for (i, c) in prefs.enumerated() {
            let got = DelegationWords.netPrefs(DelegateState(json: c["state"]))
            let want = c["prefs"]?.arrayValue ?? []
            #expect(got.count == want.count, "netPrefs \(i)")
            for (a, b) in zip(got, want) {
                #expect(a.id == s(b, "id") && a.label == s(b, "label") && a.note == s(b, "note"), "netPrefs \(i) \(a.id)")
                #expect(a.on == b["on"]?.boolValue && a.allowed == b["allowed"]?.boolValue && a.why == s(b, "why") && a.sub == (b["sub"]?.boolValue ?? false), "netPrefs \(i) \(a.id)")
                #expect(a.servers == (b["servers"]?.arrayValue ?? []).map { OutsideServer(name: s($0, "name"), host: s($0, "host")) }, "netPrefs \(i) \(a.id) servers")
            }
        }
        #expect(DelegateRoute.setNet("web", true)?.method == "PUT" && DelegateRoute.setNet("paused", true) == nil, "only the three settings")
        #expect(DelegationWords.didNote == v["small"]?["didNote"]?.stringValue)
    }

    @Test func states() throws {
        for c in try #require(v["states"]?.arrayValue) {
            let name = s(c, "name")
            let state = DelegateState(json: c["state"])
            let dock = DelegationWords.dock(state: state, llmConfigured: c["llm"]?["configured"]?.boolValue, admin: c["admin"]?.boolValue ?? false)
            #expect(dock.rawValue == s(c, "dock"), "\(name)")
            #expect(DelegationWords.stopRefusal(state) == s(c, "stop"), "\(name)")
            let count = (state?.pendingRequests ?? 0) + (state?.openChangeSets ?? 0)
            let chip = DelegationWords.chip(dock, count: count)
            #expect(chip.shown == c["chip"]?["shown"]?.boolValue, "\(name)")
            #expect(chip.spoken == s(c["chip"], "spoken"), "\(name)")
            #expect(chip.paused == c["chip"]?["paused"]?.boolValue, "\(name)")
            if let want = c["words"], !want.isNull {
                let w = try #require(DelegationWords.stateWords(dock, state: state, zone: utc), "\(name)")
                #expect(w.title == s(want, "title"), "\(name)")
                #expect(w.text == s(want, "text"), "\(name)")
                #expect(w.more == s(want, "more"), "\(name)")
                let named = try #require(DelegationWords.stateWords(dock, state: state, admins: ["Mira Okafor", "Jonas Lind", "Kit"], zone: utc))
                #expect(named.more == s(c["named"], "more"), "\(name)")
            } else {
                #expect(DelegationWords.stateWords(dock, state: state, zone: utc) == nil, "\(name)")
            }
        }
        // which switch, who changes it and where: every off state has the app's line; what the person can act on has none
        let held: (String) -> DelegateState? = { DelegateState(json: ["workspace": true, "access": true, "agent": "xbin/wsagent", "held": .string($0)]) }
        #expect(DelegationWords.whoChanges(.off, state: nil).contains("workspace admin turns workspace agents on again"))
        #expect(DelegationWords.whoChanges(.unavailable, state: nil).contains("connects a language model"))
        #expect(DelegationWords.whoChanges(.held, state: held("locked")).contains("unlocks the workspace"))
        #expect(DelegationWords.whoChanges(.held, state: held("older")).contains("updates xbin"))
        #expect(DelegationWords.whoChanges(.held, state: held("unavailable")).contains("how xbin is run"))
        for d in [DelegationWords.Dock.paused, .page, .hidden, .setup] { #expect(DelegationWords.whoChanges(d, state: nil).isEmpty) }
        // xbind can't be reached: offline with a state, hidden without one ever read
        let st = DelegateState(json: ["workspace": true, "access": true, "agent": "xbin/wsagent"])
        #expect(DelegationWords.dock(state: st, llmConfigured: true, admin: false, unreachable: true) == .offline)
        // an old xbind (no /delegate/state): no button
        #expect(DelegationWords.dock(state: nil, llmConfigured: nil, admin: true) == .hidden)
        #expect(!DelegationWords.chip(.hidden, count: 3).shown)
    }

    @Test func smallWords() throws {
        let small = try #require(v["small"])
        for c in small["left"]?.arrayValue ?? [] {
            #expect(DelegationWords.left((c[0]?.doubleValue ?? 0) / 1000) == c[1]?.stringValue)
        }
        for c in small["bytes"]?.arrayValue ?? [] {
            #expect(DelegationWords.bytes(Int(c[0]?.intValue ?? 0)) == c[1]?.stringValue)
        }
        for c in small["admins"]?.arrayValue ?? [] {
            #expect(DelegationWords.admins((c[0]?.arrayValue ?? []).compactMap(\.stringValue)) == c[1]?.stringValue)
        }
        let pw = DelegationWords.stepUp("password"), si = DelegationWords.stepUp("signin")
        #expect(pw.heading == s(small["stepUp"]?[0], "heading") && pw.text == s(small["stepUp"]?[0], "text") && pw.action == s(small["stepUp"]?[0], "action") && pw.password)
        #expect(si.heading == s(small["stepUp"]?[1], "heading") && si.text == s(small["stepUp"]?[1], "text") && si.action == s(small["stepUp"]?[1], "action") && !si.password)
        #expect(AgentChoices.armSeconds * 1000 == small["arm"]?.doubleValue)
        // the agent's text in a sheet: the web's parts, escape by escape
        let reveals = small["reveal"]?.arrayValue ?? []
        #expect(reveals.count >= 12)
        for c in reveals {
            let text = s(c, "text")
            let got = DelegationWords.reveal(text)
            let want: [DelegationWords.RevealPart] = (c["parts"]?.arrayValue ?? []).map { p in
                p["hidden"]?.stringValue.map { .hidden($0) } ?? .text(p["text"]?.stringValue ?? "")
            }
            #expect(got.parts == want, "\(DelegationWords.revealed(text))")
            #expect(got.hidden == Int(c["hidden"]?.intValue ?? -1), "\(text.unicodeScalars.map(\.value))")
            #expect(DelegationWords.revealed(text) == s(c, "revealed"))
        }
        #expect(DelegationWords.hiddenNote == small["hiddenNote"]?.stringValue)
        // one clip length for both chromes, counted the same way
        #expect(DelegationWords.clip == Int(small["clip"]?.intValue ?? 0))
        for c in small["clips"]?.arrayValue ?? [] {
            let got = DelegationWords.clipText(s(c, "input"))
            #expect(got.text == s(c, "text"))
            #expect(got.clipped == c["clipped"]?.boolValue && got.shown == Int(c["shown"]?.intValue ?? -1) && got.total == Int(c["total"]?.intValue ?? -1))
            #expect(DelegationWords.clipNote(got) == s(c, "note"))
        }
        // in place of Allow / Apply while the agent is stopped: the web's words, refusal by refusal
        let stops = small["stops"]?.arrayValue ?? []
        #expect(stops.count == 12)
        for c in stops {
            let refusal = c[0]?.stringValue ?? "", sets = c[1]?.stringValue == "changeset"
            let got = DelegationWords.stopWords(refusal, changeset: sets)
            if c[2]?.isNull ?? true {
                #expect(got == nil, "\(refusal)")
            } else {
                #expect(got?.title == s(c[2], "title") && got?.text == s(c[2], "text") && got?.still == s(c[2], "still"), "\(refusal) \(sets)")
            }
        }
        // what the agent's view may ask the chrome to open: the web chrome's PAGE_VIEWS, to the letter — never Preferences
        #expect(AgentBridge.View.allCases.map(\.rawValue) == (small["pageViews"]?.arrayValue ?? []).compactMap(\.stringValue))
        for c in small["pageMessages"]?.arrayValue ?? [] {
            let view = c[0]?.stringValue ?? ""
            let got = AgentBridge.decide(["type": "xbin:agent-open", "view": .string(view)], tile: "xbin/wsagent", holder: "xbin/wsagent")
            if c[1]?.isNull ?? true {
                if case .refuse = got {} else { Issue.record("view \(view) was opened; the web chrome refuses it") }
            } else {
                #expect(got == AgentBridge.View(rawValue: view).map { .openView($0) }, "\(view)")
            }
        }
        #expect(AgentBridge.draftLabel == small["draftFrom"]?["label"]?.stringValue)
        #expect(AgentBridge.draftNote == small["draftFrom"]?["text"]?.stringValue)
        #expect(AgentChoices.duration(for: .call) == small["duration"]?["call"]?.stringValue)
        #expect(AgentChoices.duration(for: .tile) == small["duration"]?["tile"]?.stringValue)

        // Needs you: the same rows, oldest first
        let reqs = (v["requests"]?.arrayValue ?? []).compactMap { DelegateRequest(json: $0["json"]) }
        let sets = (v["changesets"]?.arrayValue ?? []).compactMap { AdminChangeSet(json: $0["json"]) }
        let rows = AgentNeed.rows(requests: reqs, changesets: sets, now: now, zone: utc)
        let want = small["needs"]?.arrayValue ?? []
        #expect(rows.count == want.count)
        for (a, b) in zip(rows, want) {
            #expect(a.kind.rawValue == s(b, "kind") && a.id == s(b, "id") && a.title == s(b, "title") && a.note == s(b, "note"))
        }
    }

    /// The cards the app draws for the agent's `request` and `session`
    /// nodes, and Home's status line: the web words' requestCard,
    /// changeSetCard, sessionCard and homeStatus over the corpus.
    @Test func cards() throws {
        func same(_ card: DelegationWords.RequestCard, _ w: JSONValue?, _ id: String) {
            #expect(card.head == s(w, "head") && card.what == s(w, "what") && card.line == s(w, "line"), "\(id): \(card)")
            #expect(card.glyph == s(w, "glyph") && card.tone == s(w, "tone") && card.waits == w?["waits"]?.boolValue && card.button == s(w, "button"), "\(id): \(card)")
        }
        let requests = try #require(v["requests"]?.arrayValue)
        for c in requests {
            let r = try #require(DelegateRequest(json: c["json"]))
            same(DelegationWords.requestCard(r, now: now, zone: utc), c["card"], r.id)
        }
        for c in try #require(v["changesets"]?.arrayValue) {
            let cs = try #require(AdminChangeSet(json: c["json"]))
            same(DelegationWords.changeSetCard(cs, now: now), c["card"], cs.id)
        }
        let rows = try #require(v["small"]?["sessionCards"]?.arrayValue)
        #expect(rows.count >= 10)
        for c in rows {
            let row = try #require(AgentSessionRow(json: c["row"] ?? .null))
            let got = DelegationWords.sessionCard(row, tileName: s(c, "tileName"), harnessName: s(c, "harnessName"))
            let w = c["card"]
            #expect(got.tile == s(w, "tile") && got.harness == s(w, "harness") && got.glyph == s(w, "glyph") && got.word == s(w, "word"), "\(row.id): \(got)")
            #expect(got.tone == s(w, "tone") && got.line == s(w, "line") && got.note == s(w, "note") && got.button == s(w, "button") && got.waits == w?["waits"]?.boolValue, "\(row.id): \(got)")
        }
        let homes = try #require(v["small"]?["homeStatus"]?.arrayValue)
        for c in homes {
            let dock = try #require(DelegationWords.Dock(rawValue: s(c, "dock")))
            #expect(DelegationWords.homeStatus(dock, tileAgents: Int(c["tileAgents"]?.intValue ?? 0), working: Int(c["working"]?.intValue ?? 0),
                                               count: Int(c["count"]?.intValue ?? 0)) == s(c, "out"), "\(s(c, "dock"))")
        }
    }
}

/// The routes, what an answer means, and the event.
@Suite struct DelegationRouteTests {
    @Test func routes() {
        #expect(DelegateRoute.request("rq 1/x").path == "/api/xbin/delegate/requests/rq%201%2Fx")
        // a dot is encoded too: no id, even one that slipped past DelegateID, is a dot segment to a URL parser
        #expect(DelegateRoute.request("..").path == "/api/xbin/delegate/requests/%2E%2E")
        #expect(DelegateRoute.approve(".", ["grant": "once"]).path == "/api/xbin/delegate/requests/%2E/approve")
        #expect(DelegateRoute.changeset("..").path == "/api/xbin/delegate/changesets/%2E%2E")
        #expect(DelegateRoute.apply("..", items: [1]).path == "/api/xbin/delegate/changesets/%2E%2E/approve")
        let a = DelegateRoute.approve("rq1", ["grant": "once"])
        #expect(a.method == "POST" && a.path == "/api/xbin/delegate/requests/rq1/approve")
        #expect(body(a) == ["grant": "once"])
        #expect(body(DelegateRoute.apply("cs1", items: [1, 3])) == ["items": [1, 3]])
        #expect(body(DelegateRoute.apply("cs1", items: [2], password: "pw")) == ["items": [2], "password": "pw"])
        #expect(body(DelegateRoute.setPaused(false)) == ["paused": false])
        #expect(DelegateRoute.agentSessions(ids: ["a_1", "b/../c", "ok-2"]).path == "/api/xbin/term/sessions?origin=agent&compact=1&ids=a_1,ok-2")
        #expect(DelegateRoute.sealedComponents.path.hasSuffix("?sealed=1"))
    }

    @Test func outcomes() {
        #expect(DelegateOutcome.read(json(200, ["request": ["id": "r"]])) == .done(["request": ["id": "r"]]))
        #expect(DelegateOutcome.read(json(409, ["error": "no longer pending"])) == .gone("no longer pending"))
        #expect(DelegateOutcome.read(json(404, ["error": "not found"])) == .gone("not found"))
        #expect(DelegateOutcome.read(json(403, ["error": "x", "stepUp": "password"])) == .stepUp("password", wrongPassword: false))
        #expect(DelegateOutcome.read(json(403, ["error": "x", "stepUp": "password"]), sentPassword: true) == .stepUp("password", wrongPassword: true))
        #expect(DelegateOutcome.read(json(403, ["error": "x", "stepUp": "signin"])) == .stepUp("signin", wrongPassword: false))
        #expect(DelegateOutcome.read(json(409, ["unseen": true, "bodySize": 12, "bodySha256": "ab"])) == .unseen(size: 12, sha256: "ab"))
        #expect(DelegateOutcome.read(json(403, ["error": "not yours"])) == .failed("not yours"))
        // the agent is stopped: why, never "it no longer waits"
        #expect(DelegateOutcome.read(json(409, ["error": "you paused your agent", "refusal": "paused"])) == .stopped(refusal: "paused", message: "you paused your agent"))
    }

    /// Which routes are grants — what a tile's credential must never carry
    /// the person's session to (ClientTileTests checks the scheme handler).
    @Test func grants() {
        for (m, p) in [("POST", "/api/xbin/delegate/requests/r1/approve"), ("POST", "/api/xbin/delegate/requests/r1/deny"),
                       ("POST", "/api/xbin/delegate/changesets/c1/approve?x=1"), ("POST", "/api/xbin/delegate/changesets/c1/reject"),
                       ("POST", "/api/xbin/delegate/changesets/c1/undo"), ("DELETE", "/api/xbin/delegate/approvals/a1"),
                       ("POST", "/api/xbin/delegate/approvals"), ("PUT", "/api/xbin/delegate/state"),
                       ("PUT", "/api/xbin/delegate/workspace"), ("GET", "/api/xbin/delegate/audit")] {
            #expect(DelegateRoute.isGrant(method: m, path: p), "\(m) \(p)")
        }
        for (m, p) in [("GET", "/api/xbin/delegate/state"), ("GET", "/api/xbin/delegate/requests/r1"), ("GET", "/api/xbin/whoami"),
                       ("POST", "/api/apps/x/approve")] {
            #expect(!DelegateRoute.isGrant(method: m, path: p), "\(m) \(p)")
        }
    }

    @Test func event() {
        #expect(AppEvent.parse(#"{"type":"delegate","data":{"op":"request","id":"rq1","user":"ana"}}"#) == .delegate(DelegateEvent(op: "request", id: "rq1")))
        #expect(AppEvent.parse(#"{"type":"delegate","data":{"op":"state","user":"ana"}}"#) == .delegate(DelegateEvent(op: "state")))
        #expect(AppEvent.parse(#"{"type":"delegate","data":{}}"#) == .other(type: "delegate"))
    }

    /// A state this app doesn't know (a newer xbind's) is never read as one
    /// that can be decided: no Review…, no Allow, no Apply, not in Needs
    /// you — and a line that sends the person to the web.
    @Test func unknownStatesAreNotActionable() throws {
        let r = try #require(DelegateRequest(json: ["id": "0123456789abcdef01234567", "kind": "call", "state": "deferred", "method": "PUT", "target": "/api/apps/x/y"]))
        #expect(r.state == .unknown && !r.isPending)
        let card = DelegationWords.requestCard(r)
        #expect(!card.waits && card.button.isEmpty && card.line == DelegationWords.unknownState.text)
        #expect(DelegationWords.settled(r) == "This app is older than the workspace — open it on the web.")
        let cs = try #require(AdminChangeSet(json: ["id": "89abcdef0123456789abcdef", "title": "t", "state": "scheduled", "items": [["n": 1, "kind": "grant.add", "line": "x"]]]))
        #expect(cs.state == .unknown && !cs.isProposed)
        let c = DelegationWords.changeSetCard(cs)
        #expect(!c.waits && c.button.isEmpty && c.head == DelegationWords.unknownState.title)
        #expect(DelegationWords.endedWords(cs) == DelegationWords.unknownState.text)
        #expect(AgentNeed.rows(requests: [r], changesets: [cs]).isEmpty, "nothing of it waits in Needs you")
        // a missing state is no state either
        #expect(DelegateRequest(json: ["id": "0123456789abcdef01234567"])?.state == .unknown)
    }

    @Test func stateReads() throws {
        let s = try #require(DelegateState(json: ["user": "ana", "workspace": true, "paused": true, "pausedAt": "2026-11-16T10:31:00.5Z",
                                                  "workspaceAt": "0001-01-01T00:00:00Z", "pendingRequests": 2, "openChangeSets": 1,
                                                  "access": true, "agent": "xbin/wsagent", "who": ["everyone": true, "entries": 0, "chosen": true]]))
        #expect(s.paused && s.pausedAt != nil && s.workspaceAt == nil && s.access == true && s.agent == "xbin/wsagent")
        #expect(s.who == DelegateState.Who(everyone: true, entries: 0, chosen: true))
        #expect(DelegateState(json: nil) == nil)
        #expect(DelegateState(json: "nope") == nil)
        // an xbind that ships no agent: access absent
        #expect(DelegateState(json: ["workspace": false])?.access == nil)
    }
}

/// A tile's page and a native tile's runtime reach xbind through the
/// workspace's scheme handler, which gives every request the TILE's frame
/// token and nothing of the person's: the grant routes included. (xbind
/// refuses a tile's credential on each of them — docs/protocol/delegation.md
/// — and the UI tests check that against a live xbind.)
@Suite struct DelegationCredentialTests {
    /// The property: whatever a tile's page or a native tile's runtime sends
    /// through the scheme handler — any path, any header, in any case —
    /// what reaches xbind carries the TILE's frame token and nothing the app
    /// adds of the person's: no cookie, no bearer, no header the page didn't
    /// send itself (but the frame token and the client header, which the app
    /// sets to its own values). The person's credential is never an input
    /// of `forwardHeaders`, and the handler's source never names it.
    @Test func aTileRuntimeNeverCarriesThePersonsSession() throws {
        let person = "the-persons-device-session-token"
        let names = ["Cookie", "cookie", "COOKIE", "Cookie2", "cOoKiE2", "Authorization", "authorization", "Proxy-Authorization",
                     TileScheme.frameTokenHeader, TileScheme.frameTokenHeader.lowercased(), TileScheme.clientHeader, TileScheme.clientHeader.uppercased(),
                     "X-XBin-User", "Content-Type", "Accept", "Host", "Connection"]
        var rng = SystemRandomNumberGenerator()
        for round in 0..<400 {
            var page: [String: String] = [:]
            for n in names where Bool.random(using: &rng) {
                // the page may set a header to anything — even, had it learned it, the person's own token
                page[n] = round % 3 == 0 ? "Bearer \(person)" : "page-\(round)-\(n)"
            }
            let out = TileScheme.forwardHeaders(page, frameToken: "the-tiles-own", client: "app/1")
            let lower = Dictionary(grouping: out.keys, by: { $0.lowercased() })
            #expect(lower["cookie"] == nil && lower["cookie2"] == nil, "a cookie went out: \(out)")
            #expect(lower[TileScheme.frameTokenHeader.lowercased()]?.count == 1 && out[TileScheme.frameTokenHeader] == "the-tiles-own", "\(out)")
            #expect(lower[TileScheme.clientHeader.lowercased()]?.count == 1 && out[TileScheme.clientHeader] == "app/1", "\(out)")
            // everything else is the page's own, unchanged: the handler adds nothing
            for (k, v) in out where k != TileScheme.frameTokenHeader && k != TileScheme.clientHeader {
                #expect(page[k] == v, "\(k) was added or changed: \(out)")
            }
            if !page.values.contains(where: { $0.contains(person) }) {
                #expect(!out.values.contains { $0.contains(person) }, "the person's token appeared from nowhere: \(out)")
            }
        }
        // the scheme handler — every tile page and native runtime's requests — has no way to the person's session:
        // it signs with the tile's frame token, through a cookie-less transport
        let app = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../../../App")
        let handler = try String(contentsOf: app.appendingPathComponent("Tiles/TileSchemeHandler.swift"), encoding: .utf8)
        for name in ["auth.", "Authorization", "Bearer", "session(", "Cookie", "cookie", "WebTicket", "bearer"] {
            #expect(!code(handler).contains(name), "TileSchemeHandler names \(name)")
        }
        #expect(handler.contains("TileScheme.forwardHeaders(pageHeaders, frameToken: token"))
        let transport = try String(contentsOf: app.appendingPathComponent("Model/AppTransport.swift"), encoding: .utf8)
        #expect(transport.contains("c.httpCookieStorage = nil") && transport.contains("c.httpShouldSetCookies = false"), "the app's URLSession keeps and sends no cookie")
    }

    /// A Swift file without its comments: what the code itself names.
    private func code(_ s: String) -> String {
        s.split(separator: "\n", omittingEmptySubsequences: false).map { line in
            line.range(of: "//").map { String(line[..<$0.lowerBound]) } ?? String(line)
        }.joined(separator: "\n")
    }

    /// The ids a tile names — a tree's `request` and `session` nodes, the
    /// view's messages — are the shapes xbind issues, or nothing: they go
    /// into URLs the app sends with the person's session.
    @Test func idsAreXbindsShapesAndNoPath() {
        #expect(DelegateID.isRequest("0123456789abcdef01234567"))
        for bad in ["", ".", "..", "../x", "rq1", "rq_call", "0123456789ABCDEF01234567", "0123456789abcdef0123456", "0123456789abcdef012345678",
                    "0123456789abcdef0123456.", "0123456789abcdef/1234567"] {
            #expect(!DelegateID.isRequest(bad), "\(bad)")
        }
        #expect(DelegateID.isSession("8f00b204e9800998") && DelegateID.isSession("new") && DelegateID.isSession("s_1-a"))
        for bad in ["", ".", "..", "a.b", "a/b", "a b", String(repeating: "a", count: 65)] { #expect(!DelegateID.isSession(bad), "\(bad)") }
        for bad in ["..", ".", "../changesets"] {
            for type in ["xbin:agent-open-request", "xbin:agent-open-changeset"] {
                if case .refuse = AgentBridge.decide(["type": .string(type), "id": .string(bad)], tile: "xbin/wsagent", holder: "xbin/wsagent") {} else {
                    Issue.record("\(type) \(bad) was taken")
                }
            }
        }
    }

    /// Only the component xbind names as the person's agent may ask the
    /// chrome anything; what it may ask is to SHOW something by id.
    @Test func onlyTheHolderAsksAndOnlyToShow() {
        let open: JSONValue = ["type": "xbin:agent-open-request", "id": "0123456789abcdef01234567"]
        #expect(AgentBridge.decide(open, tile: "xbin/wsagent", holder: "xbin/wsagent") == .openRequest("0123456789abcdef01234567"))
        if case .refuse = AgentBridge.decide(open, tile: "apps/evil", holder: "xbin/wsagent") {} else { Issue.record("a foreign tile opened a review") }
        if case .refuse = AgentBridge.decide(open, tile: "xbin/wsagent", holder: nil) {} else { Issue.record("no agent, yet a review opened") }
        // there is no message that approves, denies, resumes or changes a preference
        for type in ["xbin:agent-approve", "xbin:agent-allow", "xbin:agent-deny", "xbin:agent-resume", "xbin:agent-prefs", "approve"] {
            let msg: JSONValue = ["type": .string(type), "id": "rq1", "grant": "always", "scope": "tile"]
            if case .refuse = AgentBridge.decide(msg, tile: "xbin/wsagent", holder: "xbin/wsagent") {} else { Issue.record("\(type) was taken") }
        }
    }

    /// A launcher the agent's view opened creates nothing by itself, even
    /// with one coding agent there: only the person's tap starts a session.
    @Test func theViewNeverStartsASession() {
        #expect(AgentLaunch.startsAtOnce(providers: 1, openedByAgentView: false), "the person opened it: at once, as before")
        #expect(!AgentLaunch.startsAtOnce(providers: 1, openedByAgentView: true), "the view opened it: nothing until a tap")
        #expect(!AgentLaunch.startsAtOnce(providers: 2, openedByAgentView: false))
        #expect(!AgentLaunch.startsAtOnce(providers: 0, openedByAgentView: false))
    }

    @Test func theAgentPage() {
        #expect(XbindPage(linkName: "agent") == .agent)
        #expect(XbindPage.agent.path == "/xbin/agent")
        #expect(XbindPageAvailability.from(json(200, ["workspace": true]), page: .agent) == .served)
        #expect(XbindPageAvailability.from(APIResponse(status: 404), page: .agent) == .notServed)
        #expect(XbindPageAvailability.from(nil, page: .agent) == .unknown)
        // the partitions page still needs its feature
        #expect(XbindPageAvailability.from(json(200, ["features": []]), page: .partitions) == .notServed)
    }
}

/// Allow, and every control that widens what the agent may do, are armed
/// the same way (the web chrome's WidenGuard): never before the delay,
/// never while covered, and narrowing is never held.
@Suite struct ArmingTests {
    @Test func clock() {
        let t0 = Date(timeIntervalSince1970: 1_000)
        var c = ArmingClock()
        #expect(!c.isArmed(at: t0))
        #expect(c.remaining(at: t0) == nil)
        c.shown(at: t0)
        #expect(!c.isArmed(at: t0))
        #expect(!c.isArmed(at: t0.addingTimeInterval(0.59)))
        #expect(c.isArmed(at: t0.addingTimeInterval(0.6)))
        #expect(abs((c.remaining(at: t0.addingTimeInterval(0.2)) ?? 0) - 0.4) < 1e-5)
        // a longer delay is honoured, a shorter one never: 600 ms is the floor
        #expect(!c.isArmed(at: t0.addingTimeInterval(1), delay: 6))
        #expect(c.isArmed(at: t0.addingTimeInterval(6), delay: 6))
        #expect(!c.isArmed(at: t0.addingTimeInterval(0.3), delay: 0.1))
        // covered (another sheet, the app elsewhere): not armed, however long it was shown
        c.covered()
        #expect(!c.isArmed(at: t0.addingTimeInterval(60)))
        // shown again: the wait starts again
        c.shown(at: t0.addingTimeInterval(60))
        #expect(!c.isArmed(at: t0.addingTimeInterval(60.5)))
        #expect(c.isArmed(at: t0.addingTimeInterval(60.6)))
    }

    /// A request of a kind this app doesn't know (a newer xbind's) is never
    /// one to decide here — as a state it doesn't know isn't — and says so.
    @Test func unknownKindsAreNotActionable() throws {
        let r = try #require(DelegateRequest(json: ["id": "rq_new", "kind": "sandbox", "state": "pending", "method": "POST", "target": "/api/apps/x/y", "tile": "apps/x"]))
        #expect(r.kind == .unknown && !r.isPending)
        #expect(DelegationWords.needTitle(r) == DelegationWords.unknownKind && DelegationWords.heading(r).plain == DelegationWords.unknownKind)
        #expect(DelegationWords.grantChoices(r).isEmpty && DelegationWords.allowLabel(r).isEmpty)
        let card = DelegationWords.requestCard(r)
        #expect(card.head == DelegationWords.unknownState.title && card.line == DelegationWords.unknownState.text && !card.waits)
        // a request from before kinds: a call
        let old = try #require(DelegateRequest(json: ["id": "rq_old", "state": "pending", "method": "POST", "target": "/api/apps/x/y"]))
        #expect(old.kind == .call && old.isPending)
    }

    @Test func whatWidens() {
        #expect(AgentWidening.widens(from: .once, to: .call) && AgentWidening.widens(from: .once, to: .tile) && AgentWidening.widens(from: .call, to: .tile))
        #expect(!AgentWidening.widens(from: .tile, to: .once) && !AgentWidening.widens(from: .call, to: .once) && !AgentWidening.widens(from: .tile, to: .call))
        #expect(!AgentWidening.widens(from: .call, to: .call))
        // an MCP tool's "always, this tool" (D239) widens Once; Once never widens it
        #expect(AgentWidening.widens(from: .once, to: .tool) && !AgentWidening.widens(from: .tool, to: .once))
        #expect(AgentWidening.widens(fromDuration: "hour", toDuration: "always"))
        #expect(!AgentWidening.widens(fromDuration: "always", toDuration: "hour") && !AgentWidening.widens(fromDuration: "always", toDuration: "always"))
        #expect(AgentWidening.widens(fromStart: "ask", toStart: "allow") && !AgentWidening.widens(fromStart: "allow", toStart: "ask"))
        #expect(AgentWidening.widens(from: false, to: true) && !AgentWidening.widens(from: true, to: false))
        // "its model only, no internet": turning it OFF is what widens
        #expect(AgentWidening.widens(from: true, to: false, widerWhenOn: false) && !AgentWidening.widens(from: false, to: true, widerWhenOn: false))
        // narrowing goes through unarmed; widening only armed
        #expect(AgentWidening.takes(widening: false, armed: false))
        #expect(!AgentWidening.takes(widening: true, armed: false))
        #expect(AgentWidening.takes(widening: true, armed: true))
    }
}
