import Foundation

/// The device credential's wire formats (plans/native.md §5; the app-side
/// contract is native/spec/device-login.md, the server side
/// internal/auth/devices.go). XbinCore only builds bytes and encodings; the
/// app signs with the Secure Enclave key (CryptoKit) and xbind verifies
/// with `ecdsa.VerifyASN1`.
///
/// - **Signed message**: the UTF-8 bytes of
///   `"xbin-device-login-v1\n" + origin + "\n" + deviceId + "\n" + nonce`,
///   where `origin` is the origin the enrollment returned
///   (``DeviceEnrollment/origin``, kept as ``WorkspaceRecord/deviceOrigin``)
///   — not whatever address the user typed. None of the three may contain
///   a newline.
/// - **Signature**: ECDSA P-256 over SHA-256 of the message, ASN.1 DER
///   (CryptoKit's `signature.derRepresentation`), base64url without padding.
/// - **Public key**: SubjectPublicKeyInfo DER (CryptoKit's
///   `publicKey.derRepresentation`), base64url without padding.
public enum DeviceLogin {
    /// The first line of the signed message (the domain separator).
    public static let domain = "xbin-device-login-v1"

    /// A field that can't go into the message.
    public struct Invalid: Error, Equatable, Sendable, CustomStringConvertible {
        public var reason: String
        public var description: String { "device login: \(reason)" }
    }

    /// The bytes the device signs.
    public static func message(origin: String, deviceId: String, nonce: String) throws -> Data {
        for (name, v) in [("origin", origin), ("device id", deviceId), ("nonce", nonce)] {
            if v.isEmpty { throw Invalid(reason: "empty \(name)") }
            if v.contains(where: { $0 == "\n" || $0 == "\r" }) { throw Invalid(reason: "newline in \(name)") }
        }
        return Data((domain + "\n" + origin + "\n" + deviceId + "\n" + nonce).utf8)
    }

    /// The bytes the device signs, for `server`.
    public static func message(server: ServerOrigin, deviceId: String, nonce: String) throws -> Data {
        try message(origin: server.origin, deviceId: deviceId, nonce: nonce)
    }

    /// The DER prefix of a P-256 SubjectPublicKeyInfo holding an uncompressed
    /// point: SEQUENCE { SEQUENCE { id-ecPublicKey, prime256v1 }, BIT STRING }.
    public static let p256SPKIPrefix: [UInt8] = [
        0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x02, 0x01,
        0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
    ]

    /// Wraps a raw uncompressed P-256 point (X9.63: `04 ‖ X ‖ Y`, 65 bytes —
    /// `SecKeyCopyExternalRepresentation`'s output) as SPKI DER.
    public static func spki(x963 point: Data) throws -> Data {
        guard point.count == 65, point.first == 0x04 else { throw Invalid(reason: "not an uncompressed P-256 point") }
        return Data(p256SPKIPrefix) + point
    }

    /// Whether `der` is a P-256 SPKI with an uncompressed point.
    public static func isP256SPKI(_ der: Data) -> Bool {
        der.count == p256SPKIPrefix.count + 65 && der.prefix(p256SPKIPrefix.count).elementsEqual(p256SPKIPrefix)
            && der[der.startIndex + p256SPKIPrefix.count] == 0x04
    }

    /// A typed enrollment code as the server wants it: upper case, dashes
    /// and white space dropped.
    public static func normalizeEnrollCode(_ typed: String) -> String {
        String(typed.uppercased().unicodeScalars.filter { $0 != "-" && !CharacterSet.whitespacesAndNewlines.contains($0) })
    }

    /// Whether `code` (normalized) has the server's shape: 26 characters of
    /// `A–Z2–7`.
    public static func isEnrollCode(_ code: String) -> Bool {
        code.utf8.count == 26 && code.utf8.allSatisfy { (0x41...0x5A).contains($0) || (0x32...0x37).contains($0) }
    }

    /// Whether `der` is shaped like an ECDSA signature: SEQUENCE { INTEGER r,
    /// INTEGER s } with nothing after it. A structural check only.
    public static func isDERSignature(_ der: Data) -> Bool {
        let b = [UInt8](der)
        var i = 0
        func length() -> Int? {
            guard i < b.count else { return nil }
            let l = Int(b[i]); i += 1
            if l < 0x80 { return l }
            guard l == 0x81, i < b.count else { return nil }
            let v = Int(b[i]); i += 1
            return v >= 0x80 ? v : nil
        }
        func integer() -> Bool {
            guard i < b.count, b[i] == 0x02 else { return false }
            i += 1
            guard let l = length(), l >= 1, l <= 33, i + l <= b.count else { return false }
            i += l
            return true
        }
        guard b.count >= 8, b[0] == 0x30 else { return false }
        i = 1
        guard let total = length(), i + total == b.count else { return false }
        return integer() && integer() && i == b.count
    }
}

// MARK: - Routes

/// The app's sign-in routes (native/spec/device-login.md). Every body is
/// JSON; times are unix seconds; binary values base64url without padding.
public enum AppAuthRoute {
    /// `POST` with a session → ``EnrollCode``.
    public static let enrollCode = "/api/xbin/devices/enroll-code"
    /// `POST` ``DeviceEnrollRequest`` (no credential) → ``DeviceEnrollment``.
    public static let enroll = "/api/xbin/devices/enroll"
    /// `POST` ``DeviceChallengeRequest`` → ``DeviceChallenge``.
    public static let challenge = "/login/device/challenge"
    /// `POST` ``DeviceLoginRequest`` → ``AppSession``.
    public static let deviceLogin = "/login/device"
    /// `POST` ``PasswordLoginRequest`` → ``AppSession``.
    public static let passwordLogin = "/api/xbin/login"
    /// `POST` ``TicketRedeemRequest`` → ``AppSession``.
    public static let ticket = "/login/ticket"
    /// `GET` → `{"devices": [DeviceInfo]}`; `DELETE …/<deviceId>` removes one.
    public static let devices = "/api/xbin/devices"
    /// `POST` with the bearer → 204 (the device stays enrolled).
    public static let logout = "/logout"

    /// The page to open in `ASWebAuthenticationSession` for an SSO sign-in
    /// (callback scheme `xbin`); `challenge` is ``PKCE/challenge(sha256Digest:)``.
    public static func ssoStart(challenge: String) -> String {
        let unreserved = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~")
        return "/login/sso?app=1&challenge=" + (challenge.addingPercentEncoding(withAllowedCharacters: unreserved) ?? challenge)
    }
}

// MARK: - Wire types

/// `POST /api/xbin/devices/enroll` — redeem a one-time code with this
/// device's public key.
public struct DeviceEnrollRequest: Codable, Sendable, Equatable {
    /// The enrollment code (``DeviceLogin/normalizeEnrollCode(_:)``).
    public var code: String
    /// The device's display name (the server trims it to 64 characters).
    public var name: String
    /// `ios`, `ipados`, `android`, …
    public var platform: String
    /// The SPKI DER public key, base64url.
    public var publicKey: String

    public init(code: String, name: String, platform: String, publicKeySPKI: Data) {
        self.code = code
        self.name = name
        self.platform = platform
        publicKey = Base64URL.encode(publicKeySPKI)
    }
}

/// The enrollment answer `{deviceId, user, origin, name}`. Keep `deviceId`
/// **and** `origin` with the workspace: the origin is what every login signs.
public struct DeviceEnrollment: Sendable, Equatable {
    public var deviceId: String
    /// The user id the device belongs to.
    public var user: String
    /// The origin to sign (``DeviceLogin/message(origin:deviceId:nonce:)``).
    public var origin: String
    /// The name as stored.
    public var name: String

    public init(json: JSONValue) throws {
        guard let id = json["deviceId"]?.stringValue, !id.isEmpty else {
            throw DeviceLogin.Invalid(reason: "enrollment response without a deviceId")
        }
        guard let origin = json["origin"]?.stringValue, !origin.isEmpty else {
            throw DeviceLogin.Invalid(reason: "enrollment response without an origin")
        }
        deviceId = id
        self.origin = origin
        user = json["user"]?.stringValue ?? ""
        name = json["name"]?.stringValue ?? ""
    }
}

/// `POST /api/xbin/devices/enroll-code` → `{code, url, origin, expires}`:
/// a code the app mints for itself after an in-app sign-in.
public struct EnrollCode: Sendable, Equatable {
    public var code: String
    /// The `xbin://enroll?u=…&c=…` link.
    public var url: String
    public var origin: String
    public var expires: Date?

    public init(json: JSONValue) throws {
        guard let c = json["code"]?.stringValue, !c.isEmpty else { throw DeviceLogin.Invalid(reason: "no enrollment code") }
        code = c
        url = json["url"]?.stringValue ?? ""
        origin = json["origin"]?.stringValue ?? ""
        expires = json["expires"]?.intValue.map { Date(timeIntervalSince1970: Double($0)) }
    }
}

/// `POST /login/device/challenge {deviceId}`.
public struct DeviceChallengeRequest: Codable, Sendable, Equatable {
    public var deviceId: String
    public init(deviceId: String) { self.deviceId = deviceId }
}

/// The challenge `{nonce, expires}`: single use, 60 s, this device only.
/// Ask for a fresh one per attempt.
public struct DeviceChallenge: Sendable, Equatable {
    public var nonce: String
    public var expires: Date?

    public init(nonce: String, expires: Date? = nil) {
        self.nonce = nonce
        self.expires = expires
    }

    public init(json: JSONValue) throws {
        guard let n = json["nonce"]?.stringValue, !n.isEmpty else {
            throw DeviceLogin.Invalid(reason: "challenge without a nonce")
        }
        nonce = n
        expires = json["expires"]?.intValue.map { Date(timeIntervalSince1970: Double($0)) }
    }
}

/// `POST /login/device {deviceId, nonce, signature}`.
public struct DeviceLoginRequest: Codable, Sendable, Equatable {
    public var deviceId: String
    /// The nonce exactly as received.
    public var nonce: String
    /// The DER signature, base64url.
    public var signature: String

    public init(deviceId: String, nonce: String, signatureDER: Data) {
        self.deviceId = deviceId
        self.nonce = nonce
        signature = Base64URL.encode(signatureDER)
    }
}

/// `POST /api/xbin/login {username, password}` — the in-app password
/// sign-in (before a device exists).
public struct PasswordLoginRequest: Codable, Sendable, Equatable {
    public var username: String
    public var password: String
    public init(username: String, password: String) {
        self.username = username
        self.password = password
    }
}

/// `POST /login/ticket {ticket, verifier}` — the second half of the SSO
/// sign-in (`xbin://sso?ticket=…` + the PKCE verifier).
public struct TicketRedeemRequest: Codable, Sendable, Equatable {
    public var ticket: String
    public var verifier: String
    public init(ticket: String, verifier: String) {
        self.ticket = ticket
        self.verifier = verifier
    }
}

/// The token response of every app sign-in route: a human bearer session
/// for the app's own Swift code only — never handed to tile code (§2
/// invariant 1). Any 401 later means: sign in again.
public struct AppSession: Sendable, Equatable {
    public struct User: Sendable, Equatable {
        public var id: String
        public var name: String
        public var role: String
    }

    public var token: String
    /// `Bearer`.
    public var tokenType: String
    public var user: User
    /// The device this session signed in with (device logins only).
    public var deviceId: String?
    /// Expires after this without use (every request slides it).
    public var expiresIdle: Date?
    /// Expires at this regardless.
    public var expiresMax: Date?

    public init(json: JSONValue) throws {
        guard let t = json["token"]?.stringValue, !t.isEmpty else {
            throw DeviceLogin.Invalid(reason: "session response without a token")
        }
        token = t
        tokenType = json["tokenType"]?.stringValue ?? "Bearer"
        let u = json["user"]
        user = User(id: u?["id"]?.stringValue ?? u?.stringValue ?? "", name: u?["name"]?.stringValue ?? "",
                    role: u?["role"]?.stringValue ?? "")
        deviceId = json["deviceId"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
        expiresIdle = json["expiresIdle"]?.intValue.map { Date(timeIntervalSince1970: Double($0)) }
        expiresMax = json["expiresMax"]?.intValue.map { Date(timeIntervalSince1970: Double($0)) }
    }

    /// The `Authorization` header value.
    public var authorization: String { "Bearer \(token)" }
}

/// One row of `GET /api/xbin/devices`.
public struct DeviceInfo: Sendable, Equatable, Identifiable {
    public var id: String
    public var name: String
    public var platform: String
    public var origin: String
    public var created: Date?
    public var lastUsed: Date?
    public var lastIP: String?
    /// The device the asking session signed in with.
    public var current: Bool

    public init(json: JSONValue) {
        id = json["id"]?.stringValue ?? ""
        name = json["name"]?.stringValue ?? ""
        platform = json["platform"]?.stringValue ?? ""
        origin = json["origin"]?.stringValue ?? ""
        created = json["created"]?.intValue.map { Date(timeIntervalSince1970: Double($0)) }
        lastUsed = json["lastUsed"]?.intValue.map { Date(timeIntervalSince1970: Double($0)) }
        lastIP = json["lastIP"]?.stringValue
        current = json["current"]?.boolValue ?? false
    }

    /// The rows of a `{"devices": […]}` response.
    public static func list(json: JSONValue) -> [DeviceInfo] {
        (json["devices"]?.arrayValue ?? []).map(DeviceInfo.init(json:))
    }
}

/// PKCE (RFC 7636, S256) for the app's SSO sign-in. Hashing is the app's
/// (CryptoKit `SHA256`): XbinCore makes and checks verifiers and encodes
/// the challenge.
public enum PKCE {
    /// A fresh verifier: 32 random bytes, base64url (43 characters).
    public static func makeVerifier() -> String {
        var g = SystemRandomNumberGenerator()
        return Base64URL.encode(Data((0..<32).map { _ in UInt8.random(in: 0...255, using: &g) }))
    }

    /// 43–128 characters of `A–Z a–z 0–9 - . _ ~`.
    public static func isValidVerifier(_ v: String) -> Bool {
        (43...128).contains(v.utf8.count) && v.utf8.allSatisfy { b in
            (0x41...0x5A).contains(b) || (0x61...0x7A).contains(b) || (0x30...0x39).contains(b)
                || b == 0x2D || b == 0x2E || b == 0x5F || b == 0x7E
        }
    }

    /// `base64url(SHA-256(verifier))`, given the digest.
    public static func challenge(sha256Digest: Data) -> String { Base64URL.encode(sha256Digest) }
}
