import Foundation

/// The app's own records of a workspace, for a card started by push (which
/// carries only xbind's push id, `ws`): its switcher title and the app's
/// id for it (the card's link). Read from the push keyring the app shares
/// with its extensions (Shared/Keychain.swift, the Keychain access group).
enum WidgetNames {
    struct Names: Sendable, Equatable {
        var title: String?
        var appWorkspace: String
    }

    static var keychainGroup: String? {
        guard let g = Bundle.main.object(forInfoDictionaryKey: "XbinKeychainGroup") as? String,
              !g.isEmpty, !g.hasPrefix("$(") else { return nil }
        return g
    }

    static func lookup(ws: String) -> Names? {
        guard !ws.isEmpty else { return nil }
        let ring = PushKeyring.load(group: keychainGroup)
        guard let e = ring.entries.first(where: { $0.pushWorkspace == ws }) else { return nil }
        return Names(title: e.title, appWorkspace: e.workspace)
    }
}
