import Foundation

// Adding a workspace, the parts that decide rather than draw (native/spec/
// device-login.md §Discovery, §Invite): what a workspace offers for signing
// in, what a scanned or pasted link is, and redeeming an invite in the app.

extension AppAuthRoute {
    /// `GET`, no credential → ``LoginMethods``.
    public static let loginMethods = "/api/xbin/login/methods"
    /// `POST {invite}`, no credential → ``InviteInfo``.
    public static let inviteCheck = "/api/xbin/invite/check"
    /// `POST {invite, password}`, no credential → the token response.
    public static let inviteRedeem = "/api/xbin/invite/redeem"
}

/// `GET /api/xbin/login/methods`: what the workspace's login page offers,
/// as data — `{"api":1,"title","auth","password":{"enabled","adminOnly"},
/// "sso":{"enabled","label"},"invites"}`. ``legacy`` stands in for an xbind
/// older than the route: the app then offers both, as it always did.
public struct LoginMethods: Sendable, Hashable {
    public var api: Int
    /// The workspace's title (its branding, else "xbin").
    public var title: String
    /// False: the workspace runs without sign-in (`--no-auth`).
    public var auth: Bool
    /// A password signs someone in (the workspace has accounts).
    public var password: Bool
    /// SSO-only mode: the password form is for admins only.
    public var passwordAdminOnly: Bool
    public var sso: Bool
    /// The SSO button's words ("Sign in with Acme").
    public var ssoLabel: String
    /// Invite links can be redeemed in the app.
    public var invites: Bool
    /// The workspace predates the route (``legacy``).
    public var isLegacy: Bool

    public static let defaultSSOLabel = "Sign in with SSO"

    public init(api: Int = 1, title: String = "", auth: Bool = true, password: Bool, passwordAdminOnly: Bool = false,
                sso: Bool, ssoLabel: String = LoginMethods.defaultSSOLabel, invites: Bool = true, isLegacy: Bool = false) {
        self.api = api
        self.title = title
        self.auth = auth
        self.password = password
        self.passwordAdminOnly = passwordAdminOnly
        self.sso = sso
        self.ssoLabel = ssoLabel
        self.invites = invites
        self.isLegacy = isLegacy
    }

    /// An xbind older than the route: password and SSO both offered.
    public static let legacy = LoginMethods(api: 0, password: true, sso: true, invites: false, isLegacy: true)

    /// Reads the answer; one without a numeric `api` isn't this route's.
    public init(json: JSONValue) throws {
        guard let api = json["api"]?.intValue else { throw DeviceLogin.Invalid(reason: "sign-in methods without api") }
        self.api = Int(api)
        title = json["title"]?.stringValue ?? ""
        auth = json["auth"]?.boolValue ?? true
        password = json["password"]?["enabled"]?.boolValue ?? false
        passwordAdminOnly = json["password"]?["adminOnly"]?.boolValue ?? false
        sso = json["sso"]?["enabled"]?.boolValue ?? false
        let label = json["sso"]?["label"]?.stringValue?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        ssoLabel = label.isEmpty ? Self.defaultSSOLabel : label
        invites = json["invites"]?.boolValue ?? false
        isLegacy = false
    }
}

/// What the sign-in screen shows for a workspace's ``LoginMethods``, in
/// order: the password form and/or the SSO button. SSO-only mode puts SSO
/// first and the form under "Workspace admin"; no accounts at all (or no
/// sign-in) leaves nothing to show but an explanation.
public struct SignInOptions: Sendable, Equatable {
    public enum Method: Sendable, Equatable {
        case password(header: String?)
        case sso(label: String)
    }

    public var methods: [Method]
    /// Why there is nothing to sign in with (nil when ``methods`` isn't empty).
    public var explanation: String?

    public static let adminHeader = "Workspace admin"

    public init(_ m: LoginMethods) {
        if !m.auth {
            methods = []
            explanation = Enrollment.Failure.noAccounts.description
            return
        }
        var out: [Method] = []
        if m.passwordAdminOnly {
            if m.sso { out.append(.sso(label: m.ssoLabel)) }
            out.append(.password(header: Self.adminHeader))
        } else {
            if m.password { out.append(.password(header: nil)) }
            if m.sso { out.append(.sso(label: m.ssoLabel)) }
        }
        methods = out
        explanation = out.isEmpty
            ? "This workspace has no accounts yet. Its owner opens it with the sign-in link from the server's "
            + "log, then adds you (or sends you an invite)."
            : nil
    }

    public var hasPassword: Bool { methods.contains { if case .password = $0 { return true } else { return false } } }
    public var ssoLabel: String? {
        for m in methods { if case .sso(let l) = m { return l } }
        return nil
    }
}

/// `POST /api/xbin/invite/check` → `{user:{id,name}, expires, title}`:
/// whom an invite link is for, before the password is set.
public struct InviteInfo: Sendable, Hashable {
    public var userID: String
    public var userName: String
    public var expires: Date?
    /// The workspace's title (its branding, else "xbin").
    public var title: String

    public init(userID: String, userName: String, expires: Date? = nil, title: String) {
        self.userID = userID
        self.userName = userName
        self.expires = expires
        self.title = title
    }

    public init(json: JSONValue) throws {
        let u = json["user"]
        guard let id = u?["id"]?.stringValue ?? u?.stringValue, !id.isEmpty else {
            throw DeviceLogin.Invalid(reason: "invite answer without a user")
        }
        userID = id
        let name = u?["name"]?.stringValue ?? ""
        userName = name.isEmpty ? id : name
        if let t = json["expires"]?.intValue {
            expires = Date(timeIntervalSince1970: Double(t))
        } else if let s = json["expires"]?.stringValue {
            expires = ISO8601DateFormatter().date(from: s)
        }
        title = json["title"]?.stringValue ?? ""
    }
}

/// What a scanned QR code or a pasted link is: a device enrollment
/// (`xbin://enroll?u=…&c=…`, from a signed-in browser), an invite
/// (`https://<workspace>/login?invite=…`), or just a workspace's address.
public enum SignInLink: Sendable, Hashable {
    case enroll(server: ServerOrigin, code: String)
    case invite(server: ServerOrigin, token: String)
    case address(ServerOrigin)

    public struct Invalid: Error, Sendable, Equatable, CustomStringConvertible {
        public var reason: String
        public var description: String { reason }
    }

    /// `allowAddress`: a plain `http(s)://host[:port]` (or a bare host) is a
    /// workspace address — true for what a QR code or the paste field
    /// holds, false where only a link makes sense (the invite field).
    public init(_ text: String, allowAddress: Bool = true) throws {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { throw Invalid(reason: "Paste the link first.") }
        if t.lowercased().hasPrefix(DeepLink.scheme + ":") {
            guard let l = try? DeepLink(string: t), case .enroll(let s, let c) = l else {
                throw Invalid(reason: "That's an xbin link, but not one that adds a device (xbin://enroll…).")
            }
            self = .enroll(server: s, code: c)
            return
        }
        if let c = URLComponents(string: t), let scheme = c.scheme?.lowercased(), scheme == "http" || scheme == "https",
           let items = c.queryItems, let tok = items.first(where: { $0.name == "invite" })?.value, !tok.isEmpty {
            var path = c.percentEncodedPath
            while path.hasSuffix("/") { path.removeLast() }
            guard path == "/login", c.user == nil, c.password == nil, let host = c.host, !host.isEmpty else {
                throw Invalid(reason: "That isn't an xbin invite link (https://…/login?invite=…).")
            }
            let server: ServerOrigin
            do { server = try ServerOrigin(scheme: scheme, host: host, port: c.port) } catch {
                throw Invalid(reason: "That invite link's address doesn't work: \(error)")
            }
            self = .invite(server: server, token: tok)
            return
        }
        guard allowAddress else {
            throw Invalid(reason: "That isn't an invite link: it looks like https://…/login?invite=…")
        }
        guard let s = try? ServerOrigin(userInput: t), !t.contains(" ") else {
            throw Invalid(reason: "That isn't an xbin link or a workspace address.")
        }
        self = .address(s)
    }

    public var server: ServerOrigin {
        switch self {
        case .enroll(let s, _), .invite(let s, _), .address(let s): return s
        }
    }
}

// MARK: - The routes

extension Enrollment {
    /// `GET /api/xbin/login/methods` — also the check that `server` is an
    /// xbin workspace at all, made before an enrollment code is spent. An
    /// xbind older than the route answers 401 (its `/api/` gate runs before
    /// the route lookup) or, without sign-in, 404: ``olderXbind(_:status:)``
    /// tells that from "not a workspace" (device-login.md §8). Anything else
    /// that isn't the route's JSON is ``ConnectProblem/notXbin(host:detail:)``;
    /// transport errors are thrown as they come (``ConnectProblem`` reads
    /// them).
    public func loginMethods(server: ServerOrigin) async throws -> LoginMethods {
        var req = APIRequest("GET", AppAuthRoute.loginMethods)
        req.setHeader("Accept", "application/json")
        let r = try await transport.send(stamp(req), to: server)
        let host = server.authority
        switch r.status {
        case 200:
            guard let j = try? r.json(), let m = try? LoginMethods(json: j) else {
                throw ConnectProblem.notXbin(host: host, detail: "its answer isn't what an xbin workspace sends")
            }
            return m
        case 401, 404:
            return try await olderXbind(server, status: r.status)
        case 429:
            throw ConnectProblem.throttled
        case 300...399:
            let to = r.header("Location").map { " to \($0)" } ?? ""
            throw ConnectProblem.notXbin(host: host, detail: "it redirects\(to)")
        case 500...599:
            throw ConnectProblem.server(status: r.status, message: APIError(r).message)
        default:
            throw ConnectProblem.notXbin(host: host, detail: "HTTP \(r.status)")
        }
    }

    /// After a 401 or 404 where a newer xbind has a public route: `GET
    /// /login`. An older xbind serves its sign-in page there (a form posting
    /// a password to /login): ``LoginMethods/legacy``, password and SSO as
    /// before. Without sign-in it redirects to `/`: nothing to sign in with.
    /// Anything else isn't an xbin workspace.
    func olderXbind(_ server: ServerOrigin, status: Int) async throws -> LoginMethods {
        var req = APIRequest("GET", "/login")
        req.setHeader("Accept", "text/html")
        let r = try await transport.send(stamp(req), to: server)
        if r.status == 200 {
            let html = String(decoding: r.body.prefix(256 * 1024), as: UTF8.self)
            if html.contains("action=\"/login\""), html.contains("name=\"password\"") { return .legacy }
        } else if (300...399).contains(r.status), let to = r.header("Location"), to == "/" || to == server.origin + "/" {
            var m = LoginMethods.legacy
            (m.auth, m.password, m.sso) = (false, false, false)
            return m
        }
        throw ConnectProblem.notXbin(host: server.authority, detail: "HTTP \(status)")
    }

    /// The QR code's path: the code's shape, then ``loginMethods(server:)``
    /// (an unreachable or non-xbin address fails here, before the code is
    /// spent; a workspace without sign-in can't enroll), then
    /// ``enroll(server:code:deviceName:workspaceID:)``.
    public func enrollChecked(server: ServerOrigin, code: String, deviceName: String,
                              workspaceID id: String = UUID().uuidString.lowercased()) async throws -> (WorkspaceRecord, SessionCredential) {
        guard DeviceLogin.isEnrollCode(DeviceLogin.normalizeEnrollCode(code)) else { throw Failure.badCode }
        let m = try await loginMethods(server: server)
        if !m.auth { throw Failure.noAccounts }
        return try await enroll(server: server, code: code, deviceName: deviceName, workspaceID: id)
    }

    /// `POST /api/xbin/invite/check` — whom the invite is for (not spent).
    public func checkInvite(server: ServerOrigin, invite: String) async throws -> InviteInfo {
        let r = try await transport.send(stamp(.json("POST", AppAuthRoute.inviteCheck, ["invite": .string(invite)])), to: server)
        switch r.status {
        case 200: return try InviteInfo(json: r.json())
        default: throw await inviteFailure(r, server)
        }
    }

    /// `POST /api/xbin/invite/redeem` — sets the invitee's password and
    /// spends the invite; the answer is a password session (enroll next,
    /// ``enrollSignedIn(server:session:deviceName:password:workspaceID:)``).
    /// A password the policy refuses leaves the invite unspent.
    public func redeemInvite(server: ServerOrigin, invite: String, password: String) async throws -> SessionCredential {
        let r = try await transport.send(stamp(.json("POST", AppAuthRoute.inviteRedeem,
                                                     ["invite": .string(invite), "password": .string(password)])), to: server)
        switch r.status {
        case 200: return SessionCredential(try AppSession(json: r.json()), kind: .password)
        case 400: throw Failure.passwordRejected(APIError(r).message)
        default: throw await inviteFailure(r, server)
        }
    }

    private func inviteFailure(_ r: APIResponse, _ server: ServerOrigin) async -> any Error {
        let e = APIError(r)
        switch r.status {
        // No such public route: an xbind older than app invites (401 from
        // its /api/ gate; 404 without sign-in) — or no workspace at all.
        case 401, 404:
            do {
                return try await olderXbind(server, status: r.status).auth ? Failure.inviteNeedsBrowser : Failure.noAccounts
            } catch { return error }
        case 403: return e.message.contains("without sign-in") ? Failure.noAccounts : Failure.inviteInvalid(e.message)
        default: return Failure.server(e)
        }
    }

    private func stamp(_ r: APIRequest) -> APIRequest {
        var r = r
        r.setHeader("X-XBin-Client", clientHeader)
        return r
    }
}
