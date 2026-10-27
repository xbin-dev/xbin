import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XbinCore

/// Where a tunnelled workspace's loopback forwarder listens, readable from
/// any thread: `http://127.0.0.1:<port>`. The port is kept in the record
/// (`TunnelInfo.port`) so pages keep their origin — and WebKit its cookies
/// and local storage for them — across launches; when it can't be had again
/// the forwarder takes another and ``generation`` moves, so open pages
/// reload there.
final class TunnelAddress: @unchecked Sendable {
    private let lock = NSLock()
    private var _port: Int
    private var _generation = 0

    init(port: Int) { _port = port }

    var port: Int { lock.lock(); defer { lock.unlock() }; return _port }
    var generation: Int { lock.lock(); defer { lock.unlock() }; return _generation }

    /// The forwarder listens on another port now.
    func moved(to port: Int) {
        lock.lock()
        if port != _port {
            _port = port
            _generation += 1
        }
        lock.unlock()
    }

    var origin: ServerOrigin { Self.origin(port: port) }

    static func origin(port: Int) -> ServerOrigin {
        // 127.0.0.1 and a port in range always make an origin.
        (try? ServerOrigin(scheme: "http", host: "127.0.0.1", port: port)) ?? (try! ServerOrigin(string: "http://127.0.0.1:1"))
    }

    /// A port for a workspace that has none yet: the dynamic range, so a
    /// fixed service's port is never chosen.
    static func freshPort() -> Int { Int.random(in: 49152...65535) }
}

/// A workspace reached through its app tunnel (D204): requests go to its
/// loopback forwarder, plain http with the forwarder's Host (native/spec/
/// tunnel.md §7), on the app's ordinary session — loopback needs nothing
/// special. ``ready()`` brings the tunnel up (TunnelManager) when it is
/// down. The public origin stays the workspace's identity.
struct TunnelTransport: WorkspaceTransport {
    let publicOrigin: ServerOrigin
    let address: TunnelAddress
    /// Brings the tunnel up (or waits for it); throws why it can't.
    let start: @Sendable () async throws -> Void
    private let shared: AppTransport

    init(publicOrigin: ServerOrigin, address: TunnelAddress, shared: AppTransport = .shared,
         start: @escaping @Sendable () async throws -> Void) {
        self.publicOrigin = publicOrigin
        self.address = address
        self.shared = shared
        self.start = start
    }

    var origin: ServerOrigin { address.origin }
    var generation: Int { address.generation }

    func ready() async throws -> ServerOrigin {
        try await start()
        return address.origin
    }

    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        guard origin == publicOrigin else { throw ForeignOriginError(origin: origin) }
        return try await shared.send(request, to: ready())
    }

    func stream(_ request: URLRequest, allowRedirect: @escaping @Sendable (URL) -> URL?,
                onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
                onData: @escaping @Sendable (Data) -> Void,
                onDone: @escaping @Sendable ((any Error)?) -> Void) -> StreamingLoad {
        shared.stream(request, allowRedirect: allowRedirect, onResponse: onResponse, onData: onData, onDone: onDone)
    }

    func upload(_ request: URLRequest, from data: Data, delegate: (any URLSessionTaskDelegate)?) async throws -> (Data, URLResponse) {
        try await shared.session.upload(for: request, from: data, delegate: delegate)
    }

    func socketConfiguration() -> URLSessionConfiguration {
        let c = Self.plainSocketConfiguration()
        // Loopback: never through a configured proxy.
        c.connectionProxyDictionary = [:]
        return c
    }
}

/// A value delivered once and awaited (callbacks and races into async code).
final class Once<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var result: Result<T, any Error>?
    private var waiters: [CheckedContinuation<T, any Error>] = []

    init() {}

    /// The first call wins (true); later ones are ignored.
    @discardableResult
    func resume(_ r: Result<T, any Error>) -> Bool {
        lock.lock()
        guard result == nil else { lock.unlock(); return false }
        result = r
        let ws = waiters
        waiters = []
        lock.unlock()
        for w in ws { w.resume(with: r) }
        return true
    }

    var value: T {
        get async throws {
            try await withCheckedThrowingContinuation { k in
                lock.lock()
                if let r = result {
                    lock.unlock()
                    k.resume(with: r)
                } else {
                    waiters.append(k)
                    lock.unlock()
                }
            }
        }
    }
}

/// `work`, or a timeout after `seconds`: the work keeps running (an FFI call
/// doesn't hear cancellation) but nobody waits for it. `onTimeout` runs
/// when the time ran out first (closing what the work holds).
func withDeadline<T: Sendable>(_ seconds: Double, _ what: String, onTimeout: @escaping @Sendable () async -> Void = {},
                               _ work: @escaping @Sendable () async throws -> T) async throws -> T {
    let once = Once<T>()
    Task {
        do { once.resume(.success(try await work())) } catch { once.resume(.failure(error)) }
    }
    let timer = Task {
        try? await Task.sleep(nanoseconds: UInt64(seconds * 1e9))
        guard !Task.isCancelled else { return }
        if once.resume(.failure(DeadlineExceeded(what: what, seconds: seconds))) { await onTimeout() }
    }
    defer { timer.cancel() }
    return try await once.value
}

struct DeadlineExceeded: Error, CustomStringConvertible {
    var what: String
    var seconds: Double
    var description: String { "\(what) took longer than \(Int(seconds)) s" }
}
