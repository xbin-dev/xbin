import Foundation

// What an agent transcript shows, item by item (plans/native.md §8.4 "the
// chat family" and §13): messages, thoughts, tool cards (subagents nest
// their own items), the plan checklist, permission and question cards,
// what a turn changed, turn dividers and gap markers. Ids are stable across
// replays of the same log (they derive from seqs and the agent's own ids),
// so a list diffs cleanly.

public enum TranscriptItem: Sendable, Hashable, Identifiable {
    case message(Message)
    case thought(Thought)
    case tool(ToolCall)
    case plan(PlanChecklist)
    case permission(PermissionCard)
    case question(QuestionCard)
    /// What a whole turn changed on disk (files.changed with `turn`), before its divider.
    case changes(TurnChanges)
    case turnEnd(TurnDivider)
    case gap(GapMarker)

    public var id: String {
        switch self {
        case .message(let x): x.id
        case .thought(let x): x.id
        case .tool(let x): x.id
        case .plan(let x): x.id
        case .permission(let x): x.id
        case .question(let x): x.id
        case .changes(let x): x.id
        case .turnEnd(let x): x.id
        case .gap(let x): x.id
        }
    }
}

public struct Message: Sendable, Hashable, Identifiable {
    public let id: String
    public var role: MessageRole
    public var messageId: String
    public var text: String
    /// The subagent call it belongs to ("" at top level).
    public var parent: String
    /// Unix ms of its first delta.
    public var ts: Int64
    /// The latest run of its list: more text may still arrive. Streaming =
    /// `open && status.isBusy` (the session's).
    public var open: Bool

    /// Text still streaming in: the open run of a session that runs a turn.
    public func isStreaming(status: SessionStatus, ended: Bool = false) -> Bool { open && !ended && status.isBusy }
}

public struct Thought: Sendable, Hashable, Identifiable {
    public let id: String
    public var text: String
    public var parent: String
    /// Unix ms: first chunk, last chunk (or what ended it).
    public var startedAt: Int64
    public var endedAt: Int64
    /// Something else arrived after it (or its turn/subagent ended).
    public var done: Bool

    /// "Thought for Ns": at least one second.
    public var seconds: Int { max(1, Int((Double(endedAt - startedAt) / 1000).rounded())) }

    /// Shimmering "Thinking…": not done while the session runs a turn.
    public func isLive(status: SessionStatus, ended: Bool = false) -> Bool { !done && !ended && status == .running }
}

/// One tool call, folded from its tool.call/tool.update events (the web's
/// newTool/foldTool record).
public struct ToolCall: Sendable, Hashable, Identifiable {
    public let id: String
    /// The agent's id for the call.
    public let toolCallId: String
    public var title = ""
    public var kind: ToolKind = .other
    public var status: ToolStatus = .pending
    public var content: [ToolContent] = []
    public var locations: [ToolLocation] = []
    public var rawInput: JSONValue?
    public var rawOutput: JSONValue?
    public var name = ""
    public var label = ""
    public var parent = ""
    /// A subagent (Task/Agent) call: `children` holds what it did.
    public var isSubagent = false
    public var planReview = false
    /// A shell command's output (ANSI included): deltas appended, a whole output replacing.
    public var output = ""
    public var exitCode: Int?
    /// What the call changed on disk (files.changed with its toolCallId).
    public var files: FilesChanged?
    /// A subagent's own messages, thoughts and tool calls.
    public var children: [TranscriptItem] = []
    /// Unix ms of its first and latest event.
    public var startedAt: Int64 = 0
    public var updatedAt: Int64 = 0
    /// +/- lines across the call's ACP diff content (computed when the content changes).
    public private(set) var contentDiffStat = DiffStat()

    public init(id: String, toolCallId: String) {
        self.id = id
        self.toolCallId = toolCallId
    }

    /// Merges one tool.call/tool.update: fields replace, outputDelta appends,
    /// output replaces the accumulated output.
    public mutating func fold(_ u: ToolCallUpdate) {
        if let v = u.title { title = v }
        if let v = u.kind { kind = v }
        if let v = u.status { status = v }
        if let v = u.content {
            content = v
            contentDiffStat = DiffStat(diffs: diffs)
        }
        if let v = u.locations { locations = v }
        if let v = u.rawInput { rawInput = v }
        if let v = u.rawOutput { rawOutput = v }
        if let v = u.name { name = v }
        if let v = u.label { label = v }
        if let v = u.parent { parent = v }
        if u.subagent { isSubagent = true }
        if u.planReview { planReview = true }
        if let v = u.outputDelta { output += v }
        if let v = u.output { output = v }
        if let v = u.exitCode { exitCode = v }
    }

    public var isRunning: Bool { status.isRunning }
    public var isExecute: Bool { kind == .execute }
    /// The card's title (D77): the harness's description, else a reading of the command.
    public var headline: String {
        ToolReading.headline(label: label, rawInput: rawInput, kind: kind, status: status, content: content, title: title, name: name, id: toolCallId)
    }

    /// The shell command ("" when the call is not one).
    public var command: String { ToolReading.command(rawInput: rawInput, kind: kind, title: title) }
    public var isPlanApproval: Bool { ToolReading.isPlanApproval(kind: kind.rawValue, planReview: planReview, rawInput: rawInput) }
    public var planText: String { ToolReading.planText(content: content, rawInput: rawInput) }
    /// A non-zero exit: the card shows an "exit N" chip.
    public var failedExit: Int? { exitCode.flatMap { $0 == 0 ? nil : $0 } }
    /// The raw input behind a toggle — not for shell, edit or plan cards (they show it their own way).
    public var rawInputText: String? {
        guard rawInput != nil, kind != .execute, kind != .edit, !isPlanApproval else { return nil }
        let t = ToolReading.rawText(rawInput)
        return t.isEmpty ? nil : t
    }

    /// The ACP diff blocks of the call's content.
    public var diffs: [ToolDiff] {
        content.compactMap { if case .diff(let p, let o, let n) = $0 { return ToolDiff(path: p, oldText: o, newText: n) }; return nil }
    }

    /// Text content worth showing (not blank, not repeating `skipping`).
    public func texts(skipping: String = "") -> [String] {
        content.compactMap { if case .text(let s) = $0, !trim(s).isEmpty, s != skipping { return s }; return nil }
    }

    /// +/- across the call's diffs and its snapshot diff (the card's chip).
    public var diffStat: DiffStat {
        var s = contentDiffStat
        if let f = files { let st = f.stat; s.added += st.add; s.removed += st.del }
        return s
    }

    /// The output as a card shows it (ANSI parsed; long output tailed).
    public func outputView(maxCharacters: Int = 20_000, maxLines: Int? = nil) -> OutputView {
        OutputView(raw: output, maxCharacters: maxCharacters, maxLines: maxLines)
    }

    /// A subagent's kind (Claude's rawInput.subagent_type).
    public var subagentType: String { rawInput?["subagent_type"]?.string ?? "" }
    /// A subagent's prompt (collapsed on the card).
    public var subagentPrompt: String { rawInput?["prompt"]?.string ?? "" }
    /// A subagent's tool calls so far ("N steps").
    public var subagentSteps: Int { children.filter { if case .tool = $0 { return true }; return false }.count }
}

public struct DiffStat: Sendable, Hashable {
    public var added = 0
    public var removed = 0
    public var isEmpty: Bool { added == 0 && removed == 0 }

    public init(added: Int = 0, removed: Int = 0) {
        self.added = added
        self.removed = removed
    }

    init(diffs: [ToolDiff]) {
        for d in diffs {
            for l in LineDiff.lines(old: d.oldText, new: d.newText) {
                if l.kind == .added { added += 1 } else if l.kind == .removed { removed += 1 }
            }
        }
    }
}

/// An ACP diff block: a file's whole old and new text.
public struct ToolDiff: Sendable, Hashable, Identifiable {
    public var path: String
    /// nil for a new file.
    public var oldText: String?
    public var newText: String
    public var id: String { path }

    public func hunks(context: Int = 3) -> [DiffHunk] { LineDiff.hunks(old: oldText, new: newText, context: context) }
    public var lines: [DiffLine] { LineDiff.lines(old: oldText, new: newText) }
    public var unified: String { LineDiff.unified(path: path, old: oldText, new: newText) }
}

/// The agent's plan for the turn (a new turn starts a fresh one; each plan
/// event replaces the entries).
public struct PlanChecklist: Sendable, Hashable, Identifiable {
    public let id: String
    public var entries: [PlanEntry]
    public var completed: Int { entries.filter { $0.status == .completed }.count }
}

public struct PermissionCard: Sendable, Hashable, Identifiable {
    public let id: String
    public var request: PermissionRequest
    /// Who answered, and how (nil while it waits).
    public var resolution: PermissionResolved?
    public var pid: String { request.pid }
    public var isPending: Bool { resolution == nil }
    public var isPlanApproval: Bool { PermissionRules.isPlanApproval(request) }
    public var heading: String { PermissionRules.heading(request) }
    public var choices: [PermissionChoice] { PermissionRules.choices(request) }
    public var settlement: PermissionSettlement? { resolution.map { PermissionRules.settlement(request, $0) } }
}

public struct QuestionCard: Sendable, Hashable, Identifiable {
    public let id: String
    public var request: ElicitationRequest
    public var fields: [FormField]
    public var resolution: ElicitationResolved?
    public var eid: String { request.eid }
    public var isPending: Bool { resolution == nil }
}

public struct TurnChanges: Sendable, Hashable, Identifiable {
    public let id: String
    public var turn: Int?
    public var files: FilesChanged
}

public struct TurnDivider: Sendable, Hashable, Identifiable {
    public let id: String
    public var turn: Int?
    public var stopReason: StopReason
    public var usage: Usage?
    public var error: String?
    /// "turn 2 · done" (the web's divider).
    public var label: String { "turn \(turn.map(String.init) ?? "") · \(stopReason.label)" }
}

/// Events were dropped here (the log's ring outran this client).
public struct GapMarker: Sendable, Hashable, Identifiable {
    public let id: String
    /// The last seq before the gap.
    public var before: UInt64
    public var label: String { "… earlier events dropped (log limit)" }
}

/// A turn's items, for a list grouped by turn.
public struct TurnGroup: Sendable, Hashable, Identifiable {
    public let id: String
    /// The turn's number (from its divider; nil while it runs or before the first).
    public var number: Int?
    public var items: [TranscriptItem]
    /// Its divider (nil: the turn in progress).
    public var end: TurnDivider?
}
