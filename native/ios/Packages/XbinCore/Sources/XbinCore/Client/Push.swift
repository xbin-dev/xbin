import Foundation

// Push notifications, the device side (native/spec/push.md; plans/native.md
// §14): the relay handle, the registration with xbind, keeping it working,
// and — for the Notification Service Extension — the envelope and payload.
// The crypto (X25519, HKDF, AES-GCM) is CryptoKit in the app and extension;
// everything around it is here.

/// `{"v":1,"epk","n","ct"}` — base64url fields, decoded.
public struct PushEnvelope: Sendable, Equatable {
    public var epk: Data
    public var nonce: Data
    /// Ciphertext followed by the 16-byte GCM tag.
    public var sealed: Data

    public init(epk: Data, nonce: Data, sealed: Data) {
        self.epk = epk
        self.nonce = nonce
        self.sealed = sealed
    }

    /// From `userInfo["xbin"]` (any Foundation JSON object) or its JSON.
    /// nil unless `v` is 1 and every field decodes to the right size.
    public init?(json: JSONValue) {
        guard json["v"]?.intValue == 1,
              let e = json["epk"]?.stringValue.flatMap(Base64URL.decode), e.count == 32,
              let n = json["n"]?.stringValue.flatMap(Base64URL.decode), n.count == 12,
              let c = json["ct"]?.stringValue.flatMap(Base64URL.decode), c.count >= 16
        else { return nil }
        self.init(epk: e, nonce: n, sealed: c)
    }

    public var ciphertext: Data { sealed.dropLast(16) }
    public var tag: Data { sealed.suffix(16) }

    /// The HKDF `info` of v1.
    public static let info = Data("xbin-push-v1".utf8)

    /// HKDF salt: `E || R` (the ephemeral key, then the device's public key).
    public func salt(recipientPublicKey r: Data) -> Data { epk + r }
}

/// The decrypted payload.
public struct PushPayload: Sendable, Equatable {
    public enum Kind: Sendable, Equatable {
        case agentPermission, agentQuestion, agentTurn, tile(String?), test, other(String)

        public init(_ raw: String) {
            switch raw {
            case "agent.permission": self = .agentPermission
            case "agent.question": self = .agentQuestion
            case "agent.turn": self = .agentTurn
            case "test": self = .test
            case "tile": self = .tile(nil)
            default:
                if raw.hasPrefix("tile.") { self = .tile(String(raw.dropFirst(5))) } else { self = .other(raw) }
            }
        }

        /// The notification category (actions per kind).
        public var category: String {
            switch self {
            case .agentPermission, .agentQuestion: return "xbin.agent.needs-you"
            case .agentTurn: return "xbin.agent.turn"
            case .tile, .other: return "xbin.tile"
            case .test: return "xbin.test"
            }
        }

        /// Needs the user's answer (goes to the Needs-you inbox).
        public var needsYou: Bool { self == .agentPermission || self == .agentQuestion }
    }

    public var ws: String
    public var kind: String
    public var title: String
    public var body: String
    public var link: String
    public var collapseID: String

    public init(ws: String, kind: String, title: String, body: String = "", link: String = "", collapseID: String = "") {
        self.ws = ws
        self.kind = kind
        self.title = title
        self.body = body
        self.link = link
        self.collapseID = collapseID
    }

    /// From the decrypted bytes; nil unless `v` is 1 (unknown fields ignored).
    public init?(plaintext: Data) {
        guard let j = try? JSONValue(parsing: plaintext), j["v"]?.intValue == 1,
              let ws = j["ws"]?.stringValue, !ws.isEmpty else { return nil }
        self.init(ws: ws, kind: j["kind"]?.stringValue ?? "", title: j["title"]?.stringValue ?? "",
                  body: j["body"]?.stringValue ?? "", link: j["link"]?.stringValue ?? "",
                  collapseID: j["collapseId"]?.stringValue ?? "")
    }

    public var typedKind: Kind { Kind(kind) }

    /// The deep link to open: `xbin://<app workspace id>/<link>` (§4), or
    /// the workspace itself for an empty or unusable link.
    public func deepLink(appWorkspace: String) -> DeepLink {
        let base = DeepLink.workspace(appWorkspace)
        let l = link.trimmingCharacters(in: .whitespaces)
        guard !l.isEmpty, !l.hasPrefix("/"), !l.contains("://") else { return base }
        return (try? DeepLink(string: "\(DeepLink.scheme)://\(appWorkspace)/\(l)")) ?? base
    }

    /// `userInfo` the extension attaches for the app (`{ws, kind, link}`).
    public var userInfo: [String: String] { ["ws": ws, "kind": kind, "link": link] }
}

/// A registration as xbind reports it (`GET/POST /api/xbin/devices/push`).
public struct PushRegistration: Sendable, Equatable {
    public var deviceId: String
    public var kinds: [String]
    public var needsNewHandle: Bool
    public var relayError: String

    public init(deviceId: String, kinds: [String] = [], needsNewHandle: Bool = false, relayError: String = "") {
        self.deviceId = deviceId
        self.kinds = kinds
        self.needsNewHandle = needsNewHandle
        self.relayError = relayError
    }

    public init?(json: JSONValue) {
        guard let d = json["deviceId"]?.stringValue, !d.isEmpty else { return nil }
        self.init(deviceId: d, kinds: (json["kinds"]?.arrayValue ?? []).compactMap(\.stringValue),
                  needsNewHandle: json["needsNewHandle"]?.boolValue ?? false,
                  relayError: json["relayError"]?.stringValue ?? "")
    }
}

/// `GET /api/xbin/devices/push` → `{workspace, enabled, devices:[…]}`.
public struct PushStatus: Sendable, Equatable {
    public var workspace: String
    public var enabled: Bool
    public var devices: [PushRegistration]

    public init(workspace: String = "", enabled: Bool = false, devices: [PushRegistration] = []) {
        self.workspace = workspace
        self.enabled = enabled
        self.devices = devices
    }

    public init(json: JSONValue) {
        workspace = json["workspace"]?.stringValue ?? ""
        enabled = json["enabled"]?.boolValue ?? false
        var list = (json["devices"]?.arrayValue ?? []).compactMap(PushRegistration.init(json:))
        if list.isEmpty, let one = json["device"].flatMap(PushRegistration.init(json:)) { list = [one] }
        devices = list
    }
}

/// What the app keeps per workspace (Keychain, shared with the extension).
public struct PushState: Sendable, Equatable, Codable {
    /// The relay handle (one per workspace).
    public var handle: String?
    /// The relay it came from.
    public var relay: String?
    /// The APNs token it was registered with (hex).
    public var apnsToken: String?
    /// xbind's push id for the workspace (`ws` in payloads).
    public var pushWorkspace: String?
    /// The X25519 public key registered (base64url).
    public var publicKey: String?
    public var kinds: [String]

    public init(handle: String? = nil, relay: String? = nil, apnsToken: String? = nil, pushWorkspace: String? = nil,
                publicKey: String? = nil, kinds: [String] = []) {
        self.handle = handle
        self.relay = relay
        self.apnsToken = apnsToken
        self.pushWorkspace = pushWorkspace
        self.publicKey = publicKey
        self.kinds = kinds
    }
}

/// What to do for a workspace on launch/foreground (push.md §1.4).
public enum PushMaintenance: Sendable, Equatable {
    /// Nothing: registered, handle usable, token and key current.
    case none
    /// No handle yet (or the relay changed): `POST <relay>/v1/handles`, then register.
    case newHandle
    /// `needsNewHandle` (or the relay forgot it): a fresh handle — DELETE the
    /// old one — then register again.
    case replaceHandle(old: String)
    /// APNs issued a new token: `PUT <relay>/v1/handles/<h>`, then register.
    case updateToken(handle: String)
    /// The registration is missing or stale on xbind: register again.
    case register

    /// Decides from the local state, the current APNs token and relay, the
    /// key and kinds the app wants, and xbind's view (nil = unknown/offline).
    public static func plan(state: PushState, apnsToken: String, relay: String, publicKey: String, kinds: [String],
                            deviceId: String, server: PushStatus?) -> PushMaintenance {
        guard let handle = state.handle, state.relay == relay else { return .newHandle }
        let reg = server?.devices.first { $0.deviceId == deviceId }
        if let reg, reg.needsNewHandle { return .replaceHandle(old: handle) }
        if state.apnsToken != apnsToken { return .updateToken(handle: handle) }
        if server != nil, reg == nil { return .register }
        if state.publicKey != publicKey || Set(state.kinds) != Set(kinds) { return .register }
        if let reg, !kinds.isEmpty, Set(reg.kinds) != Set(kinds) { return .register }
        return .none
    }
}

/// The relay's HTTP (relay/README.md).
public enum PushRelayAPI {
    public static func newHandle(apnsToken: String, topic: String, production: Bool) -> APIRequest {
        .json("POST", "/v1/handles", body(apnsToken: apnsToken, topic: topic, production: production))
    }

    public static func updateHandle(_ handle: String, apnsToken: String, topic: String, production: Bool) -> APIRequest {
        .json("PUT", "/v1/handles/\(URLComponent.encode(handle))", body(apnsToken: apnsToken, topic: topic, production: production))
    }

    public static func deleteHandle(_ handle: String) -> APIRequest {
        APIRequest("DELETE", "/v1/handles/\(URLComponent.encode(handle))")
    }

    static func body(apnsToken: String, topic: String, production: Bool) -> JSONValue {
        ["apnsToken": .string(apnsToken), "topic": .string(topic), "env": .string(production ? "production" : "development")]
    }

    /// `{handle}` from `POST /v1/handles`.
    public static func handle(from json: JSONValue) -> String? {
        guard let h = json["handle"]?.stringValue, !h.isEmpty else { return nil }
        return h
    }

    /// The relay's `{error, code}` code (`handle_unknown`, …).
    public static func errorCode(_ r: APIResponse) -> String? { (try? r.json())?["code"]?.stringValue }
}

/// xbind's push routes for the device (a signed-in human only).
public enum PushAPI {
    public static let registrations = "/api/xbin/devices/push"
    public static let prefs = "/api/xbin/push/prefs"
    public static let test = "/api/xbin/push/test"

    public static func register(deviceId: String, handle: String, publicKey: String, kinds: [String]) -> APIRequest {
        var body: [String: JSONValue] = ["deviceId": .string(deviceId), "handle": .string(handle),
                                         "publicKey": .string(publicKey)]
        if !kinds.isEmpty { body["kinds"] = .array(kinds.map(JSONValue.string)) }
        return .json("POST", registrations, .object(body))
    }

    public static func unregister(deviceId: String) -> APIRequest {
        APIRequest("DELETE", "\(registrations)/\(URLComponent.encode(deviceId))")
    }

    public static func setMuted(_ tiles: [String]) -> APIRequest {
        .json("PUT", prefs, ["mutedTiles": .array(tiles.map(JSONValue.string))])
    }

    public static func mutedTiles(_ json: JSONValue) -> [String] {
        (json["mutedTiles"]?.arrayValue ?? []).compactMap(\.stringValue)
    }
}

/// The extension's choice of key: which of the app's workspaces a payload
/// is for. `open` tries one workspace's key (CryptoKit) and returns the
/// plaintext, or nil when the GCM tag doesn't verify.
public enum PushOpener {
    public struct Opened: Sendable, Equatable {
        public var appWorkspace: String
        public var payload: PushPayload
    }

    /// Candidates are (app workspace id, xbind push id). A payload counts
    /// only when its `ws` is the push id of the workspace whose key opened it.
    public static func open(_ env: PushEnvelope, candidates: [(appWorkspace: String, pushWorkspace: String?)],
                            open: (String) -> Data?) -> Opened? {
        for c in candidates {
            guard let plain = open(c.appWorkspace), let p = PushPayload(plaintext: plain) else { continue }
            if let expected = c.pushWorkspace, !expected.isEmpty, expected != p.ws { continue }
            return Opened(appWorkspace: c.appWorkspace, payload: p)
        }
        return nil
    }

    /// The relay's silent token check (`{"aps":{"content-available":1}}`, no
    /// `xbin`) — ignored.
    public static func isRelayProbe(_ userInfo: JSONValue) -> Bool {
        userInfo["xbin"] == nil && userInfo["aps"]?["content-available"]?.intValue == 1
            && userInfo["aps"]?["alert"] == nil
    }
}

/// APNs device tokens as the relay wants them: lowercase hex.
public enum APNsToken {
    public static func hex(_ token: Data) -> String { token.map { String(format: "%02x", $0) }.joined() }
}
