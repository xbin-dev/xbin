import Foundation
import Testing
@testable import XbinCore

@Suite struct TreeStoreTests {
    final class Recorder {
        var events: [TreeStoreEvent] = []
    }

    func counterMount() throws -> BridgeMessage { try Resources.tree("18.1-counter-go").mountMessage }

    @Test func mountThenPatch() throws {
        let store = TreeStore()
        let rec = Recorder()
        let obs = store.observe { rec.events.append($0) }
        #expect(!store.isMounted && store.revision == 0)

        store.apply(try counterMount())
        #expect(store.isMounted && store.revision == 1 && store.version == 1)
        guard case .tree(1, let d1)? = rec.events.last else { Issue.record("no mount event"); return }
        #expect(d1.remounted && d1.inserted == ["r", "r.0", "r.0.0", "r.0.1"])

        let e = store.apply(.patch([.set(key: "r.0.0", props: ["detail": "43"]), .set(key: "r.0.1", props: ["busy": true])]))
        #expect(store.revision == 2)
        guard case .tree(2, let d2)? = e else { Issue.record("no patch event"); return }
        #expect(d2.propsChanged == ["r.0.0", "r.0.1"] && !d2.remounted)
        #expect(store.tree["r.0.0"]?[prop: "detail"] == "43")
        #expect(rec.events.count == 2 && rec.events.last == e)
        obs.cancel()
        store.apply(.patch([.set(key: "r.0.0", props: ["detail": "44"])]))
        #expect(rec.events.count == 2) // cancelled
        #expect(store.revision == 3)
    }

    @Test func releasingTheTokenStopsObserving() throws {
        let store = TreeStore()
        let rec = Recorder()
        do {
            let obs = store.observe { rec.events.append($0) }
            store.apply(try counterMount())
            _ = obs
        }
        store.apply(.patch([.set(key: "r.0.0", props: ["detail": "44"])]))
        #expect(rec.events.count == 1)
    }

    @Test func observersRunInRegistrationOrder() throws {
        let store = TreeStore()
        var order: [Int] = []
        let a = store.observe { _ in order.append(1) }
        let b = store.observe { _ in order.append(2) }
        store.apply(try counterMount())
        #expect(order == [1, 2])
        _ = (a, b)
    }

    @Test func patchBeforeMountFails() {
        let store = TreeStore()
        let e = store.apply(.patch([.remove(key: "r.0")]))
        #expect(e == .failed(.patch(.notMounted)))
        #expect(store.failure == .patch(.notMounted))
    }

    @Test func aBadPatchFailsUntilTheNextMount() throws {
        let store = TreeStore()
        store.apply(try counterMount())
        let e = store.apply(.patch([.remove(key: "nope")]))
        #expect(e == .failed(.patch(.unknownKey("nope"))))
        #expect(store.revision == 1)
        // Ignored while failed.
        #expect(store.apply(.patch([.set(key: "r.0.0", props: ["detail": "x"])])) == nil)
        #expect(store.apply(.meta(NativeMeta(fields: ["title": "x"])) ) == nil)
        #expect(store.tree["r.0.0"]?[prop: "detail"] == "42")
        // A mount recovers.
        store.apply(try counterMount())
        #expect(store.failure == nil && store.revision == 2)
    }

    @Test func versions() throws {
        let store = TreeStore()
        #expect(store.apply(.mount(version: 2, root: Node(key: "r", type: "text"))) == .failed(.unsupportedVersion(2)))
        #expect(store.apply(.mount(version: 0, root: Node(key: "r", type: "text"))) == .failed(.unsupportedVersion(0)))
        #expect(!store.isMounted)
        store.apply(.mount(version: 1, root: Node(key: "r", type: "text")))
        #expect(store.isMounted && store.failure == nil)
    }

    @Test func duplicateKeysInAMountFail() {
        let store = TreeStore()
        let e = store.apply(.mount(version: 1, root: Node(key: "r", type: "stack", children: [Node(key: "r", type: "text")])))
        #expect(e == .failed(.patch(.duplicateKey("r"))))
    }

    @Test func metaErrorsAndCalls() throws {
        let store = TreeStore()
        let rec = Recorder()
        let obs = store.observe { rec.events.append($0) }
        store.apply(.meta(NativeMeta(fields: ["title": "Today", "badge": 2])))
        #expect(store.meta.title == "Today" && store.meta.badge == "2")
        #expect(store.apply(.meta(NativeMeta(fields: ["title": "Today"]))) == nil) // unchanged
        store.apply(.meta(NativeMeta(fields: ["badge": .null])))
        #expect(store.meta.fields == ["title": "Today"])

        let err = RuntimeError(kind: "module", message: "boom")
        #expect(store.apply(.error(err)) == .runtimeError(err))
        #expect(store.lastRuntimeError == err)

        let call = BridgeCall(id: 1, what: "copy", args: ["x"])
        #expect(store.apply(.call(call)) == .call(call))
        #expect(store.apply(.unknown(op: "later", body: [:])) == nil)

        store.apply(try counterMount())
        #expect(store.lastRuntimeError == nil) // a mount clears it
        #expect(store.meta.title == "Today")   // meta survives a remount
        #expect(rec.events.count == 5)
        _ = obs
    }

    @Test func receiveBodies() throws {
        let store = TreeStore()
        store.receive(body: try Resources.tree("18.2-calendar").mountMessage.json.jsonString)
        #expect(store.tree["r.1.0:2"]?[prop: "title"] == "lunch with Ana")
        store.receive(body: #"{"op":"patch","ops":[["remove","r.1.0:1"]]}"#)
        #expect(store.tree["r.1"]?.children == ["r.1.0:2"])
        let e = store.receive(body: "not json")
        guard case .failed(.badMessage)? = e else { Issue.record("expected a bad-message failure"); return }
    }

    @Test func appFailureAndReset() throws {
        let store = TreeStore()
        store.apply(try counterMount())
        store.fail(.app("render timeout"))
        #expect(store.failure == .app("render timeout"))
        store.reset()
        #expect(!store.isMounted && store.failure == nil && store.revision == 2 && store.meta.fields.isEmpty)
    }

    /// A long transcript streaming in: each patch appends a message and
    /// updates the previous one. Quadratic behaviour (a child array copied
    /// per insert) would take minutes here; linear takes well under a second.
    @Test func longListsStayLinear() throws {
        let store = TreeStore()
        store.apply(.mount(version: 1, root: Node(key: "r", type: "screen", children: [Node(key: "r.0", type: "transcript")])))
        let start = Date()
        for i in 0..<50_000 {
            var ops: [PatchOp] = [.insert(parent: "r.0", index: i, node: Node(key: "r.0.0:\(i)", type: "message", props: ["text": .string("m\(i)")]))]
            if i > 0 { ops.append(.set(key: "r.0.0:\(i - 1)", props: ["streaming": false])) }
            guard case .tree? = store.apply(.patch(ops)) else { Issue.record("patch \(i) failed"); return }
        }
        #expect(store.tree["r.0"]?.children.count == 50_000)
        #expect(Date().timeIntervalSince(start) < 20)
    }

    /// Random trees, random targets, the diff's ops sent as one patch
    /// message: the store ends at the target and its delta names every
    /// change.
    @Test func randomSessions() throws {
        var r = SeededRNG(seed: 0x5E55)
        for round in 0..<100 {
            let store = TreeStore()
            var current = Gen.tree(&r)
            store.apply(.mount(version: 1, root: current))
            for step in 0..<8 {
                let target = Gen.target(from: current, &r)
                let ops = try diffOps(from: store.tree, to: target)
                let before = store.tree
                let wire = BridgeMessage.patch(ops).json.jsonString
                let e = store.receive(body: wire)
                guard case .tree(_, let delta)? = e else {
                    Issue.record("round \(round) step \(step): \(String(describing: e))")
                    return
                }
                #expect(store.tree.root == target)
                try checkDelta(delta, before: before, after: store.tree)
                current = target
            }
        }
    }
}
