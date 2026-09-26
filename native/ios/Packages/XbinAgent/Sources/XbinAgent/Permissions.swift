import Foundation

// The D77 permission-card rules, as pure functions of a request (the port of
// web/agent-cards.js permCard/planCard):
//
//  - the agent's own options, in its order — except that `meta.defaultToNo`
//    puts the rejecting ones first (stable);
//  - "allow for the session" (allow_always) hidden when the rule cannot be
//    scoped (`rule.scoped` false: no kind and no title, or a switch_mode);
//  - the heading: the adapter's `meta.title` unless it only repeats the
//    command (Claude titles a Bash ask with the command itself), else the
//    call's headline;
//  - a plan approval (switch_mode, planReview, a rawInput.plan) is a plan
//    card: the plan as markdown, every allow option in the agent's words
//    (they are modes, not "remember this"), the rejects beside a feedback box
//    whose text is sent as the next prompt once the rejected turn settles;
//  - never answered automatically; settled cards say who answered.

/// One button of a permission card.
public struct PermissionChoice: Sendable, Hashable, Identifiable {
    public var id: String
    public var label: String
    public var kind: PermissionOptionKind
    /// What to POST (`AgentClient.answerPermission`).
    public var answer: PermissionAnswer
    /// The plan card's first allow option.
    public var primary: Bool

    public var isReject: Bool { kind.isReject }
}

/// How a settled card reads.
public struct PermissionSettlement: Sendable, Hashable {
    /// "allowed", "denied", "cancelled" (plan cards: "Plan approved", "Kept planning").
    public var verb: String
    /// The chosen option's label, when one was chosen.
    public var option: String?
    public var by: Settler
    /// A plan card that ended without approval.
    public var keptPlanning: Bool
}

public enum PermissionRules {
    /// A plan approval (plan card) rather than a tool permission.
    public static func isPlanApproval(_ r: PermissionRequest) -> Bool {
        ToolReading.isPlanApproval(kind: r.toolCall.kind, planReview: false, rawInput: r.toolCall.rawInput)
    }

    /// Whether "allow for the session" may show: the rule's `scoped` (no rule
    /// at all: the web's default, true).
    public static func isScoped(_ r: PermissionRequest) -> Bool { r.rule?.scoped ?? true }

    /// The command the request would run (execute calls), "" otherwise.
    public static func command(_ r: PermissionRequest) -> String {
        ToolReading.command(rawInput: r.toolCall.rawInput, kind: ToolKind(rawValue: r.toolCall.kind), title: r.toolCall.title)
    }

    /// The card's heading.
    public static func heading(_ r: PermissionRequest) -> String {
        let tc = r.toolCall
        if isPlanApproval(r) {
            if let t = r.meta?.title, !t.isEmpty { return t }
            return tc.title.isEmpty ? "Approve the plan?" : tc.title
        }
        let cmd = command(r)
        if let mt = r.meta?.title, !mt.isEmpty, trim(mt) != trim(cmd) { return mt }
        return ToolReading.headline(label: "", rawInput: tc.rawInput, kind: ToolKind(rawValue: tc.kind), status: nil, content: tc.content,
                                    title: tc.title, name: tc.name, id: tc.id)
    }

    /// The adapter's longer description, when it sent one.
    public static func description(_ r: PermissionRequest) -> String { r.meta?.description ?? "" }

    /// What exactly would run, shown under the heading: the command, else the
    /// raw input as text ("" when there is neither).
    public static func detail(_ r: PermissionRequest) -> String {
        let cmd = command(r)
        return cmd.isEmpty ? ToolReading.rawText(r.toolCall.rawInput) : cmd
    }

    /// The request's content to show under it (text that only repeats the
    /// heading is left out).
    public static func content(_ r: PermissionRequest) -> [ToolContent] {
        let h = heading(r)
        return r.toolCall.content.filter { c in
            if case .text(let s) = c { return !trim(s).isEmpty && s != h }
            if case .terminal = c { return false }
            return true
        }
    }

    /// The agent's options in display order: rejects first when defaultToNo.
    public static func orderedOptions(_ r: PermissionRequest) -> [PermissionOption] {
        guard r.meta?.defaultToNo == true else { return r.options }
        return r.options.filter { $0.kind.isReject } + r.options.filter { !$0.kind.isReject }
    }

    /// The buttons of a (non-plan) permission card: the agent's options
    /// (allow_always dropped when not scoped); with no options, the generic
    /// allow once / allow for the session / deny decisions.
    public static func choices(_ r: PermissionRequest) -> [PermissionChoice] {
        let scoped = isScoped(r)
        let opts = orderedOptions(r)
        if opts.isEmpty {
            var out = [PermissionChoice(id: "allow_once", label: "Allow once", kind: .allowOnce, answer: .decision(.allowOnce), primary: false)]
            if scoped {
                out.append(PermissionChoice(id: "allow_always", label: "Allow for the session", kind: .allowAlways, answer: .decision(.allowAlways), primary: false))
            }
            out.append(PermissionChoice(id: "reject_once", label: "Deny", kind: .rejectOnce, answer: .decision(.rejectOnce), primary: false))
            return out
        }
        return opts.filter { scoped || $0.kind != .allowAlways }.map {
            PermissionChoice(id: $0.optionId, label: $0.name.isEmpty ? $0.optionId : $0.name, kind: $0.kind, answer: .option($0.optionId), primary: false)
        }
    }

    /// The note under a scoped request: what "allow for the session" remembers.
    public static func ruleNote(_ r: PermissionRequest) -> String? {
        guard isScoped(r), let rule = r.rule, !rule.kind.isEmpty || !rule.title.isEmpty else { return nil }
        var s = "“Allow for the session” auto-approves later \(rule.kind) calls"
        if !rule.title.isEmpty { s += " titled “\(rule.title)”" }
        return s + "."
    }

    // MARK: plan approvals

    public static func planText(_ r: PermissionRequest) -> String {
        ToolReading.planText(content: r.toolCall.content, rawInput: r.toolCall.rawInput)
    }

    /// The plan file Claude wrote, when it names one.
    public static func planFilePath(_ r: PermissionRequest) -> String { r.toolCall.rawInput?["planFilePath"]?.string ?? "" }

    /// A plan card's approve buttons: every non-reject option in the agent's
    /// order and words, the first primary — never filtered by scope (a plan's
    /// allow_always options are modes).
    public static func planApprovals(_ r: PermissionRequest) -> [PermissionChoice] {
        r.options.filter { !$0.kind.isReject }.enumerated().map { i, o in
            PermissionChoice(id: o.optionId, label: o.name.isEmpty ? o.optionId : o.name, kind: o.kind, answer: .option(o.optionId), primary: i == 0)
        }
    }

    /// A plan card's "keep planning" buttons (shown beside the feedback box).
    public static func planRejections(_ r: PermissionRequest) -> [PermissionChoice] {
        r.options.filter { $0.kind.isReject }.map {
            PermissionChoice(id: $0.optionId, label: $0.name.isEmpty ? $0.optionId : $0.name, kind: $0.kind, answer: .option($0.optionId), primary: false)
        }
    }

    // MARK: settled

    public static func settlement(_ r: PermissionRequest, _ res: PermissionResolved) -> PermissionSettlement {
        let opt = r.options.first { $0.optionId == res.optionId }
        if isPlanApproval(r) {
            let kept = res.by.isCancel || opt == nil || opt!.kind.isReject
            return PermissionSettlement(verb: kept ? "Kept planning" : "Plan approved", option: kept ? nil : opt?.name, by: res.by, keptPlanning: kept)
        }
        let verb = res.by.isCancel ? "cancelled" : (opt?.kind.isReject == true ? "denied" : "allowed")
        return PermissionSettlement(verb: verb, option: opt?.name, by: res.by, keptPlanning: false)
    }
}
