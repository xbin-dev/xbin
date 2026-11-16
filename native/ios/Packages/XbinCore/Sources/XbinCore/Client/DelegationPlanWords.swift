import Foundation

/// The rules and words of a plan's review (docs/protocol/delegation.md
/// §Plans): a port of the web chrome's `agent-plan-words.js`, held to it by
/// the same vectors as DelegationWords (native/tools/delegation-words.mjs;
/// DelegationWordsTests). A plan is a change set whose lines may create
/// tiles and start tile agents, are the person's own to apply or an
/// admin's, and may need earlier lines.
///
/// Nothing here trusts the agent: every sentence is built from xbind's
/// fields, and a kind this app doesn't know is never one that can be
/// ticked — an older app shows such a line and leaves it to the web.
public enum PlanWords {
    /// The kinds this app knows how to show.
    public static let kinds = ["grant.add", "grant.revoke", "binding.set", "binding.remove", "org.share", "limits.set", "tile.create", "agent.start"]
    public static func knownKind(_ kind: String) -> Bool { kinds.contains(kind) }

    /// What stands between a line and its tick.
    public enum LineState: String, Sendable {
        case ok, blocked, decided, unknown
        case needsAdmin = "needs-admin"
    }

    private static func plural(_ n: Int, _ one: String) -> String { "\(n) \(n == 1 ? one : one + "s")" }
    private static func list(_ ns: [Int]) -> String {
        let s = ns.map(String.init)
        return s.count < 2 ? s.joined() : "\(s.dropLast().joined(separator: ", ")) and \(s[s.count - 1])"
    }

    /// An admin's line of an admin's own plan is `.ok`: their tick and
    /// their Apply (on a recent sign-in, xbind's rule) are the consent for
    /// that line.
    public static func lineState(_ item: AdminChangeSet.Item) -> LineState {
        if !knownKind(item.kind) { return .unknown }
        if !item.result.isEmpty { return .decided }
        if !item.blocked.isEmpty { return .blocked }
        if item.needsAdmin { return .needsAdmin }
        return .ok
    }

    /// The lines that need line n, directly or through another.
    public static func neededBy(_ c: AdminChangeSet, _ n: Int) -> [Int] {
        var out = Set<Int>()
        var grew = true
        while grew {
            grew = false
            for it in c.items where !out.contains(it.n) && it.needs.contains(where: { $0 == n || out.contains($0) }) {
                out.insert(it.n)
                grew = true
            }
        }
        return out.sorted()
    }

    private static func result(_ c: AdminChangeSet, _ n: Int) -> String {
        c.items.indices.contains(n - 1) ? c.items[n - 1].result : ""
    }

    /// The person may tick it now: nothing stands in its way, and every
    /// line it needs is ticked or applied.
    public static func canTick(_ c: AdminChangeSet, ticks: [Int], item: AdminChangeSet.Item) -> Bool {
        guard lineState(item) == .ok else { return false }
        return item.needs.allSatisfy { ticks.contains($0) || result(c, $0) == "applied" }
    }

    /// The lines ticked when the review opens: never one with a warning,
    /// one that can't be ticked, or one whose needed line isn't ticked —
    /// and never an admin's line of the person's own plan: ticking it is
    /// how an admin consents to it. (A request from another person is all
    /// admin lines, opened to apply them: those start as any line does.)
    public static func planTicks(_ c: AdminChangeSet) -> [Int] {
        var ticks: [Int] = []
        let asked = !c.from.isEmpty
        for it in c.items {
            if AgentChoices.warnedStartUnticked, !it.warnings.isEmpty { continue }
            if it.admin, !asked { continue }
            if canTick(c, ticks: ticks, item: it) { ticks.append(it.n) }
        }
        return ticks
    }

    /// The ticks after line n is ticked or unticked: unticking a line
    /// unticks the lines that need it.
    public static func setTick(_ c: AdminChangeSet, ticks: [Int], _ n: Int, on: Bool) -> [Int] {
        if on { return Set(ticks + [n]).sorted() }
        let gone = Set([n] + neededBy(c, n))
        return ticks.filter { !gone.contains($0) }
    }

    /// Ticks that still stand, in order.
    public static func keepTicks(_ c: AdminChangeSet, ticks: [Int]) -> [Int] {
        var out: [Int] = []
        for it in c.items where ticks.contains(it.n) && canTick(c, ticks: out, item: it) { out.append(it.n) }
        return out
    }

    /// How long a plan's results stay up before its review closes itself.
    public static let closeAfter: TimeInterval = 1.8
    /// What the results say while they close themselves.
    public static let closingWords = "Applied. This closes by itself — each line’s outcome stays in the conversation."

    /// After Apply the review needs no tap to end: it is the person's own
    /// plan (the conversation's card keeps each line's outcome), every
    /// line they chose was applied, and nothing is left to do here (a line
    /// still to send to an admin). A failure keeps the results open.
    public static func closesItself(_ c: AdminChangeSet, chosen: [Int]) -> Bool {
        guard c.plan, c.from.isEmpty, c.undoOf.isEmpty, !chosen.isEmpty else { return false }
        if c.items.contains(where: { $0.needsAdmin && $0.result.isEmpty }) { return false }
        return chosen.allSatisfy { result(c, $0) == "applied" }
    }

    /// What the review says under a line, beside xbind's own words.
    public static func lineNotes(_ c: AdminChangeSet, item: AdminChangeSet.Item)
        -> (tag: String, needs: String, neededBy: String, why: String) {
        let st = lineState(item)
        let by = neededBy(c, item.n)
        let why: String
        switch st {
        case .unknown: why = "This page is older than this xbin and does not know this kind of change. Reload the page to review it."
        case .needsAdmin: why = "Not yours to apply: it takes an admin’s rights. xbin shows what it would change to an admin."
        case .ok where item.admin: why = "An admin change — applied as you."
        default: why = ""
        }
        return (item.needsAdmin ? "needs an admin" : item.admin ? "admin" : "",
                item.needs.isEmpty ? "" : "Needs line \(list(item.needs))\(item.deferred ? " — checked again once that exists" : "")",
                by.isEmpty ? "" : "Needed by line \(list(by)) — unticking this unticks \(by.count == 1 ? "it" : "them")",
                why)
    }

    public struct Words: Sendable, Equatable {
        public var bar: String
        public var heading: String
        public var intro: String
        public var footer: String
        public var apply: String
        public var reject: String
        /// The Ask an admin row (nil: no line waits for an admin).
        public var ask: Ask?
        public var byHand: [String]

        public struct Ask: Sendable, Equatable {
            public var text: String
            public var button: String
            public var can: Bool
            public var why: String
        }
    }

    /// The review's bar, heading, intro, footer, Apply button, and the Ask
    /// an admin row for lines that take an admin the person is not.
    public static func planWords(_ c: AdminChangeSet, ticks: [Int]) -> Words {
        let total = c.items.count, n = ticks.count
        let asked = !c.from.isEmpty
        let waits = c.items.filter { $0.needsAdmin && $0.result.isEmpty }
        let blockedBy = Set(waits.flatMap { $0.needs.filter { result(c, $0) != "applied" } }).sorted()
        var ask: Words.Ask?
        if !waits.isEmpty {
            ask = .init(text: "\(plural(waits.count, "line")) \(waits.count == 1 ? "needs" : "need") an admin.", button: "Ask an admin", can: blockedBy.isEmpty,
                        why: blockedBy.isEmpty
                            ? "The workspace’s admins get it as a request from you, in xbin’s words. They apply it with their own rights; you can apply the rest now."
                            : "Apply line \(list(blockedBy)) first: what waits for an admin needs it.")
        }
        let these = total == 1 ? "this" : "these", them = total == 1 ? "it" : "them"
        return Words(
            bar: asked ? "xbin · request from \(c.from)" : "xbin · plan",
            heading: asked ? "\(c.from) asks for \(plural(total, "admin change"))" : !c.undoOf.isEmpty ? "Undo: \(plural(total, "change"))" : "Your agent proposes \(plural(total, "change"))",
            intro: asked
                ? "\(c.from)'s workspace agent proposed \(these), and \(c.from) asked the workspace's admins to apply \(them). Each line, its before and after and its warnings are written by xbin from the change itself, as you would apply it. Nothing has been applied."
                : "Each line, its before and after and its warnings are written by xbin from the change itself. Nothing has been applied.",
            footer: "\(n) of \(total) ticked · applied in order as you. If one fails, later ones are skipped and earlier ones stay.",
            apply: n > 0 ? "Apply \(plural(n, "change"))…" : "Apply none",
            reject: asked ? "Decline" : "Reject all",
            ask: ask, byHand: c.byHand)
    }

    /// An agent.start line as a single start's sheet draws it
    /// (DelegationWords.sessionRows), from xbind's `start`.
    public static func startRows(_ item: AdminChangeSet.Item) -> DelegationWords.SessionRows? {
        let s = item.start ?? .object([:])
        let tile = s["tile"] ?? .string("")
        let body = JSONValue.object(["prompt": .string(s["prompt"]?.stringValue ?? "")]).jsonString
        guard let r = DelegateRequest(json: .object(["id": .string("line"), "kind": .string("session"), "tile": tile, "body": .string(body),
                                                      "preview": .object(["tile": tile, "session": s])])) else { return nil }
        return DelegationWords.sessionRows(r)
    }

    /// The results' line about what was sent to, or waits for, an admin.
    public static func askedWords(_ c: AdminChangeSet) -> String {
        let asked = c.items.filter { $0.result == "asked" }.count
        let waits = c.items.filter { $0.needsAdmin && $0.result.isEmpty }.count
        return [asked > 0 ? "\(plural(asked, "line")) \(asked == 1 ? "was" : "were") sent to the workspace's admins. You are told here when one of them decides." : "",
                waits > 0 ? "\(plural(waits, "line")) \(waits == 1 ? "needs" : "need") an admin and \(waits == 1 ? "was" : "were") not applied." : ""]
            .filter { !$0.isEmpty }.joined(separator: " ")
    }
}
