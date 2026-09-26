import Foundation
import XbinAgent
import XbinCore
import XbinRendererModel

// The ACP agent screen draws its transcript with the renderer's chat
// components (plans/native.md §8.4: the agent template and the shell's
// agent share them). This maps XbinAgent's reducer output onto the
// renderer's chat view models. UIKit-free; the D77 rules stay in XbinAgent
// (PermissionRules: the agent's own options and order, reject first when
// defaultToNo, "allow for the session" only when scoped, never
// auto-answered, "answered by X").

typealias CoreJSON = XbinCore.JSONValue
typealias AgentJSON = XbinAgent.JSONValue

enum AgentChat {
    /// XbinAgent's JSON → XbinCore's (through the wire form).
    static func core(_ v: AgentJSON?) -> CoreJSON? {
        guard let v else { return nil }
        return try? CoreJSON(parsing: v.compactString)
    }

    /// XbinCore's JSON → XbinAgent's.
    static func agent(_ v: CoreJSON) -> AgentJSON? {
        try? AgentJSON.parse(v.jsonString)
    }

    static func message(_ m: Message, status: SessionStatus, ended: Bool, agentName: String,
                        blocks: (String) -> [MarkdownBlock]) -> ChatMessage {
        let user: Bool
        if case .user = m.role { user = true } else { user = false }
        // A prompt's files show as chips (their bytes are never logged).
        let files = m.files.map { ChatFile(name: $0.name, mime: $0.mime) }
        return ChatMessage(id: m.id, role: user ? .user : .assistant, sender: user ? nil : agentName, text: m.text,
                           markdown: user ? nil : blocks(m.text), streaming: m.isStreaming(status: status, ended: ended),
                           time: m.ts > 0 ? ChatFormat.time(.int(m.ts)) : nil, files: files)
    }

    static func thinking(_ t: Thought, status: SessionStatus, ended: Bool) -> ChatThinking {
        let live = t.isLive(status: status, ended: ended)
        return ChatThinking(id: t.id, text: t.text, live: live, seconds: t.done ? Double(t.seconds) : nil)
    }

    static func toolState(_ s: ToolStatus) -> ToolState {
        switch s {
        case .pending, .inProgress: return .running
        case .completed: return .ok
        case .failed: return .error
        case .cancelled: return .canceled
        case .other: return .running
        }
    }

    static func icon(_ k: ToolKind) -> String {
        switch k {
        case .read: return "doc"
        case .edit: return "pencil"
        case .delete: return "trash"
        case .move: return "folder"
        case .search: return "search"
        case .execute: return "terminal"
        case .think: return "sparkles"
        case .fetch: return "globe"
        case .switchMode: return "branch"
        case .other, .unknown: return "wrench"
        }
    }

    static func toolCard(_ t: ToolCall) -> ChatToolCard {
        var chips: [ChatChip] = []
        if let code = t.failedExit { chips.append(ChatChip(text: "exit \(code)", tone: .danger)) }
        let stat = t.diffStat
        if !stat.isEmpty { chips.append(ChatChip(text: "+\(stat.added) −\(stat.removed)", tone: nil)) }
        if t.isSubagent, t.subagentSteps > 0 { chips.append(ChatChip(text: "\(t.subagentSteps) steps", tone: .muted)) }
        return ChatToolCard(id: t.id, title: t.headline, icon: t.isSubagent ? "agent" : icon(t.kind),
                            family: t.isSubagent ? "agent" : t.kind.rawValue, state: toolState(t.status), chips: chips)
    }

    /// What a tool card shows when opened.
    static func hasContent(_ t: ToolCall) -> Bool {
        !t.output.isEmpty || !t.diffs.isEmpty || !t.children.isEmpty || !t.texts().isEmpty || t.files != nil
    }

    static func diff(_ t: ToolCall) -> ChatDiff? {
        if let f = t.files { return diff(f) }
        let d = t.diffs
        guard !d.isEmpty else { return nil }
        let files = d.map { x -> ChatDiff.File in
            let lines = x.lines
            let added = lines.filter { $0.kind == .added }.count
            let removed = lines.filter { $0.kind == .removed }.count
            return ChatDiff.File(path: x.path, status: x.oldText == nil ? "added" : "modified", added: added, removed: removed)
        }
        return ChatDiff(files: files, patch: d.map(\.unified).joined(separator: "\n"))
    }

    static func diff(_ f: FilesChanged) -> ChatDiff {
        ChatDiff(files: f.changes.map { ChatDiff.File(path: $0.path, status: $0.status.rawValue, added: $0.add, removed: $0.del) },
                 patch: f.patch?.text)
    }

    static func approval(_ p: PermissionCard) -> ChatApproval {
        let r = p.request
        var text = PermissionRules.description(r)
        let detail = PermissionRules.detail(r)
        if !detail.isEmpty { text = text.isEmpty ? detail : text + "\n\n" + detail }
        if p.isPlanApproval {
            let plan = PermissionRules.planText(r)
            if !plan.isEmpty { text = plan }
        }
        let settled = p.settlement.map { s in ChatApproval.Settled(by: s.by.displayName, id: s.option ?? "") }
        return ChatApproval(id: p.id, title: p.heading, text: text.isEmpty ? nil : text,
                            options: p.choices.map { ApprovalOption(id: $0.id, label: $0.label, kind: $0.kind.rawValue) },
                            note: PermissionRules.ruleNote(r), feedback: p.isPlanApproval, settled: settled)
    }

    static func question(_ q: QuestionCard) -> ChatQuestion {
        let form = QuestionForm(schema: core(q.request.schema))
        let answer = core(q.resolution?.content)?.objectValue ?? [:]
        let title = q.request.message.isEmpty ? (form.title ?? "Question") : q.request.message
        return ChatQuestion(id: q.id, title: title, form: form, settled: !q.isPending, answer: answer)
    }

    static func plan(_ p: PlanChecklist) -> ChatPlan {
        ChatPlan(entries: p.entries.map { e in
            ChatPlan.Entry(text: e.content, status: PlanStatus(rawValue: e.status.rawValue) ?? .pending)
        })
    }

    static func activity(_ text: String?) -> ChatActivity? {
        guard let text, !text.isEmpty else { return nil }
        return ChatActivity(text: text, live: true)
    }

    static func divider(_ d: TurnDivider) -> ChatStep {
        let tone: XbinTone? = d.error != nil ? .danger : .muted
        return ChatStep(glyph: d.error != nil ? "⚠︎" : "—", text: d.error.map { "\(d.label): \($0)" } ?? d.label, tone: tone)
    }
}

/// Markdown blocks per message, re-lexed only when the text changed (a
/// streaming message grows every few hundred milliseconds).
final class MarkdownMemo {
    private var cache: [String: (String, [MarkdownBlock])] = [:]

    func blocks(id: String, text: String) -> [MarkdownBlock] {
        if let c = cache[id], c.0 == text { return c.1 }
        let b = Markdown.blocks(MarkdownLexer.tokens(text))
        cache[id] = (text, b)
        return b
    }
}
