import Foundation
import XbinCore

/// Line breaking for a wrapping horizontal `stack` (`wrap`): the SwiftUI
/// `Layout` asks this where each subview goes. Pure arithmetic, so it is
/// tested on Linux.
public enum FlowLayoutMath {
    public struct Placement: Sendable, Equatable {
        /// Top-leading origin of each item, in order.
        public var origins: [(x: Double, y: Double)]
        /// The size the whole flow takes.
        public var width: Double
        public var height: Double

        public static func == (a: Placement, b: Placement) -> Bool {
            a.width == b.width && a.height == b.height && a.origins.count == b.origins.count
                && zip(a.origins, b.origins).allSatisfy { $0.x == $1.x && $0.y == $1.y }
        }
    }

    /// Places `sizes` (width, height) left to right, starting a new line
    /// when the next item would pass `maxWidth` (an item wider than the
    /// line gets a line of its own). Items on a line are vertically
    /// centred. `maxWidth` nil: one line.
    public static func place(_ sizes: [(w: Double, h: Double)], maxWidth: Double?, spacing: Double,
                             lineSpacing: Double) -> Placement {
        var origins: [(x: Double, y: Double)] = []
        var lines: [[Int]] = [[]]
        var x = 0.0
        let limit = maxWidth ?? .infinity
        for (i, s) in sizes.enumerated() {
            if !lines[lines.count - 1].isEmpty, x + spacing + s.w > limit {
                lines.append([])
                x = 0
            }
            if !lines[lines.count - 1].isEmpty { x += spacing }
            lines[lines.count - 1].append(i)
            x += s.w
        }
        origins = Array(repeating: (0, 0), count: sizes.count)
        var y = 0.0
        var width = 0.0
        for (li, line) in lines.enumerated() where !line.isEmpty {
            let h = line.map { sizes[$0].h }.max() ?? 0
            var cx = 0.0
            for (j, i) in line.enumerated() {
                if j > 0 { cx += spacing }
                origins[i] = (cx, y + (h - sizes[i].h) / 2)
                cx += sizes[i].w
            }
            width = max(width, cx)
            y += h
            if li < lines.count - 1 { y += lineSpacing }
        }
        return Placement(origins: origins, width: width, height: y)
    }
}

/// A `nav`'s stack as SwiftUI's `NavigationStack(path:)` sees it: the
/// screens after the root that are still shown. The user popping hides
/// screens at once (renderer state) and reports `pop {depth}` — the number
/// of screens that remain — and the tile then drops them from the tree.
public enum NavStack {
    /// The pushed screen keys shown: `screens` minus the root, minus the
    /// last `popped` ones the user already left.
    public static func path(_ screens: [String], popped: Int) -> [String] {
        guard screens.count > 1 else { return [] }
        let depth = max(1, screens.count - max(0, popped))
        return Array(screens[1..<depth])
    }

    /// The user left screens: from path length `old` to `new`. Returns the
    /// new popped count and the `pop` depth to report (nil: nothing left).
    public static func pop(screens: Int, popped: Int, newPathCount: Int) -> (popped: Int, depth: Int)? {
        let shown = max(1, screens - popped)
        let remaining = newPathCount + 1
        guard remaining < shown else { return nil }
        return (screens - remaining, remaining)
    }
}

/// The text values of `field kind=date|time` (the web's input values:
/// `YYYY-MM-DD`, `HH:MM`) ↔ dates for a native picker, in a fixed calendar
/// and time zone so the value never shifts.
public enum FieldValue {
    static var calendar: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    /// `2026-09-21` → that day (00:00 UTC); nil when not a date.
    public static func date(_ s: String) -> Date? {
        let p = s.split(separator: "-").compactMap { Int($0) }
        guard p.count == 3, (1...12).contains(p[1]), (1...31).contains(p[2]) else { return nil }
        return calendar.date(from: DateComponents(year: p[0], month: p[1], day: p[2]))
    }

    /// A day → `YYYY-MM-DD`.
    public static func dateString(_ d: Date) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    /// `02:30` (or `02:30:15`) → 1970-01-01 02:30 UTC; nil when not a time.
    public static func time(_ s: String) -> Date? {
        let p = s.split(separator: ":").compactMap { Int($0) }
        guard p.count >= 2, (0...23).contains(p[0]), (0...59).contains(p[1]) else { return nil }
        return calendar.date(from: DateComponents(year: 1970, month: 1, day: 1, hour: p[0], minute: p[1]))
    }

    /// A time → `HH:MM`.
    public static func timeString(_ d: Date) -> String {
        let c = calendar.dateComponents([.hour, .minute], from: d)
        return String(format: "%02d:%02d", c.hour ?? 0, c.minute ?? 0)
    }

    /// The UTC calendar the pickers must use (so what they show is the
    /// value's own wall-clock day/time).
    public static var pickerTimeZone: TimeZone { TimeZone(identifier: "UTC")! }
}
