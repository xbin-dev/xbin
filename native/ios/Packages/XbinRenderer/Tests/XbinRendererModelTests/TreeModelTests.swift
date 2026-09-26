import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

@MainActor
@Suite struct TreeModelTests {
    static let form = """
    {"k":"r","t":"screen","p":{"title":"Form","style":"form"},"c":[
      {"k":"r.0","t":"section","c":[
        {"k":"r.0.0","t":"field","p":{"label":"Name","value":"","hint":"h"},"e":["input"]},
        {"k":"r.0.1","t":"toggle","p":{"label":"On","value":true},"e":["change"]},
        {"k":"r.0.2","t":"toggle","p":{"label":"Free"}},
        {"k":"r.0.3","t":"button","p":{"label":"Go"},"e":["tap"]},
        {"k":"r.0.4","t":"disclosure","p":{"title":"More"}}
      ]}
    ]}
    """

    @Test func mountBuildsNodes() throws {
        let (store, model, _) = mounted(try node(Self.form))
        #expect(model.count == store.tree.count && model.count == 7)
        let root = try #require(model.root)
        #expect(root.key == "r" && root.type == "screen" && root.props.string("title") == "Form")
        #expect(root.children.map(\.key) == ["r.0"])
        #expect(root.children[0].children.map(\.type) == ["field", "toggle", "toggle", "button", "disclosure"])
        #expect(model.node("r.0.3")?.listens(to: "tap") == true)
    }

    @Test func patchesUpdateOnlyTheirNodes() throws {
        let (store, model, _) = mounted(try node(Self.form))
        let field = try #require(model.node("r.0.0"))
        let section = try #require(model.node("r.0"))
        store.apply(.patch([
            .set(key: "r.0.0", props: ["hint": "new"]),
            .insert(parent: "r.0", index: 0, node: Node(key: "r.0.9", type: "text", props: ["text": "hi"])),
            .remove(key: "r.0.4"),
            .move(key: "r.0.3", parent: "r.0", index: 1),
            .events(key: "r.0.1", events: []),
        ], n: 2))
        #expect(model.node("r.0.0") === field && field.props.string("hint") == "new")
        #expect(section.children.map(\.key) == ["r.0.9", "r.0.3", "r.0.0", "r.0.1", "r.0.2"])
        #expect(model.node("r.0.4") == nil && model.node("r.0.9")?.props.string("text") == "hi")
        #expect(model.node("r.0.1")?.events == [])
        #expect(model.revision == store.revision)
    }

    @Test func aTypeChangeUnderOneKeyIsANewNode() throws {
        let (store, model, _) = mounted(try node(Self.form))
        let old = try #require(model.node("r.0.3"))
        store.apply(.patch([.remove(key: "r.0.3"), .insert(parent: "r.0", index: 3, node: Node(key: "r.0.3", type: "badge"))], n: 2))
        let new = try #require(model.node("r.0.3"))
        #expect(new !== old && new.type == "badge" && model.node("r.0")?.children[3] === new)
    }

    @Test func eventsCarryTheAppliedSequence() throws {
        let (store, model, sent) = mounted(try node(Self.form), n: 7)
        #expect(model.emit("r.0.3", "tap"))
        store.apply(.patch([.set(key: "r.0.0", props: ["hint": "x"])], n: 8))
        #expect(model.emit("r.0.3", "tap", [:]))
        #expect(sent.events.map(\.n) == [7, 8])
        #expect(sent.calls.first?.javaScript == #"xbn.event("r.0.3","tap",{},7)"#)
        // Not listened, reports nothing: not sent.
        #expect(!model.emit("r.0.4", "tap"))
        #expect(!model.emit("nope", "tap"))
    }

    @Test func boundControlledPropsShowTheUsersValueAndAlwaysReport() throws {
        let (_, model, sent) = mounted(try node(Self.form))
        let field = try #require(model.node("r.0.0"))
        #expect(model.emit("r.0.0", "input", ["value": "ab"]))
        #expect(field.value("value") == "ab" && field.props.string("value") == "ab")
        // `change` isn't listened to, but it reports the bound value: sent.
        #expect(model.emit("r.0.0", "change", ["value": "abc"]))
        #expect(sent.events.map(\.type) == ["input", "change"])
        // The toggle's change is listened to; the unbound toggle reports into
        // renderer state and sends nothing.
        #expect(model.emit("r.0.1", "change", ["value": false]))
        #expect(model.node("r.0.1")?.value("value") == false)
        #expect(!model.emit("r.0.2", "change", ["value": true]))
        let free = try #require(model.node("r.0.2"))
        #expect(free.value("value") == true && free.ui["value"] == true && !free.binds("value"))
        // Unbound disclosure: renderer-owned open.
        #expect(!model.emit("r.0.4", "toggle", ["open": true]))
        #expect(model.node("r.0.4")?.value("open") == true)
    }

    @Test func anUnrelatedSetKeepsWhatTheUserTyped() throws {
        let (store, model, _) = mounted(try node(Self.form))
        let field = try #require(model.node("r.0.0"))
        model.emit("r.0.0", "input", ["value": "dmitri@"])
        // The tile validates: only `error` changes (its shadow already has
        // the typed text, so `value` is not re-sent).
        store.apply(.patch([.set(key: "r.0.0", props: ["error": "Not an email address yet"])], n: 2))
        #expect(field.props.string("value") == "dmitri@" && field.props.string("error") == "Not an email address yet")
    }

    @Test func aResetTheTreeAlreadyHeldStillApplies() throws {
        // Composer after send: the tree's value was "" all along (typing
        // never came back as a patch); the tile resets its draft to "" and
        // the runtime re-sends value "" — a same-value set.
        let (store, model, sent) = mounted(try node("""
        {"k":"r","t":"composer","p":{"value":"","placeholder":"Message"},"e":["input","send"]}
        """))
        let c = try #require(model.root)
        model.emit("r", "input", ["value": "hello"])
        model.emit("r", "send", ["value": "hello"])
        #expect(c.props.string("value") == "hello" && sent.events.count == 2)
        store.apply(.patch([.set(key: "r", props: ["value": ""])], n: 2))
        #expect(c.props.string("value") == "")
    }

    @Test func aRefusedToggleSnapsBack() throws {
        let (store, model, _) = mounted(try node(Self.form))
        let t = try #require(model.node("r.0.1"))
        model.emit("r.0.1", "change", ["value": false])
        #expect(t.value("value") == false)
        store.apply(.patch([.set(key: "r.0.1", props: ["value": true])], n: 2))
        #expect(t.value("value") == true)
    }

    @Test func aChangedTileValueWins() throws {
        let (store, model, _) = mounted(try node(Self.form))
        let field = try #require(model.node("r.0.0"))
        model.emit("r.0.0", "input", ["value": "a"])
        store.apply(.patch([.set(key: "r.0.0", props: ["value": "A"])], n: 2))
        #expect(field.props.string("value") == "A")
        // Unset: the prop goes away (the renderer owns it from now on).
        store.apply(.patch([.unset(key: "r.0.0", props: ["value"])], n: 3))
        #expect(!field.binds("value") && field.value("value") == nil)
    }

    @Test func sheetDismissReportsOpenFalse() throws {
        let (_, model, sent) = mounted(try node("""
        {"k":"r","t":"fragment","c":[{"k":"r.0","t":"sheet","p":{"open":true,"title":"S"},"e":["dismiss"]},
          {"k":"r.1","t":"sheet","p":{"title":"Unbound"}}]}
        """))
        #expect(model.emit("r.0", "dismiss"))
        #expect(model.node("r.0")?.value("open") == false)
        #expect(sent.events.first?.payload == [:])
        // Unbound and unlistened: closes locally, sends nothing.
        #expect(!model.emit("r.1", "dismiss"))
        #expect(model.node("r.1").map { SheetProps($0).isOpen } == false)
    }

    @Test func tabsReportTheSelectedKey() throws {
        let (_, model, sent) = mounted(try node("""
        {"k":"r","t":"tabs","p":{"selected":"a"},"e":["change"],"c":[
          {"k":"r.0","t":"tab","p":{"key":"a","title":"A"},"c":[]},{"k":"r.1","t":"tab","p":{"key":"b"},"c":[]}]}
        """))
        let tabs = try #require(model.root)
        #expect(TabsState(tabs).selected == "a")
        model.emit("r", "change", ["key": "b"])
        #expect(TabsState(tabs).selected == "b" && TabsState(tabs).current?.key == "r.1")
        #expect(sent.events.map(\.payload) == [["key": "b"]])
        #expect(TabsState.title(tabs.children[1]) == "b")
    }

    @Test func remountAndResetRebuild() throws {
        let (store, model, _) = mounted(try node(Self.form))
        let old = try #require(model.root)
        store.apply(.mount(version: 1, root: Node(key: "r", type: "text", props: ["text": "x"]), n: 5))
        #expect(model.root !== old && model.root?.type == "text" && model.count == 1)
        store.reset()
        #expect(model.root == nil && model.count == 0)
    }

    @Test func failureIsObserved() throws {
        let (store, model, _) = mounted(try node(Self.form))
        store.apply(.patch([.remove(key: "nope")], n: 2))
        #expect(model.failure != nil)
        store.apply(.mount(version: 1, root: Node(key: "r", type: "text"), n: 3))
        #expect(model.failure == nil)
    }

    @Test func aModelCreatedAfterTheMountSeesTheTree() throws {
        let store = try XbinFixtures.store("controls", in: fixtureSet())
        let model = XbinTreeModel(store: store)
        #expect(model.count == store.tree.count && model.root?.type == "screen")
    }

    @Test func everyFixtureMountsWithKnownPrimitives() throws {
        let set = try fixtureSet()
        let names = try set.names()
        #expect(names.count >= 19)
        for name in names {
            let fixture = try set.expected(name)
            let unknown = XbinFixtures.types(fixture.root).subtracting(XbinVocabulary.primitives.keys)
            #expect(unknown.isEmpty, "\(name) uses \(unknown)")
            let model = XbinTreeModel(store: try XbinFixtures.store(name, in: set))
            #expect(model.count == fixture.root.subtreeCount, "\(name)")
        }
    }
}
