import Foundation

// The session directory (D73; docs/protocol.md `GET /api/xbin/term/sessions`):
// the caller's live sessions, shells and agents, oldest first. The browser
// keeps no session ids of its own and neither does the app — the terminal's
// sessions sheet and the Needs-you inbox read this list.

/// One live session of the caller.
public struct TermDirectoryEntry: Equatable, Sendable, Identifiable {
    public enum Kind: String, Sendable { case shell, agent }

    public var id: String
    /// The tile (component path) the session runs on.
    public var cwd: String
    public var net: String
    public var label: String
    public var scopes: [TermScope]
    public var gpu: String
    public var api: Bool
    /// The user's name for the tab ("" = none).
    public var name: String
    public var created: Date?
    public var lastActive: Date?
    /// Sockets attached right now (another device, another tab).
    public var clients: Int
    public var envHeld: Bool
    public var kind: Kind
    public var vm: Bool
    /// Agent sessions: provider id, mode, model, status
    /// (`starting | idle | running | waiting_permission | error | exited`)
    /// and unanswered permission requests.
    public var provider: String
    public var mode: String
    public var model: String
    public var status: String
    public var pending: Int

    public init(id: String, cwd: String, net: String = "", label: String = "", scopes: [TermScope] = [], gpu: String = "none",
                api: Bool = true, name: String = "", created: Date? = nil, lastActive: Date? = nil, clients: Int = 0,
                envHeld: Bool = false, kind: Kind = .shell, vm: Bool = false, provider: String = "", mode: String = "",
                model: String = "", status: String = "", pending: Int = 0) {
        self.id = id; self.cwd = cwd; self.net = net; self.label = label; self.scopes = scopes; self.gpu = gpu
        self.api = api; self.name = name; self.created = created; self.lastActive = lastActive; self.clients = clients
        self.envHeld = envHeld; self.kind = kind; self.vm = vm; self.provider = provider; self.mode = mode
        self.model = model; self.status = status; self.pending = pending
    }

    /// What the sheet shows: the user's name, else "shell"/the provider,
    /// with the network label.
    public var title: String {
        if !name.isEmpty { return name }
        return kind == .agent ? (provider.isEmpty ? "agent" : provider) : "shell"
    }

    /// An agent session waiting on the user (a permission request or a
    /// question).
    public var needsYou: Bool { kind == .agent && (pending > 0 || status == "waiting_permission") }
    public var isAgentBusy: Bool { kind == .agent && (status == "running" || status == "starting") }
}

public enum TermDirectory {
    /// `GET` → the list (`?cwd=` one tile).
    public static func listPath(cwd: String? = nil) -> String {
        guard let cwd, !cwd.isEmpty else { return "/api/xbin/term/sessions" }
        return "/api/xbin/term/sessions?cwd=\(encodeComponent(cwd))"
    }

    /// `PATCH {name}` renames (empty clears); `DELETE` ends the session.
    public static func sessionPath(_ id: String) -> String { "/api/xbin/term/sessions/\(encodeComponent(id))" }

    /// The body of a rename.
    public static func renameBody(_ name: String) -> Data {
        let clean = String(name.trimmingCharacters(in: .whitespacesAndNewlines).prefix(80))
        return (try? JSONEncoder().encode(["name": clean])) ?? Data("{\"name\":\"\"}".utf8)
    }

    /// Decodes the list; rows without an id are skipped, unknown fields ignored.
    public static func decode(_ data: Data) -> [TermDirectoryEntry] {
        guard let rows = try? JSONDecoder().decode([Lenient].self, from: data) else { return [] }
        return rows.compactMap(\.entry)
    }

    /// One row, every field optional and type-tolerant.
    struct Lenient: Decodable {
        var entry: TermDirectoryEntry?

        enum K: String, CodingKey {
            case id, cwd, net, label, scopes, gpu, api, name, created, lastActive, clients, envHeld, kind, vm
            case provider, mode, model, status, pending
        }

        init(from decoder: any Decoder) throws {
            let c = try decoder.container(keyedBy: K.self)
            func s(_ k: K) -> String { ((try? c.decodeIfPresent(String.self, forKey: k)) ?? nil) ?? "" }
            func b(_ k: K) -> Bool? { (try? c.decodeIfPresent(Bool.self, forKey: k)) ?? nil }
            func i(_ k: K) -> Int { ((try? c.decodeIfPresent(Int.self, forKey: k)) ?? nil) ?? 0 }
            let id = s(.id)
            guard !id.isEmpty else { entry = nil; return }
            let scopes = ((try? c.decodeIfPresent([TermScope?].self, forKey: .scopes)) ?? nil)?.compactMap { $0 } ?? []
            entry = TermDirectoryEntry(
                id: id, cwd: s(.cwd), net: s(.net), label: s(.label), scopes: scopes,
                gpu: s(.gpu).isEmpty ? "none" : s(.gpu), api: b(.api) ?? true, name: s(.name),
                created: TermDirectory.date(s(.created)), lastActive: TermDirectory.date(s(.lastActive)),
                clients: i(.clients), envHeld: b(.envHeld) ?? false,
                kind: TermDirectoryEntry.Kind(rawValue: s(.kind)) ?? .shell, vm: b(.vm) ?? false,
                provider: s(.provider), mode: s(.mode), model: s(.model), status: s(.status), pending: i(.pending))
        }
    }

    static func date(_ s: String) -> Date? {
        guard !s.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: s)
    }

    /// The shell sessions of one tile, most recently active first (the
    /// sheet's order), then agents.
    public static func forTile(_ entries: [TermDirectoryEntry], cwd: String) -> [TermDirectoryEntry] {
        entries.filter { $0.cwd == cwd }.sorted { a, b in
            if a.kind != b.kind { return a.kind == .shell }
            return (a.lastActive ?? .distantPast) > (b.lastActive ?? .distantPast)
        }
    }

    static func encodeComponent(_ s: String) -> String {
        var allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
        allowed.insert(charactersIn: "-_.!~*'()")
        return s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s
    }
}

/// The network picker's choices on a tile (D54/D65): what the session frame
/// says this caller may pick, the current one marked. A change restarts the
/// session (`TermSession.restart`), so the sheet asks first, as the web does.
public struct TermNetChoice: Equatable, Sendable, Identifiable {
    public var id: String
    public var label: String
    public var desc: String
    public var current: Bool

    public static func choices(_ info: TermSessionInfo?) -> [TermNetChoice] {
        guard let info else { return [] }
        return info.scopes.map { TermNetChoice(id: $0.id, label: $0.label, desc: $0.desc, current: $0.id == info.net) }
    }
}

/// The VM toggle (D89/D90): offered when the environment says a VM can run;
/// its reason otherwise. `net=host` can't run in a VM.
public struct TermVMChoice: Equatable, Sendable {
    public var available: Bool
    public var on: Bool
    /// Why it's unavailable, or the emulation note.
    public var note: String

    public init(env: TermEnvState?, session: TermSessionInfo?) {
        let vm = env?.vm
        available = vm?.available ?? false
        on = session?.vm ?? false
        if let vm, !vm.available { note = vm.reason.isEmpty ? "No VM sandbox on this server." : vm.reason }
        else if let vm, vm.emulated { note = vm.note.isEmpty ? "Emulated (no KVM): slower." : vm.note }
        else { note = "" }
        if session?.net == "host" { available = false; if note.isEmpty { note = "A VM can't use the host network." } }
    }
}
