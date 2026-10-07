import Foundation
import XbinCore

/// How a `screen` splits its children (the reference renderer's rules,
/// web/xb/render-structure.js): its `toolbar`s go to the navigation bar's
/// trailing end, its leading end or a bottom toolbar by `place` (rev 2); a
/// `composer` and a bar-style `tabs` dock at the bottom; `sheet`s present
/// over it — bottom sheets modally, drawers (`edge="leading"`) as an
/// overlay; the rest is the body.
@MainActor
public struct ScreenLayout {
    public enum Style: String, Sendable { case list, form, scroll }

    public let style: Style
    /// The trailing toolbar (`place` absent or `trailing`; the first such).
    public let toolbar: XbinNode?
    /// `toolbar place="leading"`: items at the bar's leading end.
    public let leadingToolbar: XbinNode?
    /// `toolbar place="bottom"`: a bottom toolbar.
    public let bottomToolbar: XbinNode?
    public let docked: [XbinNode]
    /// Bottom sheets (presented modally).
    public let sheets: [XbinNode]
    /// Drawers: sheets from the leading edge (an overlay over the screen).
    public let drawers: [XbinNode]
    public let body: [XbinNode]

    public init(_ screen: XbinNode) {
        style = Style(rawValue: screen.props.string("style") ?? "") ?? .scroll
        let all = screen.children
        toolbar = all.first { $0.type == "toolbar" && ToolbarPlace($0) == .trailing }
        leadingToolbar = all.first { $0.type == "toolbar" && ToolbarPlace($0) == .leading }
        bottomToolbar = all.first { $0.type == "toolbar" && ToolbarPlace($0) == .bottom }
        let dock = all.filter { $0.type == "composer" || ($0.type == "tabs" && $0.props.string("style") == "bar") }
        docked = dock
        sheets = all.filter { $0.type == "sheet" && !SheetProps.isDrawer($0) }
        drawers = all.filter(SheetProps.isDrawer)
        body = all.filter { n in n.type != "toolbar" && n.type != "sheet" && !dock.contains { $0 === n } }
    }

    /// A list or form screen: the body is a grouped `List`/`Form`.
    public var isGrouped: Bool { style != .scroll }

    /// The body holds a transcript: it scrolls itself (no outer scroll view),
    /// and the composer docks below it.
    public var isChat: Bool { body.contains { $0.type == "transcript" } }

    /// A scroll screen whose whole body is one `list`: that list becomes the
    /// screen's (lazy, full-bleed) body instead of a card in a scroll view.
    public var soleList: XbinNode? {
        guard style == .scroll, body.count == 1, body[0].type == "list" else { return nil }
        return body[0]
    }

    /// Whether the title shows large: `large` when given; else large on a
    /// stack's root list/form screen, inline when pushed, in a sheet, or on a
    /// scroll screen.
    public static func largeTitle(_ screen: XbinNode, pushed: Bool, inSheet: Bool) -> Bool {
        if let l = screen.props.optionalBool("large") { return l }
        let style = Style(rawValue: screen.props.string("style") ?? "") ?? .scroll
        return !pushed && !inSheet && style != .scroll
    }
}

/// A `list`'s children in runs: consecutive non-section children share one
/// group; each `section` is its own (the reference renderer's grouping).
@MainActor
public enum ListRuns {
    public enum Run {
        case rows([XbinNode])
        case section(XbinNode)
    }

    public static func runs(_ children: [XbinNode]) -> [Run] {
        var out: [Run] = []
        for c in children {
            if c.type == "section" {
                out.append(.section(c))
            } else if case .rows(let rs)? = out.last {
                out[out.count - 1] = .rows(rs + [c])
            } else {
                out.append(.rows([c]))
            }
        }
        return out
    }
}

/// A `fragment`'s (or the root's) children: sheets present over the rest
/// (bottom sheets modally, drawers as an overlay).
@MainActor
public struct FragmentLayout {
    public let content: [XbinNode]
    /// Bottom sheets.
    public let sheets: [XbinNode]
    /// Sheets from the leading edge.
    public let drawers: [XbinNode]

    public init(_ children: [XbinNode]) {
        content = children.filter { $0.type != "sheet" }
        sheets = children.filter { $0.type == "sheet" && !SheetProps.isDrawer($0) }
        drawers = children.filter(SheetProps.isDrawer)
    }

    /// The drawers of a `nav`'s screens: the navigation container draws
    /// them over itself (bar included), so a screen's drawer covers the
    /// whole stack like the reference renderer's.
    public static func drawers(ofScreens screens: [XbinNode]) -> [XbinNode] {
        screens.flatMap { $0.children.filter(SheetProps.isDrawer) }
    }
}

/// Where a `toolbar`'s items go (rev 2 `place`): the bar's trailing end (the
/// default, and for an unknown value), its leading end, or a bottom toolbar.
public enum ToolbarPlace: String, Sendable, CaseIterable {
    case trailing, leading, bottom

    @MainActor
    public init(_ toolbar: XbinNode) {
        self = ToolbarPlace(rawValue: toolbar.props.string("place") ?? "") ?? .trailing
    }
}

/// `sheet` props with the reference renderer's defaults: open unless
/// `open` is false; detents from a string or a list, `large` by default;
/// from the bottom edge unless `edge` is `leading` (a drawer: no detents).
/// Rev 2: the `full` detent covers the screen (a full-screen cover, no
/// swipe to dismiss); a `sheet` among its children presents over it.
@MainActor
public struct SheetProps {
    public enum Detent: String, Sendable, CaseIterable { case medium, large, full }
    public enum Edge: String, Sendable, CaseIterable { case bottom, leading }

    public let isOpen: Bool
    /// Where it comes from; an unknown value reads as `bottom`.
    public let edge: Edge
    public let title: String?
    public let detents: [Detent]
    /// The body is one `screen` or `nav`: it brings its own bar.
    public let isWhole: Bool
    public let toolbar: XbinNode?
    public let body: [XbinNode]
    /// Sheets presented over this one (rev 2).
    public let nested: [XbinNode]

    public init(_ sheet: XbinNode) {
        isOpen = sheet.value("open")?.boolValue ?? true
        edge = Edge(rawValue: sheet.props.string("edge") ?? "") ?? .bottom
        title = sheet.props.nonEmpty("title")
        var ds: [Detent] = []
        switch sheet.props["detents"] {
        case .string(let s)?: ds = [Detent(rawValue: s)].compactMap { $0 }
        case .array(let a)?: ds = a.compactMap { Detent(rawValue: $0.stringValue ?? "") }
        default: break
        }
        detents = ds.isEmpty ? [.large] : ds
        let tb = sheet.children.first { $0.type == "toolbar" }
        toolbar = tb
        body = sheet.children.filter { $0 !== tb && $0.type != "sheet" }
        nested = sheet.children.filter { $0.type == "sheet" }
        isWhole = body.count == 1 && (body[0].type == "screen" || body[0].type == "nav")
    }

    /// A full-screen cover (`full` among the detents): no grabber, no swipe
    /// to dismiss — the sheet's own buttons close it.
    public var isFull: Bool { detents.contains(.full) }

    /// A drawer: slides in from the leading edge over the view.
    public var isDrawer: Bool { edge == .leading }

    /// Whether `node` is a `sheet` with `edge="leading"`.
    public static func isDrawer(_ node: XbinNode) -> Bool {
        node.type == "sheet" && node.props.string("edge") == Edge.leading.rawValue
    }
}

/// A drawer's geometry and gestures (the reference renderer's
/// `.sheet.edge-leading`: 86 % of the width, at most 400 points).
public enum DrawerMetrics {
    /// The drawer's width in a container `width` points wide.
    public static func width(container width: Double) -> Double {
        max(0, min(width * 0.86, 400))
    }

    /// The backdrop's opacity while the drawer is dragged `offset` points
    /// towards the leading edge (0: fully open).
    public static func scrim(offset: Double, width: Double) -> Double {
        guard width > 0 else { return 0 }
        return 0.28 * max(0, min(1, 1 - offset / width))
    }

    /// How far a drag has moved the drawer towards the leading edge, from
    /// the drag's horizontal translation in the view's own (layout
    /// direction–relative) coordinates: negative is towards the leading
    /// edge. Never past fully open.
    public static func offset(translation: Double) -> Double {
        max(0, -translation)
    }

    /// Whether letting go closes the drawer: dragged past a third of its
    /// width, or flung towards the leading edge (the predicted end is past
    /// half of it).
    public static func dismisses(offset: Double, predicted: Double, width: Double) -> Bool {
        offset > width / 3 || predicted > width / 2
    }
}

/// How a `split` lays out its two children (plans/native.md §15).
public enum SplitLayout: Sendable, Equatable {
    /// Side by side: `NavigationSplitView` (or `ArrangementView` on an
    /// iPhone Duo SDK, iOS 27.1).
    case columns
    /// One above the other (compact width, `prefer="single"`, or not two
    /// children) — a rev-1 split, which has no `detail` state.
    case stacked
    /// Rev 2, a split with `detail` (even false) where it doesn't show two
    /// columns: a navigation stack of the list, with the detail pushed over
    /// it while `detail` is true (Back reports `close`).
    case collapsed

    public init(children: Int, regularWidth: Bool, prefer: String?, collapses: Bool = false) {
        if children >= 2 && regularWidth && prefer != "single" { self = .columns }
        else if children >= 2 && collapses { self = .collapsed }
        else { self = .stacked }
    }

    /// A split node's layout at a width.
    @MainActor
    public init(_ split: XbinNode, regularWidth: Bool) {
        self.init(children: split.children.count, regularWidth: regularWidth, prefer: split.props.string("prefer"),
                  collapses: split.binds("detail"))
    }
}

/// A rev-2 `split`'s controlled state: whether the detail is open (compact)
/// and which columns show (regular).
@MainActor
public struct SplitState {
    public enum Columns: String, Sendable, CaseIterable { case auto, all, detail }

    public let detail: Bool
    public let columns: Columns

    public init(_ split: XbinNode) {
        detail = split.value("detail")?.boolValue ?? false
        columns = Columns(rawValue: Props.text(split.value("columns")) ?? "") ?? .auto
    }
}

/// A screen's `toolbar` items for the navigation bar: what it shows (a
/// picker's current choice, badges) grouped apart from what it does
/// (buttons, menus), so the bar reads as two small groups instead of one
/// dense one.
@MainActor
public struct ToolbarGroups {
    /// Pickers and badges, in order.
    public let status: [XbinNode]
    /// Buttons and menus, in order.
    public let actions: [XbinNode]

    public init(_ toolbar: XbinNode?) {
        let items = toolbar?.children ?? []
        status = items.filter { $0.type == "picker" || $0.type == "badge" }
        actions = items.filter { $0.type != "picker" && $0.type != "badge" }
    }
}

/// `tabs` state: the tabs in order and the selected key (the shown
/// `selected`, else the first tab's key).
@MainActor
public struct TabsState {
    public let tabs: [XbinNode]
    public let selected: String

    public init(_ tabs: XbinNode) {
        let list = tabs.children.filter { $0.type == "tab" }
        self.tabs = list
        let first = list.first?.props.string("key") ?? ""
        let sel = Props.text(tabs.value("selected")) ?? first
        selected = list.contains { $0.props.string("key") == sel } ? sel : first
    }

    public var current: XbinNode? { tabs.first { $0.props.string("key") == selected } }

    public static func key(_ tab: XbinNode) -> String { tab.props.string("key") ?? "" }

    public static func title(_ tab: XbinNode) -> String {
        tab.props.nonEmpty("title") ?? tab.props.string("key") ?? ""
    }
}
