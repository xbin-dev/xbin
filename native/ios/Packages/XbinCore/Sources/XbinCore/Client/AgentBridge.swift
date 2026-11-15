import Foundation

// What the workspace agent's native view may ask of the app's chrome, and
// what the chrome tells it (D233; docs/protocol/delegation.md §The chrome).
// The view is a tile's runtime — it renders what a model wrote — so, as on
// the web, it can only ASK the chrome to show something of the chrome's own,
// by id: the app reads the request, the change set or the session from
// xbind with the person's session and draws that. No message approves,
// denies, resumes or changes a preference.
//
// This is the app's reading of `xbin.native.agent(msg)` (a `{op:"call",
// what:"agent", args:{msg}}`, ``BridgeCall/agentMessage``), held to the web
// chrome's own `pageMessage()` (web/agent-chrome-words.js): the same shapes,
// the same fields and no other.

/// The app's decision about one `agent` message of a native tile.
public enum AgentBridge {
    /// One of the chrome's panels a message may ask for.
    public enum View: String, Sendable, Equatable, CaseIterable {
        case approvals
        case activity
        case prefs
    }

    public enum Action: Sendable, Equatable {
        /// Show the approval of one request (the app fetches it by id; an id
        /// that isn't the person's shows nothing).
        case openRequest(String)
        /// Show the review of one change set.
        case openChangeset(String)
        /// Show one of the person's sessions in its tile — or, for
        /// `session` `"new"`, a new session of the tile's own agent with
        /// `draft` (plain text, the person's own words) waiting unsent in
        /// its composer. `draft` is nil for an existing session.
        case openSession(session: String, tile: String, draft: String?)
        /// Show Approvals, What my agent did or Preferences.
        case openView(View)
        /// Do nothing; reject the tile's promise with this.
        case refuse(String)
    }

    public static let openRequestType = "xbin:agent-open-request"
    public static let openChangesetType = "xbin:agent-open-changeset"
    public static let openSessionType = "xbin:open-session"
    public static let openViewType = "xbin:agent-open"
    public static let contextType = "xbin:agent-context"

    /// The most of a person's words a new session's draft carries, in
    /// characters (UTF-16 units, as the web counts them).
    public static let draftMax = 8000

    /// What `msg` — the `msg` of an `agent` call from the native tile at
    /// `tile` — asks for. `holder` is the component xbind names as the
    /// person's agent (`agent` of `GET /delegate/state`; nil: they have
    /// none): any other tile can ask the chrome nothing.
    public static func decide(_ msg: JSONValue, tile: String, holder: String?) -> Action {
        guard let holder, !holder.isEmpty, tile == holder else {
            return .refuse("agent: only the person's workspace agent may ask the app's chrome")
        }
        guard case .object(let o) = msg, case .string(let type)? = o["type"] else {
            return .refuse("agent: a message {type, …}")
        }
        let id = o["id"]?.stringValue.flatMap { isID($0) ? $0 : nil }
        switch type {
        case openRequestType:
            guard let id else { return .refuse("agent: not a request id") }
            return .openRequest(id)
        case openChangesetType:
            guard let id else { return .refuse("agent: not a change set id") }
            return .openChangeset(id)
        case openSessionType:
            guard let t = tilePath(o["tile"]), let s = o["session"]?.stringValue, isSession(s) else {
                return .refuse("agent: not a session of a tile")
            }
            if s == "new" { return .openSession(session: s, tile: t, draft: plainDraft(o["draft"])) }
            return .openSession(session: s, tile: t, draft: nil)
        case openViewType:
            guard let v = o["view"]?.stringValue.flatMap(View.init(rawValue:)) else { return .refuse("agent: not a view") }
            return .openView(v)
        default:
            return .refuse("agent: \(String(type.prefix(64))) is not something the app's chrome does")
        }
    }

    /// The decision about a runtime's call: ``decide(_:tile:holder:)`` on
    /// its message; a call that isn't an `agent` call with a message is
    /// refused.
    public static func decide(_ call: BridgeCall, tile: String, holder: String?) -> Action {
        guard let msg = call.agentMessage else { return .refuse("agent: a message {type, …}") }
        return decide(msg, tile: tile, holder: holder)
    }

    /// What the chrome tells the agent's view it was opened about
    /// (``RuntimeCall/agent(_:)``): `{type: "xbin:agent-context", tile}`,
    /// else `{…, screen}` (a name of at most 200 characters), else the bare
    /// type — opened about nothing, which clears the view's chip.
    public static func context(tile: String?, screen: String?) -> JSONValue {
        if let t = tile.flatMap({ tilePath(.string($0)) }) {
            return ["type": .string(contextType), "tile": .string(t)]
        }
        if let s = screen, !s.isEmpty, s.utf16.count <= 200 {
            return ["type": .string(contextType), "screen": .string(s)]
        }
        return ["type": .string(contextType)]
    }

    // MARK: The shapes (web/agent-chrome-words.js: ID, SESSION, tilePath, plainDraft)

    /// `^[A-Za-z0-9_.:-]{1,80}$`
    static func isID(_ s: String) -> Bool {
        let u = s.unicodeScalars
        return (1...80).contains(u.count) && u.allSatisfy { isWord($0) || $0 == "." || $0 == ":" }
    }

    /// `^[A-Za-z0-9_-]{1,64}$`
    static func isSession(_ s: String) -> Bool {
        let u = s.unicodeScalars
        return (1...64).contains(u.count) && u.allSatisfy(isWord)
    }

    private static func isWord(_ c: Unicode.Scalar) -> Bool {
        switch c {
        case "A"..."Z", "a"..."z", "0"..."9", "_", "-": return true
        default: return false
        }
    }

    /// A tile's path: slashes trimmed at the ends, at most 512 UTF-16
    /// units, no empty, `.` or `..` segment — or nil.
    static func tilePath(_ v: JSONValue?) -> String? {
        guard case .string(let raw)? = v else { return nil }
        var t = Substring(raw)
        while t.first == "/" { t = t.dropFirst() }
        while t.last == "/" { t = t.dropLast() }
        guard !t.isEmpty, t.utf16.count <= 512 else { return nil }
        for seg in t.split(separator: "/", omittingEmptySubsequences: false) where seg.isEmpty || seg == "." || seg == ".." {
            return nil
        }
        return String(t)
    }

    /// Words for a new session's composer, as plain text — never markup,
    /// never a command: line ends made `\n`, control characters (but
    /// newline and tab) and the invisible ones that reorder or hide text
    /// dropped, trimmed, at most ``draftMax`` characters; `""` for
    /// anything that isn't a string.
    static func plainDraft(_ v: JSONValue?) -> String {
        guard case .string(let raw)? = v else { return "" }
        var out = String.UnicodeScalarView()
        var afterCR = false
        for c in raw.unicodeScalars {
            if c == "\r" { out.append("\n"); afterCR = true; continue }
            if c == "\n" {
                if !afterCR { out.append("\n") }
                afterCR = false
                continue
            }
            afterCR = false
            if !unseen(c) { out.append(c) }
        }
        let trimmed = trimJS(String(out))
        return cut(trimmed, utf16: draftMax)
    }

    /// `[\u0000-\u0008\u000B-\u001F\u007F-\u009F​-‏‪-‮⁦-⁩﻿]`
    private static func unseen(_ c: Unicode.Scalar) -> Bool {
        switch c.value {
        case 0x00...0x08, 0x0B...0x1F, 0x7F...0x9F, 0x200B...0x200F, 0x202A...0x202E, 0x2066...0x2069, 0xFEFF: return true
        default: return false
        }
    }

    /// JavaScript's `String.prototype.trim`: white space and line
    /// terminators at both ends.
    private static func trimJS(_ s: String) -> String {
        func space(_ c: Unicode.Scalar) -> Bool {
            switch c.value {
            case 0x09...0x0D, 0x20, 0xA0, 0x1680, 0x2000...0x200A, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF: return true
            default: return false
            }
        }
        var u = Substring.UnicodeScalarView(s.unicodeScalars)
        while let f = u.first, space(f) { u.removeFirst() }
        while let l = u.last, space(l) { u.removeLast() }
        return String(u)
    }

    /// The first `n` UTF-16 units of `s` (the web's `slice(0, n)`), never
    /// half a surrogate pair.
    private static func cut(_ s: String, utf16 n: Int) -> String {
        guard s.utf16.count > n else { return s }
        var out = String.UnicodeScalarView()
        var used = 0
        for c in s.unicodeScalars {
            let w = c.value > 0xFFFF ? 2 : 1
            if used + w > n { break }
            out.append(c)
            used += w
        }
        return String(out)
    }
}
