import Foundation
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif
import XbinCore

// The xbin layer of an app tunnel (D204; native/spec/tunnel.md §4–§6):
// Noise inside every iroh stream, so the relays and xbind's iroh sidecar see
// only ciphertext. `Noise_IKpsk2_25519_ChaChaPoly_SHA256` pairs a device
// (the psk from the QR code), `Noise_IK_25519_ChaChaPoly_SHA256` carries its
// HTTP afterwards; the device is always the initiator. Written out from the
// Noise specification (revision 34) over CryptoKit's X25519, ChaChaPoly,
// SHA-256 and HMAC, so it reads like the spec. Compiled into the app, and
// into native/tools/app-check on Linux (swift-crypto has CryptoKit's API),
// which checks it against the Noise project's published IK/IKpsk2 vectors
// and xbind's native/spec/tunnel-vectors.json.

enum NoiseTunnelError: Error, Equatable, Sendable, CustomStringConvertible {
    /// An X25519 result of all zeros (a low-order point): the handshake ends.
    case lowOrder
    /// A message failed to decrypt (a wrong key or psk, a flipped byte).
    case decrypt
    /// A handshake message of the wrong length, or one out of turn.
    case malformed(String)
    /// The stream ended without the peer's authenticated close.
    case truncated
    /// A transport message whose type isn't data, rekey or close — or a
    /// frame after the peer's close.
    case badFrame(String)
    /// A frame over 65535 bytes, or a send after this side's close.
    case misuse(String)
    /// 2^64-1 messages under one key (never in practice: rekeying keeps n).
    case nonceExhausted

    var description: String {
        switch self {
        case .lowOrder: return "low-order key"
        case .decrypt: return "decryption failed"
        case .malformed(let m): return "malformed handshake: \(m)"
        case .truncated: return "the stream ended without a close"
        case .badFrame(let m): return "bad frame: \(m)"
        case .misuse(let m): return m
        case .nonceExhausted: return "nonce exhausted"
        }
    }
}

enum NoiseTunnel {
    enum Pattern: Sendable {
        /// `tunnel/1`.
        case ik
        /// `pair/1`: the psk at the end of message 2.
        case ikpsk2

        var protocolName: String {
            switch self {
            case .ik: return "Noise_IK_25519_ChaChaPoly_SHA256"
            case .ikpsk2: return "Noise_IKpsk2_25519_ChaChaPoly_SHA256"
            }
        }
    }

    /// Which ALPN a prologue binds.
    enum Purpose: Sendable {
        case pair, tunnel

        var label: String { self == .pair ? "xbin-pair-v1" : "xbin-tunnel-v1" }
        var pattern: Pattern { self == .pair ? .ikpsk2 : .ik }
    }

    static let dhLength = 32
    static let tagLength = 16
    /// A frame's ciphertext, tag included (len16).
    static let maxMessage = 65535
    /// A transport frame's body: the ciphertext's room less the tag and type.
    static let maxBody = maxMessage - tagLength - 1

    /// The prologue (§4): the ALPN's label, then the server's and the
    /// device's EndpointIds (32 bytes each), binding the session to the
    /// iroh peers.
    static func prologue(_ purpose: Purpose, server: Data, device: Data) -> Data {
        Data(purpose.label.utf8) + server + device
    }

    /// `SHA-256("xbin-pair-select-v1\n" ‖ code)[0:16]` (§4): message 1's
    /// payload on `pair/1`, so xbind finds the pending pairing; the code
    /// itself never travels.
    static func selector(_ link: TunnelLink) -> Data {
        Data(SHA256.hash(data: link.selectorInput)).prefix(16)
    }

    /// `HKDF-SHA256(ikm = h, salt = "", info = "xbin-sas-v1", L = 8)` (§5),
    /// RFC 5869 written out: extract with the empty salt (HMAC pads a key
    /// with zeros, so it is HashLen zeros), one expand block.
    static func sasOKM(handshakeHash h: Data) -> Data {
        let prk = hmac(key: Data(), h)
        return hmac(key: prk, Data("xbin-sas-v1".utf8) + Data([1])).prefix(8)
    }

    /// The six digits, grouped: `012 345`.
    static func sas(handshakeHash h: Data) -> String {
        TunnelSAS.digits(sasOKM(handshakeHash: h)) ?? "000 000"
    }

    static func hmac(key: Data, _ data: Data) -> Data {
        Data(HMAC<SHA256>.authenticationCode(for: data, using: SymmetricKey(data: key)))
    }

    static func hash(_ data: Data) -> Data { Data(SHA256.hash(data: data)) }

    /// Noise's HKDF (§4.3): `n` outputs of HASHLEN from the chaining key.
    static func hkdf(_ chainingKey: Data, _ ikm: Data, outputs n: Int) -> [Data] {
        let temp = hmac(key: chainingKey, ikm)
        var out: [Data] = []
        var prev = Data()
        for i in 1...n {
            prev = hmac(key: temp, prev + Data([UInt8(i)]))
            out.append(prev)
        }
        return out
    }

    /// X25519, refusing a low-order result (all zeros) — swift-crypto's
    /// BoringSSL throws for it, CryptoKit may return the zeros.
    static func dh(_ privateKey: Data, _ publicKey: Data) throws -> Data {
        guard publicKey.count == dhLength else { throw NoiseTunnelError.malformed("a public key of \(publicKey.count) bytes") }
        let shared: Data
        do {
            let k = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: privateKey)
            let p = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: publicKey)
            shared = try k.sharedSecretFromKeyAgreement(with: p).withUnsafeBytes { Data($0) }
        } catch {
            throw NoiseTunnelError.lowOrder
        }
        guard shared.contains(where: { $0 != 0 }) else { throw NoiseTunnelError.lowOrder }
        return shared
    }

    /// A new X25519 key pair (private raw, public raw).
    static func newKeyPair() -> (privateKey: Data, publicKey: Data) {
        let k = Curve25519.KeyAgreement.PrivateKey()
        return (k.rawRepresentation, k.publicKey.rawRepresentation)
    }

    static func publicKey(of privateKey: Data) throws -> Data {
        try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: privateKey).publicKey.rawRepresentation
    }
}

// MARK: - CipherState, SymmetricState (§5.1, §5.2)

struct NoiseCipherState: Sendable {
    private(set) var key: Data?
    private(set) var n: UInt64 = 0

    init(key: Data? = nil) { self.key = key }

    var hasKey: Bool { key != nil }

    /// `4×0 ‖ LE64(n)`.
    static func nonce(_ n: UInt64) -> Data {
        var d = Data(count: 4)
        withUnsafeBytes(of: n.littleEndian) { d.append(contentsOf: $0) }
        return d
    }

    mutating func encrypt(ad: Data, _ plaintext: Data) throws -> Data {
        guard let key else { return plaintext }
        guard n < UInt64.max else { throw NoiseTunnelError.nonceExhausted }
        let out = try Self.seal(key: key, n: n, ad: ad, plaintext)
        n += 1
        return out
    }

    mutating func decrypt(ad: Data, _ ciphertext: Data) throws -> Data {
        guard let key else { return ciphertext }
        guard n < UInt64.max else { throw NoiseTunnelError.nonceExhausted }
        guard ciphertext.count >= NoiseTunnel.tagLength else { throw NoiseTunnelError.decrypt }
        let c = Data(ciphertext)
        let body = c.prefix(c.count - NoiseTunnel.tagLength), tag = c.suffix(NoiseTunnel.tagLength)
        let plain: Data
        do {
            let box = try ChaChaPoly.SealedBox(nonce: ChaChaPoly.Nonce(data: Self.nonce(n)), ciphertext: body, tag: tag)
            plain = try ChaChaPoly.open(box, using: SymmetricKey(data: key), authenticating: ad)
        } catch {
            throw NoiseTunnelError.decrypt
        }
        n += 1
        return plain
    }

    /// `REKEY(k)`: the first 32 bytes of `ENCRYPT(k, 2^64-1, "", 32×0)`.
    /// The nonce counter carries on (§11.3).
    mutating func rekey() {
        guard let key, let next = try? Self.seal(key: key, n: UInt64.max, ad: Data(), Data(count: 32)) else { return }
        self.key = next.prefix(32)
    }

    private static func seal(key: Data, n: UInt64, ad: Data, _ plaintext: Data) throws -> Data {
        let box = try ChaChaPoly.seal(plaintext, using: SymmetricKey(data: key), nonce: ChaChaPoly.Nonce(data: nonce(n)),
                                      authenticating: ad)
        return box.ciphertext + box.tag
    }
}

struct NoiseSymmetricState: Sendable {
    private(set) var ck: Data
    private(set) var h: Data
    private(set) var cipher = NoiseCipherState()

    init(protocolName: String) {
        let name = Data(protocolName.utf8)
        h = name.count <= 32 ? name + Data(count: 32 - name.count) : NoiseTunnel.hash(name)
        ck = h
    }

    mutating func mixHash(_ data: Data) { h = NoiseTunnel.hash(h + data) }

    mutating func mixKey(_ ikm: Data) {
        let o = NoiseTunnel.hkdf(ck, ikm, outputs: 2)
        ck = o[0]
        cipher = NoiseCipherState(key: o[1])
    }

    mutating func mixKeyAndHash(_ ikm: Data) {
        let o = NoiseTunnel.hkdf(ck, ikm, outputs: 3)
        ck = o[0]
        mixHash(o[1])
        cipher = NoiseCipherState(key: o[2])
    }

    mutating func encryptAndHash(_ plaintext: Data) throws -> Data {
        let c = try cipher.encrypt(ad: h, plaintext)
        mixHash(c)
        return c
    }

    mutating func decryptAndHash(_ ciphertext: Data) throws -> Data {
        let p = try cipher.decrypt(ad: h, ciphertext)
        mixHash(ciphertext)
        return p
    }

    func split() -> (NoiseCipherState, NoiseCipherState) {
        let o = NoiseTunnel.hkdf(ck, Data(), outputs: 2)
        return (NoiseCipherState(key: o[0]), NoiseCipherState(key: o[1]))
    }
}

// MARK: - HandshakeState: IK and IKpsk2 (§5.3, §7.5, §9)

/// One handshake. IK's messages: `-> e, es, s, ss` and `<- e, ee, se`
/// (IKpsk2: `<- e, ee, se, psk`), the responder's static key known to the
/// initiator beforehand.
struct NoiseHandshake: Sendable {
    let initiator: Bool
    let pattern: NoiseTunnel.Pattern
    private var ss: NoiseSymmetricState
    private let s: Data, sPub: Data
    private var e: Data?, ePub: Data?
    private(set) var rs: Data?
    private var re: Data?
    private var step = 0
    /// An ephemeral key to use instead of a random one (the vectors).
    private var fixedEphemeral: Data?

    /// `staticKey`: this side's private key. `remoteStatic`: the
    /// responder's public key (the initiator's `k`). `psk`: IKpsk2's (the
    /// responder may set it after message 1, ``setPSK(_:)``).
    init(initiator: Bool, pattern: NoiseTunnel.Pattern, prologue: Data, staticKey: Data, remoteStatic: Data? = nil,
         psk: Data? = nil, ephemeral: Data? = nil) throws {
        guard staticKey.count == 32 else { throw NoiseTunnelError.malformed("a static key of \(staticKey.count) bytes") }
        if initiator {
            guard let rs = remoteStatic, rs.count == 32 else { throw NoiseTunnelError.malformed("no responder key") }
        }
        if let psk { guard psk.count == 32 else { throw NoiseTunnelError.malformed("a psk of \(psk.count) bytes") } }
        self.initiator = initiator
        self.pattern = pattern
        s = staticKey
        sPub = try NoiseTunnel.publicKey(of: staticKey)
        rs = initiator ? remoteStatic : nil
        pskSet = psk
        fixedEphemeral = ephemeral
        ss = NoiseSymmetricState(protocolName: pattern.protocolName)
        ss.mixHash(prologue)
        // The pre-message: the responder's static key.
        ss.mixHash(initiator ? remoteStatic! : sPub)
    }

    private var pskSet: Data?

    /// The responder's psk, found from message 1's payload (the selector).
    mutating func setPSK(_ psk: Data) throws {
        guard psk.count == 32 else { throw NoiseTunnelError.malformed("a psk of \(psk.count) bytes") }
        pskSet = psk
    }

    var isComplete: Bool { step == 2 }
    var handshakeHash: Data { ss.h }
    /// The peer's static key (the responder learns it from message 1).
    var remoteStatic: Data? { rs }

    private var usesPSK: Bool { pattern == .ikpsk2 }

    private mutating func newEphemeral() throws -> Data {
        let priv = fixedEphemeral ?? NoiseTunnel.newKeyPair().privateKey
        fixedEphemeral = nil
        let pub = try NoiseTunnel.publicKey(of: priv)
        e = priv
        ePub = pub
        return pub
    }

    private mutating func writeE() throws -> Data {
        let pub = try newEphemeral()
        ss.mixHash(pub)
        if usesPSK { ss.mixKey(pub) }
        return pub
    }

    private mutating func readE(_ m: inout Data) throws {
        guard m.count >= 32 else { throw NoiseTunnelError.malformed("short message") }
        let pub = Data(m.prefix(32))
        m = Data(m.dropFirst(32))
        re = pub
        ss.mixHash(pub)
        if usesPSK { ss.mixKey(pub) }
    }

    /// Writes the next handshake message with `payload`.
    mutating func writeMessage(_ payload: Data = Data()) throws -> Data {
        switch (initiator, step) {
        case (true, 0):
            var out = try writeE()                                         // e
            ss.mixKey(try NoiseTunnel.dh(e!, rs!))                         // es
            out += try ss.encryptAndHash(sPub)                             // s
            ss.mixKey(try NoiseTunnel.dh(s, rs!))                          // ss
            out += try ss.encryptAndHash(payload)
            step = 1
            return out
        case (false, 1):
            guard let re, let rs else { throw NoiseTunnelError.malformed("message 2 before message 1") }
            var out = try writeE()                                         // e
            ss.mixKey(try NoiseTunnel.dh(e!, re))                          // ee
            ss.mixKey(try NoiseTunnel.dh(e!, rs))                          // se
            if usesPSK {                                                   // psk
                guard let p = pskSet else { throw NoiseTunnelError.malformed("no psk") }
                ss.mixKeyAndHash(p)
            }
            out += try ss.encryptAndHash(payload)
            step = 2
            return out
        default:
            throw NoiseTunnelError.malformed("no message to write now")
        }
    }

    /// Reads the peer's handshake message; returns its payload.
    mutating func readMessage(_ message: Data) throws -> Data {
        var m = Data(message)
        switch (initiator, step) {
        case (false, 0):
            try readE(&m)                                                  // e
            ss.mixKey(try NoiseTunnel.dh(s, re!))                          // es
            let sLen = 32 + (ss.cipher.hasKey ? NoiseTunnel.tagLength : 0)
            guard m.count >= sLen else { throw NoiseTunnelError.malformed("short message") }
            let rsPub = try ss.decryptAndHash(Data(m.prefix(sLen)))       // s
            m = Data(m.dropFirst(sLen))
            rs = rsPub
            ss.mixKey(try NoiseTunnel.dh(s, rsPub))                        // ss
            let payload = try ss.decryptAndHash(m)
            step = 1
            return payload
        case (true, 1):
            try readE(&m)                                                  // e
            ss.mixKey(try NoiseTunnel.dh(e!, re!))                         // ee
            ss.mixKey(try NoiseTunnel.dh(s, re!))                          // se
            if usesPSK {                                                   // psk
                guard let p = pskSet else { throw NoiseTunnelError.malformed("no psk") }
                ss.mixKeyAndHash(p)
            }
            let payload = try ss.decryptAndHash(m)
            step = 2
            return payload
        default:
            throw NoiseTunnelError.malformed("no message to read now")
        }
    }

    /// The transport keys once complete: (send, receive) for this side.
    func split() throws -> (send: NoiseCipherState, receive: NoiseCipherState) {
        guard isComplete else { throw NoiseTunnelError.malformed("the handshake isn't complete") }
        let (c1, c2) = ss.split()
        return initiator ? (c1, c2) : (c2, c1)
    }
}

// MARK: - Frames over a byte stream (§4: len16 ‖ message)

/// Reads `len16(BE) ‖ message` frames.
actor NoiseFrameReader {
    let stream: any ByteStream
    private var buffer = Data()
    private var ended = false

    init(_ stream: any ByteStream) { self.stream = stream }

    /// The next frame; nil at an end of stream between frames (the caller
    /// decides whether that's truncation); a frame cut short is.
    func next() async throws -> Data? {
        while true {
            if buffer.count >= 2 {
                let n = Int(buffer[buffer.startIndex]) << 8 | Int(buffer[buffer.startIndex + 1])
                if buffer.count >= 2 + n {
                    let frame = Data(buffer.dropFirst(2).prefix(n))
                    buffer = Data(buffer.dropFirst(2 + n))
                    return frame
                }
            }
            if ended {
                if buffer.isEmpty { return nil }
                throw NoiseTunnelError.truncated
            }
            let chunk = try await stream.read(max: 64 * 1024)
            if chunk.isEmpty { ended = true } else { buffer.append(chunk) }
        }
    }
}

enum NoiseFrame {
    static let data: UInt8 = 0x00
    static let rekey: UInt8 = 0x01
    static let close: UInt8 = 0x02

    static func framed(_ message: Data) throws -> Data {
        guard message.count <= NoiseTunnel.maxMessage else { throw NoiseTunnelError.misuse("a frame over 65535 bytes") }
        return Data([UInt8(message.count >> 8), UInt8(message.count & 0xFF)]) + message
    }
}

/// When a sender rekeys (§4): after 2^20 messages, 2^30 plaintext bytes or
/// 10 minutes since its last (re)key, whichever comes first.
struct NoiseRekeyPolicy: Sendable {
    var maxMessages: UInt64 = 1 << 20
    var maxBytes: UInt64 = 1 << 30
    var maxAge: TimeInterval = 600
    var now: @Sendable () -> Date = Date.init

    private(set) var messages: UInt64 = 0
    private(set) var bytes: UInt64 = 0
    private(set) var since: Date?

    mutating func start() { since = now() }

    func isDue() -> Bool {
        messages >= maxMessages || bytes >= maxBytes || (since.map { now().timeIntervalSince($0) >= maxAge } ?? false)
    }

    mutating func sent(_ plaintextBytes: Int) {
        messages += 1
        bytes += UInt64(plaintextBytes)
    }

    mutating func rekeyed() {
        messages = 0
        bytes = 0
        since = now()
    }
}

/// The sending half of a tunnel stream: typed frames, the rekey schedule,
/// an authenticated close. One sender at a time (each direction of a
/// splice has one task).
actor NoiseSender {
    private let stream: any ByteStream
    private var cipher: NoiseCipherState
    private var policy: NoiseRekeyPolicy
    private var closed = false
    private var busy = false
    /// Rekeys sent (tests).
    private(set) var rekeys = 0

    init(stream: any ByteStream, cipher: NoiseCipherState, policy: NoiseRekeyPolicy) {
        self.stream = stream
        self.cipher = cipher
        var p = policy
        p.start()
        self.policy = p
    }

    /// Sends `data` as data frames (split at 65518 bytes).
    func send(_ data: Data) async throws {
        try enter()
        defer { busy = false }
        guard !data.isEmpty else { return }
        var rest = Data(data)
        while !rest.isEmpty {
            let chunk = Data(rest.prefix(NoiseTunnel.maxBody))
            rest = Data(rest.dropFirst(chunk.count))
            if policy.isDue() { try await writeRekey() }
            try await write(NoiseFrame.data, chunk)
            policy.sent(chunk.count)
        }
    }

    /// Rekeys now (the schedule does it by itself).
    func rekey() async throws {
        try enter()
        defer { busy = false }
        try await writeRekey()
    }

    /// The authenticated end of this direction, then the stream's half-close.
    func close() async throws {
        guard !closed else { return }
        try enter()
        defer { busy = false }
        try await write(NoiseFrame.close, Data())
        closed = true
        try await stream.finish()
    }

    private func enter() throws {
        guard !busy else { throw NoiseTunnelError.misuse("two senders at once") }
        guard !closed else { throw NoiseTunnelError.misuse("send after close") }
        busy = true
    }

    private func writeRekey() async throws {
        try await write(NoiseFrame.rekey, Data())
        // Right after sending it (§4).
        cipher.rekey()
        policy.rekeyed()
        rekeys += 1
    }

    private func write(_ type: UInt8, _ body: Data) async throws {
        let c = try cipher.encrypt(ad: Data(), Data([type]) + body)
        try await stream.write(NoiseFrame.framed(c))
    }
}

/// The receiving half: decrypts frames in order, follows the peer's rekeys,
/// and tells an authenticated close from truncation.
actor NoiseReceiver {
    private let frames: NoiseFrameReader
    private var cipher: NoiseCipherState
    private var closed = false

    init(frames: NoiseFrameReader, cipher: NoiseCipherState) {
        self.frames = frames
        self.cipher = cipher
    }

    /// One data frame's bytes (never empty); empty = the peer closed its
    /// direction. Throws ``NoiseTunnelError/truncated`` when the stream ends
    /// without that close — never an orderly end.
    func receive() async throws -> Data {
        guard !closed else { return Data() }
        while true {
            guard let frame = try await frames.next() else { throw NoiseTunnelError.truncated }
            let plain = try cipher.decrypt(ad: Data(), frame)
            guard let type = plain.first else { throw NoiseTunnelError.badFrame("no type") }
            let body = Data(plain.dropFirst())
            switch type {
            case NoiseFrame.data:
                if body.isEmpty { continue }
                return body
            case NoiseFrame.rekey:
                guard body.isEmpty else { throw NoiseTunnelError.badFrame("a rekey with a body") }
                // Right after decrypting it (§4).
                cipher.rekey()
            case NoiseFrame.close:
                guard body.isEmpty else { throw NoiseTunnelError.badFrame("a close with a body") }
                closed = true
                return Data()
            default:
                throw NoiseTunnelError.badFrame("type \(type)")
            }
        }
    }
}

/// An established tunnel stream: its two halves and what the handshake
/// proved.
struct NoiseChannel: Sendable {
    let sender: NoiseSender
    let receiver: NoiseReceiver
    let stream: any ByteStream
    /// `h` once the handshake completed (the code's input on `pair/1`).
    let handshakeHash: Data
    /// The peer's static key (the device's, on xbind's side).
    let remoteStatic: Data
}

extension NoiseTunnel {
    /// The device's side (§4): message 1 out, message 2 in.
    static func initiate(_ stream: any ByteStream, pattern: Pattern, prologue: Data, staticKey: Data, serverKey: Data,
                         psk: Data? = nil, payload: Data = Data(), ephemeral: Data? = nil,
                         policy: NoiseRekeyPolicy = NoiseRekeyPolicy()) async throws -> NoiseChannel {
        do {
            var hs = try NoiseHandshake(initiator: true, pattern: pattern, prologue: prologue, staticKey: staticKey,
                                        remoteStatic: serverKey, psk: psk, ephemeral: ephemeral)
            let frames = NoiseFrameReader(stream)
            try await stream.write(NoiseFrame.framed(hs.writeMessage(payload)))
            guard let m2 = try await frames.next() else { throw NoiseTunnelError.truncated }
            _ = try hs.readMessage(m2)
            return try channel(hs, stream, frames, policy)
        } catch {
            // A failed handshake resets the stream.
            await stream.cancel()
            throw error
        }
    }

    /// xbind's side, for the tests: reads message 1, finds the psk from its
    /// payload (`psk`; nil = refuse), answers message 2.
    static func respond(_ stream: any ByteStream, pattern: Pattern, prologue: Data, staticKey: Data,
                        psk: (Data) -> Data? = { _ in nil }, payload: Data = Data(), ephemeral: Data? = nil,
                        policy: NoiseRekeyPolicy = NoiseRekeyPolicy()) async throws -> (NoiseChannel, Data) {
        do {
            var hs = try NoiseHandshake(initiator: false, pattern: pattern, prologue: prologue, staticKey: staticKey,
                                        ephemeral: ephemeral)
            let frames = NoiseFrameReader(stream)
            guard let m1 = try await frames.next() else { throw NoiseTunnelError.truncated }
            let first = try hs.readMessage(m1)
            if pattern == .ikpsk2 {
                guard let p = psk(first) else { throw NoiseTunnelError.malformed("unknown selector") }
                try hs.setPSK(p)
            }
            try await stream.write(NoiseFrame.framed(hs.writeMessage(payload)))
            return (try channel(hs, stream, frames, policy), first)
        } catch {
            // As xbind does: it closes the stream (the device sees its end).
            try? await stream.finish()
            throw error
        }
    }

    private static func channel(_ hs: NoiseHandshake, _ stream: any ByteStream, _ frames: NoiseFrameReader,
                                _ policy: NoiseRekeyPolicy) throws -> NoiseChannel {
        let (send, receive) = try hs.split()
        guard let rs = hs.remoteStatic else { throw NoiseTunnelError.malformed("no remote static key") }
        return NoiseChannel(sender: NoiseSender(stream: stream, cipher: send, policy: policy),
                            receiver: NoiseReceiver(frames: frames, cipher: receive), stream: stream,
                            handshakeHash: hs.handshakeHash, remoteStatic: rs)
    }

    /// A `tunnel/1` stream (§4, §7): IK with this workspace's keys, both
    /// handshake payloads empty. What the loopback forwarder runs on each
    /// new iroh stream before ``TunnelSplice``.
    static func openTunnel(_ stream: any ByteStream, secrets: TunnelSecrets, serverEndpoint: Data,
                           deviceEndpoint: Data) async throws -> NoiseChannel {
        try await initiate(stream, pattern: .ik, prologue: prologue(.tunnel, server: serverEndpoint, device: deviceEndpoint),
                           staticKey: secrets.noiseStatic, serverKey: secrets.serverKey)
    }
}

// MARK: - The splice (a loopback TCP connection ⇄ a tunnel stream)

enum TunnelSplice {
    /// Pumps `plain` (the forwarder's accepted TCP connection) through
    /// `channel` both ways until both directions have ended. The TCP side's
    /// end of stream becomes the authenticated close (and the iroh stream's
    /// finish); the peer's close half-closes TCP. Truncation or any error
    /// resets both and is thrown.
    static func run(plain: any ByteStream, channel: NoiseChannel) async throws {
        try await withTaskCancellationHandler {
            try await withThrowingTaskGroup(of: Void.self) { group in
                group.addTask {
                    while true {
                        let d = try await plain.read(max: NoiseTunnel.maxBody)
                        if d.isEmpty {
                            try await channel.sender.close()
                            return
                        }
                        try await channel.sender.send(d)
                    }
                }
                group.addTask {
                    while true {
                        let d = try await channel.receiver.receive()
                        if d.isEmpty {
                            try await plain.finish()
                            return
                        }
                        try await plain.write(d)
                    }
                }
                do {
                    // next(), not waitForAll(): that one waits for every
                    // child before it throws.
                    while try await group.next() != nil {}
                } catch {
                    // The other direction may sit in a read: end both
                    // streams so it returns before the group does.
                    await plain.cancel()
                    await channel.stream.cancel()
                    throw error
                }
            }
        } onCancel: {
            Task {
                await plain.cancel()
                await channel.stream.cancel()
            }
        }
    }
}

// MARK: - Pairing (§5, §6), the device's side

/// Runs `pair/1` over an iroh stream: IKpsk2 with the QR code's psk and
/// the selector, the device's hello, then xbind's answers until `enrolled`
/// or `rejected`. `onCode` is called with the confirmation code once xbind
/// shows it on the computer (`await`).
struct PairingClient: Sendable {
    let link: TunnelLink
    let hello: PairingHello
    /// The device's Noise static private key for this workspace.
    let staticKey: Data
    let serverEndpoint: Data
    let deviceEndpoint: Data
    /// How long to wait for the computer (the code's 5 minutes, plus the
    /// confirmation's 2).
    var timeout: TimeInterval = 7 * 60
    var ephemeral: Data?

    func run(_ stream: any ByteStream,
             onCode: @escaping @Sendable (_ code: String, _ expires: Date?) async -> Void) async throws -> PairingEnrolled {
        let expired = PairingDeadline()
        do {
            return try await withTaskCancellationHandler {
                try await withThrowingTaskGroup(of: PairingEnrolled.self) { group in
                    group.addTask { try await self.exchange(stream, onCode: onCode) }
                    let limit = timeout
                    group.addTask {
                        try await Task.sleep(nanoseconds: UInt64(limit * 1_000_000_000))
                        // Ends the exchange's read, so the group can return.
                        expired.fire()
                        await stream.cancel()
                        throw PairingFailure.timedOut
                    }
                    defer { group.cancelAll() }
                    guard let r = try await group.next() else { throw PairingFailure.protocolError("no answer") }
                    return r
                }
            } onCancel: {
                Task { await stream.cancel() }
            }
        } catch {
            await stream.cancel()
            if expired.fired { throw PairingFailure.timedOut }
            throw Self.failure(error)
        }
    }

    private func exchange(_ stream: any ByteStream,
                          onCode: @escaping @Sendable (String, Date?) async -> Void) async throws -> PairingEnrolled {
        let channel: NoiseChannel
        do {
            channel = try await NoiseTunnel.initiate(stream, pattern: .ikpsk2,
                                                     prologue: NoiseTunnel.prologue(.pair, server: serverEndpoint, device: deviceEndpoint),
                                                     staticKey: staticKey, serverKey: link.serverKey, psk: link.psk,
                                                     payload: NoiseTunnel.selector(link), ephemeral: ephemeral)
        } catch NoiseTunnelError.truncated {
            throw PairingFailure.handshake("the workspace didn't take the code")
        } catch let e as NoiseTunnelError {
            throw PairingFailure.handshake(e.description)
        } catch let p as PairingFailure {
            throw p
        } catch {
            throw PairingFailure.unreachable(Self.text(error))
        }
        let code = NoiseTunnel.sas(handshakeHash: channel.handshakeHash)
        try await channel.sender.send(hello.data)
        var shown = false
        while true {
            let m = try await channel.receiver.receive()
            if m.isEmpty { throw PairingFailure.protocolError("the workspace closed the pairing without an answer") }
            let reply: PairingReply
            do { reply = try PairingReply(data: m) } catch { throw PairingFailure.protocolError(Self.text(error)) }
            switch reply {
            case .await(let expires):
                if !shown {
                    shown = true
                    await onCode(code, expires)
                }
            case .enrolled(let e):
                try? await channel.sender.close()
                return e
            case .rejected(let r, let message):
                try? await channel.sender.close()
                throw PairingFailure.rejected(r, message: message)
            }
        }
    }

    static func failure(_ error: any Error) -> PairingFailure {
        switch error {
        case let p as PairingFailure: return p
        case NoiseTunnelError.truncated: return .unreachable("the connection dropped")
        case let n as NoiseTunnelError: return .protocolError(n.description)
        case is CancellationError: return .unreachable("cancelled")
        default: return .unreachable(text(error))
        }
    }

    static func text(_ error: any Error) -> String {
        if type(of: error) is NSError.Type { return (error as NSError).localizedDescription }
        return String(describing: error)
    }
}

/// Whether a pairing's time ran out (set before its stream is cancelled, so
/// the read error that follows reads as the timeout it is).
final class PairingDeadline: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false

    var fired: Bool {
        lock.lock()
        defer { lock.unlock() }
        return value
    }

    func fire() {
        lock.lock()
        value = true
        lock.unlock()
    }
}
