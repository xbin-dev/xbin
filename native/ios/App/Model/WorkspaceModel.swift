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

    /// What native tiles say about themselves (icon, badge): the navigator
    /// and the switcher show it.
    let tileMeta: TileMetaStore

    @ObservationIgnored private var dataStoreCache: WKWebsiteDataStore?
    @ObservationIgnored private(set) lazy var schemeHandler = TileSchemeHandler(workspace: self)
    @ObservationIgnored private var observer: UUID?
    /// The app's `/ws/events` socket for this workspace (live reload, agent
    /// sessions, the Needs-you inbox); AppModel opens it while a foreground
    /// window shows the workspace.
    @ObservationIgnored private(set) lazy var events: WorkspaceEvents = makeEvents()
    @ObservationIgnored private var relist: Task<Void, Never>?
    /// Navigation when no window exists yet (never shown).
    @ObservationIgnored private lazy var detachedNav = WorkspaceNav(workspaceID: id)

    init(record: WorkspaceRecord, transport: AppTransport = .shared, keys: any DeviceKeyStore = EnclaveKeyStore(),
         sessions: any SessionStore = KeychainSessionStore()) {
        id = record.id
        self.record = record
        self.transport = transport
        let auth = WorkspaceAuth(record: record, transport: transport, keys: keys, sessions: sessions,
                                 clientHeader: AppInfo.clientHeader)
        self.auth = auth
        tileMeta = TileMetaStore(workspace: record.id)
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
        AppModel.shared.save()
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
    private func loadLayout() async -> XbinCore.JSONValue? {
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

    // MARK: Live events

    private func makeEvents() -> WorkspaceEvents {
        let client = AppInfo.clientHeader
        let e = WorkspaceEvents(auth: auth, makeSocket: { url, bearer, signal in
            URLSessionEventSocket(url: url, bearer: bearer, clientHeader: client, signal: signal)
        })
        e.userID = { [weak self] in self?.whoami?.userID ?? self?.record.user.id ?? "" }
        e.onTerm = { [weak self] t in self?.apply(t) }
        e.onBranding = { [weak self] in Task { await self?.refreshBranding() } }
        e.onResync = { [weak self] in self?.scheduleRelist() }
        return e
    }

    /// A `term` event: a status summary updates its row in place (the
    /// Needs-you inbox follows it live), anything else re-lists.
    func apply(_ t: TermEvent) {
        guard t.op == .status,
              let updated = TermDirectory.apply(statusOf: t.id, status: t.status, pending: t.pending, questions: t.questions,
                                                to: sessions) else {
            scheduleRelist()
            return
        }
        let before = sessions.first { $0.id == t.id }
        let after = before?.applying(status: t.status, pending: t.pending, questions: t.questions)
        sessions = updated
        // A tap for the person watching that session.
        guard AppModel.shared.isShowingAgent(workspace: id, session: t.id) else { return }
        switch TermDirectory.change(from: before, to: after) {
        case .settled?: Haptics.settle()
        case .needsYou?: Haptics.needsYou()
        case .failed?: Haptics.failed()
        case nil: break
        }
    }

    /// Re-reads the session directory once for a burst of changes.
    func scheduleRelist() {
        guard relist == nil else { return }
        relist = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 250_000_000)
            await self?.refreshSessions()
            self?.relist = nil
        }
    }

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

    /// Opens `s` in a window's navigation.
    func open(_ s: Surface, in nav: WorkspaceNav) {
        nav.open(s)
        lastActivity = Date()
    }

    func open(link: DeepLink, in nav: WorkspaceNav) {
        switch link {
        case .tile(_, let tile, let fragment): open(.tile(tile, fragment: fragment), in: nav)
        case .terminal(_, let session):
            open(.terminal(cwd: sessions.first { $0.id == session }?.cwd ?? "", session: session), in: nav)
        case .agent(_, let session): open(.agent(cwd: nil, session: session), in: nav)
        default: nav.showNavigator = nav.surface != nil
        }
    }

    /// The navigation code outside a window acts on: the focused window's
    /// when it shows this workspace, else a window's that shows it (see
    /// AppModel.nav(for:)). Inside a workspace's view hierarchy prefer the
    /// window's own: `@Environment(WorkspaceNav.self)`.
    var nav: WorkspaceNav { AppModel.shared.nav(for: self) ?? detachedNav }

    /// The focused window's surface here (code written for one window).
    var surface: Surface? {
        get { nav.surface }
        set { nav.surface = newValue }
    }

    /// Windows pushed over the focused window's tile (`xbin.window`).
    var windows: [PushedWindow] {
        get { nav.windows }
        set { nav.windows = newValue }
    }

    var showNavigator: Bool {
        get { nav.showNavigator }
        set { nav.showNavigator = newValue }
    }

    /// Opens `s` in the focused window (code written for one window).
    func open(_ s: Surface) { open(s, in: nav) }

    func open(link: DeepLink) { open(link: link, in: nav) }

    func tile(_ path: String) -> TileInfo? { catalog[path] }

    /// How `tile` opens: native only when the tile has a native UI, this
    /// xbind serves runtimes and hasn't turned them off
    /// (`whoami.native.runtime` ≥ 1), and neither the user nor the remote
    /// kill switch did (AppModel.runtimeGate).
    func surfaceKind(for tile: TileInfo) -> TileSurface {
        TileSurface.pick(tile, serverRuntime: whoami?.nativeRuntime, forceWeb: AppSettings.forcesWeb(id, tile.path),
                         runtimeOff: AppModel.shared.runtimeGate != nil)
    }

    /// Why native views are off in this workspace (nil: they're on).
    var runtimeGate: NativeRuntimeGate? {
        AppModel.shared.runtimeGate ?? NativeRuntimeGate.workspace(nativeRuntime: whoami?.nativeRuntime, loaded: whoami != nil)
    }

    // MARK: Safari hand-off

    /// A path of this workspace as a plain URL (Safari signs in itself).
    func safariURL(path: String) -> URL? { origin.url(path: path) }

    /// Where Safari should go for `path`, signed in: a one-shot ticket for
    /// this device's session (`POST /api/xbin/web-ticket`, the D64 pattern),
    /// or the plain URL on an xbind without the route or for a session that
    /// can't have one (WebHandoff.swift).
    func signedInURL(path: String) async -> URL? {
        let r = try? await auth.send(WebTicket.request(next: path))
        return WebTicket.destination(r, origin: origin, next: path)?.url
    }

    /// Opens `path` in an in-app Safari view, signed in when it can be
    /// (chrome tiles, "Open in Safari").
    func openInSafari(path: String) async {
        guard let u = await signedInURL(path: path) else { return }
        SafariPresenter.present(u)
    }

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
        tileMeta.removeAll()
    }
}
