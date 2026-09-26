import Foundation

// Loading tiles in the app (plans/native.md §6.1, §7.2): every tile page and
// runtime document is loaded as `xbin-ws://<workspace>/c/<tile>/…` through a
// per-workspace WKURLSchemeHandler that forwards each request to the server
// with *that tile's* frame token. These are the rules that handler follows;
// the handler itself (WebKit + URLSession) is app code.

/// How a tile opens.
public enum TileSurface: Sendable, Equatable {
    /// A WKWebView of its page (the default for every tile).
    case web
    /// Its native UI (§7): a hidden runtime document + the SwiftUI renderer.
    case native
    /// Trusted chrome acts as the human and can't run under a frame token:
    /// Safari, signed in by ticket (§6.3).
    case safari

    /// `forceWeb`: the user chose "open as web page" for this tile, or the
    /// native view failed this session; `runtimeOff`: the remote kill switch
    /// or an xbind without runtime documents.
    public static func pick(_ tile: TileInfo, serverRuntime: Int?, forceWeb: Bool = false,
                            runtimeOff: Bool = false) -> TileSurface {
        if tile.chrome { return .safari }
        guard tile.opensNatively, let v = serverRuntime, v >= 1, !forceWeb, !runtimeOff else { return .web }
        return .native
    }
}

public enum TileScheme {
    /// The custom scheme. WebKit hands every request on it to the app.
    public static let scheme = "xbin-ws"
    /// The frame-token header xbind reads (never forwarded to backends).
    public static let frameTokenHeader = "X-XBin-Frame-Token"
    /// The header that tells xbind a request comes from the app (it then
    /// injects `<meta name="xbin-ws-origin">` into tile HTML).
    public static let clientHeader = "X-XBin-Client"

    /// `app/<version>` — the value of ``clientHeader``.
    public static func clientValue(version: String) -> String { "app/\(version)" }

    /// A tile page: `xbin-ws://<workspace>/c/<tile>/[sub][?query][#fragment]`.
    /// The trailing slash matters: relative URLs resolve against it and
    /// xbind would otherwise answer with a redirect WebKit can't see.
    public static func pageURL(workspace: String, tile: String, subpath: String = "", query: String? = nil,
                               fragment: String? = nil) -> URL? {
        var s = "\(scheme)://\(workspace.lowercased())/c/\(URLComponent.encodePath(tile))/"
        let sub = WindowSpec.stripTraversal(subpath)
        if !sub.isEmpty { s += URLComponent.encodePath(sub) + (subpath.hasSuffix("/") ? "/" : "") }
        if let q = query, !q.isEmpty { s += "?" + q }
        if let f = fragment, !f.isEmpty { s += "#" + f }
        return URL(string: s)
    }

    /// The native runtime document (§7.2): `/c/<tile>/?native=1`.
    public static func runtimeURL(workspace: String, tile: String) -> URL? {
        pageURL(workspace: workspace, tile: tile, query: "native=1")
    }

    /// The server path (+ query) a scheme URL asks for, or nil when the URL
    /// isn't this workspace's (another host) or isn't the scheme at all.
    public static func serverPath(for url: URL, workspace: String) -> String? {
        guard url.scheme?.lowercased() == scheme, url.host?.lowercased() == workspace.lowercased() else { return nil }
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
        var path = c.percentEncodedPath
        if path.isEmpty { path = "/" }
        if let q = c.percentEncodedQuery { path += "?" + q }
        return path
    }

    /// The tile a document path belongs to, when it is one (`/c/<tile>/…`),
    /// given the tiles the catalog knows (the longest match wins, as xbind's
    /// own resolver does).
    public static func tile(forPath path: String, known: [String]) -> String? {
        guard path.hasPrefix("/c/") else { return nil }
        let rest = String(path.dropFirst(3)).split(separator: "?", maxSplits: 1).first.map(String.init) ?? ""
        let decoded = rest.removingPercentEncoding ?? rest
        return known.filter { decoded == $0 || decoded.hasPrefix($0 + "/") }.max { $0.count < $1.count }
    }

    /// Request headers going to the server: the page's own, minus what the
    /// app must control (cookies, hop-by-hop, any frame token the page set),
    /// plus the tile's frame token and the client header.
    public static func forwardHeaders(_ page: [String: String], frameToken: String?, client: String) -> [String: String] {
        let drop: Set<String> = ["cookie", "cookie2", "host", "connection", "keep-alive", "proxy-connection",
                                 "transfer-encoding", "upgrade", "te", "content-length", "accept-encoding",
                                 frameTokenHeader.lowercased(), clientHeader.lowercased()]
        var out: [String: String] = [:]
        for (k, v) in page where !drop.contains(k.lowercased()) { out[k] = v }
        if let frameToken { out[frameTokenHeader] = frameToken }
        out[clientHeader] = client
        return out
    }

    /// Response headers going to WebKit: the server's, unchanged (CSP
    /// sandbox included — the page stays an opaque origin) except cookies
    /// (tiles hold none) and what URLSession already undid (encoding,
    /// length after decoding, chunking).
    public static func pageResponseHeaders(_ server: [String: String]) -> [String: String] {
        let drop: Set<String> = ["set-cookie", "set-cookie2", "content-encoding", "content-length", "transfer-encoding",
                                 "connection", "keep-alive"]
        var out: [String: String] = [:]
        for (k, v) in server where !drop.contains(k.lowercased()) { out[k] = v }
        return out
    }

    /// Whether a redirect the server answered may be followed with the
    /// tile's frame token: only on the workspace's own origin.
    public static func allowsRedirect(to url: URL, origin: ServerOrigin) -> Bool {
        guard let s = url.scheme?.lowercased(), let h = url.host?.lowercased() else { return false }
        let port = url.port ?? (s == "https" ? 443 : s == "http" ? 80 : -1)
        let own = origin.port ?? (origin.scheme == "https" ? 443 : 80)
        return s == origin.scheme && h == origin.host.lowercased() && port == own
    }

    /// Methods whose request can be sent a second time after a frame token
    /// renewal (the body is in memory; nothing changed server-side).
    public static func isReplayable(method: String) -> Bool {
        ["GET", "HEAD", "OPTIONS"].contains(method.uppercased())
    }
}

/// The frame tokens of one workspace's open tiles: minted with the user's
/// session (`GET /api/xbin/frame-token?component=`), reused until they are
/// ten minutes old (xbin-client's renewal interval; they live 15), minted
/// again on demand after a 401. One mint per tile at a time.
public actor FrameTokenCache {
    public static let renewAfter: TimeInterval = 10 * 60

    public typealias Mint = @Sendable (_ component: String) async throws -> String

    private struct Entry { var token: String; var minted: Date }
    private var entries: [String: Entry] = [:]
    private var inflight: [String: Task<String, any Error>] = [:]
    private let mint: Mint
    private let now: @Sendable () -> Date

    public init(now: @escaping @Sendable () -> Date = Date.init, mint: @escaping Mint) {
        self.mint = mint
        self.now = now
    }

    /// A token for `component`, fresh enough to use now.
    public func token(for component: String) async throws -> String {
        if let e = entries[component], now().timeIntervalSince(e.minted) < Self.renewAfter { return e.token }
        return try await renew(component)
    }

    /// The cached token without minting (nil when none or stale).
    public func cached(_ component: String) -> String? {
        guard let e = entries[component], now().timeIntervalSince(e.minted) < Self.renewAfter else { return nil }
        return e.token
    }

    /// Mints a new token (joins a mint already running for the tile).
    @discardableResult
    public func renew(_ component: String) async throws -> String {
        if let t = inflight[component] { return try await t.value }
        let mint = self.mint
        let t = Task { try await mint(component) }
        inflight[component] = t
        defer { inflight[component] = nil }
        let token = try await t.value
        entries[component] = Entry(token: token, minted: now())
        return token
    }

    /// A request with `token` was refused: drop it (unless a newer one
    /// already replaced it) so the next use mints again.
    public func invalidate(_ component: String, token: String? = nil) {
        if let token, entries[component]?.token != token { return }
        entries[component] = nil
    }

    public func clear() { entries = [:] }
}

/// `GET /api/xbin/frame-token?component=<p>` → `{token}`.
public enum FrameTokenRoute {
    public static func path(component: String) -> String {
        "/api/xbin/frame-token?component=\(URLComponent.encode(component))"
    }

    public static func token(from json: JSONValue) -> String? {
        guard let t = json["token"]?.stringValue, !t.isEmpty else { return nil }
        return t
    }
}
