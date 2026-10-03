import Foundation
import XbinCore

/// A phone screen's cards (D125): two columns in portrait, a `small` card
/// one of them, a `wide` card both, every card the same height. A tile's
/// widget is drawn inside the card's inset (``XbinRenderOptions/compact``
/// in the renderer); the standard card uses the same box. The app's screen
/// grid (App/Shell/Screens/ScreenView) lays cards out with these numbers —
/// on a 390-point phone a small card is 177 × 132, a wide one 366 × 132
/// (native/spec/tree.md §13; D128 made the grid compact, from 173 × 170).
/// Larger text grows the cards (D128): the height scales as body text does
/// above the default size, so a widget's controls stay inside its card.
public enum XbinWidgetMetrics {
    /// A card's height (points) at the default text size and below,
    /// whatever its size — rows of cards line up.
    public static let cardHeight: Double = 132

    /// A card's height when body text is `scale` times its default size
    /// (Dynamic Type; UIFontMetrics' factor): 132 pt up to the default,
    /// then growing with the text — large text grows the card instead of
    /// clipping the widget in it.
    public static func cardHeight(textScale scale: Double) -> Double {
        (cardHeight * max(1, scale)).rounded()
    }
    /// The card's margin around the widget.
    public static let inset: Double = 10
    /// Base Two: square corners (XbinShapes, D185).
    public static let cornerRadius: Double = XbinShapes.radius
    /// The screen's side margin.
    public static let margin: Double = 12
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

    /// The widget's own box: the card less its inset (`textScale`: see
    /// ``cardHeight(textScale:)``).
    public static func contentSize(_ size: CardSize, screenWidth width: Double, textScale: Double = 1) -> (width: Double, height: Double) {
        (max(0, cardWidth(size, screenWidth: width) - 2 * inset), max(0, cardHeight(textScale: textScale) - 2 * inset))
    }
}
