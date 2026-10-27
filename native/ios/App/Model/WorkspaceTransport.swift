import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XbinCore

/// How one workspace's requests reach its xbind (D204): straight to its
/// server (``DirectTransport``), or through its tunnel's loopback forwarder
/// (TunnelTransport). Every request the app makes for a workspace goes
/// through its transport — the REST calls (`WorkspaceAuth`), the tile scheme
/// handler's loads, uploads, the agent's NDJSON, and the sockets (`/ws/events`,
/// `/ws/term`, a tile's pty) — so a tunnelled workspace sends nothing to its
/// public address.
///
/// The workspace's identity stays its public origin (``publicOrigin``, the
/// record's server): what device login signs, links, Handoff and Settings
/// show. ``origin`` is where the bytes go; the two are the same on a direct
/// connection.
///
/// The app's traffic that isn't a workspace's — the kill switch, the push
/// relay — stays on `AppTransport.shared` and https.
///
/// Foundation only: app-stubcheck compiles this file on Linux.
protocol WorkspaceTransport: APITransport {
    /// The workspace's public origin: the only one ``send(_:to:)`` serves,
    /// so a bearer is never sent anywhere else.
    var publicOrigin: ServerOrigin { get }
    /// Where requests physically go (synchronous: a tunnel's port is kept,
    /// so it is known before its listener is up). Direct: the server.
    var origin: ServerOrigin { get }
    /// Bumped when ``origin`` changed (a tunnel lost its port): open pages
    /// reload. Constant on a direct connection.
    var generation: Int { get }
    /// Starts the tunnel if it is down and waits until requests can flow;
    /// direct: returns at once. Its answer is ``origin``.
    func ready() async throws -> ServerOrigin
    /// One request streamed (tile pages through the scheme handler; SSE and
    /// NDJSON): the head, then the body as it arrives. `allowRedirect` says
    /// where a redirect goes — the URL to follow, or nil for none
    /// (`TileScheme.redirect`).
    func stream(_ request: URLRequest, allowRedirect: @escaping @Sendable (URL) -> URL?,
                onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
                onData: @escaping @Sendable (Data) -> Void,
                onDone: @escaping @Sendable ((any Error)?) -> Void) -> StreamingLoad
    /// An upload with progress (a native tile's attach): `request` already
    /// addressed to ``origin``.
    func upload(_ request: URLRequest, from data: Data, delegate: (any URLSessionTaskDelegate)?) async throws -> (Data, URLResponse)
    /// What every socket's URLSession uses: ephemeral, no cookies, no cache.
    func socketConfiguration() -> URLSessionConfiguration
}

/// A request for another origin than the workspace's own reached its
/// transport (a bug: refused rather than sent with the session).
struct ForeignOriginError: Error, CustomStringConvertible {
    var origin: ServerOrigin
    var description: String { "refused to send a workspace request to \(origin.origin)" }
}

extension WorkspaceTransport {
    /// The ephemeral, cookie-less, cache-less configuration of the app's
    /// sockets.
    static func plainSocketConfiguration() -> URLSessionConfiguration {
        let c = URLSessionConfiguration.ephemeral
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.urlCache = nil
        return c
    }
}

/// A workspace reached at its server, as before D204: `AppTransport.shared`
/// with a fixed origin.
struct DirectTransport: WorkspaceTransport {
    let publicOrigin: ServerOrigin
    private let shared: AppTransport

    init(origin: ServerOrigin, shared: AppTransport = .shared) {
        publicOrigin = origin
        self.shared = shared
    }

    var origin: ServerOrigin { publicOrigin }
    var generation: Int { 0 }

    func ready() async throws -> ServerOrigin { publicOrigin }

    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        guard origin == publicOrigin else { throw ForeignOriginError(origin: origin) }
        return try await shared.send(request, to: publicOrigin)
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

    func socketConfiguration() -> URLSessionConfiguration { Self.plainSocketConfiguration() }
}

/// A workspace's transport that can move (D204): a device upgraded to its
/// workspace's tunnel (§9) goes on through it without the workspace's
/// session, sockets and pages being rebuilt. Everything holds this one; it
/// forwards to the transport of the moment. Moving bumps ``generation``,
/// so open pages reload on the new address.
final class SwitchableTransport: WorkspaceTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var _current: any WorkspaceTransport
    private var base = 0

    init(_ t: any WorkspaceTransport) { _current = t }

    var current: any WorkspaceTransport { lock.lock(); defer { lock.unlock() }; return _current }

    /// From now on through `t` (the same public origin).
    func switchTo(_ t: any WorkspaceTransport) {
        lock.lock()
        base += _current.generation + 1
        _current = t
        lock.unlock()
    }

    var publicOrigin: ServerOrigin { current.publicOrigin }
    var origin: ServerOrigin { current.origin }
    var generation: Int { lock.lock(); defer { lock.unlock() }; return base + _current.generation }

    func ready() async throws -> ServerOrigin { try await current.ready() }

    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        try await current.send(request, to: origin)
    }

    func stream(_ request: URLRequest, allowRedirect: @escaping @Sendable (URL) -> URL?,
                onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
                onData: @escaping @Sendable (Data) -> Void,
                onDone: @escaping @Sendable ((any Error)?) -> Void) -> StreamingLoad {
        current.stream(request, allowRedirect: allowRedirect, onResponse: onResponse, onData: onData, onDone: onDone)
    }

    func upload(_ request: URLRequest, from data: Data, delegate: (any URLSessionTaskDelegate)?) async throws -> (Data, URLResponse) {
        try await current.upload(request, from: data, delegate: delegate)
    }

    func socketConfiguration() -> URLSessionConfiguration { current.socketConfiguration() }
}
