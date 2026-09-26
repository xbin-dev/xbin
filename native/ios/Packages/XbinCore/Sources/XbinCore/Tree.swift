import Foundation

/// A native tile's UI tree, stored flat: one ``Tree/Entry`` per key, each
/// holding its children's keys and its parent's key. The key index makes
/// every patch op a dictionary lookup, and lets a renderer identify views by
/// key and re-read only the entries a ``TreeDelta`` names.
///
/// Invariants (checked by ``Tree/validate()``): keys are unique; every
/// child's `parent` points back at the entry listing it; every entry is
/// reachable from the root exactly once.
public struct Tree: Sendable, Equatable {
    /// One node, without its subtree (children are keys).
    public struct Entry: Sendable, Hashable {
        public let key: String
        public internal(set) var type: String
        public internal(set) var props: [String: JSONValue]
        public internal(set) var events: [String]
        /// Wire fields other than `k t p e c`, preserved.
        public internal(set) var extra: [String: JSONValue]
        /// Child keys, in order.
        public internal(set) var children: [String]
        /// The parent's key; nil for the root.
        public internal(set) var parent: String?

        /// `props[name]`.
        public subscript(prop name: String) -> JSONValue? { props[name] }

        /// Whether the tile listens to `event` on this node.
        public func listens(to event: String) -> Bool { events.contains(event) }
    }

    /// The root's key; nil before the first mount.
    public private(set) var rootKey: String?
    var entries: [String: Entry] = [:]

    /// An empty tree (nothing mounted).
    public init() {}

    /// A tree holding `root`. Throws ``PatchError/duplicateKey(_:)`` when a
    /// key appears twice, ``PatchError/tooDeep(_:)`` past ``Tree/maxDepth``.
    public init(root: Node) throws {
        if Self.height(root) > Self.maxDepth { throw PatchError.tooDeep(root.key) }
        var scratch = TreeDelta()
        try add(root, parent: nil, into: &scratch)
        rootKey = root.key
    }

    public var isEmpty: Bool { rootKey == nil }

    /// The number of nodes.
    public var count: Int { entries.count }

    /// The entry for `key`.
    public subscript(key: String) -> Entry? { entries[key] }

    public func contains(_ key: String) -> Bool { entries[key] != nil }

    /// The root entry.
    public var rootEntry: Entry? { rootKey.flatMap { entries[$0] } }

    /// The root as a nested ``Node`` (materialized — O(n)).
    public var root: Node? { rootKey.flatMap(node) }

    /// The subtree at `key` as a nested ``Node`` (materialized).
    public func node(_ key: String) -> Node? {
        guard let e = entries[key] else { return nil }
        return Node(key: e.key, type: e.type, props: e.props, events: e.events,
                    children: e.children.compactMap(node), extra: e.extra)
    }

    /// `key`'s position among its parent's children; nil for the root or an
    /// unknown key.
    public func index(of key: String) -> Int? {
        guard let p = entries[key]?.parent else { return nil }
        return entries[p]?.children.firstIndex(of: key)
    }

    /// The keys from `key`'s parent up to the root.
    public func ancestors(of key: String) -> [String] {
        var out: [String] = []
        var cur = entries[key]?.parent
        while let c = cur {
            out.append(c)
            cur = entries[c]?.parent
        }
        return out
    }

    /// Whether `key` is `ancestor` or lies inside its subtree.
    public func isWithin(_ key: String, subtreeOf ancestor: String) -> Bool {
        var cur: String? = key
        while let c = cur {
            if c == ancestor { return true }
            cur = entries[c]?.parent
        }
        return false
    }

    /// Every key, in document (pre-)order.
    public var keysInOrder: [String] {
        guard let r = rootKey else { return [] }
        var out: [String] = []
        out.reserveCapacity(entries.count)
        var stack = [r]
        while let k = stack.popLast() {
            out.append(k)
            if let e = entries[k] { stack.append(contentsOf: e.children.reversed()) }
        }
        return out
    }

    /// Checks the invariants; returns a description of the first violation.
    /// For tests and debug assertions — a tree built through this API always
    /// passes.
    public func validate() -> String? {
        guard let r = rootKey else { return entries.isEmpty ? nil : "entries without a root" }
        guard let re = entries[r] else { return "root \(r) missing" }
        if re.parent != nil { return "root has a parent" }
        var seen = Set<String>()
        var stack = [r]
        while let k = stack.popLast() {
            guard seen.insert(k).inserted else { return "\(k) reachable twice" }
            guard let e = entries[k] else { return "\(k) listed but missing" }
            if e.key != k { return "\(k) stored under the wrong key" }
            for c in e.children {
                guard let ce = entries[c] else { return "child \(c) of \(k) missing" }
                if ce.parent != k { return "child \(c) of \(k) points at \(ce.parent ?? "nil")" }
                stack.append(c)
            }
        }
        if seen.count != entries.count { return "\(entries.count - seen.count) unreachable entries" }
        return nil
    }

    // MARK: - Mutation (used by patch application)

    /// Adds `node`'s subtree under `parent` (not linked into the parent's
    /// children — the caller does that). Checks all keys first, so a
    /// duplicate leaves the tree unchanged.
    mutating func add(_ node: Node, parent: String?, into delta: inout TreeDelta) throws {
        var seen = Set<String>()
        var stack = [node]
        while let n = stack.popLast() {
            if entries[n.key] != nil || !seen.insert(n.key).inserted { throw PatchError.duplicateKey(n.key) }
            stack.append(contentsOf: n.children)
        }
        var pending: [(Node, String?)] = [(node, parent)]
        while let (n, p) = pending.popLast() {
            entries[n.key] = Entry(key: n.key, type: n.type, props: n.props, events: n.events,
                                   extra: n.extra, children: n.children.map(\.key), parent: p)
            delta.noteInserted(n.key)
            for c in n.children { pending.append((c, n.key)) }
        }
    }

    /// Deletes `key`'s subtree from the index (the caller unlinks it from its
    /// parent).
    mutating func drop(_ key: String, into delta: inout TreeDelta) {
        var stack = [key]
        while let k = stack.popLast() {
            guard let e = entries.removeValue(forKey: k) else { continue }
            delta.noteRemoved(k)
            stack.append(contentsOf: e.children)
        }
    }

    mutating func withEntry<R>(_ key: String, _ body: (inout Entry) -> R) -> R? {
        guard entries[key] != nil else { return nil }
        return body(&entries[key]!)
    }
}
