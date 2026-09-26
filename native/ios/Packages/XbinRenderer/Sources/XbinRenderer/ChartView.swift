#if canImport(UIKit)
import Charts
import SwiftUI
import XbinRendererModel

/// `chart` with Swift Charts: line, bar, area (unstacked, with its line) or
/// a bare spark line; x as time, number or category; y labelled as number,
/// bytes or percent exactly as the reference renderer labels it
/// (``ChartFormat``); series colours in the reference's order.
public struct XbinChart: View {
    public let model: ChartModel

    public init(model: ChartModel) { self.model = model }

    public var body: some View {
        if model.isEmpty {
            Text("no data")
                .font(.footnote)
                .foregroundStyle(XbinColor.muted)
                .frame(maxWidth: .infinity)
                .frame(height: CGFloat(model.height))
        } else {
            let names = Self.seriesNames(model)
            let spark = model.kind == .spark
            let yKind = model.y
            let chart = Chart {
                ForEach(Array(model.series.enumerated()), id: \.offset) { si, series in
                    ForEach(Array(series.points.enumerated()), id: \.offset) { _, point in
                        ChartMarks(kind: model.kind, point: point, series: names[si])
                    }
                }
            }
            .chartForegroundStyleScale(domain: names, range: names.indices.map { XbinColor.chart($0) })
            .chartLegend(model.showsLegend ? .visible : .hidden)
            let ticks = model.yTicks
            Group {
                if spark || ticks.count < 2 {
                    chart.chartXAxis(.hidden).chartYAxis(.hidden)
                } else {
                    // The reference's y ticks (round values in the data's
                    // unit) and domain; a few x labels, since a time or
                    // number axis is labelled only at its ends and middle
                    // there.
                    chart
                        .chartYScale(domain: ticks[0]...ticks[ticks.count - 1])
                        .chartYAxis {
                            AxisMarks(values: ticks) { value in
                                AxisGridLine()
                                AxisValueLabel {
                                    if let v = value.as(Double.self) { Text(verbatim: ChartFormat.y(yKind, v)) }
                                }
                            }
                        }
                        .modifier(XAxisMarks(categories: model.x == .category))
                }
            }
            .frame(height: CGFloat(model.height))
            // Axis labels grow with the text size only so far: at the
            // accessibility sizes they would squeeze the plot to nothing
            // and truncate every label.
            .dynamicTypeSize(...DynamicTypeSize.xxLarge)
            .accessibilityLabel(Text(verbatim: "\(model.kind.rawValue) chart: " + names.joined(separator: ", ")))
        }
    }

    /// Unique legend names ("" and duplicates get their index).
    static func seriesNames(_ m: ChartModel) -> [String] {
        var seen = Set<String>()
        return m.series.enumerated().map { i, s in
            var n = s.name.isEmpty ? "series \(i + 1)" : s.name
            if seen.contains(n) { n += " (\(i + 1))" }
            seen.insert(n)
            return n
        }
    }
}

/// A time or number x axis with about three labels; categories keep the
/// automatic marks.
private struct XAxisMarks: ViewModifier {
    let categories: Bool

    func body(content: Content) -> some View {
        if categories {
            content
        } else {
            content.chartXAxis {
                AxisMarks(values: .automatic(desiredCount: 3))
            }
        }
    }
}

/// The marks for one point.
private struct ChartMarks: ChartContent {
    let kind: ChartModel.Kind
    let point: ChartModel.Point
    let series: String

    var body: some ChartContent {
        switch point.x {
        case .time(let d):
            PointMarks(kind: kind, x: PlottableValue.value("x", d), y: point.y, series: series)
        case .number(let n):
            PointMarks(kind: kind, x: PlottableValue.value("x", n), y: point.y, series: series)
        case .category(let c):
            PointMarks(kind: kind, x: PlottableValue.value("x", c), y: point.y, series: series)
        }
    }
}

private struct PointMarks<X: Plottable>: ChartContent {
    let kind: ChartModel.Kind
    let x: PlottableValue<X>
    let y: Double
    let series: String

    var body: some ChartContent {
        let yv = PlottableValue.value("y", y)
        let sv = PlottableValue.value("series", series)
        switch kind {
        case .bar:
            BarMark(x: x, y: yv)
                .foregroundStyle(by: sv)
                .position(by: sv)
        case .area:
            AreaMark(x: x, y: yv, stacking: .unstacked)
                .foregroundStyle(by: sv)
                .opacity(0.22)
            LineMark(x: x, y: yv)
                .foregroundStyle(by: sv)
        case .line, .spark:
            LineMark(x: x, y: yv)
                .foregroundStyle(by: sv)
                .interpolationMethod(.monotone)
        }
    }
}
#endif
