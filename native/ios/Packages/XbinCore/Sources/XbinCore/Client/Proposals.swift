import Foundation

// The sessions screen's PRs tool (D132, B4): cross-tile change proposals
// ("code PRs", D48; docs/protocol.md `/code/prs…`), as web/bx-prs.js shows
// them to the target tile's side — the list (open, or all), a proposal's
// patch series and review thread, a comment, and the target's two
// decisions, merged and rejected, each with an optional note. Applying is
// never a button: the patch lands only when the tile's own terminal runs
// `git am`, and the tool shows that command. `pr` events (per tile) keep
// it live.

public enum ProposalRoute {
    public static func list(target: String) -> String { "/api/xbin/code/prs?target=\(URLComponent.encode(target))" }
    public static func one(target: String, n: Int) -> String { "/api/xbin/code/pr?target=\(URLComponent.encode(target))&n=\(n)" }
    public static func series(target: String, n: Int) -> String {
        "/api/xbin/code/pr/series?target=\(URLComponent.encode(target))&n=\(n)"
    }
    public static let comment = "/api/xbin/code/pr/comment"
    public static let state = "/api/xbin/code/pr/state"

    public static func commentBody(target: String, n: Int, body: String) -> JSONValue {
        .object(["target": .string(target), "n": .int(Int64(n)), "body": .string(body)])
    }

    /// merged or rejected (the target's side), with the note for the author.
    public static func stateBody(target: String, n: Int, state: String, comment: String) -> JSONValue {
        var o: [String: JSONValue] = ["target": .string(target), "n": .int(Int64(n)), "state": .string(state)]
        if !comment.isEmpty { o["comment"] = .string(comment) }
        return .object(o)
    }

    /// The command that applies a proposal in the tile's own terminal.
    public static func applyCommand(_ n: Int) -> String { "bx code pr fetch \(n) | git am --3way" }
}

/// One proposal (the list's row, or `GET /code/pr` with its thread).
public struct Proposal: Equatable, Sendable, Identifiable {
    public struct Event: Equatable, Sendable {
        public var who: String
        /// comment | state
        public var type: String
        public var body: String
        public var state: String
        public var ts: String
    }

    public var number: Int
    public var target: String
    public var title: String
    public var message: String
    /// open | merged | rejected | withdrawn
    public var state: String
    public var created: String
    public var updated: String
    public var base: String
    /// "" | builtin-update
    public var kind: String
    public var fromComponent: String
    public var fromUser: String
    public var events: [Event]
    public var id: Int { number }

    public var isOpen: Bool { state == "open" }

    /// Who filed it, as bx-prs.js says it: the filing tile (and its user),
    /// else the user, else the owner.
    public var from: String {
        if !fromComponent.isEmpty { return fromComponent + (fromUser.isEmpty ? "" : " (\(fromUser))") }
        return fromUser.isEmpty ? "owner" : "user:\(fromUser)"
    }

    /// The list's short form: the filing tile, else the user.
    public var fromShort: String { !fromComponent.isEmpty ? fromComponent : fromUser.isEmpty ? "owner" : "user:\(fromUser)" }

    public init?(json j: JSONValue) {
        guard let n = j["number"]?.intValue else { return nil }
        number = Int(n)
        target = j["target"]?.stringValue ?? ""
        title = j["title"]?.stringValue ?? ""
        message = j["message"]?.stringValue ?? ""
        state = j["state"]?.stringValue ?? ""
        created = j["created"]?.stringValue ?? ""
        updated = j["updated"]?.stringValue ?? ""
        base = j["base"]?.stringValue ?? ""
        kind = j["kind"]?.stringValue ?? ""
        fromComponent = j["from"]?["component"]?.stringValue ?? ""
        fromUser = j["from"]?["user"]?.stringValue ?? ""
        events = (j["events"]?.arrayValue ?? []).map {
            Event(who: $0["who"]?.stringValue ?? "", type: $0["type"]?.stringValue ?? "", body: $0["body"]?.stringValue ?? "",
                  state: $0["state"]?.stringValue ?? "", ts: $0["ts"]?.stringValue ?? "")
        }
    }

    /// `GET /code/prs?target=` → its proposals, newest first.
    public static func list(_ j: JSONValue) -> [Proposal] {
        (j["prs"]?.arrayValue ?? []).compactMap(Proposal.init(json:)).sorted { $0.number > $1.number }
    }

    /// A thread event as the web shows it: a comment's text, a decision as
    /// "→ merged: note".
    public static func text(_ e: Event) -> String {
        e.type == "state" ? "→ \(e.state)\(e.body.isEmpty ? "" : ": \(e.body)")" : e.body
    }

    /// The count the tool's chip shows: the open ones.
    public static func openCount(_ list: [Proposal]) -> Int { list.filter(\.isOpen).count }
}
