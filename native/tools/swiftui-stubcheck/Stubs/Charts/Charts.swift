@_exported import SwiftUI

public protocol ChartContent {
    associatedtype Body: ChartContent
    @ChartContentBuilder @MainActor var body: Body { get }
}
public protocol _ChartLeaf: ChartContent where Body == Never {}
extension Never: ChartContent {}
extension _ChartLeaf { public var body: Never { fatalError() } }
public struct _ChartTuple<T>: _ChartLeaf { init(_ t: T) {} }
public struct _ChartCond<T, F>: _ChartLeaf { init() {} }
public struct _ChartMod<C>: _ChartLeaf { init(_ c: C) {} }

@resultBuilder
public struct ChartContentBuilder {
    public static func buildExpression<C: ChartContent>(_ content: C) -> C { content }
    public static func buildBlock<C: ChartContent>(_ content: C) -> C { content }
    public static func buildBlock<each C: ChartContent>(_ content: repeat each C) -> _ChartTuple<(repeat each C)> { _ChartTuple((repeat each content)) }
    public static func buildEither<T: ChartContent, F: ChartContent>(first: T) -> _ChartCond<T, F> { _ChartCond() }
    public static func buildEither<T: ChartContent, F: ChartContent>(second: F) -> _ChartCond<T, F> { _ChartCond() }
}

public protocol Plottable {}
public protocol ScaleDomain {}
extension ClosedRange: ScaleDomain where Bound: Plottable {}
extension Date: Plottable {}
extension Double: Plottable {}
extension String: Plottable {}

public struct PlottableValue<Value: Plottable> {
    public static func value(_ label: LocalizedStringKey, _ value: Value) -> PlottableValue<Value> { PlottableValue() }
    @_disfavoredOverload public static func value<S: StringProtocol>(_ label: S, _ value: Value) -> PlottableValue<Value> { PlottableValue() }
}

public struct MarkStackingMethod: Sendable { public static let standard = MarkStackingMethod(), unstacked = MarkStackingMethod() }
public struct InterpolationMethod: Sendable { public static let monotone = InterpolationMethod(), linear = InterpolationMethod() }

public struct LineMark: _ChartLeaf { public init<X: Plottable, Y: Plottable>(x: PlottableValue<X>, y: PlottableValue<Y>) {} }
public struct BarMark: _ChartLeaf { public init<X: Plottable, Y: Plottable>(x: PlottableValue<X>, y: PlottableValue<Y>) {} }
public struct AreaMark: _ChartLeaf {
    public init<X: Plottable, Y: Plottable>(x: PlottableValue<X>, y: PlottableValue<Y>, stacking: MarkStackingMethod = .standard) {}
}

extension ChartContent {
    public func foregroundStyle<D: Plottable>(by value: PlottableValue<D>) -> some ChartContent { _ChartMod(self) }
    public func position<P: Plottable>(by value: PlottableValue<P>) -> some ChartContent { _ChartMod(self) }
    public func opacity(_ value: Double) -> some ChartContent { _ChartMod(self) }
    public func interpolationMethod(_ method: InterpolationMethod) -> some ChartContent { _ChartMod(self) }
}

extension ForEach: ChartContent where Content: ChartContent {
    public var body: Never { fatalError() }
}
extension ForEach where Content: ChartContent {
    public init(_ data: Data, id: KeyPath<Data.Element, ID>, @ChartContentBuilder content: @escaping (Data.Element) -> Content) { self.init(_stub: ()) }
}

public struct Chart<Content: ChartContent>: _Leaf {
    public init(@ChartContentBuilder content: () -> Content) {}
}

public protocol AxisContent {}
public struct _Axis: AxisContent {}
@resultBuilder public struct AxisContentBuilder {
    public static func buildExpression<C: AxisContent>(_ c: C) -> C { c }
    public static func buildBlock<each C: AxisContent>(_ c: repeat each C) -> _Axis { _Axis() }
}
public struct AxisValue { public func `as`<P: Plottable>(_ type: P.Type) -> P? { nil } }
public struct AxisMarkValues: Sendable {
    public static func automatic(desiredCount: Int? = nil) -> AxisMarkValues { AxisMarkValues() }
    public static var automatic: AxisMarkValues { AxisMarkValues() }
}
public struct AxisMarks: AxisContent {
    public init(@AxisMarkBuilder content: @escaping (AxisValue) -> some AxisMark) {}
    public init(values: AxisMarkValues = .automatic) {}
    public init<Values: Sequence>(values: Values, @AxisMarkBuilder content: @escaping (AxisValue) -> some AxisMark) where Values.Element: Plottable {}
}
public protocol AxisMark {}
public struct _AxisMarks: AxisMark {}
@resultBuilder public struct AxisMarkBuilder {
    public static func buildExpression<C: AxisMark>(_ c: C) -> C { c }
    public static func buildBlock<each C: AxisMark>(_ c: repeat each C) -> _AxisMarks { _AxisMarks() }
}
public struct AxisGridLine: AxisMark { public init() {} }
public struct AxisValueLabel: AxisMark { public init<C: View>(@ViewBuilder content: () -> C) {} }

extension View {
    public func chartForegroundStyleScale<D: Collection, R: Collection>(domain: D, range: R) -> some View where D.Element: Plottable, R.Element: ShapeStyle { _V(self) }
    public func chartLegend(_ visibility: Visibility) -> some View { _V(self) }
    public func chartXAxis(_ visibility: Visibility) -> some View { _V(self) }
    public func chartYAxis(_ visibility: Visibility) -> some View { _V(self) }
    public func chartYAxis<C: AxisContent>(@AxisContentBuilder content: () -> C) -> some View { _V(self) }
    public func chartXAxis<C: AxisContent>(@AxisContentBuilder content: () -> C) -> some View { _V(self) }
    public func chartYScale<D: ScaleDomain>(domain: D) -> some View { _V(self) }
}
