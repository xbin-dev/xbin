import SwiftUI
import XbinCore
import XbinRenderer

/// The card every tile gets on a phone screen when it draws no widget of
/// its own (D125): its icon, title, badge and status — the dot and, when
/// there's room, what it said — and how it runs (native, or its runtime).
/// The screen grid gives it its frame and its Base Two card; the card
/// (TileCard) puts what runs there in its corner. Compact since D128: the
/// widget inset, a 28-point icon.
struct StandardCard: View {
    let tile: TileInfo
    let size: CardSize
    let workspace: WorkspaceModel

    var body: some View {
        let meta = workspace.tileMeta[tile.path]
        let status = workspace.statuses[tile.path]
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .top, spacing: 8) {
                icon(meta?.symbol)
                if size == .wide { titles }
                Spacer(minLength: 0)
                if let badge = meta?.badge { TileBadge(text: badge) }
                if let status { StatusDot(status: status).padding(.top, 4) }
            }
            Spacer(minLength: 0)
            if size == .small { titles }
            if let status, !status.message.isEmpty {
                Text(verbatim: status.message).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            marker
        }
        .padding(XbinWidgetMetrics.inset)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func icon(_ symbol: String?) -> some View {
        // A tile owns its look: its glyph in ink on the inset panel, no
        // field colour (product-ui 3: user apps get no colour).
        Image(systemName: symbol ?? "square.dashed")
            .font(.system(size: 14, weight: .semibold))
            .foregroundStyle(XbinColor.muted)
            .frame(width: 28, height: 28)
            .background(XbinColor.surface2, in: .xbinPlate)
            .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
            .accessibilityHidden(true)
    }

    private var titles: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(verbatim: tile.title).font(.headline).foregroundStyle(.primary).lineLimit(size == .wide ? 2 : 1)
            Text(verbatim: tile.path).font(.caption2.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
        }
    }

    /// Bottom left (the card's bottom right holds its sessions). A paused
    /// tile (a partition mode switch waiting for a manager, D181) says so
    /// here: nothing of it runs until then.
    @ViewBuilder private var marker: some View {
        if tile.isPaused {
            PausedBadge()
        } else if workspace.surfaceKind(for: tile) == .native {
            Text("native").font(.caption2.bold()).foregroundStyle(XbinColor.muted)
                .padding(.horizontal, 6).padding(.vertical, 2)
                .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
        } else if !tile.runtime.isEmpty, tile.runtime != "static" {
            Text(verbatim: tile.runtime).font(.caption2.monospaced()).foregroundStyle(.tertiary)
        }
    }
}
