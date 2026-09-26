import Foundation

// What the status events (and turn ends) say about the session as a whole:
// its status, the agent's modes, settings and slash commands, whether it
// needs a sign-in, context usage, the agent's title for the session. The
// transcript keeps it current; the composer and the header bind to it.

public struct AgentSessionState: Sendable, Hashable {
    /// Before the first status: starting (the create answered; the handshake runs).
    public var status: SessionStatus = .starting
    /// The last non-empty detail a status carried (an error names what to do) — sticky, as on the web.
    public var detail = ""
    public var currentMode: String?
    public var modes: [SessionMode] = []
    /// The agent's settings (model, effort, …) in its order.
    public var options: [ConfigOption] = []
    public var commands: [SlashCommand] = []
    public var agent: AgentInfo?
    /// The agent says it is signed out: offer the one-tap sign-in.
    public var login: LoginNeeded?
    /// Context usage: the latest a status or turn end reported.
    public var usage: Usage?
    /// The agent's own title for the session.
    public var title: String?
    /// The latest turn that ended, and how.
    public var lastTurn: Int?
    public var lastStopReason: StopReason?
    public var lastTurnError: String?
    /// Unix ms the running turn started (the prompt's user message, else the
    /// status that said running); nil between turns — a Live Activity's elapsed.
    public var turnStartedAt: Int64?

    public init() {}

    /// A turn is in flight.
    public var isBusy: Bool { status.isBusy }
    /// The session is over.
    public var isEnded: Bool { status.isFinal }

    mutating func apply(_ s: StatusUpdate, ts: Int64) {
        if let v = s.status {
            if v.isBusy, turnStartedAt == nil, ts != 0 { turnStartedAt = ts }
            if !v.isBusy { turnStartedAt = nil }
            status = v
        }
        if let d = s.detail, !d.isEmpty { detail = d }
        if let v = s.currentMode { currentMode = v }
        if let v = s.modes { modes = v }
        if let v = s.options { options = v }
        if let v = s.commands { commands = v }
        if let v = s.agent { agent = v }
        if let v = s.usage { usage = v }
        if let v = s.title { title = v }
        // `login` rides every status the driver builds while the agent is
        // signed out — but not its partial updates (commands/usage/title/
        // options alone), which must not clear the sign-in prompt
        if let l = s.login {
            login = l
        } else if !s.isPartial {
            login = nil
        }
    }

    mutating func userPrompted(ts: Int64) {
        if ts != 0 { turnStartedAt = ts }
    }

    mutating func apply(_ t: TurnEnd) {
        turnStartedAt = nil
        lastTurn = t.turn
        lastStopReason = t.stopReason
        lastTurnError = t.error
        if let u = t.usage { usage = u }
        if t.stopReason != .error { login = nil } // a turn that ran means the agent is signed in
    }

    /// The sign-in to offer: the agent's own report, else — when a create or
    /// prompt failed with an auth-looking error — the provider's login command.
    public func signIn(provider: AgentProvider?, lastError: String?) -> LoginNeeded? {
        if let login { return login }
        if let e = lastError, AgentAPIError.looksLikeAuth(e), let p = provider, !p.login.isEmpty {
            return LoginNeeded(provider: p.name, command: p.login)
        }
        return nil
    }

    /// The permission modes to offer: the agent's (last status that carried
    /// them), else the provider's advertised list (before the first status).
    public func availableModes(provider: AgentProvider?) -> [SessionMode] {
        modes.isEmpty ? (provider?.modes ?? []) : modes
    }

    /// The settings pickers the composer shows: one per `select` option with
    /// values, plus a mode picker only when no option already is the mode
    /// (an agent exposes its mode as modes OR as a config option — never both
    /// pickers). A mode picker's value is set with option id "mode".
    public func pickers(provider: AgentProvider? = nil) -> [SettingPicker] {
        let opts = options.filter { $0.type == "select" && !$0.values.isEmpty }
        var out: [SettingPicker] = []
        if !opts.contains(where: \.isModeOption) {
            let ms = availableModes(provider: provider)
            if !ms.isEmpty {
                out.append(SettingPicker(id: "mode", label: "mode", current: currentMode ?? "", isMode: true,
                                         values: ms.map { .init(value: $0.id, label: $0.name, description: $0.description, warning: $0.explicit) }))
            }
        }
        for o in opts {
            out.append(SettingPicker(id: o.id, label: o.name, current: o.currentValue, isMode: o.isModeOption,
                                     values: o.values.map { .init(value: $0.value, label: $0.name, description: $0.description, warning: false) }))
        }
        return out
    }
}

/// One settings picker (mode, model, effort, …): POST its `id` and the
/// chosen value to …/options.
public struct SettingPicker: Sendable, Hashable, Identifiable {
    public var id: String
    public var label: String
    public var current: String
    public var isMode: Bool
    public var values: [Value]

    public struct Value: Sendable, Hashable, Identifiable {
        public var value: String
        public var label: String
        public var description: String?
        /// An explicit mode (bypass permissions, full access): mark it.
        public var warning: Bool
        public var id: String { value }
    }
}
