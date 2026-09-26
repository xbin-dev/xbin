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
enum AddRequest: Equatable {
    case blank
    /// An `xbin://enroll` link (scanned or opened from outside).
    case enroll(server: ServerOrigin, code: String)
}

/// The app: its workspaces (several; fast switching, §4), the switcher,
/// deep links, the lock.
@MainActor
@Observable
final class AppModel {
    static let shared = AppModel()

    private(set) var workspaces: [WorkspaceModel] = []
    var selectedID: String?
    var showSwitcher = false
    var addRequest: AddRequest?
    var showInbox = false
    var showSettings = false
    var recents: [Recent] = []
    /// "Require Face ID when opening the app": true until unlocked.
    var locked = AppSettings.appLock
    /// A link that arrived before its workspace was ready.
    @ObservationIgnored private var pendingLink: DeepLink?
    /// Records that couldn't be read by this app version (kept on save).
    @ObservationIgnored private var unreadable: [JSONValue] = []

    private init() {
        let list = WorkspaceListFile.load()
        unreadable = list.unreadable
        workspaces = list.workspaces.map { WorkspaceModel(record: $0) }
        selectedID = list.selected ?? workspaces.first?.id
        if let d = UserDefaults.standard.data(forKey: "recents"),
           let r = try? JSONDecoder().decode([Recent].self, from: d) { recents = r }
    }

    var selected: WorkspaceModel? { workspaces.first { $0.id == selectedID } ?? workspaces.first }

    func workspace(_ id: String) -> WorkspaceModel? { workspaces.first { $0.id == id } }

    func select(_ id: String) {
        guard workspace(id) != nil else { return }
        selectedID = id
        showSwitcher = false
        save()
        if let w = workspace(id), w.whoami == nil { Task { await w.refresh() } }
    }

    /// ⌘1…⌘9.
    func select(index: Int) {
        guard workspaces.indices.contains(index) else { return }
        select(workspaces[index].id)
    }

    func save() {
        var list = WorkspaceList(workspaces: workspaces.map(\.record), selected: selectedID)
        list.unreadable = unreadable
        WorkspaceListFile.save(list)
    }

    // MARK: Adding and removing

    /// Adopts a freshly signed-in workspace and shows it.
    func add(_ record: WorkspaceRecord, session: SessionCredential) async {
        let w = WorkspaceModel(record: record)
        await w.auth.adopt(session)
        workspaces.append(w)
        selectedID = w.id
        addRequest = nil
        save()
        await w.refresh()
        PushManager.shared.workspaceAdded(w)
        if let link = pendingLink, let target = resolve(link) {
            pendingLink = nil
            target.open(link: link)
        }
    }

    func remove(_ id: String, removeDevice: Bool) async {
        guard let w = workspace(id) else { return }
        await PushManager.shared.workspaceRemoved(w)
        await w.forget(removeDevice: removeDevice)
        workspaces.removeAll { $0.id == id }
        recents.removeAll { $0.workspace == id }
        if selectedID == id { selectedID = workspaces.first?.id }
        save()
        saveRecents()
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

    /// `xbin://…` from outside (onOpenURL), a notification or a QR code.
    func open(url: URL) {
        guard let link = try? DeepLink(url: url) else { return }
        open(link: link)
    }

    func open(link: DeepLink) {
        switch link {
        case .enroll(let server, let code):
            addRequest = .enroll(server: server, code: code)
        case .sso, .ssoError:
            // The web authentication session delivers these to its caller.
            break
        default:
            guard let w = resolve(link) else { pendingLink = link; return }
            select(w.id)
            w.open(link: link)
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

    /// Foreground: refresh what's visible, keep push registrations right.
    func becameActive() async {
        if let w = selected { await w.refresh() }
        for w in workspaces where w.id != selectedID { await w.refreshSessions() }
        await PushManager.shared.maintainAll()
    }

    func unlock() async {
        guard locked else { return }
        if await AppLock.unlock(reason: "Unlock xbin") { locked = false }
    }

    /// Needs-you counts across workspaces (the switcher and the inbox).
    var needsYouCount: Int { workspaces.reduce(0) { $0 + $1.needsYou.count } }
}
