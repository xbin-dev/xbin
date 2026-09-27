import SwiftUI
import XbinCore

/// The card every tile gets on a phone screen when it draws no widget of
/// its own (D125): its icon, title, badge and status — the dot and, when
/// there's room, what it said — and how it runs (native, or its runtime).
/// The screen grid gives it its frame and rounded background.
struct StandardCard: View {
    let tile: TileInfo
    let size: CardSize
    let workspace: WorkspaceModel

    var body: some View {
        let meta = workspace.tileMeta[tile.path]
        let status = workspace.statuses[tile.path]
        VStack(alignment: .leading, spacing: 6) {
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
                Text(verbatim: status.message).font(.caption).foregroundStyle(.secondary).lineLimit(size == .wide ? 2 : 1)
            }
            marker
        }
        .padding(14)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func icon(_ symbol: String?) -> some View {
        Image(systemName: symbol ?? "square.dashed")
            .font(.system(size: 18, weight: .semibold))
            .foregroundStyle(Color.xbinAmber)
            .frame(width: 36, height: 36)
            .background(Color.xbinAmber.opacity(0.16), in: RoundedRectangle(cornerRadius: 10))
            .accessibilityHidden(true)
    }

    private var titles: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(verbatim: tile.title).font(.headline).foregroundStyle(.primary).lineLimit(2)
            Text(verbatim: tile.path).font(.caption2.monospaced()).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    @ViewBuilder private var marker: some View {
        if workspace.surfaceKind(for: tile) == .native {
            Text("native").font(.caption2.bold()).padding(.horizontal, 6).padding(.vertical, 2)
                .background(Color.xbinAmber.opacity(0.25), in: Capsule())
        } else if !tile.runtime.isEmpty, tile.runtime != "static" {
            Text(verbatim: tile.runtime).font(.caption2.monospaced()).foregroundStyle(.tertiary)
        }
    }
}
