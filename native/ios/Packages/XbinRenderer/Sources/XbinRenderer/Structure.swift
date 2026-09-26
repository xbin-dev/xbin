#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

// Structure primitives (plans/native.md §8.1) that aren't navigation:
// fragment, section, stack, list, disclosure, and a toolbar outside a screen.

/// `fragment`: no visual of its own — its children in place, sheets
/// presented over them and drawers laid over them.
struct FragmentView: View {
    let node: XbinNode
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let layout = FragmentLayout(node.children)
        let sheets = SheetsModifier(sheets: layout.sheets, drawers: layout.drawers)
        if placement == .list || placement == .chat || placement == .toolcard {
            // Inside a list or transcript every child is its own row.
            ForEach(node.children) { NodeView(node: $0) }
        } else if layout.content.count == 1 {
            NodeView(node: layout.content[0]).modifier(sheets)
        } else {
            VStack(spacing: 0) { ForEach(layout.content) { NodeView(node: $0) } }
                .modifier(sheets)
        }
    }
}

/// Presents bottom `sheets` over the content (or draws them inline for
/// snapshots, ``XbinRenderOptions/inlineSheets``) and lays `drawers` over
/// it (``DrawerLayer``, the same in both).
struct SheetsModifier: ViewModifier {
    let sheets: [XbinNode]
    var drawers: [XbinNode] = []
    @Environment(\.xbin) private var cx

    func body(content: Content) -> some View {
        if sheets.isEmpty {
            content.modifier(DrawersModifier(drawers: drawers))
        } else if cx?.options.inlineSheets == true {
            content
                .overlay(alignment: .bottom) {
                    ForEach(sheets) { SheetView(node: $0) }
                }
                .modifier(DrawersModifier(drawers: drawers))
        } else {
            content
                .background {
                    ForEach(sheets) { SheetView(node: $0) }
                }
                .modifier(DrawersModifier(drawers: drawers))
        }
    }
}

/// Lays `drawers` over the content (each covers it while open).
struct DrawersModifier: ViewModifier {
    let drawers: [XbinNode]

    func body(content: Content) -> some View {
        if drawers.isEmpty {
            content
        } else {
            content.overlay {
                ForEach(drawers) { DrawerLayer(node: $0) }
            }
        }
    }
}

/// `section`: a `Section` of a list/form screen, or a titled card group on a
/// scroll screen. `collapsible` sections fold with a tap on the header.
struct SectionView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let p = node.props
        let collapsible = p.bool("collapsible")
        let collapsed = collapsible && (node.value("collapsed")?.boolValue ?? false)
        let footer = collapsed ? nil : p.nonEmpty("footer")
        if placement == .list {
            Section {
                if !collapsed { ForEach(node.children) { NodeView(node: $0) } }
            } header: {
                SectionHeader(node: node, collapsible: collapsible, collapsed: collapsed)
            } footer: {
                if let footer { Text(verbatim: footer) }
            }
        } else {
            VStack(alignment: .leading, spacing: 6) {
                SectionHeader(node: node, collapsible: collapsible, collapsed: collapsed)
                    .font(.footnote)
                    .foregroundStyle(XbinColor.muted)
                    .padding(.horizontal, 16)
                if !collapsed && !node.children.isEmpty { CardGroup(nodes: node.children) }
                if let footer {
                    Text(verbatim: footer)
                        .font(.footnote)
                        .foregroundStyle(XbinColor.muted)
                        .padding(.horizontal, 16)
                }
            }
        }
    }
}

/// A section's title, badge and fold chevron.
private struct SectionHeader: View {
    let node: XbinNode
    let collapsible: Bool
    let collapsed: Bool
    @Environment(\.xbin) private var cx

    var body: some View {
        let p = node.props
        let row = HStack(spacing: 6) {
            if let t = p.nonEmpty("title") { Text(verbatim: t) }
            if let b = p.nonEmpty("badge") { Pill(text: b, tone: nil, small: true) }
            Spacer(minLength: 0)
            if collapsible {
                Image(systemName: collapsed ? XbinIcons.UI.chevronForward : XbinIcons.UI.chevronDown)
                    .font(.caption.weight(.semibold))
            }
        }
        if collapsible {
            Button {
                cx?.emit(node, "toggle", ["collapsed": .bool(!collapsed)])
            } label: {
                row.contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(.isHeader)
        } else {
            row.accessibilityAddTraits(.isHeader)
        }
    }
}

/// Cells in one rounded card with inset separators — a section or a run of
/// rows outside a `List`.
struct CardGroup: View {
    let nodes: [XbinNode]

    var body: some View {
        VStack(spacing: 0) {
            ForEach(Array(nodes.enumerated()), id: \.element.id) { i, n in
                if i > 0 { Divider().padding(.leading, 16) }
                NodeView(node: n)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 11)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(XbinColor.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .environment(\.xbinPlacement, .card)
    }
}

/// `stack`: `VStack`/`HStack` with a gap token, or a wrapping flow.
struct StackView: View {
    let node: XbinNode

    var body: some View {
        let p = node.props
        let horizontal = p.string("axis") == "h"
        let gap = CGFloat(XbinGap(p.string("gap"))?.points ?? XbinGap.standard(horizontal: horizontal))
        let align = p.string("align")
        Group {
            if horizontal && p.bool("wrap") {
                FlowLayout(spacing: gap) { ForEach(node.children) { NodeView(node: $0) } }
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else if horizontal {
                HStack(alignment: Self.vertical(align), spacing: gap) { ForEach(node.children) { NodeView(node: $0) } }
            } else {
                VStack(alignment: Self.horizontal(align), spacing: gap) { ForEach(node.children) { NodeView(node: $0) } }
                    .frame(maxWidth: .infinity, alignment: Self.frame(align))
            }
        }
        .environment(\.xbinPlacement, .free)
    }

    static func vertical(_ a: String?) -> VerticalAlignment {
        switch a ?? "" {
        case "start": return .top
        case "end": return .bottom
        default: return .center
        }
    }

    static func horizontal(_ a: String?) -> HorizontalAlignment {
        switch a ?? "" {
        case "center": return .center
        case "end": return .trailing
        default: return .leading
        }
    }

    static func frame(_ a: String?) -> Alignment {
        switch a ?? "" {
        case "center": return .center
        case "end": return .trailing
        default: return .leading
        }
    }
}

/// A wrapping horizontal layout (`stack wrap`); the line breaking is
/// ``FlowLayoutMath`` (tested on Linux).
struct FlowLayout: Layout {
    var spacing: CGFloat

    private func place(_ proposalWidth: CGFloat?, _ subviews: Subviews) -> ([CGSize], FlowLayoutMath.Placement) {
        let sizes = subviews.map { $0.sizeThatFits(.unspecified) }
        let width = proposalWidth.flatMap { $0.isFinite ? Double($0) : nil }
        let p = FlowLayoutMath.place(sizes.map { (Double($0.width), Double($0.height)) }, maxWidth: width,
                                     spacing: Double(spacing), lineSpacing: Double(spacing))
        return (sizes, p)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let (_, p) = place(proposal.width, subviews)
        let w = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? CGFloat(p.width)
        return CGSize(width: w, height: CGFloat(p.height))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let (sizes, p) = place(bounds.width, subviews)
        for (i, s) in subviews.enumerated() {
            let o = p.origins[i]
            s.place(at: CGPoint(x: bounds.minX + CGFloat(o.x), y: bounds.minY + CGFloat(o.y)),
                    anchor: .topLeading, proposal: ProposedViewSize(sizes[i]))
        }
    }
}

/// `list`: inside a list screen its rows join the screen's `List`; as the
/// whole body of a scroll screen it is the screen's lazy `List`; elsewhere
/// (a stack on a scroll screen) its rows are card groups.
struct ListNodeView: View {
    let node: XbinNode
    var asScreenBody = false
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let style = node.props.string("style") ?? "inset"
        if placement == .list {
            ListRows(node: node)
        } else if asScreenBody {
            Group {
                switch style {
                case "plain": List { ListRows(node: node) }.listStyle(.plain)
                case "grouped": List { ListRows(node: node) }.listStyle(.grouped)
                default: List { ListRows(node: node) }.listStyle(.insetGrouped)
                }
            }
            .environment(\.xbinPlacement, .list)
        } else {
            VStack(alignment: .leading, spacing: 16) {
                ForEach(Array(ListRuns.runs(node.children).enumerated()), id: \.offset) { _, run in
                    switch run {
                    case .rows(let rows): CardGroup(nodes: rows)
                    case .section(let s): SectionView(node: s)
                    }
                }
                if node.listens(to: "more") {
                    Color.clear.frame(height: 1).onAppear { cx?.emit(node, "more") }
                }
            }
        }
    }
}

/// A list's children as rows of the enclosing `List`, and the `more`
/// trigger at the end.
private struct ListRows: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        ForEach(node.children) { NodeView(node: $0) }
        if node.listens(to: "more") {
            Color.clear
                .frame(height: 1)
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
                .onAppear { cx?.emit(node, "more") }
        }
    }
}

/// `disclosure`: a `DisclosureGroup`; `open` is controlled.
struct DisclosureNodeView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let open = mainBinding(
            get: { node.value("open")?.boolValue ?? false },
            set: { cx?.emit(node, "toggle", ["open": .bool($0)]) }
        )
        DisclosureGroup(isExpanded: open) {
            ForEach(node.children) { NodeView(node: $0) }
        } label: {
            WrappingText(text: node.props.string("title") ?? "")
        }
    }
}

/// A `toolbar` outside a screen or sheet (a child-rule violation the
/// runtime reported): its items in a row.
struct ToolbarInline: View {
    let node: XbinNode

    var body: some View {
        HStack(spacing: 12) { ForEach(node.children) { NodeView(node: $0) } }
            .environment(\.xbinPlacement, .toolbar)
    }
}
#endif
