import Foundation
import Observation
import WebKit
import XbinAgent
import XbinCore
import XbinTerm

/// What a workspace shows full screen (plans/native.md §15: one surface;
/// lists are overlays).
enum Surface: Hashable, Codable {
    case tile(String, sub: String = "", fragment: String? = nil)
    case terminal(cwd: String, session: String?)
    case agent(cwd: String?, session: String?)

    var title: String {
        switch self {
        case .tile(let t, _, _): return TileInfo.humanize(t)
        case .terminal(let cwd, _): return "Terminal · \(TileInfo.humanize(cwd))"
        case .agent(let cwd, _): return "Agent" + (cwd.map { " · \(TileInfo.humanize($0))" } ?? "")
        }
    }
}

/// A screen pushed over a tile: an `xbin.window` (another sub-path of the
/// tile, or another tile), opened by the tile's request `replyID`.
struct PushedWindow: Hashable {
    var fromTile: String
    var target: String
    var title: String
    var replyID: String
}

/// One workspace: its session, catalog, navigation and web storage.
@MainActor
@Observable
final class WorkspaceModel: Identifiable {
    let id: String
    private(set) var record: WorkspaceRecord
    let auth: WorkspaceAuth
    let frameTokens: FrameTokenCache
    let transport: AppTransport

    var whoami: Whoami?
    var catalog = Catalog(tiles: [])
    var layout = PersonalLayout()
    var shared = SharedScreens()
    var navigator: NavigatorModel?
    var sessions: [TermDirectoryEntry] = []
    var loading = false
    var lastError: String?
    /// Signing in failed in a way the user must act on (re-enroll, SSO, …).
    var signInProblem: SignInError?
    var lastActivity: Date?

    /// The full-screen surface (nil: the navigator is the home).
    var surface: Surface?
    /// Windows pushed over the current tile.
    var windows: [PushedWindow] = []
    /// The navigator overlay is up.
    var showNavigator = false

    @ObservationIgnored private var dataStoreCache: WKWebsiteDataStore?
    @ObservationIgnored private(set) lazy var schemeHandler = TileSchemeHandler(workspace: self)
    @ObservationIgnored private var observer: UUID?

    init(record: WorkspaceRecord, transport: AppTransport = .shared, keys: any DeviceKeyStore = EnclaveKeyStore(),
         sessions: any SessionStore = KeychainSessionStore()) {
        id = record.id
        self.record = record
        self.transport = transport
        let auth = WorkspaceAuth(record: record, transport: transport, keys: keys, sessions: sessions,
                                 clientHeader: AppInfo.clientHeader)
        self.auth = auth
        frameTokens = FrameTokenCache { component in
            let j = try await auth.json(APIRequest("GET", FrameTokenRoute.path(component: component)))
            guard let t = FrameTokenRoute.token(from: j) else { throw APIError(status: 500, message: "no frame token") }
            return t
        }
    }

    var title: String { record.displayTitle }
    var origin: ServerOrigin { record.server }
    var userLabel: String { record.user.name ?? record.user.id }
    var canResign: Bool { record.deviceId != nil }

    /// Per-workspace web storage (§4): tiles of different workspaces never
    /// share cookies, local storage or caches.
    var dataStore: WKWebsiteDataStore {
        if let d = dataStoreCache { return d }
        let d: WKWebsiteDataStore
        if let uuid = UUID(uuidString: id) { d = WKWebsiteDataStore(forIdentifier: uuid) } else { d = .nonPersistent() }
        dataStoreCache = d
        return d
    }

    func update(record r: WorkspaceRecord) {
        record = r
        Task { await auth.update(record: r) }
    }

    var agents: AgentClient { AgentClient(transport: AgentSessionTransport(auth: auth, transport: transport)) }

    // MARK: Loading

    /// Signs in if needed, then loads whoami, the catalog, screens, the
    /// user's layout and the branding. Errors land in `lastError` /
    /// `signInProblem`; what loaded stays.
    func refresh() async {
        loading = true
        defer { loading = false }
        do {
            let who = Whoami(json: try await auth.json(APIRequest("GET", "/api/xbin/whoami")))
            whoami = who
            signInProblem = nil
            lastError = nil
            if !who.userID.isEmpty, who.userID != record.user.id || (who.name != record.user.name && !who.name.isEmpty) {
                var r = await auth.record
                r.user = WorkspaceUser(id: who.userID, name: who.name.isEmpty ? r.user.name : who.name)
                update(record: r)
            }
            async let comps = auth.json(APIRequest("GET", "/api/xbin/components"))
            async let scr = auth.json(APIRequest("GET", "/api/xbin/screens"))
            catalog = Catalog(json: try await comps)
            shared = SharedScreens(json: try? await scr)
            layout = PersonalLayout(json: await loadLayout())
            navigator = NavigatorModel(catalog: catalog, layout: layout, shared: shared, user: who.userID)
            await refreshBranding()
            await refreshSessions()
            lastActivity = Date()
        } catch let e as SignInError {
            signInProblem = e
            lastError = e.description
        } catch {
            lastError = describe(error)
        }
    }

    /// The shell's `layout` pref lives in the shell's bucket
    /// (per user × component): read it with a frame token for `shell`.
    private func loadLayout() async -> JSONValue? {
        guard let t = try? await frameTokens.token(for: "shell") else { return nil }
        let r = try? await transport.send(APIRequest("GET", "/api/xbin/prefs/layout",
                                                     headers: [TileScheme.frameTokenHeader: t,
                                                               TileScheme.clientHeader: AppInfo.clientHeader]), to: origin)
        guard let r, r.status == 200 else { return nil }
        return try? r.json()
    }

    func refreshBranding() async {
        guard let j = try? await auth.json(APIRequest("GET", "/api/xbin/branding")) else { return }
        var r = record
        r.branding = BrandingCache(response: j, fetchedAt: Date())
        if r.branding != record.branding { update(record: r) }
    }

    func refreshSessions() async {
        guard let r = try? await auth.call(APIRequest("GET", TermDirectory.listPath())) else { return }
        sessions = TermDirectory.decode(r.body)
    }

    /// Agent sessions waiting on this user (the Needs-you inbox).
    var needsYou: [TermDirectoryEntry] { sessions.filter(\.needsYou) }

    /// Signs in now (the user tapped "sign in"), surfacing the problem.
    func signIn() async {
        do {
            _ = try await auth.signIn()
            signInProblem = nil
            await refresh()
        } catch let e as SignInError {
            signInProblem = e
        } catch {
            lastError = describe(error)
        }
    }

    // MARK: Navigation

    func open(_ s: Surface) {
        windows = []
        surface = s
        showNavigator = false
        lastActivity = Date()
    }

    func open(link: DeepLink) {
        switch link {
        case .tile(_, let tile, let fragment): open(.tile(tile, fragment: fragment))
        case .terminal(_, let session): open(.terminal(cwd: sessions.first { $0.id == session }?.cwd ?? "", session: session))
        case .agent(_, let session): open(.agent(cwd: nil, session: session))
        default: showNavigator = surface != nil
        }
    }

    func tile(_ path: String) -> TileInfo? { catalog[path] }

    func surfaceKind(for tile: TileInfo) -> TileSurface {
        TileSurface.pick(tile, serverRuntime: whoami?.nativeRuntime, forceWeb: AppSettings.forcesWeb(id, tile.path),
                         runtimeOff: AppSettings.nativeRuntimeOff)
    }

    // MARK: Safari hand-off

    /// Opens a path of this workspace in Safari (chrome tiles, "open in
    /// Safari"). The browser has its own session: the user signs in there
    /// (a one-shot ticket hand-off, D64-style, needs a server route — see
    /// the WP notes).
    func safariURL(path: String) -> URL? { origin.url(path: path) }

    func describe(_ error: any Error) -> String {
        if let e = error as? APIError { return e.description }
        if let e = error as? SignInError { return e.description }
        if let e = error as? URLError { return e.localizedDescription }
        return String(describing: error)
    }

    // MARK: Teardown

    /// Forgets everything local about this workspace (the server keeps the
    /// device until it is removed there, or removes it now if reachable).
    func forget(removeDevice: Bool) async {
        if removeDevice, let dev = record.deviceId {
            _ = try? await auth.send(APIRequest("DELETE", "\(AppAuthRoute.devices)/\(URLComponent.encode(dev))"))
        }
        await auth.signOut()
        await EnclaveKeyStore().deleteKey(workspace: id)
        if let uuid = UUID(uuidString: id) {
            dataStoreCache = nil
            try? await WKWebsiteDataStore.remove(forIdentifier: uuid)
        }
        NativeStateFile.removeAll(workspace: id)
    }
}
