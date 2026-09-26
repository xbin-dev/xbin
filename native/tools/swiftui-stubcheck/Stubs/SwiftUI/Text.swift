// MARK: Text, Font, Color, styles

public struct LocalizedStringKey: ExpressibleByStringInterpolation, Sendable {
    public init(stringLiteral value: String) {}
    public init(stringInterpolation: DefaultStringInterpolation) {}
}

public struct Text: Equatable, Sendable {
    public init(verbatim content: String) {}
    public init(_ key: LocalizedStringKey) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ content: S) {}
    @_disfavoredOverload public init(_ attributed: AttributedString) {}
    public func font(_ font: Font?) -> Text { self }
    public func fontWeight(_ weight: Font.Weight?) -> Text { self }
    public func bold() -> Text { self }
    public func italic() -> Text { self }
    public func monospaced(_ isActive: Bool = true) -> Text { self }
    public func monospacedDigit() -> Text { self }
    public func foregroundStyle<S: ShapeStyle>(_ style: S) -> Text { self }
    public enum TruncationMode: Sendable { case head, middle, tail }
    public struct LineStyle: Sendable, Hashable { public static let single = LineStyle() }
}

public struct Font: Sendable, Hashable {
    public static let largeTitle = Font(), title = Font(), title2 = Font(), title3 = Font(), headline = Font(), body = Font(),
        callout = Font(), subheadline = Font(), footnote = Font(), caption = Font(), caption2 = Font()
    public enum TextStyle: Sendable { case largeTitle, title, title2, title3, headline, body, callout, subheadline, footnote, caption, caption2 }
    public enum Design: Sendable { case `default`, monospaced, rounded, serif }
    public struct Weight: Sendable, Hashable { public static let regular = Weight(), medium = Weight(), semibold = Weight(), bold = Weight() }
    public static func system(_ style: TextStyle, design: Design? = nil, weight: Weight? = nil) -> Font { Font() }
    public static func system(size: CGFloat, weight: Weight? = nil, design: Design? = nil) -> Font { Font() }
    public func bold() -> Font { self }
    public func italic() -> Font { self }
    public func monospaced() -> Font { self }
    public func monospacedDigit() -> Font { self }
    public func weight(_ weight: Weight) -> Font { self }
}

public protocol ShapeStyle: Sendable {}

public struct Color: ShapeStyle, Hashable {
    public init(uiColor: UIColor) {}
    public init(white: Double, opacity: Double = 1) {}
    public static let clear = Color(white: 0), black = Color(white: 0), white = Color(white: 1), primary = Color(white: 0),
        secondary = Color(white: 0), accentColor = Color(white: 0), blue = Color(white: 0), red = Color(white: 0)
    public func opacity(_ opacity: Double) -> Color { self }
}
extension Color: _Leaf {}
extension Text: _Leaf {}
extension Image: _Leaf {}

public struct HierarchicalShapeStyle: ShapeStyle { public static let primary = HierarchicalShapeStyle(), secondary = HierarchicalShapeStyle(), tertiary = HierarchicalShapeStyle(), quaternary = HierarchicalShapeStyle() }
extension ShapeStyle where Self == HierarchicalShapeStyle {
    public static var primary: HierarchicalShapeStyle { .init() }
    public static var secondary: HierarchicalShapeStyle { .init() }
    public static var tertiary: HierarchicalShapeStyle { .init() }
    public static var quaternary: HierarchicalShapeStyle { .init() }
}
public struct Material: ShapeStyle { public static let bar = Material(), regular = Material() }
extension ShapeStyle where Self == Material { public static var bar: Material { .init() } }
extension ShapeStyle where Self == Color {
    public static var clear: Color { .clear }
}

public struct Image: Equatable, Sendable {
    public init(systemName: String) {}
    public init(uiImage: UIImage) {}
    public func resizable() -> Image { self }
    public enum ContentMode: Sendable { case fit, fill }
}
extension View {
    public func scaledToFit() -> some View { _V(self) }
    public func scaledToFill() -> some View { _V(self) }
}

public struct Label<Title: View, Icon: View>: _Leaf {
    public init(@ViewBuilder title: () -> Title, @ViewBuilder icon: () -> Icon) {}
}
extension Label where Title == Text, Icon == Image {
    public init(_ titleKey: LocalizedStringKey, systemImage name: String) {}
    @_disfavoredOverload public init<S: StringProtocol>(_ title: S, systemImage name: String) {}
}

// MARK: Shapes

public protocol Shape: _Leaf, Sendable {}
extension Shape {
    public func fill<S: ShapeStyle>(_ content: S) -> some View { _V(self) }
    public func strokeBorder<S: ShapeStyle>(_ content: S, lineWidth: CGFloat = 1) -> some View { _V(self) }
    public func strokeBorder<S: ShapeStyle>(_ content: S, style: StrokeStyle) -> some View { _V(self) }
}
public enum RoundedCornerStyle: Sendable { case circular, continuous }
public struct RoundedRectangle: Shape { public init(cornerRadius: CGFloat, style: RoundedCornerStyle = .continuous) {} }
public struct UnevenRoundedRectangle: Shape {
    public init(topLeadingRadius: CGFloat = 0, bottomLeadingRadius: CGFloat = 0, bottomTrailingRadius: CGFloat = 0,
                topTrailingRadius: CGFloat = 0, style: RoundedCornerStyle = .continuous) {}
}
public struct Capsule: Shape { public init() {} }
public struct Circle: Shape { public init() {} }
public struct Rectangle: Shape { public init() {} }
public struct StrokeStyle: Sendable { public init(lineWidth: CGFloat = 1, dash: [CGFloat] = []) {} }
