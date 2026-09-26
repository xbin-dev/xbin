import Foundation

// Live Activities for agent turns (native/spec/push.md §7): the lock-screen
// and Dynamic Island card of a turn that runs. Everything that doesn't draw
// is here — the content state and attributes (their JSON is the relay's
// wire), what a transcript says the card shows, when the app starts,
// updates and ends one, and the strings the widget draws — so it is tested
// on Linux. The ActivityKit conformance, the widget and the app's glue are
// in native/ios/Widgets and App/Push.
//
// The card is generic on purpose: an ActivityKit payload can't be sealed,
// so what xbind pushes through the relay is only the phase, since when and
// how many requests wait. Names (the workspace's, the session's) exist only
// on the device: the app puts them in the attributes when it starts an
// activity itself; one started by push carries none, and the widget looks
// the workspace up by `ws` in the app's own records.

/// A turn as the card shows it.
public enum AgentActivityPhase: String, Sendable, Hashable, Codable, CaseIterable {
    /// The agent is working.
    case running
    /// A permission request or a question waits for the user.
    case waiting
    /// The turn is over (the card's last state before it goes).
    case idle
}

/// The content state (ActivityKit's `ContentState`). Its JSON is what the
/// relay builds (`{"phase", "since", "pending"}`); decoding is lenient — an
/// unknown phase reads as running, a missing number as 0 — so a newer
/// xbind never leaves the card undecodable.
public struct AgentActivityState: Sendable, Hashable, Codable {
    public var phase: AgentActivityPhase
    /// Unix seconds the turn started; 0 = unknown (no elapsed time shown).
    public var since: Int64
    /// Permission requests and questions waiting.
    public var pending: Int

    public init(phase: AgentActivityPhase, since: Int64 = 0, pending: Int = 0) {
        self.phase = phase
        self.since = since
        self.pending = pending
    }

    enum CodingKeys: String, CodingKey { case phase, since, pending }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let raw = (try? c.decodeIfPresent(String.self, forKey: .phase)) ?? nil
        phase = raw.flatMap(AgentActivityPhase.init(rawValue:)) ?? .running
        since = Self.int64(c, .since)
        pending = Int(clamping: Self.int64(c, .pending))
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(phase, forKey: .phase)
        try c.encode(since, forKey: .since)
        try c.encode(pending, forKey: .pending)
    }

    /// A whole number, whether the coder wrote it as an integer or a double.
    private static func int64(_ c: KeyedDecodingContainer<CodingKeys>, _ k: CodingKeys) -> Int64 {
        if let v = try? c.decodeIfPresent(Int64.self, forKey: k) { return v }
        if let d = try? c.decodeIfPresent(Double.self, forKey: k), d.isFinite { return Int64(d) }
        return 0
    }

    /// When the turn started, when known.
    public var startDate: Date? { since > 0 ? Date(timeIntervalSince1970: TimeInterval(since)) : nil }

    /// The card's last state for a turn that ended.
    public var finished: AgentActivityState { AgentActivityState(phase: .idle, since: since, pending: 0) }
}

/// The attributes (ActivityKit's `ActivityAttributes`, fixed for an
/// activity's life). The conformance itself is declared where ActivityKit
/// is (native/ios/Widgets/Shared); a push-to-start names this type
/// ("attributes-type": "AgentActivityAttributes") and fills only `ws` and
/// `ref`. Decoding is lenient: a missing field reads as "".
public struct AgentActivityAttributes: Sendable, Hashable, Codable {
    public typealias ContentState = AgentActivityState

    /// xbind's push id of the workspace (`workspace` of the push
    /// registration; "" when the app has none yet).
    public var ws: String
    /// The reference of an activity xbind started by push: the app
    /// registers the activity's token under it ("" for the app's own).
    public var ref: String
    /// Display names, set by the app ("" in a push-started one: the widget
    /// looks the workspace up by `ws`).
    public var workspace: String
    public var session: String
    /// The app's workspace id and the agent session id (the card's link);
    /// "" in a push-started one.
    public var appWorkspace: String
    public var sessionID: String

    public init(ws: String = "", ref: String = "", workspace: String = "", session: String = "",
                appWorkspace: String = "", sessionID: String = "") {
        self.ws = ws
        self.ref = ref
        self.workspace = workspace
        self.session = session
        self.appWorkspace = appWorkspace
        self.sessionID = sessionID
    }

    enum CodingKeys: String, CodingKey { case ws, ref, workspace, session, appWorkspace, sessionID }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func s(_ k: CodingKeys) -> String { ((try? c.decodeIfPresent(String.self, forKey: k)) ?? nil) ?? "" }
        self.init(ws: s(.ws), ref: s(.ref), workspace: s(.workspace), session: s(.session),
                  appWorkspace: s(.appWorkspace), sessionID: s(.sessionID))
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(ws, forKey: .ws)
        try c.encode(ref, forKey: .ref)
        try c.encode(workspace, forKey: .workspace)
        try c.encode(session, forKey: .session)
        try c.encode(appWorkspace, forKey: .appWorkspace)
        try c.encode(sessionID, forKey: .sessionID)
    }

    /// Started by push (xbind), not by the app.
    public var pushStarted: Bool { !ref.isEmpty }
}

extension AgentTranscript {
    /// What the card shows now: nil between turns (from the turn's end on,
    /// even while the idle status is still to come, and once the session is
    /// over). Waiting when the agent says so or a request is unanswered;
    /// `since` is the turn's start (xbind computes the same from the log).
    public var activityState: AgentActivityState? {
        guard state.isBusy, let started = state.turnStartedAt else { return nil }
        let n = min(pendingPermissions.count + pendingQuestions.count, 99)
        let phase: AgentActivityPhase = (state.status == .waitingPermission || n > 0) ? .waiting : .running
        return AgentActivityState(phase: phase, since: started / 1000, pending: n)
    }
}

/// When the app starts, updates and ends a card (the app decides locally
/// while it runs; xbind pushes the rest). A card appears for a turn that
/// has run `startAfter` (quick turns at the desk never flash one), at once
/// for one that waits for the user, or when the app is about to leave the
/// foreground mid-turn (ActivityKit starts activities only from the
/// foreground); it follows the state; it ends with the turn, staying
/// `dismissAfter` on the lock screen.
public struct AgentActivityPolicy: Sendable, Hashable {
    public var startAfter: TimeInterval
    public var dismissAfter: TimeInterval
    /// How long a running card may go without news before it reads stale.
    public var staleAfter: TimeInterval

    public init(startAfter: TimeInterval = 10, dismissAfter: TimeInterval = 600, staleAfter: TimeInterval = 4 * 3600) {
        self.startAfter = startAfter
        self.dismissAfter = dismissAfter
        self.staleAfter = staleAfter
    }

    public enum Action: Sendable, Hashable {
        case none
        case start(AgentActivityState)
        case update(AgentActivityState)
        /// The final state, and when the system should remove the card.
        case end(AgentActivityState, dismissAt: Date)
    }

    public struct Decision: Sendable, Hashable {
        public var action: Action
        /// Decide again then (a turn that is not old enough yet).
        public var recheckAt: Date?

        public init(_ action: Action, recheckAt: Date? = nil) {
            self.action = action
            self.recheckAt = recheckAt
        }
    }

    /// - Parameters:
    ///   - state: the session's `activityState` (nil between turns).
    ///   - shown: what the card shows (nil = no card).
    ///   - canStart: the app is in the foreground and may start one (the
    ///     user allows Live Activities and did not dismiss this turn's).
    ///   - leaving: the app is about to leave the foreground.
    public func decide(state: AgentActivityState?, shown: AgentActivityState?, canStart: Bool,
                       leaving: Bool = false, now: Date) -> Decision {
        switch (state, shown) {
        case (nil, nil):
            return Decision(.none)
        case (nil, let s?):
            return Decision(.end(s.finished, dismissAt: now.addingTimeInterval(dismissAfter)))
        case (let s?, let cur?):
            return Decision(s == cur ? .none : .update(s))
        case (let s?, nil):
            guard canStart else { return Decision(.none) }
            if s.phase == .waiting || leaving { return Decision(.start(s)) }
            guard let start = s.startDate else {
                return Decision(.none) // no clock to count from: only a wait or leaving starts it
            }
            let due = start.addingTimeInterval(startAfter)
            return due <= now ? Decision(.start(s)) : Decision(.none, recheckAt: due)
        }
    }
}

/// What the card says, as strings and symbol names — the widget only lays
/// them out.
public struct AgentActivityDisplay: Sendable, Hashable {
    /// The session's name, else "Agent".
    public var title: String
    /// The workspace's name, else "xbin".
    public var subtitle: String
    /// "Working", "Waiting for you", "2 waiting for you", "Finished";
    /// "Status unknown" when stale and not finished.
    public var status: String
    /// A short form for the compact Dynamic Island ("", "!", "2").
    public var badge: String
    /// SF Symbol.
    public var symbol: String
    public var phase: AgentActivityPhase
    public var stale: Bool
    /// Count elapsed time from here (nil: unknown, or finished).
    public var timerStart: Date?
    /// `xbin://<app workspace>/agent/<session>`, `xbin://<app workspace>`,
    /// or nil (a card the app can't place).
    public var link: String?

    /// - Parameters:
    ///   - workspaceTitle, appWorkspace: the app's own records of
    ///     `attributes.ws` (for a push-started card, which carries neither).
    public init(attributes a: AgentActivityAttributes, state s: AgentActivityState, stale: Bool,
                workspaceTitle: String? = nil, appWorkspace: String? = nil) {
        title = a.session.isEmpty ? "Agent" : a.session
        let ws = a.workspace.isEmpty ? (workspaceTitle ?? "") : a.workspace
        subtitle = ws.isEmpty ? "xbin" : ws
        phase = s.phase
        self.stale = stale && s.phase != .idle
        switch s.phase {
        case .running:
            status = "Working"
            symbol = "sparkles"
            badge = ""
        case .waiting:
            status = s.pending > 1 ? "\(s.pending) waiting for you" : "Waiting for you"
            symbol = "hand.raised.fill"
            badge = s.pending > 1 ? "\(s.pending)" : "!"
        case .idle:
            status = "Finished"
            symbol = "checkmark.circle.fill"
            badge = ""
        }
        if self.stale {
            status = "Status unknown"
            symbol = "questionmark.circle"
        }
        timerStart = s.phase == .idle || self.stale ? nil : s.startDate
        let app = a.appWorkspace.isEmpty ? (appWorkspace ?? "") : a.appWorkspace
        link = Self.link(appWorkspace: app, session: a.sessionID)
    }

    /// The card's deep link (plans/native.md §4: `xbin://<ws>/agent/<id>`).
    static func link(appWorkspace: String, session: String) -> String? {
        let allowed = CharacterSet.urlHostAllowed.subtracting(CharacterSet(charactersIn: "/?#@:"))
        guard !appWorkspace.isEmpty, let ws = appWorkspace.addingPercentEncoding(withAllowedCharacters: allowed) else { return nil }
        guard !session.isEmpty, let id = session.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/?#")))
        else { return "xbin://\(ws)" }
        return "xbin://\(ws)/agent/\(id)"
    }
}
