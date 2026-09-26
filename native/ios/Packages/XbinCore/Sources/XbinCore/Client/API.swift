import Foundation

// The app's own HTTP to a workspace (plans/native.md §2 invariant 1: the
// user's session is used by the app's Swift code only). The transport is
// injected — URLSession in the app, a stub in tests — so everything that
// decides *what* to send and *what an answer means* is Foundation-only and
// tested on Linux.

/// One request to a workspace, relative to its origin.
public struct APIRequest: Sendable, Equatable {
    public var method: String
    /// Root-relative and already encoded, query included:
    /// `/api/xbin/frame-token?component=apps%2Fdevbox`.
    public var path: String
    /// Header names as given; ``header(_:)`` looks them up case-insensitively.
    public var headers: [String: String]
    public var body: Data?

    public init(_ method: String, _ path: String, headers: [String: String] = [:], body: Data? = nil) {
        self.method = method.uppercased()
        self.path = path
        self.headers = headers
        self.body = body
    }

    /// A request with a JSON body (`Content-Type: application/json`).
    public static func json(_ method: String, _ path: String, _ body: JSONValue) -> APIRequest {
        APIRequest(method, path, headers: ["Content-Type": "application/json"], body: body.jsonData)
    }

    /// The full URL on `origin` (nil when the path isn't root-relative).
    public func url(on origin: ServerOrigin) -> URL? {
        guard path.hasPrefix("/") else { return nil }
        return origin.url(path: path)
    }

    public func header(_ name: String) -> String? {
        headers.first { $0.key.caseInsensitiveCompare(name) == .orderedSame }?.value
    }

    /// Sets a header, replacing any spelling of the same name.
    public mutating func setHeader(_ name: String, _ value: String?) {
        for k in headers.keys where k.caseInsensitiveCompare(name) == .orderedSame { headers[k] = nil }
        if let value { headers[name] = value }
    }
}

/// An answer, whatever its status.
public struct APIResponse: Sendable, Equatable {
    public var status: Int
    /// Lower-cased names.
    public var headers: [String: String]
    public var body: Data

    public init(status: Int, headers: [String: String] = [:], body: Data = Data()) {
        self.status = status
        var h: [String: String] = [:]
        for (k, v) in headers { h[k.lowercased()] = v }
        self.headers = h
        self.body = body
    }

    public var isSuccess: Bool { (200..<300).contains(status) }
    public func header(_ name: String) -> String? { headers[name.lowercased()] }

    /// The body as JSON (lossless: booleans stay booleans).
    public func json() throws -> JSONValue {
        try JSONValue(parsing: body)
    }
}

/// Sends requests to one workspace's server. The app's implementation uses
/// URLSession with no cookie storage and no cache; redirects are *not*
/// followed (a 3xx is an answer).
public protocol APITransport: Sendable {
    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse
}

/// A non-2xx answer: xbind's `{"error": "…"}` plus the fields the sign-in
/// routes add (`stepUp`, `reauth`, device-login.md).
public struct APIError: Error, Sendable, Equatable, CustomStringConvertible {
    public var status: Int
    public var message: String
    /// `password` | `signin` on `POST /api/xbin/devices/enroll-code`.
    public var stepUp: String?
    /// `sso` on `POST /login/device` in an SSO-only workspace.
    public var reauth: String?
    public var body: JSONValue?

    public init(status: Int, message: String, stepUp: String? = nil, reauth: String? = nil, body: JSONValue? = nil) {
        self.status = status
        self.message = message
        self.stepUp = stepUp
        self.reauth = reauth
        self.body = body
    }

    /// Reads xbind's error body (JSON `{error}` or plain text).
    public init(_ response: APIResponse) {
        let json = try? response.json()
        var msg = json?["error"]?.stringValue ?? ""
        if msg.isEmpty, json == nil {
            msg = String(decoding: response.body.prefix(400), as: UTF8.self)
                .trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if msg.isEmpty { msg = APIError.statusText(response.status) }
        self.init(status: response.status, message: msg, stepUp: json?["stepUp"]?.stringValue,
                  reauth: json?["reauth"]?.stringValue, body: json)
    }

    public var description: String { "\(status): \(message)" }
    public var isUnauthorized: Bool { status == 401 }
    public var isForbidden: Bool { status == 403 }
    public var isNotFound: Bool { status == 404 }
    public var isThrottled: Bool { status == 429 }

    public static func statusText(_ status: Int) -> String {
        switch status {
        case 400: return "bad request"
        case 401: return "signed out"
        case 403: return "not allowed"
        case 404: return "not found"
        case 409: return "conflict"
        case 429: return "too many attempts — wait a little"
        case 500...599: return "the server had a problem (\(status))"
        default: return "HTTP \(status)"
        }
    }
}

/// Percent-encoding for one query value or path segment, as JavaScript's
/// `encodeURIComponent` does it (the web shell builds the same URLs).
public enum URLComponent {
    private static let unreserved: CharacterSet = {
        var s = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
        s.insert(charactersIn: "-_.!~*'()")
        return s
    }()

    public static func encode(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: unreserved) ?? s
    }

    /// A tile path (`apps/devbox`) as it appears after `/c/`: each segment
    /// encoded, slashes kept.
    public static func encodePath(_ path: String) -> String {
        path.split(separator: "/", omittingEmptySubsequences: true).map { encode(String($0)) }.joined(separator: "/")
    }
}
