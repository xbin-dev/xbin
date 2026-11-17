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
/// | `xbin://<ws>/xbin/<page>` | ``page(workspace:page:)`` |
/// | `xbin://<ws>/xbin/agent#r=<id>` (`cs=`, `ts=`, `c=`, `needs`, `first`) | ``workspaceAgent(workspace:route:)`` (D241) |
/// | `xbin://enroll?u=<server>&c=<code>` | ``enroll(server:code:)`` |
/// | `xbin://enroll?u&c&tv=1&t&k&p[&r][&rq=1]` | ``tunnelEnroll(_:)`` (``TunnelLink``) |
/// | `xbin://sso?ticket=<ticket>` | ``sso(ticket:)`` |
/// | `xbin://sso?error=<code>` | ``ssoError(code:)`` |
///
/// `<ws>` names a workspace the app knows: its record id, or its server's
/// `host[:port]` (``WorkspaceRecord/matches(linkWorkspace:)``); it is
/// lowercased. `enroll` and `sso` are reserved and never name a workspace.
/// A tile id keeps its slashes (`apps/devbox`); the fragment is kept
/// percent-encoded, exactly as it goes onto the tile page's URL. An
/// `xbin/<page>` names one of xbind's own pages (``XbindPage``); a page this
/// app doesn't know is refused, so a push linking one opens the workspace
/// (``PushPayload/deepLink(appWorkspace:)``), as an older app does.
public enum DeepLink: Sendable, Hashable {
    case workspace(String)
    case tile(workspace: String, tile: String, fragment: String?)
    case terminal(workspace: String, session: String)
    case agent(workspace: String, session: String)
    /// One of xbind's own pages (D181): the app opens it in a web view of
    /// its own, signed in, once the workspace says it serves it.
    case page(workspace: String, page: XbindPage)
    /// The person's workspace agent, on one thing (D241; ``AgentRoute``):
    /// xbind's page link with a fragment — an app before it opens the agent
    /// (`xbin/agent`), an older one the workspace.
    case workspaceAgent(workspace: String, route: AgentRoute)
    /// A device enrollment QR code from a signed-in browser (§5).
    case enroll(server: ServerOrigin, code: String)
    /// A device pairing through the workspace's app tunnel (D204,
    /// native/spec/tunnel.md §2): an enrollment link with the tunnel's
    /// parameters, all of them well-formed or the link is refused.
    case tunnelEnroll(TunnelLink)
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
        /// A tunnel link's refusal, as the person is told it (an update, or
        /// a damaged link).
        public var tunnel: TunnelLink.Invalid? = nil
        public var description: String { "invalid xbin link: \(reason)" }
    }

    /// The workspace the link targets, when it targets one.
    public var workspace: String? {
        switch self {
        case .workspace(let w), .tile(let w, _, _), .terminal(let w, _), .agent(let w, _), .page(let w, _), .workspaceAgent(let w, _): return w
        case .enroll, .tunnelEnroll, .sso, .ssoError: return nil
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
            if TunnelLink.isTunnel(query: query) {
                do {
                    self = .tunnelEnroll(try TunnelLink(server: server, code: code, query: query))
                } catch let e as TunnelLink.Invalid {
                    throw Invalid(reason: "enroll: \(e)", tunnel: e)
                }
                return
            }
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
        case "xbin":
            guard rest.count == 1 else { throw Invalid(reason: "xbin: expected one page") }
            guard let page = XbindPage(linkName: rest[0]) else { throw Invalid(reason: "unknown xbind page \(rest[0])") }
            if page == .agent, let route = AgentRoute(fragment: c.fragment ?? "") {
                self = .workspaceAgent(workspace: ws, route: route)
                return
            }
            self = .page(workspace: ws, page: page)
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
        case .page(let w, let page):
            return "xbin://\(w)/\(page.link)"
        case .workspaceAgent(let w, let route):
            return "xbin://\(w)/\(XbindPage.agent.link)#\(route.fragment)"
        case .enroll(let server, let code):
            return "xbin://enroll?u=\(Self.encodeQueryValue(server.origin))&c=\(Self.encodeQueryValue(code))"
        case .tunnelEnroll(let link):
            return link.string
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

/// What a link to the person's workspace agent opens (D241;
/// plans/wsagent/ios-stage2.md §7): one router for the notifications, the
/// Live Activity, Home's row and Handoff. The fragment of xbind's page link
/// `xbin/agent#…`; ids are xbind's shapes (``DelegateID``), anything else is
/// no route (the agent itself).
public enum AgentRoute: Sendable, Hashable {
    /// One request's approval sheet.
    case request(String)
    /// One change set's (a plan's, admin changes') review.
    case changeset(String)
    /// A tile agent's session: the app's agent screen.
    case tileSession(String)
    /// One of the agent's conversations (its run id).
    case conversation(String)
    /// Needs you.
    case needs
    /// What waits first (oldest), else the agent.
    case first

    public init?(fragment raw: String) {
        let f = raw.hasPrefix("#") ? String(raw.dropFirst()) : raw
        switch f {
        case "needs": self = .needs; return
        case "first": self = .first; return
        default: break
        }
        guard let eq = f.firstIndex(of: "=") else { return nil }
        let key = String(f[..<eq]), value = String(f[f.index(after: eq)...])
        switch key {
        case "r" where DelegateID.isRequest(value): self = .request(value)
        case "cs" where DelegateID.isRequest(value): self = .changeset(value)
        case "ts" where DelegateID.isSession(value) && value != "new": self = .tileSession(value)
        case "c" where Self.isConversation(value): self = .conversation(value)
        default: return nil
        }
    }

    /// A conversation id as the agent's backend has them: a run number.
    public static func isConversation(_ s: String) -> Bool {
        (1...18).contains(s.count) && s.allSatisfy { $0.isASCII && $0.isNumber }
    }

    public var fragment: String {
        switch self {
        case .request(let id): return "r=" + id
        case .changeset(let id): return "cs=" + id
        case .tileSession(let id): return "ts=" + id
        case .conversation(let id): return "c=" + id
        case .needs: return "needs"
        case .first: return "first"
        }
    }

    /// The route a push's workspace-relative link names (`xbin/agent#…`),
    /// if any.
    public static func of(pushLink link: String) -> AgentRoute? {
        let parts = link.split(separator: "#", maxSplits: 1, omittingEmptySubsequences: false)
        guard parts.count == 2, parts[0] == Substring(XbindPage.agent.link) else { return nil }
        return AgentRoute(fragment: String(parts[1]))
    }
}
