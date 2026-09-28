import Foundation

// A tile's sessions screen in the app (D132): the web terminal window's
// counterpart — its tabs (the tile's shells and agents together, as
// web/frame-titlebar.js shows them) and its launcher (web/frame-launcher.js
// `launcher`: a box for Bash and one per agent provider, the VM switch, the
// tile's recent agent sessions with Resume). Pure: the app feeds it the
// session directory (D73), the providers, the history and the terminal
// environment, and draws what comes out.

/// One tab of a tile's sessions screen.
public struct SessionTab: Equatable, Sendable, Identifiable {
    public enum Kind: String, Sendable { case shell, agent }

    /// Stable for the tab's life (a new shell has no session id yet).
    public var key: String
    public var id: String { key }
    public var kind: Kind
    /// The session; nil while a new shell's socket opens it.
    public var session: String?
    /// The user's name for it (the directory's `name`; "" = none).
    public var name: String
    /// An agent's provider id.
    public var provider: String
    public var vm: Bool
    /// Its session is gone (it ended here or elsewhere): the tab stays —
    /// an agent's transcript, a shell's last screen — until closed.
    public var ended: Bool
    /// The directory has listed its session at least once: only then does
    /// a listing without it mean it ended (a listing can race a new one).
    public var listed: Bool

    public init(key: String, kind: Kind, session: String? = nil, name: String = "", provider: String = "", vm: Bool = false,
                ended: Bool = false, listed: Bool = false) {
        self.key = key; self.kind = kind; self.session = session; self.name = name; self.provider = provider
        self.vm = vm; self.ended = ended; self.listed = listed
    }

    /// The tab's label (frame-titlebar.js `tabLabel`): its name, else Bash
    /// for a shell, the provider's name for an agent.
    public func label(providerName: (String) -> String? = { _ in nil }) -> String {
        if !name.isEmpty { return name }
        if kind == .shell { return "Bash" }
        if !provider.isEmpty { return providerName(provider) ?? provider }
        return "Agent"
    }
}

/// A tile's tabs, kept in step with the session directory: every live
/// session on the tile is a tab, oldest first, unless the user closed its
/// tab here (it keeps running; the launcher lists it under Running); a
/// session that goes away leaves its tab ended; a new shell's tab waits
/// for its session id.
public struct SessionTabs: Equatable, Sendable {
    public private(set) var tabs: [SessionTab] = []
    /// Live sessions whose tab the user closed on this screen.
    public private(set) var closed: Set<String> = []
    private var serial = 0

    public init() {}

    public var isEmpty: Bool { tabs.isEmpty }

    public func tab(_ key: String) -> SessionTab? { tabs.first { $0.key == key } }

    public func tab(session: String) -> SessionTab? { tabs.first { $0.session == session } }

    private mutating func nextKey() -> String {
        serial += 1
        return "t\(serial)"
    }

    /// Takes a listing of the caller's sessions (the directory, any tile):
    /// this tile's rows update their tabs, new ones get a tab at the end,
    /// and a listed tab whose session is missing ends.
    public mutating func sync(_ entries: [TermDirectoryEntry], cwd: String) {
        let rows = entries.filter { $0.cwd == cwd }
        let ids = Set(rows.map(\.id))
        closed.formIntersection(ids)
        for i in tabs.indices {
            guard let s = tabs[i].session else { continue }
            if let r = rows.first(where: { $0.id == s }) {
                tabs[i].name = r.name
                tabs[i].vm = r.vm
                if r.kind == .agent, !r.provider.isEmpty { tabs[i].provider = r.provider }
                tabs[i].listed = true
                tabs[i].ended = false
            } else if tabs[i].listed {
                tabs[i].ended = true
            }
        }
        for r in rows where tab(session: r.id) == nil && !closed.contains(r.id) {
            tabs.append(SessionTab(key: nextKey(), kind: r.kind == .agent ? .agent : .shell, session: r.id, name: r.name,
                                   provider: r.provider, vm: r.vm, listed: true))
        }
    }

    /// A tab for a new shell (its socket makes the session): its key.
    public mutating func newShell(vm: Bool) -> String {
        let key = nextKey()
        tabs.append(SessionTab(key: key, kind: .shell, vm: vm))
        return key
    }

    /// A tab for an existing session (an agent just created, a session
    /// opened from a list): its key — the tab it already has, if any.
    @discardableResult
    public mutating func open(session: String, kind: SessionTab.Kind, provider: String = "", name: String = "") -> String {
        closed.remove(session)
        if let t = tab(session: session) { return t.key }
        let key = nextKey()
        tabs.append(SessionTab(key: key, kind: kind, session: session, name: name, provider: provider))
        return key
    }

    /// A new shell's tab learned its session id (the session frame); a tab
    /// a listing made for it meanwhile goes.
    public mutating func bind(_ key: String, session: String) {
        guard let i = tabs.firstIndex(where: { $0.key == key }), tabs[i].session != session else { return }
        tabs.removeAll { $0.key != key && $0.session == session }
        guard let j = tabs.firstIndex(where: { $0.key == key }) else { return }
        tabs[j].session = session
        tabs[j].ended = false
    }

    /// The tab's session ended (here: the user ended it).
    public mutating func end(_ key: String) {
        guard let i = tabs.firstIndex(where: { $0.key == key }) else { return }
        tabs[i].ended = true
    }

    /// Closes a tab on this screen; a live session keeps running (Running
    /// in the launcher). The key of the tab to show next (its neighbour),
    /// or nil when none is left.
    @discardableResult
    public mutating func close(_ key: String) -> String? {
        guard let i = tabs.firstIndex(where: { $0.key == key }) else { return nil }
        let t = tabs.remove(at: i)
        if let s = t.session, !t.ended { closed.insert(s) }
        guard !tabs.isEmpty else { return nil }
        return tabs[min(i, tabs.count - 1)].key
    }

    /// The tab after (step 1) or before (-1) `key`, wrapping around.
    public func neighbour(of key: String, step: Int) -> String? {
        guard let i = tabs.firstIndex(where: { $0.key == key }), tabs.count > 1 else { return nil }
        return tabs[(i + step % tabs.count + tabs.count) % tabs.count].key
    }

    /// The tile's live sessions without a tab here (closed on this screen).
    public func running(_ entries: [TermDirectoryEntry], cwd: String) -> [TermDirectoryEntry] {
        entries.filter { $0.cwd == cwd && closed.contains($0.id) }
    }
}

/// The launcher of a tile's sessions screen (web/frame-launcher.js
/// `launcher`): shown when the tile has no session, and on `+`.
public struct TileLauncher: Equatable, Sendable {
    /// A session kind to start: Bash, or an agent provider.
    public struct Box: Equatable, Sendable, Identifiable {
        public var kind: SessionTab.Kind
        /// The provider's id (agents).
        public var provider: String
        public var title: String
        public var subtitle: String
        public var id: String { kind == .shell ? "shell" : "agent:" + provider }
    }

    /// A past agent session of the tile (`GET /agent/history?cwd=`).
    public struct Past: Equatable, Sendable {
        public var id: String
        public var provider: String
        public var name: String
        public var preview: String
        public var turns: Int
        /// RFC 3339.
        public var ended: String
        public var loadable: Bool

        public init(id: String, provider: String, name: String = "", preview: String = "", turns: Int = 0, ended: String = "",
                    loadable: Bool = false) {
            self.id = id; self.provider = provider; self.name = name; self.preview = preview; self.turns = turns
            self.ended = ended; self.loadable = loadable
        }
    }

    /// A recent session's row: Resume when the agent can reopen it.
    public struct Recent: Equatable, Sendable, Identifiable {
        public var id: String
        public var provider: String
        public var title: String
        public var subtitle: String
        public var resumable: Bool
    }

    /// The VM switch, where VMs can run.
    public struct VMSwitch: Equatable, Sendable {
        public var on: Bool
        public var title: String
        public var subtitle: String
    }

    public var heading: String
    public var boxes: [Box]
    /// No provider known yet (the list is loading, or the server has none).
    public var loadingAgents: Bool
    public var vm: VMSwitch?
    public var recent: [Recent]

    /// `target`: what a new session calls, appended to each box's line
    /// (deploy-state.js `launcher().subtitle`, "· target: dev"; "" when
    /// the tile has no deployments).
    public init(tile: String, providers: [(id: String, name: String)], history: [Past], vmStatus: TermVMStatus?,
                vmPref: Bool, target: String = "", now: Date = Date()) {
        let vm = Self.wantVM(pref: vmPref, status: vmStatus)
        let tail = target.isEmpty ? "" : " " + target
        heading = "Start a session in \(tile)"
        var boxes = [Box(kind: .shell, provider: "", title: "Bash",
                         subtitle: (vm ? "a shell in a VM sandbox" : "a shell in the sandbox") + tail)]
        for p in providers {
            boxes.append(Box(kind: .agent, provider: p.id, title: p.name,
                             subtitle: (vm ? "coding agent, in a VM" : "coding agent") + tail))
        }
        self.boxes = boxes
        loadingAgents = providers.isEmpty
        if let st = vmStatus, st.available {
            let size = st.memMiB > 0 ? " · \(st.memMiB) MiB, \(st.vcpus) vCPU" : ""
            self.vm = VMSwitch(on: vm, title: "VM sandbox: \(vm ? "on" : "off")",
                               subtitle: "root in its own kernel\(size)\(st.emulated ? " · emulated — several times slower" : "")")
        } else {
            self.vm = nil
        }
        let name = { (id: String) in providers.first { $0.id == id }?.name ?? id }
        recent = history.prefix(8).map { r in
            let title = !r.name.isEmpty ? r.name : !r.preview.isEmpty ? r.preview : "\(r.provider) session"
            let when = Self.ago(r.ended, now: now)
            return Recent(id: r.id, provider: r.provider, title: title,
                          subtitle: "\(name(r.provider)) · \(r.turns) turn\(r.turns == 1 ? "" : "s")\(when.isEmpty ? "" : " · " + when)",
                          resumable: r.loadable)
        }
    }

    /// Whether new sessions on the tile start in a VM sandbox: the user's
    /// remembered choice, while VMs can run here at all (`wantVM`).
    public static func wantVM(pref: Bool, status: TermVMStatus?) -> Bool { pref && (status?.available ?? false) }

    /// The per-user pref holding that choice, shared with the web
    /// (term-sessions.js `vmPrefKey`): `termvm:apps:crm`.
    public static func vmPrefKey(_ cwd: String) -> String { "termvm:" + cwd.replacingOccurrences(of: "/", with: ":") }

    /// A short relative time for a listing (frame-launcher.js `ago`).
    public static func ago(_ iso: String, now: Date) -> String {
        guard let t = TermDirectory.date(iso) else { return "" }
        let s = max(0, now.timeIntervalSince(t))
        if s < 60 { return "now" }
        if s < 3600 { return "\(Int(s / 60))m ago" }
        if s < 86400 { return "\(Int(s / 3600))h ago" }
        return "\(Int(s / 86400))d ago"
    }
}
