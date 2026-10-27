import Foundation
import Network
import XbinCore

// The loopback half of a workspace's app tunnel (D204, native/spec/tunnel.md
// §7; plans/app-tunnel.md Appendix B §2, §4 Phase 3). WebKit and URLSession
// speak plain HTTP to `http://127.0.0.1:<port>`; every TCP connection the
// forwarder accepts gets its own upstream — an iroh bi-stream with its own
// Noise handshake (TunnelManager), or, in Debug builds, a plain TCP
// connection — and the bytes are spliced through unchanged, Host header
// included (`127.0.0.1:<port>`: xbind echoes it in `xbin-ws-origin`).
//
// Apple-only (Network.framework): app-stubcheck skips this file; its
// Stubs/App/AppStubs.swift declares what the checked files use of it.

/// Where one accepted connection's bytes go: set up (connected, and through
/// its handshake) by the forwarder's `connect`, then spliced.
protocol ForwardUpstream: Sendable {
    /// Pumps both directions between `local` and xbind until both end.
    func splice(with local: any ByteStream) async
}

/// Listens on `127.0.0.1:<port>` for one tunnelled workspace while the app
/// is in front. Loopback only: it binds 127.0.0.1 on the loopback interface
/// (no Local Network prompt) and refuses any peer that isn't loopback. When
/// `connect` fails for a connection — the tunnel is down — that connection
/// gets a canned `502` page with `Connection: close`, so WebKit shows why
/// rather than spinning.
final class LoopbackForwarder: @unchecked Sendable {
    typealias Connect = @Sendable () async throws -> any ForwardUpstream

    private let connect: Connect
    private let queue = DispatchQueue(label: "dev.xbin.tunnel.forwarder")
    private let lock = NSLock()
    private var listener: NWListener?
    private var connections: [ObjectIdentifier: NWConnection] = [:]
    private var tasks: [ObjectIdentifier: Task<Void, Never>] = [:]
    private var stopped = false

    init(connect: @escaping Connect) {
        self.connect = connect
    }

    /// Binds `127.0.0.1:port` (0: a port the system picks) and returns the
    /// bound port once listening. Throws when the port can't be had (taken:
    /// the caller binds 0 and keeps the new one).
    func start(port: Int) async throws -> Int {
        let params = NWParameters.tcp
        params.requiredInterfaceType = .loopback
        params.allowLocalEndpointReuse = true
        params.acceptLocalOnly = true
        let p = NWEndpoint.Port(rawValue: UInt16(clamping: max(0, port))) ?? .any
        params.requiredLocalEndpoint = .hostPort(host: .ipv4(.loopback), port: p)
        let l = try NWListener(using: params)
        lock.lock()
        stopped = false
        listener?.cancel()
        listener = l
        lock.unlock()
        l.newConnectionHandler = { [weak self] c in self?.accept(c) }
        let started = Once<Int>()
        l.stateUpdateHandler = { [weak l] state in
            switch state {
            case .ready:
                started.resume(.success(Int(l?.port?.rawValue ?? 0)))
            case .failed(let e), .waiting(let e):
                started.resume(.failure(e))
                l?.cancel()
            case .cancelled:
                started.resume(.failure(CancellationError()))
            default:
                break
            }
        }
        l.start(queue: queue)
        let bound = try await started.value
        guard bound > 0 else { throw NWError.posix(.EADDRNOTAVAIL) }
        return bound
    }

    /// Stops listening and drops every connection (the app went to the
    /// background, or the workspace is gone).
    func stop() {
        lock.lock()
        stopped = true
        let l = listener
        listener = nil
        let cs = Array(connections.values)
        let ts = Array(tasks.values)
        connections = [:]
        tasks = [:]
        lock.unlock()
        l?.cancel()
        for t in ts { t.cancel() }
        for c in cs { c.cancel() }
    }

    var isListening: Bool { lock.lock(); defer { lock.unlock() }; return listener != nil && !stopped }

    private func accept(_ c: NWConnection) {
        guard Self.isLoopback(c.endpoint) else {
            c.cancel()
            return
        }
        let key = ObjectIdentifier(c)
        lock.lock()
        if stopped {
            lock.unlock()
            c.cancel()
            return
        }
        connections[key] = c
        let connect = self.connect
        let local = NWByteStream(c, queue: queue)
        let t = Task { [weak self] in
            defer { self?.drop(key) }
            guard (try? await local.ready()) != nil else { return }
            let upstream: any ForwardUpstream
            do {
                upstream = try await connect()
            } catch {
                await Self.answer502(local, why: Self.describe(error))
                return
            }
            await upstream.splice(with: local)
            await local.cancel()
        }
        tasks[key] = t
        lock.unlock()
    }

    private func drop(_ key: ObjectIdentifier) {
        lock.lock()
        let c = connections.removeValue(forKey: key)
        tasks[key] = nil
        lock.unlock()
        c?.cancel()
    }

    static func isLoopback(_ e: NWEndpoint) -> Bool {
        guard case .hostPort(let host, _) = e else { return false }
        switch host {
        case .ipv4(let a): return a.isLoopback
        case .ipv6(let a): return a.isLoopback
        case .name(let n, _): return n == "127.0.0.1" || n == "::1" || n == "localhost"
        @unknown default: return false
        }
    }

    static func describe(_ error: any Error) -> String {
        if let e = error as? LocalizedError, let d = e.errorDescription { return d }
        return String(describing: error)
    }

    /// The tunnel is down: read the request (so closing doesn't reset it
    /// away), answer a small page, close.
    static func answer502(_ local: NWByteStream, why: String) async {
        _ = try? await local.read(max: 64 << 10)
        let body = """
        <!doctype html><meta name=viewport content='width=device-width'><title>Not connected</title>\
        <body style='font:16px -apple-system,sans-serif;padding:24px'><h3>The workspace's tunnel isn't connected</h3>\
        <p>\(why.htmlEscaped)</p><p>Pull to refresh, or open Settings → Connection.</p>
        """
        let data = Data(body.utf8)
        var head = "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/html; charset=utf-8\r\n"
        head += "Content-Length: \(data.count)\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n"
        try? await local.write(Data(head.utf8) + data)
        try? await local.finish()
        try? await Task.sleep(nanoseconds: 500_000_000)
        await local.cancel()
    }
}

/// An NWConnection as a ByteStream: reads return what arrived (empty = the
/// peer's FIN), `finish` sends our FIN (TCP half-close), `cancel` resets.
final class NWByteStream: ByteStream, @unchecked Sendable {
    private let connection: NWConnection
    private let queue: DispatchQueue
    private let lock = NSLock()
    private var ended = false

    init(_ c: NWConnection, queue: DispatchQueue) {
        connection = c
        self.queue = queue
    }

    /// Starts the connection (an accepted one, or an outgoing one) and waits
    /// until it is ready.
    func ready() async throws {
        let done = Once<Void>()
        connection.stateUpdateHandler = { state in
            switch state {
            case .ready: done.resume(.success(()))
            case .failed(let e), .waiting(let e): done.resume(.failure(e))
            case .cancelled: done.resume(.failure(CancellationError()))
            default: break
            }
        }
        connection.start(queue: queue)
        try await done.value
    }

    func read(max: Int) async throws -> Data {
        lock.lock()
        if ended { lock.unlock(); return Data() }
        lock.unlock()
        return try await withCheckedThrowingContinuation { (k: CheckedContinuation<Data, any Error>) in
            connection.receive(minimumIncompleteLength: 1, maximumLength: Swift.max(1, max)) { [weak self] data, _, complete, error in
                if let data, !data.isEmpty {
                    if complete { self?.markEnded() }
                    k.resume(returning: data)
                } else if let error {
                    k.resume(throwing: error)
                } else {
                    self?.markEnded()
                    k.resume(returning: Data())
                }
            }
        }
    }

    private func markEnded() { lock.lock(); ended = true; lock.unlock() }

    func write(_ data: Data) async throws {
        try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, any Error>) in
            connection.send(content: data, completion: .contentProcessed { error in
                if let error { k.resume(throwing: error) } else { k.resume() }
            })
        }
    }

    func finish() async throws {
        try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, any Error>) in
            connection.send(content: nil, contentContext: .finalMessage, isComplete: true, completion: .contentProcessed { error in
                if let error { k.resume(throwing: error) } else { k.resume() }
            })
        }
    }

    func cancel() async { connection.cancel() }
}

/// Pumps two byte streams into each other: an end of stream on one side
/// half-closes the other; an error on either resets both. Returns when both
/// directions are over.
enum ByteSplice {
    static func run(_ a: any ByteStream, _ b: any ByteStream) async {
        await withTaskGroup(of: Bool.self) { g in
            g.addTask { await pump(from: a, to: b) }
            g.addTask { await pump(from: b, to: a) }
            for await clean in g where !clean {
                await a.cancel()
                await b.cancel()
                g.cancelAll()
            }
        }
    }

    /// One direction; false when it failed (not an orderly end).
    private static func pump(from src: any ByteStream, to dst: any ByteStream) async -> Bool {
        do {
            while !Task.isCancelled {
                let d = try await src.read(max: 64 << 10)
                if d.isEmpty {
                    try await dst.finish()
                    return true
                }
                try await dst.write(d)
            }
            return false
        } catch {
            return false
        }
    }
}

#if DEBUG
/// Debug builds' stand-in for the tunnel (`-XbinTunnelForward YES`, the
/// spike's step 0b): each connection is spliced as is to the workspace's
/// own `host:port` over TCP — the app talks to `127.0.0.1:<port>` exactly as
/// through a tunnel, with no iroh and no xbin layer. Plain-http servers only.
struct TCPUpstream: ForwardUpstream {
    let remote: NWByteStream

    static func open(host: String, port: Int) async throws -> TCPUpstream {
        guard let p = NWEndpoint.Port(rawValue: UInt16(clamping: port)) else { throw NWError.posix(.EINVAL) }
        let c = NWConnection(host: NWEndpoint.Host(host), port: p, using: .tcp)
        let s = NWByteStream(c, queue: DispatchQueue(label: "dev.xbin.tunnel.tcp"))
        do {
            try await s.ready()
        } catch {
            c.cancel()
            throw error
        }
        return TCPUpstream(remote: s)
    }

    func splice(with local: any ByteStream) async {
        await ByteSplice.run(local, remote)
        await remote.cancel()
    }
}
#endif

/// Network reachability for the tunnels' reconnects (NWPathMonitor): calls
/// `changed(satisfied)` on the main actor whenever the path changes.
final class TunnelPathWatch: @unchecked Sendable {
    private let monitor = NWPathMonitor()

    init(changed: @escaping @MainActor @Sendable (Bool) -> Void) {
        monitor.pathUpdateHandler = { path in
            let ok = path.status == .satisfied
            Task { @MainActor in changed(ok) }
        }
        monitor.start(queue: DispatchQueue(label: "dev.xbin.tunnel.path"))
    }

    func stop() { monitor.cancel() }
}
