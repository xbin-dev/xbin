import Foundation
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif

// The device-login key when there is no Secure Enclave (the simulator) — and
// the reference the enclave path must agree with: SPKI DER public keys,
// ECDSA P-256 / SHA-256 signatures in ASN.1 DER (native/spec/device-login.md
// §1). Shared with native/tools/app-check, which verifies it against the
// spec's test vector and a live xbind on Linux.

struct SoftwareDeviceKey {
    let key: P256.Signing.PrivateKey

    init() { key = P256.Signing.PrivateKey() }

    /// From the raw 32-byte scalar (Keychain storage; the spec's vector).
    init(rawRepresentation raw: Data) throws { key = try P256.Signing.PrivateKey(rawRepresentation: raw) }

    var rawRepresentation: Data { key.rawRepresentation }

    /// `publicKey` for `POST /api/xbin/devices/enroll`.
    var publicKeySPKI: Data { key.publicKey.derRepresentation }

    /// The login signature over the message bytes (SHA-256 inside).
    func sign(_ message: Data) throws -> Data { try key.signature(for: message).derRepresentation }

    /// Checks a DER signature against an SPKI public key.
    static func verify(signature der: Data, message: Data, publicKeySPKI spki: Data) -> Bool {
        guard let pub = try? P256.Signing.PublicKey(derRepresentation: spki),
              let sig = try? P256.Signing.ECDSASignature(derRepresentation: der) else { return false }
        return pub.isValidSignature(sig, for: message)
    }
}
