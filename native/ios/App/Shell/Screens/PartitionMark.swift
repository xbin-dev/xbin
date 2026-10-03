import SwiftUI
import XbinRenderer
import UIKit
import XbinCore

/// The partitioned marker (plans/partitions/06-terminals-ops.md §12.2,
/// design A, PD-53; D181): the web shell's — a ring with its left half
/// filled, "divided" by its shape rather than its hue, in a calm teal
/// (`--bx-part`: #3fb5a3, #1f8778 where the background is light). Drawn as
/// shell-kit.js's 8×8 SVG: a ring of radius 3.35 stroked 1.1 wide, and the
/// disc's left half. Status, not a button: it takes no taps, and its words
/// (the web's tooltip) are what VoiceOver reads — or nothing, when the row
/// says them.
struct PartitionMark: View {
    var size: CGFloat = 8
    /// The marker's words for VoiceOver; nil hides it (its row speaks).
    var label: String?

    static let color = Color(uiColor: UIColor { traits in
        let hex = traits.userInterfaceStyle == .dark ? TilePartition.markColorDark : TilePartition.markColorLight
        return UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
                       blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
    })

    var body: some View {
        let unit = size / 8
        let ring = 6.7 * unit // the stroke's centre line: radius 3.35
        ZStack {
            // The left half of the disc, inside the ring.
            HStack(spacing: 0) {
                Rectangle().fill(Self.color)
                Color.clear
            }
            .frame(width: ring, height: ring)
            .clipShape(Circle())
            Circle()
                .strokeBorder(Self.color, lineWidth: 1.1 * unit)
                .frame(width: ring + 1.1 * unit, height: ring + 1.1 * unit)
        }
        .frame(width: size, height: size)
        .allowsHitTesting(false)
        .accessibilityHidden(label == nil)
        .accessibilityLabel(Text(verbatim: label ?? ""))
    }
}

/// A paused tile's badge (D181): a partition mode switch waits for a tile
/// manager, and nothing of the tile runs — opening it shows xbind's switch
/// page, which says what is asked and who decides. Quiet, like the web
/// shell's "hidden" chip: the page carries the rest.
struct PausedBadge: View {
    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: "pause.circle")
            Text("paused")
        }
        .font(.caption2.weight(.semibold))
        .foregroundStyle(.secondary)
        .padding(.horizontal, 6).padding(.vertical, 2)
        .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: TilePartition.pausedText))
    }
}
