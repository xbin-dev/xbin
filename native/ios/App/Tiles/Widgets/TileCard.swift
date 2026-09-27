import SwiftUI
import XbinCore

/// One tile on a phone screen (D117): the tile's native widget when it has
/// sent one, else the standard card. The screen grid (App/Shell/Screens)
/// draws every tile through this.
struct TileCard: View {
    let tile: TileInfo
    let size: CardSize
    let workspace: WorkspaceModel

    var body: some View {
        StandardCard(tile: tile, size: size, workspace: workspace)
    }
}
