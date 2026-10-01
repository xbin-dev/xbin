import Foundation

// xbind's own pages the app opens (D181; native/spec/push.md §2): a push or
// a link names one as `xbin/<page>` (`xbin://<ws>/xbin/<page>`), and the
// account's settings may link it. Such a page opens only top-level — never
// in a tile frame — with the person's own sign-in: the app shows it in a
// web view of its own (the chrome store, no tile bridge), signed in with a
// one-shot `POST /api/xbin/web-ticket` and nothing else. Each page is
// feature-detected: an xbind that doesn't name the page's feature has no
// page, and its link opens the workspace — what an app that doesn't know
// the link does.
//
// The partitions page (`/xbin/partitions`, docs/partitions.md §Your
// partitions page): a person's partitions, consents, personal binds,
// credentials waiting for them and a manager's switch decisions. Its
// feature, `partitions-page/1`, is in `GET /api/xbin/partitions`'
// `features` (a 404 there is an xbind without partitions).

/// One of xbind's own pages.
public enum XbindPage: String, Sendable, Hashable, Codable, CaseIterable {
    case partitions

    /// The page's path on the workspace's origin.
    public var path: String { "/xbin/\(rawValue)" }

    /// The link a push carries (`link`, workspace-relative).
    public var link: String { "xbin/\(rawValue)" }

    /// What a person reads for it (the settings entry, the window's title).
    public var title: String {
        switch self {
        case .partitions: return "Your partitions"
        }
    }

    /// The word in the feature list that says this xbind serves the page.
    public var feature: String {
        switch self {
        case .partitions: return "partitions-page/1"
        }
    }

    /// Where that feature list is read.
    public var featuresPath: String {
        switch self {
        case .partitions: return "/api/xbin/partitions"
        }
    }

    /// The probe: `GET <featuresPath>`.
    public var featuresRequest: APIRequest { APIRequest("GET", featuresPath) }

    /// A page named by a link's `xbin/<page>` (the part after `xbin/`);
    /// nil for a page this app doesn't know.
    public init?(linkName: String) {
        self.init(rawValue: linkName)
    }
}

/// Whether a workspace serves one of xbind's pages, from the probe.
public enum XbindPageAvailability: Sendable, Equatable {
    /// The feature is listed: the page is there.
    case served
    /// The xbind answered without the feature (404: no partitions at all).
    case notServed
    /// Unknown: the probe failed (offline, signed out, a 5xx) — ask again.
    case unknown

    /// From the probe's answer (`nil`: the request failed outright).
    public static func from(_ response: APIResponse?, page: XbindPage) -> XbindPageAvailability {
        guard let r = response else { return .unknown }
        if r.status == 404 { return .notServed }
        guard r.isSuccess else { return .unknown }
        guard let j = try? r.json(), let list = j["features"]?.arrayValue else { return .notServed }
        return list.contains { $0.stringValue == page.feature } ? .served : .notServed
    }
}

/// Where the page's web view goes: the ticket's URL, strictly. The page is
/// the person's own and opens signed in or not at all — never the plain
/// page (which would ask for a password in the app's web view): a refused
/// or failed ticket is an error the screen shows, with Try again.
public enum XbindPageTicket {
    public enum Outcome: Sendable, Equatable {
        case open(URL)
        case failed(String)
    }

    /// `response` is the answer to `WebTicket.request(next: page.path)`
    /// (nil: the request failed outright).
    public static func outcome(_ response: APIResponse?, origin: ServerOrigin, signedOrigin: String? = nil,
                               page: XbindPage) -> Outcome {
        guard let r = response else { return .failed("The workspace can't be reached. Try again.") }
        guard let d = WebTicket.destination(r, origin: origin, signedOrigin: signedOrigin, next: page.path) else {
            return .failed("The workspace's address can't open \(page.path).")
        }
        if !d.fellBack { return .open(d.url) }
        if r.isSuccess { return .failed("The workspace's sign-in link for this page was not on its own address.") }
        let e = APIError(r)
        if r.status == 403 || r.status == 401 {
            return .failed("This sign-in can't open \(page.title.lowercased()) (\(e.message)). It needs your own account, signed in on this device.")
        }
        return .failed(e.description)
    }
}

/// The bar over one of xbind's pages (D181): the page's own colour runs up
/// under it, as under a tile page's (TileScreens' WebTileScreen), and the
/// page keeps its own theme whatever the phone's appearance — xbind's dark
/// workspace theme in a light-mode app, say — so the bar takes the page's
/// colour and the scheme its title reads on: light text on a dark page,
/// dark text on a light one, by WCAG contrast.
public enum PageBarScheme {
    /// Whether a page whose background is (`red`, `green`, `blue`) — sRGB,
    /// 0…1 — wants a dark bar: white text contrasts more with it than black.
    public static func isDark(red: Double, green: Double, blue: Double) -> Bool {
        let l = luminance(red: red, green: green, blue: blue)
        return 1.05 / (l + 0.05) >= (l + 0.05) / 0.05
    }

    /// WCAG relative luminance.
    public static func luminance(red: Double, green: Double, blue: Double) -> Double {
        func lin(_ c: Double) -> Double {
            let v = min(max(c, 0), 1)
            return v <= 0.04045 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * lin(red) + 0.7152 * lin(green) + 0.0722 * lin(blue)
    }
}

/// The settings menu's entry to the partitions page (owner ruling I13,
/// plans/partitions/90-decisions.md; the web shell's `pageEntry`): shown
/// while the workspace serves the page, to a signed-in person — not the
/// workspace token, not an admin viewing as someone — who sees a
/// partitioned tile. Nothing on an xbind without the page, and nothing in
/// a workspace without a partitioned tile.
public enum PartitionsEntry {
    public static func shown(availability: XbindPageAvailability, catalog: Catalog, whoami: Whoami?) -> Bool {
        availability == .served && isPerson(whoami) && catalog.hasPartitionedTile
    }

    /// Whether to ask the workspace for the page's feature at all: only a
    /// person who sees a partitioned tile could get the entry, so an xbind
    /// without partitions (whose rows never carry `partition`) is never
    /// asked.
    public static func worthProbing(catalog: Catalog, whoami: Whoami?) -> Bool {
        isPerson(whoami) && catalog.hasPartitionedTile
    }

    /// A signed-in person (`whoami.kind` user, not view-as): the only
    /// viewer with partitions of their own.
    public static func isPerson(_ who: Whoami?) -> Bool {
        guard let who, who.kind == "user", !who.userID.isEmpty else { return false }
        return (who.json["impersonatedBy"]?.stringValue ?? "").isEmpty
    }
}
