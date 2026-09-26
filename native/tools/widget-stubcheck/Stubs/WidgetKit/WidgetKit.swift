@_exported import SwiftUI
import ActivityKit

// WidgetKit's Live Activity API as the widget uses it, plus the few SwiftUI
// members the SwiftUI stubs (../swiftui-stubcheck/Stubs) don't have.

@MainActor @preconcurrency
public protocol WidgetConfiguration {}

@MainActor @preconcurrency
public protocol Widget {
    associatedtype Body: WidgetConfiguration
    @WidgetConfigurationBuilder var body: Body { get }
    init()
}

@resultBuilder
public struct WidgetConfigurationBuilder {
    public static func buildBlock<C: WidgetConfiguration>(_ content: C) -> C { content }
}

@MainActor @preconcurrency
public protocol WidgetBundle {
    associatedtype Body: Widget
    @WidgetBundleBuilder var body: Body { get }
    init()
}

extension WidgetBundle {
    public static func main() {}
}

@resultBuilder
public struct WidgetBundleBuilder {
    public static func buildBlock<W: Widget>(_ content: W) -> W { content }
}

public struct ActivityViewContext<Attributes: ActivityAttributes> {
    public var state: Attributes.ContentState { fatalError() }
    public var attributes: Attributes { fatalError() }
    public var activityID: String { "" }
    public var isStale: Bool { false }
}

public struct ActivityConfiguration<Attributes: ActivityAttributes>: WidgetConfiguration {
    public init<Content: View>(for attributesType: Attributes.Type,
                               @ViewBuilder content: @escaping (ActivityViewContext<Attributes>) -> Content,
                               dynamicIsland: @escaping (ActivityViewContext<Attributes>) -> DynamicIsland) {}
}

public struct DynamicIslandExpandedRegionPosition: Sendable {
    public static let leading = DynamicIslandExpandedRegionPosition()
    public static let trailing = DynamicIslandExpandedRegionPosition()
    public static let center = DynamicIslandExpandedRegionPosition()
    public static let bottom = DynamicIslandExpandedRegionPosition()
}

public struct DynamicIslandExpandedRegion<Content: View> {
    public init(_ position: DynamicIslandExpandedRegionPosition, priority: Double = 0, @ViewBuilder content: () -> Content) {}
}

public struct DynamicIslandExpandedContent<Content> {}

@resultBuilder
public struct DynamicIslandExpandedContentBuilder {
    public static func buildBlock<each C: View>(_ regions: repeat DynamicIslandExpandedRegion<each C>)
        -> DynamicIslandExpandedContent<(repeat each C)> { DynamicIslandExpandedContent() }
}

public struct DynamicIsland {
    public init<Expanded, CompactLeading: View, CompactTrailing: View, Minimal: View>(
        @DynamicIslandExpandedContentBuilder expanded: () -> DynamicIslandExpandedContent<Expanded>,
        @ViewBuilder compactLeading: () -> CompactLeading,
        @ViewBuilder compactTrailing: () -> CompactTrailing,
        @ViewBuilder minimal: () -> Minimal) {}
    public func widgetURL(_ url: URL?) -> DynamicIsland { self }
    public func keylineTint(_ color: Color?) -> DynamicIsland { self }
}

extension View {
    public func widgetURL(_ url: URL?) -> some View { _V(self) }
    public func activityBackgroundTint(_ color: Color?) -> some View { _V(self) }
    public func activitySystemActionForegroundColor(_ color: Color?) -> some View { _V(self) }
}

// SwiftUI members the SwiftUI stubs lack
extension Text {
    public struct DateStyle: Sendable {
        public static let timer = DateStyle(), relative = DateStyle(), time = DateStyle(), date = DateStyle(), offset = DateStyle()
    }
    public init(_ date: Date, style: DateStyle) { self.init(verbatim: "") }
}

extension Color {
    public init(red: Double, green: Double, blue: Double, opacity: Double = 1) { self.init(white: 0) }
    public static let green = Color(white: 0)
    public static let orange = Color(white: 0)
}
