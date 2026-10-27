import Foundation
import IrohLib
import XbinCore

// The device's iroh endpoint for one tunnelled workspace (D204,
// native/spec/tunnel.md §3), IrohLib's objects kept inside an actor:
//
//   - preset Minimal (no n0 DNS, no pkarr publishing, no mDNS) with relay
//     mode custom: exactly the ticket's relay URL, plus its token;
//   - no ALPNs of its own: the device only dials, it is never dialed;
//   - it dials the ticket's address and refuses the connection unless
//     iroh's authenticated `remoteId` is the ticket's EndpointId;
//   - a new endpoint each time the app becomes active (iroh #4289: a UDP
//     socket is broken after a suspend), none kept in the background
//     (TunnelManager).
//
// Apple-only (IrohLib): app-stubcheck skips this file; its
// Stubs/App/AppStubs.swift declares what the checked files use of it.
// IrohLib 1.2.0's Swift names (IrohLib.swift): Endpoint.bind(options:),
// EndpointOptions(preset:secretKey:alpns:relayMode:), presetMinimal(),
// RelayMode.custom(map:), RelayMap.empty()/insert(config:),
// RelayConfig(url:quicPort:authToken:), EndpointTicket.fromString(str:)
// .endpointAddr(), Endpoint.connect(addr:alpn:), Connection.remoteId()
// .openBi()/.paths()/.rtt()/.closed()/.close(errorCode:reason:),
// BiStream.send()/.recv(), SendStream.writeAll(buf:)/.finish()/.reset(errorCode:),
// RecvStream.read(sizeLimit:) (empty at the end)/.stop(errorCode:),
// SecretKey.fromBytes(bytes:).public().toBytes().
actor TunnelEndpoint {
    static let tunnelALPN = Data("xbin/tunnel/1".utf8)
    static let pairALPN = Data("xbin/pair/1".utf8)

    /// Why a connection was refused or couldn't be made.
    struct Problem: Error, CustomStringConvertible, Equatable {
        var description: String
    }

    private let endpoint: Endpoint
    private var connection: Connection?
    /// The relay this endpoint uses (the ticket's).
    let relayURL: String
    /// The device's EndpointId (32 bytes), from its iroh secret.
    nonisolated let deviceID: Data

    private init(endpoint: Endpoint, relayURL: String, deviceID: Data) {
        self.endpoint = endpoint
        self.relayURL = relayURL
        self.deviceID = deviceID
    }

    /// The relay a ticket names, else `fallback` (the stored relay's URL).
    static func relay(of ticket: String, fallback: String?) throws -> String {
        let t: EndpointTicket
        do {
            t = try EndpointTicket.fromString(str: ticket)
        } catch {
            throw Problem(description: "the tunnel's ticket is damaged (\(error.localizedDescription))")
        }
        if let r = t.endpointAddr().relayUrl(), !r.isEmpty { return r }
        if let f = fallback, !f.isEmpty { return f }
        throw Problem(description: "the tunnel's ticket names no relay")
    }

    /// Binds a fresh endpoint with the device's iroh secret for this
    /// workspace: Minimal, the one relay (with its token), no ALPNs.
    static func bind(secret: Data, relayURL: String, relayToken: String?) async throws -> TunnelEndpoint {
        let key = try SecretKey.fromBytes(bytes: secret)
        let map = RelayMap.empty()
        try map.insert(config: RelayConfig(url: relayURL, quicPort: nil, authToken: relayToken))
        let options = EndpointOptions(preset: presetMinimal(), secretKey: secret, alpns: nil,
                                      relayMode: RelayMode.custom(map: map))
        let ep = try await Endpoint.bind(options: options)
        return TunnelEndpoint(endpoint: ep, relayURL: relayURL, deviceID: key.public().toBytes())
    }

    /// Dials the ticket's endpoint on `alpn`. Refused unless iroh's
    /// authenticated remote id is the ticket's — and `expect` (hex), when
    /// the device already knows the server's EndpointId. Returns the
    /// server's EndpointId (32 bytes).
    @discardableResult
    func connect(ticket: String, alpn: Data = TunnelEndpoint.tunnelALPN, expect: String? = nil) async throws -> Data {
        let addr = try EndpointTicket.fromString(str: ticket).endpointAddr()
        let want = addr.id().toBytes()
        if let e = expect, !e.isEmpty, e.lowercased() != Self.hex(want) {
            throw Problem(description: "the ticket names another server than this workspace's")
        }
        let c = try await endpoint.connect(addr: addr, alpn: alpn)
        let got = c.remoteId().toBytes()
        guard got == want else {
            try? c.close(errorCode: 1, reason: Data("wrong peer".utf8))
            throw Problem(description: "the tunnel answered as another server")
        }
        try? connection?.close(errorCode: 0, reason: Data())
        connection = c
        return got
    }

    /// One new bi-stream on the connection (one per forwarded TCP connection).
    func openBi() async throws -> any ByteStream {
        guard let c = connection else { throw Problem(description: "not connected") }
        let bi = try await c.openBi()
        return IrohByteStream(send: bi.send(), recv: bi.recv())
    }

    /// A connection of its own for one stream, `hello` written first: n0's
    /// dumbpipe serves one stream per connection (Debug's spike stand-in
    /// for the sidecar, TunnelDebug.dumbpipeTicket).
    func openDedicated(ticket: String, alpn: Data, hello: Data) async throws -> any ByteStream {
        let addr = try EndpointTicket.fromString(str: ticket).endpointAddr()
        let c = try await endpoint.connect(addr: addr, alpn: alpn)
        guard c.remoteId().toBytes() == addr.id().toBytes() else {
            try? c.close(errorCode: 1, reason: Data("wrong peer".utf8))
            throw Problem(description: "the tunnel answered as another server")
        }
        let bi = try await c.openBi()
        let s = IrohByteStream(send: bi.send(), recv: bi.recv(), connection: c)
        try await s.write(hello)
        return s
    }

    var isConnected: Bool { connection != nil && connection?.closeReason() == nil }

    /// Waits until the connection closes; its reason (nil: none was open).
    func closed() async -> String? {
        guard let c = connection else { return nil }
        return await c.closed()
    }

    /// The selected path: direct (UDP) or through the relay, and its RTT in
    /// seconds.
    func path() -> (path: TunnelStatus.Path, rtt: Double?)? {
        guard let c = connection else { return nil }
        let rtt = c.rtt().map { Double($0) / 1000 }
        guard let sel = c.paths().first(where: { $0.isSelected }) else { return (.relay(host: Self.host(relayURL)), rtt) }
        if sel.isRelay { return (.relay(host: Self.host(relayURL)), Double(sel.rttMs) / 1000) }
        return (.direct, Double(sel.rttMs) / 1000)
    }

    /// Closes the connection and the endpoint (never reused: iroh #4289).
    func close() async {
        try? connection?.close(errorCode: 0, reason: Data("bye".utf8))
        connection = nil
        try? await endpoint.close()
    }

    static func host(_ url: String) -> String? { URLComponents(string: url)?.host }

    static func hex(_ d: Data) -> String { d.map { String(format: "%02x", $0) }.joined() }

    /// The device's EndpointId for an iroh secret (32 bytes).
    static func endpointID(secret: Data) throws -> Data { try SecretKey.fromBytes(bytes: secret).public().toBytes() }

    /// A fresh iroh secret (32 bytes).
    static func newSecret() -> Data { SecretKey.generate().toBytes() }
}

/// An iroh bi-stream as a ByteStream.
final class IrohByteStream: ByteStream, @unchecked Sendable {
    private let send: SendStream
    private let recv: RecvStream
    /// A connection this stream owns (closed with it), if any.
    private let connection: Connection?

    init(send: SendStream, recv: RecvStream, connection: Connection? = nil) {
        self.send = send
        self.recv = recv
        self.connection = connection
    }

    func read(max: Int) async throws -> Data {
        try await recv.read(sizeLimit: UInt32(clamping: Swift.max(1, max)))
    }

    func write(_ data: Data) async throws { try await send.writeAll(buf: data) }

    func finish() async throws { try await send.finish() }

    func cancel() async {
        try? await send.reset(errorCode: 0)
        try? await recv.stop(errorCode: 0)
        try? connection?.close(errorCode: 0, reason: Data())
    }
}
