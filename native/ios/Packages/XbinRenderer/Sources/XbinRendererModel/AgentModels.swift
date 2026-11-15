import Foundation
import XbinCore

// View models of the workspace agent's nodes (D233; docs/native.md → The
// workspace agent's nodes): `card`, `connection`, a transcript's `empty`
// slot — drawn from the tile's tree like everything else — and the two
// nodes that are NOT: `request` and `session`, which a tile places by id
// and the app draws from xbind's own reading (``XbinTrustedNode``).

/// A `card`: a card of the tile's own in a transcript — a title, then its
/// children (text, fields, rows), its buttons in a row at the bottom.
public struct CardModel: Sendable, Equatable {
    public var title: String?
    public var subtitle: String?
    /// An icon name (§10.4); nil: none.
    public var icon: String?
    public var tone: XbinNoticeTone?
    public var badge: String?
    /// A quiet sentence above the buttons.
    public var note: String?

    public init(title: String? = nil, subtitle: String? = nil, icon: String? = nil, tone: XbinNoticeTone? = nil,
                badge: String? = nil, note: String? = nil) {
        self.title = title
        self.subtitle = subtitle
        self.icon = icon
        self.tone = tone
        self.badge = badge
        self.note = note
    }

    public init(props p: Props) {
        self.init(title: p.nonEmpty("title"), subtitle: p.nonEmpty("subtitle"), icon: p.nonEmpty("icon"),
                  tone: XbinNoticeTone(p.string("tone")), badge: p.nonEmpty("badge"), note: p.nonEmpty("note"))
    }

    /// Whether the card has a header line at all.
    public var hasHeader: Bool { title != nil || subtitle != nil || icon != nil || badge != nil }

    /// The colour role of the card's icon, badge and leading rule (nil:
    /// neutral — `info`, or no tone).
    public var accent: XbinTone? { tone?.tone }

    /// A card's children in two groups, in order: what is drawn in its
    /// body, and its buttons (drawn in a row at the bottom, whatever their
    /// place among the children).
    public static func split<T>(_ children: [T], type: (T) -> String) -> (body: [T], buttons: [T]) {
        var body: [T] = [], buttons: [T] = []
        for c in children {
            if type(c) == "button" { buttons.append(c) } else { body.append(c) }
        }
        return (body, buttons)
    }

    /// The header as VoiceOver reads it: "Brief for Orders' agent, asked,
    /// Claude Code · asks before acting".
    public var accessibilityHeader: String {
        [title, badge, subtitle].compactMap { $0 }.joined(separator: ", ")
    }
}

/// A `connection`: the line that says what a conversation is connected to.
public struct ConnectionModel: Sendable, Equatable {
    /// What is connected ("14 tiles · 3 orgs").
    public var connected: String?
    /// What is not ("6 not connected"); nil: nothing is left out.
    public var not: String?
    public var model: String?
    /// The model is marked private.
    public var isPrivate: Bool
    /// The whole line as one sentence, for VoiceOver.
    public var label: String?
    public var disabled: Bool

    public init(connected: String? = nil, not: String? = nil, model: String? = nil, isPrivate: Bool = false,
                label: String? = nil, disabled: Bool = false) {
        self.connected = connected
        self.not = not
        self.model = model
        self.isPrivate = isPrivate
        self.label = label
        self.disabled = disabled
    }

    public init(props p: Props) {
        self.init(connected: p.nonEmpty("connected"), not: p.nonEmpty("not"), model: p.nonEmpty("model"),
                  isPrivate: p.bool("private"), label: p.nonEmpty("label"), disabled: p.bool("disabled"))
    }

    /// The model's segment: "big", "local-llama · private".
    public var modelText: String? {
        guard let model else { return isPrivate ? "private" : nil }
        return isPrivate ? "\(model) · private" : model
    }

    /// One element for VoiceOver: the tile's own sentence, else the
    /// segments in order ("Scope: 14 tiles · 3 orgs connected. 6 not
    /// connected. Model big, private.").
    public var accessibilityLabel: String {
        if let label { return label }
        var parts: [String] = []
        if let connected { parts.append("Scope: \(connected) connected") }
        if let not { parts.append(not) }
        if let model { parts.append("Model \(model)" + (isPrivate ? ", private" : "")) }
        return parts.isEmpty ? "Scope" : parts.joined(separator: ". ") + "."
    }
}

/// A transcript's `empty` slot (rev 3): what a transcript with no children
/// shows — a heading, a sentence, short titled lists and suggestions.
public struct TranscriptEmpty: Sendable, Equatable {
    public struct List: Sendable, Equatable, Identifiable {
        public var id: Int
        public var title: String?
        public var items: [String]

        public init(id: Int, title: String? = nil, items: [String]) {
            self.id = id
            self.title = title
            self.items = items
        }
    }

    public struct Suggestion: Sendable, Equatable, Identifiable {
        public var id: String
        /// A word in small caps ("Ask", "Fix", "Change", "New"); nil: none.
        public var kind: String?
        public var text: String

        public init(id: String, kind: String? = nil, text: String) {
            self.id = id
            self.kind = kind
            self.text = text
        }

        /// "Fix: Team calendar's last sync failed. Find out why."
        public var accessibilityLabel: String { kind.map { "\($0): \(text)" } ?? text }
    }

    public var title: String?
    public var text: String?
    public var lists: [List]
    public var suggestionsTitle: String?
    public var suggestions: [Suggestion]

    public init(title: String? = nil, text: String? = nil, lists: [List] = [], suggestionsTitle: String? = nil,
                suggestions: [Suggestion] = []) {
        self.title = title
        self.text = text
        self.lists = lists
        self.suggestionsTitle = suggestionsTitle
        self.suggestions = suggestions
    }

    /// From a `transcript` node's props; nil without an `empty` object (or
    /// with one that says nothing).
    public init?(props p: Props) {
        guard let e = p.object("empty") else { return nil }
        func text(_ k: String) -> String? { Props.text(e[k]).flatMap { $0.isEmpty ? nil : $0 } }
        var lists: [List] = []
        for l in (e["lists"]?.arrayValue ?? []).compactMap(\.objectValue) {
            let items = (l["items"]?.arrayValue ?? []).compactMap { Props.text($0) }.filter { !$0.isEmpty }
            let title = Props.text(l["title"]).flatMap { $0.isEmpty ? nil : $0 }
            if title != nil || !items.isEmpty { lists.append(List(id: lists.count, title: title, items: items)) }
        }
        var seen = Set<String>()
        var suggestions: [Suggestion] = []
        for s in (e["suggestions"]?.arrayValue ?? []).compactMap(\.objectValue) {
            // A suggestion reports by id: one without, or a repeated one, is not drawn.
            guard let id = Props.text(s["id"]), !id.isEmpty, seen.insert(id).inserted,
                  let t = Props.text(s["text"]), !t.isEmpty else { continue }
            suggestions.append(Suggestion(id: id, kind: Props.text(s["kind"]).flatMap { $0.isEmpty ? nil : $0 }, text: t))
        }
        self.init(title: text("title"), text: text("text"), lists: lists, suggestionsTitle: text("suggestionsTitle"),
                  suggestions: suggestions)
        if title == nil, self.text == nil, lists.isEmpty, suggestions.isEmpty { return nil }
    }
}

/// A node the APP draws, not the tree: a tile's description places it and
/// names an id; what it shows and every button on it come from xbind
/// through the app (``XbinServices/trustedNode``, in the SwiftUI target) —
/// never from the description. A native tile cannot draw a grant.
public enum XbinTrustedNode: Sendable, Hashable {
    /// `request`: an approval request of the person's workspace agent.
    /// `kind` is the tile's hint of what it is (`call`, `session`,
    /// `template`, `changeset`); the app reads the truth by `id`.
    case request(id: String, kind: String)
    /// `session`: a tile agent's session. `tile` names where it runs while
    /// the app looks the session up.
    case session(id: String, tile: String)

    /// The request kinds of the vocabulary; anything else reads as `call`.
    public static let requestKinds: Set<String> = ["call", "session", "template", "changeset"]

    /// From a `request` or `session` node; nil for any other primitive or
    /// a node without an id.
    public init?(type: String, props p: Props) {
        guard let id = p.nonEmpty("id") else { return nil }
        switch type {
        case "request":
            let k = p.string("kind") ?? ""
            self = .request(id: id, kind: Self.requestKinds.contains(k) ? k : "call")
        case "session":
            self = .session(id: id, tile: p.string("tile") ?? "")
        default:
            return nil
        }
    }

    public var id: String {
        switch self {
        case .request(let id, _), .session(let id, _): return id
        }
    }

    /// What the renderer draws where no app provides the node (a preview,
    /// a snapshot): a neutral, inert line — never a button.
    public var placeholderTitle: String {
        switch self {
        case .request(_, let kind):
            return kind == "changeset" ? "Admin changes" : "Approval request"
        case .session(_, let tile):
            return tile.isEmpty ? "Tile agent" : "Tile agent · \(tile)"
        }
    }

    public static let placeholderCaption = "Shown by the xbin app"

    /// The placeholder's title for a node that names no id (the app can
    /// look nothing up).
    public static func placeholderTitle(type: String) -> String {
        type == "session" ? "Tile agent" : "Approval request"
    }
}
