import Foundation
import XbinCore

/// A phone screen's cards (D125): two columns in portrait, a `small` card
/// one of them, a `wide` card both, every card the same height. A tile's
/// widget is drawn inside the card's inset (``XbinRenderOptions/compact``
/// in the renderer); the standard card uses the same box. The app's screen
/// grid (App/Shell/Screens/ScreenView) lays cards out with these numbers —
/// on a 390-point phone a small card is 173 × 170, a wide one 358 × 170
/// (native/spec/tree.md §13).
public enum XbinWidgetMetrics {
    /// A card's height (points), whatever its size — rows of cards line up.
    public static let cardHeight: Double = 170
    /// The card's margin around the widget.
    public static let inset: Double = 14
    public static let cornerRadius: Double = 22
    /// The screen's side margin.
    public static let margin: Double = 16
    /// The gap between cards, across and down.
    public static let spacing: Double = 12

    /// A card's width on a screen `width` points wide.
    public static func cardWidth(_ size: CardSize, screenWidth width: Double) -> Double {
        let full = max(0, width - 2 * margin)
        switch size {
        case .wide: return full
        case .small: return max(0, (full - spacing) / 2)
        }
    }

    /// The widget's own box: the card less its inset.
    public static func contentSize(_ size: CardSize, screenWidth width: Double) -> (width: Double, height: Double) {
        (max(0, cardWidth(size, screenWidth: width) - 2 * inset), max(0, cardHeight - 2 * inset))
    }
}
