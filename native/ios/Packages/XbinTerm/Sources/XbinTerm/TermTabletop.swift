// TermTabletop.swift — the terminal on a partially folded iPhone Duo
// (plans/native.md §12 "Duo tabletop posture: the terminal fills the upper
// half, the keyboard the lower half"; §15). The fold comes from the iOS 27.1
// SDK's reserved regions (the active `.division` region's frame, margins
// included; it is active only while the device is partially folded), never
// from the hinge angle — Apple's guidance is regions for layout, the hinge for
// effects. The app compiles that call only with XBIN_SDK_27_1; everything here
// is the arithmetic, tested on Linux.

import Foundation

/// A rectangle in the terminal screen's coordinates (points, y down).
public struct TermRect: Equatable, Sendable {
    public var x, y, width, height: Double
    public init(x: Double, y: Double, width: Double, height: Double) {
        self.x = x; self.y = y; self.width = width; self.height = height
    }
    public var minY: Double { y }
    public var maxY: Double { y + height }
    public var minX: Double { x }
    public var maxX: Double { x + width }
}

public enum TermPosture: String, Equatable, Sendable {
    /// No active fold: one full-screen terminal.
    case flat
    /// A horizontal fold (the device stands like a small laptop): the
    /// terminal above it, keys and keyboard below.
    case tabletop
    /// A vertical fold (held like a book): still one full-screen terminal —
    /// more columns, never a second pane (§12).
    case book

    /// The posture a fold implies (nil or empty: flat).
    public static func of(fold: TermRect?) -> TermPosture {
        guard let f = fold, f.width > 0 || f.height > 0 else { return .flat }
        return f.width > f.height ? .tabletop : .book
    }
}

/// Where things go on the terminal screen.
public struct TermTabletopLayout: Equatable, Sendable {
    public var posture: TermPosture
    /// The terminal's height from the top: down to the fold in tabletop,
    /// down to the keyboard otherwise.
    public var terminalHeight: Double
    /// Tabletop: the key panel just below the fold (height 0: none fits).
    public var panel: TermRect
    /// Tabletop: how many rows of `TermTabletopKeys` fit in the panel.
    public var keyRows: Int

    /// The layout for a screen of `width`×`height` with the active division
    /// region `fold` (nil: none) and the software keyboard covering
    /// `keyboardHeight` at the bottom (its accessory row included; 0 when
    /// hidden or a hardware keyboard is in use).
    public static func compute(width: Double, height: Double, fold: TermRect?, keyboardHeight: Double,
                               keyRowHeight: Double = 44, maxKeyRows: Int = 4) -> TermTabletopLayout {
        let posture = TermPosture.of(fold: fold)
        let visible = max(0, height - max(0, keyboardHeight))
        // A fold off the screen, or a keyboard reaching above it: the
        // terminal gives way to the keyboard as when flat.
        guard posture == .tabletop, let f = fold, f.minY > 0, f.maxY < height, visible >= f.minY else {
            return TermTabletopLayout(posture: posture == .tabletop ? .flat : posture, terminalHeight: visible,
                                      panel: TermRect(x: 0, y: visible, width: width, height: 0), keyRows: 0)
        }
        let top = f.maxY
        let bottom = max(top, visible)
        let rows = keyRowHeight > 0 ? min(maxKeyRows, Int(((bottom - top) / keyRowHeight).rounded(.down))) : 0
        return TermTabletopLayout(posture: .tabletop, terminalHeight: f.minY,
                                  panel: TermRect(x: 0, y: top, width: width, height: bottom - top),
                                  keyRows: max(0, rows))
    }
}

extension TermTabletopLayout {
    /// A stand-in fold for checking the layout without a Duo (the app's
    /// `XbinForceTabletop` launch argument): mid-screen, 40 pt like the Duo's
    /// division region with its margins.
    public static func simulatedFold(width: Double, height: Double) -> TermRect {
        TermRect(x: 0, y: (height / 2 - 20).rounded(.down), width: width, height: 40)
    }
}

/// The key panel below the fold: the rows a terminal wants that the
/// accessory row leaves out, most useful first. With the keyboard hidden the
/// accessory row itself isn't shown, so it comes first.
public enum TermTabletopKeys {
    public static let navigation: [AccessoryKey] = [
        .key(.home), .key(.end), .key(.pageUp), .key(.pageDown), .key(.insert), .key(.delete),
    ]
    public static let functionLow: [AccessoryKey] = (1...6).map { .key(.function($0)) }
    public static let functionHigh: [AccessoryKey] = (7...12).map { .key(.function($0)) }

    /// The first `count` rows. `keyboardShown`: the accessory row rides on
    /// the keyboard; otherwise the panel starts with it (and `slot`).
    public static func rows(count: Int, keyboardShown: Bool, slot: AccessoryKey?) -> [[AccessoryKey]] {
        var all: [[AccessoryKey]] = []
        if !keyboardShown {
            var row = AccessoryKey.defaultRow
            if let slot { row.append(slot) }
            all.append(row)
        }
        all += [navigation, functionLow, functionHigh]
        return Array(all.prefix(max(0, count)))
    }
}
