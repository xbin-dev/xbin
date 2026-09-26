// The SwiftUI the app shell uses beyond native/tools/swiftui-stubcheck's stubs (declarations from
// memory of the SDK, like the rest of these stubs).

// Observable objects in the environment.
extension Environment where Value: AnyObject & Observable {
    public init(_ objectType: Value.Type) {}
}
extension View {
    public func environment<T: AnyObject & Observable>(_ object: T?) -> some View { _V(self) }
}

@propertyWrapper @dynamicMemberLookup
public struct Bindable<Value: AnyObject & Observable> {
    public init(wrappedValue: Value) {}
    public init(_ wrappedValue: Value) {}
    public var wrappedValue: Value { fatalError() }
    public var projectedValue: Bindable<Value> { self }
    public subscript<Subject>(dynamicMember keyPath: ReferenceWritableKeyPath<Value, Subject>) -> Binding<Subject> { fatalError() }
}

@propertyWrapper
public struct SceneStorage<Value>: DynamicProperty {
    public init(wrappedValue: Value, _ key: String) where Value == String {}
    public init(wrappedValue: Value, _ key: String) where Value == Bool {}
    public var wrappedValue: Value { get { fatalError() } nonmutating set {} }
    public var projectedValue: Binding<Value> { fatalError() }
}

@MainActor @preconcurrency
public struct OpenWindowAction {
    public func callAsFunction<D: Decodable & Encodable & Hashable>(value: D) {}
    public func callAsFunction(id: String) {}
}
extension EnvironmentValues {
    public var openWindow: OpenWindowAction { fatalError() }
    public var supportsMultipleWindows: Bool { false }
}

// UIKit hosting.
public struct UIViewRepresentableContext<R> {}
public struct UIViewControllerRepresentableContext<R> {}
@MainActor @preconcurrency
public protocol UIViewRepresentable: View where Body == Never {
    associatedtype UIViewType: UIView
    associatedtype Coordinator = Void
    typealias Context = UIViewRepresentableContext<Self>
    func makeUIView(context: Context) -> UIViewType
    func updateUIView(_ uiView: UIViewType, context: Context)
    func makeCoordinator() -> Coordinator
}
extension UIViewRepresentable where Coordinator == Void { public func makeCoordinator() {} }
extension UIViewRepresentable { public var body: Never { fatalError() } }
@MainActor @preconcurrency
public protocol UIViewControllerRepresentable: View where Body == Never {
    associatedtype UIViewControllerType: UIViewController
    associatedtype Coordinator = Void
    typealias Context = UIViewControllerRepresentableContext<Self>
    func makeUIViewController(context: Context) -> UIViewControllerType
    func updateUIViewController(_ vc: UIViewControllerType, context: Context)
    func makeCoordinator() -> Coordinator
}
extension UIViewControllerRepresentable where Coordinator == Void { public func makeCoordinator() {} }
extension UIViewControllerRepresentable { public var body: Never { fatalError() } }

public struct AnyTransition: Sendable {
    public static let opacity = AnyTransition()
    public static func move(edge: Edge) -> AnyTransition { AnyTransition() }
    public func combined(with other: AnyTransition) -> AnyTransition { self }
}

extension View {
    public func task(priority: TaskPriority = .userInitiated, @_inheritActorContext _ action: @escaping @Sendable () async -> Void) -> some View { _V(self) }
    public func sheet<Item: Identifiable, Content: View>(item: Binding<Item?>, onDismiss: (() -> Void)? = nil, @ViewBuilder content: @escaping (Item) -> Content) -> some View { _V(self) }
    public func animation<V: Equatable>(_ animation: Animation?, value: V) -> some View { _V(self) }
    public func transition(_ t: AnyTransition) -> some View { _V(self) }
    public func zIndex(_ value: Double) -> some View { _V(self) }
    public func blur(radius: CGFloat, opaque: Bool = false) -> some View { _V(self) }
    public func background<V: View>(_ background: V, alignment: Alignment = .center) -> some View { _V(self) }
    public func onOpenURL(perform action: @escaping (URL) -> ()) -> some View { _V(self) }
    public func onContinueUserActivity(_ activityType: String, perform action: @escaping (NSUserActivity) -> ()) -> some View { _V(self) }
    public func userActivity(_ activityType: String, isActive: Bool = true, _ update: @escaping (NSUserActivity) -> ()) -> some View { _V(self) }
    public func onDrag(_ data: @escaping () -> NSItemProvider) -> some View { _V(self) }
    public func keyboardShortcut(_ key: KeyEquivalent, modifiers: EventModifiers = .command) -> some View { _V(self) }
    public func confirmationDialog<A: View, M: View>(_ titleKey: LocalizedStringKey, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic, @ViewBuilder actions: () -> A, @ViewBuilder message: () -> M) -> some View { _V(self) }
    public func searchable(text: Binding<String>, placement: SearchFieldPlacement = .automatic, prompt: LocalizedStringKey) -> some View { _V(self) }
    public func presentationDetents(_ detents: Set<PresentationDetent>, selection: Binding<PresentationDetent>) -> some View { _V(self) }
}
public struct KeyEquivalent: Sendable, ExpressibleByExtendedGraphemeClusterLiteral {
    public init(_ character: Character) {}
    public init(extendedGraphemeClusterLiteral value: Character) {}
}
public struct EventModifiers: OptionSet, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let command = EventModifiers(rawValue: 1), shift = EventModifiers(rawValue: 2), option = EventModifiers(rawValue: 4)
}

public struct ShareLink<Label: View>: _Leaf {
    public init(item: String, subject: Text? = nil, message: Text? = nil, @ViewBuilder label: () -> Label) {}
    public init(item: URL, subject: Text? = nil, message: Text? = nil, @ViewBuilder label: () -> Label) {}
}

extension Text {
    public enum DateStyle: Sendable { case time, date, relative, offset, timer }
    public init(_ date: Date, style: DateStyle) {}
}
extension LocalizedStringKey {
    public struct StringInterpolation: StringInterpolationProtocol {
        public init(literalCapacity: Int, interpolationCount: Int) {}
        public mutating func appendLiteral(_ literal: String) {}
        public mutating func appendInterpolation<T>(_ value: T) {}
        public mutating func appendInterpolation(_ date: Date, style: Text.DateStyle) {}
    }
    public init(stringInterpolation: StringInterpolation) {}
}
extension Image {
    public func interpolation(_ i: Interpolation) -> Image { self }
    public enum Interpolation: Sendable { case none, low, medium, high }
}
extension ProgressView where Label == Text, CurrentValueLabel == EmptyView {
    public init(_ titleKey: LocalizedStringKey) {}
}
extension Section where Parent == EmptyView, Content: View, Footer: View {
    public init(@ViewBuilder content: () -> Content, @ViewBuilder footer: () -> Footer) {}
}
extension Section where Parent: View, Content: View, Footer == EmptyView {
    public init(@ViewBuilder content: () -> Content, @ViewBuilder header: () -> Parent) {}
}
extension SecureField where Label == Text {
    public init(_ titleKey: LocalizedStringKey, text: Binding<String>) {}
}
extension ShapeStyle where Self == Color {
    public static var red: Color { .red }
    public static var black: Color { .black }
    public static var white: Color { .white }
}
public struct TintShapeStyle: ShapeStyle {}
extension ShapeStyle where Self == TintShapeStyle { public static var tint: TintShapeStyle { .init() } }
extension ShapeStyle where Self == Material { public static var regularMaterial: Material { .init() }; public static var thinMaterial: Material { .init() } }
public struct BackgroundStyle: ShapeStyle {}
extension ShapeStyle where Self == BackgroundStyle { public static var background: BackgroundStyle { .init() } }
public struct LabeledContent<Label: View, Content: View>: _Leaf {}
extension LabeledContent where Label == Text, Content: View {
    public init(_ titleKey: LocalizedStringKey, @ViewBuilder content: () -> Content) {}
}
extension Button where Label == SwiftUI.Label<Text, Image> {
    public init(_ titleKey: LocalizedStringKey, systemImage: String, action: @escaping () -> Void) {}
    public init(_ titleKey: LocalizedStringKey, systemImage: String, role: ButtonRole?, action: @escaping () -> Void) {}
}
extension Section where Parent == Text, Content: View, Footer == EmptyView {
    public init(_ titleKey: LocalizedStringKey, @ViewBuilder content: () -> Content) {}
}
extension ForEach where Content: View {
    public func onMove(perform action: ((IndexSet, Int) -> Void)?) -> some View { _V(self) }
}
extension View {
    public func scrollContentBackground(_ visibility: Visibility) -> some View { _V(self) }
    public func frame(maxWidth: CGFloat? = nil, maxHeight: CGFloat? = nil) -> some View { _V(self) }
}
extension DisclosureGroup where Label == Text {
    public init(_ titleKey: LocalizedStringKey, isExpanded: Binding<Bool>, @ViewBuilder content: @escaping () -> Content) {}
}
extension DisclosureGroup {
    public init(@ViewBuilder content: @escaping () -> Content, @ViewBuilder label: () -> Label) {}
}
extension SearchFieldPlacement {
    public enum NavigationBarDrawerDisplayMode: Sendable { case automatic, always }
    public static func navigationBarDrawer(displayMode: NavigationBarDrawerDisplayMode) -> SearchFieldPlacement { .automatic }
}
extension View {
    public func navigationTitle(_ title: Text) -> some View { _V(self) }
}
extension ToolbarContentBuilder {
    public static func buildIf<C: ToolbarContent>(_ content: C?) -> _ToolbarTuple<C?> { _ToolbarTuple(content) }
}
extension Optional: ToolbarContent where Wrapped: ToolbarContent {
    public typealias Body = Never
    public var body: Never { fatalError() }
}
extension Picker where Label == Text {
    public init(_ titleKey: LocalizedStringKey, selection: Binding<SelectionValue>, @ViewBuilder content: () -> Content) {}
}
extension Toggle where Label == Text {
    public init(_ titleKey: LocalizedStringKey, isOn: Binding<Bool>) {}
}
extension TextField where Label == Text {
    public init(_ titleKey: LocalizedStringKey, text: Binding<String>) {}
}
extension View {
    public func handlesExternalEvents(preferring: Set<String>, allowing: Set<String>) -> some View { _V(self) }
}
