import Foundation

// Creating a tile from the phone (plans/native.md §4, D125): a name and an
// owner → `POST /api/xbin/create {path, title, owner?}` → the tile goes on
// the screen (MobileScreens, and LayoutEdit for a personal screen) and opens
// on "What should this tile be?". The owner choices are the web shell's
// (`_ownerOptions` in bx-shell.js, D24/D39/D52/D88), so the phone offers
// exactly what the server accepts.

public enum TileCreate {
    public static let path = "/api/xbin/create"

    public struct Owner: Sendable, Hashable, Identifiable {
        /// `user:<id>`, `org:<id>`, or "" (the workspace).
        public var value: String
        public var label: String
        public var id: String { value }

        public init(value: String, label: String) {
            self.value = value
            self.label = label
        }
    }

    /// Me when the user has an id; each org where they may create (or are
    /// its admin); the workspace for a workspace admin. Without personal
    /// tiles (the policy is org-only, or the account's are off) "me" goes —
    /// except for an admin, as the web shell has it.
    public static func owners(_ who: Whoami) -> [Owner] {
        var out: [Owner] = []
        let isUser = who.kind.isEmpty || who.kind == "user"
        if isUser, !who.userID.isEmpty { out.append(Owner(value: "user:\(who.userID)", label: "Me (personal)")) }
        for o in who.orgs where o.create || o.admin { out.append(Owner(value: "org:\(o.id)", label: o.displayName)) }
        if who.admin { out.append(Owner(value: "", label: "Workspace")) }
        if !who.admin && !who.personalTiles { return out.filter { !$0.value.hasPrefix("user:") } }
        return out
    }

    /// The owner picked to start with: the workspace for an admin, else the
    /// user — when that is offered; else the first choice.
    public static func defaultOwner(_ who: Whoami) -> String {
        let choices = owners(who)
        let want = who.admin ? "" : (who.userID.isEmpty ? "" : "user:\(who.userID)")
        return choices.contains { $0.value == want } ? want : (choices.first?.value ?? "")
    }

    /// The web's slug: lower case, runs of anything but a–z and 0–9 as one
    /// `-`, none at the ends.
    public static func slug(_ name: String) -> String {
        var out = ""
        var dash = false
        for u in name.lowercased().unicodeScalars {
            if ("a"..."z").contains(u) || ("0"..."9").contains(u) {
                if dash, !out.isEmpty { out.append("-") }
                dash = false
                out.unicodeScalars.append(u)
            } else {
                dash = true
            }
        }
        return out
    }

    /// `apps/<slug>`, or nil for a name with no letters or digits.
    public static func tilePath(name: String) -> String? {
        let s = slug(name)
        return s.isEmpty ? nil : "apps/\(s)"
    }

    /// The create request (`owner` "" — the workspace — is left out: the
    /// server's default for an admin).
    public static func request(name: String, owner: String) -> APIRequest? {
        let title = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let p = tilePath(name: title) else { return nil }
        var body: [String: JSONValue] = ["path": .string(p), "title": .string(title)]
        if !owner.isEmpty { body["owner"] = .string(owner) }
        return .json("POST", path, .object(body))
    }
}

/// What tiles report about themselves (`xbin.status`): `GET
/// /api/xbin/tile-report` → `{statuses:{<tile>:{level,message,ts}}}`, kept
/// current by `status` events. A transient report is a one-shot
/// notification, not a status; `ok` with no message clears one.
public struct TileStatuses: Sendable, Equatable {
    public static let path = "/api/xbin/tile-report"

    public struct Status: Sendable, Equatable {
        /// `ok` | `info` | `warn` | `error` (anything else reads as info).
        public var level: String
        public var message: String

        public init(level: String, message: String = "") {
            self.level = level
            self.message = message
        }
    }

    public var byTile: [String: Status]

    public init(byTile: [String: Status] = [:]) { self.byTile = byTile }

    public init(json: JSONValue?) {
        var m: [String: Status] = [:]
        for (tile, v) in json?["statuses"]?.objectValue ?? [:] {
            let level = v["level"]?.stringValue ?? ""
            let message = v["message"]?.stringValue ?? ""
            if Self.clears(level, message) { continue }
            m[tile] = Status(level: level, message: message)
        }
        byTile = m
    }

    public subscript(tile: String) -> Status? { byTile[tile] }

    /// A `status` event.
    public mutating func apply(tile: String, level: String, message: String, transient: Bool) {
        guard !transient, !tile.isEmpty else { return }
        byTile[tile] = Self.clears(level, message) ? nil : Status(level: level, message: message)
    }

    static func clears(_ level: String, _ message: String) -> Bool { level.isEmpty || (level == "ok" && message.isEmpty) }
}
