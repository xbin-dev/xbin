import Foundation
import WebKit
import XbinCore

/// The per-workspace `xbin-ws` scheme handler (plans/native.md §6.1): every
/// request a tile page or runtime document makes — the document, its assets,
/// `fetch`/XHR to `/api/…`, `/vendor/…` — goes to the real server with *that
/// tile's* frame token (never the user's session, §2 invariant 1). Bodies
/// stream through as they arrive (SSE over fetch works); responses pass
/// through unchanged but for cookies, CSP sandbox included, so each page
/// stays an opaque origin. A 401 on a replayable request renews the frame
/// token once (the session behind it was re-signed) and retries.
@MainActor
final class TileSchemeHandler: NSObject, WKURLSchemeHandler {
    private weak var workspace: WorkspaceModel?
    /// Web view → the tile it shows (its frame token signs every request).
    private var tiles: [ObjectIdentifier: String] = [:]
    private var loads: [ObjectIdentifier: StreamingLoad] = [:]
    private var stopped: Set<ObjectIdentifier> = []
    /// Largest request body read from a stream (uploads).
    nonisolated static let maxBody = 64 << 20

    init(workspace: WorkspaceModel) {
        self.workspace = workspace
    }

    func register(_ webView: WKWebView, tile: String) { tiles[ObjectIdentifier(webView)] = tile }
    func unregister(_ webView: WKWebView) { tiles[ObjectIdentifier(webView)] = nil }

    func webView(_ webView: WKWebView, start urlSchemeTask: any WKURLSchemeTask) {
        let key = ObjectIdentifier(urlSchemeTask)
        stopped.remove(key)
        let task = UncheckedTask(task: urlSchemeTask)
        guard let ws = workspace, let tile = tiles[ObjectIdentifier(webView)], let url = urlSchemeTask.request.url,
              let path = TileScheme.serverPath(for: url, workspace: ws.id) else {
            fail(task, key, status: 404, message: "not a page of this workspace")
            return
        }
        let request = urlSchemeTask.request
        Task {
            let body = await Self.body(of: request)
            await self.load(task, key, request: request, body: body, path: path, tile: tile, in: ws, attempt: 0)
        }
    }

    func webView(_ webView: WKWebView, stop urlSchemeTask: any WKURLSchemeTask) {
        let key = ObjectIdentifier(urlSchemeTask)
        stopped.insert(key)
        loads.removeValue(forKey: key)?.cancel()
    }

    private func load(_ task: UncheckedTask, _ key: ObjectIdentifier, request: URLRequest, body: Data?, path: String,
                      tile: String, in ws: WorkspaceModel, attempt: Int) async {
        let token: String
        do {
            token = attempt == 0 ? try await ws.frameTokens.token(for: tile) : try await ws.frameTokens.renew(tile)
        } catch {
            fail(task, key, status: 401, message: ws.describe(error))
            return
        }
        guard !stopped.contains(key), let url = ws.origin.url(path: path) else { return }
        var out = URLRequest(url: url)
        out.httpMethod = request.httpMethod ?? "GET"
        out.httpBody = body
        out.timeoutInterval = 3600 // streams (SSE, long polls) stay open
        let pageHeaders = request.allHTTPHeaderFields ?? [:]
        for (k, v) in TileScheme.forwardHeaders(pageHeaders, frameToken: token, client: AppInfo.clientHeader) {
            out.setValue(v, forHTTPHeaderField: k)
        }
        let method = out.httpMethod ?? "GET"
        let origin = ws.origin
        let retry = attempt == 0 && TileScheme.isReplayable(method: method)
        let pageURL = request.url
        let load = AppTransport.shared.stream(out, allowRedirect: { TileScheme.allowsRedirect(to: $0, origin: origin) },
            onResponse: { [weak self] h in
                let status = h.statusCode
                let headers = TileScheme.pageResponseHeaders(AppTransport.headers(h))
                DispatchQueue.main.async {
                    MainActor.assumeIsolated {
                        guard let self, !self.stopped.contains(key) else { return }
                        if status == 401, retry {
                            // The frame token died with its session: renew it and ask again.
                            self.loads.removeValue(forKey: key)?.cancel()
                            Task { await ws.frameTokens.invalidate(tile, token: token) }
                            Task { await self.load(task, key, request: request, body: body, path: path, tile: tile, in: ws, attempt: 1) }
                            return
                        }
                        guard let pageURL,
                              let resp = HTTPURLResponse(url: pageURL, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)
                        else { return }
                        task.task.didReceive(resp)
                    }
                }
            },
            onData: { [weak self] data in
                DispatchQueue.main.async {
                    MainActor.assumeIsolated {
                        guard let self, !self.stopped.contains(key), self.loads[key] != nil else { return }
                        task.task.didReceive(data)
                    }
                }
            },
            onDone: { [weak self] error in
                let failure = error.map { ($0 as NSError).localizedDescription }
                DispatchQueue.main.async {
                    MainActor.assumeIsolated {
                        guard let self, !self.stopped.contains(key), self.loads.removeValue(forKey: key) != nil else { return }
                        if let failure {
                            task.task.didFailWithError(URLError(.networkConnectionLost, userInfo: [NSLocalizedDescriptionKey: failure]))
                        } else {
                            task.task.didFinish()
                        }
                    }
                }
            })
        loads[key] = load
    }

    private func fail(_ task: UncheckedTask, _ key: ObjectIdentifier, status: Int, message: String) {
        guard !stopped.contains(key), let url = task.task.request.url else { return }
        let body = Data("<!doctype html><meta name=viewport content='width=device-width'><title>\(status)</title>\(message.htmlEscaped)".utf8)
        let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                                   headerFields: ["Content-Type": "text/html; charset=utf-8",
                                                  "Content-Security-Policy": "sandbox"])!
        task.task.didReceive(resp)
        task.task.didReceive(body)
        task.task.didFinish()
        stopped.insert(key)
    }

    /// The request body: in memory, or read from WebKit's stream (bounded).
    nonisolated static func body(of request: URLRequest) async -> Data? {
        if let b = request.httpBody { return b }
        guard let stream = request.httpBodyStream else { return nil }
        let box = UncheckedStream(stream: stream)
        return await Task.detached {
            let s = box.stream
            s.open()
            defer { s.close() }
            var out = Data()
            var buf = [UInt8](repeating: 0, count: 65536)
            while out.count < maxBody {
                let n = s.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                out.append(buf, count: n)
            }
            return out
        }.value
    }
}

/// WebKit's scheme task, carried into Sendable closures (only ever touched
/// on the main thread).
struct UncheckedTask: @unchecked Sendable {
    let task: any WKURLSchemeTask
}

private struct UncheckedStream: @unchecked Sendable {
    let stream: InputStream
}

extension String {
    var htmlEscaped: String {
        replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;").replacingOccurrences(of: "\"", with: "&quot;")
    }
}
