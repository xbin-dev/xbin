import SwiftUI
import XbinCore

/// The card every tile gets on a phone screen when it draws no widget of
/// its own (D117): its icon, title, badge and status.
struct StandardCard: View {
    let tile: TileInfo
    let size: CardSize
    let workspace: WorkspaceModel

    var body: some View {
        Text(verbatim: tile.title)
    }
}
