import Foundation
import Observation
import XbinCore

/// One node of the tree as the renderer shows it: an observable object per
/// key, so a SwiftUI view re-renders when *its* node changes and nothing
/// else. Children are the child node objects, in order.
///
/// ``props`` is what the app shows — the tile's props plus the values the
/// user reported for bound controlled props (a typed text, a flipped
/// toggle), exactly like the reference renderer's copy of the tree
/// (native/spec/tree.md §6). A value the user reported for a prop the tile
/// did *not* bind lives in ``ui`` (renderer-owned state). Read both through
/// ``value(_:)``.
@MainActor
@Observable
public final class XbinNode: Identifiable {
    /// The node's key (`k`), unique in the tree and stable across renders.
    public let key: String
    /// The primitive (`t`).
    public private(set) var type: String
    /// The props the app shows (see the type's doc).
    public private(set) var props: Props
    /// The events the tile listens to (`e`).
    public private(set) var events: [String]
    /// Child nodes, in order.
    public internal(set) var children: [XbinNode] = []
    /// Renderer-owned values of controlled props the tile left unbound.
    public private(set) var ui: [String: JSONValue] = [:]

    /// The props as the runtime last sent them (the store's entry).
    @ObservationIgnored private var tileProps: [String: JSONValue]
    @ObservationIgnored var childKeys: [String]

    /// Identity is the object: a node removed and re-inserted under the
    /// same key (a type change) is a new view with fresh state.
    public nonisolated var id: ObjectIdentifier { ObjectIdentifier(self) }

    /// A detached node (previews and tests): not part of any tree.
    public init(key: String, type: String, props: [String: JSONValue] = [:], events: [String] = [],
                children: [XbinNode] = []) {
        self.key = key
        self.type = type
        self.props = Props(props)
        self.events = events
        self.children = children
        tileProps = props
        childKeys = children.map(\.key)
    }

    convenience init(entry: Tree.Entry) {
        self.init(key: entry.key, type: entry.type, props: entry.props, events: entry.events)
        childKeys = entry.children
    }

    /// Whether the tile listens to `event` here.
    public func listens(to event: String) -> Bool { events.contains(event) }

    /// Whether the tile binds `prop` (it is in the tile's props): a bound
    /// controlled prop is the tile's; an unbound one is the renderer's.
    public func binds(_ prop: String) -> Bool { props.raw[prop] != nil }

    /// A controlled prop's shown value: the tile's (as reported back) when
    /// bound, else the renderer's own, else nil.
    public func value(_ prop: String) -> JSONValue? { props.raw[prop] ?? ui[prop] }

    /// The children of type `type`.
    public func children(of type: String) -> [XbinNode] { children.filter { $0.type == type } }

    // MARK: - Updates

    /// Takes the store's entry after a patch. A prop takes the tile's value
    /// when the tile's value changed or the patch restated it
    /// (``TreeDelta/restated``); otherwise a value the user reported stays.
    func refresh(from e: Tree.Entry, restated: Set<String>) {
        var shown = props.raw
        var changed = false
        for name in Set(tileProps.keys).union(e.props.keys) {
            let new = e.props[name]
            guard new != tileProps[name] || restated.contains(name) else { continue }
            if shown[name] != new {
                shown[name] = new
                changed = true
            }
        }
        tileProps = e.props
        if changed { props = Props(shown) }
        if events != e.events { events = e.events }
        if type != e.type { type = e.type }
        childKeys = e.children
    }

    /// Shows a value the user reported for `prop`: in the props when the
    /// tile binds it, else as renderer state. Returns whether it was bound.
    @discardableResult
    func show(_ prop: String, _ value: JSONValue) -> Bool {
        if props.raw[prop] != nil {
            if props.raw[prop] != value {
                var p = props.raw
                p[prop] = value
                props = Props(p)
            }
            return true
        }
        if ui[prop] != value { ui[prop] = value }
        return false
    }
}

/// The observable tree a SwiftUI renderer draws: a ``TreeStore`` (fed by
/// the tile's runtime) turned into ``XbinNode`` objects, updated from each
/// ``TreeDelta`` in the order it prescribes — drop removed, build inserted,
/// refresh updated/restated, re-list changed children — and the app → tile
/// half: ``emit(_:_:_:)`` applies the user's change to the shown props and
/// sends the event through the app's `send`.
///
/// Feed runtime messages to the store as usual (`store.receive(body:)`);
/// the model observes it. Main actor only, like the store.
@MainActor
@Observable
public final class XbinTreeModel {
    /// The store this model shows.
    public let store: TreeStore
    /// Sends an app → runtime call (the app: `callAsyncJavaScript(call.functionBody…)`
    /// on the tile's runtime WebView).
    @ObservationIgnored public var send: @MainActor (RuntimeCall) -> Void

    /// The root node; nil before the first mount (and after a reset).
    public private(set) var root: XbinNode?
    /// Why the native view gave up (the app shows the web tile), if it did.
    public private(set) var failure: TreeStoreFailure?
    /// The store revision last applied.
    public private(set) var revision = 0

    @ObservationIgnored private var nodes: [String: XbinNode] = [:]
    @ObservationIgnored private var observation: TreeStoreObservation?

    public init(store: TreeStore, send: @escaping @MainActor (RuntimeCall) -> Void = { _ in }) {
        self.store = store
        self.send = send
        rebuild()
        failure = store.failure
        observation = store.observe { [weak self] event in
            MainActor.assumeIsolated { self?.handle(event) }
        }
    }

    /// The node with `key`, if it is in the tree.
    public func node(_ key: String) -> XbinNode? { nodes[key] }

    /// The number of nodes.
    public var count: Int { nodes.count }

    // MARK: - App → tile

    /// The user acted on `key`. When the event reports a controlled prop
    /// (``XbinVocabulary/report(_:_:)``), the new value is shown first —
    /// bound props in the node's props, unbound ones as renderer state.
    /// The event goes to the tile when it listens or when the prop is bound
    /// (the runtime's shadow must stay true, tree.md §6), carrying the tree
    /// sequence the app has applied. Returns whether it was sent.
    @discardableResult
    public func emit(_ key: String, _ type: String, _ payload: JSONValue = [:]) -> Bool {
        guard let node = nodes[key] else { return false }
        var bound = false
        if let report = XbinVocabulary.report(node.type, type), let v = report.value(in: payload) {
            bound = node.show(report.prop, v)
        }
        guard bound || node.listens(to: type) else { return false }
        send(store.event(key, type, payload: payload))
        return true
    }

    /// Sets a renderer-owned value for `prop` on `key` without telling the
    /// tile — only when the tile doesn't bind the prop (an unbound composer
    /// clears itself after `send`, as the reference renderer's does).
    public func setRendererValue(_ key: String, _ prop: String, _ value: JSONValue) {
        guard let node = nodes[key], !node.binds(prop) else { return }
        node.show(prop, value)
    }

    /// Answers a ``BridgeCall`` (copy/share/open): the value, or an error
    /// that rejects the tile's promise.
    public func resolve(_ call: BridgeCall, value: JSONValue, error: String? = nil) {
        send(.resolve(id: call.id, value: value, error: error))
    }

    // MARK: - Store → nodes

    private func handle(_ event: TreeStoreEvent) {
        switch event {
        case .tree(let rev, let delta):
            apply(delta)
            revision = rev
            if failure != nil { failure = nil }
        case .failed(let f):
            failure = f
        case .reset(let rev):
            rebuild()
            revision = rev
            failure = nil
        default:
            break
        }
    }

    private func apply(_ d: TreeDelta) {
        if d.remounted {
            rebuild()
            return
        }
        let tree = store.tree
        for k in d.removed { nodes[k] = nil }
        var born: [XbinNode] = []
        for k in d.inserted {
            guard let e = tree[k] else { continue }
            let n = XbinNode(entry: e)
            nodes[k] = n
            born.append(n)
        }
        for n in born { link(n) }
        for k in d.updated.union(d.restated.keys) where !d.inserted.contains(k) {
            guard let n = nodes[k], let e = tree[k] else { continue }
            n.refresh(from: e, restated: d.restated[k] ?? [])
        }
        for k in d.childrenChanged {
            guard let n = nodes[k], let e = tree[k] else { continue }
            n.childKeys = e.children
            link(n)
        }
    }

    private func link(_ n: XbinNode) {
        let kids = n.childKeys.compactMap { nodes[$0] }
        if kids.count != n.children.count || !zip(kids, n.children).allSatisfy({ $0 === $1 }) {
            n.children = kids
        }
    }

    private func rebuild() {
        let tree = store.tree
        var fresh: [String: XbinNode] = [:]
        fresh.reserveCapacity(tree.count)
        for k in tree.keysInOrder {
            if let e = tree[k] { fresh[k] = XbinNode(entry: e) }
        }
        nodes = fresh
        for n in fresh.values { n.children = n.childKeys.compactMap { fresh[$0] } }
        let r = tree.rootKey.flatMap { fresh[$0] }
        if root !== r { root = r }
        revision = store.revision
    }
}
