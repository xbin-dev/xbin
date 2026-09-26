import Foundation
import Testing
@testable import XbinCore

// Test support for the tree, patches, the store and the rest: resources
// (the design trees, the device-login vector), random trees, a reference
// model and a keyed diff.

// MARK: - Resources

enum Resources {
    static var root: URL {
        get throws { try #require(Bundle.module.url(forResource: "Resources", withExtension: nil)) }
    }

    static func data(_ path: String) throws -> Data {
        try Data(contentsOf: root.appendingPathComponent(path))
    }

    static func json(_ path: String) throws -> JSONValue {
        try JSONValue(parsing: data(path))
    }

    /// The §18 trees of plans/native.md, copied verbatim.
    static let trees = [
        "18.1-counter-go", "18.2-calendar", "18.3-egress-approver", "18.4-s3-archiver",
        "18.5-webhooks", "18.6-devbox", "18.7-prometheus-viewer", "18.8-chat",
    ]

    static func tree(_ name: String) throws -> FixtureTree {
        try FixtureTree(data: data("trees/\(name).json"))
    }
}

extension Gen {
    static let types = ["screen", "section", "row", "button", "text", "field", "stack", "list", "toolbar", "badge"]
    static let events = ["tap", "input", "change", "submit"]
    static let propNames = ["title", "detail", "label", "value", "tone", "disabled", "busy", "mono"]

    static func props(_ r: inout SeededRNG) -> [String: JSONValue] {
        var p: [String: JSONValue] = [:]
        for _ in 0..<Int.random(in: 0...3, using: &r) {
            p[propNames.randomElement(using: &r)!] = value(&r, depth: 2)
        }
        return p
    }

    static func eventList(_ r: inout SeededRNG) -> [String] {
        events.filter { _ in Int.random(in: 0..<4, using: &r) == 0 }
    }

    /// A random tree whose keys follow the §9 rules.
    static func tree(_ r: inout SeededRNG, maxNodes: Int = 40) -> Node {
        var budget = Int.random(in: 1...maxNodes, using: &r)
        func make(_ key: String, depth: Int) -> Node {
            budget -= 1
            var n = Node(key: key, type: types.randomElement(using: &r)!, props: props(&r), events: eventList(&r))
            let want = depth > 5 ? 0 : Int.random(in: 0...4, using: &r)
            for slot in 0..<want where budget > 0 {
                let k = Bool.random(using: &r) ? NodeKey.child(key, slot)
                    : NodeKey.keyed(key, slot, "\(Int.random(in: 0...999, using: &r)).\(slot)")
                if n.find(k) == nil { n.children.append(make(k, depth: depth + 1)) }
            }
            return n
        }
        return make(NodeKey.root, depth: 0)
    }

    /// A random target for `from`: keeps the root, reuses a random subset of
    /// the other keys (mostly with the same type, sometimes another), adds
    /// new ones, and arranges them into a new random shape.
    static func target(from a: Node, _ r: inout SeededRNG) -> Node {
        var pool: [Node] = []
        func collect(_ n: Node) { for c in n.children { pool.append(c); collect(c) } }
        collect(a)
        var nodes: [Node] = []
        for old in pool where Int.random(in: 0..<10, using: &r) < 7 {
            var n = Node(key: old.key, type: old.type, props: old.props, events: old.events)
            if Int.random(in: 0..<10, using: &r) == 0 { n.type = types.randomElement(using: &r)! }
            if Int.random(in: 0..<10, using: &r) == 0 { n.events = eventList(&r) }
            if Bool.random(using: &r) {
                for _ in 0..<Int.random(in: 1...2, using: &r) {
                    let name = propNames.randomElement(using: &r)!
                    if Bool.random(using: &r) { n.props[name] = value(&r, depth: 2) } else { n.props.removeValue(forKey: name) }
                }
            }
            nodes.append(n)
        }
        for i in 0..<Int.random(in: 0...6, using: &r) {
            nodes.append(Node(key: "n\(i):\(Int.random(in: 0...99999, using: &r))", type: types.randomElement(using: &r)!,
                              props: props(&r), events: eventList(&r)))
        }
        nodes.shuffle(using: &r)
        // Place each node under the root or a node placed before it.
        var root = Node(key: a.key, type: a.type, props: a.props, events: a.events)
        if Bool.random(using: &r) { root.props = props(&r) }
        var placed: [String] = [root.key]
        var parentOf: [String: String] = [:]
        var byKey: [String: Node] = [:]
        for n in nodes {
            let p = placed.randomElement(using: &r)!
            parentOf[n.key] = p
            byKey[n.key] = n
            placed.append(n.key)
        }
        var childrenOf: [String: [String]] = [:]
        for n in nodes { childrenOf[parentOf[n.key]!, default: []].append(n.key) }
        func build(_ n: Node) -> Node {
            var out = n
            out.children = (childrenOf[n.key] ?? []).map { build(byKey[$0]!) }
            return out
        }
        return build(root)
    }
}

// MARK: - Wire normalization

/// Drops empty `p`/`e`/`c` from a wire tree, recursively — an empty one means
/// the same as an absent one (Node omits them when encoding).
func normalizeWire(_ v: JSONValue) -> JSONValue {
    guard case .object(var o) = v else { return v }
    if case .object(let p)? = o["p"], p.isEmpty { o["p"] = nil }
    if case .array(let e)? = o["e"], e.isEmpty { o["e"] = nil }
    if case .array(let c)? = o["c"] {
        o["c"] = c.isEmpty ? nil : .array(c.map(normalizeWire))
    }
    if let root = o["root"] { o["root"] = normalizeWire(root) }
    return .object(o)
}

// MARK: - A reference model: the same op semantics on a nested Node

/// An independent, deliberately naive implementation of the patch ops on a
/// nested `Node` (the semantics of the runtime's reference `applyOps`, plus
/// the app's strictness: unknown keys, bad indexes, duplicate keys and
/// cross-parent moves are refused), for differential tests against `Tree`.
struct ReferenceTree {
    var root: Node

    func path(to key: String) -> [Int]? {
        func search(_ n: Node, _ p: [Int]) -> [Int]? {
            if n.key == key { return p }
            for (i, c) in n.children.enumerated() { if let r = search(c, p + [i]) { return r } }
            return nil
        }
        return search(root, [])
    }

    func node(at path: [Int]) -> Node {
        path.reduce(root) { $0.children[$1] }
    }

    mutating func modify(at path: [Int], _ body: (inout Node) -> Void) {
        func go(_ n: inout Node, _ rest: ArraySlice<Int>) {
            if let i = rest.first { go(&n.children[i], rest.dropFirst()) } else { body(&n) }
        }
        go(&root, path[...])
    }

    var allKeys: Set<String> { Set(root.subtreeKeys) }

    func depth(_ n: Node) -> Int { 1 + (n.children.map(depth).max() ?? 0) }

    /// Applies one op; throws on anything `Tree` refuses.
    mutating func apply(_ op: PatchOp) throws {
        struct Refused: Error {}
        switch op {
        case .set(let k, let props):
            guard let p = path(to: k) else { throw Refused() }
            modify(at: p) { n in for (name, v) in props { n.props[name] = v } }
        case .unset(let k, let names):
            guard let p = path(to: k) else { throw Refused() }
            modify(at: p) { n in for name in names { n.props.removeValue(forKey: name) } }
        case .events(let k, let types):
            guard let p = path(to: k) else { throw Refused() }
            modify(at: p) { $0.events = types }
        case .insert(let parent, let i, let node):
            guard let p = path(to: parent) else { throw Refused() }
            let count = self.node(at: p).children.count
            guard i >= 0, i <= count else { throw Refused() }
            let newKeys = node.subtreeKeys
            guard Set(newKeys).count == newKeys.count, allKeys.isDisjoint(with: newKeys) else { throw Refused() }
            guard p.count + 1 + depth(node) <= Tree.maxDepth else { throw Refused() }
            modify(at: p) { $0.children.insert(node, at: i) }
        case .remove(let k):
            guard let p = path(to: k), !p.isEmpty else { throw Refused() }
            modify(at: Array(p.dropLast())) { $0.children.remove(at: p.last!) }
        case .move(let k, let parent, let i):
            guard let kp = path(to: k), !kp.isEmpty else { throw Refused() }
            let pp = Array(kp.dropLast())
            guard node(at: pp).key == parent else { throw Refused() } // within its parent only
            let count = node(at: pp).children.count - 1
            guard i >= 0, i <= count else { throw Refused() }
            modify(at: pp) { n in
                let moving = n.children.remove(at: kp.last!)
                n.children.insert(moving, at: i)
            }
        }
    }
}

// MARK: - A keyed diff (test-only): the ops that turn a tree into a target

/// The patch ops that turn `current` into `target` (same root key and
/// type), in the style the runtime emits: set/unset/events on kept nodes,
/// kept children moved into place within their parent, missing ones
/// inserted, leftovers removed; a key whose type changed, or that sits under
/// another parent, is removed and inserted afresh.
func diffOps(from current: Tree, to target: Node) throws -> [PatchOp] {
    var t = current
    var ops: [PatchOp] = []
    func emit(_ op: PatchOp) throws {
        try t.apply(op)
        ops.append(op)
    }
    func syncNode(_ n: Node) throws {
        let e = t[n.key]!
        var set: [String: JSONValue] = [:]
        for (k, v) in n.props where e.props[k] != v { set[k] = v }
        let unset = e.props.keys.filter { n.props[$0] == nil }.sorted()
        if !set.isEmpty { try emit(.set(key: n.key, props: set)) }
        if !unset.isEmpty { try emit(.unset(key: n.key, props: unset)) }
        if e.events != n.events { try emit(.events(key: n.key, events: n.events)) }
    }
    func sync(_ n: Node) throws {
        try syncNode(n)
        for (i, c) in n.children.enumerated() {
            if let e = t[c.key], e.type != c.type || e.extra != c.extra || e.parent != n.key {
                try emit(.remove(key: c.key))
            }
            if t[c.key] != nil {
                if t.index(of: c.key) != i { try emit(.move(key: c.key, parent: n.key, index: i)) }
            } else {
                var shell = c
                shell.children = []
                try emit(.insert(parent: n.key, index: i, node: shell))
            }
            try sync(c)
        }
    }
    try sync(target)
    let keep = Set(target.subtreeKeys)
    for k in t.keysInOrder where !keep.contains(k) && t.contains(k) { try emit(.remove(key: k)) }
    return ops
}
