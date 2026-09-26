// More SwiftUI for native/tools/app-stubcheck (signatures as the iOS 26 SDK documents them).
import Foundation
import Observation
import UIKit
import UniformTypeIdentifiers

@propertyWrapper @dynamicMemberLookup
public struct Bindable<Value: AnyObject & Observable>: DynamicProperty {
    public init(wrappedValue: Value) {}
    public init(_ wrappedValue: Value) {}
    public var wrappedValue: Value { fatalError() }
    public var projectedValue: Bindable<Value> { self }
    public subscript<Subject>(dynamicMember keyPath: ReferenceWritableKeyPath<Value, Subject>) -> Binding<Subject> { fatalError() }
}

@MainActor public struct UIViewRepresentableContext<Representable: UIViewRepresentable> {
    public var coordinator: Representable.Coordinator { fatalError() }
}
@MainActor @preconcurrency public protocol UIViewRepresentable: View where Body == Never {
    associatedtype UIViewType: UIView
    associatedtype Coordinator = Void
    typealias Context = UIViewRepresentableContext<Self>
    func makeUIView(context: Context) -> UIViewType
    func updateUIView(_ uiView: UIViewType, context: Context)
    func makeCoordinator() -> Coordinator
    static func dismantleUIView(_ uiView: UIViewType, coordinator: Coordinator)
}
extension UIViewRepresentable where Coordinator == Void { public func makeCoordinator() -> Void {} }
extension UIViewRepresentable {
    public static func dismantleUIView(_ uiView: UIViewType, coordinator: Coordinator) {}
    public var body: Never { fatalError() }
}
@MainActor public struct UIViewControllerRepresentableContext<Representable: UIViewControllerRepresentable> {
    public var coordinator: Representable.Coordinator { fatalError() }
}
@MainActor @preconcurrency public protocol UIViewControllerRepresentable: View where Body == Never {
    associatedtype UIViewControllerType: UIViewController
    associatedtype Coordinator = Void
    typealias Context = UIViewControllerRepresentableContext<Self>
    func makeUIViewController(context: Context) -> UIViewControllerType
    func updateUIViewController(_ vc: UIViewControllerType, context: Context)
    func makeCoordinator() -> Coordinator
}
extension UIViewControllerRepresentable where Coordinator == Void { public func makeCoordinator() -> Void {} }
extension UIViewControllerRepresentable { public var body: Never { fatalError() } }

extension OpenURLAction {
    public func callAsFunction(_ url: URL) {}
}
extension EnvironmentValues {
    public var xbinStubOpenURLKeyPathDummy: Int { 0 }
}

extension ShapeStyle where Self == Material {
    public static var thinMaterial: Material { .init() }
    public static var ultraThinMaterial: Material { .init() }
}
public struct BackgroundStyle: ShapeStyle { public init() {} }
extension ShapeStyle where Self == BackgroundStyle { public static var background: BackgroundStyle { .init() } }
extension Color {
    public static let green = Color(white: 0), orange = Color(white: 0)
}

extension Button where Label == SwiftUI.Label<Text, Image> {
    public init(_ titleKey: LocalizedStringKey, systemImage: String, action: @escaping () -> Void) {}
    public init(_ titleKey: LocalizedStringKey, systemImage: String, role: ButtonRole?, action: @escaping () -> Void) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, systemImage: String, action: @escaping () -> Void) {}
}

extension View {
    public func allowsHitTesting(_ enabled: Bool) -> some View { _V(self) }
    public func navigationTitle(_ title: Text) -> some View { _V(self) }
    public func animation<V: Equatable>(_ animation: Animation?, value: V) -> some View { _V(self) }
    public func accessibilityHint(_ hint: LocalizedStringKey) -> some View { _V(self) }
    public func task(priority: TaskPriority = .userInitiated, @_inheritActorContext _ action: @escaping @Sendable () async -> Void) -> some View { _V(self) }
    public func confirmationDialog<S: StringProtocol, A: View>(_ title: S, isPresented: Binding<Bool>, titleVisibility: Visibility = .automatic,
                                                             @ViewBuilder actions: () -> A) -> some View { _V(self) }
    public func alert<S: StringProtocol, A: View>(_ title: S, isPresented: Binding<Bool>, @ViewBuilder actions: () -> A) -> some View { _V(self) }
    public func alert<S: StringProtocol, A: View, M: View>(_ title: S, isPresented: Binding<Bool>, @ViewBuilder actions: () -> A,
                                                        @ViewBuilder message: () -> M) -> some View { _V(self) }
    public func sheet<Item: Identifiable, Content: View>(item: Binding<Item?>, onDismiss: (() -> Void)? = nil,
                                                        @ViewBuilder content: @escaping (Item) -> Content) -> some View { _V(self) }
    public func fileImporter(isPresented: Binding<Bool>, allowedContentTypes: [UTType], allowsMultipleSelection: Bool,
                             onCompletion: @escaping (Result<[URL], any Error>) -> Void,
                             onCancellation: @escaping () -> Void) -> some View { _V(self) }
    public func toolbar(_ visibility: Visibility, for bars: ToolbarPlacementStub...) -> some View { _V(self) }
}
public struct ToolbarPlacementStub: Sendable { public static let navigationBar = ToolbarPlacementStub() }
extension View {
    public func onDisappear(perform action: (() -> Void)? = nil) -> some View { _V(self) }
}
extension ShapeStyle where Self == Color {
    public static var orange: Color { .orange }
    public static var red: Color { .red }
}
extension Section where Parent == Text, Content: View, Footer == EmptyView {
    public init<S: StringProtocol>(_ title: S, @ViewBuilder content: () -> Content) {}
}
extension View {
    public func interactiveDismissDisabled(_ disabled: Bool = true) -> some View { _V(self) }
}
