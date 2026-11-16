import Foundation
import Observation
import XbinCore
import XbinTerm

// A person's workspace agent in one workspace, as the app's own chrome
// holds it (D233; docs/workspace-agent.md §In the app): xbind's state of
// their delegation, the requests and change sets that wait, the standing
// approvals — read with the device's session, which xbind takes as the
// person's own, and kept current by the `delegate` events of the
// workspace's socket. No polling: an event names a list and that list is
// read again; a reconnect reads all of it.
//
// Every grant goes out from here (approve, deny, apply, takeBack, resume):
// a tile's credential — the agent's own native view included — is refused
// all of them by xbind, and never holds this session (TileSchemeHandler).
// What a sheet shows is always what xbind answers for an id, never
// anything a tile's tree said.

/// What an approval sheet reviews.
enum AgentReview: Hashable, Identifiable {
    case request(String)
    case changeset(String)

    var id: String {
        switch self {
        case .request(let id): return "request:" + id
        case .changeset(let id): return "changeset:" + id
        }
    }
}

/// One of the chrome's own panels.
enum AgentPanel: String, Identifiable {
    case approvals, activity, prefs, setup
    var id: String { rawValue }
}

/// What the agent was opened about: the tile or screen in front.
struct AgentContext: Equatable {
    var tile: String?
    var screen: String?
}

@MainActor
@Observable
final class WorkspaceAgentModel {
    @ObservationIgnored private weak var workspace: WorkspaceModel?

    /// `GET /delegate/state`; nil: not read, or an xbind without delegation.
    private(set) var state: DelegateState?
    private(set) var unreachable = false
    private(set) var llmConfigured: Bool?
    private(set) var llmHosted = false
    /// Pending requests, oldest first.
    private(set) var requests: [DelegateRequest] = []
    private(set) var changesets: [AdminChangeSet] = []
    private(set) var approvals: [StandingApproval] = []
    /// Requests and change sets the conversation's cards show, by id, as
    /// xbind read them last.
    private(set) var known: [String: DelegateRequest] = [:]
    private(set) var knownSets: [String: AdminChangeSet] = [:]
    /// Ids a card asked for that xbind doesn't have (not the person's, or gone).
    private(set) var missing: Set<String> = []
    /// The tile agents the conversation's cards show.
    private(set) var sessionRows: [String: AgentSessionRow] = [:]
    /// The sealed component whose native view is the agent's conversation.
    private(set) var holder: TileInfo?
    private(set) var providerNames: [String: String] = [:]

    /// A read was answered `403 {refusal: "no-agent"}` (docs/protocol/
    /// delegation.md §The switches): until the state is read again — a
    /// `delegate` state event, a refresh — nothing of the agent is asked for.
    private(set) var refusedNoAgent = false
    @ObservationIgnored private var watchedSessions: Set<String> = []
    @ObservationIgnored private var sessionRead: Task<Void, Never>?
    @ObservationIgnored private var loading: Task<Void, Never>?

    init(workspace: WorkspaceModel) {
        self.workspace = workspace
    }

    // MARK: What the chrome shows

    var dock: DelegationWords.Dock {
        // xbind refused a call as "no-agent": whatever the state said, this
        // person has none (nothing is shown, nothing more is read)
        if refusedNoAgent { return .hidden }
        return DelegationWords.dock(state: state, llmConfigured: llmConfigured, admin: workspace?.whoami?.admin ?? false,
                                    unreachable: unreachable && state != nil)
    }

    /// xbind can't be reached now, and there is a conversation loaded to
    /// keep showing (its cards' Review… wait for the connection).
    var offlineWithConversation: Bool { dock == .offline && holder != nil }

    /// What waits for the person because of their agent: its requests, its
    /// proposals, and the tile agents it started that wait for them.
    var count: Int { needs.count + waitingSessions.count }

    var needs: [AgentNeed] { AgentNeed.rows(requests: requests, changesets: changesets) }

    /// Tile agents the agent started that wait for the person.
    var waitingSessions: [TermDirectoryEntry] { (workspace?.sessions ?? []).filter { $0.startedByAgent && $0.needsYou } }

    /// Tile agents it started, live.
    var tileAgents: [TermDirectoryEntry] { (workspace?.sessions ?? []).filter(\.startedByAgent) }

    var chip: DelegationWords.Chip { DelegationWords.chip(dock, count: count) }

    var homeStatus: String {
        DelegationWords.homeStatus(dock, tileAgents: tileAgents.count, working: tileAgents.filter(\.isAgentBusy).count, count: count)
    }

    /// The component that is the person's agent ("" none): the only tile
    /// whose native view may ask the chrome anything (AgentBridge).
    var holderPath: String { state?.agent ?? "" }

    /// Whether `path` is the person's agent (a link to it opens the agent,
    /// never a tile screen).
    func isHolder(_ path: String) -> Bool { !path.isEmpty && path == holderPath }

    func tileName(_ path: String) -> String {
        if let t = workspace?.catalog[path] { return t.title }
        return DelegationWords.lastOf(path)
    }

    func harnessName(_ id: String) -> String { providerNames[id] ?? "" }

    // MARK: Reading

    /// Reads everything again (a first load, a reconnect, "Check again").
    func refresh() async {
        if let t = loading { await t.value; return }
        let t = Task { await self.readAll() }
        loading = t
        await t.value
        loading = nil
    }

    private func readAll() async {
        await readState()
        // No agent for this person (or none on this xbind): nothing is shown,
        // and nothing more is read — not its component, not its lists.
        guard state != nil, dock != .hidden else {
            requests = []; changesets = []; approvals = []; holder = nil
            known = [:]; knownSets = [:]; sessionRows = [:]
            return
        }
        if let j = await get(DelegateRoute.llm) {
            llmConfigured = j["configured"]?.boolValue
            llmHosted = j["host"]?.boolValue ?? false
        }
        await withTaskGroup(of: Void.self) { g in
            g.addTask { await self.readRequests() }
            g.addTask { await self.readChangesets() }
            g.addTask { await self.readApprovals() }
            g.addTask { await self.readHolder() }
        }
        if !watchedSessions.isEmpty { await readSessions() }
    }

    /// 404: an xbind without delegation; 403: none for this caller —
    /// nothing to show either way. A failure to reach xbind keeps what was
    /// read (the chrome says "can't reach").
    func readState() async {
        do {
            let r = try await send(DelegateRoute.state)
            unreachable = false
            refusedNoAgent = false
            state = r.isSuccess ? DelegateState(json: try? r.json()) : nil
        } catch {
            unreachable = true
        }
    }

    private func readRequests() async {
        guard let j = await get(DelegateRoute.requests) else { return }
        requests = DelegateRequest.list(j)
        for r in requests { known[r.id] = r }
        // a card of a request that no longer waits shows what came of it
        for id in known.keys where known[id]?.isPending == true && !requests.contains(where: { $0.id == id }) {
            _ = await request(id)
        }
    }

    private func readChangesets() async {
        guard let j = await get(DelegateRoute.changesets) else { return }
        changesets = AdminChangeSet.list(j)
        for c in changesets { knownSets[c.id] = c }
    }

    func readApprovals() async {
        guard let j = await get(DelegateRoute.approvals) else { return }
        approvals = StandingApproval.list(j)
    }

    /// The agent's own row: sealed rows are listed only when asked for, and
    /// the app asks only here — its catalog, Home and search never see one.
    private func readHolder() async {
        let path = holderPath
        guard !path.isEmpty else { holder = nil; return }
        if holder?.path == path { return }
        guard let j = await get(DelegateRoute.sealedComponents) else { return }
        holder = Catalog(json: j)[path]
        if providerNames.isEmpty, let p = await get(DelegateRoute.providers) {
            for row in p.arrayValue ?? [] {
                if let id = row["id"]?.stringValue, let name = row["name"]?.stringValue { providerNames[id] = name }
            }
        }
    }

    /// One request as xbind reads it now — the only thing a sheet or a card
    /// is drawn from. nil: not the person's, or gone.
    @discardableResult
    func request(_ id: String) async -> DelegateRequest? {
        guard let r = try? await send(DelegateRoute.request(id)) else { return known[id] }
        guard r.isSuccess, let req = DelegateRequest(json: try? r.json()) else {
            if r.status == 404 || r.status == 403 { missing.insert(id); known[id] = nil }
            return nil
        }
        missing.remove(id)
        known[id] = req
        return req
    }

    @discardableResult
    func changeset(_ id: String) async -> AdminChangeSet? {
        guard let r = try? await send(DelegateRoute.changeset(id)) else { return knownSets[id] }
        guard r.isSuccess, let set = AdminChangeSet(json: try? r.json()) else {
            if r.status == 404 || r.status == 403 { missing.insert(id); knownSets[id] = nil }
            return nil
        }
        missing.remove(id)
        knownSets[id] = set
        return set
    }

    /// A `session` card shows `id`: its row is read (and again when a
    /// `term` event says a session changed).
    func watchSession(_ id: String) {
        guard watchedSessions.insert(id).inserted else { return }
        sessionsChanged()
    }

    /// A session of the person's changed: the cards' rows are read again,
    /// once for a burst.
    func sessionsChanged() {
        guard !watchedSessions.isEmpty, sessionRead == nil else { return }
        sessionRead = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 200_000_000)
            await self?.readSessions()
            self?.sessionRead = nil
        }
    }

    private func readSessions() async {
        guard let j = await get(DelegateRoute.agentSessions(ids: watchedSessions.sorted())) else { return }
        var rows: [String: AgentSessionRow] = [:]
        for row in AgentSessionRow.list(j) { rows[row.id] = row }
        sessionRows = rows
    }

    /// A `delegate` event: the count rides on the state, so every op reads
    /// it again, then the list it names.
    func handle(_ e: DelegateEvent) {
        Task {
            await readState()
            // (a person without an agent: the state is all that is read)
            guard dock != .hidden else {
                if !requests.isEmpty || !changesets.isEmpty || holder != nil { await readAll() }
                return
            }
            switch e.op {
            case "request":
                await readRequests()
                if !e.id.isEmpty, known[e.id] != nil { await request(e.id) }
            case "approval": await readApprovals()
            case "changeset":
                await readChangesets()
                if !e.id.isEmpty, knownSets[e.id] != nil { await changeset(e.id) }
            case "state":
                if holder == nil { await readHolder() }
            default: break
            }
        }
    }

    // MARK: What only the person's own session may send

    func approve(_ r: DelegateRequest, choice: GrantChoice.ID, duration: String) async -> DelegateOutcome {
        let out = await decide(DelegateRoute.approve(r.id, DelegationWords.approveBody(r, choice: choice, duration: duration)))
        await afterDecision(r.id, standing: choice != .once)
        return out
    }

    func deny(_ r: DelegateRequest) async -> DelegateOutcome {
        let out = await decide(DelegateRoute.deny(r.id))
        await afterDecision(r.id, standing: false)
        return out
    }

    private func afterDecision(_ id: String, standing: Bool) async {
        await readState()
        await readRequests()
        await request(id)
        if standing { await readApprovals() }
    }

    func takeBack(_ a: StandingApproval) async -> String? {
        let out = await decide(DelegateRoute.takeBack(a.id))
        await readApprovals()
        await readState()
        if case .failed(let why) = out { return why }
        return nil
    }

    /// Approves the ticked items of a change set; xbind asks for a recent
    /// sign-in first when it has none (`.stepUp`).
    func apply(_ set: AdminChangeSet, items: [Int], password: String = "") async -> DelegateOutcome {
        let out = await decide(DelegateRoute.apply(set.id, items: items, password: password), sentPassword: !password.isEmpty)
        await readState()
        await readChangesets()
        return out
    }

    func reject(_ set: AdminChangeSet) async -> DelegateOutcome {
        let out = await decide(DelegateRoute.reject(set.id))
        await readState()
        await readChangesets()
        return out
    }

    /// A NEW change set that undoes what was applied — to review like any
    /// other; nothing is undone by this.
    func undo(_ set: AdminChangeSet) async -> DelegateOutcome {
        let out = await decide(DelegateRoute.undo(set.id))
        await readState()
        await readChangesets()
        return out
    }

    /// Pause at once (the safe direction), or resume — the person's own tap.
    func setPaused(_ paused: Bool) async -> String? {
        let out = await decide(DelegateRoute.setPaused(paused))
        if case .done(let j) = out, let s = DelegateState(json: j) { state = s } else { await readState() }
        if case .failed(let why) = out { return why }
        if case .gone(let why) = out { return why }
        return nil
    }

    func setAdminChanges(_ on: Bool) async -> String? {
        let out = await decide(DelegateRoute.setAdminChanges(on))
        if case .done(let j) = out, let s = DelegateState(json: j) { state = s } else { await readState() }
        if case .failed(let why) = out { return why }
        return nil
    }

    /// One of the web and MCP settings (D236; ``NetPref/id``). xbind refuses
    /// one its workspace policy has off (409, in its words).
    func setNet(_ id: String, _ on: Bool) async -> String? {
        guard let req = DelegateRoute.setNet(id, on) else { return nil }
        let out = await decide(req)
        if case .done(let j) = out, let s = DelegateState(json: j) { state = s } else { await readState() }
        switch out {
        case .done: return nil
        case .failed(let why), .gone(let why), .stopped(_, let why): return why
        case .stepUp, .unseen: return "That couldn't be saved."
        }
    }

    func audit(before: Date? = nil) async -> [AuditRow]? {
        guard let j = await get(DelegateRoute.audit(before: before)) else { return nil }
        return AuditRow.list(j)
    }

    func prefs() async -> JSONValue? { await get(DelegateRoute.prefs) }
    func providers() async -> JSONValue? { await get(DelegateRoute.providers) }

    func setPrefs(_ patch: JSONValue) async -> JSONValue? {
        guard let r = try? await send(DelegateRoute.setPrefs(patch)), r.isSuccess else { return nil }
        return try? r.json()
    }

    private func decide(_ req: APIRequest, sentPassword: Bool = false) async -> DelegateOutcome {
        do {
            return DelegateOutcome.read(try await send(req), sentPassword: sentPassword)
        } catch {
            unreachable = true
            return .failed("xbin can't be reached")
        }
    }

    private func get(_ req: APIRequest) async -> JSONValue? {
        guard let r = try? await send(req) else { return nil }
        guard r.isSuccess else {
            if r.status == 403, (try? r.json())?["refusal"]?.stringValue == "no-agent" { refusedNoAgent = true }
            return nil
        }
        return try? r.json()
    }

    /// One request of the chrome's own, with the device's session — or, in a
    /// Debug build's `-XbinAgentDemo`, answered from canned xbind JSON with
    /// no network at all (AgentDemo.swift).
    private func send(_ req: APIRequest) async throws -> APIResponse {
        #if DEBUG
        if let demo = AgentDemo.shared, let canned = try demo.answer(req) { return canned }
        #endif
        guard let auth = workspace?.auth else { throw URLError(.userAuthenticationRequired) }
        return try await auth.send(req)
    }
}
