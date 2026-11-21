import Foundation
import Testing
@testable import XbinCore

/// StartWords is a port of the web chrome's /vendor/agent-start-words.js:
/// the vectors are that module's own output over a corpus of starts with
/// choices, follow-ups, messages and Try agains, each played through the
/// steps a person takes on the sheet (native/tools/delegation-words.mjs
/// --start-words; `make js-test` fails while they are stale) — the port
/// must decide and say exactly the same (D253).
@Suite struct DelegationStartWordsTests {
    let v: JSONValue
    let now: Date
    let utc = TimeZone(identifier: "UTC")!

    init() throws {
        v = try Resources.json("start-words.json")
        now = try #require(DelegateTime.date(v["now"]))
    }

    private func s(_ j: JSONValue?, _ k: String) -> String { j?[k]?.stringValue ?? "" }
    private func strings(_ j: JSONValue?) -> [String] { (j?.arrayValue ?? []).compactMap(\.stringValue) }

    private func samePickers(_ got: [StartPicker], _ want: JSONValue?, _ id: String) {
        let w = want?.arrayValue ?? []
        #expect(got.count == w.count, "\(id): pickers \(got.map(\.id))")
        for (a, b) in zip(got, w) {
            #expect(a.id == s(b, "id") && a.label == s(b, "label") && a.value == s(b, "value"), "\(id): \(a.id)")
            #expect(a.from == s(b, "from") && a.note == s(b, "note") && a.warn == s(b, "warn"), "\(id): \(a.id) \(a.from) | \(a.note) | \(a.warn)")
            let opts = (b["options"]?.arrayValue ?? []).map { StartPicker.Option(value: s($0, "value"), label: s($0, "label")) }
            #expect(a.options == opts, "\(id): \(a.id) options \(a.options)")
        }
    }

    /// Every start played step by step: the pick, the sheet's view, its
    /// heading, the start as picked (what runs, the sign-in it offers, its
    /// rows), the pick's body and what Allow sends — clipped and whole.
    @Test func starts() throws {
        let cases = try #require(v["starts"]?.arrayValue)
        #expect(cases.count >= 8)
        for c in cases {
            let r = try #require(DelegateRequest(json: c["json"]))
            let states = c["states"]?.arrayValue ?? []
            var pick = StartWords.pickOf(r)
            for (n, state) in states.enumerated() {
                let id = "\(r.id)#\(n)"
                if let step = state["step"]?.arrayValue, step.count == 2 {
                    let f = step[0].stringValue ?? "", value = step[1]
                    switch f {
                    case "cont": pick = StartWords.contChoice(pick, r, value.boolValue == true)
                    case "remember": pick.remember = value.boolValue == true
                    default: pick = StartWords.choose(pick, r, field: f, value: value.stringValue ?? "")
                    }
                }
                let p = state["pick"]
                #expect(pick.harness == s(p, "harness") && pick.model == s(p, "model") && pick.mode == s(p, "mode"), "\(id): \(pick)")
                #expect(pick.remember == p?["remember"]?.boolValue && pick.cont == p?["cont"]?.boolValue, "\(id): \(pick)")
                #expect(pick.changed == strings(p?["changed"]) && pick.defaulted == strings(p?["defaulted"]), "\(id): \(pick)")

                let view = StartWords.startView(r, pick, zone: utc)
                let w = state["view"]
                samePickers(view.pickers, w?["pickers"], id)
                if let cont = w?["cont"], !cont.isNull {
                    let got = try #require(view.cont, "\(id): the continue offered")
                    #expect(got.session == s(cont, "session") && got.label == s(cont, "label") && got.sub == s(cont, "sub"), "\(id): \(got)")
                    #expect(got.chips == strings(cont["chips"]) && got.signInId == s(cont, "signInId"), "\(id): \(got)")
                } else {
                    #expect(view.cont == nil, "\(id)")
                }
                #expect(view.continuing == w?["continuing"]?.boolValue && view.asks == w?["asks"]?.boolValue, "\(id)")
                #expect(view.modeChip == s(w, "modeChip") && view.fact == s(w, "fact") && view.label == s(w, "label") && view.details == s(w, "details"), "\(id)")
                let rem = w?["remember"]
                #expect(view.remember.shown == rem?["shown"]?.boolValue && view.remember.label == s(rem, "label") && view.remember.note == s(rem, "note")
                    && view.remember.keepsMode == rem?["keepsMode"]?.boolValue, "\(id): \(view.remember)")
                let h = StartWords.sheetHeading(r, view)
                #expect(h.before == s(state["heading"], "before") && h.strong == s(state["heading"], "strong") && h.after == s(state["heading"], "after"), "\(id): \(h)")
                #expect(JSONValue.object(StartWords.pickBody(r, pick)) == state["body"], "\(id): \(StartWords.pickBody(r, pick))")

                // the start as picked: what runs, and the rows the sheet draws of it — the sign-in line its coding agent offers
                let picked = StartWords.picked(r, pick)
                let ps = picked.session, wp = state["picked"]
                #expect(s(ps, "provider") == s(wp, "provider") && s(ps, "providerName") == s(wp, "providerName") && s(ps, "mode") == s(wp, "mode"), "\(id): \(ps)")
                #expect((ps["asks"]?.boolValue == true) == wp?["asks"]?.boolValue && s(ps, "model") == s(wp, "model") && s(ps, "net") == s(wp, "net"), "\(id): \(ps)")
                #expect(s(ps, "signIn") == s(wp, "signIn") && s(ps, "signInId") == s(wp, "signInId"), "\(id): \(ps)")
                #expect((ps["signInTrust"]?.objectValue != nil) == wp?["trust"]?.boolValue, "\(id): trust")
                SessionRowsCheck.same(DelegationWords.sessionRows(picked), state["session"], id, brief: false)
                if n == 0 { #expect(DelegationWords.sessionRows(picked).brief == StartWords.briefOf(r), "\(id): the brief is xbind's reading") }

                // Allow: the brief as the sheet shows it — clipped, and after Show all
                let brief = DelegationWords.sessionRows(picked).brief
                let allow = [false, true].map { all in
                    StartWords.sheetBody(r, pick: pick, onScreen: DelegationWords.clipText(brief, all: all).text, base: DelegationWords.approveBody(picked, choice: .once))
                }
                #expect(JSONValue.array(allow) == state["allow"], "\(id): \(allow)")
            }
        }
    }

    @Test func messages() throws {
        let cases = try #require(v["messages"]?.arrayValue)
        #expect(cases.count >= 6)
        for c in cases {
            let r = try #require(DelegateRequest(json: c["json"]))
            let id = r.id
            #expect(r.kind == .message && r.isPending, "\(id): a kind this app shows and decides (D253)")
            let m = StartWords.messageView(r), w = c["view"]
            #expect(m.heading.before == s(w?["heading"], "before") && m.heading.strong == s(w?["heading"], "strong") && m.heading.after == s(w?["heading"], "after"), "\(id)")
            #expect(m.title == s(w, "title") && m.agent == strings(w?["agent"]) && m.signInId == s(w, "signInId"), "\(id): \(m.agent)")
            #expect(m.text == (r.preview["message"]?["text"]?.stringValue ?? ""), "\(id): xbind's text")
            #expect(m.why == s(w, "why") && m.label == s(w, "label"), "\(id)")
            let h = StartWords.sheetHeading(r)
            #expect(h == m.heading && h.plain == DelegationWords.heading(r).plain, "\(id)")
            #expect(DelegationWords.needTitle(r) == s(c, "title") && StartWords.signInIdOf(r) == s(c, "signInId"), "\(id)")
            #expect(DelegationWords.allowLabel(r) == "Send" && DelegationWords.grantChoices(r).isEmpty, "\(id)")
            if let want = c["inPlace"], !want.isNull {
                let got = try #require(StartWords.inPlace(r), "\(id): allowed on the web's row")
                #expect(got.kind == s(want, "kind") && got.title == s(want, "title") && got.chips == strings(want["chips"]) && got.text == m.text, "\(id): \(got)")
                #expect(got.byXbin == want["byXbin"]?.boolValue && got.body == want["body"], "\(id): \(got)")
            } else {
                #expect(StartWords.inPlace(r) == nil, "\(id): the sheet's")
            }
            #expect((StartWords.appInPlace(r) != nil) == c["app"]?.boolValue, "\(id): the app's row")
            let allow = [false, true].map { all in
                StartWords.sheetBody(r, pick: nil, onScreen: DelegationWords.clipText(m.text, all: all).text, base: DelegationWords.approveBody(r, choice: .once))
            }
            #expect(JSONValue.array(allow) == c["allow"], "\(id): \(allow)")
        }
    }

    @Test func rows() throws {
        let cases = try #require(v["rows"]?.arrayValue)
        #expect(cases.count >= 30)
        for c in cases {
            let text = s(c, "text"), all = c["all"]?.boolValue == true, byXbin = c["byXbin"]?.boolValue == true
            let got = StartWords.rowText(text, all: all, byXbin: byXbin), w = c["row"]
            let id = "\(text.prefix(24))… all=\(all) xbin=\(byXbin)"
            #expect(got.whole == w?["whole"]?.boolValue && got.lead == s(w, "lead") && got.opening == s(w, "opening"), "\(id): \(got)")
            #expect(got.note == s(w, "note") && got.shown == s(w, "shown"), "\(id): \(got.note)")
            #expect(StartWords.appRowFits(text) == c["app"]?.boolValue, "\(id)")
        }
    }

    /// The batch's request rows — what Needs you draws them from — and the
    /// app's own: a follow-up on the row only where its text fits a phone's
    /// row; a start that failed as a Try again.
    @Test func batch() throws {
        let b = try #require(v["batch"])
        let byId: [String: DelegateRequest] = Dictionary(
            ((v["starts"]?.arrayValue ?? []).compactMap { $0["json"] } + (v["messages"]?.arrayValue ?? []).compactMap { $0["json"] } + (b["failedJson"]?.arrayValue ?? []))
                .compactMap { DelegateRequest(json: $0) }.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        let requests = strings(b["requests"]).compactMap { byId[$0] }, failed = strings(b["failed"]).compactMap { byId[$0] }
        #expect(failed.count == 3 && !requests.isEmpty)
        let rows = StartWords.batch(requests: requests, failed: failed, now: now, zone: utc)
        let want = b["rows"]?.arrayValue ?? []
        #expect(rows.count == want.count, "\(rows.map(\.id))")
        for (a, w) in zip(rows, want) {
            #expect(a.kind == s(w, "kind") && a.id == s(w, "id") && a.title == s(w, "title"), "\(a.id): \(a.title)")
            #expect(a.chips == strings(w["chips"]) && a.signInId == s(w, "signInId") && a.parts == strings(w["parts"]), "\(a.id): \(a.chips) \(a.parts)")
            #expect(a.action == s(w, "action") && a.review == w["review"]?.boolValue && (a.inline?.kind ?? "") == s(w, "inline"), "\(a.id)")
        }
        // the app's Needs you: the same requests, a follow-up in place only where its row shows it whole
        let needs = AgentNeed.rows(requests: requests, changesets: [], failed: failed, now: now, zone: utc)
        for w in want {
            let id = s(w, "id")
            let n = try #require(needs.first { $0.id == id }, "\(id) in Needs you")
            #expect((n.inline != nil) == w["app"]?.boolValue, "\(id): Send on the app's row")
            #expect(n.kind.rawValue == s(w, "kind") && n.parts == strings(w["parts"]), "\(id)")
            if n.inline != nil { #expect(n.title == s(w, "title") && n.chips == strings(w["chips"]) && n.signInId == s(w, "signInId"), "\(id)") }
        }
        #expect(!needs.contains { $0.id == "rq_st_failed_final" }, "a start that can't be tried again isn't one to decide")
    }

    @Test func sheetWords() throws {
        let w = try #require(v["sheet"])
        typealias X = StartWords.Sheet
        #expect(X.unread == s(w, "unread") && X.reachEnd == s(w, "reachEnd") && X.sent == s(w, "sent") && X.sentOnce == s(w, "sentOnce"))
        #expect(X.whereItGoes == s(w, "whereItGoes") && X.agentRow == s(w, "agentRow") && X.messageRow == s(w, "messageRow") && X.details == s(w, "details"))
        #expect(StartWords.from == (w["from"]?.objectValue ?? [:]).mapValues { $0.stringValue ?? "" })
        #expect(DelegationWords.modeFact == s(w, "modeFact") && DelegationWords.failedHead == s(w, "failedHead") && DelegationWords.tryAgain == s(w, "tryAgain"))
        #expect(StartWords.seenText == s(w, "seenText"))
        for (k, n) in [("appRowLines", StartWords.appRowLines), ("appRowChars", StartWords.appRowChars), ("rowFitLines", StartWords.rowFitLines),
                       ("rowFitChars", StartWords.rowFitChars), ("rowLines", StartWords.rowLines), ("rowChars", StartWords.rowChars)] {
            #expect(Int64(n) == w[k]?.intValue, "\(k)")
        }
        for pair in w["textBytes"]?.arrayValue ?? [] {
            #expect(Int64(StartWords.textBytes(pair[0]?.stringValue ?? "")) == pair[1]?.intValue)
        }
        #expect(StartWords.grouped(0) == "0" && StartWords.grouped(999) == "999" && StartWords.grouped(1200) == "1,200" && StartWords.grouped(1234567) == "1,234,567")
    }

    /// A start that failed, and a message: what an older app read as
    /// unknown — this one shows, and decides only what xbind says it may.
    @Test func failedAndMessageStates() throws {
        let failed = try #require(DelegateRequest(json: ["id": "rq_f", "kind": "session", "state": "failed", "failure": ["why": "x", "retry": true],
                                                         "chosen": ["provider": "codex"], "preview": ["session": ["provider": "claude"]]]))
        #expect(failed.state == .failed && failed.isRetryable && failed.isDecidable && !failed.isPending)
        #expect(StartWords.pickOf(failed).harness == "codex")
        let final = try #require(DelegateRequest(json: ["id": "rq_g", "kind": "session", "state": "failed", "failure": ["why": "refused"]]))
        #expect(!final.isRetryable && !final.isDecidable && DelegationWords.requestCard(final).button.isEmpty)
        // a call never fails this way: nothing to try again
        let call = try #require(DelegateRequest(json: ["id": "rq_c", "kind": "call", "state": "failed", "failure": ["retry": true]]))
        #expect(!call.isRetryable)
        let split = DelegateRequest.split(["requests": [["id": "a", "state": "pending"], ["id": "b", "state": "failed", "kind": "session"], ["id": "c", "state": "approved"]]])
        #expect(split.pending.map(\.id) == ["a", "c"] && split.failed.map(\.id) == ["b"])
    }

    /// The person's own names for their saved sign-ins, by id.
    @Test func savedSignInNames() {
        let j: JSONValue = ["harnesses": [["id": "claude", "signins": [["id": "si_9", "name": "Work"], ["id": "si_x", "name": ""], ["name": "no id"]]],
                                          ["id": "codex", "signins": [["id": "si_2", "name": "Home"]]], ["id": "gemini"]]]
        #expect(DelegateRoute.savedSignInNames(j) == ["si_9": "Work", "si_2": "Home"])
        #expect(DelegateRoute.savedSignInNames(nil).isEmpty && DelegateRoute.signins.path == "/api/xbin/agent/signins")
    }

    /// An xbind before D252 refuses a field it doesn't know (400 "need
    /// {…}"): the app sends the body once more in the shape it takes — the
    /// text's length and a continue's session facts left out, nothing else.
    @Test func anOlderXbindsShape() throws {
        func answer(_ status: Int, _ error: String) -> APIResponse {
            APIResponse(status: status, headers: ["Content-Type": "application/json"], body: JSONValue.object(["error": .string(error)]).jsonData)
        }
        let need = answer(400, #"need {grant?: "once" | "hour" | "always", scope?: "call" | "tile", unseen?: true, signin?: true, trust?: true, via?: "notification", harness?, model?, mode?, remember?: true, continue?: false, knows?: [..], seen?: {signInId?, net?}}"#)
        let cont: JSONValue = ["grant": "once", "knows": ["continue", "seen-text"], "seen": ["signInId": "si_9", "net": "", "harness": "claude", "mode": "auto", "textLen": 12]]
        #expect(StartWords.olderXbindBody(cont, refused: need) == ["grant": "once", "knows": ["continue"], "seen": ["signInId": "si_9", "net": ""]])
        let start: JSONValue = ["grant": "once", "knows": ["seen-text"], "seen": ["textLen": 12]]
        #expect(StartWords.olderXbindBody(start, refused: need) == ["grant": "once"], "v0.3.83: no knows, no seen")
        #expect(StartWords.olderXbindBody(["grant": "once"], refused: need) == nil, "nothing to leave out: the 400 stands")
        #expect(StartWords.olderXbindBody(start, refused: answer(400, "harness, model, mode and remember are a tile agent start's")) == nil, "another 400 is xbind's answer")
        #expect(StartWords.olderXbindBody(start, refused: answer(409, "need {")) == nil)
    }
}

/// The session rows of a start, held to the web's (DelegationWordsTests
/// and DelegationStartWordsTests share it).
enum SessionRowsCheck {
    static func same(_ rows: DelegationWords.SessionRows, _ want: JSONValue?, _ id: String, brief: Bool = true) {
        func s(_ j: JSONValue?, _ k: String) -> String { j?[k]?.stringValue ?? "" }
        #expect(rows.agent == s(want, "agent"), "\(id): \(rows.agent)")
        #expect(rows.modeTag == s(want?["mode"], "tag") && rows.modeText == s(want?["mode"], "text") && rows.modeName == s(want?["mode"], "name"), "\(id)")
        #expect(rows.network == s(want, "network") && rows.others == s(want, "others"), "\(id)")
        if brief { #expect(rows.brief == s(want, "brief"), "\(id)") }
        #expect(rows.comes == s(want, "comes") && rows.resumed == s(want, "resumed"), "\(id)")
        let whose = s(want, "trustWhose")
        #expect(rows.signInTrustWhose == whose, "\(id)")
        if want?["signin"]?["offer"]?.boolValue == true {
            #expect(rows.signIn == s(want, "runs") && rows.signInHolds.isEmpty, "\(id): \(rows.signIn)")
            #expect(rows.signInTrust.contains("on the web") && rows.signInTrust.contains("has no saved sign-in"), "\(id)")
            #expect(rows.signInTrust.contains("trust line") == !whose.isEmpty, "\(id)")
        } else {
            #expect(rows.signIn == s(want?["signin"], "text") && rows.signInHolds.isEmpty && rows.signInTrust.isEmpty, "\(id): \(rows.signIn)")
        }
    }
}
