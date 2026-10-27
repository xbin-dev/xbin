import Foundation
import XbinCore

/// A workspace's tunnel secrets in the Keychain (D204; plans/app-tunnel.md
/// Appendix B §5): one item per workspace — service `dev.xbin.tunnel`,
/// account = the workspace id, this device only, while unlocked, no access
/// group (the extensions never need it) — holding ``TunnelSecrets`` as JSON:
/// the device's iroh secret and Noise static key, the server's Noise key,
/// the ticket (it may name the server's addresses), the relay and its token.
/// One item, so a write is atomic and a record that lost its `tunnel` field
/// (an older app saved the list) can be rebuilt from it: the Keychain is
/// the source of truth.
enum TunnelKeyStore {
    static var service: String { TunnelSecrets.service }

    static func read(_ workspace: String) -> TunnelSecrets? {
        Keychain.read(service: service, account: workspace).flatMap { TunnelSecrets(data: $0) }
    }

    @discardableResult
    static func write(_ s: TunnelSecrets, workspace: String) -> Bool {
        Keychain.write(s.encoded(), service: service, account: workspace)
    }

    static func delete(_ workspace: String) {
        Keychain.delete(service: service, account: workspace)
    }

    /// At launch: drops the items of workspaces the list no longer has (a
    /// reinstall — Keychain items outlive the app — or a crash between
    /// pairing and saving). Returns the accounts it removed.
    @discardableResult
    static func collectGarbage(keeping workspaces: Set<String>) -> [String] {
        let stale = Keychain.accounts(service: service).filter { !workspaces.contains($0) }
        for a in stale { delete(a) }
        return stale
    }
}
