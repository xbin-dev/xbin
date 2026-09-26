import Foundation

/// One node of a native tile's UI tree — the wire object `{k, t, p?, e?, c?}`
/// of plans/native.md §9:
///
/// - `k` → ``key``: a stable key, unique in the tree (see ``NodeKey``);
/// - `t` → ``type``: the primitive (`screen`, `row`, `button`, …);
/// - `p` → ``props``: JSON props (absent = empty);
/// - `e` → ``events``: the events the tile listens to (absent = none);
/// - `c` → ``children`` (absent = none).
///
/// An empty `p`, `e` or `c` means the same as an absent one and is omitted
/// when encoding. Fields this version doesn't know are kept in ``extra`` and
/// written back, so a newer runtime's additions survive a round trip.
public struct Node: Sendable, Hashable {
    public var key: String
    public var type: String
    public var props: [String: JSONValue]
    public var events: [String]
    public var children: [Node]
    /// Wire fields other than `k t p e c`, preserved verbatim.
    public var extra: [String: JSONValue]

    public init(key: String, type: String, props: [String: JSONValue] = [:], events: [String] = [],
                children: [Node] = [], extra: [String: JSONValue] = [:]) {
        self.key = key
        self.type = type
        self.props = props
        self.events = events
        self.children = children
        self.extra = extra
    }

    /// `props[name]`.
    public subscript(prop name: String) -> JSONValue? { props[name] }

    /// Whether the tile listens to `event` on this node (so the renderer
    /// should send it through `xbn.event`).
    public func listens(to event: String) -> Bool { events.contains(event) }

    /// The number of nodes in this subtree, this one included.
    public var subtreeCount: Int { 1 + children.reduce(0) { $0 + $1.subtreeCount } }

    /// Every key in this subtree, in document (pre-)order.
    public var subtreeKeys: [String] {
        var out: [String] = []
        func walk(_ n: Node) { out.append(n.key); n.children.forEach(walk) }
        walk(self)
        return out
    }

    /// The first node in this subtree (pre-order) with `key`.
    public func find(_ key: String) -> Node? {
        if self.key == key { return self }
        for c in children { if let n = c.find(key) { return n } }
        return nil
    }
}

/// A node that doesn't have the §9 shape.
public struct NodeDecodingError: Error, Equatable, Sendable, CustomStringConvertible {
    /// Where in the tree: the keys from the root down (as far as known).
    public var path: [String]
    public var reason: String
    public var description: String {
        path.isEmpty ? "invalid node: \(reason)" : "invalid node at \(path.joined(separator: " > ")): \(reason)"
    }
}

extension Node {
    static let knownFields: Set<String> = ["k", "t", "p", "e", "c"]

    /// Decodes a node from its wire JSON. `k` and `t` must be strings (the
    /// key non-empty); `p` an object, `e` an array of strings, `c` an array
    /// of nodes — each may be absent or `null`.
    public init(json: JSONValue) throws {
        try self.init(json: json, path: [])
    }

    /// Parses and decodes a node from JSON text.
    public init(parsing text: String) throws {
        try self.init(json: JSONValue(parsing: text))
    }

    init(json: JSONValue, path: [String]) throws {
        guard case .object(let o) = json else {
            throw NodeDecodingError(path: path, reason: "expected an object, got \(json.kindName)")
        }
        guard case .string(let k)? = o["k"], !k.isEmpty else {
            throw NodeDecodingError(path: path, reason: "k must be a non-empty string")
        }
        let here = path + [k]
        guard case .string(let t)? = o["t"], !t.isEmpty else {
            throw NodeDecodingError(path: here, reason: "t must be a non-empty string")
        }
        var props: [String: JSONValue] = [:]
        switch o["p"] {
        case nil, .null?: break
        case .object(let p)?: props = p
        case let other?: throw NodeDecodingError(path: here, reason: "p must be an object, got \(other.kindName)")
        }
        var events: [String] = []
        switch o["e"] {
        case nil, .null?: break
        case .array(let a)?:
            for v in a {
                guard case .string(let s) = v else {
                    throw NodeDecodingError(path: here, reason: "e must hold strings, got \(v.kindName)")
                }
                events.append(s)
            }
        case let other?: throw NodeDecodingError(path: here, reason: "e must be an array, got \(other.kindName)")
        }
        var children: [Node] = []
        switch o["c"] {
        case nil, .null?: break
        case .array(let a)?:
            children.reserveCapacity(a.count)
            for v in a { children.append(try Node(json: v, path: here)) }
        case let other?: throw NodeDecodingError(path: here, reason: "c must be an array, got \(other.kindName)")
        }
        var extra: [String: JSONValue] = [:]
        for (name, v) in o where !Self.knownFields.contains(name) { extra[name] = v }
        self.init(key: k, type: t, props: props, events: events, children: children, extra: extra)
    }

    /// The wire JSON: `k`, `t`, and `p`/`e`/`c` only when non-empty, plus
    /// ``extra``.
    public var json: JSONValue {
        var o = extra
        o["k"] = .string(key)
        o["t"] = .string(type)
        if !props.isEmpty { o["p"] = .object(props) }
        if !events.isEmpty { o["e"] = .array(events.map(JSONValue.string)) }
        if !children.isEmpty { o["c"] = .array(children.map(\.json)) }
        return .object(o)
    }
}

extension Node: Codable {
    public init(from decoder: Decoder) throws {
        try self.init(json: JSONValue(from: decoder))
    }

    public func encode(to encoder: Encoder) throws {
        try json.encode(to: encoder)
    }
}

/// The key rules of plans/native.md §9. Keys are opaque to the app — it
/// only needs them unique and stable — but tests and tools build them the
/// way the runtime does:
///
/// - the root is `r`;
/// - a static child is `<parent>.<slot>`;
/// - a keyed child (from `repeat` or `key=`) is `<parent>.<slot>:<key>`;
/// - a hole holding a single-root template gives that root the hole's key;
///   a multi-root one gives its roots `<hole>.<i>`.
///
/// User keys may contain `.` and `:` (URLs, IPs), so a key cannot be split
/// back into its parts — the tree index, not the key text, says who the
/// parent is.
public enum NodeKey {
    public static let root = "r"

    /// `<parent>.<slot>` — a static child, or a multi-root hole's `i`th root.
    public static func child(_ parent: String, _ slot: Int) -> String { "\(parent).\(slot)" }

    /// `<parent>.<slot>:<key>` — a keyed child.
    public static func keyed(_ parent: String, _ slot: Int, _ key: String) -> String { "\(parent).\(slot):\(key)" }
}
