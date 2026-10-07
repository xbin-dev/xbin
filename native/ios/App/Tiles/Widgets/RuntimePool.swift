import Foundation
import Observation
import UIKit
import WebKit
import XbinCore

/// The native runtimes that are live, app-wide (D125): a tile screen's and
/// the widgets' on a phone screen share them. At most
/// ``NativeRuntimePool/capacity`` run — least recently used first out, an
/// open tile never (RuntimePoolPolicy) — and a card whose runtime isn't
/// live draws its tile's last widget from ``WidgetCache``. A screen starts
/// runtimes only for the cards it shows, and only for tiles that draw a
/// widget (or haven't been seen not to).
///
/// The runtimes' documents are parked in the key window, 1×1 and behind
/// the app's views, whoever shows them: WebKit keeps a document out of any
/// window from running its timers, and one view can't sit in two places
/// (a peeked screen and its tile).
@MainActor
@Observable
final class NativeRuntimePool {
    static let shared = NativeRuntimePool()
    static let capacity = 6
    /// After a runtime failed, how long a card waits before starting it
    /// again (opening the tile always tries).
    static let failureBackoff: TimeInterval = 600

    struct Key: Hashable, Sendable {
        let workspace: String
        let tile: String
    }

    /// The live runtimes: cards redraw when one comes or goes.
    private(set) var runtimes: [Key: NativeTileRuntime] = [:]

    @ObservationIgnored private var policy = RuntimePoolPolicy<Key>(capacity: NativeRuntimePool.capacity)
    @ObservationIgnored private var caches: [String: WidgetCache] = [:]
    /// The fallback of each tile screen showing a runtime (by screen).
    @ObservationIgnored private var screens: [Key: [UUID: (String) -> Void]] = [:]
    @ObservationIgnored private var failedAt: [Key: Date] = [:]
    @ObservationIgnored private var memoryWarning: NSObjectProtocol?

    private init() {
        memoryWarning = NotificationCenter.default.addObserver(
            forName: UIApplication.didReceiveMemoryWarningNotification, object: nil, queue: .main
        ) { _ in MainActor.assumeIsolated { NativeRuntimePool.shared.trim() } }
    }

    /// `workspace`'s widget cache.
    func widgets(_ workspace: WorkspaceModel) -> WidgetCache {
        if let c = caches[workspace.id] { return c }
        let c = WidgetCache(workspace: workspace.id)
        caches[workspace.id] = c
        return c
    }

    /// The live runtime of `tile`, if any.
    func runtime(_ workspace: WorkspaceModel, _ tile: String) -> NativeTileRuntime? {
        runtimes[Key(workspace: workspace.id, tile: tile)]
    }

    // MARK: Tile screens

    /// A tile screen shows `tile`: its runtime — the live one, or a new one
    /// started now — pinned until ``close(_:_:screen:)``. `fallBack` hears
    /// when the runtime fails (the screen shows the web page). A deep link's
    /// `fragment` starts a new runtime there, or takes a live one there
    /// (`xbn.navigate`, D189).
    func open(_ workspace: WorkspaceModel, _ tile: TileInfo, screen: UUID, fragment: String? = nil,
              fallBack: @escaping (String) -> Void) -> NativeTileRuntime {
        let key = Key(workspace: workspace.id, tile: tile.path)
        screens[key, default: [:]][screen] = fallBack
        failedAt[key] = nil
        let evicted = policy.open(key)
        let rt: NativeTileRuntime
        if let live = runtimes[key] {
            rt = live
            if let fragment, !fragment.isEmpty { live.navigate(fragment) }
        } else {
            rt = start(key, workspace, tile, size: widgets(workspace).lastSize(tile.path) ?? .small, fragment: fragment)
        }
        finish(evicted)
        return rt
    }

    /// The tile screen went away for good: the runtime stays warm (its
    /// card may show it; reopening is instant) until the pool needs the
    /// room, but its terminals and canvas pages end.
    func close(_ workspace: WorkspaceModel, _ tile: String, screen: UUID) {
        let key = Key(workspace: workspace.id, tile: tile)
        screens[key]?[screen] = nil
        if screens[key]?.isEmpty == true { screens[key] = nil }
        let evicted = policy.close(key)
        if !policy.isOpen(key) { runtimes[key]?.hatches.stopAll() }
        finish(evicted)
    }

    // MARK: Cards

    /// A card showing `tile`'s widget at `size` came on screen: its runtime
    /// runs — unless the tile was seen to draw no widget, or failed lately.
    func show(_ workspace: WorkspaceModel, _ tile: TileInfo, size: CardSize) {
        let key = Key(workspace: workspace.id, tile: tile.path)
        if let rt = runtimes[key] {
            rt.setWidgetSize(size)
        } else {
            let cache = widgets(workspace)
            guard cache.shouldProbe(tile.path) else { return }
            if let t = failedAt[key], Date().timeIntervalSince(t) < Self.failureBackoff { return }
        }
        let evicted = policy.show(key)
        if runtimes[key] == nil { _ = start(key, workspace, tile, size: size) }
        finish(evicted)
    }

    /// The card on screen changed size: the live runtime draws for it.
    func resize(_ workspace: WorkspaceModel, _ tile: String, size: CardSize) {
        runtimes[Key(workspace: workspace.id, tile: tile)]?.setWidgetSize(size)
    }

    /// The card went off screen.
    func hide(_ workspace: WorkspaceModel, _ tile: String) {
        let key = Key(workspace: workspace.id, tile: tile)
        policy.hide(key)
        finish([])
    }

    /// The workspace signed out: its runtimes end, its widgets are forgotten.
    func forget(workspace id: String) {
        for (key, rt) in runtimes where key.workspace == id {
            end(key, rt)
        }
        screens = screens.filter { $0.key.workspace != id }
        caches[id]?.removeAll()
        caches[id] = nil
        WidgetCache.remove(workspace: id)
    }

    // MARK: -

    private func start(_ key: Key, _ workspace: WorkspaceModel, _ tile: TileInfo, size: CardSize,
                       fragment: String? = nil) -> NativeTileRuntime {
        let rt = NativeTileRuntime(workspace: workspace, tile: tile, widgetSize: size, widgets: widgets(workspace))
        rt.onFallback = { [weak self] banner in self?.failed(key, banner) }
        runtimes[key] = rt
        Self.park(rt.webView)
        rt.start(fragment: fragment)
        return rt
    }

    private func failed(_ key: Key, _ banner: String) {
        failedAt[key] = Date()
        let listeners = Array((screens[key] ?? [:]).values)
        if let rt = runtimes[key] { end(key, rt) }
        listeners.forEach { $0(banner) }
    }

    private func end(_ key: Key, _ rt: NativeTileRuntime) {
        rt.stop()
        rt.webView.removeFromSuperview()
        runtimes[key] = nil
        policy.drop(key)
    }

    /// Stops what the policy evicted, parks what runs, and sets each
    /// runtime's visibility (on screen: visible, full speed; else throttled).
    private func finish(_ evicted: [Key]) {
        for key in evicted {
            if let rt = runtimes[key] { end(key, rt) }
        }
        for (key, rt) in runtimes {
            Self.park(rt.webView)
            rt.setVisible(policy.isVisible(key))
        }
    }

    /// Memory is short: every runtime not showing its tile goes.
    private func trim() {
        for (key, rt) in runtimes where !policy.isOpen(key) {
            end(key, rt)
        }
    }

    /// Puts a runtime document in the key window (1×1, behind the app's
    /// views, hidden from accessibility) unless it's in one already.
    private static func park(_ webView: WKWebView) {
        guard webView.window == nil, let window = hostWindow() else { return }
        webView.frame = CGRect(x: 0, y: 0, width: 1, height: 1)
        webView.accessibilityElementsHidden = true
        window.insertSubview(webView, at: 0)
    }

    private static func hostWindow() -> UIWindow? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let active = scenes.filter { $0.activationState == .foregroundActive }
        let windows = (active.isEmpty ? scenes : active).flatMap(\.windows)
        return windows.first(where: \.isKeyWindow) ?? windows.first
    }
}

/// One workspace's ``WidgetStore`` for the app: observable (a card redraws
/// when its tile's cached widget changes) and saved to Caches a moment
/// after it changes — a counter that ticks every second writes the file
/// every few seconds, not every tick.
@MainActor
@Observable
final class WidgetCache {
    let workspace: String
    @ObservationIgnored private let store: WidgetStore
    @ObservationIgnored private var saving: Task<Void, Never>?
    @ObservationIgnored private var sizes: [String: CardSize] = [:]
    /// Bumped by every change (what the views observe).
    private(set) var revision = 0

    init(workspace: String) {
        self.workspace = workspace
        store = WidgetStore(file: Self.url(workspace))
    }

    /// The widget `tile` last drew at `size`.
    func snapshot(_ tile: String, size: CardSize) -> WidgetStore.Snapshot? {
        _ = revision
        return store.snapshot(tile, size: size)
    }

    /// The size `tile`'s widget was last drawn at (a new runtime starts
    /// with it).
    func lastSize(_ tile: String) -> CardSize? {
        sizes[tile] ?? store.records[tile]?.trees.values.max(by: { $0.at < $1.at })?.size
    }

    func shouldProbe(_ tile: String) -> Bool { store.shouldProbe(tile, now: Date()) }

    func record(_ tile: String, root: Node, version: Int, size: CardSize) {
        sizes[tile] = size
        guard store.record(tile, root: root, version: version, size: size, at: Date()) else { return }
        changed()
    }

    func noteNoWidget(_ tile: String) {
        store.noteNoWidget(tile, at: Date())
        changed()
    }

    func removeAll() {
        saving?.cancel()
        store.removeAll()
        revision += 1
    }

    private func changed() {
        revision += 1
        guard saving == nil else { return }
        saving = Task { [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard let self, !Task.isCancelled else { return }
            self.saving = nil
            try? self.store.save()
        }
    }

    /// The cache file of a workspace that isn't loaded (sign-out).
    static func remove(workspace: String) {
        try? FileManager.default.removeItem(at: url(workspace))
    }

    private static func url(_ workspace: String) -> URL {
        FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("tile-widgets", isDirectory: true)
            .appendingPathComponent(workspace.replacingOccurrences(of: "/", with: "_") + ".json")
    }
}
