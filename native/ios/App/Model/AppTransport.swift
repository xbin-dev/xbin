import Foundation
import XbinAgent
import XbinCore

/// The app's URLSession: ephemeral (no cookies, no cache — sessions are
/// bearer tokens the app attaches itself), and a redirect is never followed
/// with a credential unless the caller allows that URL.
final class AppTransport: NSObject, APITransport, URLSessionTaskDelegate, @unchecked Sendable {
    static let shared = AppTransport()

    private(set) var session: URLSession!

    override init() {
        super.init()
        let c = URLSessionConfiguration.ephemeral
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.urlCache = nil
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.timeoutIntervalForRequest = 60
        c.waitsForConnectivity = false
        session = URLSession(configuration: c, delegate: self, delegateQueue: nil)
    }

    // Session-level redirects (plain `send`): an answer, not followed.
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        nil
    }

    static func urlRequest(_ r: APIRequest, origin: ServerOrigin) throws -> URLRequest {
        guard let url = r.url(on: origin) else { throw URLError(.badURL) }
        var req = URLRequest(url: url)
        req.httpMethod = r.method
        for (k, v) in r.headers { req.setValue(v, forHTTPHeaderField: k) }
        req.httpBody = r.body
        return req
    }

    static func headers(_ h: HTTPURLResponse) -> [String: String] {
        var out: [String: String] = [:]
        for (k, v) in h.allHeaderFields { out["\(k)"] = "\(v)" }
        return out
    }

    func send(_ r: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        let (data, resp) = try await session.data(for: Self.urlRequest(r, origin: origin))
        guard let h = resp as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        return APIResponse(status: h.statusCode, headers: Self.headers(h), body: data)
    }

    /// Streams one request: the response head, then body chunks as they
    /// arrive (tile pages through the scheme handler; SSE and NDJSON).
    func stream(_ request: URLRequest, allowRedirect: @escaping @Sendable (URL) -> Bool,
                onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
                onData: @escaping @Sendable (Data) -> Void,
                onDone: @escaping @Sendable ((any Error)?) -> Void) -> StreamingLoad {
        let load = StreamingLoad(allowRedirect: allowRedirect, onResponse: onResponse, onData: onData, onDone: onDone)
        let task = session.dataTask(with: request)
        task.delegate = load
        load.task = task
        task.resume()
        return load
    }
}

/// One streaming request's delegate (a per-task delegate).
final class StreamingLoad: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    fileprivate(set) weak var task: URLSessionDataTask?
    private let allowRedirect: @Sendable (URL) -> Bool
    private let onResponse: @Sendable (HTTPURLResponse) -> Void
    private let onData: @Sendable (Data) -> Void
    private let onDone: @Sendable ((any Error)?) -> Void
    private let lock = NSLock()
    private var finished = false

    init(allowRedirect: @escaping @Sendable (URL) -> Bool, onResponse: @escaping @Sendable (HTTPURLResponse) -> Void,
         onData: @escaping @Sendable (Data) -> Void, onDone: @escaping @Sendable ((any Error)?) -> Void) {
        self.allowRedirect = allowRedirect
        self.onResponse = onResponse
        self.onData = onData
        self.onDone = onDone
    }

    func cancel() {
        lock.lock(); finished = true; lock.unlock()
        task?.cancel()
    }

    private var isFinished: Bool { lock.lock(); defer { lock.unlock() }; return finished }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        guard let u = request.url, allowRedirect(u) else { return nil }
        return request
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse) async
        -> URLSession.ResponseDisposition {
        if let h = response as? HTTPURLResponse, !isFinished { onResponse(h) }
        return .allow
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        if !isFinished { onData(data) }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        lock.lock()
        let was = finished
        finished = true
        lock.unlock()
        if !was { onDone(error) }
    }
}

/// XbinAgent's transport over the workspace session: the bearer attached,
/// a 401 re-signed once (WorkspaceAuth), NDJSON streamed.
struct AgentSessionTransport: AgentTransport {
    let auth: WorkspaceAuth
    let transport: AppTransport

    func send(_ request: HTTPRequest) async throws -> HTTPResponse {
        let r = try await auth.send(APIRequest(request.method, request.target, headers: request.headers, body: request.body))
        return HTTPResponse(status: r.status, headers: r.headers, body: r.body)
    }

    func stream(_ request: HTTPRequest) async throws -> AsyncThrowingStream<Data, any Error> {
        let cred = try await auth.session(replacing: nil)
        do {
            return try await open(request, token: cred.token)
        } catch let e as AgentAPIError where e.status == 401 {
            let fresh = try await auth.session(replacing: cred.token)
            return try await open(request, token: fresh.token)
        }
    }

    /// Opens the stream: returns once the head arrived (2xx), or throws the
    /// server's error (AgentAPIError) before any chunk.
    private func open(_ request: HTTPRequest, token: String) async throws -> AsyncThrowingStream<Data, any Error> {
        let origin = await auth.origin
        var r = APIRequest(request.method, request.target, headers: request.headers, body: request.body)
        r.setHeader("Authorization", "Bearer \(token)")
        r.setHeader("X-XBin-Client", AppInfo.clientHeader)
        let urlRequest = try AppTransport.urlRequest(r, origin: origin)
        let (stream, cont) = AsyncThrowingStream<Data, any Error>.makeStream()
        let state = StreamHead()
        let transport = self.transport
        return try await withCheckedThrowingContinuation { (head: CheckedContinuation<AsyncThrowingStream<Data, any Error>, any Error>) in
            let load = transport.stream(urlRequest, allowRedirect: { _ in false }, onResponse: { h in
                if (200..<300).contains(h.statusCode) {
                    if state.claim() { head.resume(returning: stream) }
                } else {
                    state.fail(h)
                }
            }, onData: { d in
                if !state.append(d) { cont.yield(d) }
            }, onDone: { err in
                if let (h, body) = state.failure() {
                    if state.claim() {
                        head.resume(throwing: AgentAPIError(HTTPResponse(status: h.statusCode, headers: AppTransport.headers(h), body: body)))
                    }
                    cont.finish()
                } else if state.claim() {
                    head.resume(throwing: err ?? URLError(.badServerResponse))
                    cont.finish()
                } else if let err {
                    cont.finish(throwing: err)
                } else {
                    cont.finish()
                }
            })
            cont.onTermination = { _ in load.cancel() }
        }
    }
}

/// The head of a streamed response, shared by URLSession's callbacks.
private final class StreamHead: @unchecked Sendable {
    private let lock = NSLock()
    private var resumed = false
    private var errorHead: HTTPURLResponse?
    private var errorBody = Data()

    /// True the first time only (resume the waiter once).
    func claim() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if resumed { return false }
        resumed = true
        return true
    }

    func fail(_ h: HTTPURLResponse) { lock.lock(); errorHead = h; lock.unlock() }

    /// Keeps an error body (true = it was one; don't yield it).
    func append(_ d: Data) -> Bool {
        lock.lock(); defer { lock.unlock() }
        guard errorHead != nil else { return false }
        if errorBody.count < 65536 { errorBody.append(d) }
        return true
    }

    func failure() -> (HTTPURLResponse, Data)? {
        lock.lock(); defer { lock.unlock() }
        return errorHead.map { ($0, errorBody) }
    }
}
