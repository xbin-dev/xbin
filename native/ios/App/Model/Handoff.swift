import Foundation
import XbinCore

/// Handoff ("open on desktop", plans/native.md §6.3) and the activities a
/// window carries: what a window shows, as an `NSUserActivity` whose
/// `webpageURL` is that tile's page in the browser (a Mac or iPad without
/// the app opens it there, signed in or not) and whose `userInfo` holds the
/// `xbin://` link another device's app opens (the workspace named by its
/// server, since workspace ids are per install). The same activity is what
/// dragging a tile out of the navigator carries, so dropping it makes a new
/// window (Stage Manager). No credential ever goes into one.
@MainActor
enum HandoffActivity {
    static let type = HandoffLink.activityType

    /// The browser page and the app link for `surface` of `w` (nil surface:
    /// the workspace's home).
    static func describe(_ w: WorkspaceModel, _ surface: Surface?) -> (title: String, page: URL?, link: DeepLink) {
        let o = w.origin
        let home: (title: String, page: URL?, link: DeepLink) = (w.title, HandoffLink.shell(origin: o), .workspace(o.authority))
        guard let s = surface else { return home }
        switch s {
        case .tile(let path, let sub, let fragment):
            return (w.tile(path)?.title ?? TileInfo.humanize(path),
                    HandoffLink.tilePage(origin: o, tile: path, subpath: sub, fragment: fragment),
                    HandoffLink.tile(origin: o, tile: path, fragment: fragment))
        case .agent(let cwd, let session):
            if let session { return (s.title, HandoffLink.shell(origin: o), HandoffLink.agent(origin: o, session: session)) }
            if let cwd { return (s.title, HandoffLink.tilePage(origin: o, tile: cwd), HandoffLink.tile(origin: o, tile: cwd)) }
            return home
        case .terminal(let cwd, let session):
            if let session { return (s.title, HandoffLink.shell(origin: o), HandoffLink.terminal(origin: o, session: session)) }
            return (s.title, HandoffLink.tilePage(origin: o, tile: cwd), HandoffLink.tile(origin: o, tile: cwd))
        }
    }

    /// Fills `a` (SwiftUI's `.userActivity` hands one in) for Handoff.
    static func fill(_ a: NSUserActivity, _ w: WorkspaceModel, _ surface: Surface?) {
        let d = describe(w, surface)
        a.title = "\(d.title) — \(w.title)"
        a.webpageURL = d.page
        a.userInfo = HandoffLink.userInfo(d.link)
        a.requiredUserInfoKeys = [HandoffLink.linkKey]
        a.isEligibleForHandoff = AppSettings.handoff
        a.isEligibleForSearch = false
        a.isEligibleForPublicIndexing = false
        a.targetContentIdentifier = d.link.string
    }

    /// A fresh activity for a drag (a tile out of the navigator → a window).
    static func make(_ w: WorkspaceModel, _ surface: Surface?) -> NSUserActivity {
        let a = NSUserActivity(activityType: type)
        fill(a, w, surface)
        a.isEligibleForHandoff = false
        return a
    }

    /// The link a continued or dropped activity carries.
    static func link(_ a: NSUserActivity) -> DeepLink? { HandoffLink.link(fromUserInfo: a.userInfo) }
}
