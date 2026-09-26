// MARK: Controls

public struct ButtonRole: Sendable, Equatable { public static let destructive = ButtonRole(), cancel = ButtonRole() }

public struct Button<Label: View>: _Leaf {
    public init(action: @escaping () -> Void, @ViewBuilder label: () -> Label) {}
    public init(role: ButtonRole?, action: @escaping () -> Void, @ViewBuilder label: () -> Label) {}
}
extension Button where Label == Text {
    public init(_ titleKey: LocalizedStringKey, action: @escaping () -> Void) {}
    public init(_ titleKey: LocalizedStringKey, role: ButtonRole?, action: @escaping () -> Void) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, action: @escaping () -> Void) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, role: ButtonRole?, action: @escaping () -> Void) {}
}

public struct Toggle<Label: View>: _Leaf {
    public init(isOn: Binding<Bool>, @ViewBuilder label: () -> Label) {}
}

public struct Picker<Label: View, SelectionValue: Hashable, Content: View>: _Leaf {
    public init(selection: Binding<SelectionValue>, @ViewBuilder content: () -> Content, @ViewBuilder label: () -> Label) {}
}

public struct Menu<Label: View, Content: View>: _Leaf {
    public init(@ViewBuilder content: () -> Content, @ViewBuilder label: () -> Label) {}
}

public struct TextField<Label: View>: _Leaf {
    public init(text: Binding<String>, prompt: Text? = nil, @ViewBuilder label: () -> Label) {}
    public init(text: Binding<String>, prompt: Text? = nil, axis: Axis, @ViewBuilder label: () -> Label) {}
}
extension TextField where Label == Text {
    public init(_ titleKey: LocalizedStringKey, text: Binding<String>, axis: Axis) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, text: Binding<String>, axis: Axis) {}
}

public struct SecureField<Label: View>: _Leaf {
    public init(text: Binding<String>, prompt: Text? = nil, @ViewBuilder label: () -> Label) {}
}

public struct DatePickerComponents: OptionSet, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let date = DatePickerComponents(rawValue: 1), hourAndMinute = DatePickerComponents(rawValue: 2)
}
public struct DatePicker<Label: View>: _Leaf {
    public typealias Components = DatePickerComponents
    public init(selection: Binding<Date>, displayedComponents: DatePickerComponents = [.date, .hourAndMinute], @ViewBuilder label: () -> Label) {}
}

public struct DisclosureGroup<Label: View, Content: View>: _Leaf {
    public init(isExpanded: Binding<Bool>, @ViewBuilder content: @escaping () -> Content, @ViewBuilder label: () -> Label) {}
}

public struct ProgressView<Label: View, CurrentValueLabel: View>: _Leaf {}
extension ProgressView where Label == EmptyView, CurrentValueLabel == EmptyView {
    public init() {}
    public init<V: BinaryFloatingPoint>(value: V?, total: V = 1.0) {}
}

public struct ContentUnavailableView<Label: View, Description: View, Actions: View>: _Leaf {
    public init(@ViewBuilder label: () -> Label, @ViewBuilder description: () -> Description = { EmptyView() }, @ViewBuilder actions: () -> Actions = { EmptyView() }) {}
}
extension ContentUnavailableView where Label == SwiftUI.Label<Text, Image>, Description == Text?, Actions == EmptyView {
    public init(_ title: LocalizedStringKey, systemImage name: String, description: Text? = nil) {}
}

// MARK: Navigation

public struct NavigationStack<Data, Root: View>: _Leaf {}
extension NavigationStack where Data == NavigationPath {
    public init(@ViewBuilder root: () -> Root) {}
}
extension NavigationStack {
    public init(path: Binding<Data>, @ViewBuilder root: () -> Root) where Data: MutableCollection, Data: RandomAccessCollection, Data: RangeReplaceableCollection, Data.Element: Hashable {}
}
public struct NavigationPath {}
public struct NavigationSplitView<Sidebar: View, Content: View, Detail: View>: _Leaf {}
extension NavigationSplitView where Content == EmptyView {
    public init(@ViewBuilder sidebar: () -> Sidebar, @ViewBuilder detail: () -> Detail) {}
}
public struct BalancedNavigationSplitViewStyle: Sendable {}
extension BalancedNavigationSplitViewStyle { public static var balanced: BalancedNavigationSplitViewStyle { .init() } }

public enum NavigationBarItem { public enum TitleDisplayMode: Sendable { case automatic, inline, large } }

public struct TabView<SelectionValue: Hashable, Content: View>: _Leaf {
    public init(selection: Binding<SelectionValue>?, @ViewBuilder content: () -> Content) {}
}

public struct PresentationDetent: Hashable, Sendable { public static let medium = PresentationDetent(), large = PresentationDetent() }

public struct SubmitLabel: Sendable { public static let done = SubmitLabel(), go = SubmitLabel(), send = SubmitLabel(), join = SubmitLabel(), route = SubmitLabel(), search = SubmitLabel(), `return` = SubmitLabel(), next = SubmitLabel(), `continue` = SubmitLabel() }
public struct TextInputAutocapitalization: Sendable { public static let never = TextInputAutocapitalization(), words = TextInputAutocapitalization(), sentences = TextInputAutocapitalization(), characters = TextInputAutocapitalization() }

public struct AccessibilityTraits: OptionSet, Sendable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let isHeader = AccessibilityTraits(rawValue: 1), isButton = AccessibilityTraits(rawValue: 2), isImage = AccessibilityTraits(rawValue: 4)
}
public struct AccessibilityChildBehavior: Sendable { public static let combine = AccessibilityChildBehavior(), ignore = AccessibilityChildBehavior(), contain = AccessibilityChildBehavior() }

public struct ControlSize: Sendable { public static let mini = ControlSize(), small = ControlSize(), regular = ControlSize(), large = ControlSize() }
public struct ButtonBorderShape: Sendable { public static let capsule = ButtonBorderShape(), circle = ButtonBorderShape(), roundedRectangle = ButtonBorderShape() }

public protocol TextSelectability {}
public struct EnabledTextSelectability: TextSelectability {}
public struct DisabledTextSelectability: TextSelectability {}
extension TextSelectability where Self == EnabledTextSelectability { public static var enabled: EnabledTextSelectability { .init() } }
extension TextSelectability where Self == DisabledTextSelectability { public static var disabled: DisabledTextSelectability { .init() } }
