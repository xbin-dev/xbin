#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// Navigation diagnostics in Debug builds (the UI tests read them from the
/// simulator's log: `log show --predicate 'eventMessage CONTAINS "xbin-nav"'`).
@inline(__always) func navTrace(_ s: @autoclosure () -> String) {
    #if DEBUG
    NSLog("xbin-nav %@", s())
    #endif
}

/// `nav`: a `NavigationStack(path:)` over its screens (the first is the
/// root). Going back hides the top screen at once and reports
/// `pop {depth}` — the screens that remain — and the tile then drops it.
struct NavView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinNav) private var nav
    @State private var popped = 0

    var body: some View {
        let screens = node.children
        let keys = screens.map(\.key)
        let path = mainBinding(
            get: { NavStack.path(keys, popped: popped) },
            set: { new in
                navTrace("nav \(node.key) path set \(new.count) of \(keys.count) popped \(popped)")
                guard let r = NavStack.pop(screens: keys.count, popped: popped, newPathCount: new.count) else { return }
                popped = r.popped
                cx?.emit(node, "pop", ["depth": .int(Int64(r.depth))])
            }
        )
        let inSheet = nav.inSheet
        let shown = NavStack.path(keys, popped: popped)
        NavigationStack(path: path) {
            Group {
                if let first = screens.first { NodeView(node: first) }
            }
            .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: false, inSheet: inSheet, drawersHosted: true,
                                                 covered: !shown.isEmpty))
            .navigationDestination(for: String.self) { key in
                if let screen = cx?.model.node(key) {
                    NodeView(node: screen)
                        .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: true, inSheet: inSheet, drawersHosted: true,
                                                             covered: shown.last != key))
                }
            }
        }
        // The screens' drawers cover the whole stack, bar included.
        .modifier(DrawersModifier(drawers: FragmentLayout.drawers(ofScreens: screens)))
        .onChange(of: keys) { popped = 0 }
    }
}

/// `split`: list/detail. Two columns when the width is regular and
/// `prefer` isn't `single` (``SplitLayout``): `NavigationSplitView` (its
/// column visibility the rev-2 `columns`), or the iPhone Duo's
/// `ArrangementView` when built with the iOS 27.1 SDK (`XBIN_SDK_27_1`, off
/// by default: the hosted CI has only 27.0). Otherwise a rev-2 split (one
/// with `detail`) collapses to a `NavigationStack` — the list, with the
/// detail pushed over it while `detail` is true, Back reporting `close`;
/// a rev-1 split is stacked, like the reference renderer (§15).
struct SplitView: View {
    let node: XbinNode
    @Environment(\.horizontalSizeClass) private var width
    @Environment(\.xbinNav) private var nav
    @Environment(\.xbin) private var cx
    /// This split presents the tree's top-level sheets (``SplitSheets``).
    @Environment(\.xbinSplitSheets) private var topSheets

    var body: some View {
        let layout = SplitLayout(node, regularWidth: width == .regular)
        let onAppStack = layout == .collapsed && CollapsedSplit.onAppStack(cx, inSheet: nav.inSheet)
        // A split collapsed onto the app's stack presents them from the
        // screen in front; any other, over itself.
        layouts(layout)
            .environment(\.xbinSplitSheets, false)
            .modifier(SplitSheets(owner: topSheets, active: !onAppStack))
    }

    @ViewBuilder private func layouts(_ layout: SplitLayout) -> some View {
        let kids = node.children
        let flags = XbinNavFlags(inNavigation: true, pushed: false, inSheet: nav.inSheet)
        let _ = navTrace("split \(node.key) layout \(layout) width \(String(describing: width)) detail \(SplitState(node).detail)")
        if layout == .columns {
            #if XBIN_SDK_27_1
            if #available(iOS 27.1, *) {
                DuoSplit(primary: kids[0], secondary: kids[1], flags: flags)
            } else {
                ColumnsSplit(node: node, primary: kids[0], secondary: kids[1], flags: flags)
            }
            #else
            ColumnsSplit(node: node, primary: kids[0], secondary: kids[1], flags: flags)
            #endif
        } else if layout == .collapsed {
            CollapsedSplit(node: node, primary: kids[0], secondary: kids[1], inSheet: nav.inSheet, topSheets: topSheets)
        } else {
            VStack(spacing: 0) {
                ForEach(Array(kids.enumerated()), id: \.element.id) { i, kid in
                    if i > 0 { Divider() }
                    NodeView(node: kid).frame(maxHeight: .infinity)
                }
            }
        }
    }
}

/// Two columns with `NavigationSplitView`. A split that binds `columns`
/// (rev 2) controls the column visibility: `detail` hides the list behind
/// the bar's sidebar button, `all` shows both; the user's toggle reports
/// `columns {value}`.
private struct ColumnsSplit: View {
    let node: XbinNode
    let primary: XbinNode
    let secondary: XbinNode
    let flags: XbinNavFlags
    @Environment(\.xbin) private var cx
    @Environment(\.scenePhase) private var phase
    /// Until when a change to both columns is SwiftUI's own, not the
    /// person's: as the split appears, and as the app comes back to the
    /// front, it shows the list again — a link opened from outside brought
    /// back a list the person had hidden (2026-10-07).
    @State private var settling = SettleClock()

    var body: some View {
        if node.binds("columns") {
            let n = node
            let context = cx
            let clock = settling
            let visibility = mainBinding(
                get: { () -> NavigationSplitViewVisibility in
                    switch SplitState(n).columns {
                    case .detail: return .detailOnly
                    // `auto` as an unbound split shows it: both columns
                    // (a bound .automatic hid the list on an iPad).
                    case .all, .auto: return .all
                    }
                },
                set: { v in
                    let c: SplitState.Columns = v == .detailOnly ? .detail : .all
                    if c == .all, SplitState(n).columns == .detail, clock.settling {
                        navTrace("split \(n.key) keeps the list hidden (SwiftUI showed it as it appeared)")
                        return
                    }
                    if c != SplitState(n).columns { context?.emit(n, "columns", ["value": .string(c.rawValue)]) }
                }
            )
            NavigationSplitView(columnVisibility: visibility) {
                sidebar
            } detail: {
                detail
            }
            .navigationSplitViewStyle(.balanced)
            .onAppear { settling.start() }
            .onChange(of: phase) { _, p in if p == .active { settling.start() } }
        } else {
            NavigationSplitView {
                sidebar
            } detail: {
                detail
            }
            .navigationSplitViewStyle(.balanced)
        }
    }

    /// The list, 280–400 pt wide (the reference renderer's clamp).
    private var sidebar: some View {
        NodeView(node: primary).environment(\.xbinNav, flags)
            .navigationSplitViewColumnWidth(min: 280, ideal: 360, max: 400)
    }

    /// The detail. A `nav` there, in the app's own stack
    /// (``XbinRenderOptions/hostNavigation``), shows its top screen with a
    /// Back of its own (``ColumnNav``): a NavigationStack in a split's
    /// column inside the app's NavigationStack pushed nowhere — a pushed
    /// screen's title went to the app's bar and the column kept showing the
    /// one under it, covered (an iPad, 2026-10-07).
    @ViewBuilder private var detail: some View {
        if secondary.type == "nav", cx?.options.hostNavigation == true {
            ColumnNav(node: secondary)
        } else {
            NodeView(node: secondary).environment(\.xbinNav, flags)
        }
    }
}

/// A short window after the split appears or the app comes to the front.
@MainActor
private final class SettleClock {
    private var until = Date.distantPast
    func start() { until = Date().addingTimeInterval(1.5) }
    var settling: Bool { Date() < until }
}

/// A `nav` in a split's detail column inside the app's stack: its top
/// screen, with a Back that reports `pop {depth}` (the screens that remain,
/// as NavView reports it) while there is one under it.
private struct ColumnNav: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let screens = node.children
        if let top = screens.last {
            let depth = screens.count
            let under = depth > 1 ? screens[depth - 2] : nil
            let n = node
            let context = cx
            NodeView(node: top)
                .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: depth > 1, drawersHosted: false))
                .id(top.key)
                .toolbar {
                    if let under {
                        ToolbarItem(placement: .topBarLeading) {
                            Button {
                                context?.emit(n, "pop", ["depth": .int(Int64(depth - 1))])
                            } label: {
                                Label(ColumnNav.backTitle(under), systemImage: "chevron.backward")
                                    .labelStyle(.titleAndIcon)
                            }
                            .accessibilityIdentifier("BackButton")
                        }
                    }
                }
        }
    }

    /// The screen under's title when short, else "Back" (the system's rule).
    static func backTitle(_ screen: XbinNode) -> String {
        let t = screen.props.string("title") ?? ""
        return !t.isEmpty && t.count <= 14 ? t : "Back"
    }
}

/// A rev-2 split on a compact width: a navigation stack whose root is the
/// list; while `detail` is true the detail is pushed over it. Back (the
/// button or the edge swipe) hides it at once and reports `close` (→
/// `detail: false`). In the app's own navigation stack
/// (``XbinRenderOptions/hostNavigation``) the list is that stack's page and
/// the detail is pushed onto it — one stack, one bar, the system's Back.
private struct CollapsedSplit: View {
    let node: XbinNode
    let primary: XbinNode
    let secondary: XbinNode
    let inSheet: Bool
    let topSheets: Bool
    @Environment(\.xbin) private var cx

    static func onAppStack(_ cx: XbinRenderContext?, inSheet: Bool) -> Bool {
        cx?.options.hostNavigation == true && !inSheet
    }

    var body: some View {
        if Self.onAppStack(cx, inSheet: inSheet) {
            HostedCollapsedSplit(node: node, primary: primary, secondary: secondary, topSheets: topSheets)
        } else {
            OwnStackSplit(node: node, primary: primary, secondary: secondary, inSheet: inSheet)
        }
    }
}

/// A collapsed split on the app's stack: the detail is a destination of it.
private struct HostedCollapsedSplit: View {
    let node: XbinNode
    let primary: XbinNode
    let secondary: XbinNode
    let topSheets: Bool
    @Environment(\.xbin) private var cx
    @Environment(\.xbinImages) private var images
    @Environment(\.xbinCompact) private var compact

    var body: some View {
        let n = node
        let context = cx
        let shown = mainBinding(
            get: { SplitState(n).detail },
            set: { open in
                navTrace("split \(n.key) hosted set \(open) shown \(SplitState(n).detail)")
                if !open, SplitState(n).detail { context?.emit(n, "close") }
            }
        )
        let sheets = topSheets
        NodeView(node: primary)
            .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: false, inSheet: false, drawersHosted: false,
                                                 covered: SplitState(n).detail))
            .modifier(SplitSheets(owner: sheets, active: !SplitState(n).detail))
            .navigationDestination(isPresented: shown) {
                // A destination of the app's stack takes its environment
                // from that stack, not from here: without the tree's own
                // (its context, images) the pushed detail
                // drew but sent nothing — typing into its composer went
                // nowhere, a card had no Open (2026-10-07). And it is read
                // by key when drawn (LiveNode), as a destination keeps the
                // content it was pushed with.
                let carried = CarriedXbinEnvironment(cx: cx, images: images, compact: compact)
                LiveNode(key: secondary.key) { detail in
                    if detail.type == "nav" {
                        HostedNav(node: detail, carried: carried, topSheets: sheets)
                    } else {
                        NodeView(node: detail)
                            .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: true, inSheet: false, drawersHosted: false))
                            .modifier(SplitSheets(owner: sheets, active: true))
                    }
                }
                .modifier(carried)
            }
    }
}

/// A `nav` in the detail of a split collapsed onto the app's stack (the
/// agent template's: the list beside a stack of a conversation and what is
/// pushed over it, D190): its screens are destinations of that same stack,
/// one over the other. A NavigationStack of its own there would sit inside
/// a destination of the app's — SwiftUI pops such a destination as soon as
/// it is pushed (the agent's conversation closed the moment it opened on an
/// iPhone, 2026-10-07). Back from a screen of it reports `pop {depth}` (the
/// screens that remain) as NavView does; Back from its first one is the
/// split's `close`.
private struct HostedNav: View {
    let node: XbinNode
    let carried: CarriedXbinEnvironment
    let topSheets: Bool
    @Environment(\.xbin) private var cx
    /// The screens the user left, counted against the stack they left them
    /// from: once the tile's stack is another (it dropped them, or moved
    /// on), none are hidden. An `onChange(of:)` reset missed the tile's
    /// drop now and then, and Back from an automation's run showed the
    /// Automations list under the automation it came from (2026-10-07).
    @State private var popped: (count: Int, of: [String]) = (0, [])

    var body: some View {
        let screens = node.children
        let keys = screens.map(\.key)
        let hidden = popped.of == keys ? popped.count : 0
        let shown = max(1, keys.count - hidden)
        let n = node
        let context = cx
        if !screens.isEmpty {
            HostedNavLevel(screens: Array(keys.prefix(shown)), index: 0, carried: carried, topSheets: topSheets) { depth in
                navTrace("nav \(n.key) hosted pop to \(depth) of \(keys.count)")
                guard depth < shown else { return }
                popped = (keys.count - depth, keys)
                context?.emit(n, "pop", ["depth": .int(Int64(depth))])
            }
        }
    }
}

/// One screen of a ``HostedNav``, the next one pushed over it — screens by
/// key, read from the model when drawn (``LiveNode``).
private struct HostedNavLevel: View {
    let screens: [String]
    let index: Int
    let carried: CarriedXbinEnvironment
    let topSheets: Bool
    let pop: @MainActor (Int) -> Void

    var body: some View {
        let next = index + 1
        let count = screens.count
        let leave = pop
        let over = mainBinding(get: { next < count }, set: { (open: Bool) in if !open, next < count { leave(next) } })
        LiveNode(key: screens[index]) { screen in
            NodeView(node: screen)
                .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: true, inSheet: false, drawersHosted: false,
                                                     covered: next < count))
        }
        .modifier(SplitSheets(owner: topSheets, active: next >= count))
        .navigationDestination(isPresented: over) {
            if next < count {
                HostedNavLevel(screens: screens, index: next, carried: carried, topSheets: topSheets, pop: pop).modifier(carried)
            }
        }
    }
}

/// The tree's environment, carried into a destination of the app's stack
/// (which takes the stack's environment, not the tree's) — with a
/// confirmation host of its own: the tree's is presented from the tree's
/// root, which a destination covers, so a button's `confirm` on a pushed
/// screen showed nothing — Close tab, Delete, Cancel task did nothing on an
/// iPhone — and its dialog popped up later over the list, once Back
/// uncovered the root (2026-10-08).
private struct CarriedXbinEnvironment: ViewModifier {
    let cx: XbinRenderContext?
    let images: XbinImages?
    let compact: CardSize?

    func body(content: Content) -> some View {
        content
            .environment(\.xbin, cx)
            .environment(\.xbinImages, images)
            .environment(\.xbinCompact, compact)
            .modifier(ConfirmHostModifier())
    }
}

/// The tree's top-level sheets (``TopLevelView``: the sheets beside a
/// `split` that is all the tree draws), presented from here while `active`
/// and `owner` (the split presents them). A split collapsed onto the app's
/// stack presents them from the screen in front — the list, or the top
/// screen pushed over it — never from one it covers, as a confirmation
/// isn't (``CarriedXbinEnvironment``). Read from the model when drawn: a
/// destination keeps the content it was pushed with, and sheets come and go.
private struct SplitSheets: ViewModifier {
    let owner: Bool
    let active: Bool

    func body(content: Content) -> some View {
        content.background {
            if active && owner { TopLevelSheets() }
        }
    }
}

/// The bottom sheets of the tree's top level, as the model has it now.
private struct TopLevelSheets: View {
    @Environment(\.xbin) private var cx

    var body: some View {
        let _ = cx?.model.revision
        if let root = cx?.model.root, root.type == "fragment" {
            ForEach(FragmentLayout(root.children).sheets) { SheetView(node: $0) }
        }
    }
}

/// The node of `key` as the model has it now: read when drawn, and drawn
/// again on each tree update (the model's revision), so content a
/// navigation destination holds never shows a node a remount replaced.
private struct LiveNode<Content: View>: View {
    let key: String
    @ViewBuilder let content: (XbinNode) -> Content
    @Environment(\.xbin) private var cx

    var body: some View {
        let _ = cx?.model.revision
        if let node = cx?.model.node(key) { content(node) }
    }
}

/// A collapsed split with a navigation stack of its own.
private struct OwnStackSplit: View {
    let node: XbinNode
    let primary: XbinNode
    let secondary: XbinNode
    let inSheet: Bool
    @Environment(\.xbin) private var cx

    var body: some View {
        let n = node
        let context = cx
        let path = mainBinding(
            get: { SplitState(n).detail ? [SplitPath.detail] : [] },
            set: { (new: [SplitPath]) in
                if new.isEmpty, SplitState(n).detail { context?.emit(n, "close") }
            }
        )
        NavigationStack(path: path) {
            NodeView(node: primary)
                .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: false, inSheet: inSheet, drawersHosted: true,
                                                     covered: SplitState(n).detail))
                .navigationDestination(for: SplitPath.self) { _ in
                    NodeView(node: secondary)
                        .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: true, inSheet: inSheet, drawersHosted: true))
                }
        }
        .modifier(DrawersModifier(drawers: FragmentLayout.drawers(ofScreens: [primary, secondary])))
    }
}

/// The one destination of a collapsed split.
private enum SplitPath: Hashable { case detail }

#if XBIN_SDK_27_1
/// Two panes on an iPhone Duo (iOS 27.1): the system's arrangement of a
/// primary and a secondary pane, which keeps them off the hinge. The names
/// are the design's (plans/native.md §15, from secondary sources) and are
/// to be confirmed against the 27.1 SDK before the flag is turned on
/// (native/AGENTS.md).
@available(iOS 27.1, *)
private struct DuoSplit: View {
    let primary: XbinNode
    let secondary: XbinNode
    let flags: XbinNavFlags

    var body: some View {
        ArrangementView {
            NavigationStack { NodeView(node: primary) }
                .environment(\.xbinNav, flags)
            NavigationStack { NodeView(node: secondary) }
                .environment(\.xbinNav, flags)
        }
    }
}
#endif

/// `tabs`: a segmented control over the selected tab's content, or (style
/// `bar`) a `TabView`. `selected` is controlled (`change {key}`); only the
/// selected tab is materialized by the runtime.
struct TabsView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinPlacement) private var placement

    var body: some View {
        let state = TabsState(node)
        let selection = mainBinding(
            get: { TabsState(node).selected },
            set: { key in
                if key != TabsState(node).selected { cx?.emit(node, "change", ["key": .string(key)]) }
            }
        )
        if node.props.string("style") == "bar" {
            TabView(selection: selection) {
                ForEach(state.tabs) { tab in
                    TabPane(tab: tab)
                        .environment(\.xbinInTabBar, true)
                        .tabItem {
                            Label(TabsState.title(tab), systemImage: XbinIcons.symbol(tab.props.string("icon")) ?? "circle")
                        }
                        .tag(TabsState.key(tab))
                        .badge(tab.props.nonEmpty("badge").map { Text(verbatim: $0) })
                }
            }
        } else {
            let picker = Picker(selection: selection) {
                ForEach(state.tabs) { tab in
                    Text(verbatim: TabsState.title(tab) + (tab.props.nonEmpty("badge").map { " \($0)" } ?? ""))
                        .tag(TabsState.key(tab))
                }
            } label: {
                Text("Tabs")
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            if placement == .list {
                Section {
                    picker
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets())
                }
                if let current = state.current { ForEach(current.children) { NodeView(node: $0) } }
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    picker
                    if let current = state.current { ForEach(current.children) { NodeView(node: $0) } }
                }
            }
        }
    }
}

/// A bar tab's content: its one child, several in a scroll view, or a
/// spinner while the runtime materializes it.
private struct TabPane: View {
    let tab: XbinNode

    var body: some View {
        if tab.children.count == 1 {
            NodeView(node: tab.children[0])
        } else if tab.children.isEmpty {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) { ForEach(tab.children) { NodeView(node: $0) } }.padding(16)
            }
            .safeAreaPadding(.bottom, 16)
            .background(XbinColor.background)
        }
    }
}

/// `sheet`: presented while `open` (default true); swiping it away reports
/// `dismiss` (→ `open: false`). Its `toolbar` goes to the sheet's bar: a
/// plain button as the cancel action, primary ones as the confirm action.
/// `edge="leading"` is a drawer (``DrawerLayer``), laid over its container
/// rather than presented.
struct SheetView: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @Environment(\.xbinNav) private var nav

    var body: some View {
        let props = SheetProps(node)
        if props.isDrawer {
            // A drawer reached here (not laid over a container: the root,
            // a list row) draws itself in place.
            DrawerLayer(node: node)
        } else if cx?.options.inlineSheets == true {
            if props.isOpen { InlineSheet(node: node) }
        } else {
            let presented = mainBinding(
                get: { SheetProps(node).isOpen },
                set: { open in
                    if !open && SheetProps(node).isOpen { cx?.emit(node, "dismiss") }
                }
            )
            let context = cx
            if props.isFull {
                // `full` (rev 2): a full-screen cover — no swipe to dismiss.
                Color.clear
                    .frame(width: 0, height: 0)
                    .fullScreenCover(isPresented: presented) {
                        SheetContent(node: node)
                            .environment(\.xbin, context)
                            .environment(\.xbinImages, context?.images)
                            .environment(\.xbinInTabBar, false)
                            .modifier(ConfirmHostModifier())
                            .tint(XbinColor.tint)
                    }
            } else {
                Color.clear
                    .frame(width: 0, height: 0)
                    .sheet(isPresented: presented) {
                        SheetContent(node: node)
                            .presentationDetents(Set(props.detents.map { $0 == .medium ? PresentationDetent.medium : .large }))
                            .environment(\.xbin, context)
                            .environment(\.xbinImages, context?.images)
                            .environment(\.xbinInTabBar, false)
                            .modifier(ConfirmHostModifier())
                            .tint(XbinColor.tint)
                    }
            }
        }
    }
}

/// What a sheet shows: its one screen/nav as is, else its children in a
/// form with the sheet's title and toolbar; a sheet among its children
/// (rev 2) presents over it.
struct SheetContent: View {
    let node: XbinNode

    var body: some View {
        SheetContentBody(node: node)
            .modifier(SheetsModifier(sheets: SheetProps(node).nested))
    }
}

private struct SheetContentBody: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    var body: some View {
        let props = SheetProps(node)
        if props.isWhole {
            NodeView(node: props.body[0])
                .environment(\.xbinNav, XbinNavFlags(inNavigation: false, pushed: false, inSheet: true))
        } else {
            NavigationStack {
                SheetBody(nodes: props.body)
                    .navigationTitle(props.title ?? "")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        SheetToolbar(toolbar: props.toolbar) { cx?.emit(node, "dismiss") }
                    }
            }
            .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: false, inSheet: true))
        }
    }
}

/// A sheet's children: a form, unless it holds a transcript (which scrolls
/// itself).
private struct SheetBody: View {
    let nodes: [XbinNode]

    var body: some View {
        if nodes.contains(where: { $0.type == "transcript" }) {
            VStack(spacing: 0) { ForEach(nodes) { NodeView(node: $0) } }
                .environment(\.xbinPlacement, .free)
        } else {
            Form { ForEach(nodes) { NodeView(node: $0) } }
                .environment(\.xbinPlacement, .list)
        }
    }
}

private struct SheetToolbar: ToolbarContent {
    let toolbar: XbinNode?
    let dismiss: () -> Void

    var body: some ToolbarContent {
        let items = toolbar?.children ?? []
        let cancel = items.first { $0.type == "button" && $0.props.string("role") == "plain" }
        let confirm = items.filter { $0 !== cancel && $0.type == "button" && $0.props.string("role") == "primary" }
        let rest = items.filter { n in n !== cancel && !confirm.contains { $0 === n } }
        ToolbarItem(placement: .cancellationAction) {
            Group {
                if let cancel {
                    NodeView(node: cancel).fixedSize()
                } else {
                    Button(action: dismiss) { Image(systemName: XbinIcons.UI.close) }
                        .accessibilityLabel("Close")
                }
            }
            .environment(\.xbinPlacement, .toolbar)
        }
        ToolbarItemGroup(placement: .primaryAction) {
            ForEach(rest) { NodeView(node: $0).fixedSize() }.environment(\.xbinPlacement, .toolbar)
        }
        ToolbarItemGroup(placement: .confirmationAction) {
            ForEach(confirm) { NodeView(node: $0).fixedSize() }.environment(\.xbinPlacement, .toolbar)
        }
    }
}

/// An open sheet drawn in place — a card over the bottom of the view with a
/// scrim — for snapshots and previews (``XbinRenderOptions/inlineSheets``).
private struct InlineSheet: View {
    let node: XbinNode

    var body: some View {
        let props = SheetProps(node)
        let fraction: CGFloat = props.isFull ? 1 : props.detents.first == .medium ? 0.56 : 0.92
        ZStack(alignment: .bottom) {
            Color.black.opacity(0.28).ignoresSafeArea()
            VStack(spacing: 0) {
                if !props.isFull {
                    Capsule().fill(XbinColor.muted.opacity(0.5)).frame(width: 36, height: 5).padding(.vertical, 6)
                }
                SheetContent(node: node)
            }
            .frame(maxWidth: .infinity)
            .containerRelativeFrame(.vertical) { length, _ in length * fraction }
            .background(XbinColor.background,
                        in: UnevenRoundedRectangle(topLeadingRadius: 14, topTrailingRadius: 14, style: .continuous))
            .modifier(ConfirmHostModifier())
        }
    }
}

/// `sheet edge="leading"`: a drawer that slides in from the leading edge
/// over the view it is laid on (the reference renderer's
/// `.sheet.edge-leading`: 86 % of the width, at most 400 points), over a
/// dimmed backdrop. A tap on the backdrop, dragging the backdrop towards
/// the leading edge, or the accessibility escape gesture closes it and
/// reports `dismiss` (→ `open: false`); its own content is the sheet's (a
/// whole screen, or its children in a form with its title and toolbar).
/// The drawer itself doesn't follow drags: its rows' swipe actions go the
/// same way.
struct DrawerLayer: View {
    let node: XbinNode
    @Environment(\.xbin) private var cx
    @State private var offset: CGFloat = 0

    var body: some View {
        let open = SheetProps(node).isOpen
        GeometryReader { geo in
            let width = CGFloat(DrawerMetrics.width(container: Double(geo.size.width)))
            ZStack(alignment: .leading) {
                if open {
                    Color.black
                        .opacity(DrawerMetrics.scrim(offset: Double(offset), width: Double(width)))
                        .ignoresSafeArea()
                        .contentShape(Rectangle())
                        .onTapGesture { dismiss() }
                        .gesture(backdropDrag(width: width))
                        .accessibilityHidden(true)
                        .transition(.opacity)
                    DrawerPanel(node: node)
                        .frame(width: width)
                        .frame(maxHeight: .infinity)
                        .background(XbinColor.background)
                        .clipShape(UnevenRoundedRectangle(bottomTrailingRadius: 16, topTrailingRadius: 16, style: .continuous))
                        .shadow(color: Color.black.opacity(0.18), radius: 18, x: 0, y: 0)
                        .ignoresSafeArea(edges: .vertical)
                        .offset(x: -offset)
                        .accessibilityAddTraits(.isModal)
                        .accessibilityAction(.escape) { dismiss() }
                        .transition(.move(edge: .leading))
                }
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .leading)
        }
        .allowsHitTesting(open)
        .animation(.snappy(duration: 0.3), value: open)
        .onChange(of: open) { offset = 0 }
    }

    private func dismiss() {
        guard SheetProps(node).isOpen else { return }
        cx?.emit(node, "dismiss")
    }

    /// The backdrop follows a drag towards the leading edge; letting go
    /// past a third of the drawer (or flinging it) closes it.
    private func backdropDrag(width: CGFloat) -> some Gesture {
        DragGesture(minimumDistance: 8)
            .onChanged { value in
                offset = CGFloat(DrawerMetrics.offset(translation: Double(value.translation.width)))
            }
            .onEnded { value in
                let moved = DrawerMetrics.offset(translation: Double(value.translation.width))
                let predicted = DrawerMetrics.offset(translation: Double(value.predictedEndTranslation.width))
                if DrawerMetrics.dismisses(offset: moved, predicted: predicted, width: Double(width)) {
                    dismiss()
                } else {
                    withAnimation(.snappy) { offset = 0 }
                }
            }
    }
}

/// A drawer's content: the sheet's, with its own confirmation host.
private struct DrawerPanel: View {
    let node: XbinNode

    var body: some View {
        SheetContent(node: node)
            .environment(\.xbinInTabBar, false)
            .modifier(ConfirmHostModifier())
    }
}
#endif
