import Foundation

// The typed readings of each event type's `data` (docs/protocol.md §Agent
// session events). Lenient: a missing or mistyped field reads as absent.

public struct MessageDelta: Sendable, Hashable {
    public var role: MessageRole
    public var text: String
    public var messageId: String
    /// The subagent call (`tool.call` with `subagent`) the text came from.
    public var parent: String
    /// A prompt's files (the user's delta of a prompt that carried them).
    public var attachments: [MessageAttachment]

    public init(role: MessageRole = .agent, text: String, messageId: String = "", parent: String = "",
                attachments: [MessageAttachment] = []) {
        self.role = role
        self.text = text
        self.messageId = messageId
        self.parent = parent
        self.attachments = attachments
    }

    init(json d: JSONValue) {
        role = MessageRole(rawValue: d["role"]?.string ?? "")
        text = d["text"]?.text ?? ""
        messageId = d["messageId"]?.string ?? ""
        parent = d["parent"]?.string ?? ""
        attachments = (d["attachments"]?.array ?? []).compactMap(MessageAttachment.init(json:))
    }
}

/// A file a prompt carried, as the log keeps it (never the bytes):
/// `{name, mime, size, inline?}` — `inline` when the model got it with the
/// prompt (an image block, embedded text), else the agent was pointed at
/// the file.
public struct MessageAttachment: Sendable, Hashable {
    public var name: String
    public var mime: String
    public var size: Int
    public var inline: Bool

    public init(name: String, mime: String = "", size: Int = 0, inline: Bool = false) {
        self.name = name
        self.mime = mime
        self.size = size
        self.inline = inline
    }

    init?(json a: JSONValue) {
        guard let name = a["name"]?.string else { return nil }
        self.init(name: name, mime: a["mime"]?.string ?? "", size: a["size"]?.int ?? 0, inline: a["inline"]?.bool ?? false)
    }
}

public struct ThoughtDelta: Sendable, Hashable {
    public var text: String
    public var parent: String

    init(json d: JSONValue) {
        text = d["text"]?.text ?? ""
        parent = d["parent"]?.string ?? ""
    }
}

public struct PlanEntry: JSONReadable, Sendable, Hashable {
    public var content: String
    public var priority: String
    public var status: PlanEntryStatus

    public init(content: String, priority: String = "", status: PlanEntryStatus) {
        self.content = content
        self.priority = priority
        self.status = status
    }

    public init?(json: JSONValue) {
        guard json.object != nil else { return nil }
        content = json["content"]?.text ?? ""
        priority = json["priority"]?.string ?? ""
        status = PlanEntryStatus(rawValue: json["status"]?.string ?? "pending")
    }

    /// The web's checklist glyph: ✓ done, ▸ in progress, ○ to do.
    public var glyph: String { status == .completed ? "✓" : status == .inProgress ? "▸" : "○" }
}

/// One item of a tool call's content (ACP ToolCallContent).
public enum ToolContent: Sendable, Hashable {
    /// `{type:"content", content:{type:"text", text}}` (often markdown — the adapters fence output).
    case text(String)
    /// `{type:"diff", path, oldText, newText}` — oldText absent for a new file.
    case diff(path: String, oldText: String?, newText: String)
    /// `{type:"terminal", terminalId}` — the output rides the call's `output`.
    case terminal(id: String)
    /// `{type:"content", content:{type:"image", data, mimeType}}`.
    case image(mimeType: String, base64: String)
    /// Anything else, kept as sent.
    case other(JSONValue)

    public init(json it: JSONValue) {
        switch it["type"]?.string {
        case "diff":
            self = .diff(path: it["path"]?.string ?? "", oldText: it["oldText"]?.string, newText: it["newText"]?.string ?? "")
        case "terminal":
            self = .terminal(id: it["terminalId"]?.string ?? "")
        case "text":
            self = .text(it["text"]?.text ?? "")
        default:
            if let inner = it["content"], inner.object != nil {
                if let t = inner["text"]?.string {
                    self = .text(t)
                } else if inner["type"]?.string == "image", let data = inner["data"]?.string {
                    self = .image(mimeType: inner["mimeType"]?.string ?? "", base64: data)
                } else {
                    self = .other(it)
                }
            } else {
                self = .other(it)
            }
        }
    }

    public var text: String? { if case .text(let s) = self { return s }; return nil }
}

public struct ToolLocation: JSONReadable, Sendable, Hashable {
    public var path: String
    public var line: Int?

    public init?(json: JSONValue) {
        guard let p = json["path"]?.string else { return nil }
        path = p
        line = json["line"]?.int
    }
}

/// A tool.call / tool.update payload: a partial record of call `id` — only
/// the fields present change (see `ToolCall.fold`).
public struct ToolCallUpdate: Sendable, Hashable {
    public var id: String
    public var title: String?
    public var kind: ToolKind?
    public var status: ToolStatus?
    /// Replaces the call's content when present.
    public var content: [ToolContent]?
    public var locations: [ToolLocation]?
    public var rawInput: JSONValue?
    public var rawOutput: JSONValue?
    /// The tool's own name (Bash, ExitPlanMode, Task, …).
    public var name: String?
    /// A human headline the harness wrote (Claude's description of a command).
    public var label: String?
    /// The subagent call this one runs under.
    public var parent: String?
    /// A subagent (Task/Agent) call itself.
    public var subagent: Bool
    /// Codex's plan approval.
    public var planReview: Bool
    /// The command's whole output (replaces).
    public var output: String?
    /// A chunk of the command's output (appends).
    public var outputDelta: String?
    public var exitCode: Int?

    public init(id: String, title: String? = nil, kind: ToolKind? = nil, status: ToolStatus? = nil, content: [ToolContent]? = nil,
                locations: [ToolLocation]? = nil, rawInput: JSONValue? = nil, rawOutput: JSONValue? = nil, name: String? = nil,
                label: String? = nil, parent: String? = nil, subagent: Bool = false, planReview: Bool = false,
                output: String? = nil, outputDelta: String? = nil, exitCode: Int? = nil)
    {
        self.id = id
        self.title = title
        self.kind = kind
        self.status = status
        self.content = content
        self.locations = locations
        self.rawInput = rawInput
        self.rawOutput = rawOutput
        self.name = name
        self.label = label
        self.parent = parent
        self.subagent = subagent
        self.planReview = planReview
        self.output = output
        self.outputDelta = outputDelta
        self.exitCode = exitCode
    }

    public init?(json d: JSONValue) {
        guard d.object != nil else { return nil }
        id = d["id"]?.text ?? ""
        title = d["title"].flatMap { $0.isNull ? nil : ($0.text ?? $0.compactString) }
        kind = d["kind"]?.string.map(ToolKind.init(rawValue:))
        status = d["status"]?.string.map(ToolStatus.init(rawValue:))
        if let c = d["content"], !c.isNull { content = (c.array ?? []).map(ToolContent.init(json:)) }
        if let l = d["locations"], !l.isNull { locations = l.list(ToolLocation.self) ?? [] }
        rawInput = d["rawInput"].flatMap { $0.isNull ? nil : $0 }
        rawOutput = d["rawOutput"].flatMap { $0.isNull ? nil : $0 }
        name = d["name"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        label = d["label"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        parent = d["parent"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        subagent = d["subagent"]?.truthy ?? false
        planReview = d["planReview"]?.truthy ?? false
        output = d["output"]?.string
        outputDelta = d["outputDelta"]?.string
        exitCode = d["exitCode"]?.int
    }
}

/// What a permission request is about (only `id` is guaranteed).
public struct ToolCallRef: JSONReadable, Sendable, Hashable {
    public var id: String
    public var name: String
    public var title: String
    public var kind: String
    public var rawInput: JSONValue?
    public var content: [ToolContent]

    public init(id: String, name: String = "", title: String = "", kind: String = "", rawInput: JSONValue? = nil, content: [ToolContent] = []) {
        self.id = id
        self.name = name
        self.title = title
        self.kind = kind
        self.rawInput = rawInput
        self.content = content
    }

    public init?(json: JSONValue) {
        guard json.object != nil else { return nil }
        id = json["id"]?.text ?? ""
        name = json["name"]?.string ?? ""
        title = json["title"]?.text ?? ""
        kind = json["kind"]?.string ?? ""
        rawInput = json["rawInput"].flatMap { $0.isNull ? nil : $0 }
        content = (json["content"]?.array ?? []).map(ToolContent.init(json:))
    }

    /// Whether "allow for the session" can be scoped to this call — the
    /// daemon's ToolCallRef.Rule(): it has a kind or a title, and is not a
    /// mode switch (a plan approval is never remembered, D77).
    public var ruleScopable: Bool { kind != "switch_mode" && (!kind.isEmpty || !title.isEmpty) }
}

public struct PermissionOption: JSONReadable, Sendable, Hashable, Identifiable {
    public var optionId: String
    public var name: String
    public var kind: PermissionOptionKind
    public var id: String { optionId }

    public init(optionId: String, name: String, kind: PermissionOptionKind) {
        self.optionId = optionId
        self.name = name
        self.kind = kind
    }

    public init?(json: JSONValue) {
        guard let id = json["optionId"]?.text else { return nil }
        optionId = id
        name = json["name"]?.string ?? ""
        kind = PermissionOptionKind(rawValue: json["kind"]?.string ?? "")
    }
}

/// What "allow for the session" would remember; `scoped:false` = no rule is
/// possible and the choice is hidden.
public struct PermissionRule: Sendable, Hashable {
    public var kind: String
    public var title: String
    public var scoped: Bool

    public init(kind: String, title: String, scoped: Bool) {
        self.kind = kind
        self.title = title
        self.scoped = scoped
    }
}

/// The adapter's presentation hint (`_meta.permission`).
public struct PermissionMeta: Sendable, Hashable {
    public var title: String?
    public var description: String?
    /// Put the rejecting options first.
    public var defaultToNo: Bool

    public init(title: String? = nil, description: String? = nil, defaultToNo: Bool = false) {
        self.title = title
        self.description = description
        self.defaultToNo = defaultToNo
    }
}

public struct PermissionRequest: Sendable, Hashable {
    public var pid: String
    public var toolCall: ToolCallRef
    public var options: [PermissionOption]
    public var rule: PermissionRule?
    public var meta: PermissionMeta?

    public init(pid: String, toolCall: ToolCallRef, options: [PermissionOption], rule: PermissionRule?, meta: PermissionMeta?) {
        self.pid = pid
        self.toolCall = toolCall
        self.options = options
        self.rule = rule
        self.meta = meta
    }

    public init?(json d: JSONValue) {
        guard let pid = d["pid"]?.text, !pid.isEmpty else { return nil }
        self.pid = pid
        toolCall = d["toolCall"].flatMap(ToolCallRef.init(json:)) ?? ToolCallRef(id: "")
        options = d["options"]?.list(PermissionOption.self) ?? []
        if let r = d["rule"], r.object != nil {
            // a rule without `scoped` hides the choice, as the web reads it (`b.rule.scoped`)
            rule = PermissionRule(kind: r["kind"]?.string ?? "", title: r["title"]?.text ?? "", scoped: r["scoped"]?.bool ?? false)
        }
        if let m = d["meta"], m.object != nil {
            meta = PermissionMeta(title: m["title"]?.string, description: m["description"]?.string, defaultToNo: m["defaultToNo"]?.truthy ?? false)
        }
    }
}

public struct PermissionResolved: Sendable, Hashable {
    public var pid: String
    /// Empty when cancelled.
    public var optionId: String
    public var by: Settler

    public init?(json d: JSONValue) {
        guard let pid = d["pid"]?.text, !pid.isEmpty else { return nil }
        self.pid = pid
        optionId = d["optionId"]?.text ?? ""
        by = Settler(d["by"]?.string ?? "")
    }
}

public struct ElicitationRequest: Sendable, Hashable {
    public var eid: String
    public var toolCallId: String
    public var message: String
    /// A flat JSON Schema object (see `FormField.fields(schema:)`).
    public var schema: JSONValue?

    public init?(json d: JSONValue) {
        guard let eid = d["eid"]?.text, !eid.isEmpty else { return nil }
        self.eid = eid
        toolCallId = d["toolCallId"]?.string ?? ""
        message = d["message"]?.text ?? ""
        schema = d["schema"].flatMap { $0.isNull ? nil : $0 }
    }
}

public struct ElicitationResolved: Sendable, Hashable {
    public var eid: String
    public var action: ElicitationAction
    public var by: Settler
    /// The submitted values (with accept).
    public var content: JSONValue?

    public init?(json d: JSONValue) {
        guard let eid = d["eid"]?.text, !eid.isEmpty else { return nil }
        self.eid = eid
        action = ElicitationAction(rawValue: d["action"]?.string ?? "")
        by = Settler(d["by"]?.string ?? "")
        content = d["content"].flatMap { $0.isNull ? nil : $0 }
    }
}

public struct FileChange: JSONReadable, Sendable, Hashable, Identifiable {
    public var path: String
    /// A rename's source.
    public var oldPath: String?
    public var status: FileChangeStatus
    public var add: Int
    public var del: Int
    public var binary: Bool
    public var id: String { path }

    public init?(json: JSONValue) {
        guard let p = json["path"]?.string else { return nil }
        path = p
        oldPath = json["oldPath"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        status = FileChangeStatus(rawValue: json["status"]?.string ?? "")
        add = json["add"]?.int ?? 0
        del = json["del"]?.int ?? 0
        binary = json["binary"]?.bool ?? false
    }
}

/// A git patch of what changed, capped (64 KiB per call, 192 KiB per turn).
public struct FilesPatch: Sendable, Hashable {
    public var format: String
    public var text: String
    public var truncated: Bool
}

/// files.changed: what a finished tool call (`toolCallId`) or a whole turn
/// (`turn`, after its turn.end) changed in the tile, from work-tree snapshots.
public struct FilesChanged: Sendable, Hashable {
    public var toolCallId: String?
    public var turn: Int?
    public var changes: [FileChange]
    public var patch: FilesPatch?

    public init(changes: [FileChange], patch: FilesPatch? = nil, toolCallId: String? = nil, turn: Int? = nil) {
        self.changes = changes
        self.patch = patch
        self.toolCallId = toolCallId
        self.turn = turn
    }

    init(json d: JSONValue) {
        toolCallId = d["toolCallId"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        turn = d["turn"]?.int
        changes = d["changes"]?.list(FileChange.self) ?? []
        if let p = d["patch"], p.object != nil {
            patch = FilesPatch(format: p["format"]?.string ?? "", text: p["text"]?.string ?? "", truncated: p["truncated"]?.bool ?? false)
        }
    }

    /// Totals across the files (the web's filesStat).
    public var stat: (files: Int, add: Int, del: Int) {
        (changes.count, changes.reduce(0) { $0 + $1.add }, changes.reduce(0) { $0 + $1.del })
    }
}

public struct Usage: Sendable, Hashable {
    /// Tokens in the context now.
    public var used: Int
    /// The context window.
    public var size: Int
    public var cost: Cost?

    public struct Cost: Sendable, Hashable {
        public var amount: Double
        public var currency: String
    }

    init?(json: JSONValue?) {
        guard let u = json, u.object != nil else { return nil }
        used = u["used"]?.int ?? 0
        size = u["size"]?.int ?? 0
        if let c = u["cost"], let a = c["amount"]?.double {
            cost = Cost(amount: a, currency: c["currency"]?.string ?? "")
        }
    }

    /// "12 345/200 000 tokens" (the web groups thousands with a space).
    public var label: String { "\(Self.group(used))/\(Self.group(size)) tokens" }

    static func group(_ n: Int) -> String {
        let s = String(n.magnitude)
        var out = ""
        for (k, c) in s.enumerated() {
            if k > 0, (s.count - k) % 3 == 0 { out.append(" ") }
            out.append(c)
        }
        return (n < 0 ? "-" : "") + out
    }
}

public struct TurnEnd: Sendable, Hashable {
    public var turn: Int
    public var stopReason: StopReason
    public var usage: Usage?
    public var error: String?

    init(json d: JSONValue) {
        turn = d["turn"]?.int ?? 0
        stopReason = StopReason(rawValue: d["stopReason"]?.string ?? "")
        usage = Usage(json: d["usage"])
        error = d["error"]?.string.flatMap { $0.isEmpty ? nil : $0 }
    }
}

/// One setting the agent advertised (model, effort, mode, …), in its order.
public struct ConfigOption: JSONReadable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var description: String?
    public var category: String
    /// "select" today; others are kept but not rendered as pickers.
    public var type: String
    public var currentValue: String
    public var values: [Value]

    public struct Value: Sendable, Hashable, Identifiable {
        public var value: String
        public var name: String
        public var description: String?
        /// The group a grouped select put it in.
        public var group: String?
        public var id: String { value }
    }

    public init?(json: JSONValue) {
        guard let id = json["id"]?.string, !id.isEmpty else { return nil }
        self.id = id
        name = json["name"]?.string ?? id
        description = json["description"]?.string
        category = json["category"]?.string ?? ""
        type = json["type"]?.string ?? ""
        currentValue = json["currentValue"]?.text ?? ""
        var vs: [Value] = []
        for o in json["options"]?.array ?? [] {
            if let group = o["options"]?.array { // ACP's grouped select: {group, name, options}
                let gname = o["name"]?.string ?? o["group"]?.string
                for g in group { if let v = Self.value(g, group: gname) { vs.append(v) } }
            } else if let v = Self.value(o, group: nil) {
                vs.append(v)
            }
        }
        values = vs
    }

    private static func value(_ o: JSONValue, group: String?) -> Value? {
        guard let v = o["value"]?.text else { return nil }
        return Value(value: v, name: o["name"]?.string ?? v, description: o["description"]?.string, group: group)
    }

    /// The agent exposes its permission mode as this option (the web shows
    /// one mode picker, never both).
    public var isModeOption: Bool { category == "mode" || id == "mode" || name.lowercased() == "mode" }
}

/// A slash command the agent advertised; sent as prompt text `/name args`.
public struct SlashCommand: JSONReadable, Sendable, Hashable, Identifiable {
    public var name: String
    public var description: String
    /// What to type after the name (the command takes input).
    public var hint: String
    public var id: String { name }

    public init(name: String, description: String = "", hint: String = "") {
        self.name = name
        self.description = description
        self.hint = hint
    }

    public init?(json: JSONValue) {
        guard let n = json["name"]?.string, !n.isEmpty else { return nil }
        name = n
        description = json["description"]?.string ?? ""
        hint = json["hint"]?.string ?? ""
    }
}

public struct AgentInfo: Sendable, Hashable {
    public var name: String
    public var version: String
    public var title: String?
}

/// The agent says it is signed out: offer a one-tap sign-in running
/// `command` in a terminal that shares the agent's home.
public struct LoginNeeded: Sendable, Hashable {
    public var provider: String
    public var command: String

    public init(provider: String, command: String) {
        self.provider = provider
        self.command = command
    }
}

/// A status event: the session's status plus whichever of the session's
/// facts rode it (each nil when absent — absent means "unchanged").
public struct StatusUpdate: Sendable, Hashable {
    public var status: SessionStatus?
    public var detail: String?
    public var modes: [SessionMode]?
    public var currentMode: String?
    public var options: [ConfigOption]?
    public var commands: [SlashCommand]?
    public var agent: AgentInfo?
    public var login: LoginNeeded?
    public var usage: Usage?
    /// The agent's title for the session (most adapters write one after the first turn).
    public var title: String?
    /// The keys present on the wire (for the partial-update rules).
    public var keys: Set<String>

    init(json d: JSONValue) {
        keys = Set(d.object?.keys ?? [])
        status = d["status"]?.string.flatMap { $0.isEmpty ? nil : SessionStatus(rawValue: $0) }
        detail = d["detail"]?.string
        modes = d["modes"]?.list(SessionMode.self)
        currentMode = d["currentMode"]?.string.flatMap { $0.isEmpty ? nil : $0 }
        options = d["options"]?.list(ConfigOption.self)
        commands = d["commands"]?.list(SlashCommand.self)
        if let a = d["agent"], a.object != nil {
            agent = AgentInfo(name: a["name"]?.string ?? "", version: a["version"]?.string ?? "", title: a["title"]?.string)
        }
        if let l = d["login"], l["needed"]?.truthy == true {
            login = LoginNeeded(provider: l["provider"]?.string ?? "", command: l["command"]?.string ?? "")
        }
        usage = Usage(json: d["usage"])
        title = d["title"]?.string.flatMap { $0.isEmpty ? nil : $0 }
    }

    /// One of the driver's partial updates, which never carry `login` even
    /// while the agent is signed out: commands, usage, title or options alone.
    public var isPartial: Bool {
        let rest = keys.subtracting(["status"])
        return rest.count == 1 && ["commands", "usage", "title", "options"].contains(rest.first!)
    }
}
