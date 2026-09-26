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

/// The xbin palette the iOS colour roles fall back to where the platform
/// has no semantic colour (§10.1): the amber tint and its text variants, as
/// sRGB hex. The SwiftUI layer turns these into dynamic colours.
public enum XbinPalette {
    /// `accent` fills, both schemes (the app's tint).
    public static let amber: UInt32 = 0xF5A623
    /// `accentText` on light backgrounds (4.7:1 on white).
    public static let amberTextLight: UInt32 = 0xA86400
    /// `accentText` on dark backgrounds.
    public static let amberTextDark: UInt32 = 0xF5A623
    /// `onAccent`: text on an amber fill (8.3:1).
    public static let onAccent: UInt32 = 0x1B1E24
    /// The user's chat bubble (the reference renderer's `--xb-bubble`).
    public static let bubbleLight: UInt32 = 0xFDE8C2
    public static let bubbleDark: UInt32 = 0x343A44
    /// Chart series colours in order (the reference's `--xb-chart-1…6`).
    public static let chartLight: [UInt32] = [0xE08E0B, 0x2F6FD6, 0x2E7D32, 0x8E44AD, 0x00838F, 0xC2185B]
    public static let chartDark: [UInt32] = [0xF5A623, 0x5B9CF6, 0x4CAF50, 0xB27EE0, 0x26C6DA, 0xF06292]

    /// (red, green, blue) in 0…1.
    public static func components(_ hex: UInt32) -> (Double, Double, Double) {
        (Double((hex >> 16) & 0xFF) / 255, Double((hex >> 8) & 0xFF) / 255, Double(hex & 0xFF) / 255)
    }
}
