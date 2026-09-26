// MARK: State, Binding, Environment

public protocol DynamicProperty {}

@propertyWrapper
public struct State<Value>: DynamicProperty {
    public init(wrappedValue value: Value) {}
    public init(initialValue value: Value) {}
    public var wrappedValue: Value {
        get { fatalError() }
        nonmutating set {}
    }
    public var projectedValue: Binding<Value> { fatalError() }
}
extension State: Sendable where Value: Sendable {}

@propertyWrapper @dynamicMemberLookup
public struct Binding<Value> {
    public init(get: @escaping () -> Value, set: @escaping (Value) -> Void) {}
    public init(projectedValue: Binding<Value>) {}
    public static func constant(_ value: Value) -> Binding<Value> { fatalError() }
    public var wrappedValue: Value {
        get { fatalError() }
        nonmutating set {}
    }
    public var projectedValue: Binding<Value> { self }
    public subscript<Subject>(dynamicMember keyPath: WritableKeyPath<Value, Subject>) -> Binding<Subject> { fatalError() }
}

@propertyWrapper
public struct FocusState<Value: Hashable>: DynamicProperty {
    public init() where Value == Bool {}
    public var wrappedValue: Value { get { fatalError() } nonmutating set {} }
    public var projectedValue: Binding { Binding() }
    public struct Binding {
        public var wrappedValue: Value { get { fatalError() } nonmutating set {} }
    }
}

public protocol EnvironmentKey {
    associatedtype Value
    static var defaultValue: Value { get }
}

public struct EnvironmentValues {
    public subscript<K: EnvironmentKey>(key: K.Type) -> K.Value {
        get { K.defaultValue }
        set {}
    }
    public var colorScheme: ColorScheme { get { .light } set {} }
    public var dynamicTypeSize: DynamicTypeSize { get { .large } set {} }
    public var horizontalSizeClass: UserInterfaceSizeClass? { get { nil } set {} }
    public var scenePhase: ScenePhase { get { .active } set {} }
    public var timeZone: TimeZone { get { .current } set {} }
    public var calendar: Calendar { get { .current } set {} }
    public var openURL: OpenURLAction { get { fatalError() } set {} }
    public var dismiss: DismissAction { fatalError() }
}

@MainActor @preconcurrency
public struct DismissAction {
    public func callAsFunction() {}
}

public struct OpenURLAction {
    public struct Result: Sendable {
        public static let handled = Result(), discarded = Result(), systemAction = Result()
    }
    public init(handler: @escaping (URL) -> Result) {}
}

@propertyWrapper
public struct Environment<Value>: DynamicProperty {
    public init(_ keyPath: KeyPath<EnvironmentValues, Value>) {}
    public var wrappedValue: Value { fatalError() }
}

// MARK: Animation

public struct Animation: Sendable { public static let snappy = Animation(), `default` = Animation() }
public func withAnimation<Result>(_ animation: Animation? = .default, _ body: () throws -> Result) rethrows -> Result { try body() }
