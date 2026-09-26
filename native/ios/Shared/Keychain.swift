import Foundation
import Security

/// Generic-password Keychain items (the app and its Notification Service
/// Extension). Values are opaque Data; callers encode JSON.
enum Keychain {
    enum Accessibility {
        /// Sessions: only while unlocked, never leaving this device.
        case whenUnlockedThisDevice
        /// Push keys: the extension decrypts while the phone is locked
        /// (native/spec/push.md §1.1).
        case afterFirstUnlockThisDevice

        var value: CFString {
            switch self {
            case .whenUnlockedThisDevice: return kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            case .afterFirstUnlockThisDevice: return kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            }
        }
    }

    private static func base(service: String, account: String, group: String?) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        if let group { q[kSecAttrAccessGroup as String] = group }
        return q
    }

    static func read(service: String, account: String, group: String? = nil) -> Data? {
        var q = base(service: service, account: account, group: group)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? Data
    }

    @discardableResult
    static func write(_ data: Data, service: String, account: String, group: String? = nil,
                      accessibility: Accessibility = .whenUnlockedThisDevice) -> Bool {
        let q = base(service: service, account: account, group: group)
        let attrs: [String: Any] = [kSecValueData as String: data,
                                    kSecAttrAccessible as String: accessibility.value]
        var status = SecItemUpdate(q as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound {
            var add = q
            for (k, v) in attrs { add[k] = v }
            status = SecItemAdd(add as CFDictionary, nil)
        }
        return status == errSecSuccess
    }

    static func delete(service: String, account: String, group: String? = nil) {
        SecItemDelete(base(service: service, account: account, group: group) as CFDictionary)
    }
}

/// What the Notification Service Extension needs to open a push, shared
/// through the Keychain access group: per app workspace, its push private
/// key and xbind's push id for it (`ws` in payloads).
struct PushKeyring: Codable, Equatable {
    struct Entry: Codable, Equatable {
        /// The app's workspace id (lowercase UUID).
        var workspace: String
        /// xbind's push id (`workspace` from the registration).
        var pushWorkspace: String?
        /// The X25519 private key, raw 32 bytes.
        var privateKey: Data
        /// The switcher title, for the notification subtitle.
        var title: String?
    }

    var entries: [Entry] = []

    static let service = "dev.xbin.push"
    static let account = "keyring"

    static func load(group: String?) -> PushKeyring {
        guard let d = Keychain.read(service: service, account: account, group: group),
              let k = try? JSONDecoder().decode(PushKeyring.self, from: d) else { return PushKeyring() }
        return k
    }

    func save(group: String?) {
        guard let d = try? JSONEncoder().encode(self) else { return }
        Keychain.write(d, service: Self.service, account: Self.account, group: group,
                       accessibility: .afterFirstUnlockThisDevice)
    }

    subscript(workspace: String) -> Entry? { entries.first { $0.workspace == workspace } }

    mutating func upsert(_ e: Entry) {
        if let i = entries.firstIndex(where: { $0.workspace == e.workspace }) { entries[i] = e } else { entries.append(e) }
    }

    mutating func remove(_ workspace: String) { entries.removeAll { $0.workspace == workspace } }
}
