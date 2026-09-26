// Stubs for the renderer's interaction pieces: gestures, transitions and
// animation, geometry, UIKit representables, the iOS 26 toolbar spacer,
// menus' styles and order, and (behind XBIN_SDK_27_1) the iPhone Duo's
// ArrangementView as the design names it.

// MARK: Geometry

public struct GeometryProxy {
    public var size: CGSize { .zero }
}
public struct GeometryReader<Content: View>: _Leaf {
    public init(@ViewBuilder content: @escaping (GeometryProxy) -> Content) {}
}

// MARK: Gestures

@MainActor @preconcurrency public protocol Gesture {
    associatedtype Value
}
public struct _Gesture<V>: Gesture { public typealias Value = V }
public struct DragGesture: Gesture {
    public struct Value: Equatable, Sendable {
        public var translation: CGSize
        public var predictedEndTranslation: CGSize
        public var location: CGPoint
        public var startLocation: CGPoint
    }
    public enum CoordinateSpaceKind: Sendable { case local, global }
    public init(minimumDistance: CGFloat = 10) {}
}
extension Gesture {
    public func onChanged(_ action: @escaping (Value) -> Void) -> _Gesture<Value> { _Gesture() }
    public func onEnded(_ action: @escaping (Value) -> Void) -> _Gesture<Value> { _Gesture() }
}

// MARK: Transitions and animation

public struct AnyTransition: Sendable {
    public static let opacity = AnyTransition()
    public static func move(edge: Edge) -> AnyTransition { AnyTransition() }
}
extension Animation {
    public static func snappy(duration: TimeInterval = 0.5, extraBounce: Double = 0) -> Animation { Animation() }
}

// MARK: Accessibility

public struct AccessibilityActionKind: Sendable { public static let `default` = AccessibilityActionKind(), escape = AccessibilityActionKind(), magicTap = AccessibilityActionKind() }
extension AccessibilityTraits {
    public static let isModal = AccessibilityTraits(rawValue: 8)
}

// MARK: Menus

public protocol MenuStyle {}
public struct _MenuStyle: MenuStyle {}
extension MenuStyle where Self == _MenuStyle {
    public static var button: _MenuStyle { .init() }
    public static var automatic: _MenuStyle { .init() }
}
public struct MenuOrder: Sendable { public static let automatic = MenuOrder(), fixed = MenuOrder(), priority = MenuOrder() }

extension View {
    public func gesture<T: Gesture>(_ gesture: T) -> some View { _V(self) }
    public func transition(_ t: AnyTransition) -> some View { _V(self) }
    public func animation<V: Equatable>(_ animation: Animation?, value: V) -> some View { _V(self) }
    public func offset(x: CGFloat = 0, y: CGFloat = 0) -> some View { _V(self) }
    public func shadow(color: Color = Color(white: 0, opacity: 0.33), radius: CGFloat, x: CGFloat = 0, y: CGFloat = 0) -> some View { _V(self) }
    public func allowsHitTesting(_ enabled: Bool) -> some View { _V(self) }
    public func safeAreaPadding(_ edges: Edge.Set = .all, _ length: CGFloat? = nil) -> some View { _V(self) }
    public func accessibilityHint(_ hint: LocalizedStringKey) -> some View { _V(self) }
    @_disfavoredOverload public func accessibilityHint<S: StringProtocol>(_ hint: S) -> some View { _V(self) }
    public func accessibilityAction(_ actionKind: AccessibilityActionKind = .default, _ handler: @escaping () -> Void) -> some View { _V(self) }
    public func menuStyle<S: MenuStyle>(_ style: S) -> some View { _V(self) }
    public func menuOrder(_ order: MenuOrder) -> some View { _V(self) }
}

extension EnvironmentValues {
    public var isEnabled: Bool { get { true } set {} }
}

// MARK: Toolbar (iOS 26)

public struct SpacerSizing: Sendable { public static let fixed = SpacerSizing(), flexible = SpacerSizing() }
public struct ToolbarSpacer: _ToolbarLeaf {
    public init(_ sizing: SpacerSizing = .flexible, placement: ToolbarItemPlacement = .automatic) {}
}
extension Optional: ToolbarContent where Wrapped: ToolbarContent {
    public typealias Body = Never
    @_disfavoredOverload public var body: Never { fatalError() }
}
public struct _ToolbarConditional<T, F>: _ToolbarLeaf {}
extension ToolbarContentBuilder {
    public static func buildIf<C: ToolbarContent>(_ content: C?) -> C? { content }
    public static func buildEither<T: ToolbarContent, F: ToolbarContent>(first: T) -> _ToolbarConditional<T, F> { _ToolbarConditional() }
    public static func buildEither<T: ToolbarContent, F: ToolbarContent>(second: F) -> _ToolbarConditional<T, F> { _ToolbarConditional() }
}

// MARK: UIKit views in SwiftUI

public struct UIViewRepresentableContext<Representable: UIViewRepresentable> {
    public var coordinator: Representable.Coordinator { fatalError() }
    public var environment: EnvironmentValues { fatalError() }
}
@MainActor @preconcurrency public protocol UIViewRepresentable: View where Body == Never {
    associatedtype UIViewType: UIView
    associatedtype Coordinator = Void
    typealias Context = UIViewRepresentableContext<Self>
    func makeUIView(context: Context) -> UIViewType
    func updateUIView(_ uiView: UIViewType, context: Context)
    func makeCoordinator() -> Coordinator
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UIViewType, context: Context) -> CGSize?
}
extension UIViewRepresentable {
    public var body: Never { fatalError() }
    public func sizeThatFits(_ proposal: ProposedViewSize, uiView: UIViewType, context: Context) -> CGSize? { nil }
}
extension UIViewRepresentable where Coordinator == Void {
    public func makeCoordinator() {}
}

// MARK: iPhone Duo (iOS 27.1 SDK, XBIN_SDK_27_1) — the design's name, unconfirmed

@available(iOS 27.1, *)
public struct ArrangementView<Content: View>: _Leaf {
    public init(@ViewBuilder content: () -> Content) {}
}
