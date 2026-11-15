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
            }
            #expect(DelegationWords.settled(r, zone: utc) == c["settled"]?.stringValue, "\(id)")
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
            #expect(DelegationWords.endedWords(cs) == s(c, "ended"), "\(cs.id)")
            let res = DelegationWords.resultsWords(cs)
            #expect(res.title == s(c["results"], "title"), "\(cs.id)")
            #expect(res.banner == s(c["results"], "banner"), "\(cs.id)")
            #expect(res.undo == s(c["results"], "undo"), "\(cs.id)")
            for (it, want) in zip(cs.items, c["items"]?.arrayValue ?? []) {
                #expect(DelegationWords.kindTag(it.kind) == s(want, "tag"), "\(cs.id) \(it.n)")
                #expect(DelegationWords.tickable(it) == want["tickable"]?.boolValue, "\(cs.id) \(it.n)")
                for (w, ww) in zip(it.warnings, want["warnings"]?.arrayValue ?? []) {
                    let got = DelegationWords.warning(w)
                    #expect(got.tag == s(ww, "tag") && got.text == s(ww, "text"), "\(cs.id) \(it.n)")
                    #expect(got.icon == s(ww, "icon"), "\(cs.id) \(it.n)")
                }
                let r = DelegationWords.resultOf(it)
                #expect(r.word == s(want["result"], "word") && r.text == s(want["result"], "text"), "\(cs.id) \(it.n)")
            }
        }
        // before/after: a string as it is, a JSON value compact, null absent
        let first = try #require(AdminChangeSet(json: cases[0]["json"]))
        #expect(first.items[0].before == "no access")
        #expect(first.items[1].before == nil)
        #expect(first.items[1].after == "{\"host\":\"export.example\"}")
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

    @Test func cards() throws {
        let cases = try #require(v["requests"]?.arrayValue)
        let pending = try #require(DelegateRequest(json: cases[0]["json"]))
        let card = DelegationWords.requestCard(pending, now: now, zone: utc)
        #expect(card.waits && card.button == "Review…" && card.head == "Waiting for your approval")
        #expect(card.what == "Approve a change to Invoices")
        #expect(card.line == "asked 10:14 · expires in 23 h 58 min")
        let used = try #require(DelegateRequest(json: cases[6]["json"]))
        let done = DelegationWords.requestCard(used, now: now, zone: utc)
        #expect(!done.waits && done.button.isEmpty && done.tone == "ok")
        // a tile agent's card: Answer when it waits — only the person can
        let waiting = try #require(AgentSessionRow(json: ["id": "s1", "cwd": "apps/orders", "provider": "claude", "status": "waiting_permission", "pending": 1]))
        let w = DelegationWords.sessionCard(waiting, tileName: "Orders", harnessName: "Claude Code")
        #expect(w.button == "Answer" && w.note == "Only you can answer." && w.tone == "warn" && w.waits)
        let cut = try #require(AgentSessionRow(json: ["id": "s2", "status": "ended", "endedBy": "restart"]))
        #expect(DelegationWords.sessionCard(cut, tileName: "Orders", harnessName: "").word == "stopped for the update")
        let running = try #require(AgentSessionRow(json: ["id": "s3", "status": "running", "last": "Writing export_test.go"]))
        let r = DelegationWords.sessionCard(running, tileName: "Orders", harnessName: "Claude Code")
        #expect(r.word == "working" && r.button == "Open" && r.line == "Writing export_test.go")
        #expect(DelegationWords.homeStatus(.page, tileAgents: 2, working: 1, count: 2) == "working · 2 tile agents · 2 need you")
        #expect(DelegationWords.homeStatus(.page, tileAgents: 0, working: 0, count: 0) == "idle")
        #expect(DelegationWords.homeStatus(.paused, tileAgents: 2, working: 1, count: 2) == "paused")
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
    @Test func aTileRuntimeNeverCarriesThePersonsSession() {
        let grants = ["/api/xbin/delegate/requests/rq1/approve", "/api/xbin/delegate/changesets/cs1/approve", "/api/xbin/delegate/state"]
        for path in grants {
            #expect(DelegateRoute.isGrant(method: path.hasSuffix("state") ? "PUT" : "POST", path: path))
            // what the page sent — a cookie, a frame token it made up, the client header it claims
            let fwd = TileScheme.forwardHeaders(["Cookie": "xbin_session=stolen", TileScheme.frameTokenHeader: "the-persons?", "Content-Type": "application/json",
                                                 TileScheme.clientHeader: "app/9"], frameToken: "the-tiles-own", client: "app/1")
            #expect(fwd[TileScheme.frameTokenHeader] == "the-tiles-own")
            #expect(fwd[TileScheme.clientHeader] == "app/1")
            #expect(fwd.keys.allSatisfy { $0.lowercased() != "cookie" })
            // the handler adds no bearer: the only credential on the request is the tile's token
            #expect(fwd.keys.allSatisfy { $0.lowercased() != "authorization" })
        }
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

    @Test func whatWidens() {
        #expect(AgentWidening.widens(from: .once, to: .call) && AgentWidening.widens(from: .once, to: .tile) && AgentWidening.widens(from: .call, to: .tile))
        #expect(!AgentWidening.widens(from: .tile, to: .once) && !AgentWidening.widens(from: .call, to: .once) && !AgentWidening.widens(from: .tile, to: .call))
        #expect(!AgentWidening.widens(from: .call, to: .call))
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
