@_exported import Foundation
@_exported import Observation
@_exported import UIKit

// MARK: View and builders

@MainActor @preconcurrency
public protocol View {
    associatedtype Body: View
    @ViewBuilder @MainActor var body: Body { get }
}

extension Never: View {
    public typealias Body = Never
    public var body: Never { fatalError() }
}

/// A leaf view (Body == Never).
public protocol _Leaf: View where Body == Never {}
extension _Leaf {
    public var body: Never { fatalError() }
}

public struct _V<T>: _Leaf { nonisolated public init(_ t: T) {} }

@resultBuilder
public struct ViewBuilder {
    public static func buildExpression<Content: View>(_ content: Content) -> Content { content }
    public static func buildBlock() -> EmptyView { EmptyView() }
    public static func buildBlock<Content: View>(_ content: Content) -> Content { content }
    public static func buildBlock<each Content: View>(_ content: repeat each Content) -> TupleView<(repeat each Content)> {
        TupleView((repeat each content))
    }
    public static func buildIf<Content: View>(_ content: Content?) -> Content? { content }
    public static func buildEither<T: View, F: View>(first: T) -> _ConditionalContent<T, F> { _ConditionalContent() }
    public static func buildEither<T: View, F: View>(second: F) -> _ConditionalContent<T, F> { _ConditionalContent() }
    public static func buildLimitedAvailability<Content: View>(_ content: Content) -> AnyView { AnyView(content) }
}

extension Optional: View where Wrapped: View {
    public typealias Body = Never
    public var body: Never { fatalError() }
}

public struct TupleView<T>: _Leaf { nonisolated public init(_ value: T) {} }
public struct _ConditionalContent<T, F>: _Leaf { nonisolated init() {} }
public struct EmptyView: _Leaf { nonisolated public init() {} }
public struct AnyView: _Leaf { nonisolated public init<V: View>(_ view: V) {} }
public struct Group<Content>: _Leaf {}
extension Group where Content: View { public init(@ViewBuilder content: () -> Content) {} }

public struct ForEach<Data: RandomAccessCollection, ID: Hashable, Content> { public init(_stub: Void) {} }
extension ForEach: View where Content: View { public typealias Body = Never; public var body: Never { fatalError() } }
extension ForEach where Content: View {
    public init(_ data: Data, @ViewBuilder content: @escaping (Data.Element) -> Content) where Data.Element: Identifiable, ID == Data.Element.ID {}
    public init(_ data: Data, id: KeyPath<Data.Element, ID>, @ViewBuilder content: @escaping (Data.Element) -> Content) {}
}
extension ForEach where Data == Range<Int>, ID == Int, Content: View {
    public init(_ data: Range<Int>, @ViewBuilder content: @escaping (Int) -> Content) {}
}

// MARK: Stacks and containers

public struct VStack<Content: View>: _Leaf {
    public init(alignment: HorizontalAlignment = .center, spacing: CGFloat? = nil, @ViewBuilder content: () -> Content) {}
}
public struct HStack<Content: View>: _Leaf {
    public init(alignment: VerticalAlignment = .center, spacing: CGFloat? = nil, @ViewBuilder content: () -> Content) {}
}
public struct ZStack<Content: View>: _Leaf {
    public init(alignment: Alignment = .center, @ViewBuilder content: () -> Content) {}
}
public struct LazyVStack<Content: View>: _Leaf {
    public init(alignment: HorizontalAlignment = .center, spacing: CGFloat? = nil, @ViewBuilder content: () -> Content) {}
}
public struct ScrollView<Content: View>: _Leaf {
    public init(_ axes: Axis.Set = .vertical, showsIndicators: Bool = true, @ViewBuilder content: () -> Content) {}
}
public struct List<SelectionValue: Hashable, Content: View>: _Leaf {}
extension List where SelectionValue == Never {
    public init(@ViewBuilder content: () -> Content) {}
}
public struct Form<Content: View>: _Leaf { public init(@ViewBuilder content: () -> Content) {} }
public struct Section<Parent, Content, Footer>: _Leaf {}
extension Section where Parent: View, Content: View, Footer: View {
    public init(@ViewBuilder content: () -> Content, @ViewBuilder header: () -> Parent, @ViewBuilder footer: () -> Footer) {}
}
extension Section where Parent == EmptyView, Content: View, Footer == EmptyView {
    public init(@ViewBuilder content: () -> Content) {}
}
public struct Grid<Content: View>: _Leaf {
    public init(alignment: Alignment = .center, horizontalSpacing: CGFloat? = nil, verticalSpacing: CGFloat? = nil, @ViewBuilder content: () -> Content) {}
}
public struct GridRow<Content: View>: _Leaf { public init(alignment: VerticalAlignment? = nil, @ViewBuilder content: () -> Content) {} }
public struct Spacer: _Leaf { public init(minLength: CGFloat? = nil) {} }
public struct Divider: _Leaf { public init() {} }

// MARK: Geometry

public enum Axis: Sendable {
    case horizontal, vertical
    public struct Set: OptionSet, Sendable {
        public let rawValue: Int
        public init(rawValue: Int) { self.rawValue = rawValue }
        public static let horizontal = Set(rawValue: 1)
        public static let vertical = Set(rawValue: 2)
    }
}
public struct HorizontalAlignment: Sendable, Equatable { public static let leading = HorizontalAlignment(), center = HorizontalAlignment(), trailing = HorizontalAlignment() }
public struct VerticalAlignment: Sendable, Equatable { public static let top = VerticalAlignment(), center = VerticalAlignment(), bottom = VerticalAlignment(), firstTextBaseline = VerticalAlignment() }
public struct Alignment: Sendable, Equatable {
    public static let leading = Alignment(), center = Alignment(), trailing = Alignment(), top = Alignment(), bottom = Alignment(), topLeading = Alignment(), topTrailing = Alignment(), bottomLeading = Alignment(), bottomTrailing = Alignment()
}
public struct UnitPoint: Sendable, Hashable { public static let top = UnitPoint(), bottom = UnitPoint(), center = UnitPoint(), topLeading = UnitPoint() }
public enum Edge: Sendable { case top, leading, bottom, trailing
    public struct Set: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }
        public static let top = Set(rawValue: 1), bottom = Set(rawValue: 2), leading = Set(rawValue: 4), trailing = Set(rawValue: 8), horizontal = Set(rawValue: 12), vertical = Set(rawValue: 3), all = Set(rawValue: 15) }
}
public struct EdgeInsets: Sendable { public init() {}; public init(top: CGFloat, leading: CGFloat, bottom: CGFloat, trailing: CGFloat) {} }
public struct Angle: Sendable { public static func degrees(_ d: Double) -> Angle { Angle() } }
public enum Visibility: Sendable { case automatic, visible, hidden }
public enum ColorScheme: Sendable, Hashable { case light, dark }
public enum DynamicTypeSize: Sendable, Hashable, Comparable, CaseIterable { case xSmall, small, medium, large, xLarge, xxLarge, xxxLarge, accessibility1, accessibility2, accessibility3, accessibility4, accessibility5
    public var isAccessibilitySize: Bool { self >= .accessibility1 }
}
public enum UserInterfaceSizeClass: Sendable { case compact, regular }
public enum ScenePhase: Sendable { case background, inactive, active }
public struct ProposedViewSize: Sendable, Equatable {
    public var width: CGFloat?; public var height: CGFloat?
    public init(width: CGFloat?, height: CGFloat?) { self.width = width; self.height = height }
    public init(_ size: CGSize) { width = size.width; height = size.height }
    public static let unspecified = ProposedViewSize(width: nil, height: nil)
}
