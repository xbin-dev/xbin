import Foundation
import Testing
@testable import XbinCore

@Suite struct PatchTests {
    /// r(screen) > [r.0(section) > [r.0.0(row), r.0.1(button)], r.1(section) > [r.1.0:a, r.1.0:b]]
    func sample() throws -> Tree {
        try Tree(root: Node(key: "r", type: "screen", props: ["title": "T"], children: [
            Node(key: "r.0", type: "section", children: [
                Node(key: "r.0.0", type: "row", props: ["title": "Count", "detail": "42"]),
                Node(key: "r.0.1", type: "button", props: ["label": "+1", "busy": false], events: ["tap"]),
            ]),
            Node(key: "r.1", type: "section", children: [
                Node(key: "r.1.0:a", type: "row", props: ["title": "a"]),
                Node(key: "r.1.0:b", type: "row", props: ["title": "b"]),
            ]),
        ]))
    }

    @Test func setMerges() throws {
        var t = try sample()
        let d = try t.apply(.set(key: "r.0.0", props: ["detail": "43", "tone": "ok", "x": .null]))
        #expect(t["r.0.0"]?.props == ["title": "Count", "detail": "43", "tone": "ok", "x": .null])
        #expect(d.propsChanged == ["r.0.0"] && d.childrenChanged.isEmpty && d.inserted.isEmpty && d.removed.isEmpty)
        // Setting the same values is not a change.
        #expect(try t.apply(.set(key: "r.0.0", props: ["detail": "43"])).isEmpty)
        // A bool is not a number: false → 0 is a change.
        #expect(try t.apply(.set(key: "r.0.1", props: ["busy": 0])).propsChanged == ["r.0.1"])
    }

    @Test func unsetRemoves() throws {
        var t = try sample()
        let d = try t.apply(.unset(key: "r.0.0", props: ["detail", "nope"]))
        #expect(t["r.0.0"]?.props == ["title": "Count"])
        #expect(d.propsChanged == ["r.0.0"])
        #expect(try t.apply(.unset(key: "r.0.0", props: ["nope"])).isEmpty)
    }

    @Test func insertAtIndex() throws {
        var t = try sample()
        let n = Node(key: "r.0.2", type: "notice", props: ["tone": "ok", "text": "saved"],
                     children: [Node(key: "r.0.2.0", type: "text")])
        let d = try t.apply(.insert(parent: "r.0", index: 1, node: n))
        #expect(t["r.0"]?.children == ["r.0.0", "r.0.2", "r.0.1"])
        #expect(t["r.0.2.0"]?.parent == "r.0.2")
        #expect(d.inserted == ["r.0.2", "r.0.2.0"] && d.childrenChanged == ["r.0"] && d.propsChanged.isEmpty)
        #expect(t.validate() == nil)
        try t.apply(.insert(parent: "r.0", index: 3, node: Node(key: "end", type: "text")))
        #expect(t["r.0"]?.children.last == "end")
        try t.apply(.insert(parent: "r.0.0", index: 0, node: Node(key: "leaf-child", type: "text")))
        #expect(t["r.0.0"]?.children == ["leaf-child"])
    }

    @Test func insertRefusals() throws {
        var t = try sample()
        let before = t
        #expect(throws: PatchError.indexOutOfRange(key: "r.0", index: 3, count: 2)) {
            try t.apply(.insert(parent: "r.0", index: 3, node: Node(key: "x", type: "text")))
        }
        #expect(throws: PatchError.indexOutOfRange(key: "r.0", index: -1, count: 2)) {
            try t.apply(.insert(parent: "r.0", index: -1, node: Node(key: "x", type: "text")))
        }
        #expect(throws: PatchError.unknownKey("nope")) {
            try t.apply(.insert(parent: "nope", index: 0, node: Node(key: "x", type: "text")))
        }
        #expect(throws: PatchError.duplicateKey("r.1.0:a")) {
            try t.apply(.insert(parent: "r.0", index: 0, node: Node(key: "x", type: "stack", children: [Node(key: "r.1.0:a", type: "text")])))
        }
        #expect(throws: PatchError.duplicateKey("y")) {
            try t.apply(.insert(parent: "r.0", index: 0, node: Node(key: "y", type: "stack", children: [Node(key: "y", type: "text")])))
        }
        #expect(t == before) // a refused op changes nothing
    }

    @Test func removeSubtree() throws {
        var t = try sample()
        let d = try t.apply(.remove(key: "r.1"))
        #expect(t["r"]?.children == ["r.0"])
        #expect(!t.contains("r.1") && !t.contains("r.1.0:a") && !t.contains("r.1.0:b"))
        #expect(t.count == 4)
        #expect(d.removed == ["r.1", "r.1.0:a", "r.1.0:b"] && d.childrenChanged == ["r"])
        #expect(throws: PatchError.rootOp("remove")) { try t.apply(.remove(key: "r")) }
        #expect(throws: PatchError.unknownKey("r.1")) { try t.apply(.remove(key: "r.1")) }
    }

    @Test func moveWithinAParent() throws {
        var t = try sample()
        // The index counts after detaching: moving the first of two to 1 makes it last.
        var d = try t.apply(.move(key: "r.1.0:a", parent: "r.1", index: 1))
        #expect(t["r.1"]?.children == ["r.1.0:b", "r.1.0:a"])
        #expect(d.childrenChanged == ["r.1"] && d.inserted.isEmpty && d.removed.isEmpty)
        d = try t.apply(.move(key: "r.1.0:a", parent: "r.1", index: 1))
        #expect(d.isEmpty) // already there
        #expect(throws: PatchError.indexOutOfRange(key: "r.1", index: 2, count: 1)) {
            try t.apply(.move(key: "r.1.0:a", parent: "r.1", index: 2))
        }
    }

    @Test func moveAcrossParents() throws {
        var t = try sample()
        let d = try t.apply(.move(key: "r.1.0:b", parent: "r.0", index: 0))
        #expect(t["r.0"]?.children == ["r.1.0:b", "r.0.0", "r.0.1"])
        #expect(t["r.1"]?.children == ["r.1.0:a"])
        #expect(t["r.1.0:b"]?.parent == "r.0")
        #expect(d.childrenChanged == ["r.0", "r.1"])
        #expect(t.validate() == nil)
        try t.apply(.move(key: "r.1", parent: "r.0.0", index: 0)) // a subtree moves along
        #expect(t.ancestors(of: "r.1.0:a") == ["r.1", "r.0.0", "r.0", "r"])
        #expect(t.validate() == nil)
    }

    @Test func moveRefusals() throws {
        var t = try sample()
        #expect(throws: PatchError.cycle(key: "r.0", parent: "r.0.0")) { try t.apply(.move(key: "r.0", parent: "r.0.0", index: 0)) }
        #expect(throws: PatchError.cycle(key: "r.0", parent: "r.0")) { try t.apply(.move(key: "r.0", parent: "r.0", index: 0)) }
        #expect(throws: PatchError.rootOp("move")) { try t.apply(.move(key: "r", parent: "r.0", index: 0)) }
        #expect(throws: PatchError.unknownKey("x")) { try t.apply(.move(key: "x", parent: "r", index: 0)) }
        #expect(throws: PatchError.unknownKey("x")) { try t.apply(.move(key: "r.0", parent: "x", index: 0)) }
        #expect(throws: PatchError.indexOutOfRange(key: "r.1", index: 3, count: 2)) {
            try t.apply(.move(key: "r.0", parent: "r.1", index: 3))
        }
    }

    @Test func notMounted() {
        var t = Tree()
        #expect(throws: PatchError.notMounted) { try t.apply(.set(key: "r", props: [:])) }
    }

    @Test func depthLimit() throws {
        /// prefix0 > prefix1 > … > prefix(n-1)
        func chain(_ prefix: String, _ n: Int) -> Node {
            var c = Node(key: "\(prefix)\(n - 1)", type: "stack")
            for i in stride(from: n - 2, through: 0, by: -1) { c = Node(key: "\(prefix)\(i)", type: "stack", children: [c]) }
            return c
        }
        let full = chain("d", Tree.maxDepth)
        #expect(full.subtreeCount == Tree.maxDepth)
        var t = try Tree(root: full)
        #expect(throws: PatchError.tooDeep("x")) {
            try t.apply(.insert(parent: "d\(Tree.maxDepth - 1)", index: 0, node: Node(key: "x", type: "text")))
        }
        try t.apply(.insert(parent: "d\(Tree.maxDepth - 2)", index: 0, node: Node(key: "x", type: "text")))
        #expect(throws: PatchError.tooDeep("r")) { try Tree(root: Node(key: "r", type: "stack", children: [full])) }

        var root = chain("d", 200)
        root.children.append(chain("s", 60))
        var t2 = try Tree(root: root)
        #expect(throws: PatchError.tooDeep("s0")) { try t2.apply(.move(key: "s0", parent: "d199", index: 0)) }
        try t2.apply(.move(key: "s0", parent: "d150", index: 0)) // depth 151 + 60
        #expect(t2.validate() == nil)
    }

    @Test func deltaAcrossABatch() throws {
        var t = try sample()
        let d = try t.apply([
            .remove(key: "r.1.0:a"),                                                    // gone for good
            .insert(parent: "r.1", index: 0, node: Node(key: "tmp", type: "text")),
            .set(key: "tmp", props: ["text": "x"]),                                     // on a new node: not a props change
            .remove(key: "tmp"),                                                        // born and gone: reported nowhere
            .remove(key: "r.0.0"),
            .insert(parent: "r.0", index: 0, node: Node(key: "r.0.0", type: "notice")), // replaced: in both sets
            .set(key: "r.0.1", props: ["busy": true]),
            .move(key: "r.1.0:b", parent: "r.0", index: 2),
        ])
        #expect(d.removed == ["r.1.0:a", "r.0.0"])
        #expect(d.inserted == ["r.0.0"])
        #expect(d.propsChanged == ["r.0.1"])
        #expect(d.childrenChanged == ["r.0", "r.1"])
        #expect(!d.remounted)
        #expect(t["r.0.0"]?.type == "notice")
    }

    @Test func mountDelta() throws {
        var t = try sample()
        let d = try t.mount(Node(key: "r", type: "screen", children: [Node(key: "r.0", type: "text")]))
        #expect(d.remounted)
        #expect(d.inserted == ["r", "r.0"])
        #expect(d.removed == Set(try sample().keysInOrder))
    }

    // MARK: - Wire codec

    @Test func designPatchExampleDecodes() throws {
        // plans/native.md §9, verbatim.
        let msg = try BridgeMessage(parsing: #"{"op":"patch","ops":[["set","r.0.0",{"detail":"43"}],["insert","r.0",2,{"k":"r.0.2","t":"notice","p":{"tone":"ok","text":"saved"}}],["remove","r.0.3"],["move","r.1:a",  "r.1",0]]}"#)
        guard case .patch(let ops) = msg else { Issue.record("not a patch"); return }
        #expect(ops == [
            .set(key: "r.0.0", props: ["detail": "43"]),
            .insert(parent: "r.0", index: 2, node: Node(key: "r.0.2", type: "notice", props: ["tone": "ok", "text": "saved"])),
            .remove(key: "r.0.3"),
            .move(key: "r.1:a", parent: "r.1", index: 0),
        ])
    }

    @Test func opWireForms() throws {
        let ops: [PatchOp] = [
            .set(key: "k", props: ["a": 1, "b": [true]]),
            .unset(key: "k", props: ["a", "b"]),
            .insert(parent: "p", index: 3, node: Node(key: "n", type: "row", props: ["t": "x"], events: ["tap"])),
            .remove(key: "k"),
            .move(key: "k", parent: "p", index: 0),
        ]
        for op in ops {
            #expect(try PatchOp(json: op.json) == op)
            #expect(try PatchOp(json: JSONValue(parsing: op.json.jsonString)) == op)
        }
        #expect(try PatchOp.list(json: .array(ops.map(\.json))) == ops)
        #expect(ops.map(\.name) == ["set", "unset", "insert", "remove", "move"])
        #expect(ops.map(\.target) == ["k", "k", "p", "k", "k"])
        // Tolerated forms: one unset name as a string; an integral double index; trailing elements.
        #expect(try PatchOp(json: ["unset", "k", "a"]) == .unset(key: "k", props: ["a"]))
        #expect(try PatchOp(json: ["move", "k", "p", 2.0]) == .move(key: "k", parent: "p", index: 2))
        #expect(try PatchOp(json: ["remove", "k", ["future": true]]) == .remove(key: "k"))
    }

    @Test(arguments: [
        #"{}"#, #"[]"#, #"[1]"#, #"["set"]"#, #"["set","k"]"#, #"["set","k",[]]"#, #"["set",1,{}]"#,
        #"["unset","k"]"#, #"["unset","k",[1]]"#, #"["unset","k",{}]"#, #"["insert","p",0]"#,
        #"["insert","p","0",{"k":"n","t":"x"}]"#, #"["insert","p",0.5,{"k":"n","t":"x"}]"#,
        #"["insert","p",0,{"k":"n"}]"#, #"["remove"]"#, #"["move","k","p"]"#, #"["move","k",1,0]"#,
    ])
    func malformedOps(_ text: String) throws {
        let v = try JSONValue(parsing: text)
        #expect(throws: PatchError.self) { try PatchOp(json: v) }
    }

    @Test func unknownOpIsAnError() {
        #expect(throws: PatchError.unknownOp("replace")) { try PatchOp(json: ["replace", "k", ["k": "k", "t": "x"]]) }
    }

    // MARK: - Randomized

    /// Random op sequences give the same tree as the naive reference model,
    /// accept and refuse the same ops, and keep the index consistent.
    @Test func randomOpsMatchTheReference() throws {
        var r = SeededRNG(seed: 0xC0FFEE)
        for round in 0..<300 {
            let start = Gen.tree(&r, maxNodes: 30)
            var tree = try Tree(root: start)
            var ref = ReferenceTree(root: start)
            for step in 0..<40 {
                let keys = tree.keysInOrder
                let op = randomOp(keys: keys, &r)
                let before = tree
                var refAccepted = true
                do { try ref.apply(op) } catch { refAccepted = false }
                var accepted = true
                do { try tree.apply(op) } catch { accepted = false }
                #expect(accepted == refAccepted, "round \(round) step \(step): \(op)")
                if !accepted { #expect(tree == before, "a refused op must not change the tree") }
                #expect(tree.validate() == nil, "round \(round) step \(step)")
                #expect(tree.root == ref.root, "round \(round) step \(step): \(op)")
                if tree.root != ref.root { return }
            }
        }
    }

    func randomOp(keys: [String], _ r: inout SeededRNG) -> PatchOp {
        func key() -> String { Int.random(in: 0..<20, using: &r) == 0 ? "missing" : keys.randomElement(using: &r)! }
        func index() -> Int { Int.random(in: -1...5, using: &r) }
        switch Int.random(in: 0..<6, using: &r) {
        case 0: return .set(key: key(), props: Gen.props(&r))
        case 1: return .unset(key: key(), props: Gen.propNames.filter { _ in Bool.random(using: &r) })
        case 2, 3:
            var n = Gen.tree(&r, maxNodes: 4)
            // Fresh keys, except sometimes a clash on purpose.
            let prefix = Int.random(in: 0..<8, using: &r) == 0 ? "" : "i\(r.next() % 100_000)"
            func rekey(_ x: inout Node) {
                x.key = prefix.isEmpty ? keys.randomElement(using: &r)! : prefix + x.key
                for i in x.children.indices { rekey(&x.children[i]) }
            }
            rekey(&n)
            return .insert(parent: key(), index: index(), node: n)
        case 4: return .remove(key: key())
        default: return .move(key: key(), parent: key(), index: index())
        }
    }

    /// For random pairs of trees, the keyed diff's ops turn one into the
    /// other — through `Tree` and through the reference model alike.
    @Test func randomDiffsApply() throws {
        var r = SeededRNG(seed: 0xD1FF)
        for round in 0..<400 {
            let a = Gen.tree(&r)
            let b = Gen.target(from: a, &r)
            let ops = try diffOps(from: Tree(root: a), to: b)
            var tree = try Tree(root: a)
            let delta = try tree.apply(ops)
            #expect(tree.root == b, "round \(round)")
            #expect(tree.validate() == nil)
            var ref = ReferenceTree(root: a)
            for op in ops { try ref.apply(op) }
            #expect(ref.root == b, "round \(round)")
            // The ops survive the wire.
            let wire = try PatchOp.list(json: JSONValue(parsing: JSONValue.array(ops.map(\.json)).jsonString))
            #expect(wire == ops)
            try checkDelta(delta, before: Tree(root: a), after: tree)
        }
    }
}

/// The delta names every change a renderer must see (it may name more).
func checkDelta(_ d: TreeDelta, before: Tree, after: Tree, sourceLocation: SourceLocation = #_sourceLocation) throws {
    let b = Set(before.keysInOrder), a = Set(after.keysInOrder)
    #expect(d.removed.isSuperset(of: b.subtracting(a)), sourceLocation: sourceLocation)
    #expect(d.inserted.isSuperset(of: a.subtracting(b)), sourceLocation: sourceLocation)
    #expect(d.inserted.isSubset(of: a) && d.removed.isSubset(of: b), sourceLocation: sourceLocation)
    let replaced = d.removed.intersection(a)
    #expect(replaced == d.inserted.intersection(b), sourceLocation: sourceLocation)
    for k in b.intersection(a).subtracting(replaced) {
        let x = try #require(before[k]), y = try #require(after[k])
        #expect(x.type == y.type && x.events == y.events, "\(k) changed shape without being replaced", sourceLocation: sourceLocation)
        if x.props != y.props { #expect(d.propsChanged.contains(k), "props of \(k)", sourceLocation: sourceLocation) }
        if x.children != y.children { #expect(d.childrenChanged.contains(k), "children of \(k)", sourceLocation: sourceLocation) }
    }
    #expect(d.propsChanged.isDisjoint(with: d.inserted) && d.childrenChanged.isDisjoint(with: d.inserted), sourceLocation: sourceLocation)
    #expect(d.propsChanged.isSubset(of: a) && d.childrenChanged.isSubset(of: a), sourceLocation: sourceLocation)
}
