import Foundation

// A person's workspace agent, as the app's own chrome reads it from xbind
// (docs/protocol/delegation.md; D233): the state, the requests that wait,
// the proposed change sets, the standing approvals, the log — and the
// requests that decide. Everything that grants goes out with the device's
// session, which xbind takes as the person's own; a tile's credential (a
// native tile's runtime included) is refused every one of them.
//
// Foundation only, tested on Linux. What these say in words is
// DelegationWords.swift — a port of the web chrome's
// /vendor/agent-chrome-words.js, held to it by DelegationWordsTests.

/// `GET /delegate/state`: whether this person has an agent and what state
/// it is in.
public struct DelegateState: Sendable, Equatable {
    public struct Who: Sendable, Equatable {
        public var everyone: Bool
        public var entries: Int
        public var chosen: Bool
    }

    public var user: String
    /// The workspace's switch.
    public var workspace: Bool
    public var workspaceAt: Date?
    public var paused: Bool
    public var pausedAt: Date?
    public var adminChanges: Bool
    public var canAdminChanges: Bool
    public var pendingRequests: Int
    public var standingApprovals: Int
    public var openChangeSets: Int
    /// Whether the person was given an agent; nil on an xbind that ships
    /// none (the field is absent).
    public var access: Bool?
    /// The component whose page is their agent's conversation ("" none).
    public var agent: String
    /// Why it can't run now: "older" | "locked" | "unavailable" | "".
    public var held: String
    public var heldWhy: String
    /// A workspace admin's view of who was given one.
    public var who: Who?
    /// The person's own settings of the web and MCP tools (D239).
    public var web: Bool
    public var mcp: Bool
    public var mcpReadOnly: Bool
    /// What the workspace policy lets them turn on (`net`); empty on an
    /// xbind before D239, which reads as off.
    public struct NetReach: Sendable, Equatable {
        /// "off" | "read" (the web) · "off" | "tiles" (MCP) · "" absent.
        public var policy = ""
        public var why = ""
        /// The sites the web is held to ([] any public site).
        public var only: [String] = []
        /// MCP: the outside servers the person's agent may use (D240).
        public var servers: [OutsideServer] = []
    }
    public var netWeb: NetReach
    public var netMCP: NetReach

    public init?(json: JSONValue?) {
        guard let json, json.objectValue != nil else { return nil }
        user = json["user"]?.stringValue ?? ""
        workspace = json["workspace"]?.boolValue ?? false
        workspaceAt = DelegateTime.date(json["workspaceAt"])
        paused = json["paused"]?.boolValue ?? false
        pausedAt = DelegateTime.date(json["pausedAt"])
        adminChanges = json["adminChanges"]?.boolValue ?? false
        canAdminChanges = json["canAdminChanges"]?.boolValue ?? false
        pendingRequests = Int(json["pendingRequests"]?.intValue ?? 0)
        standingApprovals = Int(json["standingApprovals"]?.intValue ?? 0)
        openChangeSets = Int(json["openChangeSets"]?.intValue ?? 0)
        access = json["access"]?.boolValue
        agent = json["agent"]?.stringValue ?? ""
        held = json["held"]?.stringValue ?? ""
        heldWhy = json["heldWhy"]?.stringValue ?? ""
        if let w = json["who"], w.objectValue != nil {
            who = Who(everyone: w["everyone"]?.boolValue ?? false, entries: Int(w["entries"]?.intValue ?? 0),
                      chosen: w["chosen"]?.boolValue ?? false)
        }
        web = json["web"]?.boolValue ?? false
        mcp = json["mcp"]?.boolValue ?? false
        mcpReadOnly = json["mcpReadOnly"]?.boolValue ?? false
        let reach = { (v: JSONValue?) in
            NetReach(policy: v?["policy"]?.stringValue ?? "", why: v?["why"]?.stringValue ?? "", only: (v?["only"]?.arrayValue ?? []).compactMap(\.stringValue),
                     servers: (v?["servers"]?.arrayValue ?? []).map { OutsideServer(name: $0["name"]?.stringValue ?? $0["server"]?.stringValue ?? "", host: $0["host"]?.stringValue ?? "") })
        }
        netWeb = reach(json["net"]?["web"])
        netMCP = reach(json["net"]?["mcp"])
    }
}

/// An outside MCP server an admin listed (D240), as a person's
/// Preferences show it: its name and host.
public struct OutsideServer: Sendable, Equatable {
    public var name: String
    public var host: String
}

/// Times as xbind writes them (RFC 3339; a time never set is year 1).
public enum DelegateTime {
    public static func date(_ v: JSONValue?) -> Date? {
        guard let v else { return nil }
        if let n = v.doubleValue { return n > 0 ? Date(timeIntervalSince1970: n > 1e12 ? n / 1000 : n) : nil }
        guard let s = v.stringValue, !s.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        var d = f.date(from: s)
        if d == nil {
            f.formatOptions = [.withInternetDateTime]
            d = f.date(from: s)
        }
        guard let d, d.timeIntervalSince1970 > 0 else { return nil }
        return d
    }

    public static func rfc3339(_ d: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.string(from: d)
    }
}

/// One approval request (a tile call, a tile agent's start, a tile's
/// creation), as xbind reads it — the only thing an approval sheet is
/// drawn from.
public struct DelegateRequest: Sendable, Equatable, Identifiable {
    /// `web` and `mcp`: a web page and an MCP tool (D239). `message`: a
    /// text to a tile agent that acts without asking (D250; shown and
    /// decided here since D253). `unknown`: a kind this app doesn't know (a
    /// newer xbind's) — never decided here, as a state it doesn't know isn't.
    public enum Kind: String, Sendable { case call, session, template, web, mcp, message, unknown }
    /// `failed`: a start that was approved and didn't open (D250 review
    /// L3) — tried again from the sheet where `failureRetry` says so.
    /// `unknown`: a state this app doesn't know (a newer xbind's) — never
    /// read as one that can be decided.
    public enum State: String, Sendable { case pending, approved, denied, used, expired, withdrawn, failed, unknown }

    public var id: String
    public var kind: Kind
    public var method: String
    /// The call's path under /api/, with its query.
    public var target: String
    public var tilePath: String
    public var contentType: String
    /// The body's text while the request is pending or approved, when it is text.
    public var body: String
    public var bodyShown: Bool
    public var bodyBinary: Bool
    public var bodySize: Int
    public var bodySha256: String
    /// The agent's own words — shown quoted, as unchecked.
    public var summary: String
    public var preview: JSONValue
    public var state: State
    public var grant: String
    public var created: Date?
    public var decided: Date?
    public var expires: Date?
    public var decidedVia: String
    /// "notification": decided from a notification's action (D241).
    public var decidedFrom: String
    /// The enrolled device whose session decided ("" for any other).
    public var decidedDevice: String
    /// "xbin" for a request xbind withdrew because the agent stopped for
    /// good (switched off, no longer the person's); "" otherwise.
    public var withdrawnBy: String
    /// A start that failed (`failed`): xbind's words for why, and whether
    /// the reason isn't the agent's — its person may try it again.
    public var failureWhy: String
    public var failureRetry: Bool
    /// What the person picked on a start's approval (`{provider, model?,
    /// mode?, remember?, continue?}`), nil when nothing was.
    public var chosen: JSONValue?

    public init?(json: JSONValue?) {
        guard let json, let id = json["id"]?.stringValue, !id.isEmpty else { return nil }
        self.id = id
        kind = Kind(rawValue: json["kind"]?.stringValue ?? "call") ?? .unknown // (a request from before kinds was a call)
        method = json["method"]?.stringValue ?? ""
        target = json["target"]?.stringValue ?? ""
        tilePath = json["tile"]?.stringValue ?? ""
        contentType = json["contentType"]?.stringValue ?? ""
        body = json["body"]?.stringValue ?? ""
        bodyShown = json["bodyShown"]?.boolValue ?? false
        bodyBinary = json["bodyBinary"]?.boolValue ?? false
        bodySize = Int(json["bodySize"]?.intValue ?? 0)
        bodySha256 = json["bodySha256"]?.stringValue ?? ""
        summary = json["summary"]?.stringValue ?? ""
        preview = json["preview"] ?? .object([:])
        state = State(rawValue: json["state"]?.stringValue ?? "") ?? .unknown
        grant = json["grant"]?.stringValue ?? ""
        created = DelegateTime.date(json["created"])
        decided = DelegateTime.date(json["decided"])
        expires = DelegateTime.date(json["expires"])
        decidedVia = json["decidedVia"]?.stringValue ?? ""
        decidedFrom = json["decidedFrom"]?.stringValue ?? ""
        decidedDevice = json["decidedDevice"]?.stringValue ?? ""
        withdrawnBy = json["withdrawnBy"]?.stringValue ?? ""
        failureWhy = json["failure"]?["why"]?.stringValue ?? ""
        failureRetry = json["failure"]?["retry"]?.boolValue ?? false
        chosen = json["chosen"].flatMap { $0.objectValue != nil ? $0 : nil }
    }

    public var isPending: Bool { state == .pending && kind != .unknown }
    /// A start that failed for a reason that isn't the agent's: its person's
    /// Try again (`POST …/retry`) is decided on the sheet, like an approval.
    public var isRetryable: Bool { state == .failed && failureRetry && kind == .session }
    /// The sheet decides it: it waits, or it may be tried again.
    public var isDecidable: Bool { isPending || isRetryable }
    /// `preview.session`: the harness and mode a tile agent will run in.
    public var session: JSONValue { preview["session"] ?? .object([:]) }
    /// A session start whose mode lets the tile agent act without asking —
    /// the ordinary default (D251), a fact the sheet states. Only xbind's
    /// word that the mode asks — a harness's ask or plan mode — counts: a
    /// mode it doesn't vouch for is one that never asks.
    public var neverAsks: Bool { kind == .session && session["asks"]?.boolValue != true }
    public var isResume: Bool {
        guard kind == .session, let r = session["resume"] else { return false }
        if let b = r.boolValue { return b }
        if let s = r.stringValue { return !s.isEmpty }
        return !r.isNull
    }

    public static func list(_ json: JSONValue?) -> [DelegateRequest] {
        (json?["requests"]?.arrayValue ?? []).compactMap { DelegateRequest(json: $0) }
    }

    /// `GET /delegate/requests?failed=1`, apart: the requests that wait,
    /// and the starts that failed and may be tried again (an xbind before
    /// D250 ignores the query: none failed).
    public static func split(_ json: JSONValue?) -> (pending: [DelegateRequest], failed: [DelegateRequest]) {
        let all = list(json)
        return (all.filter { $0.state != .failed }, all.filter { $0.state == .failed })
    }
}

/// A standing approval.
public struct StandingApproval: Sendable, Equatable, Identifiable {
    public var id: String
    public var tile: String
    /// "call" | "tile" | "tool" (an MCP tool: `method` "MCP", `path` the tool).
    public var scope: String
    public var method: String
    public var path: String
    public var until: Date?
    public var created: Date?
    /// An outside MCP server's tool (D240): the server's name and host.
    public var serverName = ""
    public var serverHost = ""

    public init?(json: JSONValue?) {
        guard let json, let id = json["id"]?.stringValue, !id.isEmpty else { return nil }
        self.id = id
        if let s = json["server"], s.objectValue != nil {
            serverName = s["name"]?.stringValue ?? s["server"]?.stringValue ?? ""
            serverHost = s["host"]?.stringValue ?? ""
        }
        tile = json["tile"]?.stringValue ?? ""
        scope = json["scope"]?.stringValue ?? ""
        method = json["method"]?.stringValue ?? ""
        path = json["path"]?.stringValue ?? ""
        until = DelegateTime.date(json["until"])
        created = DelegateTime.date(json["created"])
    }

    public static func list(_ json: JSONValue?) -> [StandingApproval] {
        (json?["approvals"]?.arrayValue ?? []).compactMap { StandingApproval(json: $0) }
    }
}

/// An admin change set an agent proposed.
public struct AdminChangeSet: Sendable, Equatable, Identifiable {
    public struct Item: Sendable, Equatable, Identifiable {
        public var n: Int
        public var kind: String
        /// The agent's reason — quoted, unchecked.
        public var why: String
        /// xbind's sentence, state and warnings for the change.
        public var line: String
        public var before: String?
        public var after: String?
        public var effects: [String]
        public var warnings: [String]
        /// xbind's refusal: the item can't be applied.
        public var blocked: String
        public var hasInverse: Bool
        public var result: String
        public var error: String
        // A plan's line (docs/protocol/delegation.md §Plans).
        /// Applying it takes an admin's rights.
        public var admin: Bool
        /// …and its person is none: xbind's line only; they can ask an admin.
        public var needsAdmin: Bool
        /// The earlier lines whose tiles it names.
        public var needs: [Int]
        /// Described before the tiles it names existed.
        public var deferred: Bool
        /// What an agent.start line starts (a start request's preview.session).
        public var start: JSONValue?
        /// xbind's words for undoing it by hand, when it can't.
        public var undoByHand: String
        /// A line that waits (D243): the tile it waits on to provide
        /// `waitService`; with result "waiting", `waitHeld` is xbind's
        /// words for why it is not bound though the tile provides something.
        public var waitTile: String
        public var waitService: String
        public var waitHeld: String
        /// What an applied builtin.update line made (D248): xbind's out.
        public struct BuiltinOut: Sendable, Equatable {
            /// updated | installed | proposed
            public var update: String
            public var files: Int
            public var tile: String
            /// the change proposal's number on `tile`, when it was filed as one
            public var proposal: Int
        }
        public var builtinOut: BuiltinOut?

        public var id: Int { n }

        init?(json: JSONValue) {
            guard let n = json["n"]?.intValue else { return nil }
            self.n = Int(n)
            kind = json["kind"]?.stringValue ?? ""
            why = json["why"]?.stringValue ?? ""
            line = json["line"]?.stringValue ?? ""
            before = AdminChangeSet.shown(json["before"], said: json["beforeText"])
            after = AdminChangeSet.shown(json["after"], said: json["afterText"])
            effects = (json["effects"]?.arrayValue ?? []).compactMap(\.stringValue)
            warnings = (json["warnings"]?.arrayValue ?? []).compactMap(\.stringValue)
            blocked = json["blocked"]?.stringValue ?? ""
            hasInverse = !(json["inverse"]?.isNull ?? true)
            result = json["result"]?.stringValue ?? ""
            error = json["error"]?.stringValue ?? ""
            admin = json["admin"]?.boolValue ?? false
            needsAdmin = json["needsAdmin"]?.boolValue ?? false
            needs = (json["needs"]?.arrayValue ?? []).compactMap { $0.intValue.map { Int($0) } }
            deferred = json["deferred"]?.boolValue ?? false
            start = json["start"].flatMap { $0.objectValue != nil ? $0 : nil }
            undoByHand = json["undoByHand"]?.stringValue ?? ""
            waitTile = json["wait"]?["tile"]?.stringValue ?? ""
            waitService = json["wait"]?["service"]?.stringValue ?? ""
            waitHeld = json["wait"]?["held"]?.stringValue ?? ""
            if let o = json["out"], o.objectValue != nil, kind == "builtin.update" {
                builtinOut = BuiltinOut(update: o["update"]?.stringValue ?? "", files: Int(o["files"]?.intValue ?? 0),
                                        tile: o["tile"]?.stringValue ?? "", proposal: Int(o["proposal"]?.intValue ?? 0))
            }
        }
    }

    /// `unknown`: a state this app doesn't know — never read as one that can be applied.
    public enum State: String, Sendable { case proposed, applying, applied, partial, rejected, expired, withdrawn, unknown }

    public var id: String
    /// The agent's title — quoted, unchecked.
    public var title: String
    public var undoOf: String
    public var state: State
    public var created: Date?
    public var decided: Date?
    public var expires: Date?
    public var items: [Item]
    /// "xbin" for a proposal xbind withdrew because the agent stopped for good.
    public var withdrawnBy: String
    /// A plan: its lines may create tiles and start tile agents, and are the
    /// person's own to apply or an admin's.
    public var plan: Bool
    /// The person who asks an admin to apply it ("": the person's own agent proposed it).
    public var from: String
    /// For an undo set: what xbind can't undo, in its words.
    public var byHand: [String]

    public init?(json: JSONValue?) {
        guard let json, let id = json["id"]?.stringValue, !id.isEmpty else { return nil }
        self.id = id
        title = json["title"]?.stringValue ?? ""
        undoOf = json["undoOf"]?.stringValue ?? ""
        state = State(rawValue: json["state"]?.stringValue ?? "") ?? .unknown
        created = DelegateTime.date(json["created"])
        decided = DelegateTime.date(json["decided"])
        expires = DelegateTime.date(json["expires"])
        items = (json["items"]?.arrayValue ?? []).compactMap(Item.init(json:))
        withdrawnBy = json["withdrawnBy"]?.stringValue ?? ""
        plan = json["plan"]?.boolValue ?? false
        from = json["from"]?.stringValue ?? ""
        byHand = (json["byHand"]?.arrayValue ?? []).compactMap(\.stringValue)
    }

    public var isProposed: Bool { state == .proposed }

    /// A before/after value as the review shows it (the web's stateText):
    /// xbind's own words when it says them; a string as it is; else the
    /// value's fields, plainly — never a dump the person can't read; nil
    /// when absent.
    static func shown(_ v: JSONValue?, said: JSONValue? = nil) -> String? {
        if let s = said?.stringValue, !s.isEmpty { return s }
        guard let v, !v.isNull else { return nil }
        if let s = v.stringValue { return s }
        guard let o = v.objectValue else { return v.jsonString }
        if o.isEmpty { return "(nothing)" }
        return o.keys.sorted().map { "\($0): \(o[$0]?.jsonString ?? "null")" }.joined(separator: ", ")
    }

    public static func list(_ json: JSONValue?) -> [AdminChangeSet] {
        (json?["changesets"]?.arrayValue ?? []).compactMap { AdminChangeSet(json: $0) }
    }
}

/// One row of the audit log (`GET /delegate/audit`).
public struct AuditRow: Sendable, Equatable {
    public var ts: Date?
    public var method: String
    public var path: String
    public var tile: String
    public var cls: String
    public var effect: Bool
    public var dryRun: Bool
    public var status: Int
    public var refused: String
    public var ticket: String
    public var under: String
    public var n: Int
    /// What a web page read came to (D239).
    public var bytes: Int
    /// An outside MCP server's call or list (D240): the server, the tool.
    public var server: String
    public var tool: String

    public init(json: JSONValue) {
        ts = DelegateTime.date(json["ts"])
        method = json["method"]?.stringValue ?? ""
        path = json["path"]?.stringValue ?? ""
        tile = json["tile"]?.stringValue ?? ""
        cls = json["class"]?.stringValue ?? ""
        effect = json["effect"]?.boolValue ?? false
        dryRun = json["dryRun"]?.boolValue ?? false
        status = Int(json["status"]?.intValue ?? 0)
        refused = json["refused"]?.stringValue ?? ""
        ticket = json["ticket"]?.stringValue ?? ""
        under = json["under"]?.stringValue ?? ""
        n = Int(json["n"]?.intValue ?? 0)
        bytes = Int(json["bytes"]?.intValue ?? 0)
        server = json["server"]?.stringValue ?? ""
        tool = json["tool"]?.stringValue ?? ""
    }

    public static func list(_ json: JSONValue?) -> [AuditRow] {
        (json?["calls"]?.arrayValue ?? []).map(AuditRow.init(json:))
    }
}

/// A tile agent's session as the compact directory lists it
/// (`GET /term/sessions?origin=agent&compact=1[&ids=]`): what the app draws
/// a `session` node from.
public struct AgentSessionRow: Sendable, Equatable, Identifiable {
    public var id: String
    public var tile: String
    public var provider: String
    public var mode: String
    public var status: String
    public var origin: String
    public var pending: Int
    public var questions: Int
    public var turn: Int
    public var last: String
    /// An id that isn't live any more: "exit" | "restart" ("" while live).
    public var endedBy: String

    public init?(json: JSONValue) {
        guard let id = json["id"]?.stringValue, !id.isEmpty else { return nil }
        self.id = id
        tile = json["cwd"]?.stringValue ?? ""
        provider = json["provider"]?.stringValue ?? ""
        mode = json["mode"]?.stringValue ?? ""
        status = json["status"]?.stringValue ?? ""
        origin = json["origin"]?.stringValue ?? ""
        pending = Int(json["pending"]?.intValue ?? 0)
        questions = Int(json["questions"]?.intValue ?? 0)
        turn = Int(json["turn"]?.intValue ?? 0)
        last = json["last"]?.stringValue ?? ""
        endedBy = json["endedBy"]?.stringValue ?? ""
    }

    public var waits: Bool { status != "ended" && (pending > 0 || questions > 0 || status == "waiting_permission") }
    public var isLive: Bool { status != "ended" && status != "exited" }

    public static func list(_ json: JSONValue?) -> [AgentSessionRow] {
        (json?.arrayValue ?? []).compactMap(AgentSessionRow.init(json:))
    }
}

/// The routes of the person's own session (docs/protocol/delegation.md).
/// Rows are paths below `/api/xbin`.
public enum DelegateRoute {
    static let base = "/api/xbin/delegate"
    /// One path segment: percent-encoded, a dot too — so even an id that
    /// slipped past ``DelegateID`` can't be `.` or `..` to a URL parser.
    static func seg(_ s: String) -> String { URLComponent.encode(s).replacingOccurrences(of: ".", with: "%2E") }

    public static let state = APIRequest("GET", "\(base)/state")
    public static let requests = APIRequest("GET", "\(base)/requests")
    /// The pending requests and the starts that failed and may be tried
    /// again (D250 review L3), in one list (``DelegateRequest/split(_:)``).
    public static let requestsAndFailed = APIRequest("GET", "\(base)/requests?failed=1")
    public static let changesets = APIRequest("GET", "\(base)/changesets")
    public static let approvals = APIRequest("GET", "\(base)/approvals")
    /// Whether a language model is connected (`{configured, host?}`).
    public static let llm = APIRequest("GET", "/api/xbin/llm")
    /// The sealed rows of the catalog: the app asks for them only to find
    /// its person's agent — a listing without the query never has them.
    public static let sealedComponents = APIRequest("GET", "/api/xbin/components?sealed=1")

    public static func request(_ id: String) -> APIRequest { APIRequest("GET", "\(base)/requests/\(seg(id))") }
    public static func changeset(_ id: String) -> APIRequest { APIRequest("GET", "\(base)/changesets/\(seg(id))") }

    /// Approve: `{grant, scope?, unseen?}` (``DelegationWords/approveBody``).
    public static func approve(_ id: String, _ body: JSONValue) -> APIRequest {
        .json("POST", "\(base)/requests/\(seg(id))/approve", body)
    }
    public static func deny(_ id: String) -> APIRequest { .json("POST", "\(base)/requests/\(seg(id))/deny", .object([:])) }
    /// Try again: a start that failed for a reason not the agent's is a
    /// ticket again — `{harness?, model?, mode?, knows?, seen?}`, the picks
    /// validated as an approval's (``StartWords/sheetBody``).
    public static func retry(_ id: String, _ body: JSONValue) -> APIRequest {
        .json("POST", "\(base)/requests/\(seg(id))/retry", body)
    }

    /// From a notification's action (D241): Allow once — the only grant one
    /// may give — and Deny, each saying where it was decided.
    public static func approveFromNotification(_ id: String) -> APIRequest {
        .json("POST", "\(base)/requests/\(seg(id))/approve", ["grant": "once", "via": "notification"])
    }
    public static func denyFromNotification(_ id: String) -> APIRequest {
        .json("POST", "\(base)/requests/\(seg(id))/deny", ["via": "notification"])
    }
    public static func rejectFromNotification(_ id: String) -> APIRequest {
        .json("POST", "\(base)/changesets/\(seg(id))/reject", ["via": "notification"])
    }
    public static func takeBack(_ id: String) -> APIRequest { APIRequest("DELETE", "\(base)/approvals/\(seg(id))") }

    /// Apply the ticked items of a change set (never an empty list: xbind
    /// applies nothing for one, and the app rejects instead).
    public static func apply(_ id: String, items: [Int], password: String = "") -> APIRequest {
        // knows: the line kinds newer than every client's that this app shows for what they are —
        // xbind applies a line of one only then, so an older app never applies one (D246)
        var o: [String: JSONValue] = ["items": .array(items.map { .int(Int64($0)) }), "knows": .array(PlanWords.newerKinds.map { .string($0) })]
        if !password.isEmpty { o["password"] = .string(password) }
        return .json("POST", "\(base)/changesets/\(seg(id))/approve", .object(o))
    }
    public static func reject(_ id: String) -> APIRequest { .json("POST", "\(base)/changesets/\(seg(id))/reject", .object([:])) }
    public static func undo(_ id: String) -> APIRequest { .json("POST", "\(base)/changesets/\(seg(id))/undo", .object([:])) }
    /// Sends the lines of a plan that need an admin to the workspace's
    /// admins, as a request from the person (their own tap).
    public static func ask(_ id: String) -> APIRequest { .json("POST", "\(base)/changesets/\(seg(id))/ask", .object([:])) }

    public static func setPaused(_ paused: Bool) -> APIRequest { .json("PUT", "\(base)/state", ["paused": .bool(paused)]) }
    public static func setAdminChanges(_ on: Bool) -> APIRequest { .json("PUT", "\(base)/state", ["adminChanges": .bool(on)]) }
    /// One of the web and MCP settings (``NetPref/id``: web, mcp, mcpReadOnly).
    public static func setNet(_ id: String, _ on: Bool) -> APIRequest? {
        ["web", "mcp", "mcpReadOnly"].contains(id) ? .json("PUT", "\(base)/state", .object([id: .bool(on)])) : nil
    }

    public static func audit(before: Date? = nil) -> APIRequest {
        APIRequest("GET", "\(base)/audit" + (before.map { "?before=\(seg(DelegateTime.rfc3339($0)))" } ?? ""))
    }

    /// The person's own saved sign-ins (metadata only): what a follow-up's
    /// chip names a sign-in its session carries by (``savedSignInNames(_:)``).
    public static let signins = APIRequest("GET", "/api/xbin/agent/signins")

    /// `GET /agent/signins`: each saved sign-in's name by id — the person's
    /// own labels, which a sheet shows beside "uses your … sign-in".
    public static func savedSignInNames(_ json: JSONValue?) -> [String: String] {
        var out: [String: String] = [:]
        for h in json?["harnesses"]?.arrayValue ?? [] {
            for sg in h["signins"]?.arrayValue ?? [] {
                if let id = sg["id"]?.stringValue, !id.isEmpty, let name = sg["name"]?.stringValue, !name.isEmpty { out[id] = name }
            }
        }
        return out
    }

    public static let prefs = APIRequest("GET", "/api/xbin/agent/prefs")
    public static func setPrefs(_ patch: JSONValue) -> APIRequest { .json("PUT", "/api/xbin/agent/prefs", patch) }
    public static let providers = APIRequest("GET", "/api/xbin/agent/providers")

    /// The tile agents the person's agent started, compact, with the ended
    /// ones among `ids`.
    public static func agentSessions(ids: [String] = []) -> APIRequest {
        let clean = ids.filter { $0.range(of: "^[A-Za-z0-9_-]{1,64}$", options: .regularExpression) != nil }
        return APIRequest("GET", "/api/xbin/term/sessions?origin=agent&compact=1" + (clean.isEmpty ? "" : "&ids=" + clean.joined(separator: ",")))
    }

    /// Whether `path` is a route only the person's own session may send — a
    /// grant. The scheme handler's rules never give a tile's page or a
    /// native tile's runtime the session for anything (TileScheme); this is
    /// what the tests name when they check that.
    public static func isGrant(method: String, path: String) -> Bool {
        let p = path.split(separator: "?", maxSplits: 1).first.map(String.init) ?? path
        guard p.hasPrefix("\(base)/") else { return false }
        let m = method.uppercased()
        if m == "GET" || m == "HEAD" { return p.hasPrefix("\(base)/audit") }
        if p.hasSuffix("/approve") || p.hasSuffix("/deny") || p.hasSuffix("/reject") || p.hasSuffix("/undo") || p.hasSuffix("/retry") { return true }
        if p.hasPrefix("\(base)/approvals") { return true }
        if p == "\(base)/workspace" { return true }
        // PUT /delegate/state: the page may pause; resuming and the admin opt-in are grants (xbind tells them apart)
        return p == "\(base)/state" && m == "PUT"
    }
}

/// What an approve or apply answered.
public enum DelegateOutcome: Sendable, Equatable {
    case done(JSONValue)
    /// No longer pending (someone decided, it expired, it was withdrawn).
    case gone(String)
    /// A change set needs a recent sign-in first: "password" | "signin".
    case stepUp(String, wrongPassword: Bool)
    /// A body nobody could read needs `unseen: true`.
    case unseen(size: Int, sha256: String)
    /// Nothing can be allowed or applied now: the agent is stopped
    /// (`refusal`: workspace-off, paused, held, no-agent, …). Say why where
    /// Allow was; never send it again.
    case stopped(refusal: String, message: String)
    /// What the sheet showed changed since (409 `{changed, reload: true}`:
    /// a sign-in, the network, the continued session, how much of the text
    /// was on screen): nothing was decided — read the request again, show
    /// it as it is now, and say xbind's words (D250 review L2, D252).
    case changed(what: String, message: String)
    case failed(String)

    /// Reads an answer to approve / deny / apply.
    public static func read(_ r: APIResponse, sentPassword: Bool = false) -> DelegateOutcome {
        let j = (try? r.json()) ?? .null
        if r.isSuccess { return .done(j) }
        let msg = j["error"]?.stringValue ?? APIError.statusText(r.status)
        if r.status == 403, let s = j["stepUp"]?.stringValue, !s.isEmpty { return .stepUp(s, wrongPassword: sentPassword) }
        if r.status == 409, j["unseen"]?.boolValue == true {
            return .unseen(size: Int(j["bodySize"]?.intValue ?? 0), sha256: j["bodySha256"]?.stringValue ?? "")
        }
        if r.status == 409, let refusal = j["refusal"]?.stringValue, !refusal.isEmpty { return .stopped(refusal: refusal, message: msg) }
        if r.status == 409, j["reload"]?.boolValue == true {
            return .changed(what: j["changed"]?.stringValue ?? "", message: j["error"]?.stringValue ?? "It changed since your sheet showed it")
        }
        if r.status == 409 || r.status == 404 { return .gone(msg) }
        return .failed(msg)
    }
}

/// A `delegate` event on `/ws/events`: refetch the list it names.
public struct DelegateEvent: Sendable, Equatable {
    /// request | approval | changeset | state.
    public var op: String
    public var id: String

    public init(op: String, id: String = "") {
        self.op = op
        self.id = id
    }
}
