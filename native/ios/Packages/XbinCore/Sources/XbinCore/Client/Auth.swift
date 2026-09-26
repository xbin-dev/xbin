import Foundation

// Device login, the app side (native/spec/device-login.md; plans/native.md
// §5): the session a workspace's Swift code uses, re-signed automatically on
// a 401 with the workspace's enclave key — one Face ID prompt however many
// requests hit the 401 together.

/// A workspace's session, as the app keeps it (the Keychain).
public struct SessionCredential: Sendable, Equatable, Codable {
    public enum Kind: String, Sendable, Codable {
        /// `POST /login/device` — renewable with the device key.
        case device
        /// `POST /api/xbin/login` or an SSO ticket before a device exists.
        case password
        case sso
        /// Advanced "server URL + token" (development): nothing renews it.
        case token
    }

    public var token: String
    public var kind: Kind
    public var userID: String
    public var userName: String
    public var role: String
    public var expiresIdle: Date?
    public var expiresMax: Date?

    public init(token: String, kind: Kind, userID: String = "", userName: String = "", role: String = "",
                expiresIdle: Date? = nil, expiresMax: Date? = nil) {
        self.token = token
        self.kind = kind
        self.userID = userID
        self.userName = userName
        self.role = role
        self.expiresIdle = expiresIdle
        self.expiresMax = expiresMax
    }

    public init(_ s: AppSession, kind: Kind) {
        self.init(token: s.token, kind: kind, userID: s.user.id, userName: s.user.name, role: s.user.role,
                  expiresIdle: s.expiresIdle, expiresMax: s.expiresMax)
    }

    public var authorization: String { "Bearer \(token)" }

    /// Known to be dead (past either expiry) — sign in before using it.
    public func isExpired(at now: Date) -> Bool {
        if let m = expiresMax, now >= m { return true }
        if let i = expiresIdle, now >= i { return true }
        return false
    }
}

/// Where sessions live (the Keychain in the app, memory in tests).
public protocol SessionStore: Sendable {
    func loadSession(workspace: String) async -> SessionCredential?
    func saveSession(_ session: SessionCredential?, workspace: String) async
}

/// The workspace's device key: a P-256 key in the Secure Enclave with
/// `.biometryCurrentSet` in the app (a software key on the simulator and in
/// tests). Public keys are SPKI DER; signatures ECDSA-SHA256, ASN.1 DER.
public protocol DeviceKeyStore: Sendable {
    /// Creates the workspace's key, replacing any old one; returns its SPKI.
    func createKey(workspace: String) async throws -> Data
    func hasKey(workspace: String) async -> Bool
    /// Signs `message` — the biometric prompt, with `reason` as its text.
    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data
    func deleteKey(workspace: String) async
}

/// Why signing in failed — each case is something the UI says differently.
public enum SignInError: Error, Sendable, Equatable, CustomStringConvertible {
    /// No device is enrolled for this workspace (a password/token session
    /// ended): sign in again from the add-workspace screen.
    case notEnrolled
    /// `POST /login/device/challenge` answered 404: the device was removed
    /// on the server. Enroll again.
    case deviceRemoved
    /// 403 `{reauth:"sso"}`: an SSO-only workspace wants a fresh SSO sign-in.
    case ssoRequired
    /// 403: the account is disabled.
    case disabled(String)
    /// 401 on `POST /login/device`: the signature didn't verify (the key was
    /// replaced, e.g. biometrics changed) — enroll again.
    case rejected(String)
    /// The key can't sign: gone, or invalidated by a biometry change.
    case keyUnavailable(String)
    /// The user cancelled the Face ID prompt.
    case cancelled
    /// 429 — too many failed attempts from this address.
    case throttled
    /// Any other answer.
    case server(APIError)

    public var description: String {
        switch self {
        case .notEnrolled: return "This device isn't enrolled in the workspace. Sign in again."
        case .deviceRemoved: return "This device was removed from the workspace. Add it again."
        case .ssoRequired: return "This workspace wants you to sign in with SSO again."
        case .disabled(let m): return "The account is disabled (\(m))."
        case .rejected(let m): return "The workspace didn't accept this device's key (\(m)). Add the device again."
        case .keyUnavailable(let m): return "The device key can't be used: \(m)"
        case .cancelled: return "Sign-in cancelled."
        case .throttled: return "Too many attempts — wait a little and try again."
        case .server(let e): return e.description
        }
    }

    /// The device's enrollment is over: the UI offers "add again".
    public var needsEnrollment: Bool {
        switch self {
        case .notEnrolled, .deviceRemoved, .rejected, .keyUnavailable: return true
        default: return false
        }
    }
}

/// One workspace's authenticated HTTP: attaches the session, and on a 401
/// signs in again with the device key (single flight) and retries once.
public actor WorkspaceAuth {
    public private(set) var record: WorkspaceRecord
    public private(set) var credential: SessionCredential?
    /// Signed in within this process (a fresh session backs the tiles).
    public private(set) var signInCount = 0

    private let transport: any APITransport
    private let keys: any DeviceKeyStore
    private let sessions: any SessionStore
    private let clientHeader: String
    private let now: @Sendable () -> Date
    private var inflight: Task<SessionCredential, any Error>?
    private var loaded = false
    private var observers: [UUID: @Sendable (SessionCredential?) -> Void] = [:]

    /// `clientHeader` is sent as `X-XBin-Client` (`app/<version>`).
    public init(record: WorkspaceRecord, transport: any APITransport, keys: any DeviceKeyStore,
                sessions: any SessionStore, clientHeader: String, now: @escaping @Sendable () -> Date = Date.init) {
        self.record = record
        self.transport = transport
        self.keys = keys
        self.sessions = sessions
        self.clientHeader = clientHeader
        self.now = now
    }

    public var origin: ServerOrigin { record.server }

    /// Replaces the record (a rename, branding, a re-enrollment).
    public func update(record r: WorkspaceRecord) { record = r }

    /// Adopts a session obtained elsewhere (enrollment, password, SSO, token).
    public func adopt(_ c: SessionCredential?) async {
        credential = c
        loaded = true
        await sessions.saveSession(c, workspace: record.id)
        notify()
    }

    /// Called with the new credential whenever it changes (nil = signed out).
    @discardableResult
    public func observe(_ f: @escaping @Sendable (SessionCredential?) -> Void) -> UUID {
        let id = UUID()
        observers[id] = f
        return id
    }

    public func removeObserver(_ id: UUID) { observers[id] = nil }

    private func notify() {
        for f in observers.values { f(credential) }
    }

    private func loadIfNeeded() async {
        guard !loaded else { return }
        loaded = true
        credential = await sessions.loadSession(workspace: record.id)
    }

    /// Whether a 401 can be healed without the user typing anything.
    public var canResign: Bool { record.deviceId != nil }

    // MARK: Requests

    /// Sends with the session. A 401 (or no session at all) triggers one
    /// device sign-in — shared by every request waiting on it — and one
    /// retry; the answer after that is returned as is (never a loop).
    public func send(_ request: APIRequest) async throws -> APIResponse {
        await loadIfNeeded()
        var used = credential
        var signedHere = false
        if used == nil || used!.isExpired(at: now()), canResign {
            used = try await session(replacing: used?.token)
            signedHere = true
        }
        let first = try await transport.send(stamped(request, used), to: origin)
        // A 401 on a session signed in for this very request won't heal by
        // signing in again.
        guard first.status == 401, !signedHere else { return first }
        guard canResign else {
            if credential?.token == used?.token, credential != nil { await adopt(nil) }
            return first
        }
        let fresh = try await session(replacing: used?.token)
        return try await transport.send(stamped(request, fresh), to: origin)
    }

    /// `send`, then a non-2xx answer becomes an ``APIError``.
    public func call(_ request: APIRequest) async throws -> APIResponse {
        let r = try await send(request)
        guard r.isSuccess else { throw APIError(r) }
        return r
    }

    /// `call`, decoded as JSON.
    public func json(_ request: APIRequest) async throws -> JSONValue {
        try await call(request).json()
    }

    /// Sends without any credential (the sign-in routes; the body is the
    /// credential) — only the client header.
    public func sendAnonymous(_ request: APIRequest) async throws -> APIResponse {
        try await transport.send(stamped(request, nil), to: origin)
    }

    private func stamped(_ request: APIRequest, _ c: SessionCredential?) -> APIRequest {
        var r = request
        r.setHeader("X-XBin-Client", clientHeader)
        if let c { r.setHeader("Authorization", c.authorization) }
        return r
    }

    // MARK: Signing in

    /// A usable session: the current one unless it is `stale` (the token a
    /// 401 answered), else a device sign-in — joined when one is running.
    public func session(replacing stale: String?) async throws -> SessionCredential {
        await loadIfNeeded()
        if let c = credential, c.token != stale, !c.isExpired(at: now()) { return c }
        if let t = inflight { return try await t.value }
        let t = Task { try await self.deviceSignIn() }
        inflight = t
        defer { inflight = nil }
        let c = try await t.value
        return c
    }

    /// Challenge → sign (Face ID) → login. Stores the new session.
    public func signIn() async throws -> SessionCredential {
        try await session(replacing: credential?.token)
    }

    private func deviceSignIn() async throws -> SessionCredential {
        guard let deviceId = record.deviceId else { throw SignInError.notEnrolled }
        let ch = try await sendAnonymous(.json("POST", AppAuthRoute.challenge, ["deviceId": .string(deviceId)]))
        switch ch.status {
        case 200: break
        case 404: throw SignInError.deviceRemoved
        case 429: throw SignInError.throttled
        default: throw SignInError.server(APIError(ch))
        }
        let challenge = try DeviceChallenge(json: ch.json())
        let message = try record.deviceLoginMessage(nonce: challenge.nonce)
        let signature = try await keys.sign(message, workspace: record.id,
                                            reason: "Sign in to \(record.displayTitle)")
        let body: JSONValue = ["deviceId": .string(deviceId), "nonce": .string(challenge.nonce),
                               "signature": .string(Base64URL.encode(signature))]
        let r = try await sendAnonymous(.json("POST", AppAuthRoute.deviceLogin, body))
        switch r.status {
        case 200:
            let c = SessionCredential(try AppSession(json: r.json()), kind: .device)
            signInCount += 1
            if !c.userName.isEmpty || !c.userID.isEmpty {
                record.user = WorkspaceUser(id: c.userID.isEmpty ? record.user.id : c.userID,
                                            name: c.userName.isEmpty ? record.user.name : c.userName)
            }
            await adopt(c)
            return c
        case 401: throw SignInError.rejected(APIError(r).message)
        case 403:
            let e = APIError(r)
            if e.reauth == "sso" { throw SignInError.ssoRequired }
            throw SignInError.disabled(e.message)
        case 429: throw SignInError.throttled
        default: throw SignInError.server(APIError(r))
        }
    }

    /// `POST /logout` with the session (the device stays enrolled), then
    /// forgets it.
    public func signOut() async {
        await loadIfNeeded()
        if let c = credential {
            _ = try? await transport.send(stamped(APIRequest("POST", AppAuthRoute.logout), c), to: origin)
        }
        await adopt(nil)
    }
}

// MARK: - Adding a workspace

/// The flows that end with a new workspace record: redeeming an enrollment
/// code (a QR code from a signed-in browser, or one the app mints after a
/// password/SSO sign-in), and the development "server + token" login.
public struct Enrollment: Sendable {
    public let transport: any APITransport
    public let keys: any DeviceKeyStore
    public let clientHeader: String
    /// `ios` / `ipados`.
    public let platform: String

    public init(transport: any APITransport, keys: any DeviceKeyStore, clientHeader: String, platform: String) {
        self.transport = transport
        self.keys = keys
        self.clientHeader = clientHeader
        self.platform = platform
    }

    public enum Failure: Error, Sendable, Equatable, CustomStringConvertible {
        case badCode
        case codeRefused(String)
        case badKey(String)
        case deviceLimit(String)
        case stepUp(String)
        case notAUser(String)
        case invalidCredentials
        case server(APIError)

        public var description: String {
            switch self {
            case .badCode: return "That isn't an enrollment code (26 letters and digits)."
            case .codeRefused(let m): return "The code wasn't accepted — it may have expired or been used (\(m)). Make a new one."
            case .badKey(let m): return "The workspace refused this device's key (\(m))."
            case .deviceLimit(let m): return "Can't add the device: \(m)"
            case .stepUp(let s): return s == "password" ? "Confirm your password to add this device." : "Sign in again to add this device."
            case .notAUser(let m): return "This sign-in can't enroll a device (\(m))."
            case .invalidCredentials: return "Wrong username or password."
            case .server(let e): return e.description
            }
        }
    }

    private func send(_ r: APIRequest, _ origin: ServerOrigin, bearer: String? = nil) async throws -> APIResponse {
        var r = r
        r.setHeader("X-XBin-Client", clientHeader)
        if let bearer { r.setHeader("Authorization", "Bearer \(bearer)") }
        return try await transport.send(r, to: origin)
    }

    /// Redeems `code` on `server` with a fresh key for workspace `id`, then
    /// signs in with it. Returns the enrolled record and its first session.
    public func enroll(server: ServerOrigin, code typed: String, deviceName: String,
                       workspaceID id: String = UUID().uuidString.lowercased()) async throws -> (WorkspaceRecord, SessionCredential) {
        let code = DeviceLogin.normalizeEnrollCode(typed)
        guard DeviceLogin.isEnrollCode(code) else { throw Failure.badCode }
        let spki = try await keys.createKey(workspace: id)
        let body = DeviceEnrollRequest(code: code, name: deviceName, platform: platform, publicKeySPKI: spki)
        let req = APIRequest.json("POST", AppAuthRoute.enroll, [
            "code": .string(body.code), "name": .string(body.name), "platform": .string(body.platform),
            "publicKey": .string(body.publicKey),
        ])
        let r: APIResponse
        do { r = try await send(req, server) } catch {
            await keys.deleteKey(workspace: id)
            throw error
        }
        guard r.status == 200 else {
            await keys.deleteKey(workspace: id)
            let e = APIError(r)
            switch r.status {
            case 400: throw Failure.badKey(e.message)
            case 401: throw Failure.codeRefused(e.message)
            case 409: throw Failure.deviceLimit(e.message)
            default: throw Failure.server(e)
            }
        }
        let enrollment = try DeviceEnrollment(json: r.json())
        var record = WorkspaceRecord(id: id, server: server, user: WorkspaceUser(id: enrollment.user))
        record.enrolled(enrollment, name: deviceName)
        return (record, try await firstSignIn(record))
    }

    /// Signs in once with a just-enrolled record (an in-memory store: the
    /// caller persists what it keeps).
    private func firstSignIn(_ record: WorkspaceRecord) async throws -> SessionCredential {
        let auth = WorkspaceAuth(record: record, transport: transport, keys: keys, sessions: MemorySessionStore(),
                                 clientHeader: clientHeader)
        return try await auth.signIn()
    }

    /// `POST /api/xbin/login` — a password session (before a device exists).
    public func passwordLogin(server: ServerOrigin, username: String, password: String) async throws -> SessionCredential {
        let r = try await send(.json("POST", AppAuthRoute.passwordLogin,
                                     ["username": .string(username), "password": .string(password)]), server)
        switch r.status {
        case 200: return SessionCredential(try AppSession(json: r.json()), kind: .password)
        case 401: throw Failure.invalidCredentials
        default: throw Failure.server(APIError(r))
        }
    }

    /// `POST /login/ticket` — the end of an SSO sign-in.
    public func redeemTicket(server: ServerOrigin, ticket: String, verifier: String) async throws -> SessionCredential {
        let r = try await send(.json("POST", AppAuthRoute.ticket,
                                     ["ticket": .string(ticket), "verifier": .string(verifier)]), server)
        guard r.status == 200 else { throw Failure.server(APIError(r)) }
        return SessionCredential(try AppSession(json: r.json()), kind: .sso)
    }

    /// Mints an enrollment code with a fresh session (a step-up: within 10
    /// minutes of the sign-in, or with the password), then enrolls this
    /// device with it. On success the device session replaces `session`.
    public func enrollSignedIn(server: ServerOrigin, session: SessionCredential, deviceName: String,
                               password: String? = nil,
                               workspaceID id: String = UUID().uuidString.lowercased()) async throws -> (WorkspaceRecord, SessionCredential) {
        var body: JSONValue = [:]
        if let password { body = ["password": .string(password)] }
        let r = try await send(.json("POST", AppAuthRoute.enrollCode, body), server, bearer: session.token)
        guard r.status == 200 else {
            let e = APIError(r)
            if r.status == 403, let s = e.stepUp { throw Failure.stepUp(s) }
            if r.status == 403 { throw Failure.notAUser(e.message) }
            throw Failure.server(e)
        }
        let code = try EnrollCode(json: r.json())
        // Redeemed at the address the user gave; the origin every login
        // signs comes back from the enrollment itself (DeviceEnrollment).
        let enrolled = try await enroll(server: server, code: code.code, deviceName: deviceName, workspaceID: id)
        // The device session replaces the password/SSO one: end that.
        _ = try? await send(APIRequest("POST", AppAuthRoute.logout), server, bearer: session.token)
        return enrolled
    }

    /// Advanced login: a raw bearer token (development). Checks it with
    /// `/api/xbin/whoami` and returns a record without a device.
    public func tokenLogin(server: ServerOrigin, token: String,
                           workspaceID id: String = UUID().uuidString.lowercased()) async throws -> (WorkspaceRecord, SessionCredential) {
        let r = try await send(APIRequest("GET", "/api/xbin/whoami"), server, bearer: token)
        guard r.status == 200 else {
            if r.status == 401 { throw Failure.invalidCredentials }
            throw Failure.server(APIError(r))
        }
        let who = Whoami(json: try r.json())
        let c = SessionCredential(token: token, kind: .token, userID: who.userID, userName: who.displayName, role: who.role)
        let record = WorkspaceRecord(id: id, server: server,
                                     user: WorkspaceUser(id: who.userID.isEmpty ? "owner" : who.userID, name: who.displayName))
        return (record, c)
    }
}

/// An in-memory ``SessionStore`` (tests, and the first sign-in of an
/// enrollment before the record is saved).
public actor MemorySessionStore: SessionStore {
    private var sessions: [String: SessionCredential] = [:]
    public init() {}
    public func loadSession(workspace: String) async -> SessionCredential? { sessions[workspace] }
    public func saveSession(_ session: SessionCredential?, workspace: String) async { sessions[workspace] = session }
}
