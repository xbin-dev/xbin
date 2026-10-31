import Foundation
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif
import XbinCore

// Pairing a device through a workspace's app tunnel (D204; native/spec/
// tunnel.md §5, §6) and upgrading an enrolled one (§9): the steps, the keys
// and their cleanup. No UIKit — native/tools/app-check compiles this very
// file (a symlink) and runs it against a fake iroh dialer and an in-process
// Noise responder; the page that shows it is App/Shell/Pairing.swift. The
// iroh runtime itself (TunnelDialer, the transport that signs in over the
// tunnel) is App/Model/TunnelEndpoint.swift's, reached through
// ``TunnelRuntime``.

/// Where a workspace's ``TunnelSecrets`` live: the Keychain in the app
/// (``KeychainTunnelVault``, TunnelKeyStore.swift), memory in tests.
protocol TunnelVault: Sendable {
    func load(workspace: String) async -> TunnelSecrets?
    func save(_ secrets: TunnelSecrets, workspace: String) async
    func delete(workspace: String) async
}

/// A new device key pair for one workspace's tunnel: the iroh secret
/// (Ed25519) whose public key is the device's EndpointId, and the Noise
/// static key (X25519).
struct TunnelDeviceKeys: Sendable {
    let irohSecret: Data
    let noiseStatic: Data

    static func generate() -> TunnelDeviceKeys {
        TunnelDeviceKeys(irohSecret: Curve25519.Signing.PrivateKey().rawRepresentation,
                         noiseStatic: NoiseTunnel.newKeyPair().privateKey)
    }

    /// The device's EndpointId (what iroh's `remoteId` is on xbind's side).
    var endpointId: Data {
        (try? Curve25519.Signing.PrivateKey(rawRepresentation: irohSecret))?.publicKey.rawRepresentation ?? Data()
    }

    var noisePublic: Data { (try? NoiseTunnel.publicKey(of: noiseStatic)) ?? Data() }
}

/// A device paired and signed in: what ``AppModel/add(_:session:in:replacing:)`` takes.
struct PairedWorkspace: Sendable {
    let record: WorkspaceRecord
    let session: SessionCredential
}

/// Opens an HTTP transport to a paired workspace through its tunnel (the
/// loopback forwarder's), for the first device login.
typealias TunnelSignInTransport = @Sendable (WorkspaceRecord, TunnelSecrets) async throws -> any APITransport

/// One pairing, start to end:
///
/// 1. a new workspace id; the device-login key (Secure Enclave), the iroh
///    secret and the Noise static key under it;
/// 2. iroh: dial the ticket on `xbin/pair/1`, one stream;
/// 3. ``PairingClient``: IKpsk2 with the QR code's psk and the selector, the
///    hello, `await` (→ `onCode`: "Confirm on your computer: 012 345"),
///    `enrolled` or `rejected`;
/// 4. the record (`server` = the link's `u`, device login signs the
///    `enrolled` origin, `tunnel` set) and the Keychain item;
/// 5. device login over the tunnel (one Face ID prompt).
///
/// Every failure deletes all three keys and the item, as the classic
/// ``Enrollment/enroll(server:code:deviceName:workspaceID:)`` deletes its
/// key — and is a ``PairingFailure`` or what the sign-in threw (the page
/// turns either into a ``ConnectProblem``).
struct TunnelPairer: Sendable {
    let keys: any DeviceKeyStore
    let vault: any TunnelVault
    let dialer: any TunnelDialer
    let signInTransport: TunnelSignInTransport
    let clientHeader: String
    let platform: String
    var timeout: TimeInterval = 7 * 60

    func pair(_ link: TunnelLink, deviceName: String, workspaceID id: String = UUID().uuidString.lowercased(),
              onCode: @escaping @Sendable (_ code: String, _ expires: Date?) async -> Void,
              onSigningIn: @escaping @Sendable () async -> Void = {},
              onConnected: @escaping @Sendable () async -> Void = {}) async throws -> PairedWorkspace {
        let device = TunnelDeviceKeys.generate()
        var connection: (any TunnelConnection)?
        do {
            let spki = try await keys.createKey(workspace: id)
            let conn: any TunnelConnection
            do {
                conn = try await dialer.connect(irohSecret: device.irohSecret, ticket: link.ticket, relayToken: link.relayToken,
                                                alpn: TunnelALPN.pair)
            } catch let p as PairingFailure {
                throw p
            } catch {
                throw PairingFailure.unreachable(PairingClient.text(error))
            }
            connection = conn
            await onConnected()
            let stream: any ByteStream
            do { stream = try await conn.openStream() } catch { throw PairingFailure.unreachable(PairingClient.text(error)) }
            var client = PairingClient(link: link, hello: PairingHello(name: deviceName, platform: platform, publicKeySPKI: spki),
                                       staticKey: device.noiseStatic, serverEndpoint: conn.remoteId, deviceEndpoint: conn.localId)
            client.timeout = timeout
            let enrolled = try await client.run(stream, onCode: onCode)
            let serverEndpoint = conn.remoteId
            await conn.close()
            connection = nil

            let record = link.record(enrolled, id: id, deviceName: deviceName, serverEndpoint: serverEndpoint)
            var relay = enrolled.tunnel.relay
            if relay.token == nil { relay.token = link.relayToken }
            let secrets = TunnelSecrets(irohSecret: device.irohSecret, noiseStatic: device.noiseStatic,
                                        serverKey: link.serverKey, ticket: enrolled.tunnel.ticket, relay: relay,
                                        next: enrolled.tunnel.next, endpoint: TunnelUpgrade.hex(serverEndpoint),
                                        required: link.required || enrolled.tunnel.required)
            await vault.save(secrets, workspace: id)
            await onSigningIn()
            let transport = try await Self.overTunnel { try await signInTransport(record, secrets) }
            let enrollment = Enrollment(transport: transport, keys: keys, clientHeader: clientHeader, platform: platform)
            let session = try await Self.overTunnel { try await enrollment.firstSignIn(record, over: transport) }
            return PairedWorkspace(record: record, session: session)
        } catch {
            if let connection { await connection.close() }
            await keys.deleteKey(workspace: id)
            await vault.delete(workspace: id)
            throw error
        }
    }

    /// The first sign-in, over the tunnel just paired. Pairing is about the
    /// tunnel, so a network failure there (how the transport reports a
    /// tunnel that doesn't come up, D213 — its reason under it) is the
    /// tunnel's: ``PairingFailure/unreachable(_:)``. A cancellation, or the
    /// sign-in's own refusals, stay as they are.
    static func overTunnel<T: Sendable>(_ work: @Sendable () async throws -> T) async throws -> T {
        do {
            return try await work()
        } catch {
            guard !(error is CancellationError), NetworkFailure.isNetwork(error),
                  (error as NSError).code != -999 else { throw error } // NSURLErrorCancelled
            throw PairingFailure.unreachable(NetworkFailure.reason(error) ?? PairingClient.text(error))
        }
    }
}

/// Upgrading a device enrolled the classic way to the tunnel (§9), without
/// pairing again: new tunnel keys, `POST /api/xbin/devices/{id}/tunnel`
/// with the device's own fresh session over its direct connection, then
/// the Keychain item and the record's ``WorkspaceRecord/tunnel``. The
/// workspace answers 403 while it requires the tunnel (pair instead) or
/// when the session is older than 10 minutes (try after the next sign-in).
struct TunnelUpgrader: Sendable {
    let vault: any TunnelVault

    enum Outcome: Sendable, Equatable {
        case upgraded(TunnelInfo)
        /// Not now: a stale session, a refusal; nothing changed.
        case notNow(String)
    }

    /// `send`: the workspace's authenticated request (``WorkspaceAuth/send(_:)``).
    func upgrade(_ record: WorkspaceRecord,
                 send: @Sendable (APIRequest) async throws -> APIResponse) async -> Outcome {
        guard let deviceId = record.deviceId else { return .notNow("not enrolled") }
        let device = TunnelDeviceKeys.generate()
        let r: APIResponse
        do {
            r = try await send(TunnelUpgrade.request(deviceId: deviceId, noiseKey: device.noisePublic, endpoint: device.endpointId))
        } catch {
            return .notNow(PairingClient.text(error))
        }
        guard r.isSuccess else { return .notNow(APIError(r).description) }
        guard let config = try? TunnelClientConfig(json: r.json()), let serverKey = config.serverKey,
              let endpoint = config.endpointId else {
            return .notNow("the workspace's answer had no tunnel")
        }
        var secrets = TunnelSecrets(irohSecret: device.irohSecret, noiseStatic: device.noiseStatic, serverKey: serverKey,
                                    ticket: config.ticket, endpoint: endpoint)
        secrets.apply(config)
        await vault.save(secrets, workspace: record.id)
        return .upgraded(secrets.info(port: record.tunnel?.port))
    }
}

/// An in-memory ``TunnelVault`` (tests, previews).
actor MemoryTunnelVault: TunnelVault {
    private var items: [String: TunnelSecrets] = [:]
    func load(workspace: String) async -> TunnelSecrets? { items[workspace] }
    func save(_ secrets: TunnelSecrets, workspace: String) async { items[workspace] = secrets }
    func delete(workspace: String) async { items[workspace] = nil }
    var count: Int { items.count }
}
