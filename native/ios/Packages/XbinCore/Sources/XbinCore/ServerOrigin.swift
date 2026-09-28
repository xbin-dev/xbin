import Foundation

/// An xbind server's origin — `scheme://host[:port]`, serialized as the web
/// does (RFC 6454): lowercase scheme and host, the default port (443 for
/// https, 80 for http) omitted, IPv6 hosts in brackets, no path, no
/// trailing slash. This exact string is what the device-login message signs
/// (``DeviceLogin/message(origin:deviceId:nonce:)``), so the server must
/// serialize its own origin the same way.
public struct ServerOrigin: Sendable, Hashable, Codable, CustomStringConvertible {
    /// `http` or `https`.
    public let scheme: String
    /// Lowercase; an IPv6 address without brackets.
    public let host: String
    /// Nil when it is the scheme's default.
    public let port: Int?

    /// Why a server address was refused.
    public struct Invalid: Error, Equatable, Sendable, CustomStringConvertible {
        public var reason: String
        public var description: String { "invalid server address: \(reason)" }
    }

    public init(scheme: String, host: String, port: Int? = nil) throws {
        let s = scheme.lowercased()
        guard s == "https" || s == "http" else { throw Invalid(reason: "scheme must be https or http") }
        var h = host.lowercased()
        if h.hasPrefix("["), h.hasSuffix("]") { h = String(h.dropFirst().dropLast()) }
        guard !h.isEmpty else { throw Invalid(reason: "no host") }
        guard !h.contains(where: { $0 == "/" || $0 == "@" || $0 == "?" || $0 == "#" || $0 == " " }) else {
            throw Invalid(reason: "bad host")
        }
        if let p = port, !(1...65535).contains(p) { throw Invalid(reason: "port out of range") }
        self.scheme = s
        self.host = h
        self.port = port == Self.defaultPort(s) ? nil : port
    }

    /// Parses an absolute `http(s)://host[:port][/]` URL. A path, query or
    /// fragment is refused (xbind lives at the root of its origin), and so
    /// are credentials (`https://good@evil`).
    public init(string: String) throws {
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let c = URLComponents(string: trimmed), let scheme = c.scheme else {
            throw Invalid(reason: "not an absolute URL")
        }
        guard c.user == nil, c.password == nil else { throw Invalid(reason: "credentials in the URL") }
        guard c.percentEncodedPath.isEmpty || c.percentEncodedPath == "/" else {
            throw Invalid(reason: "a path (xbind is served at the root)")
        }
        guard c.percentEncodedQuery == nil, c.percentEncodedFragment == nil else {
            throw Invalid(reason: "a query or fragment")
        }
        guard let host = c.host, !host.isEmpty else { throw Invalid(reason: "no host") }
        try self.init(scheme: scheme, host: host, port: c.port)
    }

    /// Parses what a person types into "add a workspace": a bare
    /// `host[:port]` means https; a trailing path is dropped.
    public init(userInput: String) throws {
        var s = userInput.trimmingCharacters(in: .whitespacesAndNewlines)
        if let sep = s.range(of: "://") {
            let scheme = s[..<sep.lowerBound].lowercased()
            guard scheme == "http" || scheme == "https" else { throw Invalid(reason: "scheme must be https or http") }
        } else {
            s = "https://" + s
        }
        guard var c = URLComponents(string: s) else { throw Invalid(reason: "not a URL") }
        c.percentEncodedPath = ""
        c.percentEncodedQuery = nil
        c.percentEncodedFragment = nil
        guard let str = c.string else { throw Invalid(reason: "not a URL") }
        try self.init(string: str)
    }

    static func defaultPort(_ scheme: String) -> Int { scheme == "https" ? 443 : 80 }

    /// The origin string, e.g. `https://xbin.example.com` or
    /// `http://[::1]:8080`.
    public var origin: String {
        var h = host
        if h.contains(":") { h = "[\(h)]" }
        if let port { return "\(scheme)://\(h):\(port)" }
        return "\(scheme)://\(h)"
    }

    /// `host[:port]` — how a deep link may name this server.
    public var authority: String {
        var h = host
        if h.contains(":") { h = "[\(h)]" }
        if let port { return "\(h):\(port)" }
        return h
    }

    /// The origin as a URL (`https://host/`-less form).
    public var url: URL { URL(string: origin)! }

    /// `origin` + `path` (which should start with `/`).
    public func url(path: String) -> URL? { URL(string: origin + path) }

    /// The WebSocket origin (`wss://…` for https, `ws://…` for http).
    public var webSocketOrigin: String {
        (scheme == "https" ? "wss" : "ws") + origin.dropFirst(scheme.count)
    }

    public var isSecure: Bool { scheme == "https" }

    /// Whether what the app sends here is encrypted on the way even over
    /// plain http: https itself, loopback (it never leaves the device), or
    /// a Tailscale address — 100.64.0.0/10, fd7a:115c:a1e0::/48 or a
    /// `*.ts.net` name — whose WireGuard tunnel encrypts it. Anything else
    /// over http (a LAN address, an unqualified name) may travel in the
    /// clear, and the sign-in page says so.
    public var isEncryptedInTransit: Bool {
        if isSecure { return true }
        let h = host.lowercased()
        if h == "localhost" || h == "::1" || h.hasPrefix("127.") { return true }
        if h.hasSuffix(".ts.net") { return true }
        if h.hasPrefix("fd7a:115c:a1e0:") { return true }
        let parts = h.split(separator: ".").compactMap { Int($0) }
        if parts.count == 4, h.split(separator: ".").count == 4, parts[0] == 100, (64...127).contains(parts[1]) { return true }
        return false
    }

    public var description: String { origin }

    public init(from decoder: Decoder) throws {
        try self.init(string: decoder.singleValueContainer().decode(String.self))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(origin)
    }
}
