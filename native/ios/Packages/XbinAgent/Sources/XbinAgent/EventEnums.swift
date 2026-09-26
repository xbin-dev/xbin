import Foundation

// The wire's string enums, each open-ended (docs/compat.md: additive only —
// a value a newer daemon sends is kept as `other`, never a decode failure).

/// Declares an enum over a wire string with a catch-all `other`.
public protocol OpenEnum: RawRepresentable, Sendable, Hashable where RawValue == String {
    init(rawValue: String)
}

public enum SessionStatus: OpenEnum {
    case starting, idle, running, waitingPermission, cancelling, error, exited
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "starting": self = .starting
        case "idle": self = .idle
        case "running": self = .running
        case "waiting_permission": self = .waitingPermission
        case "cancelling": self = .cancelling
        case "error": self = .error
        case "exited": self = .exited
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .starting: "starting"
        case .idle: "idle"
        case .running: "running"
        case .waitingPermission: "waiting_permission"
        case .cancelling: "cancelling"
        case .error: "error"
        case .exited: "exited"
        case .other(let s): s
        }
    }

    /// A turn is in flight (the composer shows Stop; the prompt route answers 409).
    public var isBusy: Bool { self == .running || self == .waitingPermission || self == .cancelling }
    /// The session is over (it leaves the directory).
    public var isFinal: Bool { self == .error || self == .exited }
    /// What the web's status line says ("waiting for you", "running", …).
    public var label: String { self == .waitingPermission ? "waiting for you" : rawValue.replacingOccurrences(of: "_", with: " ") }
}

public enum ToolKind: OpenEnum {
    case read, edit, delete, move, search, execute, think, fetch, switchMode, other
    case unknown(String)

    public init(rawValue: String) {
        switch rawValue {
        case "read": self = .read
        case "edit": self = .edit
        case "delete": self = .delete
        case "move": self = .move
        case "search": self = .search
        case "execute": self = .execute
        case "think": self = .think
        case "fetch": self = .fetch
        case "switch_mode": self = .switchMode
        case "other", "": self = .other
        default: self = .unknown(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .read: "read"
        case .edit: "edit"
        case .delete: "delete"
        case .move: "move"
        case .search: "search"
        case .execute: "execute"
        case .think: "think"
        case .fetch: "fetch"
        case .switchMode: "switch_mode"
        case .other: "other"
        case .unknown(let s): s
        }
    }

    /// The web's card glyph for the kind (agent-cards.js KIND_ICON); an app
    /// maps kinds to SF Symbols itself.
    public var glyph: String {
        switch self {
        case .read: "📖"
        case .edit: "✏️"
        case .delete: "🗑️"
        case .move: "↪"
        case .search: "🔎"
        case .execute: "⚙"
        case .think: "💭"
        case .fetch: "🌐"
        case .switchMode: "⇄"
        case .other, .unknown: "•"
        }
    }
}

public enum ToolStatus: OpenEnum {
    case pending, inProgress, completed, failed, cancelled
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "pending": self = .pending
        case "in_progress": self = .inProgress
        case "completed": self = .completed
        case "failed": self = .failed
        case "cancelled": self = .cancelled
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .pending: "pending"
        case .inProgress: "in_progress"
        case .completed: "completed"
        case .failed: "failed"
        case .cancelled: "cancelled"
        case .other(let s): s
        }
    }

    public var isRunning: Bool { self == .pending || self == .inProgress }
    public var label: String { rawValue.replacingOccurrences(of: "_", with: " ") }
}

public enum StopReason: OpenEnum {
    case endTurn, maxTokens, maxTurnRequests, refusal, cancelled, error
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "end_turn": self = .endTurn
        case "max_tokens": self = .maxTokens
        case "max_turn_requests": self = .maxTurnRequests
        case "refusal": self = .refusal
        case "cancelled": self = .cancelled
        case "error": self = .error
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .endTurn: "end_turn"
        case .maxTokens: "max_tokens"
        case .maxTurnRequests: "max_turn_requests"
        case .refusal: "refusal"
        case .cancelled: "cancelled"
        case .error: "error"
        case .other(let s): s
        }
    }

    /// A human phrase for a turn divider.
    public var label: String {
        switch self {
        case .endTurn: "done"
        case .maxTokens: "hit the token limit"
        case .maxTurnRequests: "hit the request limit"
        case .refusal: "refused"
        case .cancelled: "cancelled"
        case .error: "failed"
        case .other(let s): s.isEmpty ? "done" : s.replacingOccurrences(of: "_", with: " ")
        }
    }
}

public enum PermissionOptionKind: OpenEnum {
    case allowOnce, allowAlways, rejectOnce, rejectAlways
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "allow_once": self = .allowOnce
        case "allow_always": self = .allowAlways
        case "reject_once": self = .rejectOnce
        case "reject_always": self = .rejectAlways
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .allowOnce: "allow_once"
        case .allowAlways: "allow_always"
        case .rejectOnce: "reject_once"
        case .rejectAlways: "reject_always"
        case .other(let s): s
        }
    }

    /// The web's test: the kind names a rejection (`/reject/`).
    public var isReject: Bool { rawValue.contains("reject") }
}

public enum MessageRole: OpenEnum {
    case user, agent
    case other(String)

    /// An absent role is the agent's (the web's `d.role || 'agent'`).
    public init(rawValue: String) {
        switch rawValue {
        case "user": self = .user
        case "agent", "": self = .agent
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .user: "user"
        case .agent: "agent"
        case .other(let s): s
        }
    }
}

public enum PlanEntryStatus: OpenEnum {
    case pending, inProgress, completed
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "pending": self = .pending
        case "in_progress": self = .inProgress
        case "completed": self = .completed
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .pending: "pending"
        case .inProgress: "in_progress"
        case .completed: "completed"
        case .other(let s): s
        }
    }
}

public enum FileChangeStatus: OpenEnum {
    case added, modified, deleted, renamed, typechange
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "added": self = .added
        case "modified": self = .modified
        case "deleted": self = .deleted
        case "renamed": self = .renamed
        case "typechange": self = .typechange
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .added: "added"
        case .modified: "modified"
        case .deleted: "deleted"
        case .renamed: "renamed"
        case .typechange: "typechange"
        case .other(let s): s
        }
    }

    /// git's one-letter status (A M D R T, ? otherwise).
    public var letter: String {
        switch self {
        case .added: "A"
        case .modified: "M"
        case .deleted: "D"
        case .renamed: "R"
        case .typechange: "T"
        case .other: "?"
        }
    }
}

public enum ElicitationAction: OpenEnum {
    case accept, decline, cancel
    case other(String)

    public init(rawValue: String) {
        switch rawValue {
        case "accept": self = .accept
        case "decline": self = .decline
        case "cancel": self = .cancel
        default: self = .other(rawValue)
        }
    }

    public var rawValue: String {
        switch self {
        case .accept: "accept"
        case .decline: "decline"
        case .cancel: "cancel"
        case .other(let s): s
        }
    }

    /// The settled card's verb.
    public var verb: String {
        switch self {
        case .accept: "answered"
        case .decline: "skipped"
        default: "cancelled"
        }
    }
}

/// Who settled a permission or a question: `user:<id>`, `owner`, `auto` (a
/// session rule), `cancel` (a turn cancel).
public struct Settler: Sendable, Hashable, CustomStringConvertible {
    public let raw: String
    public init(_ raw: String) { self.raw = raw }

    public var isRule: Bool { raw == "auto" }
    public var isCancel: Bool { raw == "cancel" }
    /// The user id for `user:<id>`.
    public var userID: String? { raw.hasPrefix("user:") ? String(raw.dropFirst(5)) : nil }
    /// "a session rule", "cancel", the user id, "owner" (the web's whoOf).
    public var displayName: String {
        if isRule { return "a session rule" }
        if isCancel { return "cancel" }
        return userID ?? raw
    }

    public var description: String { raw }
}
