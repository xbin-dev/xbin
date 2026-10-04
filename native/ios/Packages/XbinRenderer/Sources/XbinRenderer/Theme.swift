#if canImport(UIKit)
import SwiftUI
import UIKit
import XbinRendererModel

/// Colour roles (plans/native.md §10.1, the iOS column): Base Two's
/// Concrete Day and Concrete Night (XbinPalette, D185) — concrete surfaces,
/// ink text, the cobalt accent (periwinkle in dark) — each following the
/// appearance. The app's own screens use the same roles.
public enum XbinColor {
    /// `accent` fills, text and icons, and the app's tint.
    public static let accent = shade(XbinPalette.accent)
    /// `accentText`: text and icons in the accent (4.5:1 on the panel in
    /// both appearances, so the accent itself).
    public static let accentText = accent
    /// `onAccent`: text on an accent fill (white on cobalt, ink on
    /// periwinkle).
    public static let onAccent = shade(XbinPalette.accentInk)
    /// The renderer's tint: the accent — list buttons, bar items, back
    /// buttons and fills.
    public static let tint = accent
    /// Text on a ``tint`` fill (a prominent button).
    public static let onTint = onAccent
    /// The user's own chat turns (panel-2, product-ui 8).
    public static let bubble = shade(XbinPalette.panel2)

    /// `bg`, `surface`, `surface2`, `border`, `text`, `muted`: the canvas,
    /// the panel, the inset panel, dividers, ink and muted ink.
    public static let background = shade(XbinPalette.bg)
    public static let surface = shade(XbinPalette.panel)
    public static let surface2 = shade(XbinPalette.panel2)
    public static let border = shade(XbinPalette.border)
    /// Component edges (an outlined button, a field): 3:1 on the canvas.
    public static let borderStrong = shade(XbinPalette.borderStrong)
    public static let text = shade(XbinPalette.text)
    public static let muted = shade(XbinPalette.muted)
    public static let subtle = shade(XbinPalette.subtle)
    /// A neutral fill behind code, fields and the `info` notice.
    public static let fill = shade(XbinPalette.hover)
    public static let selection = shade(XbinPalette.selection)
    public static let focus = shade(XbinPalette.focus)

    /// Status colours (always with a glyph and a word) and their tints.
    public static let ok = shade(XbinPalette.ok)
    public static let warn = shade(XbinPalette.warn)
    public static let danger = shade(XbinPalette.danger)
    public static let info = shade(XbinPalette.info)
    public static let okBackground = shade(XbinPalette.okBg)
    public static let warnBackground = shade(XbinPalette.warnBg)
    public static let dangerBackground = shade(XbinPalette.dangerBg)
    public static let infoBackground = shade(XbinPalette.infoBg)

    /// The part tabs: what a card is (product-ui 3, 10).
    public static let partShell = shade(XbinPalette.partShell)
    public static let partTerminal = shade(XbinPalette.partTerminal)
    public static let partAgent = shade(XbinPalette.partAgent)
    public static let partAdmin = shade(XbinPalette.partAdmin)
    /// The partition marker (D181).
    public static let partition = shade(XbinPalette.partition)

    /// A `tone`'s colour for icons, dots and fills.
    public static func tone(_ t: XbinTone) -> Color {
        switch t {
        case .muted: return muted
        case .accent: return accent
        case .ok: return ok
        case .warn: return warn
        case .danger: return danger
        }
    }

    /// A `tone`'s colour for text: Base Two's status colours hold 4.5:1 as
    /// text on the panel in both appearances, so the same as ``tone(_:)``.
    public static func toneText(_ t: XbinTone) -> Color { tone(t) }

    /// A `tone`'s tint, behind a badge or a notice.
    public static func toneBackground(_ t: XbinTone?) -> Color {
        switch t {
        case .ok?: return okBackground
        case .warn?: return warnBackground
        case .danger?: return dangerBackground
        case .accent?: return selection
        case .muted?, nil: return infoBackground
        }
    }

    /// Text in `tone`, or the label colour.
    public static func text(_ t: XbinTone?) -> Color { t.map(toneText) ?? text }

    /// Chart series colour `i` (the reference renderer's six, per scheme).
    public static func chart(_ i: Int) -> Color {
        let n = XbinPalette.chartLight.count
        return dynamic(light: XbinPalette.chartLight[i % n], dark: XbinPalette.chartDark[i % n])
    }

    /// A palette entry as a colour that follows the scheme.
    public static func shade(_ s: XbinShade) -> Color { dynamic(light: s.day, dark: s.night) }

    /// A colour that follows the scheme.
    public static func dynamic(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: uiDynamic(light: light, dark: dark))
    }

    /// The same as a UIKit colour (the renderer's UIKit text views, the
    /// app's UIKit chrome).
    public static func uiDynamic(light: UInt32, dark: UInt32) -> UIColor {
        UIColor { traits in
            UIColor(xbinHex: traits.userInterfaceStyle == .dark ? dark : light)
        }
    }

    /// A palette entry for UIKit.
    public static func uiShade(_ s: XbinShade) -> UIColor { uiDynamic(light: s.day, dark: s.night) }

    /// ``tint`` for UIKit (a text view's caret and selection).
    @MainActor public static let uiTint = uiShade(XbinPalette.accent)
}

extension UIColor {
    /// An sRGB colour from `0xRRGGBB`.
    public convenience init(xbinHex hex: UInt32) {
        let (r, g, b) = XbinPalette.components(hex)
        self.init(red: CGFloat(r), green: CGFloat(g), blue: CGFloat(b), alpha: 1)
    }
}

extension Color {
    /// An sRGB colour from `0xRRGGBB` (the same in both appearances: the
    /// mark's fields, the terminal's palette).
    public init(xbinHex hex: UInt32) {
        self.init(uiColor: UIColor(xbinHex: hex))
    }
}

/// Type roles (§10.2) → Dynamic Type text styles, so every native tile
/// scales with the user's text size. Body and controls are the system font;
/// the large title is Bricolage Grotesque 800 and `mono` JetBrains Mono
/// (XbinFaces), both scaling with the text style they stand for.
public enum XbinFont {
    public static func font(_ role: XbinTypeRole) -> Font {
        switch role {
        case .largeTitle: return display(.largeTitle)
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
        case .mono: return mono(.body)
        }
    }

    /// Bricolage Grotesque 800 at a text style's size, scaling with it.
    public static func display(_ style: Font.TextStyle, size: CGFloat? = nil) -> Font {
        .custom(XbinFaces.display, size: size ?? pointSize(style), relativeTo: style)
    }

    /// JetBrains Mono at a text style's size, scaling with it.
    public static func mono(_ style: Font.TextStyle, size: CGFloat? = nil) -> Font {
        .custom(XbinFaces.mono, size: size ?? pointSize(style), relativeTo: style)
    }

    /// Code: JetBrains Mono at the footnote size.
    public static let code = mono(.footnote)

    /// A UIKit font in a bundled face (the terminal, the nav bar's large
    /// title), or the system's when the face isn't registered.
    public static func uiFont(_ face: String, size: CGFloat, fallbackWeight: UIFont.Weight = .regular,
                              monospaced: Bool = false) -> UIFont {
        if let f = UIFont(name: face, size: size) { return f }
        return monospaced ? .monospacedSystemFont(ofSize: size, weight: fallbackWeight) : .systemFont(ofSize: size, weight: fallbackWeight)
    }

    /// iOS's default ("Large") point size of a text style.
    static func pointSize(_ style: Font.TextStyle) -> CGFloat {
        switch style {
        case .largeTitle: return 34
        case .title: return 28
        case .title2: return 22
        case .title3: return 20
        case .headline, .body: return 17
        case .callout: return 16
        case .subheadline: return 15
        case .footnote: return 13
        case .caption: return 12
        case .caption2: return 11
        @unknown default: return 17
        }
    }
}

extension Shape where Self == RoundedRectangle {
    /// Base Two's shape for custom cards, plates, badges and buttons: 2 pt
    /// corners (XbinShapes).
    public static var xbinPlate: RoundedRectangle { RoundedRectangle(cornerRadius: XbinShapes.radius, style: .circular) }
}

/// A part tab: the 3 pt rule along a card's top edge that says what the
/// card is — green a terminal, magenta an agent session, yellow admin,
/// cobalt the shell (product-ui 3, 10). Never a state.
public enum XbinPart: Sendable {
    case shell, terminal, agent, admin

    public var color: Color {
        switch self {
        case .shell: return XbinColor.partShell
        case .terminal: return XbinColor.partTerminal
        case .agent: return XbinColor.partAgent
        case .admin: return XbinColor.partAdmin
        }
    }
}

extension View {
    /// The part tab along the top edge (inside the view's bounds).
    public func xbinPartTab(_ part: XbinPart?) -> some View {
        overlay(alignment: .top) {
            if let part {
                Rectangle().fill(part.color).frame(height: XbinPalette.partTabHeight).accessibilityHidden(true)
            }
        }
    }

    /// Base Two's primary button (product-ui 6): the accent fill, the
    /// accent ink, square corners — one per screen.
    public func xbinPrimary() -> some View {
        buttonStyle(.borderedProminent)
            .buttonBorderShape(.roundedRectangle(radius: XbinShapes.radius))
            .foregroundStyle(XbinColor.onAccent)
    }

    /// Base Two's secondary button: bordered, square corners.
    public func xbinSecondary() -> some View {
        buttonStyle(.bordered)
            .buttonBorderShape(.roundedRectangle(radius: XbinShapes.radius))
    }

    /// Base Two's concrete behind a list or form (D185): the canvas shows
    /// where iOS's grouped grey was; the rows stay on the panel. The app's
    /// panels sit on it (PanelStack); a sheet's or a navigation page's own
    /// list takes it here.
    public func concreteBackground() -> some View {
        scrollContentBackground(.hidden).background(XbinColor.background)
    }

    /// A Base Two card: the panel, 2 pt corners, a hairline edge.
    public func xbinCard(_ fill: Color = XbinColor.surface, edge: Bool = true) -> some View {
        background(fill, in: .xbinPlate)
            .overlay { if edge { RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1) } }
            .clipShape(.xbinPlate)
    }
}

/// A square badge with text in a tone (row badges, `badge`, chips): Base
/// Two's badges are square, the tone's tint behind the word.
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
                    .foregroundStyle(color)
            }
            Text(verbatim: text)
                .font(small ? .caption2.weight(.semibold) : .caption.weight(.semibold))
                .lineLimit(1)
                .foregroundStyle(tone.map(XbinColor.toneText) ?? XbinColor.muted)
        }
        .padding(.horizontal, small ? 6 : 8)
        .padding(.vertical, small ? 2 : 3)
        .background(color.opacity(0.15), in: .xbinPlate)
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
            .foregroundStyle(symbol == XbinIcons.placeholder ? XbinColor.muted : tone.map(XbinColor.tone) ?? XbinColor.text)
            .accessibilityHidden(true)
    }
}
#endif
