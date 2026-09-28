import Foundation
import Observation
import WebKit
import XbinAgent
import XbinCore
import XbinTerm

/// One workspace: its session, catalog, events socket and web storage —
/// what its windows share (each window navigates it on its own:
/// WorkspaceNav, Navigation.swift).
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
    /// Home: the screens by section (HomeModel, D125).
    var home = HomeModel()
    /// How this user arranged screens on their phone (the `mobile-screens`
    /// pref, per user, next to `layout`).
    var mobile = MobileScreens()
    /// What tiles report about themselves (the cards' status dots).
    var statuses = TileStatuses()
    /// Home has loaded once (screens can say "gone" rather than "loading").
    var homeLoaded = false
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
    @ObservationIgnored private var chromeStoreCache: WKWebsiteDataStore?
    /// The first message of an agent session a build chooser started: the
    /// agent screen sends it once attached (and keeps it as a draft if it
    /// can't).
    @ObservationIgnored private var pendingPrompts: [String: String] = [:]
    @ObservationIgnored private(set) lazy var schemeHandler = TileSchemeHandler(workspace: self)
    @ObservationIgnored private var observer: UUID?
    /// The app's `/ws/events` socket for this workspace (live reload, agent
    /// sessions, the Needs-you inbox); AppModel opens it while a foreground
    /// window shows the workspace.
    @ObservationIgnored private(set) lazy var events: WorkspaceEvents = makeEvents()
    @ObservationIgnored private var relist: Task<Void, Never>?

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

    /// Where chrome tiles keep the cookie session their web ticket opens
    /// (§6.3): a store of its own, never the tiles' — a tile page must not
    /// sit next to the user's session.
    var chromeDataStore: WKWebsiteDataStore {
        if let d = chromeStoreCache { return d }
        let d: WKWebsiteDataStore
        if let uuid = Self.chromeStoreID(id) { d = WKWebsiteDataStore(forIdentifier: uuid) } else { d = .nonPersistent() }
        chromeStoreCache = d
        return d
    }

    /// The chrome store's id: the workspace's, its last byte flipped.
    static func chromeStoreID(_ workspace: String) -> UUID? {
        guard var u = UUID(uuidString: workspace)?.uuid else { return nil }
        u.15 ^= 0xFF
        return UUID(uuid: u)
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
            async let mob = auth.send(APIRequest("GET", MobileScreens.path))
            async let report = auth.json(APIRequest("GET", TileStatuses.path))
            catalog = Catalog(json: try await comps)
            shared = SharedScreens(json: try? await scr)
            layout = PersonalLayout(json: await loadLayout())
            if let r = try? await mob { mobile = MobileScreens(json: r.status == 200 ? try? r.json() : nil) }
            if let r = try? await report { statuses = TileStatuses(json: r) }
            rebuildHome()
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

    /// Re-reads the `layout` or `mobile-screens` pref and rebuilds Home.
    func reloadScreens(key: String) async {
        if key == MobileScreens.key {
            guard let r = try? await auth.send(APIRequest("GET", MobileScreens.path)), r.status == 200 || r.status == 404 else { return }
            mobile = MobileScreens(json: r.status == 200 ? try? r.json() : nil)
        } else {
            layout = PersonalLayout(json: await loadLayout())
        }
        rebuildHome()
    }

    func rebuildHome() {
        home = HomeModel(catalog: catalog, layout: layout, shared: shared, whoami: whoami)
        homeLoaded = true
    }

    /// The shell's `layout` pref, as stored. It lives in the `root` bucket
    /// (the shell is `/c/root/`), which the app's session reads without a
    /// frame token; builds before D125 read `shell`'s (a frame token for
    /// shell), so a layout only there is still found.
    private func loadLayout() async -> XbinCore.JSONValue? {
        if let r = try? await auth.send(APIRequest("GET", LayoutPref.path)) {
            if r.status == 200 { return try? r.json() }
            if r.status != 404 { return nil }
        }
        guard let t = try? await frameTokens.token(for: LayoutPref.legacyComponent) else { return nil }
        let r = try? await transport.send(APIRequest("GET", LayoutPref.path,
                                                     headers: [TileScheme.frameTokenHeader: t,
                                                               TileScheme.clientHeader: AppInfo.clientHeader]), to: origin)
        guard let r, r.status == 200 else { return nil }
        return try? r.json()
    }

    // MARK: Screens (D125)

    func tilesOnPhone(_ s: ScreenInfo) -> [String] { cards(for: s).map(\.path) }

    /// A screen's cards on this phone (the merge rule).
    func cards(for s: ScreenInfo) -> [MobileScreens.Card] {
        mobile.cards(for: s.id, screenTiles: s.tiles, visible: { self.catalog[$0]?.isListed == true })
    }

    /// Saves how screen `id` is arranged on this phone: the pref as stored
    /// now (the screens another client arranged, and keys a newer app
    /// wrote, kept), this screen replaced.
    func saveArrangement(_ id: String, cards: [MobileScreens.Card], hidden: [String]) async throws {
        let r = try await auth.send(APIRequest("GET", MobileScreens.path))
        guard r.status == 200 || r.status == 404 else { throw APIError(r) }
        let stored = r.status == 200 ? try? r.json() : nil
        var next = MobileScreens(json: stored)
        next.set(id, cards: cards, hidden: hidden)
        mobile = next
        _ = try await auth.call(LayoutPref.put(MobileScreens.path, next.merged(into: stored, screen: id), writer: AppSettings.installID))
    }

    /// "+ New screen": a personal screen in the layout pref (read fresh,
    /// every field kept). Its id.
    func addScreen(name: String) async throws -> String {
        let r = try await auth.send(APIRequest("GET", LayoutPref.path))
        guard r.status == 200 || r.status == 404 else { throw APIError(r) }
        let stored = r.status == 200 ? try r.json() : nil
        let id = LayoutPref.newScreenID()
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let next = LayoutPref.addingScreen(id: id, name: trimmed.isEmpty ? LayoutPref.nextScreenName(stored) : trimmed,
                                           to: stored, seed: shared.workspaceDefaultTiles)
        _ = try await auth.call(LayoutPref.put(LayoutPref.path, next, writer: AppSettings.installID))
        layout = PersonalLayout(json: next)
        rebuildHome()
        return id
    }

    /// Creates a tile (`POST /api/xbin/create`) and, for a personal screen
    /// `onScreen`, places it on the screen's web layout too (org screens
    /// keep their draft/revision flow on the web; the phone arrangement is
    /// the caller's). Its path.
    func createTile(name: String, owner: String, onScreen: String?) async throws -> String {
        guard let req = TileCreate.request(name: name, owner: owner), let path = TileCreate.tilePath(name: name) else {
            throw APIError(status: 400, message: "Enter a name with letters or digits.")
        }
        _ = try await auth.call(req)
        if let comps = try? await auth.json(APIRequest("GET", "/api/xbin/components")) { catalog = Catalog(json: comps) }
        if let id = onScreen, home.screen(id)?.kind == .personal,
           let r = try? await auth.send(APIRequest("GET", LayoutPref.path)), r.status == 200,
           let next = LayoutPref.adding(tile: path, toScreen: id, in: try? r.json()) {
            if (try? await auth.call(LayoutPref.put(LayoutPref.path, next, writer: AppSettings.installID))) != nil {
                layout = PersonalLayout(json: next)
            }
        }
        rebuildHome()
        return path
    }

    func setPendingPrompt(_ text: String, session: String) { pendingPrompts[session] = text }
    func takePendingPrompt(_ session: String) -> String? { pendingPrompts.removeValue(forKey: session) }

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
        let tokens = frameTokens
        e.onCodeChange = { tile in await tokens.invalidate(tile) }
        e.onEvent = { [weak self] ev in
            switch ev {
            case .tileStatus(let tile, let level, let message, let transient):
                self?.statuses.apply(tile: tile, level: level, message: message, transient: transient)
            case .prefs(let component, let key, let writer):
                // Another client (the web shell, another phone) changed the
                // layout or the phone arrangement: Home and the screens follow.
                guard LayoutPref.concernsHome(component: component, key: key, writer: writer, me: AppSettings.installID) else { return }
                Task { await self?.reloadScreens(key: key) }
            default:
                break
            }
        }
        e.onBranding = { [weak self] in Task { await self?.refreshBranding() } }
        e.onNativeSwitch = { [weak self] in Task { await self?.refreshWhoami() } }
        // A gap may have hidden a `native` too: re-read whoami with the list.
        e.onResync = { [weak self] in
            self?.scheduleRelist()
            Task { await self?.refreshWhoami() }
            // …and a layout or arrangement written meanwhile (a `prefs`).
            Task {
                await self?.reloadScreens(key: "layout")
                await self?.reloadScreens(key: MobileScreens.key)
            }
        }
        return e
    }

    /// Re-reads whoami alone — the workspace's native-runtime switch changed
    /// (`native` on the events socket, plans/native.md §23). Tile screens
    /// pick their surface from it (surfaceKind), so an open native view
    /// falls back to its web page at once, and new tiles open as pages.
    /// A failed read keeps what was known.
    func refreshWhoami() async {
        guard let j = try? await auth.json(APIRequest("GET", "/api/xbin/whoami")) else { return }
        let who = Whoami(json: j)
        guard !who.userID.isEmpty else { return }
        whoami = who
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
    //
    // Navigation is per window (WorkspaceNav, Navigation.swift): a screen
    // acts on its own window's — `@Environment(WorkspaceNav.self)` — never
    // on "the focused window", so a page in a background window can't push
    // onto, or navigate, the window in front.

    /// Opens `s` full screen in a window's navigation, over the screen it
    /// sits on — the one shown when it's there — or Home: where back goes.
    func open(_ s: Surface, in nav: WorkspaceNav) {
        let screen = WorkspaceNav.screen(for: s, current: nav.screenID) { path, current in
            self.home.screenID(containing: path, preferring: current, tiles: self.tilesOnPhone)
        }
        nav.open(s, on: screen)
        lastActivity = Date()
    }

    /// Restores a window's place (a launch, a new window's value).
    func restore(_ t: WindowTarget, in nav: WorkspaceNav) {
        nav.restore(screen: t.screen, surface: t.surface)
    }

    func open(link: DeepLink, in nav: WorkspaceNav) {
        switch link {
        case .tile(_, let tile, let fragment): open(.tile(tile, fragment: fragment), in: nav)
        case .terminal(_, let session):
            open(.terminal(cwd: sessions.first { $0.id == session }?.cwd ?? "", session: session), in: nav)
        case .agent(_, let session): open(.agent(cwd: nil, session: session), in: nav)
        default: nav.goHome()
        }
    }

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

    // MARK: Chrome tiles

    /// Where a chrome tile's web view goes first (§6.3): a one-shot ticket
    /// for this device's session (`POST /api/xbin/web-ticket`, D100),
    /// redeemed inside that web view — the workspace shows "Continue as
    /// <name>", one tap gives the view its own cookie session — or the
    /// plain page on an xbind without the route or for a session that
    /// can't have one (WebHandoff.swift).
    func chromeURL(path: String) async -> URL? {
        let r = try? await auth.send(WebTicket.request(next: path))
        return WebTicket.destination(r, origin: origin, signedOrigin: record.signedOrigin, next: path)?.url
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
        if let chrome = Self.chromeStoreID(id) {
            chromeStoreCache = nil
            _ = WKWebsiteDataStore(forIdentifier: chrome) // (as below: WebKit must have seen it)
            try? await WKWebsiteDataStore.remove(forIdentifier: chrome)
        }
        if let uuid = UUID(uuidString: id) {
            dataStoreCache = nil
            // WebKit crashes (SIGSEGV, iOS 27 simulator) removing a data
            // store it hasn't seen in this process — a workspace whose web
            // tiles weren't opened since launch. Naming it first sets WebKit
            // up for it; the object goes at once, so it isn't "in use".
            _ = WKWebsiteDataStore(forIdentifier: uuid)
            try? await WKWebsiteDataStore.remove(forIdentifier: uuid)
        }
        NativeStateFile.removeAll(workspace: id)
        NativeRuntimePool.shared.forget(workspace: id)
        tileMeta.removeAll()
    }
}
