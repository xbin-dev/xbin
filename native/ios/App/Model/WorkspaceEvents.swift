import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XbinAgent
import XbinCore

// The app's own `/ws/events` socket for one workspace (plans/native.md §7.7,
// §13; XbinCore Client/Events.swift has the rules). It runs while a
// foreground window shows the workspace and is authenticated with the
// workspace session as a bearer: a human principal, so the socket and what
// it carries stay in the app's Swift code — never handed to a tile, never
// injected into a web view (§2 invariant 1).
//
// Other code hooks in without knowing about sockets:
//
//   // a tile screen (native runtime or web page) — live reload:
//   .task { await workspace.events.onReload(of: tile.path) { runtime?.reload() } }
//   // an agent screen's feed — live session events, catch-up after a gap:
//   tasks.append(workspace.events.deliver(to: feed))
//
// Foundation only (no UIKit/WebKit): native/tools/app-check compiles this
// file on Linux and drives it with a scripted socket.

/// What an agent screen's feed gets from the socket.
enum SessionSignal: Sendable {
    case event(SessionHubEvent)
    /// The socket reopened after a gap: re-read the log (`?since=`).
    case resync
}

/// What a socket reports (always on the main actor).
enum EventSocketSignal: Sendable, Equatable {
    case opened
    case text(String)
    case closed(EventSocketPolicy.Close)
}

/// One connection attempt.
@MainActor
protocol EventSocket: AnyObject {
    func close()
}

/// Opens a socket to `url` with `bearer`, reporting to `signal`.
typealias EventSocketFactory = @MainActor (_ url: URL, _ bearer: String,
                                           _ signal: @escaping @MainActor (EventSocketSignal) -> Void) -> any EventSocket

@MainActor
final class WorkspaceEvents {
    private let auth: WorkspaceAuth
    private let makeSocket: EventSocketFactory
    private let sleep: @Sendable (Double) async -> Void
    private let jitter: () -> Double

    private(set) var policy = EventSocketPolicy()
    private var socket: (any EventSocket)?
    /// Bumped per connection attempt: signals from an older socket are ignored.
    private var generation = 0
    private var pending: Task<Void, Never>?
    /// The bearer the current socket was opened with (a 401 replaces it).
    private var token: String?

    /// A `term` event (the session directory) for this workspace's user.
    var onTerm: ((TermEvent) -> Void)?
    /// The workspace's title or icon changed.
    var onBranding: (() -> Void)?
    /// A socket reopened after a gap: re-list what may have changed.
    var onResync: (() -> Void)?
    /// Every parsed event (diagnostics and tests).
    var onEvent: ((AppEvent) -> Void)?
    /// The user whose term events count (an admin's socket carries everyone's).
    var userID: () -> String = { "" }

    private struct ReloadSub { let tile: String; let cont: AsyncStream<Void>.Continuation }
    private struct SessionSub { let id: String; let cont: AsyncStream<SessionSignal>.Continuation }
    private var reloadSubs: [UUID: ReloadSub] = [:]
    private var sessionSubs: [UUID: SessionSub] = [:]

    init(auth: WorkspaceAuth, makeSocket: @escaping EventSocketFactory,
         sleep: @escaping @Sendable (Double) async -> Void = { try? await Task.sleep(nanoseconds: UInt64($0 * 1e9)) },
         jitter: @escaping () -> Double = { Double.random(in: 0.8...1.2) }) {
        self.auth = auth
        self.makeSocket = makeSocket
        self.sleep = sleep
        self.jitter = jitter
    }

    var isConnected: Bool { policy.connected }

    // MARK: Hooks for screens

    /// Change notices for `tile` (a tile path): one element per `reload`
    /// that targets it — the most specific open tile covering the changed
    /// component, as the web shell picks — or a grant change naming it.
    /// Ends when the iterating task is cancelled.
    func reloads(of tile: String) -> AsyncStream<Void> {
        let (stream, cont) = AsyncStream<Void>.makeStream(bufferingPolicy: .bufferingNewest(1))
        let key = UUID()
        reloadSubs[key] = ReloadSub(tile: tile, cont: cont)
        cont.onTermination = { [weak self] _ in
            Task { @MainActor in self?.reloadSubs[key] = nil }
        }
        return stream
    }

    /// Runs `perform` on every reload of `tile` until the calling task is
    /// cancelled (a view's `.task`). A save is often several writes (and
    /// several events): it waits 200 ms first, so a burst reloads once or
    /// twice, not once per file.
    func onReload(of tile: String, perform: () -> Void) async {
        for await _ in reloads(of: tile) {
            try? await Task.sleep(nanoseconds: 200_000_000)
            if Task.isCancelled { return }
            perform()
        }
    }

    /// The live events of agent session `id`, and a `.resync` after a gap.
    func sessionSignals(_ id: String) -> AsyncStream<SessionSignal> {
        let (stream, cont) = AsyncStream<SessionSignal>.makeStream(bufferingPolicy: .bufferingNewest(256))
        let key = UUID()
        sessionSubs[key] = SessionSub(id: id, cont: cont)
        cont.onTermination = { [weak self] _ in
            Task { @MainActor in self?.sessionSubs[key] = nil }
        }
        return stream
    }

    /// Feeds `feed` from the socket until the returned task is cancelled:
    /// its session's frames go to `receive`, a reconnect to `catchUp`.
    func deliver(to feed: AgentSessionFeed) -> Task<Void, Never> {
        let stream = sessionSignals(feed.sessionID)
        return Task {
            for await s in stream {
                switch s {
                case .event(let e): await feed.receive(e)
                case .resync: await feed.catchUp()
                }
            }
        }
    }

    /// Open tiles that follow reloads (tests, diagnostics).
    var followedTiles: [String] { reloadSubs.values.map(\.tile) }

    // MARK: Lifecycle

    /// True while a foreground window shows this workspace.
    func setWanted(_ on: Bool) { perform(policy.setWanted(on)) }

    private func perform(_ action: EventSocketPolicy.Action) {
        switch action {
        case .none:
            break
        case .disconnect:
            pending?.cancel()
            pending = nil
            generation += 1
            socket?.close()
            socket = nil
        case .connect:
            pending?.cancel()
            generation += 1
            let gen = generation
            pending = Task { [weak self] in await self?.connect(gen) }
        case .retry(let after):
            pending?.cancel()
            let sleep = self.sleep
            pending = Task { [weak self] in
                await sleep(after)
                guard !Task.isCancelled, let self else { return }
                self.perform(self.policy.retryDue())
            }
        case .reauthenticate:
            pending?.cancel()
            let stale = token
            let auth = self.auth
            pending = Task { [weak self] in
                let fresh = try? await auth.session(replacing: stale)
                guard !Task.isCancelled, let self else { return }
                if fresh == nil { self.perform(self.policy.reauthFailed()) } else { self.perform(self.policy.retryDue()) }
            }
        }
    }

    private func connect(_ gen: Int) async {
        // The current session — signing in only if there is none (the
        // workspace's own refresh usually got one first).
        let cred = try? await auth.session(replacing: nil)
        let origin = await auth.origin
        guard gen == generation, !Task.isCancelled else { return }
        guard let cred, let url = EventsRoute.url(on: origin) else {
            perform(policy.reauthFailed())
            return
        }
        token = cred.token
        socket?.close()
        socket = makeSocket(url, cred.token) { [weak self] s in self?.handle(s, gen) }
    }

    private func handle(_ s: EventSocketSignal, _ gen: Int) {
        guard gen == generation else { return }
        switch s {
        case .opened:
            if policy.opened() {
                onResync?()
                for sub in sessionSubs.values { sub.cont.yield(.resync) }
            }
        case .text(let text):
            receive(text)
        case .closed(let why):
            socket = nil
            perform(policy.closed(why, jitter: jitter()))
        }
    }

    // MARK: Routing

    /// One frame (public for tests: the socket calls it).
    func receive(_ text: String) {
        guard let e = AppEvent.parse(text) else { return }
        onEvent?(e)
        switch e {
        case .reload(let component):
            guard let target = ReloadTargets.target(for: component, open: reloadSubs.values.map(\.tile)) else { return }
            for sub in reloadSubs.values where sub.tile == target { sub.cont.yield(()) }
        case .grants(let component?):
            // A grant on this very tile changed: reload it so a page that
            // was refused retries (the web shell does the same).
            for sub in reloadSubs.values where ReloadTargets.trimmed(sub.tile) == ReloadTargets.trimmed(component) {
                sub.cont.yield(())
            }
        case .branding:
            onBranding?()
        case .term(let t):
            if t.isFor(userID: userID()) { onTerm?(t) }
        case .session(let id, _, let frame):
            let subs = sessionSubs.values.filter { $0.id == id }
            guard !subs.isEmpty, let j = try? XbinAgent.JSONValue.parse(frame), let hub = SessionHubEvent(json: j) else { return }
            for sub in subs { sub.cont.yield(.event(hub)) }
        default:
            break
        }
    }
}

extension ReloadTargets {
    /// A tile path without leading or trailing slashes.
    static func trimmed(_ s: String) -> String { s.trimmingCharacters(in: CharacterSet(charactersIn: "/")) }
}

// MARK: - The socket

/// `/ws/events` over URLSessionWebSocketTask: the bearer and the client
/// header on the upgrade, no cookies, a ping every 30 s (a dead network
/// otherwise looks open until TCP gives up). Signals arrive on the main
/// actor, never from inside `init` or `close`.
@MainActor
final class URLSessionEventSocket: EventSocket {
    private var session: URLSession?
    private var task: URLSessionWebSocketTask?
    private var pinger: Task<Void, Never>?
    private var opened = false
    private var finished = false
    private let signal: @MainActor (EventSocketSignal) -> Void

    init(url: URL, bearer: String, clientHeader: String, signal: @escaping @MainActor (EventSocketSignal) -> Void) {
        self.signal = signal
        var req = URLRequest(url: url)
        req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        req.setValue(clientHeader, forHTTPHeaderField: "X-XBin-Client")
        req.timeoutInterval = 20
        let config = URLSessionConfiguration.ephemeral
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.urlCache = nil
        let delegate = EventSocketDelegate()
        let s = URLSession(configuration: config, delegate: delegate, delegateQueue: .main)
        let t = s.webSocketTask(with: req)
        t.maximumMessageSize = 4 << 20
        session = s
        task = t
        delegate.owner = self
        t.resume()
        receive()
    }

    func close() {
        finished = true
        pinger?.cancel()
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
                        self.signal(.text(s))
                        self.receive()
                    case .success(.data(let d)):
                        self.signal(.text(String(decoding: d, as: UTF8.self)))
                        self.receive()
                    case .success:
                        self.receive()
                    case .failure:
                        break // didComplete / didClose says why
                    }
                }
            }
        }
    }

    fileprivate func didOpen() {
        guard !finished else { return }
        opened = true
        signal(.opened)
        pinger = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 30_000_000_000)
                guard !Task.isCancelled, let self, let task = self.task else { return }
                task.sendPing { [weak self] error in
                    guard error != nil else { return }
                    DispatchQueue.main.async { MainActor.assumeIsolated { self?.finish(.dropped) } }
                }
            }
        }
    }

    fileprivate func didClose() { finish(.dropped) }

    fileprivate func didComplete(status: Int?, failed: Bool) {
        if opened { finish(.dropped); return }
        if let status, status >= 300 { finish(.refused(status: status)); return }
        finish(failed ? .unreachable : .dropped)
    }

    private func finish(_ why: EventSocketPolicy.Close) {
        guard !finished else { return }
        finished = true
        pinger?.cancel()
        session?.invalidateAndCancel()
        signal(.closed(why))
    }
}

/// URLSession's delegate for one socket (callbacks on the main queue).
private final class EventSocketDelegate: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    weak var owner: URLSessionEventSocket?

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        MainActor.assumeIsolated { owner?.didOpen() }
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask,
                    didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        MainActor.assumeIsolated { owner?.didClose() }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        let status = (task.response as? HTTPURLResponse)?.statusCode
        let failed = error != nil
        MainActor.assumeIsolated { owner?.didComplete(status: status, failed: failed) }
    }

    /// Never follow a redirect with the bearer: a 3xx is an answer (retried
    /// with backoff), not a place to send the session. (AppTransport's
    /// form; Linux's Foundation has no async variant — there it's unused.)
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        nil
    }
}
