import Foundation

// What the app's chrome for the workspace agent says, and the rules it
// decides by — a port of the web chrome's /vendor/agent-chrome-words.js
// (D232), so the app and the dock say the same things from the same
// answers of xbind (D233). Pure; DelegationWordsTests holds it to the JS
// with vectors made by native/tools/delegation-words.mjs.
//
// Everything is said from xbind's own reading: a request's `preview`, a
// change set's `line` / `before` / `after` / `warnings` / `blocked`, the
// audit rows. The agent's own text (`summary`, `why`, a set's `title`) is
// never mixed into a sentence: the views quote it, labelled as unchecked.

/// The design's recommendations where the owner has not decided otherwise
/// (the UX plan's open questions) — the web's `CHOICES`, and the app's own.
public enum AgentChoices {
    /// Allow is not armed before this long after the sheet is fully shown.
    public static let armSeconds: Double = 0.6
    /// A standing choice's default duration: until taken back for the exact
    /// action, one hour for the whole tile (Q5).
    public static func duration(for choice: GrantChoice.ID) -> String { choice == .tile ? "hour" : "always" }
    /// A change with a warning starts unticked (Q7).
    public static let warnedStartUnticked = true
    /// While paused the composer is off: one meaning for pause (Q10).
    public static let typeWhilePaused = false
    /// "An admin can fix this: A or B" (Q15).
    public static let adminsNamed = 2
    // The app's own (D233):
    /// The agent opens as a sheet, large first (Q1); medium keeps the tile usable.
    public static let sheetOpensLarge = true
    /// An iPad shows it as a trailing column — no sidebar is built for it (Q2).
    public static let iPadColumnFraction: Double = 0.4
    /// The wider grant and applying admin changes ask Face ID, as a local
    /// gate: it never counts as the server's recent sign-in (Q4).
    public static let biometricsForWider = true
    public static let biometricsForAdminChanges = true
    /// From the lock screen: Review and Deny only (Q3) — notification
    /// actions are not part of this stage.
    public static let allowFromLockScreen = false
}

/// When a control that grants or widens may be pressed (the web chrome's
/// arming rule, docs/protocol/delegation.md §The chrome): not for
/// ``AgentChoices/armSeconds`` after it appears, and not while something
/// covers it or the app is not in front. Pure: the view keeps the timer.
public struct ArmingClock: Sendable, Equatable {
    /// When what it guards was last shown, uncovered, with the app in
    /// front; nil while it is covered or the app is elsewhere.
    public private(set) var since: Date?

    public init() {}

    /// It appeared, was uncovered, the app came back, or what the control
    /// means changed: the wait starts again.
    public mutating func shown(at now: Date) { since = now }
    /// Something covers it, or the app left the front.
    public mutating func covered() { since = nil }

    public func isArmed(at now: Date, delay: Double = AgentChoices.armSeconds) -> Bool {
        guard let since else { return false }
        return now.timeIntervalSince(since) >= max(delay, AgentChoices.armSeconds)
    }

    /// How long until it arms (0: armed; nil: never while covered).
    public func remaining(at now: Date, delay: Double = AgentChoices.armSeconds) -> Double? {
        guard let since else { return nil }
        return max(0, max(delay, AgentChoices.armSeconds) - now.timeIntervalSince(since))
    }
}

/// What WIDENS what the agent may do — the controls that are armed like
/// Allow. Narrowing is never held up.
public enum AgentWidening {
    /// Choosing a grant: once < always this exact action < every change.
    public static func widens(from: GrantChoice.ID, to: GrantChoice.ID) -> Bool { rank(to) > rank(from) }
    private static func rank(_ c: GrantChoice.ID) -> Int {
        switch c {
        case .once: return 0
        case .call, .tool: return 1
        case .tile: return 2
        }
    }

    /// A standing approval's duration: "until I take it back" is wider than
    /// "for one hour".
    public static func widens(fromDuration a: String, toDuration b: String) -> Bool { a != "always" && b == "always" }

    /// Starting tile agents: "allow" (without asking) is wider than "ask".
    public static func widens(fromStart a: String, toStart b: String) -> Bool { a != "allow" && b == "allow" }

    /// A switch that gives the agent more when on (the admin opt-in, other
    /// tiles' APIs) — or when off (`widerWhenOn: false`: "its model only, no
    /// internet").
    public static func widens(from a: Bool, to b: Bool, widerWhenOn: Bool = true) -> Bool { a != b && b == widerWhenOn }

    /// Whether a change goes through now: narrowing always, widening only armed.
    public static func takes(widening: Bool, armed: Bool) -> Bool { !widening || armed }
}

/// One way to allow a tile call.
public struct GrantChoice: Sendable, Equatable, Identifiable {
    public enum ID: String, Sendable { case once, call, tile, tool }
    public var id: ID
    public var label: String
    public var text: String
    public var wider: Bool
}

/// The heading of an approval: the tile's name is the strong part.
public struct ApprovalHeading: Sendable, Equatable {
    public var before: String
    public var strong: String
    public var after: String
    public var plain: String { before + strong + after }
}

public enum DelegationWords {
    // MARK: Small words

    /// "10:14" in `zone`'s time.
    public static func clock(_ d: Date, zone: TimeZone = .current) -> String {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = zone
        let c = cal.dateComponents([.hour, .minute], from: d)
        return String(format: "%02d:%02d", c.hour ?? 0, c.minute ?? 0)
    }

    /// How long from now: "23 h 58 min", "7 days", "4 min", "less than a minute".
    public static func left(_ seconds: TimeInterval) -> String {
        let m = Int((seconds / 60).rounded(.down))
        if m < 1 { return "less than a minute" }
        if m < 60 { return "\(m) min" }
        let h = m / 60
        if h < 48 { return m % 60 != 0 ? "\(h) h \(m % 60) min" : "\(h) h" }
        return "\(h / 24) days"
    }

    /// "asked 10:14 · expires in 23 h 58 min" (what of it is known).
    public static func asked(created: Date?, expires: Date?, now: Date = Date(), zone: TimeZone = .current) -> String {
        var parts: [String] = []
        if let created { parts.append("asked \(clock(created, zone: zone))") }
        if let expires { parts.append(expires > now ? "expires in \(left(expires.timeIntervalSince(now)))" : "expired") }
        return parts.joined(separator: " · ")
    }

    /// "48 bytes", "14 KiB", "3.2 MiB".
    public static func bytes(_ n: Int) -> String {
        if n < 1024 { return "\(n) byte\(n == 1 ? "" : "s")" }
        if n < 1024 * 1024 { return "\(trim(Double(n) / 1024, digits: n < 10240 ? 1 : 0)) KiB" }
        return "\(trim(Double(n) / 1_048_576, digits: 1)) MiB"
    }

    /// JavaScript's `+(x).toFixed(d)`: fixed, then without trailing zeros.
    static func trim(_ x: Double, digits: Int) -> String {
        var s = String(format: "%.\(digits)f", x)
        if s.contains(".") {
            while s.hasSuffix("0") { s.removeLast() }
            if s.hasSuffix(".") { s.removeLast() }
        }
        return s
    }

    static func plural(_ n: Int, _ one: String) -> String { "\(n) \(n == 1 ? one : one + "s")" }

    /// A tile's name from its path: the last segment, capitalised.
    public static func lastOf(_ path: String) -> String {
        guard let s = path.split(separator: "/", omittingEmptySubsequences: true).last, let f = s.first else { return "" }
        return f.uppercased() + s.dropFirst()
    }

    /// The tile a request is about: its name, path and org.
    public static func tile(_ r: DelegateRequest) -> (name: String, path: String, org: String) {
        let t = r.preview["tile"] ?? r.preview["session"]?["tile"]
        if let t, t.objectValue != nil {
            let path = t["path"]?.stringValue ?? ""
            let name = t["name"]?.stringValue ?? t["title"]?.stringValue ?? ""
            return (name.isEmpty ? lastOf(path) : name, path, t["org"]?.stringValue ?? "")
        }
        let path = t?.stringValue ?? r.tilePath
        return (lastOf(path), path, "")
    }

    /// The person's own level on the tile, in words.
    public static func levelWords(_ level: String) -> String {
        switch level {
        case "owner": return "you own it"
        case "admin": return "you manage it"
        case "terminal", "write", "full": return "you can write here"
        case "read": return "you can only read here"
        default: return level.isEmpty ? "" : "your level: \(level)"
        }
    }

    // MARK: A request

    /// A request as a row of Needs you.
    public static func needTitle(_ r: DelegateRequest) -> String {
        let t = tile(r).name.isEmpty ? "a tile" : tile(r).name
        switch r.kind {
        case .session: return r.isResume ? "Continue the tile agent in \(t)" : "Start a tile agent in \(t)"
        case .template: return "Create a tile"
        case .call: return "Approve a change to \(t)"
        case .web, .mcp, .unknown: return netNeedTitle(r)
        }
    }

    public static func heading(_ r: DelegateRequest) -> ApprovalHeading {
        let t = tile(r).name.isEmpty ? "a tile" : tile(r).name
        switch r.kind {
        case .session:
            return r.isResume ? ApprovalHeading(before: "Continue the tile agent in ", strong: t, after: "?")
                              : ApprovalHeading(before: "Start a tile agent in ", strong: t, after: "?")
        case .template: return ApprovalHeading(before: "Your agent wants to create a tile", strong: "", after: "")
        case .call: return ApprovalHeading(before: "Your agent wants to change something in ", strong: t, after: "")
        case .web, .mcp, .unknown: return netHeading(r)
        }
    }

    /// The most of a body or a brief a sheet shows before Show all, in
    /// characters (Unicode scalars, as the web counts code points) — one
    /// length for both chromes.
    public static let clip = 4000

    /// `text` as a sheet shows it: all of it, or its first ``clip``
    /// characters (`all`: the person asked for all of it).
    public static func clipText(_ text: String, all: Bool = false) -> (text: String, clipped: Bool, shown: Int, total: Int) {
        let total = text.unicodeScalars.count
        guard !all, total > clip else { return (text, false, total, total) }
        return (String(String.UnicodeScalarView(text.unicodeScalars.prefix(clip))), true, clip, total)
    }

    /// The note under clipped text: "first 4000 of 9321 characters".
    public static func clipNote(_ c: (text: String, clipped: Bool, shown: Int, total: Int)) -> String {
        c.clipped ? "first \(c.shown) of \(c.total) characters" : ""
    }

    /// What the call carries: the body as text with how much of it is
    /// shown — "shown in full" only when it is — or, for bytes nobody can
    /// read, no text and its size and hash.
    public static func sends(_ r: DelegateRequest, all: Bool = false) -> (text: String, note: String, binary: Bool, clipped: Bool) {
        let pb = r.preview["body"]
        if r.bodyBinary || pb?["binary"]?.boolValue == true {
            let sha = pb?["sha256"]?.stringValue ?? r.bodySha256
            let size = Int(pb?["size"]?.intValue ?? 0)
            return ("", "not text — \(bytes(size > 0 ? size : r.bodySize))\(sha.isEmpty ? "" : ", sha256 \(sha)")", true, false)
        }
        if r.bodySize == 0, r.body.isEmpty { return ("", "nothing: the call has no body", false, false) }
        // (xbind's bodyShown is a boolean: `body` is the whole body, as text)
        let c = clipText(r.body, all: all)
        return (c.text, "\(bytes(r.bodySize)) · \(c.clipped ? clipNote(c) : "shown in full")", false, c.clipped)
    }

    // MARK: The agent's text, as a sheet shows it

    /// One part of the agent's text: drawn as it is, or a character never
    /// drawn as it is — its escape, "U+202E".
    public enum RevealPart: Sendable, Equatable {
        case text(String)
        case hidden(String)
    }

    /// The line above text that holds a hidden character.
    public static let hiddenNote = "contains hidden characters — shown as ⟨U+…⟩"

    /// Whether `c` is never drawn as it is in the agent's text: a control
    /// character but newline and tab, or any format character (Cf) — the
    /// bidi embeddings, overrides and isolates, the zero-width ones, the
    /// word joiner, the byte order mark (the web's `reveal`).
    public static func isHidden(_ c: Unicode.Scalar) -> Bool {
        if c == "\n" || c == "\t" { return false }
        switch c.properties.generalCategory {
        case .control, .format: return true
        default: return false
        }
    }

    /// The agent's text in parts: runs drawn as they are between the hidden
    /// characters, each of those a part of its own.
    public static func reveal(_ text: String) -> (parts: [RevealPart], hidden: Int) {
        var parts: [RevealPart] = []
        var run = String.UnicodeScalarView()
        var n = 0
        for c in text.unicodeScalars {
            guard isHidden(c) else { run.append(c); continue }
            if !run.isEmpty { parts.append(.text(String(run))); run = String.UnicodeScalarView() }
            let hex = String(c.value, radix: 16, uppercase: true)
            parts.append(.hidden("U+" + String(repeating: "0", count: max(0, 4 - hex.count)) + hex))
            n += 1
        }
        if !run.isEmpty { parts.append(.text(String(run))) }
        return (parts, n)
    }

    /// The text with each hidden character as its escape, ⟨U+202E⟩ — what
    /// VoiceOver reads.
    public static func revealed(_ text: String) -> String {
        reveal(text).parts.map { p in
            switch p {
            case .text(let t): return t
            case .hidden(let code): return "⟨\(code)⟩"
            }
        }.joined()
    }

    public static func undoWords(_ r: DelegateRequest) -> String {
        let t = tile(r).name.isEmpty ? "the tile" : tile(r).name
        return "xbin can't undo this. Whether \(t) can is up to \(t)."
    }

    /// The three ways to allow a tile call; a session start and a tile
    /// create have none (they are asked every time).
    public static func grantChoices(_ r: DelegateRequest) -> [GrantChoice] {
        if r.kind == .mcp { return mcpChoices(r) }
        guard r.kind == .call else { return [] }
        let t = tile(r).name.isEmpty ? "this tile" : tile(r).name
        let st = r.preview["standing"]
        let call = st?["call"]?.stringValue ?? ""
        let whole = st?["tile"]?.stringValue ?? ""
        return [
            GrantChoice(id: .once, label: "Once", text: "This request only. The agent has 15 minutes to use it.", wider: false),
            GrantChoice(id: .call, label: "Always, this exact action",
                        text: call.isEmpty ? "\(r.method) to this path on \(t), whatever it sends, without asking again." : call, wider: false),
            GrantChoice(id: .tile, label: "Always, every change to \(t)",
                        text: whole.isEmpty ? "Any create, change or delete in \(t) that you could do yourself, without asking again." : whole, wider: true),
        ]
    }

    /// How long a standing approval lasts — the contract's `hour` and `always`.
    public static let durations: [(id: String, label: String)] = [("hour", "For one hour"), ("always", "Until I take it back")]

    /// The Allow button's words — they follow the choice.
    public static func allowLabel(_ r: DelegateRequest, choice: GrantChoice.ID = .once) -> String {
        switch r.kind {
        case .session: return r.neverAsks ? "Start without asking" : "Start"
        case .template: return "Create the tile"
        case .web, .mcp, .unknown: return netAllowLabel(r, choice: choice)
        case .call:
            switch choice {
            case .once: return "Allow once"
            case .call: return "Allow always"
            case .tile, .tool: return "Allow every change to \(tile(r).name.isEmpty ? "the tile" : tile(r).name)"
            }
        }
    }

    /// …and as VoiceOver says them.
    public static func allowSpoken(_ r: DelegateRequest, choice: GrantChoice.ID = .once) -> String {
        if r.kind == .mcp { return choice == .tool ? "Allow always, this tool" : "Allow once" }
        guard r.kind == .call else { return allowLabel(r, choice: choice) }
        return choice == .call ? "Allow always, this exact action" : allowLabel(r, choice: choice)
    }

    /// What `POST …/approve` sends: `{grant, scope?, unseen?}`. A body
    /// nobody could read is approved only by saying so.
    public static func approveBody(_ r: DelegateRequest, choice: GrantChoice.ID = .once, duration: String = "") -> JSONValue {
        var o: [String: JSONValue] = [:]
        if sends(r).binary { o["unseen"] = true }
        let tool = r.kind == .mcp && choice == .tool // an MCP tool: always, this tool (D239)
        if !tool, r.kind != .call || choice == .once {
            o["grant"] = "once"
            return .object(o)
        }
        o["grant"] = .string(duration == "hour" || duration == "always" ? duration : AgentChoices.duration(for: choice))
        o["scope"] = tool ? "tool" : choice == .tile ? "tile" : "call"
        return .object(o)
    }

    /// The second step of the wider grant.
    public static func widerWords(_ r: DelegateRequest) -> (heading: String, means: String, ends: String, back: String, allow: String) {
        let t = tile(r).name.isEmpty ? "this tile" : tile(r).name
        return ("Allow every change to \(t)?",
                "From now on your agent can create, change and delete anything in \(t) that you can, without asking.",
                "It ends by itself after the time you pick. You can take it back sooner in Approvals.",
                "Go back", "Allow every change")
    }

    /// A tile agent's start as the sheet's rows: the mode leads; one that
    /// never asks is the danger wording, and Deny becomes the primary button.
    public struct SessionRows: Sendable, Equatable {
        public var agent: String
        public var modeTag: String
        public var modeAsks: Bool
        public var modeText: String
        public var modeName: String
        public var network: String
        public var others: String
        public var brief: String
        public var danger: String
        public var comes: String
        /// What the session would sign in with, when that is the person's
        /// to know before allowing (D230): "Uses your saved … sign-in" with
        /// `signInHolds` — who will hold it; approving is what puts it into
        /// the session — or "Needs your … sign-in". "" otherwise.
        public var signIn: String = ""
        public var signInHolds: String = ""
    }

    public static func sessionRows(_ r: DelegateRequest) -> SessionRows {
        let s = r.session
        let t = tile(r).name.isEmpty ? "the tile" : tile(r).name
        let asks = s["asks"]?.boolValue == true
        let who = nonEmpty(s["providerName"]?.stringValue) ?? nonEmpty(s["provider"]?.stringValue) ?? "A coding agent"
        let modelName = s["model"]?.stringValue ?? ""
        let model = modelName.isEmpty ? "" : " · model \(modelName)"
        let credentials = s["credentials"]?.stringValue ?? ""
        let signIn = s["signIn"]?.stringValue ?? ""
        let saved = signIn == "saved" && !(s["signInId"]?.stringValue ?? "").isEmpty
        let cred = signIn == "saved" || signIn == "needed" ? ""
            : credentials == "own" ? ", on your own sign-in"
            : credentials == "connector" || !modelName.isEmpty ? ", through the workspace's connector" : ""
        var brief = ""
        if let b = try? JSONValue(parsing: r.body.isEmpty ? "{}" : r.body), b.objectValue != nil {
            brief = b["prompt"]?.stringValue ?? b["brief"]?.stringValue ?? b["message"]?.stringValue ?? ""
        } else {
            brief = r.body
        }
        if brief.isEmpty { brief = s["prompt"]?.stringValue ?? "" }
        let net = s["net"]?.stringValue ?? ""
        return SessionRows(
            agent: "\(who)\(model)\(cred)",
            modeTag: asks ? "asks before acting" : "acts without asking", modeAsks: asks,
            modeText: asks ? "It asks you before it edits a file or runs a command."
                           : "It edits files and runs commands in \(t) without asking you.",
            modeName: s["mode"]?.stringValue ?? "",
            network: net.isEmpty || net == "none" ? "Its model only. No internet." : "Network: \(net)",
            others: s["api"]?.boolValue == false ? "It can call no other tile." : "It can call the tile APIs this tile is granted.",
            brief: brief,
            danger: asks ? "" : "Your workspace agent asked for a mode that never asks. You will not get a chance to stop a single edit or command.",
            comes: "Its questions and permission requests come to you, in \(t) and in Needs you. Your workspace agent can't answer them.",
            signIn: saved ? "Uses your saved \(who) sign-in"
                : signIn == "needed" && s["provider"]?.stringValue != nil ? "Needs your \(who) sign-in: without it the tile agent starts signed out." : "",
            signInHolds: saved ? "The tile's coding agent will hold that sign-in: it, and anything it runs in \(t), can read it." : "")
    }

    private static func nonEmpty(_ s: String?) -> String? { (s ?? "").isEmpty ? nil : s }

    /// A decided request in a few words.
    public static func settled(_ r: DelegateRequest, zone: TimeZone = .current) -> String {
        let at = r.decided.map { " · \(clock($0, zone: zone))" } ?? ""
        switch r.state {
        case .approved: return "Allowed\(r.grant == "once" || r.grant.isEmpty ? " once" : "") by you\(at)"
        case .used: return "Allowed by you, and used\(at)"
        case .denied: return "Denied by you\(at)"
        case .expired: return "Expired — not approved within 24 hours."
        case .withdrawn: return r.withdrawnBy == "xbin" ? "Withdrawn by xbin when your agent was stopped" : "Withdrawn by the agent"
        case .pending: return "Waiting for your approval"
        case .unknown: return unknownState.text
        }
    }

    /// A request or change set in a state this app doesn't know (a newer
    /// xbind's): never read as one that can be decided — no Allow, no
    /// Apply, and this line.
    public static let unknownState = (title: "A state this app doesn't know",
                                      text: "This app is older than the workspace — open it on the web.")

    // MARK: When nothing can be allowed

    /// Why the person can't ALLOW anything for their agent now, from
    /// `GET /delegate/state` — the refusal xbind answers Allow, Always and
    /// Apply with then: "workspace-off", "no-agent", "paused", "held"; ""
    /// when nothing stops it. Deny and Reject always work.
    public static func stopRefusal(_ state: DelegateState?) -> String {
        guard let state else { return "" }
        if !state.workspace { return "workspace-off" }
        if state.access == false { return "no-agent" }
        if state.paused { return "paused" }
        if !state.held.isEmpty { return "held" }
        return ""
    }

    /// What a sheet says in place of Allow (`changeset`: Apply) while the
    /// agent is stopped; nil for no refusal. A refusal this app doesn't
    /// know is said in general words.
    public static func stopWords(_ refusal: String, changeset: Bool = false) -> (title: String, text: String, still: String)? {
        guard !refusal.isEmpty else { return nil }
        let text: String
        switch refusal {
        case "workspace-off": text = "Workspace agents are turned off for this workspace, so nothing can be allowed until an admin turns them on again."
        case "paused": text = "Your agent is paused. Resume it first; then this can be allowed."
        case "held": text = "Your agent is held on this xbin right now, so nothing can be allowed."
        case "no-agent": text = "You no longer have a workspace agent, so nothing can be allowed for it."
        default: text = "Your agent can't act on this xbin right now, so nothing can be allowed."
        }
        return (changeset ? "Can't be applied now" : "Can't be allowed now", text, changeset ? "You can still reject it." : "You can still deny it.")
    }

    /// The whole line, as the sheet shows it.
    public static func stopLine(_ refusal: String, changeset: Bool = false) -> String {
        guard let w = stopWords(refusal, changeset: changeset) else { return "" }
        return "\(w.title). \(w.text) \(w.still)"
    }

    /// A request's card in the conversation (the app draws it for a
    /// `request` node): a glyph, a tone, a head line and a quiet one.
    public struct RequestCard: Sendable, Equatable {
        public var head: String
        public var what: String
        public var line: String
        /// An icon of the vocabulary: shield, clock, check, xmark.
        public var glyph: String
        /// "", warn, ok.
        public var tone: String
        public var waits: Bool
        /// "Review…" while it waits, "" after.
        public var button: String
    }

    public static func requestCard(_ r: DelegateRequest, now: Date = Date(), zone: TimeZone = .current) -> RequestCard {
        let what = needTitle(r)
        switch r.kind == .unknown ? .unknown : r.state { // a kind this app doesn't know is never one to decide
        case .pending:
            return RequestCard(head: "Waiting for your approval", what: what,
                               line: asked(created: r.created, expires: r.expires, now: now, zone: zone),
                               glyph: "shield", tone: "warn", waits: true, button: "Review…")
        case .approved:
            return RequestCard(head: settled(r, zone: zone), what: what, line: "running now…", glyph: "clock", tone: "", waits: false, button: "")
        case .used:
            return RequestCard(head: settled(r, zone: zone), what: what, line: r.kind == .call ? "\(r.method) \(r.target)" : r.kind == .web ? webOf(r).url : "",
                               glyph: "check", tone: "ok", waits: false, button: "")
        case .denied, .withdrawn:
            return RequestCard(head: settled(r, zone: zone), what: what, line: "", glyph: "xmark", tone: "", waits: false, button: "")
        case .expired:
            return RequestCard(head: settled(r, zone: zone), what: what, line: "", glyph: "clock", tone: "", waits: false, button: "")
        case .unknown:
            return RequestCard(head: unknownState.title, what: what, line: unknownState.text, glyph: "info", tone: "", waits: false, button: "")
        }
    }

    /// A change set's card in the conversation.
    public static func changeSetCard(_ c: AdminChangeSet, now: Date = Date()) -> RequestCard {
        let w = needChangeTitle(c)
        switch c.state {
        case .proposed:
            let exp = c.expires.map { $0 > now ? "expires in \(left($0.timeIntervalSince(now)))" : "expired" } ?? ""
            return RequestCard(head: w.title, what: "", line: [w.note, exp].filter { !$0.isEmpty }.joined(separator: " · "),
                               glyph: "shield", tone: "warn", waits: true, button: "Review…")
        case .applying: return RequestCard(head: w.title, what: "", line: "applying now…", glyph: "clock", tone: "", waits: false, button: "")
        case .applied, .partial:
            return RequestCard(head: resultsWords(c).title, what: "", line: "", glyph: c.state == .applied ? "check" : "warning",
                               tone: c.state == .applied ? "ok" : "warn", waits: false, button: "Results")
        case .rejected: return RequestCard(head: "Rejected by you", what: "", line: "", glyph: "xmark", tone: "", waits: false, button: "")
        case .expired: return RequestCard(head: "Expired — not approved within 7 days.", what: "", line: "", glyph: "clock", tone: "", waits: false, button: "")
        case .withdrawn:
            return RequestCard(head: c.withdrawnBy == "xbin" ? "Withdrawn by xbin when your agent was stopped" : "Withdrawn by the agent",
                               what: "", line: "", glyph: "xmark", tone: "", waits: false, button: "")
        case .unknown:
            return RequestCard(head: unknownState.title, what: "", line: unknownState.text, glyph: "info", tone: "", waits: false, button: "")
        }
    }

    // MARK: Standing approvals

    public struct StandingRow: Sendable, Equatable {
        public var tile: String
        public var text: String
        public var wider: Bool
        public var until: String
    }

    public static func standingRow(_ a: StandingApproval, now: Date = Date()) -> StandingRow {
        let name = lastOf(a.tile)
        return StandingRow(
            tile: name.isEmpty ? a.tile : name,
            text: a.scope == "tile" ? "Always · every change" : a.scope == "tool" ? "Always · this tool: \(a.path)"
                : "Always · this action: \(a.method) \(a.path)".trimmingCharacters(in: .whitespaces),
            wider: a.scope == "tile",
            until: a.until.map { $0 > now ? "ends in \(left($0.timeIntervalSince(now)))" : "ended" } ?? "until you take it back")
    }

    public static let standingNote = "Starting a tile agent, creating a tile and reading a web page are asked every time. Taking an approval back stops a change that is running under it."

    // MARK: Change sets

    /// "grant" · "binding" · "org share" · "limit".
    public static func kindTag(_ kind: String) -> String {
        switch kind {
        case "grant.add", "grant.revoke": return "grant"
        case "binding.set", "binding.remove": return "binding"
        case "org.share": return "org share"
        case "limits.set": return "limit"
        case "tile.create": return "new tile"
        case "agent.start": return "tile agent"
        default: return kind.split(separator: ".").first.map(String.init) ?? ""
        }
    }

    /// xbind's warning as a caps tag, an icon and its sentence
    /// ("PUBLIC: anyone on the internet…").
    public static func warning(_ w: String) -> (tag: String, icon: String, text: String) {
        var tag = "WARNING"
        var text = w
        if let colon = w.firstIndex(of: ":") {
            let head = String(w[..<colon])
            let ok = head.count >= 2 && head.count <= 21 && (head.first?.isUppercase ?? false) && (head.first?.isASCII ?? false)
                && head.allSatisfy { ($0.isASCII && $0.isUppercase) || $0 == " " }
            if ok {
                tag = head.trimmingCharacters(in: .whitespaces)
                text = String(w[w.index(after: colon)...]).trimmingCharacters(in: .whitespaces)
            }
        }
        let icons = ["PUBLIC": "globe", "OPEN PORT": "network", "EGRESS": "external"]
        return (tag, icons[tag] ?? "warning", text)
    }

    public static func tickable(_ item: AdminChangeSet.Item) -> Bool { item.blocked.isEmpty }

    /// The items ticked when the review opens: never a blocked one, never
    /// (Q7) one with a warning.
    public static func initialTicks(_ c: AdminChangeSet) -> [Int] {
        c.items.filter { tickable($0) && !(AgentChoices.warnedStartUnticked && !$0.warnings.isEmpty) }.map(\.n)
    }

    public static func reviewWords(_ c: AdminChangeSet, ticked: Int) -> (heading: String, intro: String, footer: String, apply: String) {
        let total = c.items.count
        return ("Your agent proposes \(plural(total, "admin change"))",
                "Each line, its before and after and its warnings are written by xbin from the change itself. Nothing has been applied.",
                "\(ticked) of \(total) ticked · applied in order as you. If one fails, later ones are skipped and earlier ones stay.",
                ticked > 0 ? "Apply \(plural(ticked, "change"))…" : "Apply none")
    }

    public static func needChangeTitle(_ c: AdminChangeSet) -> (title: String, note: String) {
        let w = c.items.filter { !$0.warnings.isEmpty }.count
        let b = c.items.filter { !$0.blocked.isEmpty }.count
        let na = c.items.filter { $0.needsAdmin && $0.result.isEmpty }.count
        let note = [w > 0 ? "\(w) \(w == 1 ? "has" : "have") a warning" : "", b > 0 ? "\(b) can't be applied" : "",
                    na > 0 ? "\(na) \(na == 1 ? "needs" : "need") an admin" : ""].filter { !$0.isEmpty }
        // a plan, or another person's request to an admin, says which it is
        let title = !c.from.isEmpty ? "\(c.from) asks for \(plural(c.items.count, "admin change"))"
            : c.plan ? "A plan of \(plural(c.items.count, "change"))" : "\(plural(c.items.count, "admin change")) proposed"
        return (title, note.joined(separator: " · "))
    }

    /// An item's result: a word, an icon, a tone, and a sentence where one
    /// is due ("xbin said: …" for a failure).
    public static func resultOf(_ item: AdminChangeSet.Item) -> (word: String, icon: String, tone: String, text: String) {
        switch item.result {
        case "applied": return ("applied", "check", "ok", "")
        case "failed": return ("failed", "error", "danger", "xbin said: “\(item.error.isEmpty ? "it failed" : item.error)”")
        case "skipped": return ("skipped", "minus", "", "")
        case "stale":
            return ("out of date", "refresh", "warn",
                    "Someone changed this after the agent proposed it. What you ticked no longer matches, so xbin left it alone.")
        case "rejected": return ("left out by you", "minus", "", "")
        case "blocked": return ("couldn't be applied", "lock", "warn", item.blocked.isEmpty ? item.error : item.blocked)
        case "asked": return ("sent to an admin", "clock", "", "")
        default: return item.needsAdmin && item.result.isEmpty ? ("needs an admin", "lock", "", "") : (item.result, "", "", "")
        }
    }

    private static let numbers = ["none", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"]
    private static func num(_ n: Int) -> String { numbers.indices.contains(n) ? numbers[n] : String(n) }

    /// The results' title, the banner of a partial outcome, and the Undo
    /// button ("" when nothing can be undone).
    public static func resultsWords(_ c: AdminChangeSet) -> (title: String, banner: String, undo: String) {
        let chosen = c.items.filter { ["applied", "failed", "skipped", "stale"].contains($0.result) }
        let applied = c.items.filter { $0.result == "applied" }
        let failed = chosen.filter { $0.result == "failed" }.count
        let skipped = chosen.filter { $0.result == "skipped" }.count
        let title = applied.isEmpty ? "Nothing was applied"
            : applied.count == chosen.count ? "All \(applied.count) change\(applied.count == 1 ? "" : "s") applied"
            : "\(applied.count) of \(chosen.count) changes applied"
        var banner = ""
        if failed > 0 {
            let n = num(failed)
            banner = "\(n.prefix(1).uppercased() + n.dropFirst()) change\(failed == 1 ? "" : "s") failed"
            if skipped > 0 {
                banner += ", so the \(skipped == 1 ? "one" : num(skipped)) after \(failed == 1 ? "it" : "them") \(skipped == 1 ? "was" : "were") skipped"
            }
            banner += "."
            if !applied.isEmpty {
                banner += " The \(applied.count == 1 ? "one that was" : "\(num(applied.count)) that were") applied \(applied.count == 1 ? "stays" : "stay")."
            }
        }
        let undoable = applied.filter(\.hasInverse).count
        return (title, banner, undoable > 0 ? "Undo the \(undoable) applied…" : "")
    }

    /// How a set that was not applied ended ("" for one applied, in part or
    /// whole, or still waiting).
    public static func endedWords(_ c: AdminChangeSet) -> String {
        switch c.state {
        case .rejected: return "Rejected by you. Nothing was applied."
        case .expired: return "Expired. Nothing was applied."
        case .withdrawn:
            return c.withdrawnBy == "xbin" ? "Withdrawn by xbin when your agent was stopped. Nothing was applied." : "Withdrawn by the agent. Nothing was applied."
        case .unknown: return unknownState.text
        default: return ""
        }
    }

    /// The recent-sign-in step, as the server asked for it.
    public static func stepUp(_ kind: String) -> (heading: String, text: String, action: String, password: Bool) {
        kind == "signin"
            ? ("Sign in again first.", "Applying admin changes needs a sign-in from the last ten minutes.", "Sign in again", false)
            : ("Confirm it's you", "Applying admin changes needs your password, or a sign-in from the last ten minutes.", "Continue", true)
    }

    // MARK: What my agent did

    public struct Did: Sendable, Equatable {
        public var text: String
        /// The tile's name inside the sentence, said after `text`.
        public var strong: String
        public var detail: String
        public var icon: String
        /// change | refused | read.
        public var group: String
        public var at: Date?
    }

    private static func grantWords(_ under: String) -> String {
        let parts = under.split(separator: " ").map(String.init)
        let g = ["once": "once", "hour": "for one hour", "always": "until you take it back"][parts.first ?? ""]
        guard let g else { return "" }
        let sc = parts.count > 1 ? parts[1] : ""
        return g + (sc == "tile" ? ", every change" : sc == "call" ? ", this action" : "")
    }

    static func underWords(_ row: AuditRow) -> String {
        let u = row.under
        if u == "preference" { return "without asking: your preference" }
        if u.hasPrefix("approval:") { return "under a standing approval of yours" }
        if !u.isEmpty { return "under “always, \(u == "tile" ? "every change" : "this action")”" }
        return row.ticket.isEmpty ? "" : "you allowed it once"
    }

    private static func asking(_ row: AuditRow) -> String {
        let prefix = "/api/xbin/delegate/"
        guard row.path.hasPrefix(prefix), !row.dryRun, !row.method.isEmpty, row.method != "GET" else { return "" }
        let tail = row.path.dropFirst(prefix.count)
        let what = tail.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? ""
        guard ["requests", "changesets", "sessions"].contains(what) else { return "" }
        let rest = tail.count > what.count
        if what == "sessions" {
            return row.method == "POST" && !rest ? "Began a session" : row.method == "DELETE" ? "Ended a session" : "Updated its session's record"
        }
        if row.method == "POST", !rest { return what == "requests" ? "Asked for your approval" : "Proposed admin changes" }
        return row.method == "DELETE" ? (what == "requests" ? "Withdrew a request" : "Withdrew proposed admin changes") : ""
    }

    /// One row of the audit log as a sentence.
    public static func did(_ row: AuditRow) -> Did {
        let t = lastOf(row.tile)
        let call = "\(row.method) \(row.path)".trimmingCharacters(in: .whitespaces)
        if row.cls.hasPrefix("decision:") {
            let d = row.cls.dropFirst(9)
            let text = d.hasPrefix("den") ? "You denied a request" : d.hasPrefix("revoke") ? "You took an approval back" : "You allowed a request"
            return Did(text: text, strong: "", detail: [t, grantWords(row.under)].filter { !$0.isEmpty }.joined(separator: " · "),
                       icon: "person", group: "change", at: row.ts)
        }
        if let d = netDid(row) { return d }
        if row.cls.hasPrefix("changeset:") {
            return Did(text: "Applied an admin change as you", strong: "",
                       detail: kindTag(String(row.cls.dropFirst(10))) + (row.status >= 400 ? " · failed" : ""),
                       icon: "shield", group: "change", at: row.ts)
        }
        if !row.refused.isEmpty {
            return Did(text: "Was refused: ", strong: t.isEmpty ? call : t, detail: row.refused, icon: "lock", group: "refused", at: row.ts)
        }
        let under = underWords(row)
        if row.cls == "session" || row.path.hasSuffix("/term/sessions") {
            return Did(text: "Started a tile agent in ", strong: t, detail: under, icon: "agent", group: "change", at: row.ts)
        }
        let own = asking(row)
        if !own.isEmpty {
            return Did(text: own, strong: "", detail: "", icon: own.contains("session") ? "agent" : "shield", group: "read", at: row.ts)
        }
        if !row.method.isEmpty, row.method != "GET", !row.dryRun {
            return Did(text: "Changed ", strong: t.isEmpty ? call : t, detail: [call, under].filter { !$0.isEmpty }.joined(separator: " · "),
                       icon: "pencil", group: "change", at: row.ts)
        }
        return Did(text: row.dryRun ? "Checked what a change would do in " : "Read ", strong: t.isEmpty ? call : t,
                   detail: call + (row.n > 0 ? " · \(row.n) more before this weren't listed" : ""), icon: "eye", group: "read", at: row.ts)
    }

    public static let didFilters: [(id: String, label: String)] = [("change", "Changes"), ("refused", "Refused"), ("all", "Everything")]
    public static let didNote = "Kept by xbin for 13 months. Your agent and its page can't read or change this list. It never holds what was sent, only what was called."

    // MARK: The agent's own state

    /// What the app shows in place of the agent's view, or `.page`.
    public enum Dock: String, Sendable { case hidden, setup, unavailable, off, paused, held, offline, soon, page }

    /// The web chrome's `dockState`: `llmConfigured` nil = not read (taken
    /// as connected), `admin` the person is a workspace admin.
    public static func dock(state: DelegateState?, llmConfigured: Bool?, admin: Bool, unreachable: Bool = false) -> Dock {
        guard let state else { return unreachable ? .offline : .hidden }
        if unreachable { return .offline }
        let wsAdmin = admin || state.who != nil
        let anyAdmin = wsAdmin || state.canAdminChanges
        if state.access == false, !wsAdmin { return .hidden }
        if !state.workspace { return state.workspaceAt != nil ? .off : anyAdmin ? .setup : .hidden }
        if let who = state.who, !who.chosen { return .setup }
        // an admin who gave others one and not themselves: administering it isn't having one
        if state.access == false { return .hidden }
        if llmConfigured == false { return .unavailable }
        if state.paused { return .paused }
        if !state.held.isEmpty { return .held }
        return state.agent.isEmpty ? .soon : .page
    }

    /// The entry button: whether it shows, what VoiceOver says, its count.
    public struct Chip: Sendable, Equatable {
        public var shown: Bool
        public var spoken: String
        public var count: Int
        public var paused: Bool
        public var setup: Bool
    }

    public static func chip(_ dock: Dock, count: Int) -> Chip {
        switch dock {
        case .hidden: return Chip(shown: false, spoken: "", count: 0, paused: false, setup: false)
        case .setup: return Chip(shown: true, spoken: "Set up the workspace agent", count: 0, paused: false, setup: true)
        case .paused: return Chip(shown: true, spoken: "Workspace agent, paused", count: count, paused: true, setup: false)
        default:
            return Chip(shown: true, spoken: count > 0 ? "Workspace agent, \(count) need\(count == 1 ? "s" : "") you" : "Workspace agent",
                        count: count, paused: false, setup: false)
        }
    }

    /// Home's row under the agent's name: "working · 2 tile agents · 2 need
    /// you", "idle", "paused".
    public static func homeStatus(_ dock: Dock, tileAgents: Int, working: Int, count: Int) -> String {
        switch dock {
        case .paused: return "paused"
        case .setup: return "not set up yet"
        case .off: return "turned off for this workspace"
        case .unavailable, .held, .soon: return "not available right now"
        case .offline: return "can't reach xbin"
        case .hidden: return ""
        case .page:
            var parts = [working > 0 ? "working" : "idle"]
            if tileAgents > 0 { parts.append(plural(tileAgents, "tile agent")) }
            if count > 0 { parts.append("\(count) need\(count == 1 ? "s" : "") you") }
            return parts.joined(separator: " · ")
        }
    }

    /// "Mira Okafor or Jonas Lind" (the first two, "and 3 more"); "an admin" when none are known.
    public static func admins(_ names: [String]) -> String {
        let n = names.filter { !$0.isEmpty }
        if n.isEmpty { return "an admin" }
        let shown = n.prefix(AgentChoices.adminsNamed).joined(separator: " or ")
        return n.count > AgentChoices.adminsNamed ? "\(shown) and \(n.count - AgentChoices.adminsNamed) more" : shown
    }

    /// §10's words for what shows in place of the conversation.
    public static func stateWords(_ dock: Dock, state: DelegateState?, admins names: [String] = [], zone: TimeZone = .current)
        -> (title: String, text: String, more: String)? {
        let known = names.contains { !$0.isEmpty }
        let fix = known ? "An admin can fix this: \(admins(names))." : "An admin can fix this."
        func at(_ d: Date?) -> String { d.map { " at \(clock($0, zone: zone))" } ?? "" }
        switch dock {
        case .unavailable:
            return ("Not available right now.",
                    "The workspace agent needs a language model, and this workspace has none connected at the moment. Your earlier conversations and the notebook are kept.", fix)
        case .off:
            return ("Turned off for this workspace.",
                    "An admin turned workspace agents off\(at(state?.workspaceAt)). Nothing an agent started is running on your behalf. Your conversations and notebook are kept for when it is turned on again.", "")
        case .paused:
            return ("Paused by you\(at(state?.pausedAt)).",
                    "Your agent can't read or change anything in the workspace until you resume. Tile agents that are already running keep going; their questions still come to you.", "")
        case .held:
            switch state?.held ?? "" {
            case "older":
                return ("Waiting for a newer xbin.",
                        "This workspace is running an older xbin than the one that last saved your agent's data. To keep that data safe the agent stays off until xbin is updated again. Nothing has been changed or removed.", "")
            case "unavailable":
                var why = state?.heldWhy ?? ""
                if why.hasSuffix(".") { why.removeLast() }
                return ("Not available on this xbin.",
                        "The workspace agent can't run here right now\(why.isEmpty ? "" : ": \(why)"). Your conversations and notebook are kept.", fix)
            default:
                return ("Locked after a restart.",
                        "Your agent's conversations and notebook are encrypted, and the workspace was restarted. They stay locked until an admin unlocks the workspace. Nothing is lost.", fix)
            }
        case .offline:
            return ("Can't reach xbin.", "Showing what was loaded before. Approvals need a connection.", "")
        case .soon:
            return ("Workspace agents are on.",
                    "Approvals, what your agent did and your preferences work here already. The agent's own conversation is not part of this xbin yet: it arrives with an update.", "")
        case .hidden, .setup, .page:
            return nil
        }
    }

    /// Who changes the switch that is on, and where — the app's own line
    /// under ``stateWords`` (the web's dock has the controls beside the
    /// words; the app has none of an admin's, so it says where they are).
    /// "": nothing to add (the person's own Resume is on the screen).
    public static func whoChanges(_ dock: Dock, state: DelegateState?) -> String {
        switch dock {
        case .off: return "A workspace admin turns workspace agents on again: on the web, in the workspace agent's setup."
        case .unavailable: return "A workspace admin connects a language model: on the web, under Language models."
        case .held:
            switch state?.held ?? "" {
            case "older": return "Whoever runs this workspace updates xbin; nothing here can."
            case "unavailable": return "Whoever runs this workspace changes how xbin is run; nothing here can."
            default: return "A workspace admin unlocks the workspace, on the web."
            }
        case .offline: return "It comes back by itself once xbin can be reached."
        case .hidden, .setup, .paused, .soon, .page: return ""
        }
    }

    /// The three setup steps an admin sees, read-only in the app (setup is
    /// the web's: "Open the setup on the web").
    public static func setupSteps(state: DelegateState?, llmConfigured: Bool, hosted: Bool) -> [(title: String, text: String, done: Bool, tag: String)] {
        let on = state?.workspace ?? false
        let who = state?.who
        let chosen = who?.chosen ?? false
        let n = who?.entries ?? 0
        return [
            ("Connect a language model",
             llmConfigured ? (hosted ? "Your host provides the models and sets the limits; those can't be changed here." : "Connected.")
                           : "None is connected. Use a provider key, or a model your organisation runs itself.",
             llmConfigured, hosted ? "Set by your host" : ""),
            ("Turn workspace agents on",
             on ? "On. You can turn it off again at any time; every agent stops at once." : "Off. You can turn it off again at any time; every agent stops at once.",
             on, ""),
            ("Choose who gets one",
             chosen ? ((who?.everyone ?? false) ? "Everyone in this workspace." : "\(n) \(n == 1 ? "person or org" : "people and orgs") so far.")
                    : "Nobody has one yet. Everyone, or the people and orgs you pick. Orgs can add their own rules.",
             chosen, ""),
        ]
    }

    public static let setupIntro = (title: "Set up the workspace agent",
                                    text: "One agent per person that plans work across tiles and asks before anything changes. It is off until you finish these three steps. Nobody else sees it yet.")

    // MARK: A tile agent's card (a `session` node)

    public struct SessionCard: Sendable, Equatable {
        public var tile: String
        public var harness: String
        /// An icon of the vocabulary and a word.
        public var glyph: String
        public var word: String
        /// "", warn, ok, danger.
        public var tone: String
        public var line: String
        public var note: String
        /// "Open" or "Answer".
        public var button: String
        public var waits: Bool
    }

    /// What a tile agent's card says, from xbind's compact session row
    /// (the web page's `agentCard`, with the app's capitalisation left to
    /// the view): Answer when it waits for the person — only they can.
    public static func sessionCard(_ s: AgentSessionRow, tileName: String, harnessName: String) -> SessionCard {
        let harness = harnessName.isEmpty ? (s.provider.isEmpty ? "Coding agent" : s.provider) : harnessName
        var c = SessionCard(tile: tileName, harness: harness, glyph: "pause", word: "stopped", tone: "", line: s.last, note: "",
                            button: "Open", waits: false)
        if s.waits {
            c.glyph = "bell"; c.word = "waiting for you"; c.tone = "warn"; c.waits = true
            c.line = s.questions > 0 && s.pending == 0 ? "It asks you a question." : "It asks your permission."
            c.note = "Only you can answer."
            c.button = "Answer"
            return c
        }
        switch s.status {
        case "starting": c.glyph = "clock"; c.word = "starting"
        case "running", "cancelling": c.glyph = "clock"; c.word = "working"
        case "idle": c.glyph = "check"; c.word = "finished"; c.tone = "ok"
        case "error": c.glyph = "error"; c.word = "failed"; c.tone = "danger"
        case "ended", "exited":
            if s.endedBy == "restart" {
                c.glyph = "pause"; c.word = "stopped for the update"; c.tone = "warn"
                c.line = "It didn't finish. Its transcript is kept, and it can continue where it stopped."
            } else {
                c.glyph = "check"; c.word = "finished"; c.tone = "ok"
            }
        default: break
        }
        return c
    }
}

/// What waits for the person because of their agent — the rows the app's
/// inbox adds above the sessions (the web tray's `needs`), oldest first.
public struct AgentNeed: Sendable, Equatable, Identifiable {
    public enum Kind: String, Sendable { case request, changeset }
    public var kind: Kind
    public var id: String
    public var title: String
    public var note: String
    public var at: Date?

    public static func rows(requests: [DelegateRequest], changesets: [AdminChangeSet], now: Date = Date(), zone: TimeZone = .current) -> [AgentNeed] {
        var rows: [AgentNeed] = []
        for r in requests where r.isPending {
            rows.append(AgentNeed(kind: .request, id: r.id, title: DelegationWords.needTitle(r),
                                  note: DelegationWords.asked(created: r.created, expires: r.expires, now: now, zone: zone), at: r.created))
        }
        for c in changesets where c.isProposed {
            let w = DelegationWords.needChangeTitle(c)
            let exp = c.expires.flatMap { $0 > now ? "expires in \(DelegationWords.left($0.timeIntervalSince(now)))" : nil } ?? ""
            rows.append(AgentNeed(kind: .changeset, id: c.id, title: w.title, note: [w.note, exp].filter { !$0.isEmpty }.joined(separator: " · "),
                                  at: c.created))
        }
        return rows.sorted { ($0.at ?? .distantPast) < ($1.at ?? .distantPast) }
    }
}
