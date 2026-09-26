#if canImport(UIKit)
import SwiftUI
import XbinCore
import XbinRendererModel

/// `screen`: a `List` (style list), a `Form` (form) or a `ScrollView`
/// (scroll, the default) with a navigation title; its `toolbar` goes to the
/// bar, a composer (or bar tabs) docks at the bottom, sheets present over
/// it. Outside a navigation container it brings its own `NavigationStack`.
struct ScreenView: View {
    let node: XbinNode
    @Environment(\.xbinNav) private var nav

    var body: some View {
        if nav.inNavigation {
            ScreenContent(node: node)
        } else {
            NavigationStack {
                ScreenContent(node: node)
            }
            .environment(\.xbinNav, XbinNavFlags(inNavigation: true, pushed: false, inSheet: nav.inSheet))
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
            .toolbar { ScreenToolbar(toolbar: layout.toolbar) }
            .modifier(RefreshModifier(node: node))
            .modifier(SearchModifier(node: node))
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if !layout.docked.isEmpty {
                    VStack(spacing: 0) { ForEach(layout.docked) { NodeView(node: $0) } }
                        .environment(\.xbinPlacement, .dock)
                }
            }
            .modifier(SheetsModifier(sheets: layout.sheets))
            .onAppear { if node.listens(to: "appear") { cx?.emit(node, "appear") } }
    }
}

/// The screen's body by style.
private struct ScreenBody: View {
    let node: XbinNode
    let layout: ScreenLayout

    var body: some View {
        switch layout.style {
        case .list:
            List { ForEach(layout.body) { NodeView(node: $0) } }
                .listStyle(.insetGrouped)
                .environment(\.xbinPlacement, .list)
        case .form:
            Form { ForEach(layout.body) { NodeView(node: $0) } }
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

/// A screen's `toolbar` items at the trailing end of the bar.
struct ScreenToolbar: ToolbarContent {
    let toolbar: XbinNode?

    var body: some ToolbarContent {
        ToolbarItemGroup(placement: .topBarTrailing) {
            ForEach(toolbar?.children ?? []) { NodeView(node: $0) }
                .environment(\.xbinPlacement, .toolbar)
        }
    }
}

/// `refreshable` + a `refresh` listener → pull to refresh.
private struct RefreshModifier: ViewModifier {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    func body(content: Content) -> some View {
        if node.props.bool("refreshable") && node.listens(to: "refresh") {
            let context = cx
            let n = node
            content.refreshable {
                await MainActor.run { context?.emit(n, "refresh") }
                // The tile answers with a patch whenever it likes; keep the
                // spinner up briefly so the gesture reads as done.
                try? await Task.sleep(for: .milliseconds(600))
            }
        } else {
            content
        }
    }
}

/// `search` present (even "") → a search field; `search {value}` reports it.
private struct SearchModifier: ViewModifier {
    let node: XbinNode
    @Environment(\.xbin) private var cx

    func body(content: Content) -> some View {
        if node.value("search") != nil {
            let text = mainBinding(
                get: { Props.text(node.value("search")) ?? "" },
                set: { cx?.emit(node, "search", ["value": .string($0)]) }
            )
            content.searchable(text: text)
        } else {
            content
        }
    }
}
#endif
