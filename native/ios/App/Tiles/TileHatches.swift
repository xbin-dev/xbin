import Foundation
import SwiftUI
import XbinCore
import XbinRenderer

/// A native tile's escape hatches (plans/native.md §8.4, §8.5; docs/native.md
/// "Escape hatches"): what the renderer's `terminal`, `canvas` and composer
/// attach button get from the app (``XbinServices``). Everything runs AS
/// THE TILE — its frame token, its own paths only (TileResource) — never
/// as the user. One per runtime: the terminals and canvas islands live per
/// node key while the node is in the tree, so re-renders (and going full
/// screen) keep their sockets and pages.
@MainActor
final class TileHatches {
    let workspace: WorkspaceModel
    let tile: TileInfo
    let attach: TileAttachFlow
    private var terminals: [String: TileTerminalController] = [:]
    private var islands: [String: (url: URL, controller: WebTileController)] = [:]

    init(workspace: WorkspaceModel, tile: TileInfo) {
        self.workspace = workspace
        self.tile = tile
        attach = TileAttachFlow(workspace: workspace, tile: tile.path)
    }

    private var known: [String] { workspace.catalog.tiles.map(\.path) }

    /// `terminal`: the tile's pty socket in SwiftTerm.
    func terminal(_ r: XbinTerminalRequest) -> AnyView {
        let c: TileTerminalController
        if let existing = terminals[r.key], existing.src == r.src {
            c = existing
            if existing.requestTitle != r.title { existing.requestTitle = r.title }
        } else {
            terminals[r.key]?.stop()
            c = TileTerminalController(workspace: workspace, tile: tile.path, request: r, known: known,
                                       canOpenLinks: tile.canOpenLinks)
            terminals[r.key] = c
        }
        return AnyView(TileTerminalElement(controller: c))
    }

    /// `canvas`: static no-script html, or a page of the tile in an island.
    func canvas(_ r: XbinCanvasRequest) -> AnyView {
        if let html = r.html { return AnyView(StaticCanvas(html: html)) }
        guard let src = r.src, let path = TileResource.pagePath(src, tile: tile.path, known: known),
              let url = TileResource.schemeURL(workspace: workspace.id, path: path) else {
            return AnyView(CanvasRefused(src: r.src ?? ""))
        }
        let c: WebTileController
        if let island = islands[r.key], island.url == url {
            c = island.controller
        } else {
            islands[r.key]?.controller.close()
            c = WebTileController(workspace: workspace, tile: tile.path, canOpenLinks: tile.canOpenLinks, url: url, island: true)
            islands[r.key] = (url, c)
        }
        return AnyView(CanvasIsland(controller: c, tile: tile.path))
    }

    /// The composer's attach button: the pickers, then the uploads.
    func upload(_ r: XbinAttachRequest) async -> [XbinUpload] {
        await attach.run(r)
    }

    /// Drops the terminals and islands whose nodes left the tree.
    func prune(_ tree: Tree) {
        for (k, c) in terminals where !tree.contains(k) {
            c.stop()
            terminals[k] = nil
        }
        for (k, i) in islands where !tree.contains(k) {
            i.controller.close()
            islands[k] = nil
        }
    }

    /// The runtime stopped: every socket and page goes.
    func stopAll() {
        terminals.values.forEach { $0.stop() }
        islands.values.forEach { $0.controller.close() }
        terminals = [:]
        islands = [:]
    }

    /// The renderer's services for these hatches.
    func services(copy: @escaping @MainActor (String) -> Void, openLink: (@MainActor (URL) -> Void)?,
                  imageData: (@MainActor (String) async throws -> Data)?) -> XbinServices {
        // Typed locals: Xcode's type checker gave up on inline closures in
        // one big initializer call (native/AGENTS.md, 1904702).
        let terminal: @MainActor (XbinTerminalRequest) -> AnyView = { [weak self] r in
            self?.terminal(r) ?? AnyView(EmptyView())
        }
        let canvas: @MainActor (XbinCanvasRequest) -> AnyView = { [weak self] r in
            self?.canvas(r) ?? AnyView(EmptyView())
        }
        let attach: @MainActor (XbinAttachRequest) async -> [XbinUpload] = { [weak self] r in
            guard let self else { return [] }
            return await self.upload(r)
        }
        return XbinServices(copy: copy, openLink: openLink, imageData: imageData, terminal: terminal, canvas: canvas,
                            attach: attach)
    }
}
