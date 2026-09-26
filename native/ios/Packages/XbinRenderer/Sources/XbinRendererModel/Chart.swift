import Foundation
import XbinCore

/// A `chart` node's data, cleaned the way the reference renderer cleans it
/// (web/xb/render-chart.js): points without a finite y (or, for number and
/// time axes, a finite x) are dropped; category x values are their text.
public struct ChartModel: Sendable, Equatable {
    public enum Kind: String, Sendable, CaseIterable { case line, bar, area, spark }
    public enum XKind: String, Sendable, CaseIterable { case time, number, category }
    public enum YKind: String, Sendable, CaseIterable { case number, bytes, percent }

    public enum X: Sendable, Hashable {
        /// `x: time` — ms since the epoch.
        case time(Date)
        case number(Double)
        case category(String)
    }

    public struct Point: Sendable, Hashable {
        public var x: X
        public var y: Double
    }

    public struct Series: Sendable, Equatable {
        public var name: String
        public var points: [Point]
    }

    public var kind: Kind
    public var x: XKind
    public var y: YKind
    /// Points: the `height` token, else 36 for a spark line and 160 others.
    public var height: Double
    public var series: [Series]

    public var isEmpty: Bool { series.allSatisfy { $0.points.isEmpty } }

    /// Whether the chart shows a legend (more than one series, not a spark).
    public var showsLegend: Bool { kind != .spark && series.count > 1 }

    public init(props: Props) {
        kind = Kind(rawValue: props.string("kind") ?? "") ?? .line
        x = XKind(rawValue: props.string("x") ?? "") ?? .number
        y = YKind(rawValue: props.string("y") ?? "") ?? .number
        height = XbinHeight(props.string("height"))?.points ?? (kind == .spark ? 36 : 160)
        let xk = x
        series = props.array("series").map { s in
            let name = Props.text(s["name"]) ?? ""
            let points: [Point] = (s["points"]?.arrayValue ?? []).compactMap { p in
                guard let a = p.arrayValue, a.count >= 2 else { return nil }
                guard let yv = ChartModel.number(a[1]), yv.isFinite else { return nil }
                switch xk {
                case .category:
                    return Point(x: .category(Props.text(a[0]) ?? ""), y: yv)
                case .number:
                    guard let xv = ChartModel.number(a[0]), xv.isFinite else { return nil }
                    return Point(x: .number(xv), y: yv)
                case .time:
                    guard let xv = ChartModel.number(a[0]), xv.isFinite else { return nil }
                    return Point(x: .time(Date(timeIntervalSince1970: xv / 1000)), y: yv)
                }
            }
            return Series(name: name, points: points)
        }
    }

    /// JavaScript's `Number(v)` for the values a point may hold (`null`
    /// and `""` are 0, like the reference renderer's).
    static func number(_ v: JSONValue) -> Double? {
        switch v {
        case .int(let i): return Double(i)
        case .double(let d): return d
        case .string(let s):
            let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
            return t.isEmpty ? 0 : Double(t)
        case .bool(let b): return b ? 1 : 0
        case .null: return 0
        default: return nil
        }
    }

    /// The y range of all points.
    public var yRange: ClosedRange<Double>? {
        let ys = series.flatMap { $0.points.map(\.y) }
        guard let lo = ys.min(), let hi = ys.max() else { return nil }
        return lo...hi
    }
}

/// Axis label formats, the reference renderer's (`fmtY`, `compact`,
/// `bytes`), so both renderers label a chart alike.
public enum ChartFormat {
    /// A y value: `bytes` → `4.3 GB`, `percent` (a fraction) → `42%`, else
    /// compact (`18.4K`, `0.52`).
    public static func y(_ kind: ChartModel.YKind, _ v: Double) -> String {
        switch kind {
        case .bytes: return bytes(v)
        case .percent: return precision(v * 100, 3) + "%"
        case .number: return compact(v)
        }
    }

    /// `18422` → `18.4K`, `0.004` → `4.0e-3`, `0` → `0`.
    public static func compact(_ v: Double) -> String {
        let a = abs(v)
        if a >= 1e12 { return precision(v / 1e12, 3) + "T" }
        if a >= 1e9 { return precision(v / 1e9, 3) + "G" }
        if a >= 1e6 { return precision(v / 1e6, 3) + "M" }
        if a >= 1e3 { return precision(v / 1e3, 3) + "K" }
        if a == 0 { return "0" }
        if a < 0.01 { return exponential(v) }
        return precision(v, 3)
    }

    /// Binary units: `4617089843` → `4.3 GB`.
    public static func bytes(_ v: Double) -> String {
        let units = ["B", "KB", "MB", "GB", "TB", "PB"]
        var i = 0
        var x = abs(v)
        while x >= 1024 && i < units.count - 1 {
            x /= 1024
            i += 1
        }
        let n = i == 0 ? String(Int64(x.rounded())) : precision(x, 3)
        return (v < 0 ? "-" : "") + n + " " + units[i]
    }

    /// JavaScript's `String(+v.toPrecision(p))`: `p` significant digits,
    /// trailing zeros dropped.
    public static func precision(_ v: Double, _ p: Int) -> String {
        guard v.isFinite else { return "" }
        if v == 0 { return "0" }
        let e = Int((log10(abs(v))).rounded(.down))
        let k = p - 1 - e
        // Scale by an exact power of ten in the direction that keeps it
        // exact (1e-9 is not; 1e9 is).
        let r: Double
        if k >= 0 {
            let s = pow(10, Double(k))
            r = (v * s).rounded() / s
        } else {
            let s = pow(10, Double(-k))
            r = (v / s).rounded() * s
        }
        return Props.number(r.isFinite ? r : v)
    }

    /// JavaScript's `v.toExponential(1)`: `0.004` → `4.0e-3`.
    public static func exponential(_ v: Double) -> String {
        guard v.isFinite, v != 0 else { return "0.0e+0" }
        var e = Int((log10(abs(v))).rounded(.down))
        var m = v / pow(10, Double(e))
        m = (m * 10).rounded() / 10
        if abs(m) >= 10 {
            m /= 10
            e += 1
        }
        let mant = String(format: "%.1f", m)
        return mant + "e" + (e < 0 ? "-" : "+") + String(abs(e))
    }
}
