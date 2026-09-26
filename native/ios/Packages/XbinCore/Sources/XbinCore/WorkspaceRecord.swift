import Foundation

/// One workspace the app is signed into: `(server, user, device key)`
/// (plans/native.md §4). One server may appear twice (two accounts).
///
/// The record holds no secret. The device's private key lives in the Secure
/// Enclave and the session/relay handle in the Keychain, both found by the
/// record's ``id``.
///
/// Persistence format (``WorkspaceList/encoded()``): JSON with sorted keys;
/// dates are integer milliseconds since the Unix epoch; the server is its
/// origin string. Unknown fields are ignored when reading, absent optional
/// ones are nil — so records written by an older or newer app still load.
public struct WorkspaceRecord: Sendable, Hashable, Identifiable {
    /// The app's own id for this workspace: a lowercase UUID, stable for the
    /// record's lifetime. Usable as `<ws>` in `xbin://` links.
    public var id: String
    public var server: ServerOrigin
    public var user: WorkspaceUser
    /// The id xbind gave this device at enrollment; nil until enrolled.
    public var deviceId: String?
    /// The origin the enrollment returned (``DeviceEnrollment/origin``) —
    /// what every device login signs. It can differ from ``server`` (an
    /// operator's `--external-url` vs the LAN address the app talks to);
    /// nil = ``server``'s origin.
    public var deviceOrigin: String?
    /// The name this device enrolled under (shown in the Devices list).
    public var deviceName: String?
    /// `GET /api/xbin/branding`, cached for the switcher.
    public var branding: BrandingCache?
    public var addedAt: Date
    public var lastUsedAt: Date?

    public init(id: String = UUID().uuidString.lowercased(), server: ServerOrigin, user: WorkspaceUser,
                deviceId: String? = nil, deviceOrigin: String? = nil, deviceName: String? = nil,
                branding: BrandingCache? = nil, addedAt: Date = Date(), lastUsedAt: Date? = nil) {
        self.id = id.lowercased()
        self.server = server
        self.user = user
        self.deviceId = deviceId
        self.deviceOrigin = deviceOrigin
        self.deviceName = deviceName
        self.branding = branding
        self.addedAt = addedAt
        self.lastUsedAt = lastUsedAt
    }

    /// Whether `ref` — the `<ws>` of an `xbin://` link — names this
    /// workspace: its ``id``, or its server's `host[:port]`
    /// (case-insensitive).
    public func matches(linkWorkspace ref: String) -> Bool {
        let r = ref.lowercased()
        return r == id || r == server.authority
    }

    /// Records an enrollment: the device id and the origin to sign.
    public mutating func enrolled(_ e: DeviceEnrollment, name: String? = nil) {
        deviceId = e.deviceId
        deviceOrigin = e.origin
        deviceName = name ?? (e.name.isEmpty ? deviceName : e.name)
    }

    /// The bytes to sign for a login with `nonce` (``DeviceLogin``); throws
    /// when the device isn't enrolled.
    public func deviceLoginMessage(nonce: String) throws -> Data {
        guard let deviceId else { throw DeviceLogin.Invalid(reason: "this workspace has no enrolled device") }
        return try DeviceLogin.message(origin: deviceOrigin ?? server.origin, deviceId: deviceId, nonce: nonce)
    }

    /// The title for the switcher: the branding title, else the host.
    public var displayTitle: String {
        if let t = branding?.title, !t.isEmpty { return t }
        return server.host
    }
}

/// The signed-in user of a workspace.
public struct WorkspaceUser: Sendable, Hashable, Codable {
    /// xbind's user id (`alice`).
    public var id: String
    /// A display name, when xbind has one.
    public var name: String?

    public init(id: String, name: String? = nil) {
        self.id = id
        self.name = name
    }
}

/// A workspace's branding (D76), as last fetched.
public struct BrandingCache: Sendable, Hashable {
    /// The workspace title; empty = xbin's own.
    public var title: String
    /// The icon as a `data:` URI; nil = xbin's mark.
    public var icon: String?
    public var fetchedAt: Date?

    public init(title: String, icon: String? = nil, fetchedAt: Date? = nil) {
        self.title = title
        self.icon = icon
        self.fetchedAt = fetchedAt
    }

    /// From the `GET /api/xbin/branding` body `{title, icon, hasIcon}`.
    public init(response: JSONValue, fetchedAt: Date? = nil) {
        title = response["title"]?.stringValue ?? ""
        let icon = response["icon"]?.stringValue ?? ""
        self.icon = icon.isEmpty ? nil : icon
        self.fetchedAt = fetchedAt
    }
}

/// Every workspace on this device, in switcher order — the file the app
/// persists.
public struct WorkspaceList: Sendable, Hashable {
    /// The format version.
    public static let currentVersion = 1

    public var workspaces: [WorkspaceRecord]
    /// The foreground workspace's id.
    public var selected: String?
    /// Records this version couldn't read (a newer app's, a damaged one):
    /// kept verbatim and written back, so one bad record never costs the
    /// user their other workspaces — nor itself.
    public var unreadable: [JSONValue] = []

    public init(workspaces: [WorkspaceRecord] = [], selected: String? = nil) {
        self.workspaces = workspaces
        self.selected = selected
    }

    public subscript(id id: String) -> WorkspaceRecord? { workspaces.first { $0.id == id } }

    /// The workspace an `xbin://` link names: an id match first, then a
    /// server `host[:port]` match (the most recently used one when a server
    /// has several accounts).
    public func resolve(linkWorkspace ref: String) -> WorkspaceRecord? {
        let r = ref.lowercased()
        if let byID = workspaces.first(where: { $0.id == r }) { return byID }
        return workspaces.filter { $0.server.authority == r }
            .max { ($0.lastUsedAt ?? $0.addedAt) < ($1.lastUsedAt ?? $1.addedAt) }
    }

    /// The persisted bytes.
    public func encoded() throws -> Data {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .prettyPrinted, .withoutEscapingSlashes]
        return try enc.encode(self)
    }

    /// Reads persisted bytes. A newer major version of the list is refused
    /// rather than misread; a record that doesn't read lands in
    /// ``unreadable``.
    public static func decode(_ data: Data) throws -> WorkspaceList {
        try JSONDecoder().decode(WorkspaceList.self, from: data)
    }
}

// MARK: - Codable (explicit, so the format doesn't depend on encoder settings)

extension WorkspaceRecord: Codable {
    enum CodingKeys: String, CodingKey {
        case id, server, user, deviceId, deviceOrigin, deviceName, branding, addedAt, lastUsedAt
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id).lowercased()
        server = try c.decode(ServerOrigin.self, forKey: .server)
        user = try c.decode(WorkspaceUser.self, forKey: .user)
        deviceId = try c.decodeIfPresent(String.self, forKey: .deviceId)
        deviceOrigin = try c.decodeIfPresent(String.self, forKey: .deviceOrigin)
        deviceName = try c.decodeIfPresent(String.self, forKey: .deviceName)
        branding = try c.decodeIfPresent(BrandingCache.self, forKey: .branding)
        addedAt = try c.decodeIfPresent(Int64.self, forKey: .addedAt).map(Date.init(unixMillis:)) ?? Date(timeIntervalSince1970: 0)
        lastUsedAt = try c.decodeIfPresent(Int64.self, forKey: .lastUsedAt).map(Date.init(unixMillis:))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(server, forKey: .server)
        try c.encode(user, forKey: .user)
        try c.encodeIfPresent(deviceId, forKey: .deviceId)
        try c.encodeIfPresent(deviceOrigin, forKey: .deviceOrigin)
        try c.encodeIfPresent(deviceName, forKey: .deviceName)
        try c.encodeIfPresent(branding, forKey: .branding)
        try c.encode(addedAt.unixMillis, forKey: .addedAt)
        try c.encodeIfPresent(lastUsedAt?.unixMillis, forKey: .lastUsedAt)
    }
}

extension BrandingCache: Codable {
    enum CodingKeys: String, CodingKey { case title, icon, fetchedAt }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        icon = try c.decodeIfPresent(String.self, forKey: .icon)
        fetchedAt = try c.decodeIfPresent(Int64.self, forKey: .fetchedAt).map(Date.init(unixMillis:))
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(title, forKey: .title)
        try c.encodeIfPresent(icon, forKey: .icon)
        try c.encodeIfPresent(fetchedAt?.unixMillis, forKey: .fetchedAt)
    }
}

extension WorkspaceList: Codable {
    enum CodingKeys: String, CodingKey { case v, workspaces, selected }

    /// The list's version is newer than this app understands.
    public struct NewerVersion: Error, Equatable, Sendable {
        public var version: Int
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let v = try c.decodeIfPresent(Int.self, forKey: .v) ?? 1
        guard v <= Self.currentVersion else { throw NewerVersion(version: v) }
        workspaces = []
        for raw in try c.decodeIfPresent([JSONValue].self, forKey: .workspaces) ?? [] {
            if let r = try? JSONDecoder().decode(WorkspaceRecord.self, from: JSONEncoder().encode(raw)) {
                workspaces.append(r)
            } else {
                unreadable.append(raw)
            }
        }
        selected = try c.decodeIfPresent(String.self, forKey: .selected)
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(Self.currentVersion, forKey: .v)
        var all = c.nestedUnkeyedContainer(forKey: .workspaces)
        for w in workspaces { try all.encode(w) }
        for raw in unreadable { try all.encode(raw) }
        try c.encodeIfPresent(selected, forKey: .selected)
    }
}

extension Date {
    /// Integer milliseconds since the Unix epoch.
    var unixMillis: Int64 { Int64((timeIntervalSince1970 * 1000).rounded()) }

    init(unixMillis ms: Int64) {
        self.init(timeIntervalSince1970: Double(ms) / 1000)
    }
}
