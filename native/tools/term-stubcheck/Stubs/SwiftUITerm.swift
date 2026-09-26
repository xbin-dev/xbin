// Extra SwiftUI declarations the terminal uses (real SDK signatures as far as
// known), on top of ../../swiftui-stubcheck/Stubs/SwiftUI (GeometryReader,
// UIViewRepresentable and accessibilityHint live there).

@propertyWrapper @dynamicMemberLookup
public struct Bindable<Value> {
    public init(wrappedValue: Value) where Value: AnyObject & Observable {}
    public init(_ wrappedValue: Value) where Value: AnyObject & Observable {}
    public var wrappedValue: Value { fatalError() }
    public var projectedValue: Bindable<Value> { self }
    public subscript<Subject>(dynamicMember keyPath: ReferenceWritableKeyPath<Value, Subject>) -> Binding<Subject> { fatalError() }
}

public struct EventModifiers: OptionSet, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let shift = EventModifiers(rawValue: 1), control = EventModifiers(rawValue: 2), option = EventModifiers(rawValue: 4), command = EventModifiers(rawValue: 8)
}
public struct KeyEquivalent: Hashable, Sendable, ExpressibleByExtendedGraphemeClusterLiteral {
    public init(_ character: Character) {}
    public init(extendedGraphemeClusterLiteral: Character) {}
    public static let `return` = KeyEquivalent("\r"), escape = KeyEquivalent("\u{1b}"), upArrow = KeyEquivalent("u"), downArrow = KeyEquivalent("d")
}
public struct KeyPress: Sendable {
    public struct Phases: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }
        public static let down = Phases(rawValue: 1), `repeat` = Phases(rawValue: 2), up = Phases(rawValue: 4), all = Phases(rawValue: 7) }
    public enum Result: Sendable { case handled, ignored }
    public var key: KeyEquivalent { fatalError() }
    public var characters: String { "" }
    public var modifiers: EventModifiers { [] }
    public var phase: Phases { .down }
}
public struct KeyboardShortcut: Sendable {
    public static let defaultAction = KeyboardShortcut(), cancelAction = KeyboardShortcut()
}
extension View {
    public func onKeyPress(phases: KeyPress.Phases = [.down, .repeat], action: @escaping (KeyPress) -> KeyPress.Result) -> some View { _V(self) }
    public func keyboardShortcut(_ key: KeyEquivalent, modifiers: EventModifiers = .command) -> some View { _V(self) }
    public func keyboardShortcut(_ shortcut: KeyboardShortcut) -> some View { _V(self) }
    public func minimumScaleFactor(_ factor: CGFloat) -> some View { _V(self) }
    public func clipped(antialiased: Bool = false) -> some View { _V(self) }
    public func statusBarHidden(_ hidden: Bool = true) -> some View { _V(self) }
    public func persistentSystemOverlays(_ visibility: Visibility) -> some View { _V(self) }
    public func toolbar(_ visibility: Visibility, for bars: ToolbarPlacement...) -> some View { _V(self) }
    public func alert<A: View, M: View>(_ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, @ViewBuilder actions: () -> A, @ViewBuilder message: () -> M) -> some View { _V(self) }
    public func alert<A: View>(_ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, @ViewBuilder actions: () -> A) -> some View { _V(self) }
    public func confirmationDialog<A: View, M: View>(_ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic, @ViewBuilder actions: () -> A, @ViewBuilder message: () -> M) -> some View { _V(self) }
    @_disfavoredOverload public func confirmationDialog<S: StringProtocol, A: View, M: View>(_ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic, @ViewBuilder actions: () -> A, @ViewBuilder message: () -> M) -> some View { _V(self) }
    public func navigationTitle(_ title: Text) -> some View { _V(self) }
    public func task(priority: TaskPriority = .userInitiated, @_inheritActorContext _ action: @escaping @Sendable () async -> Void) -> some View { _V(self) }
    public func onDisappear(perform action: (() -> Void)? = nil) -> some View { _V(self) }
    public func foregroundStyle<S1: ShapeStyle, S2: ShapeStyle>(_ primary: S1, _ secondary: S2) -> some View { _V(self) }
}
public struct ToolbarPlacement: Sendable { public static let navigationBar = ToolbarPlacement(), tabBar = ToolbarPlacement() }
extension ToolbarItemGroup {}

public struct GridItem: Sendable {
    public enum Size: Sendable { case fixed(CGFloat), flexible(minimum: CGFloat = 10, maximum: CGFloat = .infinity), adaptive(minimum: CGFloat, maximum: CGFloat = .infinity) }
    public init(_ size: GridItem.Size = .flexible(), spacing: CGFloat? = nil, alignment: Alignment? = nil) {}
}
public struct LazyVGrid<Content: View>: _Leaf {
    public init(columns: [GridItem], alignment: HorizontalAlignment = .center, spacing: CGFloat? = nil, @ViewBuilder content: () -> Content) {}
}

extension Toggle where Label == Text {
    public init(_ titleKey: LocalizedStringKey, isOn: Binding<Bool>) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, isOn: Binding<Bool>) {}
}
extension Picker where Label == Text {
    public init(_ titleKey: LocalizedStringKey, selection: Binding<SelectionValue>, @ViewBuilder content: () -> Content) {}
}
extension Button where Label == SwiftUI.Label<Text, Image> {
    public init(_ titleKey: LocalizedStringKey, systemImage: String, action: @escaping () -> Void) {}
    public init(_ titleKey: LocalizedStringKey, systemImage: String, role: ButtonRole?, action: @escaping () -> Void) {}
}
extension TextField where Label == Text {
    public init(_ titleKey: LocalizedStringKey, text: Binding<String>) {}
}
extension Section where Parent == Text, Content: View, Footer == EmptyView {
    public init(_ titleKey: LocalizedStringKey, @ViewBuilder content: () -> Content) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, @ViewBuilder content: () -> Content) {}
}
extension Section where Parent: View, Content: View, Footer == EmptyView {
    public init(@ViewBuilder content: () -> Content, @ViewBuilder header: () -> Parent) {}
}
public struct NavigationLink<Label: View, Destination: View>: _Leaf {
    public init(@ViewBuilder destination: () -> Destination, @ViewBuilder label: () -> Label) {}
}
extension ProgressView {}
extension Material { public static let ultraThinMaterial = Material(), regularMaterial = Material() }
extension ShapeStyle where Self == Material {
    public static var ultraThinMaterial: Material { .init() }
    public static var regularMaterial: Material { .init() }
}
public struct TintShapeStyle: ShapeStyle { public init() {} }
extension ShapeStyle where Self == TintShapeStyle { public static var tint: TintShapeStyle { .init() } }
extension Color {
    public init(_ uiColor: UIColor) { self.init(uiColor: uiColor) }
    public static let orange = Color(white: 0)
}
extension ShapeStyle where Self == Color {
    public static var orange: Color { .orange }
    public static var red: Color { .red }
    public static var black: Color { .black }
}
extension AccessibilityTraits { public static let isSelected = AccessibilityTraits(rawValue: 8) }
extension EnvironmentValues {}
extension OpenURLAction { @MainActor public func callAsFunction(_ url: URL) {} }
extension ControlSize {}
public struct _ControlSizeHolder {}
extension ProgressView where Label == EmptyView, CurrentValueLabel == EmptyView {}
public func withAnimation<Result>(_ body: () throws -> Result) rethrows -> Result { try body() }
extension Section where Parent == EmptyView, Content: View, Footer: View {
    public init(@ViewBuilder content: () -> Content, @ViewBuilder footer: () -> Footer) {}
}
// iOS 27.1 (reported: github.com/artemnovichkov/iPhone-Duo-by-Examples); only
// with XBIN_SDK_27_1.
public struct ReservedRegion: Identifiable, Sendable {
    public var id: Int { 0 }
    public var frame: CGRect { .zero }
    public var margins: EdgeInsets { EdgeInsets() }
    public var isActive: Bool { false }
    public struct Kind: Hashable, Sendable { public static let division = Kind(), occlusion = Kind() }
    public struct QueryOptions: OptionSet, Sendable { public let rawValue: Int; public init(rawValue: Int) { self.rawValue = rawValue }; public static let includeInactive = QueryOptions(rawValue: 1) }
}
extension GeometryProxy {
    public func reservedRegions(kind: ReservedRegion.Kind, options: ReservedRegion.QueryOptions = []) -> [ReservedRegion] { [] }
}
