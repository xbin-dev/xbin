import Foundation

// App tunnels (D204; the wire contract is native/spec/tunnel.md): the parts
// that decide rather than move bytes. The pairing link the QR code holds
// (§2), the pairing messages (§6), the confirmation code's digits (§5), the
// tunnel's client config (§7), what a workspace record keeps of its tunnel,
// the connection's status and reconnect policy, and `ByteStream`, the one
// shape every byte pipe has here (an iroh bi-stream, a loopback TCP
// connection, an in-memory pair in tests).
//
// The cryptography — Noise IK/IKpsk2, the selector, the code's HKDF — is
// CryptoKit's and lives in the app's `Shared/NoiseTunnel.swift`, checked on
// Linux by native/tools/app-check against native/spec/tunnel-vectors.json.
// iroh itself is the app's (IrohLib; `App/Model/TunnelEndpoint.swift`).

// MARK: - The pairing link (§2)

/// `xbin://enroll?u=<origin>&c=<code>&tv=1&t=<ticket>&k=<server key>&p=<psk>[&r=<relay token>][&rq=1]`:
/// a device enrollment that pairs through the workspace's tunnel.
///
/// Parsing is all or nothing: a link with any tunnel parameter must be a
/// whole, well-formed tunnel link, or it is refused — never quietly read as
/// a classic `xbin://enroll` link (the classic redemption would fail anyway:
/// xbind mints these codes as tunnel codes, which never redeem over plain
/// enrollment, so the confirmation can't be skipped).
public struct TunnelLink: Sendable, Hashable {
    /// The tunnel version this app speaks (`tv`).
    public static let version = 1
    /// The query keys that make a link a tunnel link.
    public static let tunnelKeys: [String] = ["tv", "t", "k", "p", "r", "rq"]

    /// `u`: the workspace's origin (what the record's `server` becomes).
    public var server: ServerOrigin
    /// `c`: the enroll code. Only its selector travels (§4).
    public var code: String
    /// `t`: an iroh EndpointTicket (`endpoint…`): the server's EndpointId and
    /// its relay URL.
    public var ticket: String
    /// `k`: the server's Noise static public key (X25519, 32 bytes).
    public var serverKey: Data
    /// `p`: the pairing psk (32 bytes, single use).
    public var psk: Data
    /// `r`: the relay's access token (a custom relay that has one).
    public var relayToken: String?
    /// `rq=1`: the workspace refuses this device's app traffic outside the
    /// tunnel — pinned, never relaxed by anything but the workspace's own
    /// client config over the tunnel.
    public var required: Bool

    public init(server: ServerOrigin, code: String, ticket: String, serverKey: Data, psk: Data,
                relayToken: String? = nil, required: Bool = false) {
        self.server = server
        self.code = code
        self.ticket = ticket
        self.serverKey = serverKey
        self.psk = psk
        self.relayToken = relayToken
        self.required = required
    }

    /// Why a tunnel link was refused.
    public enum Invalid: Error, Sendable, Equatable, CustomStringConvertible {
        /// `tv` names a version this app doesn't speak: update the app.
        case newerVersion(String)
        /// A parameter is missing or damaged.
        case malformed(String)

        public var description: String {
            switch self {
            case .newerVersion:
                return "This pairing code is for a newer version of the app. Update xbin, then scan the code again."
            case .malformed(let why):
                return "That pairing link is damaged (\(why)). Copy it again, or make a new code on the computer: "
                    + "settings → add a device."
            }
        }
    }

    /// Whether an `xbin://enroll` query carries any tunnel parameter.
    public static func isTunnel(query: [String: String]) -> Bool {
        tunnelKeys.contains { query[$0] != nil }
    }

    /// Reads the tunnel parameters of an `xbin://enroll` link whose `u` and
    /// `c` were already read.
    public init(server: ServerOrigin, code: String, query q: [String: String]) throws {
        guard let tv = q["tv"] else { throw Invalid.malformed("no tunnel version") }
        guard let v = Int(tv), String(v) == tv, v >= 1 else { throw Invalid.malformed("tunnel version \(tv)") }
        guard v == Self.version else { throw Invalid.newerVersion(tv) }
        guard let t = q["t"], TunnelTicket.isWellFormed(t) else { throw Invalid.malformed("the ticket") }
        guard let k = q["k"].flatMap(Self.key32) else { throw Invalid.malformed("the server key") }
        guard let p = q["p"].flatMap(Self.key32) else { throw Invalid.malformed("the pairing secret") }
        var token: String?
        if let r = q["r"] {
            guard TunnelRelay.isToken(r) else { throw Invalid.malformed("the relay token") }
            token = r
        }
        var required = false
        switch q["rq"] {
        case nil, "0"?: required = false
        case "1"?: required = true
        case let other?: throw Invalid.malformed("rq=\(other)")
        }
        self.init(server: server, code: code, ticket: t, serverKey: k, psk: p, relayToken: token, required: required)
    }

    /// 32 bytes, base64url without padding (exactly 43 characters).
    public static func key32(_ s: String) -> Data? {
        guard s.utf8.count == 43, !s.contains("="), let d = Base64URL.decode(s), d.count == 32 else { return nil }
        return d
    }

    /// The link as the QR code holds it.
    public var string: String {
        var s = "xbin://enroll?u=\(DeepLink.encodeQueryValue(server.origin))&c=\(DeepLink.encodeQueryValue(code))"
        s += "&tv=\(Self.version)&t=\(DeepLink.encodeQueryValue(ticket))"
        s += "&k=\(Base64URL.encode(serverKey))&p=\(Base64URL.encode(psk))"
        if let r = relayToken { s += "&r=\(DeepLink.encodeQueryValue(r))" }
        if required { s += "&rq=1" }
        return s
    }

    /// The selector's input (§4): `"xbin-pair-select-v1\n" ‖ code`; the
    /// selector is its SHA-256, first 16 bytes (`NoiseTunnel.selector`).
    public var selectorInput: Data { Data("xbin-pair-select-v1\n".utf8) + Data(code.utf8) }
}

/// An iroh EndpointTicket as text: `endpoint` and lowercase base32 (RFC 4648,
/// no padding) of its bytes. The app checks the shape only; IrohLib parses
/// it (`EndpointTicket.parse`) and the connection is refused unless the
/// remote's id is the ticket's.
public enum TunnelTicket {
    public static let prefix = "endpoint"

    /// `endpoint` + base32 that decodes to at least an EndpointId's 32 bytes;
    /// at most 1 KiB (the QR code's ticket holds a relay URL, no addresses).
    public static func isWellFormed(_ s: String) -> Bool {
        guard s.utf8.count <= 1024, s.lowercased().hasPrefix(prefix) else { return false }
        guard let bytes = base32Decode(s.dropFirst(prefix.count)) else { return false }
        return bytes.count >= 32
    }

    /// RFC 4648 base32 without padding, either case. Nil for anything else.
    public static func base32Decode<S: StringProtocol>(_ s: S) -> Data? {
        var out = Data()
        var buffer: UInt32 = 0
        var bits = 0
        for u in s.utf8 {
            let v: UInt32
            switch u {
            case UInt8(ascii: "a")...UInt8(ascii: "z"): v = UInt32(u - UInt8(ascii: "a"))
            case UInt8(ascii: "A")...UInt8(ascii: "Z"): v = UInt32(u - UInt8(ascii: "A"))
            case UInt8(ascii: "2")...UInt8(ascii: "7"): v = UInt32(u - UInt8(ascii: "2")) + 26
            default: return nil
            }
            buffer = (buffer << 5) | v
            bits += 5
            if bits >= 8 {
                bits -= 8
                out.append(UInt8((buffer >> UInt32(bits)) & 0xFF))
            }
        }
        // Leftover bits are padding: fewer than 5, and zero.
        guard bits < 5, buffer & ((1 << UInt32(bits)) - 1) == 0 else { return nil }
        return out.isEmpty ? nil : out
    }
}

// MARK: - The confirmation code (§5)

/// The six digits both sides derive from the handshake hash. The HKDF is
/// CryptoKit's (`NoiseTunnel.sasOKM`); this is the arithmetic and the
/// display.
public enum TunnelSAS {
    /// `big-endian uint64(okm) mod 1 000 000`; nil unless `okm` is 8 bytes.
    public static func number(_ okm: Data) -> Int? {
        guard okm.count == 8 else { return nil }
        var n: UInt64 = 0
        for b in okm { n = n << 8 | UInt64(b) }
        return Int(n % 1_000_000)
    }

    /// Six digits, zero-padded and grouped: `012 345`.
    public static func digits(_ okm: Data) -> String? {
        number(okm).map(grouped)
    }

    /// `n` (0…999 999) as `012 345`.
    public static func grouped(_ n: Int) -> String {
        let s = String(n % 1_000_000)
        let six = String(repeating: "0", count: 6 - s.count) + s
        return six.prefix(3) + " " + six.suffix(3)
    }

    /// A code as a server sends it — `"012345"`, `"012 345"` or a number —
    /// grouped; nil for anything else.
    public static func parse(_ v: JSONValue?) -> String? {
        if let n = v?.intValue, (0..<1_000_000).contains(n) { return grouped(Int(n)) }
        guard let s = v?.stringValue else { return nil }
        let d = s.filter { $0 != " " }
        guard d.count == 6, d.allSatisfy({ $0.isASCII && $0.isNumber }), let n = Int(d) else { return nil }
        return grouped(n)
    }

    /// What VoiceOver reads: the digits one by one ("0 1 2, 3 4 5").
    public static func spoken(_ grouped: String) -> String {
        grouped.split(separator: " ").map { $0.map(String.init).joined(separator: " ") }.joined(separator: ", ")
    }
}

// MARK: - Relays and the client config (§6.3, §7)

/// An iroh relay: n0's (no URL) or a custom one, with its access token.
public struct TunnelRelay: Sendable, Hashable, Codable {
    /// nil = n0's public relays.
    public var url: String?
    public var token: String?

    public init(url: String? = nil, token: String? = nil) {
        self.url = url
        self.token = token
    }

    public init(json: JSONValue?) {
        url = json?["url"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
        token = json?["token"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
    }

    /// A relay token as xbind accepts one: 1–256 printable ASCII
    /// characters, no white space.
    public static func isToken(_ s: String) -> Bool {
        !s.isEmpty && s.utf8.count <= 256 && s.utf8.allSatisfy { (0x21...0x7E).contains($0) }
    }

    /// The relay's host, for status lines ("relay.example.com"); nil for n0's.
    public var host: String? { url.flatMap { URLComponents(string: $0)?.host } }
}

/// The tunnel's coordinates as xbind gives them: in the pairing's
/// `enrolled` message (`tunnel`), and from `GET /api/xbin/app-tunnel/client`
/// after every connect — which also carries the server's EndpointId and
/// Noise key and, during a relay rotation, the `next` relay to try when the
/// current one refuses.
public struct TunnelClientConfig: Sendable, Equatable {
    public var ticket: String
    public var relay: TunnelRelay
    public var next: TunnelRelay?
    public var required: Bool
    /// The server's EndpointId (hex); absent in the `enrolled` message.
    public var endpointId: String?
    /// The server's Noise static key; absent in the `enrolled` message.
    public var serverKey: Data?

    public init(ticket: String, relay: TunnelRelay = TunnelRelay(), next: TunnelRelay? = nil, required: Bool = false,
                endpointId: String? = nil, serverKey: Data? = nil) {
        self.ticket = ticket
        self.relay = relay
        self.next = next
        self.required = required
        self.endpointId = endpointId
        self.serverKey = serverKey
    }

    public init(json: JSONValue) throws {
        guard let t = json["ticket"]?.stringValue, TunnelTicket.isWellFormed(t) else {
            throw DeviceLogin.Invalid(reason: "tunnel config without a ticket")
        }
        ticket = t
        relay = TunnelRelay(json: json["relay"])
        next = json["relay"]?["next"].flatMap { $0.objectValue == nil ? nil : TunnelRelay(json: $0) }
        required = json["required"]?.boolValue ?? false
        endpointId = json["endpointId"]?.stringValue.flatMap { $0.isEmpty ? nil : $0.lowercased() }
        if let k = json["serverKey"]?.stringValue {
            guard let d = TunnelLink.key32(k) else { throw DeviceLogin.Invalid(reason: "tunnel config with a bad server key") }
            serverKey = d
        }
    }

    /// `GET /api/xbin/app-tunnel/client` (the device's session, over the tunnel).
    public static let path = "/api/xbin/app-tunnel/client"
}

// MARK: - What a record keeps (§5 of the iOS plan)

/// A workspace's tunnel, as its record keeps it: nothing secret. The keys,
/// the ticket (it may name the server's addresses) and the relay token are
/// in the Keychain item `dev.xbin.tunnel` (``TunnelSecrets``), which is the
/// source of truth: an app that dropped this field (an older one, saving
/// the list) gets it back from there.
public struct TunnelInfo: Sendable, Hashable {
    /// xbind's iroh EndpointId (hex).
    public var endpoint: String
    /// The custom relay's URL; nil = n0's public relays.
    public var relay: String?
    /// The workspace refuses this device off the tunnel.
    public var required: Bool
    /// The loopback forwarder's port, kept across launches (pages' origins).
    public var port: Int?

    public init(endpoint: String, relay: String? = nil, required: Bool = false, port: Int? = nil) {
        self.endpoint = endpoint
        self.relay = relay
        self.required = required
        self.port = port
    }
}

extension TunnelInfo: Codable {
    enum CodingKeys: String, CodingKey { case endpoint, relay, required, port }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        endpoint = try c.decode(String.self, forKey: .endpoint)
        relay = try c.decodeIfPresent(String.self, forKey: .relay)
        required = try c.decodeIfPresent(Bool.self, forKey: .required) ?? false
        port = try c.decodeIfPresent(Int.self, forKey: .port)
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(endpoint, forKey: .endpoint)
        try c.encodeIfPresent(relay, forKey: .relay)
        try c.encode(required, forKey: .required)
        try c.encodeIfPresent(port, forKey: .port)
    }
}

/// The Keychain item of one workspace's tunnel (service ``service``,
/// account = the workspace id, this device only): the device's iroh secret
/// and Noise static key — one pair per workspace, so two workspaces can't
/// link the device — and what it needs to reach the server.
public struct TunnelSecrets: Sendable, Equatable {
    public static let service = "dev.xbin.tunnel"
    public static let currentVersion = 1

    /// The device's iroh secret key (Ed25519 seed, 32 bytes).
    public var irohSecret: Data
    /// The device's Noise static private key (X25519, 32 bytes).
    public var noiseStatic: Data
    /// The server's Noise static public key.
    public var serverKey: Data
    public var ticket: String
    public var relay: TunnelRelay
    /// A relay to try when ``relay`` refuses (a rotation in progress).
    public var next: TunnelRelay?
    /// The server's EndpointId (hex); empty until a connection learned it.
    public var endpoint: String
    public var required: Bool

    public init(irohSecret: Data, noiseStatic: Data, serverKey: Data, ticket: String, relay: TunnelRelay = TunnelRelay(),
                next: TunnelRelay? = nil, endpoint: String = "", required: Bool = false) {
        self.irohSecret = irohSecret
        self.noiseStatic = noiseStatic
        self.serverKey = serverKey
        self.ticket = ticket
        self.relay = relay
        self.next = next
        self.endpoint = endpoint
        self.required = required
    }

    /// Takes what the workspace says now (after a connect, or `enrolled`).
    /// `required` only ever comes from the workspace over the tunnel.
    public mutating func apply(_ c: TunnelClientConfig) {
        ticket = c.ticket
        relay = c.relay
        next = c.next
        required = c.required
        if let e = c.endpointId { endpoint = e }
        if let k = c.serverKey { serverKey = k }
    }

    /// What the record keeps of it (with the record's port).
    public func info(port: Int?) -> TunnelInfo {
        TunnelInfo(endpoint: endpoint, relay: relay.url, required: required, port: port)
    }

    public func encoded() -> Data { json.jsonData }

    public var json: JSONValue {
        var o: [String: JSONValue] = [
            "v": .int(Int64(Self.currentVersion)),
            "irohSecret": .string(Base64URL.encode(irohSecret)),
            "noiseStatic": .string(Base64URL.encode(noiseStatic)),
            "serverNoiseKey": .string(Base64URL.encode(serverKey)),
            "ticket": .string(ticket),
            "endpoint": .string(endpoint),
            "required": .bool(required),
        ]
        if let u = relay.url { o["relay"] = .string(u) }
        if let t = relay.token { o["relayToken"] = .string(t) }
        if let n = next {
            var no: [String: JSONValue] = [:]
            if let u = n.url { no["url"] = .string(u) }
            if let t = n.token { no["token"] = .string(t) }
            o["next"] = .object(no)
        }
        return .object(o)
    }

    /// Reads the item; nil for a damaged or newer one (the caller treats the
    /// workspace as not tunnelled and offers to pair again).
    public init?(data: Data) {
        guard let j = try? JSONValue(parsing: data), let v = j["v"]?.intValue, v == Int64(Self.currentVersion),
              let iroh = j["irohSecret"]?.stringValue.flatMap(TunnelLink.key32),
              let noise = j["noiseStatic"]?.stringValue.flatMap(TunnelLink.key32),
              let server = j["serverNoiseKey"]?.stringValue.flatMap(TunnelLink.key32),
              let ticket = j["ticket"]?.stringValue else { return nil }
        self.init(irohSecret: iroh, noiseStatic: noise, serverKey: server, ticket: ticket,
                  relay: TunnelRelay(url: j["relay"]?.stringValue, token: j["relayToken"]?.stringValue),
                  next: j["next"].flatMap { $0.objectValue == nil ? nil : TunnelRelay(json: $0) },
                  endpoint: j["endpoint"]?.stringValue ?? "", required: j["required"]?.boolValue ?? false)
    }
}

// MARK: - Pairing messages (§6)

/// The device's first data message on `pair/1`. Its Noise key and
/// EndpointId are the handshake's and iroh's; neither travels here.
public struct PairingHello: Sendable, Equatable {
    public var name: String
    public var platform: String
    /// The device-login P-256 key, SPKI DER.
    public var publicKeySPKI: Data

    public init(name: String, platform: String, publicKeySPKI: Data) {
        self.name = name
        self.platform = platform
        self.publicKeySPKI = publicKeySPKI
    }

    /// `{"v":1,"name","platform","publicKey"}` (the key base64url, as
    /// `POST /api/xbin/devices/enroll` takes it).
    public var json: JSONValue {
        ["v": 1, "name": .string(name), "platform": .string(platform), "publicKey": .string(Base64URL.encode(publicKeySPKI))]
    }

    public var data: Data { json.jsonData }
}

/// Why xbind ended a pairing (`{"type":"rejected","reason"}`).
public enum PairingRejection: Sendable, Equatable {
    /// "They don't" on the computer: the code is burned.
    case mismatch
    /// The code, or the confirmation window, ran out.
    case expired
    /// The person is at their device limit.
    case limit
    /// The pairing was refused (an unknown or spent code, a disabled account).
    case refused
    case other(String)

    public init(_ s: String) {
        switch s {
        case "mismatch": self = .mismatch
        case "expired": self = .expired
        case "limit": self = .limit
        case "refused": self = .refused
        default: self = .other(s)
        }
    }
}

/// `enrolled`: the device exists; sign in with device login over the tunnel.
public struct PairingEnrolled: Sendable, Equatable {
    public var deviceId: String
    public var user: WorkspaceUser
    /// What device login signs (never a loopback address).
    public var origin: String
    public var name: String
    public var tunnel: TunnelClientConfig

    public init(deviceId: String, user: WorkspaceUser, origin: String, name: String, tunnel: TunnelClientConfig) {
        self.deviceId = deviceId
        self.user = user
        self.origin = origin
        self.name = name
        self.tunnel = tunnel
    }

    /// As the classic enrollment's answer (``WorkspaceRecord/enrolled(_:name:)``).
    public var enrollment: DeviceEnrollment {
        DeviceEnrollment(deviceId: deviceId, user: user.id, origin: origin, name: name)
    }
}

/// One message from xbind on `pair/1`.
public enum PairingReply: Sendable, Equatable {
    /// The computer now shows the code; it waits until `expires`.
    case await(expires: Date?)
    case enrolled(PairingEnrolled)
    case rejected(PairingRejection, message: String)

    public init(data: Data) throws {
        try self.init(json: JSONValue(parsing: data))
    }

    public init(json j: JSONValue) throws {
        switch j["type"]?.stringValue {
        case "await"?:
            self = .await(expires: j["expires"]?.stringValue.flatMap(Self.date))
        case "enrolled"?:
            guard let id = j["deviceId"]?.stringValue, !id.isEmpty else {
                throw DeviceLogin.Invalid(reason: "pairing: enrolled without a deviceId")
            }
            guard let origin = j["origin"]?.stringValue, !origin.isEmpty else {
                throw DeviceLogin.Invalid(reason: "pairing: enrolled without an origin")
            }
            guard let t = j["tunnel"] else { throw DeviceLogin.Invalid(reason: "pairing: enrolled without its tunnel") }
            let u = j["user"]
            let uid = u?["id"]?.stringValue ?? u?.stringValue ?? ""
            let uname = u?["name"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
            self = .enrolled(PairingEnrolled(deviceId: id, user: WorkspaceUser(id: uid, name: uname), origin: origin,
                                             name: j["name"]?.stringValue ?? "", tunnel: try TunnelClientConfig(json: t)))
        case "rejected"?:
            self = .rejected(PairingRejection(j["reason"]?.stringValue ?? ""), message: j["error"]?.stringValue ?? "")
        default:
            throw DeviceLogin.Invalid(reason: "pairing: unknown message \(j["type"]?.stringValue ?? "?")")
        }
    }

    static func date(_ s: String) -> Date? {
        let f = ISO8601DateFormatter()
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: s)
    }
}

extension TunnelLink {
    /// The record of a device this link paired: requests go through the
    /// tunnel, ``WorkspaceRecord/server`` stays the link's `u`, device login
    /// signs the `enrolled` origin. `serverEndpoint`: xbind's EndpointId as
    /// iroh authenticated it.
    public func record(_ e: PairingEnrolled, id: String, deviceName: String, serverEndpoint: Data,
                       port: Int? = nil) -> WorkspaceRecord {
        var r = WorkspaceRecord(id: id, server: server, user: e.user)
        r.enrolled(e.enrollment, name: deviceName)
        r.tunnel = TunnelInfo(endpoint: TunnelUpgrade.hex(serverEndpoint), relay: e.tunnel.relay.url,
                              required: required || e.tunnel.required, port: port)
        return r
    }
}

/// How a pairing ended without a device (the phone's side). The app turns
/// it into a ``ConnectProblem``.
public enum PairingFailure: Error, Sendable, Equatable, CustomStringConvertible {
    /// xbind said no (`rejected`).
    case rejected(PairingRejection, message: String)
    /// No answer from the computer in time.
    case timedOut
    /// The Noise handshake failed: a wrong or spent code (the psk), a
    /// damaged link, or not this workspace.
    case handshake(String)
    /// The tunnel couldn't be reached, or the connection dropped.
    case unreachable(String)
    /// xbind broke the protocol.
    case protocolError(String)

    public var description: String {
        switch self {
        case .rejected(let r, let m): return "pairing rejected (\(r)): \(m)"
        case .timedOut: return "pairing timed out"
        case .handshake(let m): return "pairing handshake failed: \(m)"
        case .unreachable(let m): return "tunnel unreachable: \(m)"
        case .protocolError(let m): return "pairing protocol error: \(m)"
        }
    }
}

// MARK: - The computer's side, from the app (Add another device)

/// A pairing the app minted for another device (it is "the computer"):
/// `GET /api/xbin/devices/pairing/{id}` →
/// `{state, sas?, device {name, platform}, expires}`, polled until the code
/// appears; then `POST …/confirm {match}`.
public struct PairingStatus: Sendable, Equatable {
    public enum State: Sendable, Equatable {
        /// Minted, or the new device is mid-handshake: no code yet.
        case waiting
        /// The new device sent its key: compare the codes.
        case confirm
        case done
        case rejected
        /// The new device went away before the confirmation.
        case abandoned
        case expired
        case other(String)

        public init(_ s: String) {
            switch s {
            case "minted", "pending", "waiting", "handshaken": self = .waiting
            case "confirm": self = .confirm
            case "done", "enrolled", "confirmed": self = .done
            case "rejected", "declined": self = .rejected
            case "abandoned": self = .abandoned
            case "expired": self = .expired
            default: self = .other(s)
            }
        }

        /// Nothing more will happen: stop polling.
        public var isFinal: Bool {
            switch self {
            case .waiting, .confirm: return false
            default: return true
            }
        }
    }

    public var state: State
    /// The code, grouped (`012 345`), once the device's key arrived.
    public var sas: String?
    public var deviceName: String?
    public var devicePlatform: String?
    public var expires: Date?
    /// The person is at their device limit: confirming must name a device to
    /// replace.
    public var atLimit: Bool

    public init(state: State, sas: String? = nil, deviceName: String? = nil, devicePlatform: String? = nil,
                expires: Date? = nil, atLimit: Bool = false) {
        self.state = state
        self.sas = sas
        self.deviceName = deviceName
        self.devicePlatform = devicePlatform
        self.expires = expires
        self.atLimit = atLimit
    }

    public init(json j: JSONValue) {
        state = State(j["state"]?.stringValue ?? "")
        sas = TunnelSAS.parse(j["sas"])
        deviceName = j["device"]?["name"]?.stringValue
        devicePlatform = j["device"]?["platform"]?.stringValue
        if let n = j["expires"]?.intValue {
            expires = Date(timeIntervalSince1970: Double(n))
        } else {
            expires = j["expires"]?.stringValue.flatMap(PairingReply.date)
        }
        atLimit = j["atLimit"]?.boolValue ?? (j["limit"]?.objectValue != nil)
    }

    public static func path(_ id: String) -> String { "/api/xbin/devices/pairing/\(URLComponent.encode(id))" }

    public static func poll(_ id: String) -> APIRequest { APIRequest("GET", path(id)) }

    /// `POST …/confirm {match}`, naming the device to replace at the limit.
    public static func confirm(_ id: String, match: Bool, replace: String? = nil) -> APIRequest {
        var body: [String: JSONValue] = ["match": .bool(match)]
        if let replace { body["replace"] = .string(replace) }
        return .json("POST", path(id) + "/confirm", .object(body))
    }
}

// MARK: - Upgrading an enrolled device (§9)

public enum TunnelUpgrade {
    /// `POST /api/xbin/devices/{id}/tunnel {noiseKey, endpoint}`: this
    /// device's own fresh session, over a direct connection; the answer is
    /// ``TunnelClientConfig``'s.
    public static func request(deviceId: String, noiseKey: Data, endpoint: Data) -> APIRequest {
        .json("POST", "\(AppAuthRoute.devices)/\(URLComponent.encode(deviceId))/tunnel",
              ["noiseKey": .string(Base64URL.encode(noiseKey)), "endpoint": .string(hex(endpoint))])
    }

    /// Whether to offer it: the workspace has tunnels on, this record isn't
    /// tunnelled yet, and it is enrolled (it has a device to upgrade).
    public static func isDue(record: WorkspaceRecord, methods: LoginMethods?) -> Bool {
        guard record.tunnel == nil, record.deviceId != nil, let t = methods?.tunnel else { return false }
        return t.enabled
    }

    /// Lowercase hex (EndpointIds on the wire).
    public static func hex(_ d: Data) -> String {
        d.map { b in String(b, radix: 16).count == 1 ? "0" + String(b, radix: 16) : String(b, radix: 16) }.joined()
    }
}

// MARK: - Status and reconnecting

/// The tunnel's state for the Connection row and screen.
public enum TunnelStatus: Sendable, Equatable {
    /// The workspace isn't tunnelled: requests go to its address.
    case direct
    case connecting
    /// Up: a direct UDP path, or through a relay (its host; nil = n0's).
    case connected(path: Path, rtt: Double?)
    case failed(String)

    public enum Path: Sendable, Equatable {
        case direct
        case relay(host: String?)
    }

    /// "Tunnel · direct · 18 ms", "Tunnel · relay (relay.example.com) · 85
    /// ms", "Tunnel · connecting…", "Direct (https)".
    public func summary(server: ServerOrigin) -> String {
        switch self {
        case .direct: return "Direct (\(server.scheme))"
        case .connecting: return "Tunnel · connecting…"
        case .connected(let path, let rtt):
            var s = "Tunnel · "
            switch path {
            case .direct: s += "direct"
            case .relay(let host): s += host.map { "relay (\($0))" } ?? "relay"
            }
            if let rtt { s += " · \(Int((rtt * 1000).rounded())) ms" }
            return s
        case .failed(let why): return "Tunnel · not connected (\(why))"
        }
    }
}

/// When to try the tunnel again: exponential backoff while the app is in
/// front, nothing in the background (the app never keeps an endpoint there,
/// iroh #4289); coming back to the front tries at once.
public struct TunnelReconnectPolicy: Sendable, Equatable {
    public var base: Double
    public var maximum: Double
    public private(set) var failures = 0
    public private(set) var isActive: Bool

    public init(base: Double = 0.5, maximum: Double = 30, isActive: Bool = true) {
        self.base = base
        self.maximum = maximum
        self.isActive = isActive
    }

    /// The wait before attempt number `failures + 1`.
    public var nextDelay: Double {
        guard failures > 0 else { return 0 }
        return min(maximum, base * pow(2, Double(failures - 1)))
    }

    /// A connect failed: the delay before the next try; nil in the background.
    public mutating func failed() -> Double? {
        failures += 1
        return isActive ? nextDelay : nil
    }

    public mutating func succeeded() { failures = 0 }

    /// Foreground (true) or background. Returning to the front resets the
    /// backoff: the next try is now.
    public mutating func setActive(_ active: Bool) {
        if active, !isActive { failures = 0 }
        isActive = active
    }
}

// MARK: - Byte streams

/// A bidirectional byte pipe: an iroh bi-stream, a loopback TCP connection,
/// an in-memory pair in tests. Reads return what arrived (at most `max`
/// bytes); an empty result is the peer's end of stream (it called
/// ``finish()``). Errors are the transport's.
public protocol ByteStream: Sendable {
    func read(max: Int) async throws -> Data
    func write(_ data: Data) async throws
    /// Half-close: this side writes nothing more.
    func finish() async throws
    /// Aborts both directions (a reset); pending reads end with an error.
    func cancel() async
}
