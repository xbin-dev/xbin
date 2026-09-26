import Foundation
import XbinCore

/// How a `screen` splits its children (the reference renderer's rules,
/// web/xb/render-structure.js): its `toolbar` goes to the navigation bar; a
/// `composer` and a bar-style `tabs` dock at the bottom; `sheet`s present
/// over it; the rest is the body.
@MainActor
public struct ScreenLayout {
    public enum Style: String, Sendable { case list, form, scroll }

    public let style: Style
    public let toolbar: XbinNode?
    public let docked: [XbinNode]
    public let sheets: [XbinNode]
    public let body: [XbinNode]

    public init(_ screen: XbinNode) {
        style = Style(rawValue: screen.props.string("style") ?? "") ?? .scroll
        let all = screen.children
        let tb = all.first { $0.type == "toolbar" }
        toolbar = tb
        let dock = all.filter { $0.type == "composer" || ($0.type == "tabs" && $0.props.string("style") == "bar") }
        docked = dock
        sheets = all.filter { $0.type == "sheet" }
        body = all.filter { n in n !== tb && n.type != "sheet" && !dock.contains { $0 === n } }
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

/// A `fragment`'s (or the root's) children: sheets present over the rest.
@MainActor
public struct FragmentLayout {
    public let content: [XbinNode]
    public let sheets: [XbinNode]

    public init(_ children: [XbinNode]) {
        content = children.filter { $0.type != "sheet" }
        sheets = children.filter { $0.type == "sheet" }
    }
}

/// `sheet` props with the reference renderer's defaults: open unless
/// `open` is false; detents from a string or a list, `large` by default.
@MainActor
public struct SheetProps {
    public enum Detent: String, Sendable, CaseIterable { case medium, large }

    public let isOpen: Bool
    public let title: String?
    public let detents: [Detent]
    /// The body is one `screen` or `nav`: it brings its own bar.
    public let isWhole: Bool
    public let toolbar: XbinNode?
    public let body: [XbinNode]

    public init(_ sheet: XbinNode) {
        isOpen = sheet.value("open")?.boolValue ?? true
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
        body = sheet.children.filter { $0 !== tb }
        isWhole = body.count == 1 && (body[0].type == "screen" || body[0].type == "nav")
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
