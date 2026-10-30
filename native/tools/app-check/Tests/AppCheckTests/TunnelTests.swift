import Foundation
import Testing
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif
import XbinCore

// The app tunnel's xbin layer (D204; native/spec/tunnel.md §4–§6):
// Shared/NoiseTunnel.swift — the app's own file, symlinked here — against
// the Noise project's published IK and IKpsk2 vectors (cacophony, in
// ../../Fixtures), itself (initiator against responder), and the framing,
// rekeying, close and truncation rules. xbind's native/spec/
// tunnel-vectors.json joins these once it is generated.

// MARK: - In-memory streams

/// One direction of an in-memory stream.
actor MemoryPipe {
    private var buffer = Data()
    private var finished = false
    private var cancelled = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    /// Bytes per read at most (to cut frames apart).
    let chunk: Int

    init(chunk: Int = .max) { self.chunk = chunk }

    func read(max: Int) async throws -> Data {
        while true {
            if cancelled { throw MemoryStreamError.reset }
            if !buffer.isEmpty {
                let n = Swift.min(max, chunk, buffer.count)
                let out = Data(buffer.prefix(n))
                buffer = Data(buffer.dropFirst(n))
                return out
            }
            if finished { return Data() }
            await withCheckedContinuation { waiters.append($0) }
        }
    }

    func write(_ d: Data) throws {
        if cancelled { throw MemoryStreamError.reset }
        guard !finished else { throw MemoryStreamError.writeAfterFinish }
        buffer.append(d)
        wake()
    }

    func finish() {
        finished = true
        wake()
    }

    func cancel() {
        cancelled = true
        wake()
    }

    var isFinished: Bool { finished }

    private func wake() {
        let w = waiters
        waiters = []
        for c in w { c.resume() }
    }
}

enum MemoryStreamError: Error { case reset, writeAfterFinish }

/// One end of an in-memory bidirectional stream.
struct MemoryStream: ByteStream {
    let inbound: MemoryPipe
    let outbound: MemoryPipe

    func read(max: Int) async throws -> Data { try await inbound.read(max: max) }
    func write(_ data: Data) async throws { try await outbound.write(data) }
    func finish() async throws { await outbound.finish() }
    func cancel() async {
        await inbound.cancel()
        await outbound.cancel()
    }

    /// Two connected ends.
    static func pair(chunk: Int = .max) -> (MemoryStream, MemoryStream) {
        let a = MemoryPipe(chunk: chunk), b = MemoryPipe(chunk: chunk)
        return (MemoryStream(inbound: a, outbound: b), MemoryStream(inbound: b, outbound: a))
    }
}

/// A stream whose outgoing bytes a test can tamper with.
struct TamperStream: ByteStream {
    let base: MemoryStream
    let tamper: @Sendable (Data) -> Data
    func read(max: Int) async throws -> Data { try await base.read(max: max) }
    func write(_ data: Data) async throws { try await base.write(tamper(data)) }
    func finish() async throws { try await base.finish() }
    func cancel() async { await base.cancel() }
}

func hexData(_ s: String?) -> Data { s.map(hex) ?? Data() }

// MARK: - The published vectors

@Suite struct NoiseVectorTests {
    /// cacophony's `Noise_IK_25519_ChaChaPoly_SHA256` and
    /// `Noise_IKpsk2_25519_ChaChaPoly_SHA256`: both roles reproduce every
    /// message byte for byte, the handshake hash, and the transport messages
    /// (which alternate directions after the handshake).
    @Test func cacophony() throws {
        let v = try JSONValue(parsing: Data(contentsOf: repo.appendingPathComponent("native/tools/app-check/Fixtures/noise-cacophony-ik.json")))
        let vectors = try #require(v["vectors"]?.arrayValue)
        #expect(vectors.count == 2)
        for vec in vectors {
            let name = vec["protocol_name"]?.stringValue ?? ""
            let pattern: NoiseTunnel.Pattern = name.contains("psk2") ? .ikpsk2 : .ik
            #expect(pattern.protocolName == name)
            let psk = vec["init_psks"]?.arrayValue?.first?.stringValue.map(hex)
            var i = try NoiseHandshake(initiator: true, pattern: pattern, prologue: hexData(vec["init_prologue"]?.stringValue),
                                       staticKey: hexData(vec["init_static"]?.stringValue),
                                       remoteStatic: hexData(vec["init_remote_static"]?.stringValue), psk: psk,
                                       ephemeral: hexData(vec["init_ephemeral"]?.stringValue))
            var r = try NoiseHandshake(initiator: false, pattern: pattern, prologue: hexData(vec["resp_prologue"]?.stringValue),
                                       staticKey: hexData(vec["resp_static"]?.stringValue),
                                       psk: vec["resp_psks"]?.arrayValue?.first?.stringValue.map(hex),
                                       ephemeral: hexData(vec["resp_ephemeral"]?.stringValue))
            let messages = try #require(vec["messages"]?.arrayValue)
            // The handshake.
            let m1 = try i.writeMessage(hexData(messages[0]["payload"]?.stringValue))
            #expect(m1 == hexData(messages[0]["ciphertext"]?.stringValue), "\(name) message 1")
            #expect(try r.readMessage(m1) == hexData(messages[0]["payload"]?.stringValue))
            #expect(r.remoteStatic == (try NoiseTunnel.publicKey(of: hexData(vec["init_static"]?.stringValue))))
            let m2 = try r.writeMessage(hexData(messages[1]["payload"]?.stringValue))
            #expect(m2 == hexData(messages[1]["ciphertext"]?.stringValue), "\(name) message 2")
            #expect(try i.readMessage(m2) == hexData(messages[1]["payload"]?.stringValue))
            #expect(i.isComplete && r.isComplete)
            #expect(i.handshakeHash == hexData(vec["handshake_hash"]?.stringValue), "\(name) h")
            #expect(r.handshakeHash == i.handshakeHash)
            // Transport: initiator, responder, initiator, …
            var (iSend, iRecv) = try i.split()
            var (rSend, rRecv) = try r.split()
            for (n, m) in messages.dropFirst(2).enumerated() {
                let payload = hexData(m["payload"]?.stringValue), ct = hexData(m["ciphertext"]?.stringValue)
                if n % 2 == 0 {
                    #expect(try iSend.encrypt(ad: Data(), payload) == ct, "\(name) transport \(n)")
                    #expect(try rRecv.decrypt(ad: Data(), ct) == payload)
                } else {
                    #expect(try rSend.encrypt(ad: Data(), payload) == ct, "\(name) transport \(n)")
                    #expect(try iRecv.decrypt(ad: Data(), ct) == payload)
                }
            }
        }
    }

    /// The nonce is `4×0 ‖ LE64(n)`; REKEY is ENCRYPT at the maximum nonce,
    /// and the counter carries on.
    @Test func cipherState() throws {
        #expect(NoiseCipherState.nonce(1) == Data([0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0]))
        #expect(NoiseCipherState.nonce(0x0102) == Data([0, 0, 0, 0, 2, 1, 0, 0, 0, 0, 0, 0]))
        let key = Data((0..<32).map { UInt8($0) })
        var a = NoiseCipherState(key: key), b = NoiseCipherState(key: key)
        let c0 = try a.encrypt(ad: Data(), Data("x".utf8))
        // ChaCha20-Poly1305 with that nonce, by CryptoKit directly.
        let direct = try ChaChaPoly.seal(Data("x".utf8), using: SymmetricKey(data: key),
                                         nonce: ChaChaPoly.Nonce(data: Data(count: 12)))
        #expect(c0 == direct.ciphertext + direct.tag)
        #expect(try b.decrypt(ad: Data(), c0) == Data("x".utf8))
        a.rekey()
        b.rekey()
        let expected = try ChaChaPoly.seal(Data(count: 32), using: SymmetricKey(data: key),
                                           nonce: ChaChaPoly.Nonce(data: Data(count: 4) + Data(repeating: 0xFF, count: 8)))
        #expect(a.key == expected.ciphertext.prefix(32))
        #expect(a.n == 1)
        let c1 = try a.encrypt(ad: Data(), Data("y".utf8))
        #expect(try b.decrypt(ad: Data(), c1) == Data("y".utf8))
        // Out of step: refused, and the counter doesn't move.
        var c = NoiseCipherState(key: key)
        #expect(throws: NoiseTunnelError.decrypt) { try c.decrypt(ad: Data(), c1) }
        #expect(c.n == 0)
    }

    /// RFC 5869's HKDF-SHA256 with an empty salt (§5) against CryptoKit's
    /// own HKDF, and the selector against a plain SHA-256.
    @Test func sasAndSelector() {
        let h = Data((0..<32).map { UInt8($0 * 7 & 0xFF) })
        let okm = NoiseTunnel.sasOKM(handshakeHash: h)
        let kit = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: h), salt: Data(),
                                         info: Data("xbin-sas-v1".utf8), outputByteCount: 8)
        #expect(okm == kit.withUnsafeBytes { Data($0) })
        #expect(NoiseTunnel.sas(handshakeHash: h) == TunnelSAS.digits(okm))
        let link = TunnelLink(server: try! ServerOrigin(string: "https://x.example"), code: "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
                              ticket: "endpointaaaa", serverKey: Data(count: 32), psk: Data(count: 32))
        #expect(NoiseTunnel.selector(link) == Data(SHA256.hash(data: Data("xbin-pair-select-v1\nABCDEFGHIJKLMNOPQRSTUVWXYZ".utf8))).prefix(16))
        #expect(NoiseTunnel.selector(link).count == 16)
        #expect(NoiseTunnel.prologue(.pair, server: Data(repeating: 1, count: 32), device: Data(repeating: 2, count: 32))
            == Data("xbin-pair-v1".utf8) + Data(repeating: 1, count: 32) + Data(repeating: 2, count: 32))
        #expect(NoiseTunnel.prologue(.tunnel, server: Data(), device: Data()) == Data("xbin-tunnel-v1".utf8))
    }

    /// An all-zero X25519 result (a low-order point) aborts.
    @Test func lowOrderPoints() throws {
        let k = NoiseTunnel.newKeyPair().privateKey
        #expect(throws: NoiseTunnelError.lowOrder) { try NoiseTunnel.dh(k, Data(count: 32)) }
        var one = Data(count: 32)
        one[0] = 1
        #expect(throws: NoiseTunnelError.lowOrder) { try NoiseTunnel.dh(k, one) }
        // A responder key that is a low-order point fails message 1.
        var i = try NoiseHandshake(initiator: true, pattern: .ik, prologue: Data(), staticKey: k, remoteStatic: Data(count: 32))
        #expect(throws: NoiseTunnelError.lowOrder) { try i.writeMessage() }
    }
}

// MARK: - The handshake over streams

struct TunnelKeys {
    let device = NoiseTunnel.newKeyPair()
    let server = NoiseTunnel.newKeyPair()
    let serverID = Data(repeating: 0xAA, count: 32)
    let deviceID = Data(repeating: 0xDD, count: 32)
    let psk = Data((0..<32).map { _ in UInt8.random(in: 0...255) })
    var link: TunnelLink {
        TunnelLink(server: try! ServerOrigin(string: "https://xbin.example.com"), code: "K3DQAAAABBBBCCCCDDDDEEEEFF",
                   ticket: "endpoint" + String(repeating: "a", count: 60), serverKey: server.publicKey, psk: psk)
    }
    func prologue(_ p: NoiseTunnel.Purpose) -> Data { NoiseTunnel.prologue(p, server: serverID, device: deviceID) }
}

@Suite struct NoiseHandshakeTests {
    @Test func tunnelIK() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair(chunk: 7)   // frames cut apart on the way
        async let server = NoiseTunnel.respond(s, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.server.privateKey)
        let device = try await NoiseTunnel.initiate(d, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.device.privateKey,
                                                    serverKey: k.server.publicKey)
        let (srv, payload1) = try await server
        #expect(payload1.isEmpty)
        #expect(srv.remoteStatic == k.device.publicKey)      // the device's key, from the handshake
        #expect(device.remoteStatic == k.server.publicKey)
        #expect(srv.handshakeHash == device.handshakeHash)
        try await device.sender.send(Data("GET / HTTP/1.1\r\n\r\n".utf8))
        #expect(try await srv.receiver.receive() == Data("GET / HTTP/1.1\r\n\r\n".utf8))
        try await srv.sender.send(Data("HTTP/1.1 200 OK\r\n\r\n".utf8))
        #expect(try await device.receiver.receive() == Data("HTTP/1.1 200 OK\r\n\r\n".utf8))
    }

    @Test func pairingIKpsk2() async throws {
        let k = TunnelKeys()
        let link = k.link
        let (d, s) = MemoryStream.pair()
        let selector = NoiseTunnel.selector(link)
        let psk = k.psk
        async let server = NoiseTunnel.respond(s, pattern: .ikpsk2, prologue: k.prologue(.pair), staticKey: k.server.privateKey,
                                               psk: { $0 == selector ? psk : nil })
        let device = try await NoiseTunnel.initiate(d, pattern: .ikpsk2, prologue: k.prologue(.pair), staticKey: k.device.privateKey,
                                                    serverKey: k.server.publicKey, psk: link.psk, payload: selector)
        let (srv, payload1) = try await server
        #expect(payload1 == selector)
        // Both sides derive the same code; it never travels.
        #expect(NoiseTunnel.sas(handshakeHash: srv.handshakeHash) == NoiseTunnel.sas(handshakeHash: device.handshakeHash))
        #expect(NoiseTunnel.sas(handshakeHash: device.handshakeHash).count == 7)
    }

    /// Wrong psk, wrong prologue (another device's EndpointId), wrong server
    /// key, a flipped byte: no session.
    @Test func refusals() async throws {
        let k = TunnelKeys()
        func attempt(devicePrologue: Data? = nil, devicePSK: Data? = nil, serverKey: Data? = nil,
                     tamper: (@Sendable (Data) -> Data)? = nil) async -> Bool {
            let (d0, s) = MemoryStream.pair()
            let d: any ByteStream = tamper.map { TamperStream(base: d0, tamper: $0) } ?? d0
            let psk = k.psk
            let server = Task {
                try await NoiseTunnel.respond(s, pattern: .ikpsk2, prologue: k.prologue(.pair), staticKey: k.server.privateKey,
                                              psk: { _ in psk })
            }
            do {
                let ch = try await NoiseTunnel.initiate(d, pattern: .ikpsk2, prologue: devicePrologue ?? k.prologue(.pair),
                                                        staticKey: k.device.privateKey, serverKey: serverKey ?? k.server.publicKey,
                                                        psk: devicePSK ?? k.psk, payload: Data(count: 16))
                // IKpsk2: the responder learns of a wrong psk only from the
                // first transport message.
                try await ch.sender.send(Data("hello".utf8))
                let (srv, _) = try await server.value
                let got = try await srv.receiver.receive()
                return got == Data("hello".utf8)
            } catch {
                await d0.cancel()
                await s.cancel()
                return false
            }
        }
        #expect(await attempt())
        #expect(await !attempt(devicePSK: Data(repeating: 1, count: 32)))
        #expect(await !attempt(devicePrologue: NoiseTunnel.prologue(.pair, server: k.serverID, device: Data(repeating: 0xEE, count: 32))))
        #expect(await !attempt(devicePrologue: k.prologue(.tunnel)))
        #expect(await !attempt(serverKey: NoiseTunnel.newKeyPair().publicKey))
        #expect(await !attempt(tamper: { d in
            var d = d
            if d.count > 40 { d[40] ^= 1 }   // inside message 1's encrypted static key
            return d
        }))
    }
}

// MARK: - Transport frames

@Suite struct NoiseTransportTests {
    func connected(policy: NoiseRekeyPolicy = NoiseRekeyPolicy(),
                   chunk: Int = .max) async throws -> (NoiseChannel, NoiseChannel, MemoryStream, MemoryStream) {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair(chunk: chunk)
        async let server = NoiseTunnel.respond(s, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.server.privateKey,
                                               policy: policy)
        let device = try await NoiseTunnel.initiate(d, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.device.privateKey,
                                                    serverKey: k.server.publicKey, policy: policy)
        return (device, try await server.0, d, s)
    }

    /// Big writes split into frames of at most 65535 bytes; a close is an
    /// orderly end, then nothing more.
    @Test func framesAndClose() async throws {
        let (dev, srv, _, s) = try await connected()
        let big = Data((0..<200_000).map { UInt8(truncatingIfNeeded: $0) })
        try await dev.sender.send(big)
        var got = Data()
        while got.count < big.count { got += try await srv.receiver.receive() }
        #expect(got == big)
        try await dev.sender.close()
        #expect(try await srv.receiver.receive() == Data())
        #expect(try await srv.receiver.receive() == Data())
        #expect(await s.inbound.isFinished)
        await #expect(throws: NoiseTunnelError.misuse("send after close")) { try await dev.sender.send(Data("x".utf8)) }
    }

    /// The stream ending without a close is truncation — never an EOF.
    @Test func truncation() async throws {
        let (dev, srv, d, _) = try await connected()
        try await dev.sender.send(Data("partial".utf8))
        try await d.finish()   // the sidecar drops the stream
        #expect(try await srv.receiver.receive() == Data("partial".utf8))
        await #expect(throws: NoiseTunnelError.truncated) { try await srv.receiver.receive() }
        // A frame cut short is too.
        let (dev2, srv2, d2, _) = try await connected()
        _ = dev2
        try await d2.write(Data([0x00, 0x40, 1, 2, 3]))
        try await d2.finish()
        await #expect(throws: NoiseTunnelError.truncated) { try await srv2.receiver.receive() }
    }

    /// Rekeys follow the count (and the clock); the receiver follows them.
    @Test func rekeying() async throws {
        var policy = NoiseRekeyPolicy()
        policy.maxMessages = 3
        let (dev, srv, _, _) = try await connected(policy: policy)
        for i in 0..<10 {
            try await dev.sender.send(Data("m\(i)".utf8))
            #expect(try await srv.receiver.receive() == Data("m\(i)".utf8))
        }
        #expect(await dev.sender.rekeys == 3)   // before the 4th, 7th and 10th
        try await srv.sender.rekey()
        try await srv.sender.send(Data("back".utf8))
        #expect(try await dev.receiver.receive() == Data("back".utf8))

        // Ten minutes since the last key.
        let clock = Clock()
        var timed = NoiseRekeyPolicy()
        timed.now = { clock.now }
        let (d2, s2, _, _) = try await connected(policy: timed)
        try await d2.sender.send(Data("a".utf8))
        clock.advance(601)
        try await d2.sender.send(Data("b".utf8))
        #expect(try await s2.receiver.receive() == Data("a".utf8))
        #expect(try await s2.receiver.receive() == Data("b".utf8))
        #expect(await d2.sender.rekeys == 1)

        // A byte budget.
        var bytes = NoiseRekeyPolicy()
        bytes.maxBytes = 100_000
        let (d3, s3, _, _) = try await connected(policy: bytes)
        try await d3.sender.send(Data(count: 250_000))
        var n = 0
        while n < 250_000 { n += try await s3.receiver.receive().count }
        #expect(await d3.sender.rekeys == 1)    // frames of 65518: rekey before the 3rd (131036 ≥ 100000)
    }

    /// Frame types other than data, rekey and close abort; so do bodies on
    /// rekey and close.
    @Test func unknownTypes() async throws {
        for (type, body) in [(UInt8(3), Data()), (0x01, Data([1])), (0x02, Data([1])), (0xFF, Data())] {
            let k = TunnelKeys()
            let (d, s) = MemoryStream.pair()
            var i = try NoiseHandshake(initiator: true, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.device.privateKey,
                                       remoteStatic: k.server.publicKey)
            async let server = NoiseTunnel.respond(s, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.server.privateKey)
            try await d.write(NoiseFrame.framed(i.writeMessage()))
            let reader = NoiseFrameReader(d)
            _ = try i.readMessage(try #require(try await reader.next()))
            var (send, _) = try i.split()
            try await d.write(NoiseFrame.framed(send.encrypt(ad: Data(), Data([type]) + body)))
            let (srv, _) = try await server
            await #expect(throws: NoiseTunnelError.self, "type \(type)") { try await srv.receiver.receive() }
        }
    }
}

final class Clock: @unchecked Sendable {
    private let lock = NSLock()
    private var t = Date(timeIntervalSince1970: 1_790_000_000)
    var now: Date { lock.lock(); defer { lock.unlock() }; return t }
    func advance(_ s: TimeInterval) { lock.lock(); t += s; lock.unlock() }
}

// MARK: - The splice

@Suite struct TunnelSpliceTests {
    /// A loopback TCP connection through the tunnel to xbind's side and
    /// back: 1 MiB each way, then half-closes in both orders.
    @Test func megabytesAndHalfClose() async throws {
        let k = TunnelKeys()
        let (tcpApp, tcpForwarder) = MemoryStream.pair(chunk: 1500)      // WebKit ⇄ the forwarder
        let (irohDevice, irohServer) = MemoryStream.pair(chunk: 1200)    // the iroh stream
        async let server = NoiseTunnel.respond(irohServer, pattern: .ik, prologue: k.prologue(.tunnel),
                                               staticKey: k.server.privateKey)
        let device = try await NoiseTunnel.initiate(irohDevice, pattern: .ik, prologue: k.prologue(.tunnel),
                                                    staticKey: k.device.privateKey, serverKey: k.server.publicKey)
        let (srv, _) = try await server
        let splice = Task { try await TunnelSplice.run(plain: tcpForwarder, channel: device) }

        let up = Data((0..<(1 << 20)).map { UInt8(truncatingIfNeeded: $0 &* 31) })
        let down = Data((0..<(1 << 20)).map { UInt8(truncatingIfNeeded: $0 &* 17) })
        // The app writes a request and closes its side (a half-close).
        let writer = Task {
            try await tcpApp.write(up)
            try await tcpApp.finish()
        }
        var got = Data()
        while true {
            let d = try await srv.receiver.receive()
            if d.isEmpty { break }
            got += d
        }
        #expect(got == up)                          // and then the authenticated close
        try await writer.value
        // xbind answers after the request ended, then closes.
        try await srv.sender.send(down)
        try await srv.sender.close()
        var back = Data()
        while true {
            let d = try await tcpApp.read(max: 65536)
            if d.isEmpty { break }
            back += d
        }
        #expect(back == down)
        try await splice.value                      // both directions ended: done
    }

    /// The peer's stream ending without a close resets the TCP side and
    /// throws.
    @Test func truncationResets() async throws {
        let k = TunnelKeys()
        let (tcpApp, tcpForwarder) = MemoryStream.pair()
        let (irohDevice, irohServer) = MemoryStream.pair()
        async let server = NoiseTunnel.respond(irohServer, pattern: .ik, prologue: k.prologue(.tunnel),
                                               staticKey: k.server.privateKey)
        let device = try await NoiseTunnel.initiate(irohDevice, pattern: .ik, prologue: k.prologue(.tunnel),
                                                    staticKey: k.device.privateKey, serverKey: k.server.publicKey)
        let (srv, _) = try await server
        let splice = Task { try await TunnelSplice.run(plain: tcpForwarder, channel: device) }
        try await srv.sender.send(Data("HTTP/1.1 200 OK\r\n".utf8))
        #expect(try await tcpApp.read(max: 100) == Data("HTTP/1.1 200 OK\r\n".utf8))
        try await irohServer.finish()               // no close message
        await #expect(throws: NoiseTunnelError.truncated) { try await splice.value }
        await #expect(throws: MemoryStreamError.self) { _ = try await tcpApp.read(max: 10) }
    }
}

/// The probe a new connection answers before the app uses it (D211): the
/// IK handshake, `GET /api/xbin/login/methods` with the forwarder's Host,
/// xbind's status — read to xbind's close, then ours.
@Suite struct TunnelProbeChannelTests {
    @Test func readsXbindsStatus() async throws {
        let k = TunnelKeys()
        let (irohDevice, irohServer) = MemoryStream.pair(chunk: 5)
        let secrets = TunnelSecrets(irohSecret: Data(repeating: 1, count: 32), noiseStatic: k.device.privateKey,
                                    serverKey: k.server.publicKey, ticket: "endpoint" + String(repeating: "a", count: 60))
        let server = Task {
            let (srv, _) = try await NoiseTunnel.respond(irohServer, pattern: .ik, prologue: k.prologue(.tunnel),
                                                         staticKey: k.server.privateKey)
            var request = Data()
            while !request.suffix(4).elementsEqual(Data("\r\n\r\n".utf8)) { request += try await srv.receiver.receive() }
            try await srv.sender.send(Data("HTTP/1.1 403 Forbidden\r\nContent-Type: application/json\r\n\r\n{\"tunnel\":\"device\"}".utf8))
            try await srv.sender.close()
            #expect(try await srv.receiver.receive().isEmpty)    // the device closes its side after the answer
            return String(decoding: request, as: UTF8.self)
        }
        let status = try await NoiseTunnel.probe(irohDevice, secrets: secrets, serverEndpoint: k.serverID,
                                                 deviceEndpoint: k.deviceID, host: "127.0.0.1:61234")
        #expect(status == 403)   // any answer of xbind's proves the tunnel
        #expect(try await server.value == String(decoding: TunnelProbe.request(host: "127.0.0.1:61234"), as: UTF8.self))
    }

    @Test func refusesAnAnswerThatIsntHTTP() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair()
        async let server: Void = {
            let (srv, _) = try await NoiseTunnel.respond(s, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.server.privateKey)
            _ = try await srv.receiver.receive()
            try await srv.sender.send(Data("SSH-2.0-nope\r\n".utf8))
            try await srv.sender.close()
        }()
        let device = try await NoiseTunnel.initiate(d, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.device.privateKey,
                                                    serverKey: k.server.publicKey)
        await #expect(throws: NoiseTunnelError.self) { _ = try await NoiseTunnel.probe(device, host: "127.0.0.1:1") }
        try await server
    }

    /// The stream ending without xbind's close is truncation, not an answer.
    @Test func truncationFails() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair()
        async let server: Void = {
            let (srv, _) = try await NoiseTunnel.respond(s, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.server.privateKey)
            _ = try await srv.receiver.receive()
            try await srv.sender.send(Data("HTTP/1.1 200 OK\r\n".utf8))
            try await s.finish()
        }()
        let device = try await NoiseTunnel.initiate(d, pattern: .ik, prologue: k.prologue(.tunnel), staticKey: k.device.privateKey,
                                                    serverKey: k.server.publicKey)
        await #expect(throws: NoiseTunnelError.truncated) { _ = try await NoiseTunnel.probe(device, host: "127.0.0.1:1") }
        try await server
    }
}

// MARK: - Pairing

/// xbind's side of `pair/1`, scripted.
struct FakePairingServer {
    let keys: TunnelKeys
    /// What it answers after the hello (each a JSON data message), and
    /// whether it closes after.
    let replies: [JSONValue]
    var close = true

    func run(_ s: MemoryStream, selector: Data) async throws -> (hello: JSONValue, code: String) {
        let psk = keys.psk
        let (ch, first) = try await NoiseTunnel.respond(s, pattern: .ikpsk2, prologue: keys.prologue(.pair),
                                                        staticKey: keys.server.privateKey,
                                                        psk: { $0 == selector ? psk : nil })
        #expect(first == selector)
        let hello = try JSONValue(parsing: try await ch.receiver.receive())
        for r in replies { try await ch.sender.send(r.jsonData) }
        if close { try await ch.sender.close() }
        return (hello, NoiseTunnel.sas(handshakeHash: ch.handshakeHash))
    }
}

final class Seen: @unchecked Sendable {
    private let lock = NSLock()
    private var codes: [String] = []
    func add(_ c: String) { lock.lock(); codes.append(c); lock.unlock() }
    var all: [String] { lock.lock(); defer { lock.unlock() }; return codes }
}

@Suite struct PairingClientTests {
    static let ticket = "endpoint" + String(repeating: "a", count: 60)
    static let enrolled: JSONValue = [
        "type": "enrolled", "deviceId": "dev_1", "user": ["id": "alice", "name": "Alice"], "origin": "https://xbin.example.com",
        "name": "Alice's iPhone", "tunnel": ["ticket": .string(ticket), "relay": [:], "required": false],
    ]

    func client(_ k: TunnelKeys, timeout: TimeInterval = 30) -> PairingClient {
        var c = PairingClient(link: k.link, hello: PairingHello(name: "Alice's iPhone", platform: "ios", publicKeySPKI: Data([1, 2, 3])),
                              staticKey: k.device.privateKey, serverEndpoint: k.serverID, deviceEndpoint: k.deviceID)
        c.timeout = timeout
        return c
    }

    @Test func enrolls() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair(chunk: 5)
        let fake = FakePairingServer(keys: k, replies: [["type": "await", "expires": "2026-10-27T12:05:00Z"], Self.enrolled])
        async let server = fake.run(s, selector: NoiseTunnel.selector(k.link))
        let seen = Seen()
        let e = try await client(k).run(d) { code, _ in seen.add(code) }
        let (hello, code) = try await server
        #expect(e.deviceId == "dev_1" && e.user.id == "alice" && e.tunnel.ticket == Self.ticket)
        // The phone showed the code the computer computed, once.
        #expect(seen.all == [code])
        // The hello: name, platform, the device-login key; no Noise key or EndpointId (those are the handshake's).
        #expect(hello == ["v": 1, "name": "Alice's iPhone", "platform": "ios", "publicKey": "AQID"])
    }

    @Test func rejections() async throws {
        for (reason, want) in [("mismatch", ConnectProblem.pairingDeclined), ("expired", .pairingExpired)] {
            let k = TunnelKeys()
            let (d, s) = MemoryStream.pair()
            let fake = FakePairingServer(keys: k, replies: [["type": "await"], ["type": "rejected", "reason": .string(reason), "error": "no"]])
            async let server = fake.run(s, selector: NoiseTunnel.selector(k.link))
            do {
                _ = try await client(k).run(d) { _, _ in }
                Issue.record("\(reason): enrolled")
            } catch let f as PairingFailure {
                #expect(ConnectProblem(f, server: k.link.server) == want)
            }
            _ = try await server
        }
    }

    /// A wrong code's psk never gets past the handshake; the selector of
    /// another code isn't found.
    @Test func wrongCode() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair()
        let server = Task {
            try await NoiseTunnel.respond(s, pattern: .ikpsk2, prologue: k.prologue(.pair), staticKey: k.server.privateKey,
                                          psk: { _ in nil })
        }
        do {
            _ = try await client(k).run(d) { _, _ in }
            Issue.record("paired with an unknown selector")
        } catch let f as PairingFailure {
            guard case .handshake = f else { Issue.record("not a handshake failure: \(f)"); return }
            #expect(ConnectProblem(f, server: nil).kind == .codeRefused)
        }
        _ = try? await server.value
    }

    /// Nobody confirms: the pairing times out as expired.
    @Test func timesOut() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair()
        let fake = FakePairingServer(keys: k, replies: [["type": "await"]], close: false)
        let server = Task { try await fake.run(s, selector: NoiseTunnel.selector(k.link)) }
        let start = Date()
        await #expect(throws: PairingFailure.timedOut) { try await client(k, timeout: 0.5).run(d) { _, _ in } }
        #expect(Date().timeIntervalSince(start) < 5)
        _ = try? await server.value
    }

    /// The stream dropping mid-pairing: unreachable, not enrolled.
    @Test func dropped() async throws {
        let k = TunnelKeys()
        let (d, s) = MemoryStream.pair()
        let psk = k.psk
        let selector = NoiseTunnel.selector(k.link)
        let server = Task {
            let (ch, _) = try await NoiseTunnel.respond(s, pattern: .ikpsk2, prologue: k.prologue(.pair),
                                                        staticKey: k.server.privateKey, psk: { $0 == selector ? psk : nil })
            _ = try await ch.receiver.receive()
            try await s.finish()
        }
        await #expect(throws: PairingFailure.unreachable("the connection dropped")) { try await client(k).run(d) { _, _ in } }
        try await server.value
    }
}

// MARK: - The app's pairing flow (App/Model/TunnelPairing.swift)

/// An iroh connection to an in-process xbind: each stream a memory pair,
/// whose server end `serve` runs.
struct FakeConnection: TunnelConnection {
    let localId: Data
    let remoteId: Data
    let serve: @Sendable (MemoryStream) async -> Void
    let closed = Seen()
    func openStream() async throws -> any ByteStream {
        let (d, s) = MemoryStream.pair()
        Task { await serve(s) }
        return d
    }
    func close() async { closed.add("closed") }
}

struct FakeDialer: TunnelDialer {
    let keys: TunnelKeys
    let serve: @Sendable (MemoryStream) async -> Void
    var fail: String?
    let dialed = Seen()
    func connect(irohSecret: Data, ticket: String, relayToken: String?, alpn: String) async throws -> any TunnelConnection {
        dialed.add("\(alpn) \(relayToken ?? "-")")
        if let fail { throw PairingFailure.unreachable(fail) }
        return FakeConnection(localId: keys.deviceID, remoteId: keys.serverID, serve: serve)
    }
}

/// A software device-login key store.
actor SoftwareKeys: DeviceKeyStore {
    private var keys: [String: SoftwareDeviceKey] = [:]
    func createKey(workspace: String) async throws -> Data {
        let k = SoftwareDeviceKey()
        keys[workspace] = k
        return k.publicKeySPKI
    }
    func hasKey(workspace: String) async -> Bool { keys[workspace] != nil }
    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data {
        guard let k = keys[workspace] else { throw SignInError.keyUnavailable("no key") }
        return try k.sign(message)
    }
    func deleteKey(workspace: String) async { keys[workspace] = nil }
    var count: Int { keys.count }
}

/// xbind's device login, as the tunnel's HTTP carries it.
struct FakeDeviceLogin: APITransport {
    let seen = Seen()
    func send(_ request: APIRequest, to origin: ServerOrigin) async throws -> APIResponse {
        seen.add("\(request.method) \(request.path) \(origin.origin)")
        let body: JSONValue
        switch request.path {
        case AppAuthRoute.challenge: body = ["nonce": "n0nce", "expires": 1_900_000_000]
        case AppAuthRoute.deviceLogin:
            body = ["token": "s.tunnel", "user": ["id": "alice", "name": "Alice", "role": "user"], "expiresIdle": 1_900_000_000]
        default: return APIResponse(status: 404, body: Data())
        }
        return APIResponse(status: 200, headers: ["Content-Type": "application/json"], body: body.jsonData)
    }
}

@Suite struct TunnelPairerTests {
    static let enrolled: JSONValue = [
        "type": "enrolled", "deviceId": "dev_7", "user": ["id": "alice", "name": "Alice"], "origin": "https://ext.example.com",
        "name": "Alice's iPhone",
        "tunnel": ["ticket": .string(PairingClientTests.ticket), "relay": ["url": "https://relay.example.com"], "required": false],
    ]

    func pairer(_ k: TunnelKeys, replies: [JSONValue], keys: SoftwareKeys, vault: MemoryTunnelVault,
                login: FakeDeviceLogin = FakeDeviceLogin(), fail: String? = nil) -> (TunnelPairer, FakeDialer) {
        let selector = NoiseTunnel.selector(k.link)
        let dialer = FakeDialer(keys: k, serve: { s in
            _ = try? await FakePairingServer(keys: k, replies: replies).run(s, selector: selector)
        }, fail: fail)
        let p = TunnelPairer(keys: keys, vault: vault, dialer: dialer, signInTransport: { _, _ in login },
                             clientHeader: "app/1", platform: "ios")
        return (p, dialer)
    }

    @Test func pairsAndSignsInOverTheTunnel() async throws {
        let k = TunnelKeys()
        var link = k.link
        link.relayToken = "tok"
        let keys = SoftwareKeys(), vault = MemoryTunnelVault(), login = FakeDeviceLogin()
        let (p, dialer) = pairer(k, replies: [["type": "await"], Self.enrolled], keys: keys, vault: vault, login: login)
        let codes = Seen()
        let paired = try await p.pair(link, deviceName: "Alice's iPhone", workspaceID: "ws1") { c, _ in codes.add(c) }
        #expect(dialer.dialed.all == ["xbin/pair/1 tok"])
        #expect(codes.all.count == 1)
        let r = paired.record
        #expect(r.id == "ws1" && r.server == link.server && r.deviceId == "dev_7")
        #expect(r.signedOrigin == "https://ext.example.com")
        #expect(r.tunnel?.endpoint == TunnelUpgrade.hex(k.serverID) && r.tunnel?.relay == "https://relay.example.com")
        #expect(paired.session.token == "s.tunnel" && paired.session.kind == .device)
        // The device login went over the tunnel's transport.
        #expect(login.seen.all.map { $0.split(separator: " ")[1] } == ["/login/device/challenge", "/login/device"])
        let s = try #require(await vault.load(workspace: "ws1"))
        #expect(s.serverKey == k.server.publicKey && s.ticket == PairingClientTests.ticket)
        #expect(s.relay == TunnelRelay(url: "https://relay.example.com", token: "tok"))   // the QR's token kept
        #expect(s.endpoint == TunnelUpgrade.hex(k.serverID))
        #expect(await keys.hasKey(workspace: "ws1"))
    }

    /// Any failure takes every key and the Keychain item with it.
    @Test func failuresCleanUp() async throws {
        for (name, replies, fail) in [("declined", [["type": "await"], ["type": "rejected", "reason": "mismatch"]] as [JSONValue], nil as String?),
                                      ("unreachable", [], "no relay")] {
            let k = TunnelKeys()
            let keys = SoftwareKeys(), vault = MemoryTunnelVault()
            let (p, _) = pairer(k, replies: replies, keys: keys, vault: vault, fail: fail)
            do {
                _ = try await p.pair(k.link, deviceName: "x", workspaceID: "ws1") { _, _ in }
                Issue.record("\(name): paired")
            } catch let f as PairingFailure {
                if name == "declined" { #expect(ConnectProblem(f, server: nil) == .pairingDeclined) }
                if name == "unreachable" { #expect(f == .unreachable("no relay")) }
            }
            #expect(await keys.count == 0, "\(name)")
            #expect(await vault.count == 0, "\(name)")
        }
    }

    @Test func upgrade() async throws {
        let vault = MemoryTunnelVault()
        let rec = WorkspaceRecord(id: "w", server: try ServerOrigin(string: "https://xbin.example.com"), user: WorkspaceUser(id: "a"),
                                  deviceId: "dev_1")
        let serverKey = NoiseTunnel.newKeyPair().publicKey
        let sent = Seen()
        let out = await TunnelUpgrader(vault: vault).upgrade(rec) { req in
            sent.add(req.path)
            let b = try JSONValue(parsing: req.body ?? Data())
            #expect(b["noiseKey"]?.stringValue.flatMap(Base64URL.decode)?.count == 32)
            #expect(b["endpoint"]?.stringValue?.count == 64)
            return APIResponse(status: 200, headers: ["Content-Type": "application/json"],
                               body: JSONValue.object(["endpointId": "abcd", "ticket": .string(PairingClientTests.ticket),
                                                       "serverKey": .string(Base64URL.encode(serverKey)),
                                                       "relay": [:], "required": false]).jsonData)
        }
        #expect(out == .upgraded(TunnelInfo(endpoint: "abcd")))
        #expect(sent.all == ["/api/xbin/devices/dev_1/tunnel"])
        let s = try #require(await vault.load(workspace: "w"))
        #expect(s.serverKey == serverKey && s.endpoint == "abcd")
        // The device's keys are its own: the Noise public key it registered is its static's.
        // A refusal changes nothing.
        let v2 = MemoryTunnelVault()
        let refused = await TunnelUpgrader(vault: v2).upgrade(rec) { _ in
            APIResponse(status: 403, body: Data(#"{"error":"sign in again","stepUp":"signin"}"#.utf8))
        }
        guard case .notNow = refused else { Issue.record("upgraded on a 403"); return }
        #expect(await v2.count == 0)
    }
}

// MARK: - xbind's vectors (native/spec/tunnel-vectors.json, §10)

/// Byte for byte against what xbind's Go (flynn/noise) generates: keys and
/// EndpointIds, the prologue, the selector and psk, both handshake
/// messages and their frames, h, the code, and every transport frame each
/// way (rekeys and closes included) — then every invalid case fails where
/// it says. Skipped until the file exists in this tree.
@Suite struct TunnelSpecVectorTests {
    static var file: URL { repo.appendingPathComponent("native/spec/tunnel-vectors.json") }

    func load() throws -> JSONValue? {
        guard FileManager.default.fileExists(atPath: Self.file.path) else { return nil }
        return try JSONValue(parsing: Data(contentsOf: Self.file))
    }

    struct Vector {
        let j: JSONValue
        func h(_ k: String) -> Data { hexData(j[k]?.stringValue) }
        var purpose: NoiseTunnel.Purpose { j["alpn"]?.stringValue == TunnelALPN.pair ? .pair : .tunnel }
        var psk: Data? { j["psk"]?.stringValue.map(hex) }
        var link: TunnelLink {
            TunnelLink(server: try! ServerOrigin(string: "https://xbin.example.com"), code: j["code"]?.stringValue ?? "",
                       ticket: "endpointaaaa", serverKey: h("serverStaticPublic"), psk: psk ?? Data(count: 32))
        }

        func initiator(psk: Data? = nil, prologue: Data? = nil, serverKey: Data? = nil) throws -> NoiseHandshake {
            try NoiseHandshake(initiator: true, pattern: purpose.pattern, prologue: prologue ?? h("prologue"),
                               staticKey: h("deviceStaticPrivate"), remoteStatic: serverKey ?? h("serverStaticPublic"),
                               psk: psk ?? self.psk, ephemeral: h("deviceEphemeralPrivate"))
        }

        func responder(prologue: Data? = nil) throws -> NoiseHandshake {
            try NoiseHandshake(initiator: false, pattern: purpose.pattern, prologue: prologue ?? h("prologue"),
                               staticKey: h("serverStaticPrivate"), psk: psk, ephemeral: h("serverEphemeralPrivate"))
        }
    }

    @Test func vectors() async throws {
        guard let v = try load() else { return }
        #expect(v["version"]?.intValue == 1)
        let vectors = try #require(v["vectors"]?.arrayValue)
        #expect(Set(vectors.compactMap { $0["alpn"]?.stringValue }) == [TunnelALPN.pair, TunnelALPN.tunnel])
        for j in vectors {
            let x = Vector(j: j), name = j["name"]?.stringValue ?? "?"
            #expect(NoiseTunnel.Pattern.ik.protocolName == j["protocol"]?.stringValue || x.purpose == .pair, "\(name)")
            #expect(x.purpose.pattern.protocolName == j["protocol"]?.stringValue, "\(name) protocol")
            // Keys: X25519 publics, EndpointIds = the iroh secrets' Ed25519 publics, the link's k.
            #expect(try NoiseTunnel.publicKey(of: x.h("deviceStaticPrivate")) == x.h("deviceStaticPublic"))
            #expect(try NoiseTunnel.publicKey(of: x.h("serverStaticPrivate")) == x.h("serverStaticPublic"))
            #expect(try NoiseTunnel.publicKey(of: x.h("deviceEphemeralPrivate")) == x.h("deviceEphemeralPublic"))
            #expect(TunnelDeviceKeys(irohSecret: x.h("deviceIrohSecret"), noiseStatic: x.h("deviceStaticPrivate")).endpointId
                == x.h("deviceEndpointId"), "\(name) device EndpointId")
            #expect(TunnelDeviceKeys(irohSecret: x.h("serverIrohSecret"), noiseStatic: Data(count: 32)).endpointId
                == x.h("serverEndpointId"), "\(name) server EndpointId")
            #expect(j["serverKey"]?.stringValue.flatMap(TunnelLink.key32) == x.h("serverStaticPublic"))
            #expect(NoiseTunnel.prologue(x.purpose, server: x.h("serverEndpointId"), device: x.h("deviceEndpointId")) == x.h("prologue"),
                    "\(name) prologue")
            if x.purpose == .pair {
                #expect(NoiseTunnel.selector(x.link) == x.h("selector"), "selector")
                #expect(x.h("message1Payload") == x.h("selector"))
                #expect(j["pskLink"]?.stringValue.flatMap(TunnelLink.key32) == x.psk)
            } else {
                #expect(x.h("message1Payload").isEmpty)
            }
            // The handshake, both roles.
            var i = try x.initiator(), r = try x.responder()
            let m1 = try i.writeMessage(x.h("message1Payload"))
            #expect(m1 == x.h("message1"), "\(name) message 1")
            #expect(try NoiseFrame.framed(m1) == x.h("message1Frame"))
            #expect(try r.readMessage(m1) == x.h("message1Payload"))
            #expect(r.remoteStatic == x.h("deviceStaticPublic"))
            let m2 = try r.writeMessage()
            #expect(m2 == x.h("message2"), "\(name) message 2")
            #expect(try NoiseFrame.framed(m2) == x.h("message2Frame"))
            #expect(try i.readMessage(m2).isEmpty)
            #expect(i.handshakeHash == x.h("handshakeHash"), "\(name) h")
            #expect(r.handshakeHash == x.h("handshakeHash"))
            // The code.
            #expect(NoiseTunnel.sasOKM(handshakeHash: i.handshakeHash) == x.h("sasOkm"), "\(name) sasOkm")
            #expect(TunnelSAS.number(x.h("sasOkm")).map(Int64.init) == j["sas"]?.intValue)
            #expect(NoiseTunnel.sas(handshakeHash: i.handshakeHash) == j["sasDisplay"]?.stringValue)
            // Transport, each way: the sender's frames, then a receiver over them.
            var (dSend, dRecv) = try i.split()
            var (sSend, sRecv) = try r.split()
            try checkDirection(j["deviceToServer"], send: &dSend, name: "\(name) device→server")
            try checkDirection(j["serverToDevice"], send: &sSend, name: "\(name) server→device")
            try await receive(j["deviceToServer"], cipher: sRecv, name: name)
            try await receive(j["serverToDevice"], cipher: dRecv, name: name)
            _ = (dRecv, sRecv)
        }
    }

    /// The sender's frames: encrypt(type ‖ body), framed; REKEY right after a rekey frame.
    func checkDirection(_ frames: JSONValue?, send: inout NoiseCipherState, name: String) throws {
        for (n, f) in (frames?.arrayValue ?? []).enumerated() {
            let type = UInt8(f["type"]?.intValue ?? 99)
            let frame = try NoiseFrame.framed(send.encrypt(ad: Data(), Data([type]) + hexData(f["body"]?.stringValue)))
            #expect(frame == hexData(f["frame"]?.stringValue), "\(name) frame \(n)")
            if type == NoiseFrame.rekey { send.rekey() }
        }
    }

    /// The receiver: data bodies in order, the rekey followed, the close an orderly end.
    func receive(_ frames: JSONValue?, cipher: NoiseCipherState, name: String) async throws {
        let all = frames?.arrayValue ?? []
        let (a, b) = MemoryStream.pair(chunk: 13)
        for f in all { try await a.write(hexData(f["frame"]?.stringValue)) }
        try await a.finish()
        let rx = NoiseReceiver(frames: NoiseFrameReader(b), cipher: cipher)
        for f in all where f["type"]?.intValue == 0 {
            #expect(try await rx.receive() == hexData(f["body"]?.stringValue), "\(name) data")
        }
        if all.last?["type"]?.intValue == 2 { #expect(try await rx.receive() == Data(), "\(name) close") }
    }

    @Test func invalidCases() async throws {
        guard let v = try load() else { return }
        let bases = Dictionary((v["vectors"]?.arrayValue ?? []).map { ($0["name"]?.stringValue ?? "", $0) }, uniquingKeysWith: { a, _ in a })
        let invalid = try #require(v["invalid"]?.arrayValue)
        #expect(invalid.count >= 5)
        for c in invalid {
            let name = c["name"]?.stringValue ?? "?"
            let base = try #require(bases[c["base"]?.stringValue ?? ""], "\(name): base")
            let x = Vector(j: base)
            let field = { (k: String) -> Data? in c[k]?.stringValue.map(hex) }
            switch (c["side"]?.stringValue, c["step"]?.stringValue) {
            case ("device"?, "writeMessage1"?):
                var i = try x.initiator(serverKey: field("serverStaticPublic"))
                #expect(throws: (any Error).self, "\(name)") { try i.writeMessage(x.h("message1Payload")) }
            case ("device"?, "readMessage2"?):
                var i = try x.initiator(psk: field("psk"))
                let m1 = try i.writeMessage(x.h("message1Payload"))
                #expect(m1 == x.h("message1"), "\(name): message 1 doesn't depend on the psk")
                #expect(throws: (any Error).self, "\(name)") { try i.readMessage(field("message2") ?? x.h("message2")) }
            case ("server"?, "readMessage1"?):
                var r = try x.responder(prologue: field("prologue"))
                #expect(throws: (any Error).self, "\(name)") { try r.readMessage(field("message1") ?? x.h("message1")) }
            case ("server"?, "transport"?):
                var i = try x.initiator(), r = try x.responder()
                _ = try r.readMessage(i.writeMessage(x.h("message1Payload")))
                _ = try i.readMessage(r.writeMessage())
                let (_, sRecv) = try r.split()
                let (a, b) = MemoryStream.pair(chunk: 11)
                try await a.write(field("stream") ?? Data())
                try await a.finish()
                let rx = NoiseReceiver(frames: NoiseFrameReader(b), cipher: sRecv)
                var failed = false
                for _ in 0..<16 {
                    do {
                        if try await rx.receive().isEmpty { break }
                    } catch {
                        failed = true
                        break
                    }
                }
                #expect(failed, "\(name): the stream must fail, never end in order")
            default:
                Issue.record("\(name): unknown case \(c["side"]?.stringValue ?? "?")/\(c["step"]?.stringValue ?? "?")")
            }
        }
    }
}
