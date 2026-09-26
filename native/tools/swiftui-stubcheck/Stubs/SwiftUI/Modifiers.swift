// MARK: Styles

public protocol PrimitiveButtonStyle {}
public struct _BtnStyle: PrimitiveButtonStyle {}
extension PrimitiveButtonStyle where Self == _BtnStyle {
    public static var bordered: _BtnStyle { .init() }
    public static var borderedProminent: _BtnStyle { .init() }
    public static var borderless: _BtnStyle { .init() }
    public static var plain: _BtnStyle { .init() }
    public static var automatic: _BtnStyle { .init() }
}
public protocol ListStyle {}
public struct _ListStyle: ListStyle {}
extension ListStyle where Self == _ListStyle {
    public static var plain: _ListStyle { .init() }
    public static var insetGrouped: _ListStyle { .init() }
    public static var grouped: _ListStyle { .init() }
}
public protocol PickerStyle {}
public struct _PickerStyle: PickerStyle {}
extension PickerStyle where Self == _PickerStyle {
    public static var menu: _PickerStyle { .init() }
    public static var segmented: _PickerStyle { .init() }
    public static var inline: _PickerStyle { .init() }
}
public protocol LabelStyle {}
public struct _LabelStyle: LabelStyle {}
extension LabelStyle where Self == _LabelStyle {
    public static var iconOnly: _LabelStyle { .init() }
    public static var titleOnly: _LabelStyle { .init() }
}
public protocol NavigationSplitViewStyle {}
extension BalancedNavigationSplitViewStyle: NavigationSplitViewStyle {}
extension NavigationSplitViewStyle where Self == BalancedNavigationSplitViewStyle {
    public static var balanced: BalancedNavigationSplitViewStyle { .init() }
}

public protocol SymbolEffect {}
public protocol IndefiniteSymbolEffect {}
public struct PulseSymbolEffect: SymbolEffect, IndefiniteSymbolEffect {}
public struct VariableColorSymbolEffect: SymbolEffect, IndefiniteSymbolEffect { public var iterative: VariableColorSymbolEffect { self } }
extension SymbolEffect where Self == PulseSymbolEffect { public static var pulse: PulseSymbolEffect { .init() } }
extension SymbolEffect where Self == VariableColorSymbolEffect { public static var variableColor: VariableColorSymbolEffect { .init() } }

@MainActor @preconcurrency public protocol ViewModifier {
    associatedtype Body: View
    typealias Content = _ViewModifier_Content<Self>
    @ViewBuilder @MainActor func body(content: Content) -> Body
}
public struct _ViewModifier_Content<M>: _Leaf {}

public struct ScrollGeometry: Sendable {
    public var contentOffset: CGPoint
    public var containerSize: CGSize
    public var contentSize: CGSize
}
public struct ScrollAnchorRole: Sendable { public static let initialOffset = ScrollAnchorRole(), sizeChanges = ScrollAnchorRole(), alignment = ScrollAnchorRole() }

// MARK: Modifiers (only the ones XbinRenderer uses)

extension View {
    public func modifier<M: ViewModifier>(_ modifier: M) -> some View { _V(self) }
    public func environment<V>(_ keyPath: WritableKeyPath<EnvironmentValues, V>, _ value: V) -> some View { _V(self) }
    public func font(_ font: Font?) -> some View { _V(self) }
    public func fontWeight(_ weight: Font.Weight?) -> some View { _V(self) }
    public func monospaced(_ isActive: Bool = true) -> some View { _V(self) }
    public func monospacedDigit() -> some View { _V(self) }
    public func foregroundStyle<S: ShapeStyle>(_ style: S) -> some View { _V(self) }
    public func tint<S: ShapeStyle>(_ tint: S?) -> some View { _V(self) }
    public func tint(_ tint: Color?) -> some View { _V(self) }
    public func background<S: ShapeStyle, T: Shape>(_ style: S, in shape: T) -> some View { _V(self) }
    public func background<S: ShapeStyle>(_ style: S, ignoresSafeAreaEdges edges: Edge.Set = .all) -> some View { _V(self) }
    public func background<V: View>(alignment: Alignment = .center, @ViewBuilder content: () -> V) -> some View { _V(self) }
    public func overlay<V: View>(alignment: Alignment = .center, @ViewBuilder content: () -> V) -> some View { _V(self) }
    public func overlay<S: View>(_ overlay: S, alignment: Alignment = .center) -> some View { _V(self) }
    public func padding(_ edges: Edge.Set = .all, _ length: CGFloat? = nil) -> some View { _V(self) }
    public func padding(_ length: CGFloat) -> some View { _V(self) }
    public func frame(width: CGFloat? = nil, height: CGFloat? = nil, alignment: Alignment = .center) -> some View { _V(self) }
    public func frame(minWidth: CGFloat? = nil, idealWidth: CGFloat? = nil, maxWidth: CGFloat? = nil, minHeight: CGFloat? = nil,
                      idealHeight: CGFloat? = nil, maxHeight: CGFloat? = nil, alignment: Alignment = .center) -> some View { _V(self) }
    public func fixedSize(horizontal: Bool, vertical: Bool) -> some View { _V(self) }
    public func fixedSize() -> some View { _V(self) }
    public func opacity(_ opacity: Double) -> some View { _V(self) }
    public func disabled(_ disabled: Bool) -> some View { _V(self) }
    public func lineLimit(_ number: Int?) -> some View { _V(self) }
    public func lineLimit(_ limit: ClosedRange<Int>) -> some View { _V(self) }
    public func multilineTextAlignment(_ alignment: TextAlignment) -> some View { _V(self) }
    public func truncationMode(_ mode: Text.TruncationMode) -> some View { _V(self) }
    public func textSelection<S: TextSelectability>(_ selectability: S) -> some View { _V(self) }
    public func contentShape<S: Shape>(_ shape: S) -> some View { _V(self) }
    public func clipShape<S: Shape>(_ shape: S, style: FillStyle = FillStyle()) -> some View { _V(self) }
    public func rotationEffect(_ angle: Angle, anchor: UnitPoint = .center) -> some View { _V(self) }
    public func symbolEffect<T: IndefiniteSymbolEffect & SymbolEffect>(_ effect: T, options: SymbolEffectOptions = .default, isActive: Bool = true) -> some View { _V(self) }
    public func ignoresSafeArea(_ regions: SafeAreaRegions = .all, edges: Edge.Set = .all) -> some View { _V(self) }
    public func id<ID: Hashable>(_ id: ID) -> some View { _V(self) }
    public func tag<V: Hashable>(_ tag: V) -> some View { _V(self) }
    public func badge(_ label: Text?) -> some View { _V(self) }
    public func tabItem<V: View>(@ViewBuilder _ label: () -> V) -> some View { _V(self) }
    public func buttonStyle<S: PrimitiveButtonStyle>(_ style: S) -> some View { _V(self) }
    public func buttonBorderShape(_ shape: ButtonBorderShape) -> some View { _V(self) }
    public func controlSize(_ controlSize: ControlSize) -> some View { _V(self) }
    public func listStyle<S: ListStyle>(_ style: S) -> some View { _V(self) }
    public func listRowBackground<V: View>(_ view: V?) -> some View { _V(self) }
    public func listRowInsets(_ insets: EdgeInsets?) -> some View { _V(self) }
    public func listRowSeparator(_ visibility: Visibility, edges: VerticalEdge.Set = .all) -> some View { _V(self) }
    public func pickerStyle<S: PickerStyle>(_ style: S) -> some View { _V(self) }
    public func labelStyle<S: LabelStyle>(_ style: S) -> some View { _V(self) }
    public func labelsHidden() -> some View { _V(self) }
    public func navigationSplitViewStyle<S: NavigationSplitViewStyle>(_ style: S) -> some View { _V(self) }
    public func accessibilityLabel(_ label: Text) -> some View { _V(self) }
    public func accessibilityLabel(_ label: LocalizedStringKey) -> some View { _V(self) }
    @_disfavoredOverload public func accessibilityLabel<S: StringProtocol>(_ label: S) -> some View { _V(self) }
    public func accessibilityValue(_ value: LocalizedStringKey) -> some View { _V(self) }
    @_disfavoredOverload public func accessibilityValue<S: StringProtocol>(_ value: S) -> some View { _V(self) }
    public func accessibilityHidden(_ hidden: Bool) -> some View { _V(self) }
    public func accessibilityElement(children: AccessibilityChildBehavior = .ignore) -> some View { _V(self) }
    public func accessibilityAddTraits(_ traits: AccessibilityTraits) -> some View { _V(self) }
    public func onAppear(perform action: (() -> Void)? = nil) -> some View { _V(self) }
    public func onTapGesture(count: Int = 1, perform action: @escaping () -> Void) -> some View { _V(self) }
    public func onChange<V: Equatable>(of value: V, initial: Bool = false, _ action: @escaping () -> Void) -> some View { _V(self) }
    public func onChange<V: Equatable>(of value: V, initial: Bool = false, _ action: @escaping (_ oldValue: V, _ newValue: V) -> Void) -> some View { _V(self) }
    public func task<T: Equatable>(id value: T, priority: TaskPriority = .userInitiated, @_inheritActorContext _ action: @escaping @Sendable () async -> Void) -> some View { _V(self) }
    public func onSubmit(of triggers: SubmitTriggers = .text, _ action: @escaping () -> Void) -> some View { _V(self) }
    public func submitLabel(_ submitLabel: SubmitLabel) -> some View { _V(self) }
    public func keyboardType(_ type: UIKeyboardType) -> some View { _V(self) }
    public func textContentType(_ textContentType: UITextContentType?) -> some View { _V(self) }
    public func textInputAutocapitalization(_ autocapitalization: TextInputAutocapitalization?) -> some View { _V(self) }
    public func autocorrectionDisabled(_ disable: Bool = true) -> some View { _V(self) }
    public func privacySensitive(_ sensitive: Bool = true) -> some View { _V(self) }
    public func focused(_ condition: FocusState<Bool>.Binding) -> some View { _V(self) }
    public func navigationTitle<S: StringProtocol>(_ title: S) -> some View { _V(self) }
    public func navigationSubtitle<S: StringProtocol>(_ subtitle: S) -> some View { _V(self) }
    public func navigationBarTitleDisplayMode(_ displayMode: NavigationBarItem.TitleDisplayMode) -> some View { _V(self) }
    public func navigationDestination<D: Hashable, C: View>(for data: D.Type, @ViewBuilder destination: @escaping (D) -> C) -> some View { _V(self) }
    public func toolbar<Content: ToolbarContent>(@ToolbarContentBuilder content: () -> Content) -> some View { _V(self) }
    public func refreshable(action: @escaping @Sendable () async -> Void) -> some View { _V(self) }
    public func searchable(text: Binding<String>, placement: SearchFieldPlacement = .automatic, prompt: Text? = nil) -> some View { _V(self) }
    public func safeAreaInset<V: View>(edge: VerticalEdge, alignment: HorizontalAlignment = .center, spacing: CGFloat? = nil, @ViewBuilder content: () -> V) -> some View { _V(self) }
    public func sheet<Content: View>(isPresented: Binding<Bool>, onDismiss: (() -> Void)? = nil, @ViewBuilder content: @escaping () -> Content) -> some View { _V(self) }
    public func fullScreenCover<Content: View>(isPresented: Binding<Bool>, onDismiss: (() -> Void)? = nil, @ViewBuilder content: @escaping () -> Content) -> some View { _V(self) }
    public func presentationDetents(_ detents: Set<PresentationDetent>) -> some View { _V(self) }
    public func confirmationDialog<S: StringProtocol, A: View, M: View, T>(_ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic, presenting data: T?, @ViewBuilder actions: (T) -> A, @ViewBuilder message: (T) -> M) -> some View { _V(self) }
    public func swipeActions<T: View>(edge: HorizontalEdge = .trailing, allowsFullSwipe: Bool = true, @ViewBuilder content: () -> T) -> some View { _V(self) }
    public func contextMenu<MenuItems: View>(@ViewBuilder menuItems: () -> MenuItems) -> some View { _V(self) }
    public func containerRelativeFrame(_ axes: Axis.Set, alignment: Alignment = .center, _ length: @escaping (CGFloat, Axis) -> CGFloat) -> some View { _V(self) }
    public func defaultScrollAnchor(_ anchor: UnitPoint?) -> some View { _V(self) }
    public func defaultScrollAnchor(_ anchor: UnitPoint?, for role: ScrollAnchorRole) -> some View { _V(self) }
    public func onScrollGeometryChange<T: Equatable>(for type: T.Type, of transform: @escaping (ScrollGeometry) -> T, action: @escaping (_ oldValue: T, _ newValue: T) -> Void) -> some View { _V(self) }
    public func gridColumnAlignment(_ guide: HorizontalAlignment) -> some View { _V(self) }
}

public enum TextAlignment: Sendable { case leading, center, trailing }
public struct FillStyle: Sendable { public init() {} }
public struct SymbolEffectOptions: Sendable { public static let `default` = SymbolEffectOptions() }
public struct SafeAreaRegions: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let all = SafeAreaRegions(rawValue: 1) }
public enum VerticalEdge: Sendable { case top, bottom
    public struct Set: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let all = Set(rawValue: 3) }
}
public enum HorizontalEdge: Sendable { case leading, trailing }
public struct SubmitTriggers: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let text = SubmitTriggers(rawValue: 1) }
public struct SearchFieldPlacement: Sendable { public static let automatic = SearchFieldPlacement() }

// MARK: Toolbar

public protocol ToolbarContent {
    associatedtype Body: ToolbarContent
    @ToolbarContentBuilder @MainActor var body: Body { get }
}
public protocol _ToolbarLeaf: ToolbarContent where Body == Never {}
extension Never: ToolbarContent {}
extension _ToolbarLeaf { public var body: Never { fatalError() } }
public struct ToolbarItemPlacement: Sendable {
    public static let topBarTrailing = ToolbarItemPlacement(), topBarLeading = ToolbarItemPlacement(), cancellationAction = ToolbarItemPlacement(),
        confirmationAction = ToolbarItemPlacement(), primaryAction = ToolbarItemPlacement(), principal = ToolbarItemPlacement(), automatic = ToolbarItemPlacement()
}
public struct ToolbarItem<ID, Content: View>: _ToolbarLeaf {}
extension ToolbarItem where ID == () {
    public init(placement: ToolbarItemPlacement = .automatic, @ViewBuilder content: () -> Content) {}
}
public struct ToolbarItemGroup<Content: View>: _ToolbarLeaf {
    public init(placement: ToolbarItemPlacement = .automatic, @ViewBuilder content: () -> Content) {}
}
public struct _ToolbarTuple<T>: _ToolbarLeaf { init(_ t: T) {} }
@resultBuilder
public struct ToolbarContentBuilder {
    public static func buildExpression<C: ToolbarContent>(_ content: C) -> C { content }
    public static func buildBlock<C: ToolbarContent>(_ content: C) -> C { content }
    public static func buildBlock<each C: ToolbarContent>(_ content: repeat each C) -> _ToolbarTuple<(repeat each C)> { _ToolbarTuple((repeat each content)) }
}

// MARK: Layout

public protocol Layout: View where Body == Never {
    typealias Subviews = LayoutSubviews
    associatedtype Cache = Void
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Cache) -> CGSize
    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Cache)
}
extension Layout {
    public var body: Never { fatalError() }
    public func callAsFunction<V: View>(@ViewBuilder _ content: () -> V) -> some View { _V(self) }
}
public struct LayoutSubview {
    public func sizeThatFits(_ proposal: ProposedViewSize) -> CGSize { .zero }
    public func place(at position: CGPoint, anchor: UnitPoint = .topLeading, proposal: ProposedViewSize) {}
}
public struct LayoutSubviews: RandomAccessCollection {
    public var startIndex: Int { 0 }
    public var endIndex: Int { 0 }
    public subscript(i: Int) -> LayoutSubview { LayoutSubview() }
}

// MARK: Rendering

@MainActor public final class ImageRenderer<Content: View> {
    public init(content: Content) {}
    public var scale: CGFloat = 1
    public var proposedSize: ProposedViewSize = .unspecified
    public var uiImage: UIImage? { nil }
}
@MainActor open class UIHostingController<Content: View>: UIViewController {
    public init(rootView: Content) { super.init() }
}
extension UIContentSizeCategory { public init(_ size: DynamicTypeSize) { self.init() } }

// MARK: AttributedString attributes (the SwiftUI scope)

extension AttributeScopes {
    public struct SwiftUIAttributes: AttributeScope {
        public enum FontAttribute: AttributedStringKey { public typealias Value = Font; public static let name = "SwiftUI.Font" }
        public enum ForegroundColorAttribute: AttributedStringKey { public typealias Value = Color; public static let name = "SwiftUI.ForegroundColor" }
        public enum BackgroundColorAttribute: AttributedStringKey { public typealias Value = Color; public static let name = "SwiftUI.BackgroundColor" }
        public enum StrikethroughStyleAttribute: AttributedStringKey { public typealias Value = Text.LineStyle; public static let name = "SwiftUI.StrikethroughStyle" }
        public enum UnderlineStyleAttribute: AttributedStringKey { public typealias Value = Text.LineStyle; public static let name = "SwiftUI.UnderlineStyle" }
        public let font: FontAttribute
        public let foregroundColor: ForegroundColorAttribute
        public let backgroundColor: BackgroundColorAttribute
        public let strikethroughStyle: StrikethroughStyleAttribute
        public let underlineStyle: UnderlineStyleAttribute
    }
}
