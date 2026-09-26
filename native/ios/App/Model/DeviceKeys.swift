import Foundation
import CryptoKit
import LocalAuthentication
import Security
import XbinCore

/// The device-login keys (native/spec/device-login.md §1; plans/native.md
/// §5): one P-256 key per workspace in the Secure Enclave, usable only after
/// Face ID/Touch ID with the biometrics enrolled today (`.biometryCurrentSet`
/// — re-enrolling biometrics invalidates it), never exportable. The
/// Keychain keeps the enclave's wrapped key blob, which is useless off this
/// device. The simulator (no enclave) uses a software key.
struct EnclaveKeyStore: DeviceKeyStore {
    static let service = "dev.xbin.devicekey"

    func createKey(workspace: String) async throws -> Data {
        #if targetEnvironment(simulator)
        let k = SoftwareDeviceKey()
        Keychain.write(k.rawRepresentation, service: Self.service, account: workspace)
        return k.publicKeySPKI
        #else
        guard SecureEnclave.isAvailable else {
            throw SignInError.keyUnavailable("this device has no Secure Enclave")
        }
        let key: SecureEnclave.P256.Signing.PrivateKey
        do {
            key = try SecureEnclave.P256.Signing.PrivateKey(compactRepresentable: false,
                                                            accessControl: try Self.access([.privateKeyUsage, .biometryCurrentSet]))
        } catch {
            // No biometrics enrolled: the device passcode guards the key instead.
            key = try SecureEnclave.P256.Signing.PrivateKey(compactRepresentable: false,
                                                            accessControl: try Self.access([.privateKeyUsage, .userPresence]))
        }
        Keychain.write(key.dataRepresentation, service: Self.service, account: workspace)
        return key.publicKey.derRepresentation
        #endif
    }

    func hasKey(workspace: String) async -> Bool {
        Keychain.read(service: Self.service, account: workspace) != nil
    }

    func sign(_ message: Data, workspace: String, reason: String) async throws -> Data {
        guard let blob = Keychain.read(service: Self.service, account: workspace) else {
            throw SignInError.keyUnavailable("the key for this workspace is gone")
        }
        // Signing blocks while the biometric sheet is up: off the main actor.
        return try await Task.detached(priority: .userInitiated) {
            #if targetEnvironment(simulator)
            do { return try SoftwareDeviceKey(rawRepresentation: blob).sign(message) } catch {
                throw SignInError.keyUnavailable(error.localizedDescription)
            }
            #else
            let ctx = LAContext()
            ctx.localizedReason = reason
            do {
                let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob, authenticationContext: ctx)
                return try key.signature(for: message).derRepresentation
            } catch {
                throw Self.map(error)
            }
            #endif
        }.value
    }

    func deleteKey(workspace: String) async {
        Keychain.delete(service: Self.service, account: workspace)
    }

    static func access(_ flags: SecAccessControlCreateFlags) throws -> SecAccessControl {
        var err: Unmanaged<CFError>?
        guard let ac = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, flags, &err) else {
            throw SignInError.keyUnavailable(err?.takeRetainedValue().localizedDescription ?? "access control")
        }
        return ac
    }

    /// LocalAuthentication cancel codes → `.cancelled`; the rest (biometrics
    /// changed, key invalidated) → `.keyUnavailable`, which offers re-enrolling.
    static func map(_ error: any Error) -> SignInError {
        let ns = error as NSError
        if ns.domain == LAErrorDomain {
            switch LAError.Code(rawValue: ns.code) {
            case .userCancel?, .appCancel?, .systemCancel?, .userFallback?: return .cancelled
            default: break
            }
        }
        if ns.domain == NSOSStatusErrorDomain, ns.code == Int(errSecUserCanceled) { return .cancelled }
        return .keyUnavailable(ns.localizedDescription)
    }
}

/// "Require Face ID when opening the app" (a separate setting, §5).
enum AppLock {
    static func unlock(reason: String) async -> Bool {
        let ctx = LAContext()
        var err: NSError?
        guard ctx.canEvaluatePolicy(.deviceOwnerAuthentication, error: &err) else { return true }
        return (try? await ctx.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason)) ?? false
    }
}
