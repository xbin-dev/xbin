import Foundation
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif
import XbinCore

// Opening (and, for tests, sealing) push envelopes — native/spec/push.md §3.
// Compiled into the app and the Notification Service Extension, and into
// native/tools/app-check on Linux (swift-crypto has the same API), which
// checks it against native/spec/push-vectors.json.

enum PushCrypto {
    /// Opens `env` with the device's X25519 private key (raw, 32 bytes).
    /// nil = not sealed to this key (the GCM tag fails) or malformed.
    static func open(_ env: PushEnvelope, privateKey raw: Data) -> Data? {
        guard let priv = try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw),
              let key = derive(priv: priv, epk: env.epk) else { return nil }
        guard let nonce = try? AES.GCM.Nonce(data: env.nonce),
              let box = try? AES.GCM.SealedBox(nonce: nonce, ciphertext: env.ciphertext, tag: env.tag)
        else { return nil }
        return try? AES.GCM.open(box, using: key)
    }

    /// The AES key: HKDF-SHA256(X25519(r, E), salt: E ‖ R, info: "xbin-push-v1").
    static func derive(priv: Curve25519.KeyAgreement.PrivateKey, epk: Data) -> SymmetricKey? {
        guard let e = try? Curve25519.KeyAgreement.PublicKey(rawRepresentation: epk),
              let shared = try? priv.sharedSecretFromKeyAgreement(with: e) else { return nil }
        // An all-zero secret means a low-order point: refuse (push.md §3).
        let zero = shared.withUnsafeBytes { $0.allSatisfy { $0 == 0 } }
        guard !zero else { return nil }
        let salt = epk + priv.publicKey.rawRepresentation
        return shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: salt, sharedInfo: PushEnvelope.info,
                                              outputByteCount: 32)
    }

    /// Seals `plaintext` to `recipient` with a given ephemeral key and nonce
    /// — what xbind does; here to reproduce the vectors.
    static func seal(_ plaintext: Data, to recipient: Data, ephemeral: Data, nonce: Data) -> PushEnvelope? {
        guard let e = try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: ephemeral),
              let r = try? Curve25519.KeyAgreement.PublicKey(rawRepresentation: recipient),
              let shared = try? e.sharedSecretFromKeyAgreement(with: r) else { return nil }
        let epk = e.publicKey.rawRepresentation
        let key = shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: epk + recipient,
                                                 sharedInfo: PushEnvelope.info, outputByteCount: 32)
        guard let n = try? AES.GCM.Nonce(data: nonce), let box = try? AES.GCM.seal(plaintext, using: key, nonce: n)
        else { return nil }
        return PushEnvelope(epk: epk, nonce: nonce, sealed: box.ciphertext + box.tag)
    }

    /// A new device key pair: (private raw, public raw), 32 bytes each.
    static func newKeyPair() -> (privateKey: Data, publicKey: Data) {
        let k = Curve25519.KeyAgreement.PrivateKey()
        return (k.rawRepresentation, k.publicKey.rawRepresentation)
    }

    static func publicKey(of privateKey: Data) -> Data? {
        (try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: privateKey))?.publicKey.rawRepresentation
    }
}
