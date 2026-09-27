import SwiftUI
import XbinCore
import XbinRenderer

/// One tile on a phone screen (D125): the tile's native widget when it has
/// sent one, else the standard card. The screen grid (App/Shell/Screens)
/// draws every tile through this.
///
/// A native tile's card runs the tile's runtime while it is on screen
/// (``NativeRuntimePool``: a few at most, least recently used first out)
/// and draws the widget live; otherwise — the pool's room is taken, the
/// runtime is still starting, the app just launched — the widget it drew
/// last (``WidgetCache``), which doesn't take taps. A tile that draws no
/// widget, or whose widget just failed to render, gets the standard card. The grid gives either one its frame, its
/// card background and its tap (open the tile); taps on the widget's
/// controls go to the tile instead (`target:"widget"` events).
struct TileCard: View {
    let tile: TileInfo
    let size: CardSize
    let workspace: WorkspaceModel

    /// On screen as far as the pool knows (appeared and scrolled into view).
    @State private var shown = false

    var body: some View {
        let native = workspace.surfaceKind(for: tile) == .native
        let pool = NativeRuntimePool.shared
        let live = native ? pool.runtime(workspace, tile.path) : nil
        Group {
            if live?.widgetModel.failure != nil {
                // The widget's render failed (the runtime is asked for its
                // last good tree): the standard card meanwhile.
                StandardCard(tile: tile, size: size, workspace: workspace)
            } else if let rt = live, rt.widgetModel.root != nil {
                XbinTreeView(model: rt.widgetModel, services: rt.widgetServices,
                             options: XbinRenderOptions(compact: size))
                    .xbinWidgetInset()
                    .id(ObjectIdentifier(rt))
            } else if native, let snap = pool.widgets(workspace).snapshot(tile.path, size: size) {
                CachedWidget(snapshot: snap)
                    .xbinWidgetInset()
                    .id(snap.at)
            } else {
                StandardCard(tile: tile, size: size, workspace: workspace)
            }
        }
        .onAppear { setShown(native) }
        .onDisappear { setShown(false) }
        .onScrollVisibilityChange(threshold: 0.2) { setShown($0 && native) }
        .onChange(of: size) { if shown { pool.resize(workspace, tile.path, size: size) } }
    }

    private func setShown(_ on: Bool) {
        guard on != shown else { return }
        shown = on
        if on {
            NativeRuntimePool.shared.show(workspace, tile, size: size)
        } else {
            NativeRuntimePool.shared.hide(workspace, tile.path)
        }
    }
}

/// The widget a tile drew last, while its runtime isn't live: drawn as it
/// was, without taps (the card's tap opens the tile).
private struct CachedWidget: View {
    @State private var model: XbinTreeModel
    private let size: CardSize

    init(snapshot: WidgetStore.Snapshot) {
        let store = TreeStore()
        store.apply(.mount(version: snapshot.version, root: snapshot.root))
        _model = State(initialValue: XbinTreeModel(store: store))
        size = snapshot.size
    }

    var body: some View {
        XbinTreeView(model: model, options: XbinRenderOptions(compact: size))
            .allowsHitTesting(false)
    }
}
