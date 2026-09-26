import Foundation
import Observation
import XbinCore

/// A place the user was, across workspaces (the switcher's recents).
struct Recent: Codable, Hashable, Identifiable {
    var workspace: String
    var surface: Surface
    var title: String
    var at: Date
    var id: String { "\(workspace)|\(surface.title)|\(title)" }
}

/// What's waiting for the user in the add-workspace sheet.
enum AddRequest: Equatable, Identifiable {
    case blank
    /// An `xbin://enroll` link (scanned or opened from outside).
    case enroll(server: ServerOrigin, code: String)

    var id: String {
        switch self {
        case .blank: return "blank"
        case .enroll(let s, let c): return "\(s.origin)|\(c)"
        }
    }
}

/// The app: its workspaces (several; fast switching, §4), the windows that
/// show them, deep links, the lock, the native-runtime switches.
///
/// Each window has its own selection and navigation (SceneModel); what the
/// workspaces share — sessions, catalogs, the events sockets — lives here
/// and in WorkspaceModel. A screen navigates its own window only
/// (`@Environment(WorkspaceNav.self)`); what arrives from outside every
/// window — a notification tap, an `xbin://` link — goes to the focused
/// one: the one the user last touched (``focusedScene``).
@MainActor
@Observable
final class AppModel {
    static let shared = AppModel()

    private(set) var workspaces: [WorkspaceModel] = []
    var recents: [Recent] = []
    /// "Require Face ID when opening the app": true until unlocked.
    var locked = AppSettings.appLock
    /// Why native tile UIs are off app-wide (Settings, the remote switch);
    /// nil = on. A workspace can still turn them off for itself.
    private(set) var runtimeGate: NativeRuntimeGate?
    /// The app is in the foreground (some window active).
    private(set) var isActive = false
    /// The workspace a new window starts on (the last one picked anywhere;
    /// kept in workspaces.json).
    private(set) var lastSelectedID: String?
    /// The last run ended in the foreground (a crash, a watchdog kill) and
    /// the remote switch hasn't been read since: windows restore to their
    /// workspace, not straight into a tile (WindowTarget.restoring, §23).
    private(set) var cautiousRestore = ForegroundMark(UserDefaults.standard).lastRunEndedInForeground

    /// A link that arrived before its workspace was ready.
    @ObservationIgnored private var pendingLink: DeepLink?
    /// Records that couldn't be read by this app version (kept on save).
    @ObservationIgnored private var unreadable: [JSONValue] = []
    @ObservationIgnored private var scenes: [WeakScene] = []
    @ObservationIgnored private weak var focused: SceneModel?
    /// The remote switch's fetch in flight (one at a time).
    @ObservationIgnored private var remoteFetch: Task<Void, Never>?
    @ObservationIgnored private let foregroundMark = ForegroundMark(UserDefaults.standard)

    private struct WeakScene { weak var scene: SceneModel? }

    private init() {
        let list = WorkspaceListFile.load()
        unreadable = list.unreadable
        workspaces = list.workspaces.map { WorkspaceModel(record: $0) }
        lastSelectedID = list.selected ?? workspaces.first?.id
        if let d = UserDefaults.standard.data(forKey: "recents"),
           let r = try? JSONDecoder().decode([Recent].self, from: d) { recents = r }
        runtimeGate = RemoteConfig.gate()
    }

    func workspace(_ id: String) -> WorkspaceModel? { workspaces.first { $0.id == id } }

    func save() {
        var list = WorkspaceList(workspaces: workspaces.map(\.record), selected: lastSelectedID)
        list.unreadable = unreadable
        WorkspaceListFile.save(list)
    }

    // MARK: Windows

    /// A window appeared (its root view). True when it took a link that was
    /// waiting for a window (it shows that, not its restored place).
    @discardableResult
    func register(_ scene: SceneModel) -> Bool {
        // Its UI is about to mount (at launch, before the scene is active):
        // a crash from here on is a crash in the foreground.
        foregroundMark.enteredForeground()
        scenes.removeAll { $0.scene == nil || $0.scene === scene }
        scenes.append(WeakScene(scene: scene))
        if focused == nil { focused = scene }
        guard let link = pendingLink, let w = resolve(link) else { return false }
        pendingLink = nil
        scene.select(w.id)
        w.open(link: link, in: scene.nav(for: w))
        return true
    }

    /// The user is in this window now (it became key or active).
    func focus(_ scene: SceneModel) {
        focused = scene
        if let id = scene.selectedID { lastSelectedID = id }
    }

    /// Live windows, most recently focused first.
    var liveScenes: [SceneModel] {
        let all = scenes.compactMap(\.scene)
        guard let f = focused, all.contains(where: { $0 === f }) else { return all }
        return [f] + all.filter { $0 !== f }
    }

    /// The window the user last touched (nil before any appeared).
    var focusedScene: SceneModel? { liveScenes.first }

    /// A window picked a workspace.
    func selectedInScene(_ id: String) {
        lastSelectedID = id
        save()
        updateSockets()
    }

    /// Scene phase or selection changed: exactly the workspaces a foreground
    /// window shows keep an events socket (plans/native.md §7.7).
    func updateSockets() {
        let shown = Set(liveScenes.filter(\.isForeground).compactMap(\.selectedID))
        for w in workspaces { w.events.setWanted(isActive && shown.contains(w.id)) }
    }

    // MARK: Adding and removing

    /// Adopts a freshly signed-in workspace and shows it in `scene`.
    func add(_ record: WorkspaceRecord, session: SessionCredential, in scene: SceneModel?) async {
        let w = WorkspaceModel(record: record)
        await w.auth.adopt(session)
        workspaces.append(w)
        lastSelectedID = w.id
        scene?.addRequest = nil
        scene?.select(w.id)
        save()
        await w.refresh()
        PushManager.shared.workspaceAdded(w)
        if let link = pendingLink, let target = resolve(link) {
            pendingLink = nil
            let s = scene ?? focusedScene
            s?.select(target.id)
            if let s { target.open(link: link, in: s.nav(for: target)) }
        }
    }

    func remove(_ id: String, removeDevice: Bool) async {
        guard let w = workspace(id) else { return }
        w.events.setWanted(false)
        await PushManager.shared.workspaceRemoved(w)
        await w.forget(removeDevice: removeDevice)
        workspaces.removeAll { $0.id == id }
        recents.removeAll { $0.workspace == id }
        for s in liveScenes { s.forget(id) }
        if lastSelectedID == id { lastSelectedID = workspaces.first?.id }
        save()
        saveRecents()
        updateSockets()
    }

    /// Reorders the switcher (List's onMove offsets).
    func move(from: IndexSet, to: Int) {
        let moving = from.map { workspaces[$0] }
        var rest = workspaces.enumerated().filter { !from.contains($0.offset) }.map(\.element)
        let at = to - from.filter { $0 < to }.count
        rest.insert(contentsOf: moving, at: max(0, min(at, rest.count)))
        workspaces = rest
        save()
    }

    // MARK: Links

    func resolve(_ link: DeepLink) -> WorkspaceModel? {
        guard let ref = link.workspace else { return nil }
        return workspaces.first { $0.record.matches(linkWorkspace: ref) }
    }

    /// Keeps a link until its workspace exists (added, or a window appears).
    func holdLink(_ link: DeepLink) { pendingLink = link }

    /// `xbin://…` from outside a window (a notification, a widget).
    func open(url: URL) {
        guard let link = try? DeepLink(url: url) else { return }
        open(link: link)
    }

    /// A link from outside a window: the focused window follows it.
    func open(link: DeepLink) {
        if let s = focusedScene { s.open(link: link) } else { pendingLink = link }
    }

    /// Whether a foreground window shows the surface `link` names — then a
    /// notification about it needs no banner (push.md §4.5).
    func isShowing(app: String?, link: String) -> Bool {
        guard let app, isActive else { return false }
        let target = PushPayload(ws: "", kind: "", title: "", link: link).deepLink(appWorkspace: app)
        for s in liveScenes where s.isForeground && s.selectedID == app {
            guard let surface = s.existingNav(app)?.surface else { continue }
            switch target {
            case .tile(_, let t, _): if case .tile(let p, _, _) = surface, p == t { return true }
            case .agent(_, let id): if case .agent(_, let sid) = surface, sid == id { return true }
            case .terminal(_, let id): if case .terminal(_, let sid) = surface, sid == id { return true }
            default: break
            }
        }
        return false
    }

    /// Whether a foreground window shows agent session `session` of `workspace`.
    func isShowingAgent(workspace: String, session: String) -> Bool {
        guard isActive else { return false }
        return liveScenes.contains { s in
            guard s.isForeground, s.selectedID == workspace, case .agent(_, let sid)? = s.existingNav(workspace)?.surface else {
                return false
            }
            return sid == session
        }
    }

    // MARK: Recents

    func visited(_ w: WorkspaceModel, _ s: Surface, title: String) {
        recents.removeAll { $0.workspace == w.id && $0.surface == s }
        recents.insert(Recent(workspace: w.id, surface: s, title: title, at: Date()), at: 0)
        if recents.count > 20 { recents.removeLast(recents.count - 20) }
        saveRecents()
    }

    private func saveRecents() {
        if let d = try? JSONEncoder().encode(recents) { UserDefaults.standard.set(d, forKey: "recents") }
    }

    // MARK: Lifecycle

    /// The app came to the foreground (scene phase active — called before
    /// the app lock or anything else awaits): the remote switch is re-read
    /// when due, first and beside everything else, so a workspace's slow
    /// network or a native tile that crashes as it mounts can't keep it from
    /// landing (§23); and the crash mark is set.
    func enteredForeground() {
        refreshRemoteSwitch()
        foregroundMark.enteredForeground()
    }

    /// Foreground, past the lock: refresh what's visible, keep push
    /// registrations right, reopen the events sockets.
    func becameActive() async {
        isActive = true
        updateSockets()
        let shown = Set(liveScenes.compactMap(\.selectedID))
        for w in workspaces where shown.contains(w.id) { await w.refresh() }
        for w in workspaces where !shown.contains(w.id) { await w.refreshSessions() }
        await PushManager.shared.maintainAll()
    }

    /// Fetches the remote switch when it is due — at once after a run that
    /// ended in the foreground — off the activation's path; the gate follows
    /// the moment it lands. At launch too (the app delegate), before any
    /// window restores.
    func refreshRemoteSwitch() {
        guard remoteFetch == nil else { return }
        let unclean = cautiousRestore
        remoteFetch = Task { [weak self] in
            let changed = await RemoteConfig.refreshIfDue(afterUncleanExit: unclean)
            guard let self else { return }
            self.remoteFetch = nil
            if changed { self.runtimeGate = RemoteConfig.gate() }
            self.cautiousRestore = false
        }
    }

    /// Background: the sockets close (reopened, and caught up, on return).
    func enteredBackground() {
        isActive = false
        foregroundMark.enteredBackground()
        updateSockets()
    }

    /// Settings changed the user's native-views switch.
    func runtimeSwitchChanged() { runtimeGate = RemoteConfig.gate() }

    func unlock() async {
        guard locked else { return }
        if await AppLock.unlock(reason: "Unlock xbin") { locked = false }
    }

    /// Needs-you counts across workspaces (the switcher and the inbox).
    var needsYouCount: Int { workspaces.reduce(0) { $0 + $1.needsYou.count } }
}
