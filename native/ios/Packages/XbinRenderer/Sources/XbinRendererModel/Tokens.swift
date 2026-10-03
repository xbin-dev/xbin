import Foundation

// Theme tokens (plans/native.md §10). Tiles name roles; the SwiftUI layer
// maps each to a system colour, a Dynamic Type style or a length. Every enum
// parses leniently: an unknown value is nil and the renderer draws its
// default (the runtime has already sent a `bad-token` diagnostic).

/// `tone` (§10.1): colour roles for text, icons and badges.
public enum XbinTone: String, CaseIterable, Sendable {
    case muted, accent, ok, warn, danger

    public init?(_ raw: String?) {
        guard let raw, let t = XbinTone(rawValue: raw) else { return nil }
        self = t
    }
}

/// `notice` `tone`: the tones plus `info`, a neutral surface.
public enum XbinNoticeTone: String, CaseIterable, Sendable {
    case info, muted, accent, ok, warn, danger

    public init?(_ raw: String?) {
        guard let raw, let t = XbinNoticeTone(rawValue: raw) else { return nil }
        self = t
    }

    /// The SF Symbol the notice leads with.
    public var symbol: String {
        switch self {
        case .info, .muted: return "info.circle"
        case .accent: return "sparkles"
        case .ok: return "checkmark.circle"
        case .warn: return "exclamationmark.triangle"
        case .danger: return "exclamationmark.octagon"
        }
    }

    /// The tone its colour comes from (nil: neutral).
    public var tone: XbinTone? {
        switch self {
        case .info: return nil
        case .muted: return .muted
        case .accent: return .accent
        case .ok: return .ok
        case .warn: return .warn
        case .danger: return .danger
        }
    }
}

/// `style` of `text` (§10.2): Dynamic Type text styles of the same names;
/// `mono` is the body style, monospaced.
public enum XbinTypeRole: String, CaseIterable, Sendable {
    case largeTitle, title, title2, title3, headline, body, callout, subheadline, footnote, caption, caption2, mono

    public init?(_ raw: String?) {
        guard let raw, let t = XbinTypeRole(rawValue: raw) else { return nil }
        self = t
    }
}

/// `stack` `gap` (§10.3), in points.
public enum XbinGap: String, CaseIterable, Sendable {
    case none, xs, s, m, l, xl, xxl

    public init?(_ raw: String?) {
        guard let raw, let t = XbinGap(rawValue: raw) else { return nil }
        self = t
    }

    public var points: Double {
        switch self {
        case .none: return 0
        case .xs: return 4
        case .s: return 8
        case .m: return 12
        case .l: return 16
        case .xl: return 24
        case .xxl: return 32
        }
    }

    /// The gap a stack uses without a `gap` prop (the reference renderer's:
    /// 8 across, 12 down).
    public static func standard(horizontal: Bool) -> Double { horizontal ? 8 : 12 }
}

/// `height` of `image`, `chart` and `canvas`, in points (the reference
/// renderer's 48/96/160/240/360).
public enum XbinHeight: String, CaseIterable, Sendable {
    case xs, s, m, l, xl

    public init?(_ raw: String?) {
        guard let raw, let t = XbinHeight(rawValue: raw) else { return nil }
        self = t
    }

    public var points: Double {
        switch self {
        case .xs: return 48
        case .s: return 96
        case .m: return 160
        case .l: return 240
        case .xl: return 360
        }
    }
}

/// One colour in both appearances: Concrete Day (light) and Concrete Night
/// (dark), as sRGB hex.
public struct XbinShade: Sendable, Equatable {
    public let day: UInt32
    public let night: UInt32

    public init(_ day: UInt32, _ night: UInt32) {
        self.day = day
        self.night = night
    }

    /// The same colour in both.
    public init(both: UInt32) { self.init(both, both) }

    public func value(dark: Bool) -> UInt32 { dark ? night : day }
}

/// The app's palette: Base Two's product tokens (D184, D185), Concrete Day
/// and Concrete Night — web/theme.css's values, which the reference
/// renderer's table (web/xb/render-theme.js) equals where it names the same
/// role (ThemeParityTests). The SwiftUI layer (XbinColor) turns each into a
/// colour that follows the appearance. Status always comes with a glyph and
/// a word; the part colours say what a card is (a 3 pt rule), never a state.
public enum XbinPalette {
    // Surfaces, back to front.
    /// The canvas (`--bx-bg`): behind everything.
    public static let bg = XbinShade(0xE8E9EE, 0x0B0C12)
    /// Cards, rows, bars (`--bx-panel`; Night's is lifted one step, B2 Q4).
    public static let panel = XbinShade(0xFFFFFF, 0x1F2028)
    /// Inset panels and fields (`--bx-panel-2`); a person's own chat turns.
    public static let panel2 = XbinShade(0xF7F8FA, 0x262730)
    /// A neutral fill: a code well, a pressed row (`--bx-hover`).
    public static let hover = XbinShade(0xEEF0F4, 0x2A2B34)
    public static let selection = XbinShade(0xDDE2FF, 0x262C5C)
    /// Dividers (`--bx-border`).
    public static let border = XbinShade(0xCDD0D8, 0x33353F)
    /// Component edges, 3:1 on the canvas (`--bx-border-strong`).
    public static let borderStrong = XbinShade(0x7E8194, 0x666A7E)

    // Text.
    public static let text = XbinShade(0x0B0C12, 0xE9EAF0)
    public static let muted = XbinShade(0x4B4D5C, 0xA3A6B6)
    /// Placeholders, tertiary text (`--bx-subtle`).
    public static let subtle = XbinShade(0x626576, 0x8E91A2)

    /// The accent — actions, selection, links (B2 Q6): cobalt, periwinkle
    /// in Night; as text and icons it holds 4.5:1 on the panel.
    public static let accent = XbinShade(0x1F3DFF, 0x8C9BFF)
    /// Text on an accent fill (`--bx-accent-ink`).
    public static let accentInk = XbinShade(0xFFFFFF, 0x0B0C12)
    /// The keyboard focus ring (`--bx-focus`).
    public static let focus = XbinShade(0x0086A6, 0x3DD6F5)

    // Status: text and icons, and their tints (12 % over the panel).
    public static let ok = XbinShade(0x436C0C, 0xA3CF5E)
    public static let okBg = XbinShade(0xEEF5E1, 0x2F352E)
    public static let warn = XbinShade(0x9A4A06, 0xF2994A)
    public static let warnBg = XbinShade(0xFBEEDF, 0x382F2C)
    public static let danger = XbinShade(0xC81E1E, 0xFF7A7A)
    public static let dangerBg = XbinShade(0xFBE7E7, 0x3A2B32)
    public static let info = XbinShade(0x3D4A5C, 0xA9B4C6)
    public static let infoBg = XbinShade(0xECEEF2, 0x30323B)

    /// The partition marker (yours/shared/global, D181; `--bx-part`), not a
    /// part colour.
    public static let partition = XbinShade(0x1F8778, 0x3FB5A3)

    // The part tabs (product-ui 3, 10): a 3 pt rule on a card says what it
    // is. The same in both themes, except that cobalt lifts in Night.
    public static let partShell = XbinShade(0x1F3DFF, 0x3350FF)
    public static let partTerminal = XbinShade(both: 0x00A86B)
    public static let partAgent = XbinShade(both: 0xDB0072)
    public static let partAdmin = XbinShade(both: 0xFFD000)
    /// The part tab's thickness, in points.
    public static let partTabHeight = 3.0
    /// Elevated modes (admin rights in force): the yellow field and its ink.
    public static let elevated = XbinShade(both: 0xFFD000)
    public static let elevatedInk = XbinShade(both: 0x0B0C12)

    // The mark (plans/brand/marks): the same in both themes.
    public static let markTile: UInt32 = 0x1F3DFF
    public static let markB: UInt32 = 0xFFFFFF
    public static let markX: UInt32 = 0xFFD000

    /// The brand's colour fields and the ink set on each (`--bx-field-*`),
    /// in the doubling order (yellow ×1, green ×2, magenta ×4, cobalt ×8):
    /// the first run's stair only, never behind content (product-ui 4, 10).
    public enum Field {
        public static let yellow: UInt32 = 0xFFD000, yellowInk: UInt32 = 0x0B0C12
        public static let green: UInt32 = 0x00A86B, greenInk: UInt32 = 0x0B0C12
        public static let magenta: UInt32 = 0xDB0072, magentaInk: UInt32 = 0xFFFFFF
        public static let cobalt: UInt32 = 0x1F3DFF, cobaltInk: UInt32 = 0xFFFFFF
    }

    /// Chart series in order: the terminal's ANSI order (blue, magenta,
    /// cyan, green, yellow, red), the reference's `--xb-chart-1…6`.
    public static let chartLight: [UInt32] = [0x1F3DFF, 0xB0005C, 0x0E7490, 0x00794A, 0x8A6100, 0xC81E1E]
    public static let chartDark: [UInt32] = [0x6F86FF, 0xFF5FB0, 0x4FC3DC, 0x4CD69B, 0xFFD54A, 0xFF6B6B]

    /// The terminal: Concrete Night's terminal palette in both appearances
    /// (product-ui 7; the web terminal's "Concrete Night") — the phone's
    /// terminal stays dark, with its dark keyboard and key row.
    public enum Terminal {
        public static let background: UInt32 = 0x0B0C12
        public static let foreground: UInt32 = 0xE6E7EE
        public static let cursor: UInt32 = 0x8C9BFF
        public static let cursorInk: UInt32 = 0x0B0C12
        public static let selection: UInt32 = 0x262C5C
        /// A bar over the terminal (Night's panel) and its text (Night's
        /// muted).
        public static let bar: UInt32 = 0x1F2028
        public static let barText: UInt32 = 0xA3A6B6
        /// The key row's keys (Night's panel-2 step up from the bar).
        public static let key: UInt32 = 0x33353F
        /// The 16 ANSI colours: black, red, green, yellow, blue, magenta,
        /// cyan, white, then the bright eight.
        public static let ansi: [UInt32] = [
            0x1C1D26, 0xFF6B6B, 0x4CD69B, 0xFFD54A, 0x6F86FF, 0xFF5FB0, 0x4FC3DC, 0xC9CBD6,
            0x5C5F70, 0xFF8F8F, 0x7BE6B6, 0xFFE27A, 0x96A6FF, 0xFF8CC8, 0x85DCEC, 0xFFFFFF,
        ]
    }

    /// (red, green, blue) in 0…1.
    public static func components(_ hex: UInt32) -> (Double, Double, Double) {
        (Double((hex >> 16) & 0xFF) / 255, Double((hex >> 8) & 0xFF) / 255, Double(hex & 0xFF) / 255)
    }

    /// The WCAG contrast ratio of two colours (1…21).
    public static func contrast(_ a: UInt32, _ b: UInt32) -> Double {
        func luminance(_ hex: UInt32) -> Double {
            let (r, g, b) = components(hex)
            func linear(_ c: Double) -> Double { c <= 0.03928 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4) }
            return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b)
        }
        let (x, y) = (luminance(a), luminance(b))
        return (max(x, y) + 0.05) / (min(x, y) + 0.05)
    }
}

/// Base Two's shapes (D184, D185): custom cards, plates, badges and buttons
/// have square corners, 2 pt. Native controls (bars, sheets, lists,
/// switches, segmented controls) keep the system's.
public enum XbinShapes {
    public static let radius = 2.0
}

/// The app's own glyphs: the workspace's drawn glyph names
/// (/vendor/bx-icons.js, D184 — what the web's view models carry as `icon`,
/// e.g. XbinCore's DeployView.Glyph) → the SF Symbol of the same meaning
/// and, where SF Symbols has it, the same shape. The app draws SF Symbols,
/// not the web's drawings: they scale and weigh with Dynamic Type and sit
/// with the system's bars, menus and lists (D185). Status always pairs its
/// glyph with a word and the status colour. (A tile's own icon names are
/// the vocabulary's, XbinIcons.)
public enum XbinGlyphs {
    public static let symbols: [String: String] = [
        "ok": "checkmark.square", "warning": "exclamationmark.triangle", "error": "exclamationmark.octagon",
        "info": "info.square", "wait": "hourglass", "live": "square.fill", "pin": "pin", "refresh": "arrow.clockwise",
        "deploy": "arrow.up.to.line", "shield": "shield", "branch": "arrow.triangle.branch", "terminal": "apple.terminal",
        // The agents' glyph is two linked squares (product-ui 8: no faces,
        // robots or sparkles); square.on.square is SF Symbols' nearest.
        "agent": "square.on.square", "plug": "powerplug", "lock": "lock", "globe": "globe",
    ]

    /// The SF Symbol for a glyph name; the info square for an unknown one.
    public static func symbol(_ name: String) -> String { symbols[name] ?? "info.square" }

    /// A status level's glyph and word (the web shell's STATUS: ok, info,
    /// warn, error).
    public static func status(_ level: String) -> (symbol: String, word: String) {
        switch level {
        case "ok": return (symbols["ok"]!, "OK")
        case "warn", "warning": return (symbols["warning"]!, "Warning")
        case "error", "danger", "failed": return (symbols["error"]!, "Error")
        default: return (symbols["info"]!, "Info")
        }
    }
}

/// The faces the app bundles (App/Resources/Fonts, SIL OFL 1.1, the
/// workspace's own: web/vendor/fonts): large titles in Bricolage Grotesque
/// 800 (the one brand flourish, product-ui 10), terminals and code in
/// JetBrains Mono; everything else is the system font, so Dynamic Type
/// works. PostScript names, as UIFont and Font.custom take them; a face
/// that isn't registered falls back to the system's.
public enum XbinFaces {
    public static let display = "BricolageGrotesque96ptExtraBold-ExtraBold"
    public static let mono = "JetBrainsMono-Regular"
    public static let monoBold = "JetBrainsMono-Bold"
    /// The files, under App/Resources/Fonts (Info.plist's UIAppFonts).
    public static let files = ["BricolageGrotesque-ExtraBold.ttf", "JetBrainsMono-Regular.ttf", "JetBrainsMono-Bold.ttf"]
}
