import Foundation

// The app's own `/ws/events` socket (plans/native.md §7.7, §13; docs/protocol.md
// "`/ws/events` — event stream"): one per workspace shown in a foreground
// window, authenticated with the workspace session as a bearer — a human
// principal, so it is the app's alone (never handed to tile code, never
// injected into a web view; §2 invariant 1). What arrives:
//
// - `reload` for a component → the open native runtime or web page of the
//   most specific open tile covering it reloads (live reload, §7.7);
// - `session` (the caller's agent sessions, D74/D97) → that session's feed;
// - `term` → the session directory: `status` carries the session's summary
//   inline (the Needs-you inbox), `open`/`close`/`rename` mean re-list;
// - `branding` → re-read the workspace's title and icon;
// - `native` → the workspace's native-runtime switch changed: re-read whoami
//   (`native.runtime`), so an admin turning native views off reaches every
//   open app at once (plans/native.md §23).
//
// This file is the pure half: parsing frames, picking the reload target and
// the reconnect policy. The socket (URLSessionWebSocketTask) is app code.

/// One `/ws/events` frame the app acts on.
public enum AppEvent: Sendable, Equatable {
    /// A component's source changed.
    case reload(component: String)
    /// Build progress of a component with a backend (`build-start`,
    /// `build-error` with the compiler's text, `build-ok`).
    case build(component: String, phase: BuildPhase, text: String)
    /// The grant table changed (`component` when the event names one).
    case grants(component: String?)
    /// The workspace's title or icon changed (D76).
    case branding
    /// The workspace's native-runtime switch changed (an admin's `PUT
    /// /api/xbin/native-runtime`): re-read whoami's `native.runtime`.
    case nativeSwitch
    /// A terminal or agent session of the caller changed (D73).
    case term(TermEvent)
    /// An agent session event (D74). `frame` is the whole frame's text, for
    /// XbinAgent's `SessionHubEvent(json:)`.
    case session(id: String, component: String, frame: String)
    /// A tile reported its condition (`status`).
    case tileStatus(component: String, level: String, message: String)
    /// Anything else (`bus`, `pr`, future types): ignored by the app.
    case other(type: String)

    public enum BuildPhase: String, Sendable { case start, error, ok }

    /// Parses one text frame; nil for anything that isn't a JSON object
    /// with a `type`.
    public static func parse(_ text: String) -> AppEvent? {
        guard let j = try? JSONValue(parsing: text), let type = j["type"]?.stringValue else { return nil }
        let component = j["component"]?.stringValue ?? ""
        switch type {
        case "reload":
            return component.isEmpty ? .other(type: type) : .reload(component: component)
        case "build-start", "build-error", "build-ok":
            let phase: BuildPhase = type == "build-start" ? .start : type == "build-ok" ? .ok : .error
            return .build(component: component, phase: phase, text: j["text"]?.stringValue ?? "")
        case "grants":
            return .grants(component: component.isEmpty ? nil : component)
        case "branding":
            return .branding
        case "native":
            return .nativeSwitch
        case "term":
            guard let d = j["data"], let t = TermEvent(component: component, data: d) else { return .other(type: type) }
            return .term(t)
        case "session":
            let d = j["data"]
            let topic = j["topic"]?.stringValue ?? ""
            var id = d?["id"]?.stringValue ?? ""
            if id.isEmpty, topic.hasPrefix("session.") { id = String(topic.dropFirst("session.".count)) }
            guard !id.isEmpty else { return .other(type: type) }
            return .session(id: id, component: component, frame: text)
        case "status":
            let d = j["data"]
            return .tileStatus(component: component, level: d?["level"]?.stringValue ?? "",
                               message: d?["message"]?.stringValue ?? "")
        default:
            return .other(type: type)
        }
    }
}

/// A `term` event: `{op, id, user}` plus, for `status`, the agent session's
/// summary (`status`, `pending`, `questions`, `turn`).
public struct TermEvent: Sendable, Equatable {
    public enum Op: String, Sendable { case open, close, rename, status }

    public var component: String
    public var op: Op
    public var id: String
    public var user: String
    /// `status` only: starting | idle | running | waiting_permission |
    /// cancelling | error | exited.
    public var status: String?
    /// `status` only: unanswered permission requests.
    public var pending: Int?
    /// `status` only: unanswered questions (elicitations).
    public var questions: Int?
    /// `status` only: prompts taken so far.
    public var turn: Int?

    public init(component: String, op: Op, id: String, user: String = "", status: String? = nil,
                pending: Int? = nil, questions: Int? = nil, turn: Int? = nil) {
        self.component = component
        self.op = op
        self.id = id
        self.user = user
        self.status = status
        self.pending = pending
        self.questions = questions
        self.turn = turn
    }

    /// nil for an unknown op or no id (ignored, as the web ignores them).
    init?(component: String, data: JSONValue) {
        guard let raw = data["op"]?.stringValue, let op = Op(rawValue: raw),
              let id = data["id"]?.stringValue, !id.isEmpty else { return nil }
        self.init(component: component, op: op, id: id, user: data["user"]?.stringValue ?? "")
        if op == .status {
            status = data["status"]?.stringValue
            pending = data["pending"]?.intValue.map { Int($0) }
            questions = data["questions"]?.intValue.map { Int($0) }
            turn = data["turn"]?.intValue.map { Int($0) }
        }
    }

    /// True when the directory must be re-read (the row itself changed or
    /// went away); a `status` carries its own row data.
    public var needsRelist: Bool { op != .status }

    /// Whether this is a session of the user `userID`. xbind sends each
    /// user's term events to that user — and to every admin, so an admin's
    /// socket also carries other users' (D97). `user` is the session's home
    /// key (internal/term/homes.go); an event without one is taken as ours.
    public func isFor(userID: String) -> Bool {
        user.isEmpty || user == Self.homeKey(userID: userID)
    }

    /// internal/term/homes.go `sanitizeHomeKey`: every character outside
    /// `[A-Za-z0-9._-]` becomes `_`; empty or dots only is `owner`.
    public static func homeKey(userID: String) -> String {
        var out = ""
        for u in userID.unicodeScalars {
            switch u {
            case "a"..."z", "A"..."Z", "0"..."9", "-", "_", ".": out.unicodeScalars.append(u)
            default: out.append("_")
            }
        }
        return out.allSatisfy({ $0 == "." }) ? "owner" : out
    }
}

/// Live-reload targeting, as the web shell does it (web/events-socket.js
/// `isReloadTarget`): a change in `component` reloads the most specific open
/// tile that covers it — the tile itself or its nearest open ancestor — so a
/// change inside `apps/calendar/widgets/x` reloads only `apps/calendar/widgets`
/// when both it and `apps/calendar` are open.
public enum ReloadTargets {
    /// True when a change in `component` is inside `tile`.
    public static func covers(_ tile: String, _ component: String) -> Bool {
        let t = trim(tile), c = trim(component)
        guard !t.isEmpty else { return false }
        return c == t || c.hasPrefix(t + "/")
    }

    /// The open tile a change in `component` reloads, or nil.
    public static func target(for component: String, open: some Sequence<String>) -> String? {
        var best: String?
        for t in open where covers(t, component) {
            if best == nil || trim(t).count > trim(best!).count { best = t }
        }
        return best
    }

    static func trim(_ s: String) -> String {
        var s = Substring(s)
        while s.hasPrefix("/") { s = s.dropFirst() }
        while s.hasSuffix("/") { s = s.dropLast() }
        return String(s)
    }
}

/// The `/ws/events` URL path. The app authenticates with
/// `Authorization: Bearer <session>` on the upgrade (no `?frame=`: that is
/// for tiles).
public enum EventsRoute {
    public static let path = "/ws/events"

    /// `ws(s)://host[:port]/ws/events`.
    public static func url(on origin: ServerOrigin) -> URL? { URL(string: origin.webSocketOrigin + path) }
}

/// When the events socket connects, reconnects and gives up. A value type
/// driven by the socket's owner: every input returns what to do next.
///
/// - It runs only while ``wanted`` (a foreground window shows the
///   workspace); going to the background closes it and cancels any retry.
/// - A drop or a network error reconnects after a backoff: 0.5 s doubling
///   to 15 s (the bundled web clients' numbers), reset once a socket opens.
/// - A 401 on the upgrade means the session died: re-sign once, then
///   connect; a second 401 in a row waits for the next foreground.
/// - A 403 or 404 stops until the next foreground (not allowed; an xbind
///   without the route).
public struct EventSocketPolicy: Sendable, Equatable {
    public enum Close: Sendable, Equatable {
        /// An open socket closed (the server evicted a slow consumer, a
        /// proxy cut it, the network changed).
        case dropped
        /// The upgrade was answered with this HTTP status.
        case refused(status: Int)
        /// No connection (DNS, TCP, TLS, offline).
        case unreachable
    }

    public enum Action: Sendable, Equatable {
        /// Open a socket now.
        case connect
        /// Open a socket after this many seconds.
        case retry(after: Double)
        /// Get a fresh session (re-sign), then ``connect``.
        case reauthenticate
        /// Close the socket, schedule nothing.
        case disconnect
        /// Nothing to do (already in that state).
        case none
    }

    public static let initialBackoff = 0.5
    public static let maxBackoff = 15.0

    public private(set) var wanted = false
    public private(set) var connected = false
    public private(set) var connecting = false
    /// The next retry's delay (before jitter).
    public private(set) var backoff = EventSocketPolicy.initialBackoff
    /// A socket has opened before in this policy's life (a reopen means
    /// events may have been missed: re-list, catch up).
    public private(set) var everOpened = false
    /// Stopped until the next ``setWanted(_:)`` (403/404, a second 401).
    public private(set) var parked = false
    private var reauthed = false

    public init() {}

    /// A foreground window shows the workspace (true), or none does / the
    /// app went to the background (false).
    public mutating func setWanted(_ on: Bool) -> Action {
        if on == wanted { return on && !connected && !connecting && parked ? unpark() : .none }
        wanted = on
        if !on {
            connected = false
            connecting = false
            return .disconnect
        }
        return unpark()
    }

    private mutating func unpark() -> Action {
        parked = false
        reauthed = false
        backoff = Self.initialBackoff
        connecting = true
        return .connect
    }

    /// The retry timer fired (or a re-sign finished).
    public mutating func retryDue() -> Action {
        guard wanted, !connected, !connecting, !parked else { return .none }
        connecting = true
        return .connect
    }

    /// The socket opened. Returns true when events may have been missed
    /// since an earlier socket (re-list the sessions, catch feeds up).
    public mutating func opened() -> Bool {
        let missed = everOpened
        connected = true
        connecting = false
        everOpened = true
        backoff = Self.initialBackoff
        reauthed = false
        return missed
    }

    /// The socket closed or never opened. `jitter` scales the delay (tests
    /// pass 1; the app a random 0.8…1.2).
    public mutating func closed(_ why: Close, jitter: Double = 1) -> Action {
        connected = false
        connecting = false
        guard wanted else { return .none }
        switch why {
        case .refused(let status) where status == 401:
            if reauthed { parked = true; return .none }
            reauthed = true
            return .reauthenticate
        case .refused(let status) where status == 403 || status == 404:
            parked = true
            return .none
        default:
            let d = backoff * jitter
            backoff = min(backoff * 2, Self.maxBackoff)
            return .retry(after: d)
        }
    }

    /// No session to connect with — the re-sign after a 401 failed, or
    /// there was none to start with (the user must act): park until the
    /// next foreground.
    public mutating func reauthFailed() -> Action {
        connected = false
        connecting = false
        parked = true
        return .none
    }
}
