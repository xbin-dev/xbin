import Foundation
import Testing
@testable import XbinCore

/// PlanWords is a port of the web chrome's plan review
/// (web/agent-plan-words.js): the vectors are that module's own output over
/// a corpus of plans, each under four people's states
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
        #expect(cases.count >= 8)
        for c in cases {
            let cs = try #require(AdminChangeSet(json: c["json"]))
            let id = cs.id
            #expect(cs.plan)
            let need = DelegationWords.needChangeTitle(cs)
            #expect(need.title == s(c["need"], "title") && need.note == s(c["need"], "note"), "\(id)")
            #expect(PlanWords.askedWords(cs) == s(c, "askedWords"), "\(id)")
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
                    #expect(rows.modeAsks == (s(ws["mode"], "tone") == "ok") && rows.danger == s(ws, "danger"), "\(id) \(it.n): only xbind's word that the mode asks")
                }
            }
            let asked = !cs.from.isEmpty
            for p in try #require(c["people"]?.arrayValue) {
                let who = "\(id) as \(s(p, "name"))"
                let state = DelegateState(json: p["state"])
                let ticks = PlanWords.planTicks(cs, state: state, asked: asked)
                #expect(ticks == ints(p["ticks"]), "\(who): ticks")
                let w = PlanWords.planWords(cs, ticks: ticks, state: state)
                let ww = p["words"]
                #expect(w.bar == s(ww, "bar") && w.heading == s(ww, "heading") && w.intro == s(ww, "intro"), "\(who)")
                #expect(w.footer == s(ww, "footer") && w.apply == s(ww, "apply") && w.reject == s(ww, "reject"), "\(who)")
                #expect(PlanWords.planWords(cs, ticks: [], state: state).apply == s(p, "none"), "\(who)")
                #expect((w.optIn == nil) == (ww?["optIn"]?.objectValue == nil), "\(who): the opt-in is offered")
                if let o = w.optIn { #expect(o.text == s(ww?["optIn"], "text") && o.label == s(ww?["optIn"], "label") && o.note == s(ww?["optIn"], "note"), "\(who)") }
                #expect((w.ask == nil) == (ww?["ask"]?.objectValue == nil), "\(who): ask an admin")
                if let a = w.ask {
                    #expect(a.text == s(ww?["ask"], "text") && a.button == s(ww?["ask"], "button") && a.why == s(ww?["ask"], "why") && a.can == ww?["ask"]?["can"]?.boolValue, "\(who)")
                }
                #expect(w.byHand == (ww?["byHand"]?.arrayValue ?? []).compactMap(\.stringValue), "\(who)")
                let lines = p["lines"]?.arrayValue ?? []
                #expect(lines.count == cs.items.count, "\(who)")
                for (it, l) in zip(cs.items, lines) {
                    #expect(PlanWords.lineState(it, state: state, asked: asked).rawValue == s(l, "state"), "\(who) \(it.n): state")
                    #expect(PlanWords.canTick(cs, ticks: ticks, item: it, state: state, asked: asked) == l["can"]?.boolValue, "\(who) \(it.n): can")
                    #expect(PlanWords.canTick(cs, ticks: [], item: it, state: state, asked: asked) == l["alone"]?.boolValue, "\(who) \(it.n): alone")
                    let n = PlanWords.lineNotes(cs, item: it, state: state, asked: asked)
                    #expect(n.tag == s(l["notes"], "tag") && n.needs == s(l["notes"], "needs") && n.neededBy == s(l["notes"], "neededBy") && n.why == s(l["notes"], "why"), "\(who) \(it.n): notes")
                    #expect(PlanWords.setTick(cs, ticks: ticks, it.n, on: false) == ints(l["off"]), "\(who) \(it.n): untick")
                    #expect(PlanWords.setTick(cs, ticks: ticks, it.n, on: true) == ints(l["on"]), "\(who) \(it.n): tick")
                }
                let keeps: [[Int]] = [cs.items.map(\.n), [3, 4, 5], [1, 2, 3], [6]]
                for (k, want) in zip(keeps, p["kept"]?.arrayValue ?? []) {
                    #expect(PlanWords.keepTicks(cs, ticks: k, state: state, asked: asked) == ints(want), "\(who): kept \(k)")
                }
            }
        }
    }

    /// A kind this app doesn't know (a newer xbind's): shown, never ticked —
    /// whatever the person's rights, alone or with every other line.
    @Test func unknownKindsAreNeverActionable() throws {
        let cs = try #require(AdminChangeSet(json: try JSONValue(parsing: """
        {"id":"x","plan":true,"state":"proposed","items":[{"n":1,"kind":"tile.create","line":"a"},{"n":2,"kind":"tile.delete","line":"b"},{"n":3,"kind":"future.kind","line":"c","needs":[1]}]}
        """)))
        for st in [#"{"user":"a","canAdminChanges":true,"adminChanges":true}"#, #"{"user":"a"}"#] {
            let state = DelegateState(json: try JSONValue(parsing: st))
            #expect(PlanWords.planTicks(cs, state: state) == [1])
            #expect(PlanWords.keepTicks(cs, ticks: [1, 2, 3], state: state) == [1])
            for n in [2, 3] {
                #expect(PlanWords.lineState(cs.items[n - 1], state: state) == .unknown)
                #expect(!PlanWords.canTick(cs, ticks: [1, 2, 3], item: cs.items[n - 1], state: state))
            }
        }
    }
}
