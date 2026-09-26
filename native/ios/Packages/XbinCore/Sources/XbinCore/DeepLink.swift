import Foundation

/// An `xbin://` link (plans/native.md §4, §5). The custom scheme is the
/// contract — universal links can't cover self-hosted domains.
///
/// | Link | Case |
/// |---|---|
/// | `xbin://<ws>` | ``workspace(_:)`` |
/// | `xbin://<ws>/c/<tile>[#fragment]` | ``tile(workspace:tile:fragment:)`` |
/// | `xbin://<ws>/term/<session>` | ``terminal(workspace:session:)`` |
/// | `xbin://<ws>/agent/<session>` | ``agent(workspace:session:)`` |
/// | `xbin://enroll?u=<server>&c=<code>` | ``enroll(server:code:)`` |
/// | `xbin://sso?ticket=<ticket>` | ``sso(ticket:)`` |
/// | `xbin://sso?error=<code>` | ``ssoError(code:)`` |
///
/// `<ws>` names a workspace the app knows: its record id, or its server's
/// `host[:port]` (``WorkspaceRecord/matches(linkWorkspace:)``); it is
/// lowercased. `enroll` and `sso` are reserved and never name a workspace.
/// A tile id keeps its slashes (`apps/devbox`); the fragment is kept
/// percent-encoded, exactly as it goes onto the tile page's URL.
public enum DeepLink: Sendable, Hashable {
    case workspace(String)
    case tile(workspace: String, tile: String, fragment: String?)
    case terminal(workspace: String, session: String)
    case agent(workspace: String, session: String)
    /// A device enrollment QR code from a signed-in browser (§5).
    case enroll(server: ServerOrigin, code: String)
    /// The end of an SSO sign-in (`ASWebAuthenticationSession` callback):
    /// a one-shot ticket (2 minutes), redeemed with the app's PKCE verifier
    /// (``TicketRedeemRequest``).
    case sso(ticket: String)
    /// An SSO sign-in that failed: `denied`, `failed`, `noaccount`,
    /// `disabled`, … (the codes the web login page explains).
    case ssoError(code: String)

    public static let scheme = "xbin"
    /// Hosts that are link kinds, not workspaces.
    public static let reservedHosts: Set<String> = ["enroll", "sso"]

    /// Why a link was refused.
    public struct Invalid: Error, Equatable, Sendable, CustomStringConvertible {
        public var reason: String
        public var description: String { "invalid xbin link: \(reason)" }
    }

    /// The workspace the link targets, when it targets one.
    public var workspace: String? {
        switch self {
        case .workspace(let w), .tile(let w, _, _), .terminal(let w, _), .agent(let w, _): return w
        case .enroll, .sso, .ssoError: return nil
        }
    }

    public init(url: URL) throws {
        try self.init(string: url.absoluteString)
    }

    public init(string: String) throws {
        guard let c = URLComponents(string: string) else { throw Invalid(reason: "not a URL") }
        guard c.scheme?.lowercased() == Self.scheme else { throw Invalid(reason: "not an xbin:// link") }
        guard let rawHost = c.host, !rawHost.isEmpty else { throw Invalid(reason: "no workspace") }
        let host = rawHost.lowercased()
        let query = Self.queryDictionary(c)

        switch host {
        case "enroll":
            guard let u = query["u"], !u.isEmpty else { throw Invalid(reason: "enroll: u (the server) missing") }
            guard let code = query["c"], !code.isEmpty else { throw Invalid(reason: "enroll: c (the code) missing") }
            let server: ServerOrigin
            do { server = try ServerOrigin(string: u) } catch { throw Invalid(reason: "enroll: \(error)") }
            self = .enroll(server: server, code: code)
            return
        case "sso":
            if let t = query["ticket"], !t.isEmpty {
                self = .sso(ticket: t)
            } else if let e = query["error"], !e.isEmpty {
                self = .ssoError(code: e)
            } else {
                throw Invalid(reason: "sso: ticket missing")
            }
            return
        default:
            break
        }

        var ws = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
        if let p = c.port { ws += ":\(p)" }
        let segments = try Self.pathSegments(c)
        guard let kind = segments.first else {
            self = .workspace(ws)
            return
        }
        let rest = Array(segments.dropFirst())
        switch kind {
        case "c":
            guard !rest.isEmpty else { throw Invalid(reason: "no tile") }
            let frag = c.percentEncodedFragment.flatMap { $0.isEmpty ? nil : $0 }
            self = .tile(workspace: ws, tile: rest.joined(separator: "/"), fragment: frag)
        case "term", "agent":
            guard rest.count == 1 else { throw Invalid(reason: "\(kind): expected one session id") }
            self = kind == "term" ? .terminal(workspace: ws, session: rest[0]) : .agent(workspace: ws, session: rest[0])
        default:
            throw Invalid(reason: "unknown link kind \(kind)")
        }
    }

    /// Decoded, non-empty path segments; a trailing slash is ignored, an
    /// empty segment in the middle is refused.
    static func pathSegments(_ c: URLComponents) throws -> [String] {
        var raw = c.percentEncodedPath
        if raw.hasPrefix("/") { raw.removeFirst() }
        if raw.hasSuffix("/") { raw.removeLast() }
        if raw.isEmpty { return [] }
        return try raw.split(separator: "/", omittingEmptySubsequences: false).map { seg in
            guard !seg.isEmpty, let s = String(seg).removingPercentEncoding, !s.isEmpty, !s.contains("/") else {
                throw Invalid(reason: "bad path segment")
            }
            guard s != ".", s != ".." else { throw Invalid(reason: "dot segment") }
            return s
        }
    }

    static func queryDictionary(_ c: URLComponents) -> [String: String] {
        var out: [String: String] = [:]
        for item in c.queryItems ?? [] where out[item.name] == nil { out[item.name] = item.value ?? "" }
        return out
    }

    // MARK: - Building

    /// The link as a string (the inverse of ``init(string:)``).
    public var string: String {
        switch self {
        case .workspace(let w):
            return "xbin://\(w)"
        case .tile(let w, let tile, let frag):
            let path = tile.split(separator: "/").map { Self.encodeSegment(String($0)) }.joined(separator: "/")
            return "xbin://\(w)/c/\(path)" + (frag.map { "#\($0)" } ?? "")
        case .terminal(let w, let s):
            return "xbin://\(w)/term/\(Self.encodeSegment(s))"
        case .agent(let w, let s):
            return "xbin://\(w)/agent/\(Self.encodeSegment(s))"
        case .enroll(let server, let code):
            return "xbin://enroll?u=\(Self.encodeQueryValue(server.origin))&c=\(Self.encodeQueryValue(code))"
        case .sso(let t):
            return "xbin://sso?ticket=\(Self.encodeQueryValue(t))"
        case .ssoError(let code):
            return "xbin://sso?error=\(Self.encodeQueryValue(code))"
        }
    }

    public var url: URL { URL(string: string)! }

    private static let unreserved = CharacterSet(charactersIn:
        "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~")
    private static let segmentAllowed = unreserved.union(CharacterSet(charactersIn: "!$&'()*+,;=:@"))

    static func encodeSegment(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: segmentAllowed) ?? s
    }

    static func encodeQueryValue(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: unreserved) ?? s
    }
}
