import Foundation

// The workspace agent's notifications in the app (D241; plans/wsagent/
// ios-stage2.md §3–§4, §11): what a payload's `agent` member says, which
// category — which lock-screen actions — a notification gets, the text the
// extension draws from the request's own fields, and the check an action
// runs before it decides anything. Foundation only, tested on Linux: the
// extension (NotificationService) and the app (App/Push/AgentNotifications)
// are glue around this.
//
// The rules a change here must keep:
// - the category comes from the request's fields and the person's settings,
//   never from text (a summary, a title, a question take no part), and Allow
//   once needs xbind's `offer` AND this file's own check of the same fields;
// - the visible text is drawn from those fields, never the agent's words in
//   a title, every hidden character as its escape;
// - an action decides only what xbind reads now exactly as it was shown,
//   with the setting on now — a reply is a message whatever it says.

/// What the person turned on for one workspace (§11). Missing keys read as
/// the defaults: Needs you, Finished, the Live Activity and Deny on; Allow
/// once off until the person turns it on themselves.
public struct AgentNotifyPrefs: Sendable, Equatable, Codable {
    public var needsYou = true
    public var finished = true
    public var liveActivity = true
    public var lockDeny = true
    public var lockAllowOnce = false
    /// The person turned Allow once on after its explanation, once.
    public var allowOnceExplained = false

    public init() {}

    enum CodingKeys: String, CodingKey { case needsYou, finished, liveActivity, lockDeny, lockAllowOnce, allowOnceExplained }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func b(_ k: CodingKeys, _ d: Bool) -> Bool { ((try? c.decodeIfPresent(Bool.self, forKey: k)) ?? nil) ?? d }
        needsYou = b(.needsYou, true)
        finished = b(.finished, true)
        liveActivity = b(.liveActivity, true)
        lockDeny = b(.lockDeny, true)
        lockAllowOnce = b(.lockAllowOnce, false)
        allowOnceExplained = b(.allowOnceExplained, false)
    }

    /// The kinds the push registration asks for: `base` as it is while every
    /// agent kind is on (`agent` takes them all, later ones too), else `agent`
    /// spelled out without what the person turned off. xbind sends nothing
    /// of a kind a registration leaves out.
    public func kinds(base: [String]) -> [String] {
        if needsYou && finished && liveActivity { return base }
        var agent = ["agent.permission", "agent.question", "agent.turn", "agent.activity", "agent.clear"]
        if needsYou { agent += ["agent.approval", "agent.changeset", "agent.ask"] }
        if finished { agent.append("agent.done") }
        if liveActivity { agent.append("agent.wsactivity") }
        return base.flatMap { $0 == "agent" ? agent : [$0] }
    }
}

/// A payload's `agent` member (native/spec/push.md §2.1); nil unless `v` is 1.
public struct AgentNoticeData: Sendable, Equatable {
    public struct Request: Sendable, Equatable, Codable {
        public var id = ""
        public var kind = ""
        public var tile = ""
        public var tileName = ""
        public var expires: Int64 = 0
        /// xbind's lock-screen rule: "once" when Allow once may be offered.
        public var offer = ""
        // Only with offer "once": the call itself.
        public var method = ""
        public var target = ""
        public var contentType = ""
        public var body = ""
        public var bodySize = 0
        public var bodySha256 = ""

        public init() {}

        init(json j: JSONValue) {
            id = j["id"]?.stringValue ?? ""
            kind = j["kind"]?.stringValue ?? ""
            tile = j["tile"]?.stringValue ?? ""
            tileName = j["tileName"]?.stringValue ?? ""
            expires = j["expires"]?.intValue ?? 0
            offer = j["offer"]?.stringValue ?? ""
            method = j["method"]?.stringValue ?? ""
            target = j["target"]?.stringValue ?? ""
            contentType = j["contentType"]?.stringValue ?? ""
            body = j["body"]?.stringValue ?? ""
            bodySize = Int(j["bodySize"]?.intValue ?? 0)
            bodySha256 = j["bodySha256"]?.stringValue ?? ""
        }
    }

    public struct ChangeSet: Sendable, Equatable {
        public var id = ""
        public var items = 0
        public var warned = 0
        public var plan = false
        public var asks = false
    }

    public var badge: Int?
    public var request: Request?
    public var changeset: ChangeSet?
    public var conversation: String?
    /// The agent's question (agent.ask): its own words, drawn quoted and revealed.
    public var ask: String?
    /// agent.clear: the collapse ids that no longer stand.
    public var clear: [String] = []

    public init?(json: JSONValue?) {
        guard let j = json, j.objectValue != nil, j["v"]?.intValue == 1 else { return nil }
        badge = j["badge"]?.intValue.map { Int(clamping: $0) }
        if let r = j["request"], r.objectValue != nil {
            let req = Request(json: r)
            request = DelegateID.isRequest(req.id) ? req : nil
        }
        if let c = j["changeset"], c.objectValue != nil, let id = c["id"]?.stringValue, DelegateID.isRequest(id) {
            changeset = ChangeSet(id: id, items: Int(c["items"]?.intValue ?? 0), warned: Int(c["warned"]?.intValue ?? 0),
                                  plan: c["plan"]?.boolValue ?? false, asks: c["asks"]?.boolValue ?? false)
        }
        if let id = j["conversation"]?["id"]?.stringValue, AgentRoute.isConversation(id) { conversation = id }
        ask = j["ask"]?.stringValue
        clear = (j["clear"]?.arrayValue ?? []).compactMap(\.stringValue).filter { $0.count <= 96 }
    }
}

/// What a notification showed of a request: the fields its text was drawn
/// from, kept in its `userInfo` (`shown`), which an action is checked against.
public struct AgentShownRequest: Sendable, Equatable, Codable {
    public var id: String
    public var kind: String
    public var tile: String
    public var method: String
    public var target: String
    public var contentType: String
    public var body: String
    public var bodySha256: String
    /// Allow once was offered (the call was shown whole).
    public var whole: Bool

    public init(id: String, kind: String, tile: String, method: String = "", target: String = "", contentType: String = "",
                body: String = "", bodySha256: String = "", whole: Bool = false) {
        self.id = id
        self.kind = kind
        self.tile = tile
        self.method = method
        self.target = target
        self.contentType = contentType
        self.body = body
        self.bodySha256 = bodySha256
        self.whole = whole
    }

    public var encoded: String {
        (try? JSONEncoder().encode(self)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }

    public init?(encoded: String) {
        guard let d = encoded.data(using: .utf8), let s = try? JSONDecoder().decode(Self.self, from: d) else { return nil }
        self = s
    }
}

public enum AgentNotice {
    // The categories (the app registers each with its actions: §3.5).
    public static let requestAllowDeny = "xbin.agent.request.allow-deny"
    public static let requestAllow = "xbin.agent.request.allow"
    public static let requestDeny = "xbin.agent.request.deny"
    public static let requestReview = "xbin.agent.request.review"
    public static let changesetReject = "xbin.agent.changeset.reject"
    public static let changesetReview = "xbin.agent.changeset.review"
    public static let conversation = "xbin.agent.conversation"
    public static let followupRetry = "xbin.agent.followup.retry"
    public static let followup = "xbin.agent.followup"

    /// The action identifiers.
    public static let allowOnceAction = "allow-once"
    public static let denyAction = "deny"
    public static let rejectAction = "reject"
    public static let reviewAction = "review"
    public static let replyAction = "reply"
    public static let retryAction = "retry"
    public static let openAction = "open"

    /// The app's own lock-screen check of a request's fields — the same
    /// rule as xbind's (delegation.LockScreenOffer), on what the payload
    /// carries: a tile call, not a GET, the whole body there, at most 200
    /// bytes and 3 lines, a target of at most 120 bytes on one line, a plain
    /// content type, nothing hidden, and all of it certainly shown in the
    /// collapsed notification (LockScreenFit). xbind's `offer` alone is not
    /// enough.
    public static func eligible(_ r: AgentNoticeData.Request) -> Bool {
        let m = r.method.uppercased()
        let ct = r.contentType.split(separator: ";").first.map { $0.trimmingCharacters(in: .whitespaces).lowercased() } ?? ""
        return r.offer == "once" && r.kind == "call" && DelegateID.isRequest(r.id) && !r.tile.isEmpty
            && !m.isEmpty && m != "GET" && m != "HEAD"
            && r.body.utf8.count == r.bodySize && r.bodySize <= 200 && r.body.split(separator: "\n", omittingEmptySubsequences: false).count <= 3
            && !r.target.isEmpty && r.target.utf8.count <= 120 && !r.target.contains("\n")
            && ["", "application/json", "text/plain"].contains(ct)
            && !hides(r.target) && !hides(r.body) && !hides(r.method)
            && LockScreenFit.fits(method: r.method, target: r.target, body: r.body)
    }

    static func hides(_ s: String) -> Bool { s.unicodeScalars.contains(where: DelegationWords.isHidden) }

    /// The category of an agent notification: §3.5's matrix, from the
    /// payload's kind and fields and this workspace's settings.
    public static func category(kind: String, data: AgentNoticeData?, prefs: AgentNotifyPrefs) -> String {
        switch kind {
        case "agent.approval":
            let allow = prefs.lockAllowOnce && (data?.request).map(eligible) == true
            switch (allow, prefs.lockDeny) {
            case (true, true): return requestAllowDeny
            case (true, false): return requestAllow
            case (false, true): return data?.request == nil ? requestReview : requestDeny
            case (false, false): return requestReview
            }
        case "agent.changeset":
            return prefs.lockDeny && data?.changeset != nil ? changesetReject : changesetReview
        case "agent.ask", "agent.done":
            return data?.conversation != nil ? conversation : followup
        default:
            return followup
        }
    }

    /// Whether a category offers Allow once (the action side's own read).
    public static func offersAllow(_ category: String) -> Bool { category == requestAllowDeny || category == requestAllow }

    /// What the extension draws.
    public struct Content: Sendable, Equatable {
        public var title: String
        public var body: String
        public var category: String
        public var threadID: String
        public var timeSensitive: Bool
        /// The app's userInfo: `{app, ws, kind, link, collapse, shown?, conversation?, holder?}`.
        public var userInfo: [String: String]
        public var badge: Int?
    }

    /// The notification for an agent payload of workspace `appWorkspace`
    /// (nil: not an agent kind this app draws — the payload's own text
    /// stands, as on an older app).
    public static func content(_ p: PushPayload, appWorkspace: String, prefs: AgentNotifyPrefs) -> Content? {
        guard p.kind.hasPrefix("agent."), p.kind != "agent.permission", p.kind != "agent.question", p.kind != "agent.turn",
              p.kind != "agent.clear", let data = AgentNoticeData(json: p.agent) else { return nil }
        var info: [String: String] = ["app": appWorkspace, "ws": p.ws, "kind": p.kind, "link": p.link, "collapse": p.collapseID]
        var title = p.title, body = p.body
        switch p.kind {
        case "agent.approval":
            guard let r = data.request else { break }
            let name = DelegationWords.revealed(r.tileName.isEmpty ? DelegationWords.lastOf(r.tile) : r.tileName)
            switch r.kind {
            case "template": title = "Approval needed — a new tile"
            case "web": title = "Approval needed — a web page"
            default: title = "Approval needed — \(name)"
            }
            let whole = eligible(r)
            if whole {
                body = callText(method: r.method, target: r.target, body: r.body)
            } else {
                body = words(kind: r.kind, tile: name)
            }
            info["shown"] = AgentShownRequest(id: r.id, kind: r.kind, tile: r.tile, method: whole ? r.method : "",
                                              target: whole ? r.target : "", contentType: whole ? r.contentType : "",
                                              body: whole ? r.body : "", bodySha256: whole ? r.bodySha256 : "", whole: whole).encoded
        case "agent.changeset":
            if let c = data.changeset { info["changeset"] = c.id }
        case "agent.ask":
            if let q = data.ask?.trimmingCharacters(in: .whitespacesAndNewlines), !q.isEmpty {
                // the agent's own words: quoted, below xbind's title, every hidden character as its escape
                body = "Your agent asks: “\(DelegationWords.revealed(q))”"
            }
        default:
            break
        }
        if let c = data.conversation { info["conversation"] = c }
        return Content(title: title, body: body, category: category(kind: p.kind, data: data, prefs: prefs), threadID: "\(p.ws)/xbin/agent",
                       timeSensitive: p.kind == "agent.approval" || p.kind == "agent.changeset", userInfo: info, badge: data.badge)
    }

    /// The call as the sheet shows it, in full: the method and target, then
    /// the body (revealed — though an eligible one hides nothing).
    public static func callText(method: String, target: String, body: String) -> String {
        "\(method) \(DelegationWords.revealed(target))\n" + (body.isEmpty ? "(no body)" : DelegationWords.revealed(body))
    }

    /// xbind's sentence for a request's kind (an older app's body too).
    public static func words(kind: String, tile: String) -> String {
        switch kind {
        case "session": return "Your workspace agent wants to start a tile agent in \(tile). Open to see exactly what."
        case "template": return "Your workspace agent wants to create a tile. Open to see exactly what."
        case "web": return "Your workspace agent wants to read a web page. Open to see the address."
        case "mcp": return "Your workspace agent wants to use a tool of \(tile). Open to see exactly what."
        default: return "Your workspace agent wants to change something in \(tile). Open to see exactly what."
        }
    }
}

/// What an action of a notification does. Every action id maps to one
/// effect: a reply is a message to the conversation whatever its text says
/// — only `allow-once`, `deny` and `reject` decide anything.
public enum AgentNoticeAction: Sendable, Equatable {
    case allowOnce
    case deny
    case reject
    case message(String)
    case retry
    /// Opens the app on what the notification is about (a tap, Review…, Open).
    case open

    public static func of(actionID: String, text: String?) -> AgentNoticeAction {
        switch actionID {
        case AgentNotice.allowOnceAction: return .allowOnce
        case AgentNotice.denyAction: return .deny
        case AgentNotice.rejectAction: return .reject
        case AgentNotice.replyAction: return .message(text ?? "")
        case AgentNotice.retryAction: return .retry
        default: return .open
        }
    }

    /// Whether it decides a request or a change set (and so needs the re-read).
    public var decides: Bool { self == .allowOnce || self == .deny || self == .reject }
}

/// The check an action runs before it decides (§4.1).
public enum AgentNoticeVerdict: Sendable, Equatable {
    /// xbind's reading is what was shown, and it may be decided this way.
    case send
    /// xbind can't be reached: nothing is sent.
    case unreachable
    /// It doesn't wait any more (nil: xbind has no such request — 404).
    case gone(DelegateRequest?)
    /// xbind's reading differs from what was shown: nothing is sent.
    case changed
    /// This needs the app's own sheet (why, in words).
    case review(String)
}

public enum AgentNoticeDecision {
    /// - Parameters:
    ///   - want: `.allowOnce` or `.deny`.
    ///   - shown: what the notification showed.
    ///   - current: xbind's reading now; nil with `reachable` true: 404/403.
    ///   - prefs: this workspace's settings now.
    public static func verify(_ want: AgentNoticeAction, shown: AgentShownRequest, current: DelegateRequest?, reachable: Bool,
                              prefs: AgentNotifyPrefs) -> AgentNoticeVerdict {
        switch want {
        case .allowOnce:
            guard prefs.lockAllowOnce else { return .review("Allow once from the lock screen is off for this workspace.") }
            guard shown.whole else { return .review("This request needs a look in the app.") }
        case .deny:
            guard prefs.lockDeny else { return .review("Deny from the lock screen is off for this workspace.") }
        default:
            return .review("")
        }
        guard reachable else { return .unreachable }
        guard let r = current, r.id == shown.id else { return .gone(nil) }
        guard r.kind != .unknown, r.state != .unknown else { return .review(DelegationWords.unknownState.text) }
        guard r.isPending else { return .gone(r) }
        guard r.kind.rawValue == shown.kind, r.tilePath == shown.tile else { return .changed }
        if want == .allowOnce {
            // everything the notification showed, and nothing it didn't
            guard r.kind == .call, r.method == shown.method, r.target == shown.target, r.contentType == shown.contentType,
                  r.bodyShown, !r.bodyBinary, r.body == shown.body, r.bodySha256 == shown.bodySha256,
                  r.bodySize == shown.body.utf8.count else { return .changed }
            let again = AgentNoticeData.Request.reading(r)
            guard AgentNotice.eligible(again), r.preview["readWeb"]?.boolValue != true else {
                return .review("This request needs a look in the app.")
            }
        }
        return .send
    }

    /// A change set's Reject: it must still be proposed, and be the one shown.
    public static func verifyReject(setID: String, current: AdminChangeSet?, reachable: Bool, prefs: AgentNotifyPrefs) -> AgentNoticeVerdict {
        guard prefs.lockDeny else { return .review("Reject from the lock screen is off for this workspace.") }
        guard reachable else { return .unreachable }
        guard let c = current, c.id == setID else { return .gone(nil) }
        guard c.state != .unknown else { return .review(DelegationWords.unknownState.text) }
        return c.isProposed ? .send : .review(AgentNoticeWords.setSettled(c))
    }
}

extension AgentNoticeData.Request {
    /// A request as xbind reads it, in the payload's shape (the app's own
    /// check runs on the re-read too).
    static func reading(_ r: DelegateRequest) -> AgentNoticeData.Request {
        var x = AgentNoticeData.Request()
        x.id = r.id
        x.kind = r.kind.rawValue
        x.tile = r.tilePath
        x.offer = "once"
        x.method = r.method
        x.target = r.target
        x.contentType = r.contentType
        x.body = r.body
        x.bodySize = r.bodySize
        x.bodySha256 = r.bodySha256
        return x
    }
}

/// The follow-ups' words and "already decided" — the app's own (there is no
/// notification on the web), in the chrome's voice.
public enum AgentNoticeWords {
    public struct Note: Sendable, Equatable {
        public var title: String
        public var body: String
        /// Retry and Review… (false: Review… only).
        public var retry: Bool
    }

    public static func unreachable(_ workspace: String, call: String = "") -> Note {
        Note(title: "Couldn't reach \(workspace) — nothing was decided",
             body: call.isEmpty ? "Retry, or open it once you're connected." : call, retry: true)
    }

    public static func changed() -> Note {
        Note(title: "Nothing was decided", body: "This request isn't the one the notification showed. Open it to review.", retry: false)
    }

    public static func review(_ why: String) -> Note {
        Note(title: "Nothing was decided", body: (why.isEmpty ? "" : why + " ") + "Open it to review.", retry: false)
    }

    public static func gone(_ r: DelegateRequest?, where place: String) -> Note {
        guard let r else {
            return Note(title: "Nothing to decide", body: gone404, retry: false)
        }
        let line = DelegationWords.settled(r)
        return Note(title: "Already decided", body: place.isEmpty ? line : "\(line) — \(place)", retry: false)
    }

    public static let gone404 = "This request is gone — it was decided or withdrawn, and xbin keeps decided requests a day."

    public static func allowed(call: String) -> Note { Note(title: "Allowed once", body: call, retry: false) }
    public static func denied(_ what: String) -> Note { Note(title: "Denied", body: what, retry: false) }
    public static func rejected() -> Note { Note(title: "Rejected", body: "Nothing of it was applied.", retry: false) }
    public static func stopped(_ message: String) -> Note { Note(title: "Nothing was allowed", body: message, retry: false) }
    public static func failed(_ why: String, allow: Bool) -> Note {
        Note(title: allow ? "Couldn't allow it" : "Couldn't decide it", body: why, retry: false)
    }

    public static func replyFailed(_ workspace: String, text: String, why: String = "") -> Note {
        Note(title: "Your reply wasn't sent", body: (why.isEmpty ? "Couldn't reach \(workspace)." : why) + " You wrote: “\(text)”", retry: false)
    }

    /// Where a decided request was decided, from xbind's record: "on the web",
    /// "from a notification on this iPhone", "on Ana's iPad" ("" for one xbind
    /// says nothing about, an expiry, a withdrawal).
    public static func decidedWhere(_ r: DelegateRequest, thisDevice: String, thisDeviceName: String,
                                    deviceNames: [String: String]) -> String {
        guard r.state == .approved || r.state == .used || r.state == .denied else { return "" }
        let device: String
        switch r.decidedVia {
        case "session", "cookie": device = "on the web"
        case "device", "app":
            if !r.decidedDevice.isEmpty, r.decidedDevice == thisDevice { device = "on \(thisDeviceName)" }
            else if let n = deviceNames[r.decidedDevice], !n.isEmpty { device = "on \(DelegationWords.revealed(n))" }
            else { device = "on another of your devices" }
        case "": return ""
        default: device = "elsewhere"
        }
        return r.decidedFrom == "notification" ? "from a notification \(device)" : device
    }

    /// A set that no longer waits, in a line.
    public static func setSettled(_ c: AdminChangeSet) -> String {
        switch c.state {
        case .applied: return "It was applied."
        case .partial: return "It was partly applied."
        case .rejected: return "It was rejected."
        case .expired: return "It expired."
        case .withdrawn: return c.withdrawnBy == "xbin" ? "xbin withdrew it when your agent was stopped." : "The agent withdrew it."
        default: return "It doesn't wait any more."
        }
    }
}

/// The notifications to take back (§3.2, §9): a delivered agent
/// notification whose thing doesn't wait any more — by what an
/// `agent.clear` names, or by what xbind lists as waiting now.
public enum AgentNoticeSweep {
    public struct Delivered: Sendable, Equatable {
        public var identifier: String
        public var app: String
        public var kind: String
        public var collapse: String
        public init(identifier: String, app: String, kind: String, collapse: String) {
            self.identifier = identifier
            self.app = app
            self.kind = kind
            self.collapse = collapse
        }
    }

    /// The identifiers `clear` (an agent.clear's list) takes back in `app`.
    public static func cleared(_ delivered: [Delivered], app: String, clear: [String]) -> [String] {
        let c = Set(clear)
        return delivered.filter { $0.app == app && !$0.collapse.isEmpty && c.contains($0.collapse) }.map(\.identifier)
    }

    /// The identifiers of `app`'s request and set notifications whose thing
    /// isn't among what waits now.
    public static func stale(_ delivered: [Delivered], app: String, pendingRequests: Set<String>, openSets: Set<String>) -> [String] {
        delivered.filter { d in
            guard d.app == app else { return false }
            if d.collapse.hasPrefix("agent-approval:") { return !pendingRequests.contains(String(d.collapse.dropFirst("agent-approval:".count))) }
            if d.collapse.hasPrefix("agent-changeset:") { return !openSets.contains(String(d.collapse.dropFirst("agent-changeset:".count))) }
            if d.collapse == "agent-approval" { return pendingRequests.isEmpty && openSets.isEmpty }
            return false
        }.map(\.identifier)
    }
}

/// The app icon's badge (§9): what waits for the person, across workspaces.
public enum AgentBadge {
    public static func total(_ counts: [String: Int]) -> Int { counts.values.reduce(0) { $0 + max(0, $1) } }
}
