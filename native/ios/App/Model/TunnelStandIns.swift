// STAND-INS — DELETE THIS FILE WHEN MERGING apptunnel/i2 (D204).
//
// Worker I2 owns the tunnel's protocol types (plans/app-tunnel.md Appendix B
// §4 Phase 2): XbinCore `Client/Tunnel.swift` (ByteStream, TunnelRelay,
// TunnelClientConfig, TunnelInfo, TunnelSecrets, TunnelStatus,
// TunnelReconnectPolicy), the record's `tunnel` field and
// `isEncryptedInTransit` (WorkspaceRecord.swift), and the xbin layer — Noise
// IK and `TunnelSplice` — in `Shared/NoiseTunnel.swift`. The iroh runtime
// (TunnelEndpoint, LoopbackForwarder, TunnelTransport, TunnelManager,
// TunnelKeyStore) is written against those names as apptunnel/i2 declares
// them (its working tree, 2026-10-27); the declarations below are the subset
// it uses, so this branch builds before that one lands.
//
// After the merge: delete this file. The record's `tunnel` below is a
// computed stand-in, so the merged stored property fails to compile beside
// it (an invalid redeclaration) until it goes. Then point `TunnelXbinLayer`
// (the one function here that isn't I2's) at NoiseTunnel's IK initiator and
// TunnelSplice, and move it into LoopbackForwarder.swift.
import Foundation
import XbinCore

/// A bidirectional byte pipe (I2: XbinCore Client/Tunnel.swift).
protocol ByteStream: Sendable {
    func read(max: Int) async throws -> Data
    func write(_ data: Data) async throws
    /// Half-close: this side writes nothing more.
    func finish() async throws
    /// Aborts both directions (a reset); pending reads end with an error.
    func cancel() async
}

/// A relay as xbind names it (I2).
struct TunnelRelay: Sendable, Hashable, Codable {
    var url: String?
    var token: String?

    init(url: String? = nil, token: String? = nil) {
        self.url = url
        self.token = token
    }

    init(json: JSONValue?) {
        url = json?["url"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
        token = json?["token"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
    }

    var host: String? { url.flatMap { URLComponents(string: $0)?.host } }
}

/// `GET /api/xbin/app-tunnel/client` (I2).
struct TunnelClientConfig: Sendable, Equatable {
    var ticket: String
    var relay: TunnelRelay
    var next: TunnelRelay?
    var required: Bool
    var endpointId: String?
    var serverKey: Data?

    init(json: JSONValue) throws {
        guard let t = json["ticket"]?.stringValue, t.lowercased().hasPrefix("endpoint") else {
            throw APIError(status: 502, message: "tunnel config without a ticket")
        }
        ticket = t
        relay = TunnelRelay(json: json["relay"])
        next = json["relay"]?["next"].flatMap { $0.objectValue == nil ? nil : TunnelRelay(json: $0) }
        required = json["required"]?.boolValue ?? false
        endpointId = json["endpointId"]?.stringValue.flatMap { $0.isEmpty ? nil : $0.lowercased() }
        serverKey = json["serverKey"]?.stringValue.flatMap { Base64URL.decode($0) }.flatMap { $0.count == 32 ? $0 : nil }
    }

    static let path = "/api/xbin/app-tunnel/client"
}

/// What a record keeps of its tunnel (I2).
struct TunnelInfo: Sendable, Hashable, Codable {
    var endpoint: String
    var relay: String?
    var required: Bool
    var port: Int?

    init(endpoint: String, relay: String? = nil, required: Bool = false, port: Int? = nil) {
        self.endpoint = endpoint
        self.relay = relay
        self.required = required
        self.port = port
    }
}

/// The Keychain item `dev.xbin.tunnel` (I2).
struct TunnelSecrets: Sendable, Equatable {
    static let service = "dev.xbin.tunnel"
    static let currentVersion = 1

    var irohSecret: Data
    var noiseStatic: Data
    var serverKey: Data
    var ticket: String
    var relay: TunnelRelay
    var next: TunnelRelay?
    var endpoint: String
    var required: Bool

    init(irohSecret: Data, noiseStatic: Data, serverKey: Data, ticket: String, relay: TunnelRelay = TunnelRelay(),
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

    mutating func apply(_ c: TunnelClientConfig) {
        ticket = c.ticket
        relay = c.relay
        next = c.next
        required = c.required
        if let e = c.endpointId { endpoint = e }
        if let k = c.serverKey { serverKey = k }
    }

    func info(port: Int?) -> TunnelInfo { TunnelInfo(endpoint: endpoint, relay: relay.url, required: required, port: port) }

    func encoded() -> Data {
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
        return JSONValue.object(o).jsonData
    }

    init?(data: Data) {
        func key(_ v: JSONValue?) -> Data? { v?.stringValue.flatMap(Base64URL.decode).flatMap { $0.count == 32 ? $0 : nil } }
        guard let j = try? JSONValue(parsing: data), j["v"]?.intValue == Int64(Self.currentVersion),
              let iroh = key(j["irohSecret"]), let noise = key(j["noiseStatic"]), let server = key(j["serverNoiseKey"]),
              let ticket = j["ticket"]?.stringValue else { return nil }
        self.init(irohSecret: iroh, noiseStatic: noise, serverKey: server, ticket: ticket,
                  relay: TunnelRelay(url: j["relay"]?.stringValue, token: j["relayToken"]?.stringValue),
                  endpoint: j["endpoint"]?.stringValue ?? "", required: j["required"]?.boolValue ?? false)
    }
}

/// The tunnel's state for the Connection row and screen (I2).
enum TunnelStatus: Sendable, Equatable {
    case direct
    case connecting
    case connected(path: Path, rtt: Double?)
    case failed(String)

    enum Path: Sendable, Equatable {
        case direct
        case relay(host: String?)
    }

    func summary(server: ServerOrigin) -> String {
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

/// Backoff while in front, nothing in the background (I2).
struct TunnelReconnectPolicy: Sendable, Equatable {
    var base: Double
    var maximum: Double
    private(set) var failures = 0
    private(set) var isActive: Bool

    init(base: Double = 0.5, maximum: Double = 30, isActive: Bool = true) {
        self.base = base
        self.maximum = maximum
        self.isActive = isActive
    }

    var nextDelay: Double {
        guard failures > 0 else { return 0 }
        return min(maximum, base * pow(2, Double(failures - 1)))
    }

    mutating func failed() -> Double? {
        failures += 1
        return isActive ? nextDelay : nil
    }

    mutating func succeeded() { failures = 0 }

    mutating func setActive(_ active: Bool) {
        if active, !isActive { failures = 0 }
        isActive = active
    }
}

extension WorkspaceRecord {
    /// I2: a stored, Codable field of the record. Stand-in: kept beside the
    /// record in memory only (nothing pairs yet without apptunnel/i2; the
    /// Debug forwarder sets it).
    var tunnel: TunnelInfo? {
        get { TunnelRecordStandIn.shared.get(id) }
        set { TunnelRecordStandIn.shared.set(id, newValue) }
    }

    /// I2: through its tunnel always, else as its address says.
    var isEncryptedInTransit: Bool { tunnel != nil || server.isEncryptedInTransit }
}

private final class TunnelRecordStandIn: @unchecked Sendable {
    static let shared = TunnelRecordStandIn()
    private let lock = NSLock()
    private var infos: [String: TunnelInfo] = [:]
    func get(_ id: String) -> TunnelInfo? { lock.lock(); defer { lock.unlock() }; return infos[id] }
    func set(_ id: String, _ v: TunnelInfo?) { lock.lock(); infos[id] = v; lock.unlock() }
}

/// The xbin layer on one tunnel stream (spec §4). I2's
/// `Shared/NoiseTunnel.swift` provides both halves — its IK initiator and
/// `TunnelSplice` — and these two calls become those; until it lands the
/// handshake refuses, so a real tunnel answers the forwarder's 502 page.
enum TunnelXbinLayer {
    struct Unavailable: Error, CustomStringConvertible {
        var description: String { "this build has no xbin tunnel layer yet (D204 phase 2)" }
    }

    /// The transport keys after a completed IK handshake.
    struct Session: Sendable {}

    /// Noise IK as initiator on `remote`: prologue `xbin-tunnel-v1` ‖ server
    /// EndpointId ‖ device EndpointId (`prologue`), both payloads empty.
    static func handshake(_ remote: any ByteStream, deviceStatic: Data, serverStatic: Data, prologue: Data) async throws -> Session {
        throw Unavailable()
    }

    /// `local` pumped through the session until both directions end.
    static func splice(local: any ByteStream, remote: any ByteStream, session: Session) async throws {
        throw Unavailable()
    }
}
