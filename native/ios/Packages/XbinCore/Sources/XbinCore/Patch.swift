import Foundation

/// One patch op (plans/native.md §9; the runtime's contract is
/// native/spec/tree.md, its reference `applyOps` is web/xb/rt-diff.js). On
/// the wire each op is an array; ops apply in order, and an index refers to
/// the parent's children at the time its op applies:
///
/// | Op | Wire | Meaning |
/// |---|---|---|
/// | set | `["set", key, {props}]` | merge props into the node (a `null` value is a value, not a removal) |
/// | unset | `["unset", key, [names]]` (or one name as a string) | remove props; unknown names are ignored |
/// | events | `["events", key, [types]]` | replace the events the tile listens to on the node |
/// | insert | `["insert", parent, index, node]` | insert a subtree as the parent's `index`th child (`0…count`) |
/// | remove | `["remove", key]` | remove a subtree (never the root — mount replaces it) |
/// | move | `["move", key, parent, index]` | move a child **within** `parent` (which must be its parent) to `index`, counted after it left |
///
/// A child whose type changes under the same key arrives as remove + insert.
/// Elements after the ones listed are ignored, so an op can grow
/// additively; an unknown op name is an error (the app falls back to web).
public enum PatchOp: Sendable, Hashable {
    case set(key: String, props: [String: JSONValue])
    case unset(key: String, props: [String])
    case events(key: String, events: [String])
    case insert(parent: String, index: Int, node: Node)
    case remove(key: String)
    case move(key: String, parent: String, index: Int)

    /// The wire name (`set`, `unset`, …).
    public var name: String {
        switch self {
        case .set: return "set"
        case .unset: return "unset"
        case .events: return "events"
        case .insert: return "insert"
        case .remove: return "remove"
        case .move: return "move"
        }
    }

    /// The node key the op acts on (for `insert`, the parent).
    public var target: String {
        switch self {
        case .set(let k, _), .unset(let k, _), .events(let k, _), .remove(let k), .move(let k, _, _): return k
        case .insert(let p, _, _): return p
        }
    }
}

/// Why a patch (or a mount) could not be applied. Any of these means the
/// runtime and the app disagree about the tree: the app drops the native
/// view and falls back to the web tile (plans/native.md §7.6).
public enum PatchError: Error, Equatable, Sendable, CustomStringConvertible {
    /// The op isn't shaped like any §9 op.
    case malformed(String)
    /// An op name this version doesn't know.
    case unknownOp(String)
    /// The key (or parent) isn't in the tree.
    case unknownKey(String)
    /// An inserted subtree reuses a key already in the tree (or twice).
    case duplicateKey(String)
    /// The index is outside `0…count` for the parent.
    case indexOutOfRange(key: String, index: Int, count: Int)
    /// Removing or moving the root.
    case rootOp(String)
    /// A move naming a parent that isn't the node's parent (moves stay
    /// within a parent).
    case notAChild(key: String, parent: String)
    /// The tree would nest deeper than ``Tree/maxDepth``.
    case tooDeep(String)
    /// A patch arrived before any mount.
    case notMounted

    public var description: String {
        switch self {
        case .malformed(let s): return "malformed op: \(s)"
        case .unknownOp(let s): return "unknown op \(s)"
        case .unknownKey(let k): return "unknown key \(k)"
        case .duplicateKey(let k): return "duplicate key \(k)"
        case .indexOutOfRange(let k, let i, let n): return "index \(i) out of range 0…\(n) in \(k)"
        case .rootOp(let s): return "\(s) of the root"
        case .notAChild(let k, let p): return "\(k) is not a child of \(p)"
        case .tooDeep(let k): return "tree deeper than \(Tree.maxDepth) at \(k)"
        case .notMounted: return "patch before mount"
        }
    }
}

// MARK: - Wire codec

extension PatchOp {
    /// Decodes one op from its wire array.
    public init(json: JSONValue) throws {
        guard case .array(let a) = json, case .string(let op)? = a.first else {
            throw PatchError.malformed("expected [name, …], got \(json.kindName)")
        }
        func str(_ i: Int, _ what: String) throws -> String {
            guard i < a.count, case .string(let s) = a[i] else { throw PatchError.malformed("\(op): \(what) must be a string") }
            return s
        }
        func idx(_ i: Int) throws -> Int {
            guard i < a.count, let n = a[i].intValue, let v = Int(exactly: n) else {
                throw PatchError.malformed("\(op): index must be an integer")
            }
            return v
        }
        switch op {
        case "set":
            let k = try str(1, "key")
            guard a.count > 2, case .object(let p) = a[2] else { throw PatchError.malformed("set: props must be an object") }
            self = .set(key: k, props: p)
        case "unset":
            let k = try str(1, "key")
            guard a.count > 2 else { throw PatchError.malformed("unset: names missing") }
            switch a[2] {
            case .string(let s): self = .unset(key: k, props: [s])
            case .array(let names):
                var out: [String] = []
                for n in names {
                    guard case .string(let s) = n else { throw PatchError.malformed("unset: names must be strings") }
                    out.append(s)
                }
                self = .unset(key: k, props: out)
            default: throw PatchError.malformed("unset: names must be an array")
            }
        case "events":
            let k = try str(1, "key")
            guard a.count > 2, case .array(let types) = a[2] else { throw PatchError.malformed("events: types must be an array") }
            var out: [String] = []
            for t in types {
                guard case .string(let s) = t else { throw PatchError.malformed("events: types must be strings") }
                out.append(s)
            }
            self = .events(key: k, events: out)
        case "insert":
            let p = try str(1, "parent")
            let i = try idx(2)
            guard a.count > 3 else { throw PatchError.malformed("insert: node missing") }
            do { self = .insert(parent: p, index: i, node: try Node(json: a[3])) } catch let e as NodeDecodingError {
                throw PatchError.malformed("insert: \(e)")
            }
        case "remove":
            self = .remove(key: try str(1, "key"))
        case "move":
            self = .move(key: try str(1, "key"), parent: try str(2, "parent"), index: try idx(3))
        default:
            throw PatchError.unknownOp(op)
        }
    }

    /// The wire array.
    public var json: JSONValue {
        switch self {
        case .set(let k, let p): return ["set", .string(k), .object(p)]
        case .unset(let k, let names): return ["unset", .string(k), .array(names.map(JSONValue.string))]
        case .events(let k, let types): return ["events", .string(k), .array(types.map(JSONValue.string))]
        case .insert(let p, let i, let n): return ["insert", .string(p), .int(Int64(i)), n.json]
        case .remove(let k): return ["remove", .string(k)]
        case .move(let k, let p, let i): return ["move", .string(k), .string(p), .int(Int64(i))]
        }
    }

    /// Decodes a list of ops (a patch message's `ops`).
    public static func list(json: JSONValue) throws -> [PatchOp] {
        guard case .array(let a) = json else { throw PatchError.malformed("ops must be an array") }
        return try a.map(PatchOp.init(json:))
    }
}

// MARK: - The delta a renderer consumes

/// What one mount or patch changed, so a renderer re-reads only those
/// entries. Apply it in this order: drop views for ``removed``, build views
/// for ``inserted``, refresh ``updated``, re-list ``childrenChanged``.
///
/// - ``inserted``: keys created by this change that are in the tree now.
/// - ``removed``: keys that were in the tree before and were removed — a
///   key removed and re-inserted in one patch (possibly as another type) is
///   in both sets: rebuild it.
/// - ``updated``: surviving keys (not in ``inserted``) whose props or
///   events actually changed (a `set` to the same value is not a change).
/// - ``childrenChanged``: surviving keys (not in ``inserted``) whose child
///   list changed (insert, remove, move in or out, reorder).
/// - ``restated``: per surviving key (not in ``inserted``), the props a
///   `set` or `unset` op named — **whether or not the value changed**. A
///   renderer that shows its own value for a controlled prop (native/spec/
///   tree.md §6: a typed text, a flipped toggle) takes the tile's value again
///   for these: a `set` equal to what the tree already held is how the tile
///   refuses a change or resets a field it never saw typed into.
public struct TreeDelta: Sendable, Equatable {
    /// The whole tree was replaced (a mount): re-read everything.
    public var remounted = false
    public var inserted: Set<String> = []
    public var removed: Set<String> = []
    public var updated: Set<String> = []
    public var childrenChanged: Set<String> = []
    /// Key → prop names a `set`/`unset` named (see the type's doc). Not a
    /// change by itself: ``isEmpty`` ignores it.
    public var restated: [String: Set<String>] = [:]

    public init() {}

    public var isEmpty: Bool {
        !remounted && inserted.isEmpty && removed.isEmpty && updated.isEmpty && childrenChanged.isEmpty
    }

    /// Keys born in this change (so a later removal of one is not a
    /// removal of anything the renderer has seen).
    var born: Set<String> = []

    mutating func noteInserted(_ k: String) {
        born.insert(k)
        inserted.insert(k)
    }

    mutating func noteRemoved(_ k: String) {
        inserted.remove(k)
        if born.remove(k) == nil { removed.insert(k) } // it existed before this change
    }

    /// Drops keys that don't need reporting once the change is complete.
    mutating func finish(in tree: Tree) {
        updated = updated.filter { tree.contains($0) && !inserted.contains($0) }
        childrenChanged = childrenChanged.filter { tree.contains($0) && !inserted.contains($0) }
        restated = restated.filter { tree.contains($0.key) && !inserted.contains($0.key) }
        born = []
    }

    public static func == (a: TreeDelta, b: TreeDelta) -> Bool {
        a.remounted == b.remounted && a.inserted == b.inserted && a.removed == b.removed
            && a.updated == b.updated && a.childrenChanged == b.childrenChanged && a.restated == b.restated
    }
}

// MARK: - Application

extension Tree {
    /// The deepest a tree may nest (the root is depth 1). Deeper trees are
    /// refused rather than risking the renderer's stack.
    public static let maxDepth = 256

    /// Replaces the tree with `root`. The delta is ``TreeDelta/remounted``,
    /// with every old key in ``TreeDelta/removed`` and every new one in
    /// ``TreeDelta/inserted``.
    @discardableResult
    public mutating func mount(_ root: Node) throws -> TreeDelta {
        let fresh = try Tree(root: root)
        var d = TreeDelta()
        d.remounted = true
        d.removed = Set(entries.keys)
        d.inserted = Set(fresh.entries.keys)
        self = fresh
        return d
    }

    /// Applies `ops` in order and reports what changed. Each op is checked
    /// before it mutates anything, so the tree is always consistent; when an
    /// op fails, the ops before it stay applied and the error is thrown (the
    /// caller treats that as fatal for the native view).
    @discardableResult
    public mutating func apply(_ ops: [PatchOp]) throws -> TreeDelta {
        var d = TreeDelta()
        for op in ops { try apply(op, into: &d) }
        d.finish(in: self)
        return d
    }

    /// Applies one op.
    @discardableResult
    public mutating func apply(_ op: PatchOp) throws -> TreeDelta {
        try apply([op])
    }

    mutating func apply(_ op: PatchOp, into d: inout TreeDelta) throws {
        guard rootKey != nil else { throw PatchError.notMounted }
        switch op {
        case .set(let k, let props):
            guard entries[k] != nil else { throw PatchError.unknownKey(k) }
            d.restated[k, default: []].formUnion(props.keys)
            var changed = false
            withEntry(k) { e in
                for (name, v) in props where e.props[name] != v {
                    e.props[name] = v
                    changed = true
                }
            }
            if changed { d.updated.insert(k) }

        case .unset(let k, let names):
            guard entries[k] != nil else { throw PatchError.unknownKey(k) }
            d.restated[k, default: []].formUnion(names)
            var changed = false
            withEntry(k) { e in
                for name in names where e.props.removeValue(forKey: name) != nil { changed = true }
            }
            if changed { d.updated.insert(k) }

        case .events(let k, let types):
            guard entries[k] != nil else { throw PatchError.unknownKey(k) }
            var changed = false
            withEntry(k) { e in
                if e.events != types {
                    e.events = types
                    changed = true
                }
            }
            if changed { d.updated.insert(k) }

        case .insert(let p, let i, let node):
            // Read counts, never hold an Entry copy across a mutation: the
            // copy would share the child array and force a full copy (COW).
            guard let count = entries[p]?.children.count else { throw PatchError.unknownKey(p) }
            guard i >= 0, i <= count else { throw PatchError.indexOutOfRange(key: p, index: i, count: count) }
            if depth(of: p) + Self.height(node) > Self.maxDepth { throw PatchError.tooDeep(node.key) }
            try add(node, parent: p, into: &d)
            withEntry(p) { $0.children.insert(node.key, at: i) }
            d.childrenChanged.insert(p)

        case .remove(let k):
            guard entries[k] != nil else { throw PatchError.unknownKey(k) }
            guard let p = entries[k]?.parent else { throw PatchError.rootOp("remove") }
            unlink(k, from: p)
            drop(k, into: &d)
            d.childrenChanged.insert(p)

        case .move(let k, let p, let i):
            guard entries[k] != nil else { throw PatchError.unknownKey(k) }
            guard let parent = entries[k]?.parent else { throw PatchError.rootOp("move") }
            guard parent == p else { throw PatchError.notAChild(key: k, parent: p) }
            let count = (entries[p]?.children.count ?? 1) - 1
            guard i >= 0, i <= count else { throw PatchError.indexOutOfRange(key: p, index: i, count: count) }
            if index(of: k) == i { return } // already there
            unlink(k, from: p)
            withEntry(p) { $0.children.insert(k, at: i) }
            d.childrenChanged.insert(p)
        }
    }

    private mutating func unlink(_ k: String, from p: String) {
        withEntry(p) { pe in
            if let at = pe.children.lastIndex(of: k) { pe.children.remove(at: at) }
        }
    }

    /// 1 for the root, 2 for its children, …
    func depth(of key: String) -> Int { ancestors(of: key).count + 1 }

    static func height(_ n: Node) -> Int {
        var best = 0
        var stack = [(n, 1)]
        while let (cur, h) = stack.popLast() {
            best = max(best, h)
            for c in cur.children { stack.append((c, h + 1)) }
        }
        return best
    }
}
