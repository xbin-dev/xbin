import Foundation

// The agent-session REST models (docs/protocol.md §/api/xbin: agent/providers,
// term/sessions, agent/history). Each keeps the JSON it was read from
// (`json`) — Codable round-trips exactly and a field a newer xbind adds is
// never lost — and exposes the typed fields a screen binds to. Reading is
// lenient: a missing or mistyped field reads as its zero value.

/// One coding agent the daemon runs (`GET /agent/providers`).
public struct AgentProvider: JSONBacked, Sendable, Hashable, Identifiable {
    public let json: JSONValue
    public let id: String
    public let name: String
    /// Modes in display order; `explicit` ones are never defaults (bypass, full access).
    public let modes: [SessionMode]
    public let defaultMode: String
    /// The shell command that signs the CLI in (run in a terminal sharing the agent's $HOME).
    public let login: String

    public init?(json: JSONValue) {
        guard let id = json["id"]?.string, !id.isEmpty else { return nil }
        self.json = json
        self.id = id
        name = json["name"]?.string ?? id
        modes = json["modes"]?.list(SessionMode.self) ?? []
        defaultMode = json["defaultMode"]?.string ?? ""
        login = json["login"]?.string ?? ""
    }
}

/// A permission mode: a provider's (`/agent/providers`) or the agent's own
/// (`status.modes`, ACP SessionMode).
public struct SessionMode: JSONBacked, Sendable, Hashable, Identifiable {
    public let json: JSONValue
    public let id: String
    public let name: String
    public let description: String?
    /// Never a default; must be asked for by name (bypass permissions, full access).
    public let explicit: Bool

    public init?(json: JSONValue) {
        guard let id = json["id"]?.string, !id.isEmpty else { return nil }
        self.json = json
        self.id = id
        name = json["name"]?.string ?? id
        description = json["description"]?.string
        explicit = json["explicit"]?.bool ?? false
    }
}

/// A network scope the caller may pick for a sandbox (SessionInfo.scopes).
public struct SessionScope: JSONBacked, Sendable, Hashable, Identifiable {
    public let json: JSONValue
    public let id: String
    public let label: String
    public let desc: String

    public init?(json: JSONValue) {
        guard let id = json["id"]?.string else { return nil }
        self.json = json
        self.id = id
        label = json["label"]?.string ?? id
        desc = json["desc"]?.string ?? ""
    }
}

/// A terminal session's directory row (`GET /term/sessions`, the create's
/// answer, `session` in `GET /term/sessions/<id>`). Shells and agents share it.
public struct SessionInfo: JSONBacked, Sendable, Hashable, Identifiable {
    public let json: JSONValue
    public let id: String
    public let cwd: String
    public let net: String
    public let label: String
    public let scopes: [SessionScope]
    public let gpu: String
    public let api: Bool
    public let name: String
    public let created: String
    public let lastActive: String
    public let clients: Int
    public let envHeld: Bool
    /// "shell" | "agent".
    public let kind: String
    public let vm: Bool
    public let provider: String
    public let mode: String
    public let model: String
    /// The agent's status (agent sessions only).
    public let status: SessionStatus?
    /// Unanswered permission requests.
    public let pending: Int

    public init?(json: JSONValue) {
        guard let id = json["id"]?.string, !id.isEmpty else { return nil }
        self.json = json
        self.id = id
        cwd = json["cwd"]?.string ?? ""
        net = json["net"]?.string ?? ""
        label = json["label"]?.string ?? ""
        scopes = json["scopes"]?.list(SessionScope.self) ?? []
        gpu = json["gpu"]?.string ?? ""
        api = json["api"]?.bool ?? false
        name = json["name"]?.string ?? ""
        created = json["created"]?.string ?? ""
        lastActive = json["lastActive"]?.string ?? ""
        clients = json["clients"]?.int ?? 0
        envHeld = json["envHeld"]?.bool ?? false
        kind = json["kind"]?.string ?? "shell"
        vm = json["vm"]?.bool ?? false
        provider = json["provider"]?.string ?? ""
        mode = json["mode"]?.string ?? ""
        model = json["model"]?.string ?? ""
        status = json["status"]?.string.map(SessionStatus.init(rawValue:))
        self.pending = json["pending"]?.int ?? 0
    }

    public var isAgent: Bool { kind == "agent" }
    public var createdDate: Date? { parseTime(created) }
    public var lastActiveDate: Date? { parseTime(lastActive) }
}

/// A request the agent waits on, as `GET /term/sessions/<id>` lists it
/// (`permissions`): no rule or meta — `request` derives the rule the way the
/// daemon does.
public struct PendingPermission: JSONBacked, Sendable, Hashable, Identifiable {
    public let json: JSONValue
    public let pid: String
    public let toolCall: ToolCallRef
    public let options: [PermissionOption]
    public var id: String { pid }

    public init?(json: JSONValue) {
        guard let pid = json["pid"]?.string, !pid.isEmpty else { return nil }
        self.json = json
        self.pid = pid
        toolCall = json["toolCall"].flatMap(ToolCallRef.init(json:)) ?? ToolCallRef(id: "")
        options = json["options"]?.list(PermissionOption.self) ?? []
    }

    /// The same request as a permission.request event would carry it.
    public var request: PermissionRequest {
        PermissionRequest(pid: pid, toolCall: toolCall, options: options,
                          rule: PermissionRule(kind: toolCall.kind, title: toolCall.title, scoped: toolCall.ruleScopable), meta: nil)
    }
}

/// `GET /term/sessions/<id>` → {session, permissions}.
public struct SessionSnapshot: JSONBacked, Sendable, Hashable {
    public let json: JSONValue
    public let session: SessionInfo
    public let permissions: [PendingPermission]

    public init?(json: JSONValue) {
        guard let s = json["session"].flatMap(SessionInfo.init(json:)) else { return nil }
        self.json = json
        session = s
        permissions = json["permissions"]?.list(PendingPermission.self) ?? []
    }
}

/// `GET /term/sessions/<id>/events?since=` → {events, next, truncated}.
public struct EventsPage: JSONBacked, Sendable, Hashable {
    public let json: JSONValue
    public let events: [AgentEvent]
    public let next: UInt64
    /// The cursor predated the oldest kept event: earlier events were dropped.
    public let truncated: Bool

    public init?(json: JSONValue) {
        guard json.object != nil else { return nil }
        self.json = json
        events = json["events"]?.list(AgentEvent.self) ?? []
        next = json["next"]?.uint64 ?? events.last?.seq ?? 0
        truncated = json["truncated"]?.bool ?? false
    }
}

/// A past agent session (`GET /agent/history`, `meta` of its transcript).
public struct HistoryEntry: JSONBacked, Sendable, Hashable, Identifiable {
    public let json: JSONValue
    public let id: String
    public let cwd: String
    public let provider: String
    public let mode: String
    public let name: String
    public let created: String
    public let ended: String
    public let turns: Int
    /// The first prompt, one line.
    public let preview: String
    public let acpSessionId: String
    /// The agent can reopen it: `POST /term/sessions {resume: id}`.
    public let loadable: Bool

    public init?(json: JSONValue) {
        guard let id = json["id"]?.string, !id.isEmpty else { return nil }
        self.json = json
        self.id = id
        cwd = json["cwd"]?.string ?? ""
        provider = json["provider"]?.string ?? ""
        mode = json["mode"]?.string ?? ""
        name = json["name"]?.string ?? ""
        created = json["created"]?.string ?? ""
        ended = json["ended"]?.string ?? ""
        turns = json["turns"]?.int ?? 0
        preview = json["preview"]?.string ?? ""
        acpSessionId = json["acpSessionId"]?.string ?? ""
        loadable = json["loadable"]?.bool ?? false
    }

    public var createdDate: Date? { parseTime(created) }
    public var endedDate: Date? { parseTime(ended) }
}

/// `GET /agent/history/<id>/events` → {meta, events}: a past session's
/// transcript in the live event shape, read-only.
public struct HistoryTranscript: JSONBacked, Sendable, Hashable {
    public let json: JSONValue
    public let meta: HistoryEntry
    public let events: [AgentEvent]

    public init?(json: JSONValue) {
        guard let m = json["meta"].flatMap(HistoryEntry.init(json:)) else { return nil }
        self.json = json
        meta = m
        events = json["events"]?.list(AgentEvent.self) ?? []
    }
}

/// `POST /term/sessions/<id>/prompt` → {ok, turn}.
public struct PromptAccepted: Sendable, Hashable {
    public let turn: Int
    public init(turn: Int) { self.turn = turn }
}

/// `POST /term/sessions/<id>/restart` → {session, resumed}.
public struct RestartResult: Sendable, Hashable {
    public let session: SessionInfo
    /// The agent reopened the conversation (session/load); false: a fresh one.
    public let resumed: Bool
}

// MARK: - Requests

/// `POST /term/sessions` for an agent: everything optional but cwd and provider.
public struct CreateAgentSession: Sendable, Hashable {
    public var cwd: String
    public var provider: String
    public var mode: String?
    public var model: String?
    public var options: [String: String]
    /// Sandbox pickers: network scope ("internet", "none", "host", "set:<name>" …).
    public var net: String?
    /// false: a code-only sandbox (no terminal token).
    public var api: Bool?
    public var gpu: String?
    public var vm: Bool?
    public var name: String?
    /// A past session id (`HistoryEntry.id`) to reopen.
    public var resume: String?

    public init(cwd: String, provider: String, mode: String? = nil, model: String? = nil, options: [String: String] = [:],
                net: String? = nil, api: Bool? = nil, gpu: String? = nil, vm: Bool? = nil, name: String? = nil, resume: String? = nil)
    {
        self.cwd = cwd
        self.provider = provider
        self.mode = mode
        self.model = model
        self.options = options
        self.net = net
        self.api = api
        self.gpu = gpu
        self.vm = vm
        self.name = name
        self.resume = resume
    }

    public var body: JSONValue {
        var o = JSONObject([("cwd", .string(cwd)), ("kind", "agent"), ("provider", .string(provider))])
        if let mode, !mode.isEmpty { o["mode"] = .string(mode) }
        if let model, !model.isEmpty { o["model"] = .string(model) }
        if !options.isEmpty {
            o["options"] = .object(JSONObject(options.keys.sorted().map { ($0, .string(options[$0]!)) }))
        }
        if let net, !net.isEmpty { o["net"] = .string(net) }
        if let api { o["api"] = .bool(api) }
        if let gpu, !gpu.isEmpty { o["gpu"] = .string(gpu) }
        if let vm { o["vm"] = .bool(vm) }
        if let name, !name.isEmpty { o["name"] = .string(String(name.prefix(64))) }
        if let resume, !resume.isEmpty { o["resume"] = .string(resume) }
        return .object(o)
    }
}

/// `POST /term/sessions/<id>/restart`: other sandbox pickers (nil = keep).
public struct RestartOptions: Sendable, Hashable {
    public var net: String?
    public var api: Bool?
    public var gpu: String?
    public var vm: Bool?

    public init(net: String? = nil, api: Bool? = nil, gpu: String? = nil, vm: Bool? = nil) {
        self.net = net
        self.api = api
        self.gpu = gpu
        self.vm = vm
    }

    public var body: JSONValue {
        var o = JSONObject()
        if let net { o["net"] = .string(net) }
        if let api { o["api"] = .bool(api) }
        if let gpu { o["gpu"] = .string(gpu) }
        if let vm { o["vm"] = .bool(vm) }
        return .object(o)
    }
}

/// How a permission is answered: one of the agent's options, or a decision
/// (an option kind — the daemon picks that kind's first option).
public enum PermissionAnswer: Sendable, Hashable {
    case option(String)
    case decision(PermissionOptionKind)

    public var body: JSONValue {
        switch self {
        case .option(let id): return ["optionId": .string(id)]
        case .decision(let k): return ["decision": .string(k.rawValue)]
        }
    }
}

/// An answer to a question (elicitation): accept with the form's values,
/// decline (skip) or cancel.
public enum QuestionAction: String, Sendable, Hashable {
    case accept, decline, cancel
}

func parseTime(_ s: String) -> Date? {
    guard !s.isEmpty else { return nil }
    return try? Date(s, strategy: .iso8601)
}
