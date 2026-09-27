import Foundation
import Testing
@testable import XbinCore

// A tile's widget (D125): the `target:"widget"` tree messages, the widget
// store inside a TreeStore, the calls back, the caps, the on-disk cache
// and the runtime pool's policy.

@Suite struct WidgetBridgeTests {
    static let widgetRoot = #"{"k":"w","t":"stack","c":[{"k":"w.0","t":"text","p":{"text":"Count: 3","type":"title"}},{"k":"w.1","t":"button","p":{"label":"+1"},"e":["tap"]}]}"#

    @Test func decodesWidgetTreeMessages() throws {
        let m = try BridgeMessage(parsing: #"{"op":"mount","target":"widget","v":1,"n":1,"root":\#(Self.widgetRoot)}"#)
        guard case .widget(.mount(1, let root, 1)) = m else { Issue.record("not a widget mount: \(m)"); return }
        #expect(root.key == "w" && root.children.count == 2)
        #expect(m.op == "mount" && m.target == .widget)

        let p = try BridgeMessage(parsing: #"{"op":"patch","target":"widget","n":2,"ops":[["set","w.0",{"text":"Count: 4"}]]}"#)
        guard case .widget(.patch(let ops, 2)) = p else { Issue.record("not a widget patch: \(p)"); return }
        #expect(ops == [.set(key: "w.0", props: ["text": "Count: 4"])])

        // An explicit main target is the main tree; a target from the future
        // is not ours; a non-string target is a bad message.
        let main = try BridgeMessage(parsing: #"{"op":"mount","target":"main","v":1,"root":{"k":"r","t":"text"}}"#)
        guard case .mount = main else { Issue.record("not a main mount"); return }
        #expect(main.target == .main)
        let later = try BridgeMessage(parsing: #"{"op":"patch","target":"watch","ops":[]}"#)
        guard case .unknown(op: "patch", _) = later else { Issue.record("a later target should be unknown"); return }
        #expect(throws: BridgeDecodingError.self) { try BridgeMessage(parsing: #"{"op":"mount","target":1,"root":{"k":"r","t":"text"}}"#) }
        // Only tree messages have targets: a meta's `target` is just a field.
        guard case .meta = try BridgeMessage(parsing: #"{"op":"meta","target":"widget","title":"x"}"#) else {
            Issue.record("meta with a target"); return
        }
    }

    @Test func widgetMessagesRoundTrip() throws {
        let m = try BridgeMessage(parsing: #"{"op":"mount","target":"widget","v":1,"n":7,"root":\#(Self.widgetRoot)}"#)
        #expect(m.json["target"] == "widget" && m.json["op"] == "mount" && m.json["n"] == 7)
        #expect(try BridgeMessage(json: m.json) == m)
    }

    @Test func eventsCarryTheirTarget() {
        #expect(RuntimeCall.event(key: "w.1", type: "tap", payload: [:], n: 3, target: .widget).javaScript
                == #"xbn.event("w.1","tap",{},3,"widget")"#)
        // No sequence yet: n is null, so the target stays the fifth argument.
        #expect(RuntimeCall.event(key: "w.1", type: "tap", payload: [:], target: .widget).javaScript
                == #"xbn.event("w.1","tap",{},null,"widget")"#)
        // The main tree's events are what every runtime has always understood.
        #expect(RuntimeCall.event(key: "r.0", type: "tap", payload: [:], n: 3).javaScript == #"xbn.event("r.0","tap",{},3)"#)
        #expect(RuntimeCall.event(key: "r.0", type: "tap", payload: [:], target: .main).javaScript == #"xbn.event("r.0","tap",{})"#)
    }

    @Test func sizeAndRemountCalls() {
        #expect(RuntimeCall.widgetSize(.wide).javaScript == #"xbn.widgetSize?.("wide")"#)
        #expect(RuntimeCall.widgetSize(.small).functionBody == #"return xbn.widgetSize?.("small");"#)
        #expect(RuntimeCall.remount(.widget).javaScript == #"xbn.remount("widget")"#)
        #expect(RuntimeCall.remount().javaScript == "xbn.remount()")
    }

    @Test func capsAskForTheWidget() throws {
        let base = NativeCaps(renderer: "ios", app: "1.0", prims: ["text": 1], features: ["chart.area"])
        #expect(!base.drawsWidgets && base.json["widgetSize"] == nil)
        let caps = base.withWidget(size: .small)
        #expect(caps.drawsWidgets && caps.supports("widget") && caps.widgetSize == .small)
        #expect(caps.json["features"] == ["chart.area", "widget"])
        #expect(caps.json["widgetSize"] == "small")
        #expect(caps.withWidget(size: .wide).features == ["chart.area", "widget"]) // not twice
        #expect(try NativeCaps(json: caps.json) == caps)
        let script = RuntimeScript.documentStart(caps: caps.withWidget(size: .wide), state: nil)
        #expect(script.contains(#""widgetSize":"wide""#) && script.contains(#""widget""#))
    }
}

@Suite struct TreeStoreWidgetTests {
    final class Recorder {
        var events: [TreeStoreEvent] = []
    }

    func widgetMount(_ count: Int = 3, n: Int = 1) throws -> BridgeMessage {
        try BridgeMessage(parsing: #"{"op":"mount","target":"widget","v":1,"n":\#(n),"root":{"k":"w","t":"stack","c":[{"k":"w.0","t":"text","p":{"text":"Count: \#(count)"}},{"k":"w.1","t":"button","p":{"label":"+1"},"e":["tap"]}]}}"#)
    }

    func mainMount() throws -> BridgeMessage { try Resources.tree("18.1-counter-go").mountMessage }

    @Test func widgetTreesGoToTheWidgetStore() throws {
        let store = TreeStore()
        let widget = try #require(store.widget)
        #expect(store.target == .main && widget.target == .widget && widget.widget == nil)
        let main = Recorder(), wrec = Recorder()
        let o1 = store.observe { main.events.append($0) }
        let o2 = widget.observe { wrec.events.append($0) }

        store.apply(try mainMount())
        let e = store.apply(try widgetMount())
        #expect(widget.isMounted && widget.revision == 1 && widget.treeSequence == 1)
        #expect(store.revision == 1 && store.tree["w"] == nil && widget.tree["r"] == nil)
        guard case .tree(1, let d)? = e else { Issue.record("no widget mount event"); return }
        #expect(d.remounted)
        #expect(main.events.count == 1 && wrec.events.count == 1) // each store tells its own observers

        store.apply(.widget(.patch([.set(key: "w.0", props: ["text": "Count: 4"])], n: 2)))
        #expect(widget.tree["w.0"]?[prop: "text"] == "Count: 4" && widget.treeSequence == 2 && widget.revision == 2)
        // The main tree's sequence is its own.
        store.apply(.patch([.set(key: "r.0.0", props: ["detail": "43"])], n: 9))
        #expect(store.treeSequence == 9 && widget.treeSequence == 2)
        _ = (o1, o2)
    }

    @Test func widgetEventsGoBackWithTheWidgetTarget() throws {
        let store = TreeStore()
        store.apply(try widgetMount(n: 4))
        let widget = try #require(store.widget)
        #expect(widget.event("w.1", "tap") == .event(key: "w.1", type: "tap", payload: [:], n: 4, target: .widget))
        #expect(store.event("r.0", "tap") == .event(key: "r.0", type: "tap", payload: [:], n: nil, target: .main))
    }

    @Test func aBrokenWidgetFailsOnlyTheWidget() throws {
        let store = TreeStore()
        store.apply(try mainMount())
        store.apply(try widgetMount())
        let widget = try #require(store.widget)
        let e = store.apply(.widget(.patch([.remove(key: "w.9")])))
        guard case .failed = e else { Issue.record("the bad widget patch should fail the widget: \(String(describing: e))"); return }
        #expect(widget.failure != nil && store.failure == nil && store.isMounted)
        // The main tree goes on; a fresh widget mount (the remount the app
        // asks for) heals the widget.
        store.apply(.patch([.set(key: "r.0.0", props: ["detail": "43"])]))
        #expect(store.tree["r.0.0"]?[prop: "detail"] == "43")
        store.apply(try widgetMount(5, n: 3))
        #expect(widget.failure == nil && widget.tree["w.0"]?[prop: "text"] == "Count: 5")
    }

    @Test func aWidgetsRenderErrorFailsOnlyTheWidget() throws {
        let store = TreeStore()
        store.apply(try mainMount())
        store.apply(try widgetMount())
        let widget = try #require(store.widget)
        let e = store.apply(try BridgeMessage(parsing: #"{"op":"error","kind":"exception","message":"boom","where":"","target":"widget"}"#))
        guard case .failed(.runtime(let err))? = e else { Issue.record("the widget should fail: \(String(describing: e))"); return }
        #expect(err.target == .widget && widget.failure != nil)
        #expect(store.failure == nil && store.lastRuntimeError == nil)
        // A second report while failed changes nothing; the tile's own
        // errors still fail the tile.
        #expect(store.apply(.error(RuntimeError(fields: ["kind": "exception", "message": "again", "target": "widget"]))) == nil)
        store.apply(.error(RuntimeError(kind: "exception", message: "main")))
        #expect(store.failure != nil)
        // A non-fatal widget error is reported like any other.
        let s2 = TreeStore()
        let e2 = s2.apply(.error(RuntimeError(fields: ["kind": "uncaught", "message": "x", "target": "widget"])))
        guard case .runtimeError? = e2 else { Issue.record("reported"); return }
        #expect(s2.failure == nil && s2.widget?.failure == nil)
    }

    @Test func aFailedTileStillTakesItsWidget() throws {
        // The main view failed (the app shows the web tile); the widget is
        // separate and keeps drawing from whatever the runtime sends.
        let store = TreeStore()
        store.apply(.error(RuntimeError(kind: "exception", message: "boom")))
        #expect(store.failure != nil)
        store.apply(try widgetMount())
        #expect(store.widget?.isMounted == true)
    }

    @Test func onlyTreesReachTheWidgetStore() throws {
        let store = TreeStore()
        let widget = try #require(store.widget)
        store.apply(.meta(NativeMeta(fields: ["title": "Counter"])))
        #expect(store.meta.title == "Counter" && widget.meta.title == nil)
        // Fed directly (never by the runtime), the widget store ignores
        // everything but trees, and nested targets.
        #expect(widget.apply(.meta(NativeMeta(fields: ["title": "x"]))) == nil)
        #expect(widget.apply(.state(["a": 1])) == nil && widget.savedState == nil)
        #expect(widget.apply(.widget(try widgetMount())) == nil && !widget.isMounted)
    }

    @Test func resetClearsBothTrees() throws {
        let store = TreeStore()
        store.apply(try mainMount())
        store.apply(try widgetMount())
        let widget = try #require(store.widget)
        let rec = Recorder()
        let o = widget.observe { rec.events.append($0) }
        store.reset()
        #expect(!store.isMounted && !widget.isMounted && widget.treeSequence == nil)
        guard case .reset(2)? = rec.events.last else { Issue.record("the widget store's observers hear the reset"); return }
        _ = o
    }
}

@Suite struct WidgetStoreTests {
    static let t0 = Date(timeIntervalSince1970: 1_790_000_000)

    func tree(_ text: String, flag: Bool = true) -> Node {
        Node(key: "w", type: "stack", children: [
            Node(key: "w.0", type: "text", props: ["text": .string(text), "selectable": .bool(flag), "lines": 2]),
        ])
    }

    func tempFile() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("widget-store-\(UUID().uuidString)")
            .appendingPathComponent("ws.json")
    }

    @Test func keepsTheLastTreePerSizeOnDisk() throws {
        let file = tempFile()
        defer { try? FileManager.default.removeItem(at: file.deletingLastPathComponent()) }
        let s = WidgetStore(file: file)
        #expect(s.snapshot("apps/counter", size: .small) == nil)
        #expect(s.record("apps/counter", root: tree("Count: 3"), size: .small, at: Self.t0))
        #expect(!s.record("apps/counter", root: tree("Count: 3"), size: .small, at: Self.t0.addingTimeInterval(5))) // unchanged
        #expect(s.record("apps/counter", root: tree("Count: 3 · wide"), size: .wide, at: Self.t0))
        #expect(s.isDirty)
        try s.save()
        #expect(!s.isDirty)

        let back = WidgetStore(file: file)
        #expect(back.snapshot("apps/counter", size: .small)?.root == tree("Count: 3"))
        #expect(back.snapshot("apps/counter", size: .wide)?.root == tree("Count: 3 · wide"))
        #expect(back.snapshot("apps/counter", size: .small)?.at == Self.t0)
        // Booleans stay booleans, integers integers.
        let props = try #require(back.snapshot("apps/counter", size: .small)?.root.children.first?.props)
        #expect(props["selectable"] == .bool(true) && props["lines"] == .int(2))

        back.removeAll()
        #expect(back.records.isEmpty && !FileManager.default.fileExists(atPath: file.path))
    }

    @Test func aMissingOrBrokenFileIsEmpty() throws {
        let file = tempFile()
        defer { try? FileManager.default.removeItem(at: file.deletingLastPathComponent()) }
        #expect(WidgetStore(file: file).records.isEmpty)
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("{not json".utf8).write(to: file)
        #expect(WidgetStore(file: file).records.isEmpty)
        try Data(#"{"v":1,"tiles":{"a":{"trees":{"huge":{"root":{"k":"w","t":"text"}},"small":{"root":{"t":"text"}}}}}}"#.utf8).write(to: file)
        #expect(WidgetStore(file: file).records.isEmpty) // an unknown size, a node without a key
    }

    @Test func boundsWhatATileMayStore() {
        let s = WidgetStore(file: nil)
        let big = Node(key: "w", type: "text", props: ["text": .string(String(repeating: "x", count: WidgetStore.maxTreeBytes))])
        #expect(!s.record("apps/big", root: big, size: .small, at: Self.t0))
        #expect(s.snapshot("apps/big", size: .small) == nil)

        for i in 0..<(WidgetStore.maxTiles + 5) {
            s.record("apps/t\(i)", root: tree("\(i)"), size: .small, at: Self.t0.addingTimeInterval(Double(i)))
        }
        #expect(s.records.count == WidgetStore.maxTiles)
        #expect(s.records["apps/t0"] == nil && s.records["apps/t4"] == nil && s.records["apps/t5"] != nil) // oldest out
    }

    @Test func remembersTilesThatDrawNoWidget() {
        let s = WidgetStore(file: nil)
        #expect(s.shouldProbe("apps/notes", now: Self.t0))
        s.record("apps/notes", root: tree("old"), size: .small, at: Self.t0)
        s.noteNoWidget("apps/notes", at: Self.t0)
        #expect(s.snapshot("apps/notes", size: .small) == nil) // the old widget is gone with it
        #expect(!s.shouldProbe("apps/notes", now: Self.t0.addingTimeInterval(3600)))
        #expect(s.shouldProbe("apps/notes", now: Self.t0.addingTimeInterval(WidgetStore.noWidgetTTL)))
        #expect(s.shouldProbe("apps/notes", now: Self.t0.addingTimeInterval(-60))) // a clock that went back
        // A widget after all (the tile's code changed): probing again.
        s.record("apps/notes", root: tree("new"), size: .small, at: Self.t0.addingTimeInterval(60))
        #expect(s.shouldProbe("apps/notes", now: Self.t0.addingTimeInterval(120)))
    }

    @Test func noWidgetSurvivesTheFile() throws {
        let file = tempFile()
        defer { try? FileManager.default.removeItem(at: file.deletingLastPathComponent()) }
        let s = WidgetStore(file: file)
        s.noteNoWidget("apps/notes", at: Self.t0)
        try s.save()
        #expect(!WidgetStore(file: file).shouldProbe("apps/notes", now: Self.t0.addingTimeInterval(10)))
    }
}

@Suite struct RuntimePoolPolicyTests {
    @Test func evictsTheLeastRecentlyUsed() {
        var p = RuntimePoolPolicy<String>(capacity: 3)
        #expect(p.show("a").isEmpty && p.show("b").isEmpty && p.show("c").isEmpty)
        #expect(p.show("d") == ["a"])
        #expect(p.live == ["b", "c", "d"])
        // Using b again makes c the oldest.
        p.hide("b")
        #expect(p.show("b").isEmpty)
        #expect(p.show("e") == ["c"])
    }

    @Test func offScreenGoesBeforeOnScreen() {
        var p = RuntimePoolPolicy<String>(capacity: 3)
        _ = p.show("a"); _ = p.show("b"); _ = p.show("c")
        p.hide("c") // scrolled away: first out, though the newest
        #expect(p.show("d") == ["c"])
        #expect(p.isShown("a") && !p.isShown("c"))
    }

    @Test func neverEvictsAnOpenTile() {
        var p = RuntimePoolPolicy<String>(capacity: 2)
        #expect(p.open("t").isEmpty)
        _ = p.show("a")
        #expect(p.show("b") == ["a"])
        #expect(p.show("c") == ["b"]) // t, the oldest, is open
        #expect(p.live == ["t", "c"])
        // With every slot open the pool grows rather than close a tile…
        var q = RuntimePoolPolicy<String>(capacity: 1)
        _ = q.open("x")
        #expect(q.open("y").isEmpty && q.live == ["x", "y"])
        // …and shrinks back once one closes.
        #expect(q.close("x") == ["x"])
        #expect(q.live == ["y"])
    }

    @Test func closingKeepsTheRuntimeWarm() {
        var p = RuntimePoolPolicy<String>(capacity: 3)
        _ = p.open("t")
        #expect(p.close("t").isEmpty && p.live.contains("t") && !p.isVisible("t"))
        // Reopened while warm: nothing starts, nothing goes.
        #expect(p.open("t").isEmpty && p.isOpen("t") && p.isVisible("t"))
        // A tile open in two windows stays pinned until both close.
        _ = p.open("t")
        _ = p.close("t")
        #expect(p.isOpen("t"))
    }

    @Test func aDroppedRuntimeLeavesTheCount() {
        var p = RuntimePoolPolicy<String>(capacity: 2)
        _ = p.show("a"); _ = p.show("b")
        p.drop("a")
        #expect(p.live == ["b"])
        #expect(p.show("c").isEmpty)
        #expect(p.isShown("a")) // its card is still there, showing the cache
    }
}
