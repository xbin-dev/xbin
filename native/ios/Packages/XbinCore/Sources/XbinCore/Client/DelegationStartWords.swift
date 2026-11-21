import Foundation

// A tile agent's start the person picks on the sheet, a follow-up that
// continues the agent's own session, a message to a tile agent that acts
// without asking, and a start tried again (D250–D253) — a port of the web
// chrome's /vendor/agent-start-words.js, so the app's sheet and its Needs
// you rows decide by the same rules and say the same words as the web's.
// Pure; DelegationStartWordsTests holds it to the JS with the vectors made
// by native/tools/delegation-words.mjs (`starts`, `messages`, `rows`,
// `batch`, `sheet`).
//
// Everything is read from xbind's answers (docs/protocol/delegation.md): a
// start's preview.session — what it opens with, `choices` (what may be
// picked), `from` (where each part came from), `suggested` and
// `modeNotTaken` (the agent's suggestion), `continue` (the agent's own idle
// session the start would continue) — and a message's preview.message.
// A fact is never joined to another with "·": lists of facts, each drawn as
// an element of its own.

/// What the person has picked on a start's sheet so far (the web's `pick`).
public struct StartPick: Sendable, Equatable {
    public var harness: String
    public var model: String
    public var mode: String
    /// "Remember for this tile", ticked.
    public var remember: Bool
    /// Continuing the session the start offers (`preview.session.continue`).
    public var cont: Bool
    /// The pickers the person changed, in the order they did ("harness",
    /// "model", "mode").
    public var changed: [String]
    /// The model and mode a coding agent the person switched to reset to
    /// its own defaults: the defaults, not the person's pick.
    public var defaulted: [String]

    public init(harness: String, model: String, mode: String, remember: Bool = false, cont: Bool = false, changed: [String] = [], defaulted: [String] = []) {
        self.harness = harness
        self.model = model
        self.mode = mode
        self.remember = remember
        self.cont = cont
        self.changed = changed
        self.defaulted = defaulted
    }
}

/// One of the sheet's pickers: Coding agent, Model or Mode.
public struct StartPicker: Sendable, Equatable, Identifiable {
    public struct Option: Sendable, Equatable, Hashable {
        public var value: String
        public var label: String
    }
    /// "harness" | "model" | "mode".
    public var id: String
    public var label: String
    public var value: String
    public var options: [Option]
    /// Where the value came from, as the sheet says it under the picker
    /// ("remembered for this tile", "your pick", …; "" none).
    public var from: String
    /// A plain note under it ("" none).
    public var note: String
    /// What the agent suggested and isn't picked for the person ("" none).
    public var warn: String

    /// The option that is picked ("" — a value no option has).
    public var valueLabel: String { options.first { $0.value == value }?.label ?? "" }
}

/// The sheet's choosing part for a start (the web's `startView`).
public struct StartView: Sendable, Equatable {
    /// The session a start would continue.
    public struct Cont: Sendable, Equatable {
        public var session: String
        /// "Continue the Claude Code session from 10:52 with this follow-up".
        public var label: String
        /// "Start a new session instead".
        public var sub: String
        /// Its facts: coding agent, model, mode, the sign-in it carries.
        public var chips: [String]
        /// Which saved sign-in it carries, by id ("" none).
        public var signInId: String
    }
    public struct Remember: Sendable, Equatable {
        public var shown: Bool
        public var label: String
        public var note: String
        public var keepsMode: Bool
    }
    /// None while it continues a session (that session keeps its own).
    public var pickers: [StartPicker]
    public var cont: Cont?
    public var continuing: Bool
    public var remember: Remember
    /// The picked mode asks before acting.
    public var asks: Bool
    /// The mode as a fact: "asks before acting" | "acts without asking".
    public var modeChip: String
    /// The plain line under it ("" when it asks).
    public var fact: String
    /// Allow's words: "Start" | "Send" (continuing).
    public var label: String
    /// The call, for Details: "POST /api/xbin/term/sessions".
    public var details: String
}

/// A message to a tile agent's session, as the sheet shows it.
public struct MessageView: Sendable, Equatable {
    public var heading: ApprovalHeading
    public var title: String
    /// The session's facts as chips.
    public var agent: [String]
    public var signInId: String
    public var text: String
    public var why: String
    public var label: String
}

/// A follow-up's text as a Needs you row shows it (the web's `rowText`).
public struct RowText: Sendable, Equatable {
    /// All of it is shown.
    public var whole: Bool
    public var lead: String
    /// xbin's own brief's opening paragraph, moved after the goal ("" none).
    public var opening: String
    public var note: String
    /// The part on screen: what `seen.textLen` counts.
    public var shown: String
}

/// A follow-up its person may allow on its row (the web's `inPlace`).
public struct InPlace: Sendable, Equatable {
    /// "message" | "continue".
    public var kind: String
    public var title: String
    public var chips: [String]
    public var text: String
    public var byXbin: Bool
    /// What Send sends (before how much of the text was on screen).
    public var body: JSONValue
}

/// One row of the batch of what waits (the web's `batch`) — the requests
/// that wait and the starts that may be tried again.
public struct BatchRow: Sendable, Equatable, Identifiable {
    /// "request" | "retry".
    public var kind: String
    public var id: String
    public var title: String
    public var chips: [String]
    public var signInId: String
    /// Its facts, each an element of its own (asked, expires; why it failed).
    public var parts: [String]
    /// "Review…" | "Send" | "Try again".
    public var action: String
    public var review: Bool
    public var inline: InPlace?
    public var at: Date?
}

public enum StartWords {
    /// Where a start's choice came from, as the sheet says it under each picker.
    public static let from: [String: String] = [
        "tile": "remembered for this tile", "preference": "your preference", "agent": "suggested by your agent", "default": "the default", "you": "your pick",
    ]

    /// The words the sheet says around a text it sends (the web's SHEET).
    public enum Sheet {
        public static let unread = "Show all of the text to allow it"
        public static let reachEnd = "Scroll to the end of the text to allow it"
        public static let sent = "sent exactly like this"
        public static let sentOnce = "sent exactly like this, once"
        public static let whereItGoes = "Where it goes"
        public static let agentRow = "Tile agent"
        public static let messageRow = "Message"
        public static let details = "Details"
        /// A row's: xbin's own brief's opening paragraph, after the goal (``rowText``).
        public static let opensWith = "It opens with: "
    }

    /// The approve's word that `seen.textLen` says how much of the text was
    /// on screen (D252).
    public static let seenText = "seen-text"

    /// A follow-up shown whole on the web's row: at most 12 lines and 1,200
    /// characters; a longer one shows 3 lines (at most 280 characters).
    public static let rowFitLines = 12
    public static let rowFitChars = 1200
    public static let rowLines = 3
    public static let rowChars = 280
    /// …and on the app's Needs you row (D253): at most 6 lines and 400
    /// characters — any other opens the sheet.
    public static let appRowLines = 6
    public static let appRowChars = 400

    // MARK: Reading a start

    static func session(_ r: DelegateRequest) -> JSONValue { r.preview["session"] ?? .object([:]) }
    static func str(_ v: JSONValue?) -> String { v?.stringValue ?? "" }
    /// JavaScript's truthiness, for a value read from xbind.
    static func truthy(_ v: JSONValue?) -> Bool {
        guard let v else { return false }
        switch v {
        case .null: return false
        case .bool(let b): return b
        case .int(let i): return i != 0
        case .double(let d): return d != 0 && !d.isNaN
        case .string(let s): return !s.isEmpty
        case .array, .object: return true
        }
    }
    static func harnesses(_ s: JSONValue) -> [JSONValue] { s["choices"]?["harnesses"]?.arrayValue ?? [] }
    /// The agent's own idle session the start would continue, when it offers one.
    static func contOf(_ s: JSONValue) -> JSONValue? {
        guard let c = s["continue"], c.objectValue != nil, truthy(c["session"]) else { return nil }
        return c
    }
    static func modes(_ h: JSONValue?) -> [JSONValue] { h?["modes"]?.arrayValue ?? [] }

    /// The mode as the one fact the sheet, the batch and the app show of it (D251).
    public static func modeChip(_ asks: Bool) -> String { asks ? "asks before acting" : "acts without asking" }

    /// The sheet's first pick: what xbind preselected — continuing the
    /// session there when the start offers one — remember unticked; a Try
    /// again (state failed) preselects the picks that were approved, never
    /// the sign-in tick.
    public static func pickOf(_ r: DelegateRequest) -> StartPick {
        let s = session(r)
        if r.state == .failed, let ch = r.chosen {
            return StartPick(harness: nonEmpty(str(ch["provider"])) ?? str(s["provider"]), model: str(ch["model"]), mode: str(ch["mode"]),
                             cont: truthy(ch["continue"]))
        }
        let c = contOf(s)
        return StartPick(harness: nonEmpty(str(c?["provider"])) ?? str(s["provider"]), model: str(s["model"]), mode: str(s["mode"]), cont: c != nil)
    }

    /// The person continues the session there, or starts a new one — on what
    /// xbind preselected for a new one.
    public static func contChoice(_ pick: StartPick, _ r: DelegateRequest, _ cont: Bool) -> StartPick {
        let s = session(r)
        var next = pick
        if cont, let c = contOf(s) {
            next.cont = true
            next.harness = str(c["provider"])
            next.changed = pick.changed.filter { $0 != "harness" }
            return next
        }
        next.cont = false
        next.harness = str(s["provider"])
        next.model = str(s["model"])
        next.mode = str(s["mode"])
        next.changed = []
        return next
    }

    /// The pick after the person changed one picker — a coding agent changed
    /// resets its model and mode to its own defaults.
    public static func choose(_ pick: StartPick, _ r: DelegateRequest, field: String, value: String) -> StartPick {
        var next = pick
        if !next.changed.contains(field) { next.changed.append(field) }
        next.defaulted = pick.defaulted.filter { $0 != field }
        switch field {
        case "harness":
            next.harness = value
            if let h = harnesses(session(r)).first(where: { str($0["id"]) == value }) {
                next.model = ""
                next.mode = str(h["defaultMode"])
                next.changed = next.changed.filter { $0 != "model" && $0 != "mode" }
                next.defaulted = ["model", "mode"]
            }
        case "model": next.model = value
        case "mode": next.mode = value
        default: break
        }
        next.remember = true // a pick of their own: offered to keep — they can untick it
        return next
    }

    /// Whether the pick continues the session the start offers.
    static func continuing(_ s: JSONValue, _ pick: StartPick) -> JSONValue? {
        guard let c = contOf(s), pick.cont, pick.harness == str(c["provider"]) else { return nil }
        return c
    }

    /// The start as the person has picked it so far: the request with its
    /// preview.session drawn for that pick (the coding agent's name, the
    /// mode and whether it asks, the model, and THAT coding agent's sign-in
    /// offer and network). A request without choices is itself.
    public static func picked(_ r: DelegateRequest, _ pick: StartPick) -> DelegateRequest {
        let s = session(r)
        guard var o = s.objectValue else { return r }
        if let c = continuing(s, pick) {
            // continuing the session there: its own sign-in, a fact of that session (D251: no tick again)
            o["signInId"] = .string(str(c["signInId"]))
            o["signInTrust"] = .null
            return with(r, o)
        }
        guard let h = harnesses(s).first(where: { str($0["id"]) == pick.harness }) else { return r }
        let mode = modes(h).first { str($0["id"]) == pick.mode }
        let same = str(h["id"]) == str(s["provider"])
        o["provider"] = h["id"] ?? .string("")
        o["providerName"] = h["name"] ?? .null
        o["mode"] = .string(pick.mode)
        o["asks"] = .bool(mode?["asks"]?.boolValue == true)
        o["model"] = .string(pick.model)
        if let n = h["net"], !n.isNull { o["net"] = n } // (else the start's own)
        if let si = h["signIn"], !si.isNull { o["signIn"] = si } else if !same { o["signIn"] = .string("") }
        o["signInId"] = .string(nonEmpty(str(h["signInId"])) ?? (same ? str(s["signInId"]) : ""))
        if let t = h["signInTrust"], truthy(t) { o["signInTrust"] = t } else if same, let t = s["signInTrust"], truthy(t) { o["signInTrust"] = t } else { o["signInTrust"] = .null }
        return with(r, o)
    }

    private static func with(_ r: DelegateRequest, _ session: [String: JSONValue]) -> DelegateRequest {
        var out = r
        var p = r.preview.objectValue ?? [:]
        p["session"] = .object(session)
        out.preview = .object(p)
        return out
    }

    /// A session a start (or a message) goes to, as its facts — coding
    /// agent, model, mode, the sign-in it carries.
    public static func contChips(_ c: JSONValue?) -> [String] {
        let name = nonEmpty(str(c?["providerName"])) ?? nonEmpty(str(c?["provider"])) ?? "the tile agent"
        let model = str(c?["model"])
        return [name, model.isEmpty ? "" : "model \(model)", modeChip(c?["asks"]?.boolValue == true),
                str(c?["signInId"]).isEmpty ? "" : "uses your \(name) sign-in"].filter { !$0.isEmpty }
    }

    private static func modeOptions(_ h: JSONValue) -> [StartPicker.Option] {
        modes(h).map { x in
            let name = nonEmpty(str(x["name"])) ?? nonEmpty(str(x["id"])) ?? "its own default"
            return StartPicker.Option(value: str(x["id"]), label: "\(name), \(x["explicit"]?.boolValue == true ? "skips every check" : modeChip(x["asks"]?.boolValue == true))")
        }
    }

    private static func explicitNote(_ m: JSONValue?, _ h: JSONValue) -> String {
        guard let m, m["explicit"]?.boolValue == true else { return "" }
        return "\(nonEmpty(str(m["name"])) ?? str(m["id"])) skips every check \(str(h["name"])) has of its own, even the ones it keeps in its other modes."
    }

    /// "Remember for this tile" as the sheet offers it: it keeps only what
    /// the person changed — any mode but a bypass one (D251).
    private static func rememberWords(_ continuing: Bool, _ hs: [JSONValue], _ h: JSONValue?, _ pick: StartPick) -> StartView.Remember {
        let mode = modes(h).first { str($0["id"]) == pick.mode }
        let explicit = mode?["explicit"]?.boolValue == true
        let note = mode != nil && explicit
            ? "Your next start here preselects the coding agent and model you changed. A mode that skips every check isn't remembered: you pick it each time."
            : "Your next start here preselects what you changed."
        return .init(shown: !continuing && !hs.isEmpty && h != nil, label: "Remember for this tile", note: note, keepsMode: mode != nil && !explicit)
    }

    /// The sheet's choosing part for a start.
    public static func startView(_ r: DelegateRequest, _ pick: StartPick, zone: TimeZone = .current) -> StartView {
        let s = session(r)
        let fromOf = s["from"], sug = s["suggested"]
        let hs = harnesses(s)
        let h = hs.first { str($0["id"]) == pick.harness }
        let c = contOf(s)
        let isContinuing = continuing(s, pick) != nil
        func fromWords(_ f: String) -> String {
            let key = pick.changed.contains(f) ? "you" : pick.defaulted.contains(f) ? "default" : str(fromOf?[f])
            return from[key] ?? ""
        }
        var pickers: [StartPicker] = []
        if DelegateRequest.resumes(s), let h {
            // a resume keeps its coding agent and model; its mode is picked again — a bypass one it ran in last is never
            // taken: it resumes in its coding agent's work mode, and says what it was in (D251 review M2)
            let m = modes(h).first { str($0["id"]) == pick.mode }
            let was = str(s["resumeModeNotTaken"])
            pickers.append(StartPicker(id: "mode", label: "Mode", value: pick.mode, options: modeOptions(h), from: "", note: explicitNote(m, h),
                                       warn: was.isEmpty ? "" : "It was in \(was), which skips every check; it resumes in \(nonEmpty(str(s["mode"])) ?? "its own default mode"). Pick it yourself if you want it."))
        } else if !isContinuing, !hs.isEmpty {
            let suggested = str(sug?["harness"])
            pickers.append(StartPicker(id: "harness", label: "Coding agent", value: pick.harness,
                                       options: hs.map { x in
                                           let id = str(x["id"])
                                           return .init(value: id, label: id == suggested && id != pick.harness ? "\(str(x["name"])) (suggested by your agent)" : str(x["name"]))
                                       },
                                       from: fromWords("harness"), note: "", warn: ""))
            if let h {
                let models: [StartPicker.Option] = (h["models"]?.arrayValue ?? []).map { m in
                    let id = str(m["id"])
                    return .init(value: id, label: id.isEmpty ? "its default" : str(m["from"]) == "agent" ? "\(id) (suggested by your agent)" : id)
                }
                pickers.append(StartPicker(id: "model", label: "Model", value: pick.model, options: models, from: fromWords("model"),
                                           note: h["modelsKnown"]?.boolValue == true ? "" : "\(str(h["name"]))'s own list shows in its session once it runs: these are the models xbin knows of it.",
                                           warn: ""))
                let notTakenOf = str(s["modeNotTaken"])
                let notTaken = !notTakenOf.isEmpty && str(h["id"]) == str(s["provider"]) && pick.mode != notTakenOf ? notTakenOf : ""
                let m = modes(h).first { str($0["id"]) == pick.mode }
                pickers.append(StartPicker(id: "mode", label: "Mode", value: pick.mode, options: modeOptions(h), from: fromWords("mode"),
                                           note: explicitNote(m, h),
                                           // the agent's suggestion of a bypass mode, never preselected (only the person names one)
                                           warn: notTaken.isEmpty ? "" : "Your agent suggested \(notTaken), which skips every check. It isn't picked for you: pick it yourself if you want it."))
            }
        }
        let ps = session(picked(r, pick))
        let asks = isContinuing ? c?["asks"]?.boolValue == true : ps["asks"]?.boolValue == true
        var cont: StartView.Cont?
        if let c {
            let name = nonEmpty(str(c["providerName"])) ?? str(c["provider"])
            let started = DelegateTime.date(c["started"])
            cont = .init(session: str(c["session"]),
                         label: "Continue the \(name) session\(started.map { " from \(DelegationWords.clock($0, zone: zone))" } ?? "") with this follow-up",
                         sub: "Start a new session instead", chips: contChips(c), signInId: str(c["signInId"]))
        }
        return StartView(pickers: pickers, cont: cont, continuing: isContinuing,
                         remember: DelegateRequest.resumes(s) ? .init(shown: false, label: "", note: "", keepsMode: false) : rememberWords(isContinuing, hs, h, pick),
                         asks: asks, modeChip: modeChip(asks), fact: asks ? "" : DelegationWords.modeFact, label: isContinuing ? "Send" : "Start",
                         details: "\(nonEmpty(r.method) ?? "POST") \(nonEmpty(r.target) ?? "/api/xbin/term/sessions")")
    }

    /// What the approval sends of the pick (POST …/approve): `{knows:
    /// ["continue"], seen: {signInId, net[, harness, mode]}, harness, model,
    /// mode, remember?, continue: false?}` — only for a start with choices;
    /// `{}` for anything else, and for an older xbind's start (it takes no pick).
    public static func pickBody(_ r: DelegateRequest, _ pick: StartPick) -> [String: JSONValue] {
        let s = session(r)
        guard r.kind == .session, !harnesses(s).isEmpty else { return [:] }
        // seen: what this sheet shows of what xbind resolves again when it is decided — the sign-in it offers, the
        // network — held to it there: a change since is refused, nothing decided, and the sheet looks again (D250 review L2)
        let ps = session(picked(r, pick))
        var seen: [String: JSONValue] = ["signInId": .string(str(ps["signInId"])), "net": .string(str(ps["net"]))]
        var out: [String: JSONValue] = ["knows": ["continue"]]
        if DelegateRequest.resumes(s) { // a resume picks its mode again, nothing else (D251 review M2)
            out["seen"] = .object(seen)
            out["mode"] = .string(pick.mode)
            return out
        }
        if let c = continuing(s, pick) {
            // continuing: the session keeps its own — held to it as shown: its coding agent and mode (D251)
            seen["harness"] = .string(str(c["provider"]))
            seen["mode"] = .string(str(c["mode"]))
            out["seen"] = .object(seen)
            return out
        }
        out["seen"] = .object(seen)
        if contOf(s) != nil { out["continue"] = false }
        out["harness"] = .string(pick.harness)
        out["model"] = .string(pick.model)
        out["mode"] = .string(pick.mode)
        if pick.remember { out["remember"] = true }
        return out
    }

    /// A text's length as xbind counts it: UTF-8 bytes (seen.textLen).
    public static func textBytes(_ text: String) -> Int { text.utf8.count }

    /// An approve body that says how much of the text was on screen when
    /// its person clicked (`onScreen`, the part shown): knows "seen-text".
    public static func withSeenText(_ body: [String: JSONValue], onScreen: String) -> [String: JSONValue] {
        var out = body
        var knows = (body["knows"]?.arrayValue ?? []).compactMap(\.stringValue)
        if !knows.contains(seenText) { knows.append(seenText) }
        out["knows"] = .array(knows.map { .string($0) })
        var seen = body["seen"]?.objectValue ?? [:]
        seen["textLen"] = .int(Int64(textBytes(onScreen)))
        out["seen"] = .object(seen)
        return out
    }

    /// What the sheet's Allow sends: `base` (``DelegationWords/approveBody``'s
    /// `{grant, scope?, unseen?}`) with the pick, how much of the text it
    /// sends was on screen (a start's brief, a message), and for a Try again
    /// (state failed: `POST …/retry`) only what a retry takes — no grant, no
    /// continue, nothing remembered, no length. The web's sheet sends the
    /// same (sheetBody).
    public static func sheetBody(_ r: DelegateRequest, pick: StartPick?, onScreen: String, base: JSONValue = ["grant": "once"]) -> JSONValue {
        var body = base.objectValue ?? [:]
        if let pick { body.merge(pickBody(r, pick)) { _, new in new } }
        if r.kind == .message || (r.kind == .session && r.state != .failed) { body = withSeenText(body, onScreen: onScreen) }
        if r.state == .failed { for k in ["grant", "scope", "unseen", "continue", "remember"] { body[k] = nil } }
        return .object(body)
    }

    /// The sheet's heading: a message's; a start the person continues
    /// ("Continue the session in …?"); a start tried again ("Try again to
    /// start a tile agent in …?"); else the request's own.
    public static func sheetHeading(_ r: DelegateRequest, _ view: StartView? = nil) -> ApprovalHeading {
        let h = DelegationWords.heading(r)
        guard r.kind == .session else { return h }
        if view?.continuing == true { return ApprovalHeading(before: "Continue the session in ", strong: h.strong, after: "?") }
        if r.state == .failed, r.failureRetry { return ApprovalHeading(before: "Try again to start a tile agent in ", strong: h.strong, after: "?") }
        return h
    }

    /// What a start's session is told first — xbind's reading (preview.session.prompt).
    public static func briefOf(_ r: DelegateRequest) -> String { str(session(r)["prompt"]) }

    /// A pending start as the batch's chips: the coding agent it would run
    /// on and whether it asks ([] for anything else).
    public static func startChips(_ r: DelegateRequest) -> [String] {
        guard r.kind == .session else { return [] }
        let s = session(r)
        if let c = contOf(s) { return contChips(c) }
        let model = str(s["model"])
        return [nonEmpty(str(s["providerName"])) ?? str(s["provider"]), model.isEmpty ? "" : "model \(model)", modeChip(s["asks"]?.boolValue == true)].filter { !$0.isEmpty }
    }

    // MARK: A message to a tile agent that acts without asking

    /// The saved sign-in a follow-up's session carries — a message's, a
    /// continue's — by id ("" none).
    public static func signInIdOf(_ r: DelegateRequest) -> String {
        if r.kind == .message { return str(r.preview["message"]?["signInId"]) }
        guard r.kind == .session, let c = contOf(session(r)) else { return "" }
        return str(c["signInId"])
    }

    public static func messageView(_ r: DelegateRequest) -> MessageView {
        let m = r.preview["message"]
        let provider = DelegationWords.messageAgent(r)
        let mode = str(m?["mode"]), model = str(m?["model"]), signInId = str(m?["signInId"])
        return MessageView(heading: DelegationWords.heading(r), title: DelegationWords.needTitle(r),
                           agent: [provider, mode.isEmpty ? "" : "mode \(mode)", modeChip(false), model.isEmpty ? "" : "model \(model)",
                                   signInId.isEmpty ? "" : "uses your \(provider) sign-in"].filter { !$0.isEmpty },
                           signInId: signInId, text: str(m?["text"]),
                           why: "Send sends exactly this text to this session, once. It isn't sent if the session's mode or coding agent changed since.",
                           label: "Send")
    }

    /// The text an approval of `r` sends ("" none): a start's brief, a message.
    public static func approvalText(_ r: DelegateRequest) -> String {
        switch r.kind {
        case .message: return messageView(r).text
        case .session: return briefOf(r)
        default: return ""
        }
    }

    // MARK: A follow-up on its row

    private static let briefOpening = "This is a piece of work in this tile, handed to you by "

    /// "1,200": a count as the web's `toLocaleString('en-US')` writes it.
    static func grouped(_ n: Int) -> String {
        var digits = String(n)
        var out = ""
        while digits.count > 3 {
            out = "," + digits.suffix(3) + out
            digits = String(digits.dropLast(3))
        }
        return digits + out
    }

    /// A follow-up's text as its row shows it: whole when it fits (12
    /// lines, 1,200 characters) or `all`; else its first lines and how long
    /// it is. `byXbin`: xbind says xbin wrote it — its opening paragraph
    /// moves after the goal.
    public static func rowText(_ text: String, all: Bool = false, byXbin: Bool = false) -> RowText {
        let chars = text.unicodeScalars.count
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false).count
        var lead = text, opening = ""
        if byXbin, text.hasPrefix(briefOpening), let cut = text.range(of: "\n\n"), cut.lowerBound > text.startIndex {
            opening = String(text[..<cut.lowerBound])
            lead = String(text[cut.lowerBound...].drop { $0 == "\n" })
        }
        let size = "\(lines) \(lines == 1 ? "line" : "lines"), \(grouped(chars)) characters"
        let over = lines > rowFitLines || chars > rowFitChars
        if all || !over { return RowText(whole: true, lead: lead, opening: opening, note: all && over ? size : "", shown: text) }
        let first = lead.split(separator: "\n", omittingEmptySubsequences: false).prefix(rowLines).joined(separator: "\n")
        let head = String(String.UnicodeScalarView(first.unicodeScalars.prefix(rowChars)))
        return RowText(whole: false, lead: head, opening: "", note: "\(size): show all of it to send it", shown: head)
    }

    /// The app's Needs you row shows all of `text` (D253): at most
    /// ``appRowLines`` lines and ``appRowChars`` characters.
    public static func appRowFits(_ text: String) -> Bool {
        text.split(separator: "\n", omittingEmptySubsequences: false).count <= appRowLines && text.unicodeScalars.count <= appRowChars
    }

    /// A follow-up its person can allow on its row — a message, or a start
    /// that continues the agent's own session there (D251 B2) — nil for
    /// anything else, and for one the sheet must show: text that holds
    /// hidden characters, a conversation that read the web, a start xbind
    /// offers no choices for.
    public static func inPlace(_ r: DelegateRequest) -> InPlace? {
        guard r.state == .pending, !truthy(r.preview["readWeb"]) else { return nil }
        if r.kind == .message {
            let m = messageView(r)
            guard !m.text.isEmpty, DelegationWords.reveal(m.text).hidden == 0 else { return nil }
            return InPlace(kind: "message", title: m.title, chips: m.agent, text: m.text, byXbin: false, body: ["grant": "once"])
        }
        let s = session(r)
        guard r.kind == .session, let c = contOf(s), !DelegateRequest.resumes(s), !harnesses(s).isEmpty else { return nil }
        let text = briefOf(r)
        guard !text.isEmpty, DelegationWords.reveal(text).hidden == 0 else { return nil }
        let tile = r.tilePath.split(separator: "/", omittingEmptySubsequences: true).last.map(String.init) ?? "the tile"
        var body: [String: JSONValue] = ["grant": "once"]
        body.merge(pickBody(r, pickOf(r))) { _, new in new }
        return InPlace(kind: "continue", title: "Continue the \(nonEmpty(str(c["providerName"])) ?? str(c["provider"])) session in \(tile)", chips: contChips(c), text: text,
                       byXbin: str(s["briefBy"]) == "xbin", body: .object(body))
    }

    /// …where the app's row may allow it: its text fits the row (``appRowFits``).
    public static func appInPlace(_ r: DelegateRequest) -> InPlace? {
        guard let x = inPlace(r), appRowFits(x.text) else { return nil }
        return x
    }

    // MARK: The batch

    /// When it was asked and when it expires — two facts, never one joined line.
    public static func askedParts(created: Date?, expires: Date?, now: Date = Date(), zone: TimeZone = .current) -> [String] {
        [created.map { "asked \(DelegationWords.clock($0, zone: zone))" } ?? "",
         expires.map { $0 > now ? "expires in \(DelegationWords.left($0.timeIntervalSince(now)))" : "expired" } ?? ""].filter { !$0.isEmpty }
    }

    /// The batch's rows of requests: each pending one once (a follow-up the
    /// web's row allows: `inline`), and each start that may be tried again,
    /// oldest first.
    public static func batch(requests: [DelegateRequest], failed: [DelegateRequest], now: Date = Date(), zone: TimeZone = .current) -> [BatchRow] {
        var rows: [BatchRow] = []
        for r in requests where r.state == .pending {
            let inline = inPlace(r)
            rows.append(BatchRow(kind: "request", id: r.id, title: inline?.title ?? DelegationWords.needTitle(r), chips: inline?.chips ?? startChips(r),
                                 signInId: signInIdOf(r), parts: askedParts(created: r.created, expires: r.expires, now: now, zone: zone),
                                 action: inline == nil ? "Review…" : "Send", review: true, inline: inline, at: r.created))
        }
        for r in failed where r.state == .failed && r.failureRetry {
            rows.append(BatchRow(kind: "retry", id: r.id, title: "Didn't start: \(DelegationWords.needTitle(r))", chips: [], signInId: "",
                                 parts: [r.failureWhy, "approved by you"].filter { !$0.isEmpty }, action: DelegationWords.tryAgain, review: false, inline: nil, at: r.created))
        }
        return rows.enumerated().sorted { a, b in
            let x = a.element.at?.timeIntervalSince1970 ?? 0, y = b.element.at?.timeIntervalSince1970 ?? 0
            return x != y ? x < y : a.offset < b.offset
        }.map(\.element)
    }

    // MARK: An older xbind

    /// An xbind before D252 (v0.3.84 and older) reads an approve strictly and
    /// refuses a field it doesn't know — `seen.textLen`, a continue's
    /// `seen.harness` and `seen.mode` — with 400 "need {…}", nothing
    /// decided. Its sheet holds Allow back itself; the app's does too. Such
    /// an answer: the body in the shape that xbind takes, sent once more —
    /// nil when there is nothing an older xbind wouldn't know (then the 400
    /// stands).
    public static func olderXbindBody(_ body: JSONValue, refused: APIResponse) -> JSONValue? {
        guard refused.status == 400, let error = (try? refused.json())?["error"]?.stringValue, error.hasPrefix("need {"),
              var o = body.objectValue else { return nil }
        var changed = false
        if var seen = o["seen"]?.objectValue {
            for k in ["textLen", "harness", "mode"] where seen[k] != nil {
                seen[k] = nil
                changed = true
            }
            o["seen"] = seen.isEmpty ? nil : .object(seen)
        }
        if let knows = o["knows"]?.arrayValue, knows.contains(.string(seenText)) {
            let rest = knows.filter { $0 != .string(seenText) }
            o["knows"] = rest.isEmpty ? nil : .array(rest)
            changed = true
        }
        return changed ? .object(o) : nil
    }

    private static func nonEmpty(_ s: String?) -> String? { (s ?? "").isEmpty ? nil : s }
}

extension DelegateRequest {
    /// `preview.session.resume`, as the web reads it (truthy).
    static func resumes(_ s: JSONValue) -> Bool { StartWords.truthy(s["resume"]) }
}
