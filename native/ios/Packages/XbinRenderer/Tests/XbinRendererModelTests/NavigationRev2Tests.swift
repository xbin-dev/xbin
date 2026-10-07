import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// Vocabulary rev 2 (D189) against its fixtures: the collapsing split,
/// toolbars by place, a row's actions per edge, submenus, the full-screen
/// and stacked sheets, scroll targets and edges, search and refresh.
@MainActor
@Suite struct NavigationRev2Tests {
    func model(_ fixture: String) throws -> (XbinTreeModel, Sent) {
        let sent = Sent()
        let m = XbinTreeModel(store: try XbinFixtures.store(fixture, in: fixtureSet())) { sent.calls.append($0) }
        return (m, sent)
    }

    @Test func aSplitWithDetailCollapsesOnCompactWidth() throws {
        let (m, sent) = try model("split-collapse")
        let split = try #require(m.root)
        #expect(SplitLayout(split, regularWidth: false) == .collapsed)
        #expect(SplitLayout(split, regularWidth: true) == .columns)
        let state = SplitState(split)
        #expect(state.detail && state.columns == .detail)
        // A rev-1 split (no detail) still stacks when compact.
        let (old, _) = try model("split")
        #expect(SplitLayout(try #require(old.root), regularWidth: false) == .stacked)
        #expect(SplitLayout(children: 2, regularWidth: true, prefer: "single", collapses: true) == .collapsed)
        #expect(SplitLayout(children: 1, regularWidth: false, prefer: nil, collapses: true) == .stacked)
        // Back reports close: the detail is closed at once (a constant report).
        #expect(m.emit(split.key, "close"))
        #expect(SplitState(split).detail == false)
        #expect(m.emit(split.key, "columns", ["value": "all"]))
        #expect(SplitState(split).columns == .all)
        #expect(sent.events.map(\.type) == ["close", "columns"])
    }

    /// A value the person set lives on in the store: a second model of the
    /// same runtime (another screen) starts from it.
    @Test func aReportedValueReachesTheNextModel() throws {
        let store = try XbinFixtures.store("split-collapse", in: fixtureSet())
        let m = XbinTreeModel(store: store) { _ in }
        let split = try #require(m.root)
        #expect(m.emit(split.key, "columns", ["value": "all"]))
        let again = XbinTreeModel(store: store) { _ in }
        #expect(SplitState(try #require(again.root)).columns == .all)
    }

    @Test func toolbarsGoWhereTheirPlaceSays() throws {
        let (m, _) = try model("search-toolbars")
        let layout = ScreenLayout(try #require(m.root))
        #expect(layout.leadingToolbar?.key == "r.0" && layout.toolbar?.key == "r.1" && layout.bottomToolbar?.key == "r.2")
        #expect(layout.body.map(\.type) == ["list"], "no toolbar in the body")
        // A rev-1 toolbar (no place) is the trailing one.
        let (nav, _) = try model("structure-nav")
        let screen = try #require(nav.root?.children.first)
        #expect(ScreenLayout(screen).toolbar != nil && ScreenLayout(screen).leadingToolbar == nil)
    }

    @Test func rowActionsByEdgeAndSubmenus() throws {
        let (m, _) = try model("search-toolbars")
        let row = try #require(m.node("r.3.0:i-412"))
        let a = RowActions(row)
        #expect(a.leading.map { $0.props.string("label") } == ["Mark read"])
        #expect(a.trailing.map { $0.props.string("label") } == ["Archive", "Delete"])
        #expect(a.leadingFull && a.trailingFull)
        #expect(a.trailingSwipeOrder.map { $0.props.string("label") } == ["Archive", "Delete"], "full: the first outermost")
        #expect(a.all.count == 3)
        // Rev 1 (one actions, no full): reversed, the destructive outermost.
        let (folded, _) = try model("sections-rows")
        func rows(_ n: XbinNode) -> [XbinNode] { (n.type == "row" ? [n] : []) + n.children.flatMap(rows) }
        let foldedRoot = try #require(folded.root)
        let withActions = try #require(rows(foldedRoot).first { !RowActions($0).isEmpty })
        let old = RowActions(withActions)
        #expect(!old.trailingFull && old.leading.isEmpty && old.trailingSwipeOrder.map(\.key) == old.trailing.reversed().map(\.key))
        let menu = try #require(m.node("r.0.0"))
        let kinds = MenuItems.items(menu).map { item -> String in
            switch item {
            case .button: return "button"
            case .divider: return "divider"
            case .submenu(let s): return "submenu:\(s.props.string("label") ?? "")"
            }
        }
        #expect(kinds == ["button", "button", "divider", "submenu:Group by"])
    }

    @Test func aFullScreenSheetWithOneStackedOverIt() throws {
        let (m, _) = try model("sheet-stack")
        let outer = SheetProps(try #require(m.node("r.1")))
        #expect(outer.isFull && outer.isOpen && !outer.isWhole)
        #expect(outer.nested.map(\.key) == ["r.1.3"])
        #expect(!outer.body.contains { $0.type == "sheet" })
        let inner = SheetProps(outer.nested[0])
        #expect(!inner.isFull && inner.detents == [.medium] && inner.nested.isEmpty)
    }

    @Test func scrollTargetsKeysAndEdges() throws {
        #expect(ScrollTarget("end#3") == .end && ScrollTarget("start") == .start && ScrollTarget("m-5#x") == .key("m-5"))
        #expect(ScrollTarget("") == nil && ScrollTarget(nil) == nil && ScrollTarget("#2") == nil)
        let (m, _) = try model("split-collapse")
        let transcript = try #require(m.node("r.1.1"))
        #expect(ScrollKeys.child(of: transcript, key: "m-33")?.key == "r.1.1.0:m-33")
        #expect(ScrollKeys.child(of: transcript, key: "r.1.1.0:m-31")?.key == "r.1.1.0:m-31")
        #expect(ScrollKeys.child(of: transcript, key: "m-3") == nil, "a suffix after the colon only")
        var e = ScrollEdges()
        #expect(e.update(start: true, end: false).map { "\($0.edge)=\($0.at)" } == ["start=true", "end=false"])
        #expect(e.update(start: true, end: false).isEmpty)
        #expect(e.update(start: false, end: true).map { "\($0.edge)=\($0.at)" } == ["start=false", "end=true"])
        let at = ScrollEdges.at(offset: 0, viewport: 800, content: 2000)
        #expect(at.start && !at.end)
        #expect(ScrollEdges.at(offset: 1190, viewport: 800, content: 2000).end)
        let short = ScrollEdges.at(offset: -400, viewport: 800, content: 300)
        #expect(short.start && short.end, "content that fits shows both ends")
    }

    @Test func searchScopesAndSuggestions() throws {
        let (m, sent) = try model("search-toolbars")
        let screen = try #require(m.root)
        let s = SearchProps(screen)
        #expect(s.scopes.map(\.label) == ["Open", "Closed", "All"] && s.scope == "open")
        #expect(s.suggestions.map(\.value) == ["deadlock", "label:billing", "author:ana"])
        #expect(s.suggestions[1].icon == "tag" && s.suggestions[0].icon == nil)
        #expect(m.emit(screen.key, "scope", ["value": "closed"]))
        #expect(SearchProps(screen).scope == "closed")
        #expect(m.emit(screen.key, "submit", ["value": "retry"]))
        #expect(Props.text(screen.value("search")) == "retry")
        #expect(sent.events.map(\.type) == ["scope", "submit"])
    }

    @Test func refreshEndsWhenTheTileSaysSo() {
        typealias R = RefreshCompletion
        // the tile sets refreshing, then clears it
        var p = R.next(.waitingForStart, refreshing: false, elapsed: .milliseconds(100))
        #expect(p == .waitingForStart)
        p = R.next(p, refreshing: true, elapsed: .milliseconds(200))
        #expect(p == .running)
        #expect(R.next(p, refreshing: true, elapsed: .seconds(30)) == .running)
        #expect(R.next(p, refreshing: false, elapsed: .seconds(31)) == .done)
        // a reload that never shows: done after the start window
        #expect(R.next(.waitingForStart, refreshing: false, elapsed: .seconds(1)) == .done)
        // never past the limit
        #expect(R.next(.running, refreshing: true, elapsed: .seconds(60)) == .done)
    }

    /// Every rev-2 prop, event, enum value and child is named by the
    /// vocabulary with `since: 2` (`enumSince`, the child rule's `since`) on a
    /// primitive at rev 2 — the table this renderer reports in caps.
    @Test func revisionTwoIsWhatTheRendererClaims() throws {
        let prims = try #require(try vocabJSON()["prims"]?.objectValue)
        var rev2: Set<String> = []
        for (name, p) in prims {
            for (_, s) in p["props"]?.objectValue ?? [:] where s["since"]?.intValue == 2 || s["enumSince"] != nil { rev2.insert(name) }
            for (_, s) in p["events"]?.objectValue ?? [:] where s["since"]?.intValue == 2 { rev2.insert(name) }
            if p["children"]?["since"] != nil { rev2.insert(name) }
        }
        #expect(rev2 == Set(XbinVocabulary.primitives.filter { $0.value == 2 }.keys))
        #expect(rev2 == ["screen", "toolbar", "list", "actions", "sheet", "split", "menu", "transcript"])
    }
}
