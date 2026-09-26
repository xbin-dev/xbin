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
