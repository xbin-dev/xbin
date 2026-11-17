import Foundation

// The workspace agent's Live Activity (D241; plans/wsagent/ios-stage2.md
// §5; native/spec/push.md §7.5): one card per workspace while the person's
// agent works or something waits for them. Everything that doesn't draw is
// here — the state and attributes (their JSON is the relay's wire), when the
// app starts, updates and ends the card, and the words the widget draws — so
// it is tested on Linux. The ActivityKit conformance, the widget and the
// app's glue are in native/ios/Widgets and App/Push/AgentLiveActivity.swift.
//
// Counts and a phase only: the pushed state goes through the relay unsealed,
// so no tile name, request text or conversation title is ever in it — the
// same on the lock screen, in the Dynamic Island and in what the app sets.

/// What the card says the agent is doing.
public enum WorkspaceAgentPhase: String, Sendable, Hashable, Codable, CaseIterable {
    /// Its turn runs, or tile agents it started are working.
    case running
    /// Something waits for the person (whatever else runs).
    case waiting
    /// The person paused it (the card's last state).
    case paused
    /// Nothing works or waits (the card's last state, or its grace).
    case idle
}

/// The content state; decoding is lenient (an unknown phase reads as
/// running, a missing number as 0), so a newer xbind never leaves the card
/// undecodable — and an older relay's three fields decode too.
public struct WorkspaceAgentActivityState: Sendable, Hashable, Codable {
    public var phase: WorkspaceAgentPhase
    /// Unix seconds this stretch began (0 = unknown: no elapsed time shown).
    public var since: Int64
    /// What waits for the person (0–99).
    public var pending: Int
    /// Tile agents it started that are working (0–99).
    public var agents: Int

    public init(phase: WorkspaceAgentPhase, since: Int64 = 0, pending: Int = 0, agents: Int = 0) {
        self.phase = phase
        self.since = since
        self.pending = min(max(pending, 0), 99)
        self.agents = min(max(agents, 0), 99)
    }

    enum CodingKeys: String, CodingKey { case phase, since, pending, agents }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let raw = (try? c.decodeIfPresent(String.self, forKey: .phase)) ?? nil
        phase = raw.flatMap(WorkspaceAgentPhase.init(rawValue:)) ?? .running
        since = Self.int64(c, .since)
        pending = Int(clamping: min(max(Self.int64(c, .pending), 0), 99))
        agents = Int(clamping: min(max(Self.int64(c, .agents), 0), 99))
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(phase, forKey: .phase)
        try c.encode(since, forKey: .since)
        try c.encode(pending, forKey: .pending)
        try c.encode(agents, forKey: .agents)
    }

    private static func int64(_ c: KeyedDecodingContainer<CodingKeys>, _ k: CodingKeys) -> Int64 {
        if let v = try? c.decodeIfPresent(Int64.self, forKey: k) { return v }
        if let d = try? c.decodeIfPresent(Double.self, forKey: k), d.isFinite { return Int64(d) }
        return 0
    }

    public var startDate: Date? { since > 0 ? Date(timeIntervalSince1970: TimeInterval(since)) : nil }

    /// Over: paused or idle (the card's last states).
    public var isOver: Bool { phase == .paused || phase == .idle }
}

/// The attributes (fixed for the card's life). A push-to-start names this
/// type ("attributes-type": "WorkspaceAgentActivityAttributes") and fills
/// only `ws` and `ref`; a missing field reads as "".
public struct WorkspaceAgentActivityAttributes: Sendable, Hashable, Codable {
    public typealias ContentState = WorkspaceAgentActivityState

    /// xbind's push id of the workspace.
    public var ws: String
    /// The reference of a card xbind started by push ("" for the app's own).
    public var ref: String
    /// The workspace's name (set by the app; "" in a push-started one).
    public var workspace: String
    /// The app's workspace id (the card's link; "" in a push-started one).
    public var appWorkspace: String

    public init(ws: String = "", ref: String = "", workspace: String = "", appWorkspace: String = "") {
        self.ws = ws
        self.ref = ref
        self.workspace = workspace
        self.appWorkspace = appWorkspace
    }

    enum CodingKeys: String, CodingKey { case ws, ref, workspace, appWorkspace }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        func s(_ k: CodingKeys) -> String { ((try? c.decodeIfPresent(String.self, forKey: k)) ?? nil) ?? "" }
        self.init(ws: s(.ws), ref: s(.ref), workspace: s(.workspace), appWorkspace: s(.appWorkspace))
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(ws, forKey: .ws)
        try c.encode(ref, forKey: .ref)
        try c.encode(workspace, forKey: .workspace)
        try c.encode(appWorkspace, forKey: .appWorkspace)
    }

    public var pushStarted: Bool { !ref.isEmpty }
}

/// What the app sees of its person's agent in a workspace (WorkspaceAgentModel).
public struct WorkspaceAgentPicture: Sendable, Hashable {
    /// They have an agent that may act now (workspace agents on, given, not held).
    public var has: Bool
    public var paused: Bool
    /// What waits for them because of it.
    public var pending: Int
    /// Tile agents it started that are working.
    public var agents: Int

    public init(has: Bool, paused: Bool = false, pending: Int = 0, agents: Int = 0) {
        self.has = has
        self.paused = paused
        self.pending = pending
        self.agents = agents
    }
}

/// When the app starts, updates and ends the card (it decides while it runs;
/// xbind pushes the rest). A card appears after `startAfter` of work (a quick
/// turn at the desk never flashes one), at once when something waits, or
/// when the app leaves the front with the agent working; idle, it says so and
/// goes after `idleGrace`; paused it goes at once on paused; with no agent
/// that may act it goes at once.
public struct WorkspaceAgentActivityPolicy: Sendable, Hashable {
    public var startAfter: TimeInterval
    public var idleGrace: TimeInterval
    public var dismissAfter: TimeInterval
    public var pausedShow: TimeInterval
    public var staleAfter: TimeInterval

    public init(startAfter: TimeInterval = 10, idleGrace: TimeInterval = 120, dismissAfter: TimeInterval = 300,
                pausedShow: TimeInterval = 60, staleAfter: TimeInterval = 4 * 3600) {
        self.startAfter = startAfter
        self.idleGrace = idleGrace
        self.dismissAfter = dismissAfter
        self.pausedShow = pausedShow
        self.staleAfter = staleAfter
    }

    public enum Action: Sendable, Hashable {
        case none
        case start(WorkspaceAgentActivityState)
        case update(WorkspaceAgentActivityState)
        case end(WorkspaceAgentActivityState, dismissAt: Date)
    }

    public struct Decision: Sendable, Hashable {
        public var action: Action
        /// Decide again then (a stretch not old enough to start, an idle grace).
        public var recheckAt: Date?
        public init(_ action: Action, recheckAt: Date? = nil) {
            self.action = action
            self.recheckAt = recheckAt
        }
    }

    /// - Parameters:
    ///   - picture: what the agent does now.
    ///   - shown: what the card shows (nil: no card).
    ///   - stretchSince: when the app first saw this stretch (work or a wait) begin.
    ///   - idleSince: when it first saw nothing working or waiting (nil while something is).
    ///   - canStart: in the front, the person allows Live Activities and the card, and didn't swipe this stretch's away.
    ///   - leaving: the app is about to leave the front.
    public func decide(picture p: WorkspaceAgentPicture, shown: WorkspaceAgentActivityState?, stretchSince: Date?,
                       idleSince: Date?, canStart: Bool, leaving: Bool = false, now: Date) -> Decision {
        let since = Int64((stretchSince ?? now).timeIntervalSince1970)
        guard p.has else {
            guard let s = shown else { return Decision(.none) }
            return Decision(.end(WorkspaceAgentActivityState(phase: .idle, since: s.since), dismissAt: now))
        }
        if p.paused {
            guard let s = shown else { return Decision(.none) }
            return Decision(.end(WorkspaceAgentActivityState(phase: .paused, since: s.since, pending: p.pending), dismissAt: now.addingTimeInterval(pausedShow)))
        }
        let working = p.agents > 0
        let waiting = p.pending > 0
        if working || waiting {
            let want = WorkspaceAgentActivityState(phase: waiting ? .waiting : .running, since: shown?.since ?? since, pending: p.pending, agents: p.agents)
            if let s = shown { return Decision(s == want ? .none : .update(want)) }
            guard canStart else { return Decision(.none) }
            if waiting || leaving { return Decision(.start(want)) }
            let due = (stretchSince ?? now).addingTimeInterval(startAfter)
            return due <= now ? Decision(.start(want)) : Decision(.none, recheckAt: due)
        }
        // nothing works or waits
        guard let s = shown else { return Decision(.none) }
        let idle = WorkspaceAgentActivityState(phase: .idle, since: s.since)
        let over = (idleSince ?? now).addingTimeInterval(idleGrace)
        if over <= now { return Decision(.end(idle, dismissAt: now.addingTimeInterval(dismissAfter))) }
        return Decision(s == idle ? .none : .update(idle), recheckAt: over)
    }
}

/// What the card says, as strings and symbol names — the widget lays them out.
public struct WorkspaceAgentActivityDisplay: Sendable, Hashable {
    /// "Workspace agent".
    public var title: String
    /// The workspace's name, else "xbin".
    public var subtitle: String
    /// "Working", "Needs you (2)", "Paused", "Done"; "Status unknown" when stale.
    public var status: String
    /// The counts in words ("3 tile agents working", "1 tile agent working",
    /// "Your agent can't act until you resume.", "" for none).
    public var detail: String
    /// The compact Dynamic Island's trailing text ("2", "3", "").
    public var badge: String
    /// SF Symbols.
    public var symbol: String
    public var phase: WorkspaceAgentPhase
    public var stale: Bool
    public var timerStart: Date?
    /// What a tap opens: what waits first while something waits, else the agent.
    public var link: String?
    /// The expanded island's second button: the agent itself.
    public var agentLink: String?
    /// The expanded island's first button, while something waits ("Review").
    public var reviewLabel: String?

    public init(attributes a: WorkspaceAgentActivityAttributes, state s: WorkspaceAgentActivityState, stale: Bool,
                workspaceTitle: String? = nil, appWorkspace: String? = nil) {
        title = "Workspace agent"
        let ws = a.workspace.isEmpty ? (workspaceTitle ?? "") : a.workspace
        subtitle = ws.isEmpty ? "xbin" : ws
        phase = s.phase
        self.stale = stale && !s.isOver
        let agentsLine = s.agents == 0 ? "" : s.agents == 1 ? "1 tile agent working" : "\(s.agents) tile agents working"
        switch s.phase {
        case .running:
            status = "Working"
            detail = agentsLine
            symbol = "square.on.square"
            badge = s.agents > 0 ? "\(s.agents)" : ""
        case .waiting:
            status = s.pending > 0 ? "Needs you (\(s.pending))" : "Needs you"
            detail = agentsLine
            symbol = "hand.raised.fill"
            badge = s.pending > 0 ? "\(s.pending)" : "!"
        case .paused:
            status = "Paused"
            detail = "Your agent can't act until you resume."
            symbol = "pause.fill"
            badge = ""
        case .idle:
            status = "Done"
            detail = "Nothing is running."
            symbol = "checkmark.square.fill"
            badge = ""
        }
        if self.stale {
            status = "Status unknown"
            symbol = "questionmark.square"
        }
        timerStart = s.isOver || self.stale ? nil : s.startDate
        let app = a.appWorkspace.isEmpty ? (appWorkspace ?? "") : a.appWorkspace
        agentLink = Self.link(appWorkspace: app, fragment: nil)
        link = s.phase == .waiting ? Self.link(appWorkspace: app, fragment: "first") : agentLink
        reviewLabel = s.phase == .waiting && !self.stale ? "Review" : nil
    }

    /// `xbin://<app workspace>/xbin/agent[#<fragment>]` (D241's router), or
    /// nil for a card the app can't place.
    static func link(appWorkspace: String, fragment: String?) -> String? {
        let allowed = CharacterSet.urlHostAllowed.subtracting(CharacterSet(charactersIn: "/?#@:"))
        guard !appWorkspace.isEmpty, let ws = appWorkspace.addingPercentEncoding(withAllowedCharacters: allowed) else { return nil }
        return "xbin://\(ws)/xbin/agent" + (fragment.map { "#" + $0 } ?? "")
    }
}
