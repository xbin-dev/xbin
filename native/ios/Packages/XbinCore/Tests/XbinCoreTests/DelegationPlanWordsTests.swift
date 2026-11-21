import Foundation
import Testing
@testable import XbinCore

/// PlanWords is a port of the web chrome's plan review
/// (web/agent-plan-words.js): the vectors are that module's own output over
/// a corpus of plans
/// (native/tools/delegation-words.mjs, `plans`), and the port must say
/// exactly the same — which lines start ticked, what a tick needs, what is
/// an admin's, and that a kind this app doesn't know is never ticked.
@Suite struct DelegationPlanWordsTests {
    let v: JSONValue

    init() throws { v = try Resources.json("delegation-words.json") }

    private func s(_ j: JSONValue?, _ k: String) -> String { j?[k]?.stringValue ?? "" }
    private func ints(_ j: JSONValue?) -> [Int] { (j?.arrayValue ?? []).compactMap { $0.intValue.map { Int($0) } } }

    @Test func plans() throws {
        let cases = try #require(v["plans"]?.arrayValue)
        #expect(cases.count >= 10)
        for c in cases {
            let cs = try #require(AdminChangeSet(json: c["json"]))
            let id = cs.id
            #expect(cs.plan)
            let need = DelegationWords.needChangeTitle(cs)
            #expect(need.title == s(c["need"], "title") && need.note == s(c["need"], "note"), "\(id)")
            #expect(PlanWords.askedWords(cs) == s(c, "askedWords"), "\(id)")
            #expect(PlanWords.waitingWords(cs) == s(c, "waitingWords"), "\(id): what waits for xbin")
            let wantItems = c["items"]?.arrayValue ?? []
            #expect(cs.items.count == wantItems.count, "\(id)")
            for (it, want) in zip(cs.items, wantItems) {
                #expect(DelegationWords.kindTag(it.kind) == s(want, "tag"), "\(id) \(it.n)")
                #expect(PlanWords.knownKind(it.kind) == want["known"]?.boolValue, "\(id) \(it.n)")
                let r = DelegationWords.resultOf(it)
                #expect(r.word == s(want["result"], "word") && r.text == s(want["result"], "text"), "\(id) \(it.n): result")
                #expect(it.before == want["before"]?.stringValue, "\(id) \(it.n): before \(it.before ?? "nil")")
                #expect(it.after == want["after"]?.stringValue, "\(id) \(it.n): after \(it.after ?? "nil")")
                #expect(PlanWords.neededBy(cs, it.n) == ints(want["neededBy"]), "\(id) \(it.n): neededBy")
                if let ws = want["start"], ws.objectValue != nil {
                    let rows = try #require(PlanWords.startRows(it))
                    #expect(rows.agent == s(ws, "agent") && rows.brief == s(ws, "brief") && rows.network == s(ws, "network"), "\(id) \(it.n): start")
                    #expect(rows.modeTag == s(ws["mode"], "tag") && rows.modeText == s(ws["mode"], "text") && rows.modeName == s(ws["mode"], "name"), "\(id) \(it.n): mode")
                    #expect(s(ws["mode"], "tone") == "" && ws["danger"] == nil && rows.modeTag == s(ws["mode"], "tag") && rows.modeText == s(ws["mode"], "text"), "\(id) \(it.n): the mode, a fact (D251)")
                    #expect(rows.briefBy == s(ws, "briefBy"), "\(id) \(it.n): who wrote the brief")
                }
            }
            let who = id
            let ticks = PlanWords.planTicks(cs)
            #expect(ticks == ints(c["ticks"]), "\(who): ticks")
            let w = PlanWords.planWords(cs, ticks: ticks)
            let ww = c["words"]
            #expect(w.bar == s(ww, "bar") && w.heading == s(ww, "heading") && w.intro == s(ww, "intro"), "\(who)")
            #expect(w.footer == s(ww, "footer") && w.apply == s(ww, "apply") && w.reject == s(ww, "reject"), "\(who)")
            #expect(PlanWords.planWords(cs, ticks: []).apply == s(c, "none"), "\(who)")
            #expect(ww?["optIn"] == nil, "\(who): the review offers no switch")
            #expect((w.ask == nil) == (ww?["ask"]?.objectValue == nil), "\(who): ask an admin")
            if let a = w.ask {
                #expect(a.text == s(ww?["ask"], "text") && a.button == s(ww?["ask"], "button") && a.why == s(ww?["ask"], "why") && a.can == ww?["ask"]?["can"]?.boolValue, "\(who)")
            }
            #expect(w.byHand == (ww?["byHand"]?.arrayValue ?? []).compactMap(\.stringValue), "\(who)")
            let lines = c["lines"]?.arrayValue ?? []
            #expect(lines.count == cs.items.count, "\(who)")
            for (it, l) in zip(cs.items, lines) {
                #expect(PlanWords.lineState(it).rawValue == s(l, "state"), "\(who) \(it.n): state")
                #expect(PlanWords.canTick(cs, ticks: ticks, item: it) == l["can"]?.boolValue, "\(who) \(it.n): can")
                #expect(PlanWords.canTick(cs, ticks: [], item: it) == l["alone"]?.boolValue, "\(who) \(it.n): alone")
                let n = PlanWords.lineNotes(cs, item: it)
                #expect(n.tag == s(l["notes"], "tag") && n.needs == s(l["notes"], "needs") && n.neededBy == s(l["notes"], "neededBy") && n.why == s(l["notes"], "why"), "\(who) \(it.n): notes")
                #expect(PlanWords.setTick(cs, ticks: ticks, it.n, on: false) == ints(l["off"]), "\(who) \(it.n): untick")
                #expect(PlanWords.setTick(cs, ticks: ticks, it.n, on: true) == ints(l["on"]), "\(who) \(it.n): tick")
            }
            let keeps: [[Int]] = [cs.items.map(\.n), [3, 4, 5], [1, 2, 3], [6]]
            for (k, want) in zip(keeps, c["kept"]?.arrayValue ?? []) {
                #expect(PlanWords.keepTicks(cs, ticks: k) == ints(want), "\(who): kept \(k)")
            }
            // after Apply: the review closes itself only when every chosen line was applied and nothing waits
            let closes = try #require(c["closes"]?.arrayValue)
            #expect(closes.count == 4, "\(who)")
            for x in closes {
                let done = try #require(AdminChangeSet(json: x["json"]))
                #expect(PlanWords.closesItself(done, chosen: ints(x["chosen"])) == x["want"]?.boolValue, "\(who): closes itself after \(ints(x["chosen"])) (\(done.state))")
            }
        }
    }

    /// An admin's line of one's own plan starts unticked and is the
    /// admin's to tick — no switch stands before it —, and says whose
    /// rights apply it; after an Apply in which nothing failed the review
    /// closes itself, and a failure keeps it open.
    @Test func adminLinesAndClosing() throws {
        let cs = try #require(AdminChangeSet(json: try JSONValue(parsing: """
        {"id":"x","plan":true,"state":"proposed","items":[{"n":1,"kind":"tile.create","line":"a"},{"n":2,"kind":"binding.set","line":"b","admin":true,"needs":[1]},{"n":3,"kind":"binding.set","line":"c","needs":[1]}]}
        """)))
        #expect(PlanWords.planTicks(cs) == [1, 3])
        #expect(PlanWords.lineState(cs.items[1]) == .ok && PlanWords.canTick(cs, ticks: [1], item: cs.items[1]))
        #expect(PlanWords.lineNotes(cs, item: cs.items[1]).why == "An admin change — applied as you.")
        #expect(PlanWords.lineNotes(cs, item: cs.items[0]).why.isEmpty)
        func done(_ r: [String]) throws -> AdminChangeSet {
            try #require(AdminChangeSet(json: try JSONValue(parsing: """
            {"id":"x","plan":true,"state":"applied","items":[{"n":1,"kind":"tile.create","line":"a","result":"\(r[0])"},{"n":2,"kind":"binding.set","line":"b","admin":true,"needs":[1],"result":"\(r[1])"},{"n":3,"kind":"binding.set","line":"c","needs":[1],"result":"\(r[2])"}]}
            """)))
        }
        #expect(PlanWords.closesItself(try done(["applied", "applied", "applied"]), chosen: [1, 2, 3]))
        #expect(PlanWords.closesItself(try done(["applied", "rejected", "applied"]), chosen: [1, 3]), "a line left out by choice is no failure")
        #expect(!PlanWords.closesItself(try done(["applied", "failed", "skipped"]), chosen: [1, 2, 3]), "a failure stays open")
        #expect(!PlanWords.closesItself(try done(["rejected", "rejected", "rejected"]), chosen: []))
        #expect(PlanWords.closeAfter >= 1)
    }

    /// A kind this app doesn't know (a newer xbind's): shown, never ticked —
    /// whatever the person's rights, alone or with every other line.
    @Test func unknownKindsAreNeverActionable() throws {
        let cs = try #require(AdminChangeSet(json: try JSONValue(parsing: """
        {"id":"x","plan":true,"state":"proposed","items":[{"n":1,"kind":"tile.create","line":"a"},{"n":2,"kind":"tile.delete","line":"b"},{"n":3,"kind":"future.kind","line":"c","needs":[1]}]}
        """)))
        #expect(PlanWords.planTicks(cs) == [1])
        #expect(PlanWords.keepTicks(cs, ticks: [1, 2, 3]) == [1])
        for n in [2, 3] {
            #expect(PlanWords.lineState(cs.items[n - 1]) == .unknown)
            #expect(!PlanWords.canTick(cs, ticks: [1, 2, 3], item: cs.items[n - 1]))
        }
    }

    /// What the app can't act on it says truthfully (review, D243): a line
    /// that waits is shown as waiting, with xbind's whole reason when it is
    /// held, and the sheet names no control it doesn't have — cancelling
    /// is on the web. A tile agent's permission request is its session's
    /// card: only the person answers, in the app's own agent screen.
    @Test func whatWaitsIsToldTruthfullyInTheApp() throws {
        let held = "apps/dice declares mcp for the role \"writer\": what you approved is the role reader"
        let json = try JSONValue(parsing: #"""
        {"id":"c1","user":"bob","agent":"apps/q","title":"A dice tool","state":"applied","plan":true,"items":[
          {"n":1,"kind":"tile.create","line":"Create the tile apps/dice","result":"applied"},
          {"n":2,"kind":"binding.set","line":"Bind apps/assistant's mcp to apps/dice","result":"waiting",
           "wait":{"component":"apps/assistant","slot":"mcp","tile":"apps/dice","kind":"http","service":"mcp","role":"reader","until":"2026-11-17T10:00:00Z",
                   "held":"apps/dice declares mcp for the role \"writer\": what you approved is the role reader"}}]}
        """#)
        let cs = try #require(AdminChangeSet(json: json))
        let r = DelegationWords.resultOf(cs.items[1])
        #expect(r.word == "waiting" && r.tone == "warn")
        #expect(r.text == "Not bound — \(held). xbin applies it once apps/dice provides mcp as you approved it.", "the whole mismatch is the person's to read")
        let words = PlanWords.waitingWordsInApp(cs)
        #expect(words == "1 line waits: xbin applies it by itself once the tile provides what you approved. To cancel it, open this review on the web.")
        #expect(!words.contains("from here"), "the sheet has no control that cancels a waiting line")
        var two = cs
        two.items.append(cs.items[1])
        #expect(PlanWords.waitingWordsInApp(two).hasSuffix("To cancel them, open this review on the web."))
        var none = cs
        none.items.removeLast()
        #expect(PlanWords.waitingWordsInApp(none).isEmpty)
        // a result this app doesn't know is shown as xbind named it, with no tone and nothing to press
        var unknown = cs.items[1]
        unknown.result = "parked"
        let u = DelegationWords.resultOf(unknown)
        #expect(u.word == "parked" && u.tone.isEmpty && u.text.isEmpty)

        let row = try #require(AgentSessionRow(json: try JSONValue(parsing: #"{"id":"s1","cwd":"apps/dice","provider":"claude","status":"waiting_permission","pending":1,"origin":"agent"}"#)))
        let card = DelegationWords.sessionCard(row, tileName: "Dice", harnessName: "Claude Code")
        #expect(card.waits && card.word == "waiting for you" && card.line == "It asks your permission." && card.note == "Only you can answer." && card.button == "Answer")
    }
}
