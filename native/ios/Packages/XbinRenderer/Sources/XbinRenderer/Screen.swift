#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// `screen`: a `List` (style list), a `Form` (form) or a `ScrollView`
/// (scroll, the default) with a navigation title; its `toolbar`s go to the
/// bar (trailing, leading) or a bottom toolbar by `place`, a composer (or
/// bar tabs) docks at the bottom, sheets present over it. Outside a
/// navigation container it brings its own `NavigationStack`.
struct ScreenView: View {
    let node: XbinNode
    @Environment(\.xbinNav) private var nav

    var body: some View {
        if nav.inNavigation {
            ScreenContent(node: node)
        } else {
            // Its drawers go over the whole stack, bar included.
            NavigationStack {
                ScreenContent(node: node)
            }
            .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: false, inSheet: nav.inSheet, drawersHosted: true))
            .modifier(DrawersModifier(drawers: ScreenLayout(node).drawers))
        }
    }
}

private struct ScreenContent: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinNav) private var nav

    var body: some View {
        let layout = ScreenLayout(node)
        let p = node.props
        let large = ScreenLayout.largeTitle(node, pushed: nav.pushed, inSheet: nav.inSheet)
        ScreenBody(node: node, layout: layout)
            .navigationTitle(p.string("title") ?? "")
            .navigationSubtitle(p.string("subtitle") ?? "")
            .navigationBarTitleDisplayMode(large ? .large : .inline)
            .toolbar {
                ScreenToolbar(toolbar: layout.toolbar, leading: layout.leadingToolbar,
                              bottom: nav.covered ? nil : layout.bottomToolbar, search: node.value("search") != nil)
            }
            .modifier(RefreshModifier(node: node))
            .modifier(SearchModifier(node: node))
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if !layout.docked.isEmpty {
                    VStack(spacing: 0) { ForEach(layout.docked) { NodeView(node: $0) } }
                        .environment(\.xbinPlacement, .dock)
                }
            }
            // Drawers the container doesn't draw (a split's column) go over
            // the content.
            .modifier(SheetsModifier(sheets: layout.sheets, drawers: nav.drawersHosted ? [] : layout.drawers))
            .onAppear { if node.listens(to: "appear") { cx?.emit(node, "appear") } }
    }
}

/// The screen's body by style. In a bar tab its scrolling content keeps
/// clear of the floating tab bar: a little more room at its end, so the
/// last row scrolls fully out from under the bar's glass.
private struct ScreenBody: View {
    let node: XbinNode
    let layout: ScreenLayout
    @Environment(\.xbinInTabBar) private var inTabBar

    var body: some View {
        styled.safeAreaPadding(.bottom, inTabBar ? 16 : 0)
    }

    @ViewBuilder
    private var styled: some View {
        switch layout.style {
        case .list:
            List { ForEach(layout.body) { NodeView(node: $0) } }
                .listStyle(.insetGrouped)
                .concreteBackground()
                .modifier(ListScrollModifier(list: layout.body.first { $0.type == "list" }))
                .environment(\.xbinPlacement, .list)
        case .form:
            Form { ForEach(layout.body) { NodeView(node: $0) } }
                .concreteBackground()
                .modifier(ListScrollModifier(list: layout.body.first { $0.type == "list" }))
                .environment(\.xbinPlacement, .list)
        case .scroll:
            if let list = layout.soleList {
                ListNodeView(node: list, asScreenBody: true)
            } else if layout.isChat {
                // A transcript scrolls itself; the composer docks below it.
                VStack(spacing: 0) { ForEach(layout.body) { NodeView(node: $0) } }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(XbinColor.background)
                    .environment(\.xbinPlacement, .free)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) { ForEach(layout.body) { NodeView(node: $0) } }
                        .padding(16)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .background(XbinColor.background)
                .environment(\.xbinPlacement, .free)
            }
        }
    }
}

/// A screen's `toolbar` items at the trailing end of the bar, in two
/// groups (``ToolbarGroups``): what it shows — a picker's current choice,
/// badges, compact — then, apart, what it does (buttons, menus). One long
/// group of all of them crowded the title out (a chat's model picker,
/// tool count and new-chat button).
struct ScreenToolbar: ToolbarContent {
    let toolbar: XbinNode?
    /// `place="leading"` (rev 2): at the bar's leading end, after Back.
    var leading: XbinNode?
    /// `place="bottom"` (rev 2): a bottom toolbar, its items spread out.
    var bottom: XbinNode?
    /// The screen searches: on iOS 26+ the search field lives in the bottom
    /// bar, so a bottom toolbar keeps a place for it (else it is pushed out).
    var search = false

    var body: some ToolbarContent {
        if let leading, !leading.children.isEmpty {
            ToolbarItemGroup(placement: .topBarLeading) {
                ForEach(leading.children) { NodeView(node: $0).fixedSize() }
                    .environment(\.xbinPlacement, .toolbar)
            }
        }
        if let bottom, !bottom.children.isEmpty {
            ToolbarItemGroup(placement: .bottomBar) {
                ForEach(Array(bottom.children.enumerated()), id: \.element.id) { i, item in
                    if i > 0 { Spacer() }
                    NodeView(node: item).fixedSize()
                }
                .environment(\.xbinPlacement, .toolbar)
            }
            if search {
                ToolbarSpacer(.fixed, placement: .bottomBar)
                DefaultToolbarItem(kind: .search, placement: .bottomBar)
            }
        }
        let groups = ToolbarGroups(toolbar)
        if !groups.status.isEmpty {
            ToolbarItemGroup(placement: .topBarTrailing) {
                ForEach(groups.status) { NodeView(node: $0).fixedSize() }
                    .environment(\.xbinPlacement, .toolbar)
            }
            if !groups.actions.isEmpty {
                ToolbarSpacer(.fixed, placement: .topBarTrailing)
            }
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            // At their ideal size: a bar item is otherwise measured short
            // and its label truncated ("d…" for a `draft` badge).
            ForEach(groups.actions) { NodeView(node: $0).fixedSize() }
                .environment(\.xbinPlacement, .toolbar)
        }
    }
}

/// `refreshable` + a `refresh` listener → pull to refresh. Rev 2: a screen
/// that binds `refreshing` keeps the spinner until the tile sets it false
/// (``RefreshCompletion``); without it the spinner ends after a moment.
private struct RefreshModifier: ViewModifier {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    func body(content: Content) -> some View {
        if node.props.bool("refreshable") && node.listens(to: "refresh") {
            let context = cx
            let n = node
            content.refreshable {
                await MainActor.run { context?.emit(n, "refresh") }
                guard await MainActor.run(body: { n.binds("refreshing") }) else {
                    // The tile answers with a patch whenever it likes; keep
                    // the spinner up briefly so the gesture reads as done.
                    try? await Task.sleep(for: RefreshCompletion.legacy)
                    return
                }
                let clock = ContinuousClock()
                let started = clock.now
                var phase = RefreshCompletion.Phase.waitingForStart
                while phase != .done, !Task.isCancelled {
                    try? await Task.sleep(for: RefreshCompletion.poll)
                    let on = await MainActor.run { n.value("refreshing")?.boolValue ?? false }
                    phase = RefreshCompletion.next(phase, refreshing: on, elapsed: clock.now - started)
                }
            }
        } else {
            content
        }
    }
}

/// `search` present (even "") → a search field; `search {value}` reports it.
/// Rev 2: the return key (or a suggestion) reports `submit {value}`; `scopes`
/// show under the field while searching (`scope {value}`); `suggestions`
/// list under it while it has focus.
private struct SearchModifier: ViewModifier {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    func body(content: Content) -> some View {
        if node.value("search") != nil {
            let text = mainBinding(
                get: { Props.text(node.value("search")) ?? "" },
                set: { cx?.emit(node, "search", ["value": .string($0)]) }
            )
            let search = SearchProps(node)
            let context = cx
            let n = node
            let submit = { (value: String) in
                if Props.text(n.value("search")) != value { context?.emit(n, "search", ["value": .string(value)]) }
                if n.listens(to: "submit") { context?.emit(n, "submit", ["value": .string(value)]) }
            }
            let scope = mainBinding(
                get: { SearchProps(n).scope ?? "" },
                set: { v in if v != SearchProps(n).scope { context?.emit(n, "scope", ["value": .string(v)]) } }
            )
            content
                .searchable(text: text)
                .onSubmit(of: .search) { submit(Props.text(n.value("search")) ?? "") }
                .modifier(SearchScopesModifier(scopes: search.scopes, selection: scope))
                .searchSuggestions {
                    ForEach(search.suggestions) { s in
                        Button { submit(s.value) } label: {
                            Label(s.label, systemImage: XbinIcons.symbol(s.icon) ?? XbinIcons.UI.search)
                        }
                        .foregroundStyle(XbinColor.text)
                    }
                }
        } else {
            content
        }
    }
}

/// The search field's scopes, when there are any.
private struct SearchScopesModifier: ViewModifier {
    let scopes: [SearchProps.Option]
    let selection: Binding<String>

    func body(content: Content) -> some View {
        if scopes.isEmpty {
            content
        } else {
            content.searchScopes(selection) {
                ForEach(scopes) { Text(verbatim: $0.label).tag($0.value) }
            }
        }
    }
}

/// A `list`'s rev-2 scrolling when its rows are the rows of an enclosing
/// `List` (a list or form screen, or a scroll screen whose body is the one
/// list): it opens at `anchor`, jumps to `scrollTo` when that changes, and
/// reports `edge` as the start or the end comes into or out of view.
struct ListScrollModifier: ViewModifier {
    let list: XbinNode?
    @Environment(\.xbin) private var cx
    @State private var edges = ScrollEdges()

    func body(content: Content) -> some View {
        if let list, list.binds("anchor") || list.binds("scrollTo") || list.listens(to: "edge") {
            let context = cx
            ScrollViewReader { proxy in
                content
                    .onAppear {
                        if let a = list.props.nonEmpty("anchor"), let row = ScrollKeys.child(of: list, key: a) {
                            proxy.scrollTo(row.id, anchor: .top)
                        }
                    }
                    .onChange(of: list.props.string("scrollTo") ?? "") { _, to in
                        switch ScrollTarget(to) {
                        case .start?: if let first = list.children.first { withAnimation { proxy.scrollTo(first.id, anchor: .top) } }
                        case .end?: if let last = list.children.last { withAnimation { proxy.scrollTo(last.id, anchor: .bottom) } }
                        case .key(let k)?: if let row = ScrollKeys.child(of: list, key: k) { withAnimation { proxy.scrollTo(row.id) } }
                        case nil: break
                        }
                    }
                    .onScrollGeometryChange(for: [Bool].self) { geo in
                        let at = ScrollEdges.at(offset: Double(geo.contentOffset.y), viewport: Double(geo.containerSize.height),
                                                content: Double(geo.contentSize.height))
                        return [at.start, at.end]
                    } action: { _, at in
                        guard list.listens(to: "edge"), at.count == 2 else { return }
                        for e in edges.update(start: at[0], end: at[1]) {
                            context?.emit(list, "edge", ["edge": .string(e.edge), "at": .bool(e.at)])
                        }
                    }
            }
        } else {
            content
        }
    }
}
#endif
