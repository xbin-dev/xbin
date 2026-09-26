import Foundation
import XbinCore
import XbinTerm

/// One `/ws/term` socket for XbinTerm's TermSession: URLSessionWebSocketTask
/// with the workspace session as a bearer (the app's own code, §2). It
/// reports the upgrade's HTTP status when it's refused (401/403/404/409/400
/// mean different things to the session), `.unreachable` for network
/// errors and `.dropped` when an open socket closes — always on the main
/// actor, never from inside `start`/`send`/`close`.
@MainActor
final class WebSocketTransport: TermTransport {
    private let url: URL?
    private let token: String
    private let events: @MainActor (TermTransportEvent) -> Void
    private var session: URLSession?
    private var task: URLSessionWebSocketTask?
    private var opened = false
    private var finished = false

    init(origin: ServerOrigin, path: String, token: String, events: @escaping @MainActor (TermTransportEvent) -> Void) {
        url = URL(string: origin.webSocketOrigin + path)
        self.token = token
        self.events = events
    }

    func start() {
        guard let url else {
            DispatchQueue.main.async { MainActor.assumeIsolated { self.finish(.failed) } }
            return
        }
        var req = URLRequest(url: url)
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        req.setValue(AppInfo.clientHeader, forHTTPHeaderField: "X-XBin-Client")
        req.timeoutInterval = 20
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        let delegate = SocketDelegate(owner: self)
        let s = URLSession(configuration: config, delegate: delegate, delegateQueue: .main)
        session = s
        let t = s.webSocketTask(with: req)
        t.maximumMessageSize = 16 << 20
        task = t
        t.resume()
        receive()
    }

    func send(_ msg: TermWireMessage) {
        guard let task, opened, !finished else { return }
        switch msg {
        case .text(let s): task.send(.string(s)) { _ in }
        case .binary(let b): task.send(.data(Data(b))) { _ in }
        }
    }

    func close() {
        finished = true
        task?.cancel(with: .normalClosure, reason: nil)
        session?.invalidateAndCancel()
        task = nil
        session = nil
    }

    private func receive() {
        task?.receive { [weak self] result in
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    guard let self, !self.finished else { return }
                    switch result {
                    case .success(.string(let s)):
                        self.events(.message(.text(s)))
                        self.receive()
                    case .success(.data(let d)):
                        self.events(.message(.binary([UInt8](d))))
                        self.receive()
                    case .success:
                        self.receive()
                    case .failure:
                        // didComplete/didClose reports why.
                        break
                    }
                }
            }
        }
    }

    fileprivate func didOpen() {
        guard !finished else { return }
        opened = true
        events(.opened)
    }

    fileprivate func didClose() { finish(.dropped) }

    fileprivate func didComplete(status: Int?, error: (any Error)?) {
        if opened { finish(.dropped); return }
        if let status, status >= 300 { finish(.refused(status: status, body: "")); return }
        if let error { finish(.unreachable(error.localizedDescription)); return }
        finish(.failed)
    }

    private func finish(_ why: TermCloseInfo) {
        guard !finished else { return }
        finished = true
        events(.closed(why))
        session?.invalidateAndCancel()
    }
}

/// URLSession's delegate for one socket (callbacks on the main queue).
private final class SocketDelegate: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    weak var owner: WebSocketTransport?
    init(owner: WebSocketTransport) { self.owner = owner }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        MainActor.assumeIsolated { owner?.didOpen() }
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask,
                    didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        MainActor.assumeIsolated { owner?.didClose() }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        let status = (task.response as? HTTPURLResponse)?.statusCode
        let message = error.map { $0 as NSError }
        MainActor.assumeIsolated { owner?.didComplete(status: status, error: message) }
    }
}
