import Foundation

/// The state of one native tile's view, fed by its runtime's bridge messages:
/// the tree, a revision, the tile's meta, the last runtime error — and
/// whether the view has failed (the app then falls back to the web tile,
/// plans/native.md §7.6).
///
/// Observers are plain closures, called synchronously after each message is
/// applied — no Combine; the SwiftUI renderer wraps the store in its own
/// observable model. The store is not thread-safe: use it from one
/// isolation domain (the main actor, where WebKit delivers messages).
/// Read entries (`store.tree[key]`) rather than keeping a copy of `tree`
/// across messages — a live copy makes the next patch copy the whole index
/// (copy-on-write).
public final class TreeStore {
    /// The tree major version this store understands (`mount.v`).
    public static let supportedVersion = 1

    /// The current tree (empty before the first mount).
    public private(set) var tree = Tree()
    /// Bumped by every mount and every patch that applied.
    public private(set) var revision = 0
    /// The `v` of the current mount.
    public private(set) var version: Int?
    /// The `n` of the last tree message (mount or patch) applied — what
    /// ``event(_:_:payload:)`` reports back so the runtime can order a
    /// controlled value against its own patches.
    public private(set) var treeSequence: Int?
    /// The tile's `xbin.native.saveState(…)` blob. It outlives the runtime
    /// (``reset()`` keeps it): persist it, and inject it into the next
    /// runtime with ``RuntimeScript/documentStart(caps:state:)``.
    public private(set) var savedState: JSONValue?
    /// The runtime's diagnostics, oldest first (the last
    /// ``maxDiagnostics``).
    public private(set) var diagnostics: [RuntimeDiagnostic] = []
    public static let maxDiagnostics = 200
    /// The merged `xbin.native.meta(…)` fields.
    public private(set) var meta = NativeMeta()
    /// The last error the runtime reported (cleared by a mount).
    public private(set) var lastRuntimeError: RuntimeError?
    /// Set when the native view can't continue: the runtime and the app
    /// disagree (a bad message, an op that doesn't apply, an unsupported
    /// version) or the runtime reported a fatal error. While set, everything
    /// but a mount is ignored; the app shows the web tile.
    public private(set) var failure: TreeStoreFailure?

    public var isMounted: Bool { !tree.isEmpty }

    private var observers: [Int: (TreeStoreEvent) -> Void] = [:]
    private var nextObserver = 0

    /// A store; `savedState` is the blob persisted from an earlier runtime.
    public init(savedState: JSONValue? = nil) {
        self.savedState = savedState
    }

    /// A user action on node `key`, as the call to send — with the tree
    /// sequence the renderer has applied.
    public func event(_ key: String, _ type: String, payload: JSONValue = [:]) -> RuntimeCall {
        .event(key: key, type: type, payload: payload, n: treeSequence)
    }

    /// Registers `observer`; it is called after every change until the
    /// returned token is cancelled or released.
    public func observe(_ observer: @escaping (TreeStoreEvent) -> Void) -> TreeStoreObservation {
        let id = nextObserver
        nextObserver += 1
        observers[id] = observer
        return TreeStoreObservation(store: self, id: id)
    }

    fileprivate func removeObserver(_ id: Int) { observers[id] = nil }

    private func emit(_ event: TreeStoreEvent) {
        for id in observers.keys.sorted() { observers[id]?(event) }
    }

    /// Decodes and applies a `WKScriptMessage.body` (see
    /// ``BridgeMessage/init(body:)``). A body that doesn't decode fails the
    /// store.
    @discardableResult
    public func receive(body: Any) -> TreeStoreEvent? {
        let message: BridgeMessage
        do { message = try BridgeMessage(body: body) } catch {
            return fail(.badMessage(String(describing: error)))
        }
        return apply(message)
    }

    /// Applies one message and returns the event it produced (also sent to
    /// the observers); nil when the message changed nothing or was ignored.
    @discardableResult
    public func apply(_ message: BridgeMessage) -> TreeStoreEvent? {
        if case .mount(let v, let root, let n) = message {
            guard v <= Self.supportedVersion, v >= 1 else { return fail(.unsupportedVersion(v)) }
            do {
                let delta = try tree.mount(root)
                version = v
                treeSequence = n
                failure = nil
                lastRuntimeError = nil
                revision += 1
                let e = TreeStoreEvent.tree(revision: revision, delta: delta)
                emit(e)
                return e
            } catch {
                return fail(.patch(error as? PatchError ?? .malformed(String(describing: error))))
            }
        }
        if failure != nil { return nil }
        switch message {
        case .mount:
            return nil // handled above
        case .patch(let ops, let n):
            guard isMounted else { return fail(.patch(.notMounted)) }
            do {
                let delta = try tree.apply(ops)
                if let n { treeSequence = n }
                revision += 1
                let e = TreeStoreEvent.tree(revision: revision, delta: delta)
                emit(e)
                return e
            } catch {
                return fail(.patch(error as? PatchError ?? .malformed(String(describing: error))))
            }
        case .meta(let m):
            let merged = meta.merging(m)
            if merged == meta { return nil }
            meta = merged
            let e = TreeStoreEvent.meta(merged)
            emit(e)
            return e
        case .error(let err):
            lastRuntimeError = err
            let e = TreeStoreEvent.runtimeError(err)
            emit(e)
            if err.isFatal { return fail(.runtime(err)) }
            return e
        case .diag(let diag):
            diagnostics.append(diag)
            if diagnostics.count > Self.maxDiagnostics { diagnostics.removeFirst(diagnostics.count - Self.maxDiagnostics) }
            let e = TreeStoreEvent.diagnostic(diag)
            emit(e)
            return e
        case .state(let st):
            savedState = st
            let e = TreeStoreEvent.state(st)
            emit(e)
            return e
        case .call(let c):
            let e = TreeStoreEvent.call(c)
            emit(e)
            return e
        case .unknown:
            return nil
        }
    }

    /// Marks the store failed (e.g. the app's render timeout, §7.6).
    @discardableResult
    public func fail(_ reason: TreeStoreFailure) -> TreeStoreEvent {
        failure = reason
        let e = TreeStoreEvent.failed(reason)
        emit(e)
        return e
    }

    /// Forgets the runtime's state (it was torn down or reloaded) — all but
    /// ``savedState``, which is meant to outlive it. The revision keeps
    /// counting so a renderer never sees one twice.
    public func reset() {
        tree = Tree()
        version = nil
        treeSequence = nil
        diagnostics = []
        meta = NativeMeta()
        lastRuntimeError = nil
        failure = nil
        revision += 1
        emit(.reset(revision: revision))
    }
}

/// What a ``TreeStore`` observer is told.
public enum TreeStoreEvent: Sendable, Equatable {
    /// The tree changed (a mount has ``TreeDelta/remounted`` set).
    case tree(revision: Int, delta: TreeDelta)
    /// The merged meta changed.
    case meta(NativeMeta)
    /// The runtime reported an error. A fatal one
    /// (``RuntimeError/isFatal``) is followed by ``failed(_:)``; others are
    /// reported and the view keeps going.
    case runtimeError(RuntimeError)
    /// A diagnostic for the tile's author.
    case diagnostic(RuntimeDiagnostic)
    /// The tile saved its state blob: persist it.
    case state(JSONValue)
    /// The tile asked the app for something; answer with
    /// ``RuntimeCall/resolve(id:value:error:)``.
    case call(BridgeCall)
    /// The native view can't continue: fall back to the web tile.
    case failed(TreeStoreFailure)
    /// ``TreeStore/reset()`` cleared the store.
    case reset(revision: Int)
}

/// Why a native view failed.
public enum TreeStoreFailure: Error, Sendable, Equatable, CustomStringConvertible {
    /// A message that doesn't decode.
    case badMessage(String)
    /// A mount or patch that doesn't apply.
    case patch(PatchError)
    /// A mount with a major version this app doesn't speak.
    case unsupportedVersion(Int)
    /// The runtime reported a fatal error (``RuntimeError/isFatal``).
    case runtime(RuntimeError)
    /// The app gave up on the runtime (render timeout, crash, kill switch).
    case app(String)

    public var description: String {
        switch self {
        case .badMessage(let s): return s
        case .patch(let e): return e.description
        case .unsupportedVersion(let v): return "tree version \(v) is newer than this app (\(TreeStore.supportedVersion))"
        case .runtime(let e): return "\(e.kind): \(e.message)" + (e.location.map { " (\($0))" } ?? "")
        case .app(let s): return s
        }
    }
}

/// Keeps an observer registered; cancel it (or let it go) to stop.
public final class TreeStoreObservation {
    private weak var store: TreeStore?
    private let id: Int

    fileprivate init(store: TreeStore, id: Int) {
        self.store = store
        self.id = id
    }

    public func cancel() {
        store?.removeObserver(id)
        store = nil
    }

    deinit { store?.removeObserver(id) }
}
