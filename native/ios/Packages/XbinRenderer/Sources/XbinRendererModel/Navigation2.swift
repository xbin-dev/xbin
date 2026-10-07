import Foundation
import XbinCore

// Vocabulary rev 2 (D189) — the parts of the navigation revision that
// don't draw: a row's swipe actions per edge, scroll targets and edges,
// search scopes and suggestions, and when a pull to refresh ends.

/// A row's `actions` children by edge (rev 2: `edge`, `full`): the buttons
/// swiped in from the trailing edge (the default) and from the leading one,
/// and whether a full swipe runs the first of each. The ⋯ menu and the
/// context menu list the leading edge's first, then the trailing's.
@MainActor
public struct RowActions {
    public let trailing: [XbinNode]
    public let leading: [XbinNode]
    public let trailingFull: Bool
    public let leadingFull: Bool

    public init(_ row: XbinNode) {
        let groups = row.children.filter { $0.type == "actions" }
        let lead = groups.filter { $0.props.string("edge") == "leading" }
        let trail = groups.filter { $0.props.string("edge") != "leading" }
        let buttons = { (gs: [XbinNode]) in gs.flatMap { $0.children.filter { $0.type == "button" } } }
        trailing = buttons(trail)
        leading = buttons(lead)
        trailingFull = trail.contains { $0.props.bool("full") }
        leadingFull = lead.contains { $0.props.bool("full") }
    }

    /// Every button, the leading edge's first.
    public var all: [XbinNode] { leading + trailing }

    /// The trailing swipe actions in SwiftUI's order (the first is
    /// outermost): reversed, so a destructive last button sits outermost as
    /// in rev 1 — but with `full`, the first button outermost, the one a
    /// full swipe runs.
    public var trailingSwipeOrder: [XbinNode] {
        guard trailingFull, let first = trailing.first else { return trailing.reversed() }
        return [first] + trailing.dropFirst().reversed()
    }
    public var isEmpty: Bool { trailing.isEmpty && leading.isEmpty }
}

/// A `menu`'s items: buttons, dividers and (rev 2) submenus, in order.
@MainActor
public enum MenuItems {
    public enum Item {
        case button(XbinNode)
        case divider
        case submenu(XbinNode)
    }

    public static func items(_ menu: XbinNode) -> [Item] {
        menu.children.compactMap { c in
            switch c.type {
            case "button": return .button(c)
            case "divider": return .divider
            case "menu": return .submenu(c)
            default: return nil
            }
        }
    }
}

/// A `scrollTo` value (rev 2): a child's key, or `start` / `end`; text
/// after a `#` only makes a new value (`end#3` jumps again).
public enum ScrollTarget: Sendable, Equatable {
    case start
    case end
    case key(String)

    public init?(_ raw: String?) {
        guard let raw else { return nil }
        let key = String(raw.split(separator: "#", maxSplits: 1, omittingEmptySubsequences: false).first ?? "")
        switch key {
        case "": return nil
        case "start": self = .start
        case "end": self = .end
        default: self = .key(key)
        }
    }
}

/// Finding the child an `anchor` or `scrollTo` names: by the tile's key=
/// (the wire key ends in `:<key>`) or by its wire key — among a list's or
/// transcript's children and the rows of its sections.
@MainActor
public enum ScrollKeys {
    public static func matches(_ node: XbinNode, _ key: String) -> Bool {
        node.key == key || node.key.hasSuffix(":" + key)
    }

    public static func child(of parent: XbinNode, key: String) -> XbinNode? {
        for c in parent.children {
            if matches(c, key) { return c }
            if c.type == "section", let r = c.children.first(where: { matches($0, key) }) { return r }
        }
        return nil
    }
}

/// The `edge` events a scroll view owes the tile as it moves: an edge it
/// reached or left since the last report (the first report of each edge
/// is a change too).
public struct ScrollEdges: Sendable, Equatable {
    public var atStart: Bool?
    public var atEnd: Bool?

    public init() {}

    /// Within `slack` points of the start and the end.
    public static func at(offset: Double, viewport: Double, content: Double, slack: Double = 32) -> (start: Bool, end: Bool) {
        (offset <= slack, offset + viewport >= content - slack)
    }

    /// The changes to report, `(edge, at)`, and the new state.
    public mutating func update(start: Bool, end: Bool) -> [(edge: String, at: Bool)] {
        var out: [(edge: String, at: Bool)] = []
        if atStart != start { out.append(("start", start)); atStart = start }
        if atEnd != end { out.append(("end", end)); atEnd = end }
        return out
    }
}

/// A `screen`'s search (rev 2): its scopes, the selected one, and the
/// suggestions shown while the field has focus.
@MainActor
public struct SearchProps {
    public struct Option: Sendable, Equatable, Identifiable {
        public let value: String
        public let label: String
        public let icon: String?
        public var id: String { value }
    }

    public let scopes: [Option]
    /// The selected scope's value (the first scope's when none is).
    public let scope: String?
    public let suggestions: [Option]

    public init(_ screen: XbinNode) {
        let opts = { (name: String) -> [Option] in
            screen.props.objects(name).compactMap { o in
                guard let v = Props.text(o["value"]) else { return nil }
                return Option(value: v, label: Props.text(o["label"]).flatMap { $0.isEmpty ? nil : $0 } ?? v, icon: Props.text(o["icon"]))
            }
        }
        scopes = opts("scopes")
        suggestions = opts("suggestions")
        let s = Props.text(screen.value("scope"))
        scope = scopes.contains { $0.value == s } ? s : scopes.first?.value
    }
}

/// When a pull to refresh ends (rev 2 `refreshing`). A screen that binds
/// `refreshing` keeps the spinner up until the tile sets it false: the app
/// waits up to ``start`` for the tile to set it true (a reload that
/// finished at once never does), then until it is false again, at most
/// ``limit`` in all. A screen without it (rev 1) ends after ``legacy``.
public enum RefreshCompletion {
    public static let legacy: Duration = .milliseconds(600)
    public static let start: Duration = .seconds(1)
    public static let limit: Duration = .seconds(60)
    public static let poll: Duration = .milliseconds(100)

    public enum Phase: Sendable, Equatable { case waitingForStart, running, done }

    /// The next phase after a poll at `elapsed` that saw `refreshing`.
    public static func next(_ phase: Phase, refreshing: Bool, elapsed: Duration) -> Phase {
        if elapsed >= limit { return .done }
        switch phase {
        case .waitingForStart:
            if refreshing { return .running }
            return elapsed >= start ? .done : .waitingForStart
        case .running:
            return refreshing ? .running : .done
        case .done:
            return .done
        }
    }
}
