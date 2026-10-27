import Foundation
import IrohLib
import XbinCore

// The device's iroh endpoints (D204, native/spec/tunnel.md §3) behind
// XbinCore's TunnelDialer / TunnelConnection, IrohLib's objects kept inside:
//
//   - preset Minimal (no n0 DNS, no pkarr publishing, no mDNS) with relay
//     mode custom: exactly the ticket's relay URL, plus its token;
//   - no ALPNs of its own: the device only dials, it is never dialed;
//   - it dials the ticket's address and refuses the connection unless
//     iroh's authenticated `remoteId` is the ticket's EndpointId;
//   - one endpoint per connection, made fresh each time (TunnelManager makes
//     a connection each time the app becomes active, none in the
//     background: iroh #4289, a UDP socket broken after a suspend).
//
// Apple-only (IrohLib): app-stubcheck skips this file; its
// Stubs/App/AppStubs.swift declares what the checked files use of it.
// IrohLib 1.2.0's Swift names (IrohLib.swift): Endpoint.bind(options:),
// EndpointOptions(preset:secretKey:alpns:relayMode:), presetMinimal(),
// RelayMode.custom(map:), RelayMap.empty()/insert(config:),
// RelayConfig(url:quicPort:authToken:), EndpointTicket.fromString(str:)
// .endpointAddr() .relayUrl()/.id(), Endpoint.connect(addr:alpn:),
// Connection.remoteId()/.openBi()/.paths()/.rtt()/.closed()/
// .close(errorCode:reason:), BiStream.send()/.recv(),
// SendStream.writeAll(buf:)/.finish()/.reset(errorCode:),
// RecvStream.read(sizeLimit:) (empty at the end)/.stop(errorCode:),
// SecretKey.fromBytes(bytes:).public().toBytes().

/// IrohLib as XbinCore's TunnelDialer (TunnelRuntime.dialer).
struct IrohTunnelDialer: TunnelDialer {
    struct Problem: Error, CustomStringConvertible, Equatable {
        var description: String
    }

    func connect(irohSecret: Data, ticket: String, relayToken: String?, alpn: String) async throws -> any TunnelConnection {
        let addr: EndpointAddr
        do {
            addr = try EndpointTicket.fromString(str: ticket).endpointAddr()
        } catch {
            throw Problem(description: "the tunnel's ticket is damaged (\(Self.text(error)))")
        }
        guard let relay = addr.relayUrl(), !relay.isEmpty else { throw Problem(description: "the tunnel's ticket names no relay") }
        let local = try SecretKey.fromBytes(bytes: irohSecret).public().toBytes()
        let map = RelayMap.empty()
        try map.insert(config: RelayConfig(url: relay, quicPort: nil, authToken: relayToken))
        let ep = try await Endpoint.bind(options: EndpointOptions(preset: presetMinimal(), secretKey: irohSecret, alpns: nil,
                                                                  relayMode: RelayMode.custom(map: map)))
        do {
            let c = try await ep.connect(addr: addr, alpn: Data(alpn.utf8))
            let remote = c.remoteId().toBytes()
            guard remote == addr.id().toBytes() else {
                try? c.close(errorCode: 1, reason: Data("wrong peer".utf8))
                throw Problem(description: "the tunnel answered as another server")
            }
            return IrohTunnelConnection(endpoint: ep, connection: c, relayURL: relay, localId: local, remoteId: remote)
        } catch {
            try? await ep.close()
            throw error
        }
    }

    static func text(_ error: any Error) -> String {
        if let e = error as? LocalizedError, let d = e.errorDescription { return d }
        return String(describing: error)
    }
}

/// One iroh connection (and the endpoint it was made on, closed with it).
final class IrohTunnelConnection: TunnelConnection, TunnelConnectionDetails, @unchecked Sendable {
    private let endpoint: Endpoint
    private let connection: Connection
    private let relayURL: String
    let localId: Data
    let remoteId: Data

    init(endpoint: Endpoint, connection: Connection, relayURL: String, localId: Data, remoteId: Data) {
        self.endpoint = endpoint
        self.connection = connection
        self.relayURL = relayURL
        self.localId = localId
        self.remoteId = remoteId
    }

    func openStream() async throws -> any ByteStream {
        let bi = try await connection.openBi()
        return IrohByteStream(send: bi.send(), recv: bi.recv())
    }

    func close() async {
        try? connection.close(errorCode: 0, reason: Data("bye".utf8))
        try? await endpoint.close()
    }

    func closed() async -> String { await connection.closed() }

    func path() async -> (path: TunnelStatus.Path, rtt: Double?)? {
        let host = URLComponents(string: relayURL)?.host
        let rtt = connection.rtt().map { Double($0) / 1000 }
        guard let sel = connection.paths().first(where: { $0.isSelected }) else { return (.relay(host: host), rtt) }
        return (sel.isRelay ? .relay(host: host) : .direct, Double(sel.rttMs) / 1000)
    }
}

/// An iroh bi-stream as a ByteStream: empty at the peer's finish.
final class IrohByteStream: ByteStream, @unchecked Sendable {
    private let send: SendStream
    private let recv: RecvStream

    init(send: SendStream, recv: RecvStream) {
        self.send = send
        self.recv = recv
    }

    func read(max: Int) async throws -> Data {
        try await recv.read(sizeLimit: UInt32(clamping: Swift.max(1, max)))
    }

    func write(_ data: Data) async throws { try await send.writeAll(buf: data) }

    func finish() async throws { try await send.finish() }

    func cancel() async {
        try? await send.reset(errorCode: 0)
        try? await recv.stop(errorCode: 0)
    }
}
