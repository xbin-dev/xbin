#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinRendererModel

/// Colour roles (plans/native.md §10.1, the iOS column): platform semantic
/// colours, with the xbin amber as the tint and a darker amber for text on
/// light backgrounds.
public enum XbinColor {
    /// `accent` fills and the app tint.
    public static let amber = Color(uiColor: UIColor(xbinHex: XbinPalette.amber))
    /// `accentText`: amber text and icons, darkened in light mode (4.7:1).
    public static let accentText = dynamic(light: XbinPalette.amberTextLight, dark: XbinPalette.amberTextDark)
    /// `onAccent`: text on an amber fill.
    public static let onAccent = Color(uiColor: UIColor(xbinHex: XbinPalette.onAccent))
    /// The renderer's tint: amber in dark mode, the darkened `accentText`
    /// amber in light mode — the tint colours text (list buttons, bar items,
    /// back buttons) as well as fills, and plain amber on white is ≈1.9:1.
    public static let tint = accentText
    /// Text on a ``tint`` fill (a prominent button): white on the dark
    /// amber (4.7:1), `onAccent` on amber (8.3:1).
    public static let onTint = Color(uiColor: UIColor { traits in
        traits.userInterfaceStyle == .dark ? UIColor(xbinHex: XbinPalette.onAccent) : UIColor(red: 1, green: 1, blue: 1, alpha: 1)
    })
    /// The user's chat bubble.
    public static let bubble = dynamic(light: XbinPalette.bubbleLight, dark: XbinPalette.bubbleDark)

    /// `bg`, `surface`, `surface2`, `border`, `text`, `muted`.
    public static let background = Color(uiColor: .systemGroupedBackground)
    public static let surface = Color(uiColor: .secondarySystemGroupedBackground)
    public static let surface2 = Color(uiColor: .tertiarySystemGroupedBackground)
    public static let border = Color(uiColor: .separator)
    public static let text = Color(uiColor: .label)
    public static let muted = Color(uiColor: .secondaryLabel)
    /// A neutral fill behind code, fields and the `info` notice.
    public static let fill = Color(uiColor: .tertiarySystemFill)

    /// A `tone`'s colour for text and icons (`accent` → ``accentText``).
    public static func tone(_ t: XbinTone) -> Color {
        switch t {
        case .muted: return muted
        case .accent: return accentText
        case .ok: return Color(uiColor: .systemGreen)
        case .warn: return Color(uiColor: .systemOrange)
        case .danger: return Color(uiColor: .systemRed)
        }
    }

    /// Text in `tone`, or the label colour.
    public static func text(_ t: XbinTone?) -> Color { t.map(tone) ?? text }

    /// Chart series colour `i` (the reference renderer's six, per scheme).
    public static func chart(_ i: Int) -> Color {
        let n = XbinPalette.chartLight.count
        return dynamic(light: XbinPalette.chartLight[i % n], dark: XbinPalette.chartDark[i % n])
    }

    /// A colour that follows the scheme.
    public static func dynamic(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: uiDynamic(light: light, dark: dark))
    }

    /// The same as a UIKit colour (the renderer's UIKit text views).
    static func uiDynamic(light: UInt32, dark: UInt32) -> UIColor {
        UIColor { traits in
            UIColor(xbinHex: traits.userInterfaceStyle == .dark ? dark : light)
        }
    }

    /// ``tint`` for UIKit (a text view's caret and selection).
    @MainActor static let uiTint = uiDynamic(light: XbinPalette.amberTextLight, dark: XbinPalette.amberTextDark)
}

extension UIColor {
    /// An sRGB colour from `0xRRGGBB`.
    convenience init(xbinHex hex: UInt32) {
        let (r, g, b) = XbinPalette.components(hex)
        self.init(red: CGFloat(r), green: CGFloat(g), blue: CGFloat(b), alpha: 1)
    }
}

/// Type roles (§10.2) → Dynamic Type text styles, so every native tile
/// scales with the user's text size.
public enum XbinFont {
    public static func font(_ role: XbinTypeRole) -> Font {
        switch role {
        case .largeTitle: return .largeTitle
        case .title: return .title
        case .title2: return .title2
        case .title3: return .title3
        case .headline: return .headline
        case .body: return .body
        case .callout: return .callout
        case .subheadline: return .subheadline
        case .footnote: return .footnote
        case .caption: return .caption
        case .caption2: return .caption2
        case .mono: return Font.body.monospaced()
        }
    }

    /// Code: monospaced at the footnote size.
    public static let code = Font.system(.footnote, design: .monospaced)
}

/// A capsule with text in a tone (row badges, `badge`, chips).
struct Pill: View {
    let text: String
    var tone: XbinTone?
    var pulse = false
    var small = false

    var body: some View {
        let color = tone.map(XbinColor.tone) ?? XbinColor.muted
        HStack(spacing: 4) {
            if pulse {
                Image(systemName: "circle.fill")
                    .font(.system(size: 6))
                    .symbolEffect(.pulse)
            }
            Text(verbatim: text)
                .font(small ? .caption2.weight(.semibold) : .caption.weight(.semibold))
                .lineLimit(1)
        }
        .padding(.horizontal, small ? 6 : 8)
        .padding(.vertical, small ? 2 : 3)
        .foregroundStyle(color)
        .background(color.opacity(0.15), in: Capsule())
    }
}

/// An SF Symbol for an icon name (§10.4); unknown names draw the
/// placeholder.
struct XbinIconImage: View {
    let name: String?
    var tone: XbinTone?

    var body: some View {
        let symbol = XbinIcons.symbol(name) ?? XbinIcons.placeholder
        Image(systemName: symbol)
            .foregroundStyle(symbol == XbinIcons.placeholder ? XbinColor.muted : XbinColor.text(tone))
            .accessibilityHidden(true)
    }
}
#endif
