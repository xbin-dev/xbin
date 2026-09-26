import Foundation

// Leaving the app for the browser, and bringing another device in:
//
// - **Signed-in Safari** (plans/native.md §6.3, the D64 pattern): the app
//   asks its workspace for a one-shot ticket bound to its device session,
//   `POST /api/xbin/web-ticket {next}` → `{url}`, and opens that URL in
//   `SFSafariViewController`. xbind spends the ticket on the GET but never
//   signs a browser in on a GET (anyone can mint a link for their own
//   account and hand it over): a browser already signed in as the same
//   person lands on `next`; a signed-out one gets a "Continue as <name>"
//   page naming the account (and a one-shot nonce cookie), whose button
//   posts `POST /login/web-ticket {confirm}` → a cookie session and a 303
//   to `next`. So the user taps Continue once. An xbind without the route
//   (404/405), or one that refuses (a token or password session), gets the
//   plain URL: the user signs in there as before.
// - **Handoff** ("open on desktop"): the activity a window advertises names
//   its tile's web page (`webpageURL`, which a Mac or iPad without the app
//   opens in the browser) and the `xbin://` link another device's app
//   resolves by the server's `host[:port]` (workspace ids are per install).
// - **Add another device** (device-login.md §3): the signed-in app mints an
//   enrollment code, `POST /api/xbin/devices/enroll-code`, and shows its
//   `xbin://enroll` link as a QR code for the new device to scan. A session
//   older than the step-up window gets 403 `{stepUp}`: `signin` → sign in
//   again with the device key (a fresh device login counts) and retry;
//   `password` → ask for the password and send it.

public enum WebTicket {
    public static let path = "/api/xbin/web-ticket"

    /// The page to land on must be a same-origin path: `/…`, never `//host`
    /// or a scheme. Anything else becomes `/`.
    public static func cleanNext(_ next: String) -> String {
        let n = next.trimmingCharacters(in: .whitespacesAndNewlines)
        guard n.hasPrefix("/"), !n.hasPrefix("//"), !n.contains("\\"),
              !n.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7F }) else { return "/" }
        return n
    }

    /// `POST /api/xbin/web-ticket {next}` (with the workspace session).
    public static func request(next: String) -> APIRequest {
        .json("POST", path, ["next": .string(cleanNext(next))])
    }

    /// Where Safari goes: the ticket URL when the workspace issued one on its
    /// own origin, else the plain page (`fellBack` true).
    public struct Destination: Sendable, Equatable {
        public var url: URL
        /// The ticket wasn't available (older xbind, refused, bad answer):
        /// the browser may ask the user to sign in.
        public var fellBack: Bool
    }

    /// Reads the answer. `response` nil = the request failed outright.
    /// A ticket URL must be on the workspace's own origin (relative, or
    /// absolute with the same scheme, host and port); `{ticket}` without a
    /// `url` is redeemed at `/login?ticket=…&next=…`.
    public static func destination(_ response: APIResponse?, origin: ServerOrigin, next: String) -> Destination? {
        let path = cleanNext(next)
        guard let plain = origin.url(path: path) else { return nil }
        let fallback = Destination(url: plain, fellBack: true)
        guard let r = response, r.isSuccess, let j = try? r.json() else { return fallback }
        if let u = j["url"]?.stringValue, !u.isEmpty {
            guard let url = sameOrigin(u, origin) else { return fallback }
            return Destination(url: url, fellBack: false)
        }
        if let t = j["ticket"]?.stringValue, !t.isEmpty,
           let url = origin.url(path: "/login?ticket=\(URLComponent.encode(t))&next=\(URLComponent.encode(path))") {
            return Destination(url: url, fellBack: false)
        }
        return fallback
    }

    static func sameOrigin(_ s: String, _ origin: ServerOrigin) -> URL? {
        if s.hasPrefix("/") {
            guard !s.hasPrefix("//") else { return nil }
            return origin.url(path: s)
        }
        guard let c = URLComponents(string: s), let scheme = c.scheme, let host = c.host,
              c.user == nil, c.password == nil,
              let o = try? ServerOrigin(scheme: scheme, host: host, port: c.port), o == origin else { return nil }
        return URL(string: s)
    }
}

/// What a window advertises for Handoff.
public enum HandoffLink {
    /// The `NSUserActivity` type (Info.plist `NSUserActivityTypes`).
    public static let activityType = "dev.xbin.app.view"
    /// The `userInfo` key holding the `xbin://` link.
    public static let linkKey = "link"

    /// A tile's page in the browser: `/c/<tile>/[sub][#fragment]`.
    public static func tilePage(origin: ServerOrigin, tile: String, subpath: String = "", fragment: String? = nil) -> URL? {
        var p = "/c/\(URLComponent.encodePath(tile))/"
        let sub = WindowSpec.stripTraversal(subpath)
        if !sub.isEmpty { p += URLComponent.encodePath(sub) + (subpath.hasSuffix("/") ? "/" : "") }
        if let f = fragment, !f.isEmpty { p += "#" + f }
        return origin.url(path: p)
    }

    /// The workspace's shell (terminal and agent sessions live in its windows).
    public static func shell(origin: ServerOrigin) -> URL? { origin.url(path: "/") }

    /// A link for another device: the workspace named by `host[:port]`.
    public static func tile(origin: ServerOrigin, tile: String, fragment: String? = nil) -> DeepLink {
        .tile(workspace: origin.authority, tile: tile, fragment: fragment)
    }

    public static func agent(origin: ServerOrigin, session: String) -> DeepLink {
        .agent(workspace: origin.authority, session: session)
    }

    public static func terminal(origin: ServerOrigin, session: String) -> DeepLink {
        .terminal(workspace: origin.authority, session: session)
    }

    public static func userInfo(_ link: DeepLink) -> [String: String] { [linkKey: link.string] }

    /// The link a continued activity carries (nil: none, or not ours).
    public static func link(fromUserInfo info: [AnyHashable: Any]?) -> DeepLink? {
        guard let s = info?[linkKey] as? String, let l = try? DeepLink(string: s) else { return nil }
        switch l {
        case .tile, .agent, .terminal, .workspace: return l
        case .enroll, .sso, .ssoError: return nil // never from an activity
        }
    }
}

/// "Add another device": minting the code the new device scans.
public enum AddDevice {
    /// `POST /api/xbin/devices/enroll-code` (`{password}` after a
    /// `password` step-up).
    public static func request(password: String? = nil) -> APIRequest {
        var body: JSONValue = [:]
        if let password { body = ["password": .string(password)] }
        return .json("POST", AppAuthRoute.enrollCode, body)
    }

    public enum Outcome: Sendable, Equatable {
        /// Show it: the `xbin://enroll` link for the QR code, the code for
        /// typing, when it expires.
        case code(link: String, code: String, expires: Date?)
        /// Sign in again with this device's key (a fresh device login is a
        /// step-up), then ask again.
        case signInAgain
        /// Ask for the account password and send it.
        case needsPassword
        /// This session can't mint codes (a token session, not a user).
        case refused(String)
        case failed(APIError)
    }

    public static func outcome(_ r: APIResponse, origin: ServerOrigin) -> Outcome {
        guard r.status == 200 else {
            let e = APIError(r)
            if r.status == 403, let s = e.stepUp { return s == "password" ? .needsPassword : .signInAgain }
            if r.status == 403 { return .refused(e.message) }
            return .failed(e)
        }
        guard let j = try? r.json(), let c = try? EnrollCode(json: j) else {
            return .failed(APIError(status: 500, message: "the server's answer had no enrollment code"))
        }
        return .code(link: link(for: c, origin: origin), code: c.code, expires: c.expires)
    }

    /// The server's link when it is a well-formed `xbin://enroll` one, else
    /// one built from the code and the workspace's origin.
    public static func link(for c: EnrollCode, origin: ServerOrigin) -> String {
        if let l = try? DeepLink(string: c.url), case .enroll = l { return c.url }
        let server = (try? ServerOrigin(string: c.origin)) ?? origin
        return DeepLink.enroll(server: server, code: c.code).string
    }

    /// The code as people read it aloud: groups of four, `ABCD-EFGH-…`.
    public static func grouped(_ code: String) -> String {
        var out = ""
        for (i, ch) in DeviceLogin.normalizeEnrollCode(code).enumerated() {
            if i > 0, i % 4 == 0 { out.append("-") }
            out.append(ch)
        }
        return out
    }
}
