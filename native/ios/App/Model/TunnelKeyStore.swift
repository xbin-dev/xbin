import Foundation
import XbinCore

// A workspace's tunnel keys in the Keychain (D204; native/spec/tunnel.md §1):
// one item per workspace, service `dev.xbin.tunnel`, account = the
// workspace id, this device only, when unlocked, no access group (the
// extensions never use it). The item is the source of truth for the
// record's `tunnel`: an older app that saved the list without the field
// gets it back here at launch, and items whose workspace is gone (a
// reinstall keeps Keychain items) are swept.

struct KeychainTunnelVault: TunnelVault {
    func load(workspace: String) async -> TunnelSecrets? { Self.read(workspace) }

    func save(_ secrets: TunnelSecrets, workspace: String) async {
        Keychain.write(secrets.encoded(), service: TunnelSecrets.service, account: workspace.lowercased(),
                       accessibility: .whenUnlockedThisDevice)
    }

    func delete(workspace: String) async {
        Keychain.delete(service: TunnelSecrets.service, account: workspace.lowercased())
    }

    static func read(_ workspace: String) -> TunnelSecrets? {
        Keychain.read(service: TunnelSecrets.service, account: workspace.lowercased()).flatMap(TunnelSecrets.init(data:))
    }

    /// At launch: each record's `tunnel` back from its item when an older
    /// app dropped it; with `sweep`, items of workspaces no longer in the
    /// list (nor in `keeping`) deleted.
    static func reconcile(_ records: [WorkspaceRecord], sweep: Bool = true, keeping: Set<String> = []) -> [WorkspaceRecord] {
        let ids = Set(records.map(\.id)).union(keeping)
        if sweep {
            for account in Keychain.accounts(service: TunnelSecrets.service) where !ids.contains(account.lowercased()) {
                Keychain.delete(service: TunnelSecrets.service, account: account)
            }
        }
        return records.map { r in
            guard r.tunnel == nil, let s = read(r.id), !s.endpoint.isEmpty else { return r }
            var r = r
            r.tunnel = s.info(port: nil)
            return r
        }
    }
}

/// The iroh runtime pairing and the device upgrade need — set by
/// App/Model/TunnelEndpoint.swift when the app links IrohLib (D204 phase
/// 3). nil: this build can't open tunnels; a pairing link says so and no
/// device is upgraded.
@MainActor
enum TunnelRuntime {
    static var dialer: (any TunnelDialer)?
    static var signInTransport: TunnelSignInTransport?

    static var isAvailable: Bool { dialer != nil && signInTransport != nil }

    /// What a pairing link meets in a build without the runtime.
    static let unavailable = ConnectProblem.other(
        "This version of the app can't pair through a workspace's tunnel yet. Update xbin, then scan the code again.")
}

extension TunnelRuntime {
    /// Workspaces whose upgrade was tried in this process (once each).
    static var upgradeTried: Set<String> = []
}

extension WorkspaceModel {
    /// D204 §9: an enrolled device of a workspace that turned its app tunnel
    /// on moves to the tunnel without pairing again. Only in a build that
    /// can open tunnels, once per process, and only while the session is one
    /// this process signed in (the route wants one from the last 10 minutes;
    /// no Face ID prompt just for this). A refusal changes nothing.
    func upgradeToTunnelIfDue() async {
        guard TunnelRuntime.isAvailable, record.tunnel == nil, record.deviceId != nil,
              !TunnelRuntime.upgradeTried.contains(id), await auth.signInCount > 0 else { return }
        let methods: LoginMethods?
        if let r = try? await auth.sendAnonymous(APIRequest("GET", AppAuthRoute.loginMethods)), r.status == 200,
           let j = try? r.json() {
            methods = try? LoginMethods(json: j)
        } else {
            methods = nil
        }
        guard TunnelUpgrade.isDue(record: record, methods: methods) else { return }
        TunnelRuntime.upgradeTried.insert(id)
        let auth = self.auth
        let out = await TunnelUpgrader(vault: KeychainTunnelVault()).upgrade(record) { try await auth.send($0) }
        if case .upgraded(let info) = out {
            var r = record
            r.tunnel = info
            update(record: r)
        }
    }
}
