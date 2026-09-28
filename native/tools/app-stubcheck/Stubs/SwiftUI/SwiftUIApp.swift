// The SwiftUI the app uses beyond swiftui-stubcheck's (the renderer's) and
// term-stubcheck's (the terminal's) stubs — declarations from memory of the
// SDK, like the rest of these stubs.
import Observation
import UniformTypeIdentifiers

// Observable objects in the environment.
extension Environment where Value: AnyObject & Observable {
    public init(_ objectType: Value.Type) {}
}
extension View {
    public func environment<T: AnyObject & Observable>(_ object: T?) -> some View { _V(self) }
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

// UIKit view controllers in SwiftUI (views: swiftui-stubcheck's Interaction.swift).
@MainActor public struct UIViewControllerRepresentableContext<Representable: UIViewControllerRepresentable> {
    public var coordinator: Representable.Coordinator { fatalError() }
}
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

extension AnyTransition {
    public func combined(with other: AnyTransition) -> AnyTransition { self }
}

extension View {
    public func sheet<Item: Identifiable, Content: View>(item: Binding<Item?>, onDismiss: (() -> Void)? = nil,
                                                        @ViewBuilder content: @escaping (Item) -> Content) -> some View { _V(self) }
    public func zIndex(_ value: Double) -> some View { _V(self) }
    public func blur(radius: CGFloat, opaque: Bool = false) -> some View { _V(self) }
    // Deprecated in the SDK, so a Color (a view and a style) takes the style overload.
    @_disfavoredOverload public func background<V: View>(_ background: V, alignment: Alignment = .center) -> some View { _V(self) }
    public func onOpenURL(perform action: @escaping (URL) -> ()) -> some View { _V(self) }
    public func onContinueUserActivity(_ activityType: String, perform action: @escaping (NSUserActivity) -> ()) -> some View { _V(self) }
    public func userActivity(_ activityType: String, isActive: Bool = true, _ update: @escaping (NSUserActivity) -> ()) -> some View { _V(self) }
    public func onDrag(_ data: @escaping () -> NSItemProvider) -> some View { _V(self) }
    public func searchable(text: Binding<String>, placement: SearchFieldPlacement = .automatic, prompt: LocalizedStringKey) -> some View { _V(self) }
    public func presentationDetents(_ detents: Set<PresentationDetent>, selection: Binding<PresentationDetent>) -> some View { _V(self) }
    public func scrollContentBackground(_ visibility: Visibility) -> some View { _V(self) }
    public func handlesExternalEvents(preferring: Set<String>, allowing: Set<String>) -> some View { _V(self) }
    public func interactiveDismissDisabled(_ disabled: Bool = true) -> some View { _V(self) }
    public func fileImporter(isPresented: Binding<Bool>, allowedContentTypes: [UTType], allowsMultipleSelection: Bool,
                             onCompletion: @escaping (Result<[URL], any Error>) -> Void,
                             onCancellation: @escaping () -> Void) -> some View { _V(self) }
    public func confirmationDialog<S: StringProtocol, A: View>(_ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
                                                             @ViewBuilder actions: () -> A) -> some View { _V(self) }
    @_disfavoredOverload public func alert<S: StringProtocol, A: View>(_ title: S, isPresented: Binding<Bool>, @ViewBuilder actions: () -> A) -> some View { _V(self) }
    @_disfavoredOverload public func alert<S: StringProtocol, A: View, M: View>(_ title: S, isPresented: Binding<Bool>, @ViewBuilder actions: () -> A,
                                                                             @ViewBuilder message: () -> M) -> some View { _V(self) }
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
extension SecureField where Label == Text {
    public init(_ titleKey: LocalizedStringKey, text: Binding<String>) {}
}
extension Color { public static let green = Color(white: 0) }
extension ShapeStyle where Self == Color {
    public static var white: Color { .white }
}
extension ShapeStyle where Self == Material { public static var thinMaterial: Material { .init() } }
public struct BackgroundStyle: ShapeStyle { public init() {} }
extension ShapeStyle where Self == BackgroundStyle { public static var background: BackgroundStyle { .init() } }
public struct LabeledContent<Label: View, Content: View>: _Leaf {}
extension LabeledContent where Label == Text, Content: View {
    public init(_ titleKey: LocalizedStringKey, @ViewBuilder content: () -> Content) {}
}
extension Button where Label == SwiftUI.Label<Text, Image> {
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, systemImage: String, action: @escaping () -> Void) {}
}
extension ForEach where Content: View {
    public func onMove(perform action: ((IndexSet, Int) -> Void)?) -> some View { _V(self) }
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

// The onboarding (Shell/Onboarding.swift): the busy overlay's words, the
// xbin mark drawn as shapes.
extension ProgressView where Label == Text, CurrentValueLabel == EmptyView {
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S) {}
}
extension Color {
    public init(red: Double, green: Double, blue: Double, opacity: Double = 1) { self.init(white: 0) }
}
public struct Path: Shape {
    public init() {}
    public mutating func move(to point: CGPoint) {}
    public mutating func addLine(to point: CGPoint) {}
    public mutating func addArc(center: CGPoint, radius: CGFloat, startAngle: Angle, endAngle: Angle, clockwise: Bool) {}
    public mutating func addEllipse(in rect: CGRect) {}
    public mutating func closeSubpath() {}
}

// The panels, Home and the screens (Shell/PanelStack.swift, Shell/Screens).
extension TextField where Label == Text {
    public init(_ titleKey: LocalizedStringKey, text: Binding<String>, prompt: Text?) {}
}
extension Text {
    public enum Case: Sendable { case uppercase, lowercase }
}
extension View {
    public func textCase(_ textCase: Text.Case?) -> some View { _V(self) }
}
extension View {
    public func accessibilityIdentifier(_ identifier: String) -> some View { _V(self) }
    public func accessibilityValue(_ value: Text) -> some View { _V(self) }
    public func accessibilityAction(_ handler: @escaping () -> Void) -> some View { _V(self) }
    public func symbolRenderingMode(_ mode: SymbolRenderingMode?) -> some View { _V(self) }
}
public struct SymbolRenderingMode: Sendable {
    public static let monochrome = SymbolRenderingMode(), hierarchical = SymbolRenderingMode(), palette = SymbolRenderingMode()
}
extension Image {
    public func symbolRenderingMode(_ mode: SymbolRenderingMode?) -> Image { self }
}
public enum EditMode: Sendable, Hashable { case inactive, transient, active }
extension EnvironmentValues {
    public var editMode: Binding<EditMode>? { get { nil } set {} }
    public var defaultMinListRowHeight: CGFloat { get { 44 } set {} }
}
// Compact lists (Shell/Screens/HomeView.swift, D128): the sidebar style
// and its foldable sections.
extension ListStyle where Self == _ListStyle {
    public static var sidebar: _ListStyle { .init() }
}
extension Section where Parent: View, Content: View, Footer == EmptyView {
    public init(isExpanded: Binding<Bool>, @ViewBuilder content: () -> Content, @ViewBuilder header: () -> Parent) {}
}
extension MutableCollection where Self: RangeReplaceableCollection {
    public mutating func move(fromOffsets source: IndexSet, toOffset destination: Int) {}
}
extension Animation {
    public static func smooth(duration: TimeInterval = 0.5, extraBounce: Double = 0) -> Animation { Animation() }
}
public enum AnimationCompletionCriteria: Sendable { case logicallyComplete, removed }
public func withAnimation<Result>(_ animation: Animation? = .default, completionCriteria: AnimationCompletionCriteria = .logicallyComplete,
                                  _ body: () throws -> Result, completion: @escaping () -> Void) rethrows -> Result { try body() }
public struct Transaction {
    public init() {}
    public init(animation: Animation?) {}
    public var animation: Animation?
    public var disablesAnimations = false
}
public func withTransaction<Result>(_ transaction: Transaction, _ body: () throws -> Result) rethrows -> Result { try body() }
extension Animation {
    public static func linear(duration: TimeInterval) -> Animation { Animation() }
    public func delay(_ delay: TimeInterval) -> Animation { self }
}
