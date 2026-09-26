import Foundation

// Live Activities, the wire around them (native/spec/push.md §7): the relay
// handles of ActivityKit tokens, and their registration with xbind. The
// ActivityKit side is the app's (App/Push/LiveActivities.swift); the card's
// model is XbinAgent's (LiveActivity.swift).

extension PushRelayAPI {
    /// `POST <relay>/v1/handles` for an ActivityKit push token (an
    /// activity's update token, or — `start` — the app's push-to-start
    /// token) under `parent`, the device handle of the same workspace: it
    /// lives, binds and goes with that one. The same token under the same
    /// parent answers the same handle. A parent holds one push-to-start
    /// handle and at most 16 activity handles; an activity's handle is
    /// retired by its end.
    public static func newActivityHandle(apnsToken: String, parent: String, topic: String, production: Bool,
                                         start: Bool = false) -> APIRequest {
        var o: [String: JSONValue] = ["apnsToken": .string(apnsToken), "topic": .string(topic),
                                      "env": .string(production ? "production" : "development"),
                                      "pushType": .string("liveactivity"), "parent": .string(parent)]
        if start { o["start"] = .bool(true) }
        return .json("POST", "/v1/handles", .object(o))
    }
}

extension PushAPI {
    public static let activities = "/api/xbin/devices/push/activities"

    /// The registration with the push-to-start handle: `startHandle` nil
    /// leaves it as it is, "" removes it.
    public static func register(deviceId: String, handle: String, publicKey: String, kinds: [String], startHandle: String?) -> APIRequest {
        var r = register(deviceId: deviceId, handle: handle, publicKey: publicKey, kinds: kinds)
        if let startHandle, var o = r.body.flatMap({ try? JSONValue(parsing: $0) })?.objectValue {
            o["startHandle"] = .string(startHandle)
            r = .json("POST", registrations, .object(o))
        }
        return r
    }

    /// `POST /api/xbin/devices/push/activities`: an activity the device
    /// shows, by its session (one the app started) or by the `ref` a
    /// push-started one carries — one of them. `since`: when the card says
    /// the turn started (unix seconds; xbind takes it for a turn it did not
    /// see begin).
    public static func registerActivity(deviceId: String, session: String? = nil, ref: String? = nil, handle: String,
                                        since: Int64 = 0) -> APIRequest {
        var o: [String: JSONValue] = ["deviceId": .string(deviceId), "handle": .string(handle)]
        if let session, !session.isEmpty { o["session"] = .string(session) }
        if let ref, !ref.isEmpty { o["ref"] = .string(ref) }
        if since > 0 { o["since"] = .int(since) }
        return .json("POST", activities, .object(o))
    }

    /// `DELETE …/devices/push/<deviceId>/activities/<session>`: the device
    /// stopped showing it.
    public static func unregisterActivity(deviceId: String, session: String) -> APIRequest {
        APIRequest("DELETE", "\(registrations)/\(URLComponent.encode(deviceId))/activities/\(URLComponent.encode(session))")
    }

    /// The session an activity registration names (`{activity: {session}}`).
    public static func activitySession(_ json: JSONValue) -> String? {
        guard let s = json["activity"]?["session"]?.stringValue, !s.isEmpty else { return nil }
        return s
    }

    /// Whether xbind holds a push-to-start handle for this device, from
    /// `GET /api/xbin/devices/push` (`devices[].pushToStart`) or a
    /// registration's answer (`device.pushToStart`); nil when the device is
    /// not in it.
    public static func hasPushToStart(_ json: JSONValue, deviceId: String) -> Bool? {
        var list = json["devices"]?.arrayValue ?? []
        if let one = json["device"] { list.append(one) }
        for d in list where d["deviceId"]?.stringValue == deviceId {
            return d["pushToStart"]?.boolValue ?? false
        }
        return nil
    }
}

/// What xbind's answer to `POST /api/xbin/devices/push/activities` means
/// for the card (native/spec/push.md §7.3).
public enum ActivityRegistration: Sendable, Equatable {
    /// xbind follows the session's turn for the card.
    case following(session: String)
    /// The turn is over: xbind sends the card its end (`ended`) and keeps
    /// no registration. The card ends.
    case ended(session: String)
    /// Nothing on xbind to follow (404: the session is gone, a ref it never
    /// gave or forgot — another workspace's start, an xbind restart —, a
    /// device it holds no registration for; 403: a registration of another
    /// sign-in). A push-started card ends; the relay handle is useless.
    case unknown
    /// Anything else (offline, throttled, a server error): try again later.
    case retry

    public static func of(status: Int, json: JSONValue?) -> ActivityRegistration {
        switch status {
        case 200..<300:
            guard let json, let session = PushAPI.activitySession(json) else { return .retry }
            if json["activity"]?["ended"]?.boolValue == true { return .ended(session: session) }
            return .following(session: session)
        case 403, 404:
            return .unknown
        default:
            return .retry
        }
    }

    public static func of(_ r: APIResponse) -> ActivityRegistration {
        of(status: r.status, json: try? r.json())
    }
}

/// The app's push-to-start registration for one workspace: the ActivityKit
/// token, the relay handle made for it, and the device handle it hangs off.
public struct LiveStartState: Sendable, Equatable, Codable {
    public var token: String?
    public var handle: String?
    public var parent: String?

    public init(token: String? = nil, handle: String? = nil, parent: String? = nil) {
        self.token = token
        self.handle = handle
        self.parent = parent
    }
}

/// What to do about push-to-start for a workspace, on launch, foreground and
/// a new token (the counterpart of ``PushMaintenance``).
public enum LiveStartPlan: Sendable, Equatable {
    case none
    /// Get the relay handle for `token` under the device handle, then
    /// register it with xbind (`startHandle`). Also when xbind dropped the
    /// one it had: the relay may have dropped it first (a handle nothing
    /// pushed to expires), and asking again answers the same handle while
    /// it lives.
    case create(token: String)
    /// Push-to-start is off (the setting, or no token): tell xbind
    /// (`startHandle: ""`) and forget the handle.
    case clear

    /// - Parameters:
    ///   - enabled: the user allows Live Activities started by push.
    ///   - token: the current push-to-start token (hex), when ActivityKit gave one.
    ///   - deviceHandle: the workspace's device handle (nil: push isn't set up).
    ///   - serverHasIt: xbind's `pushToStart` for this device (nil: unknown).
    public static func plan(enabled: Bool, token: String?, state: LiveStartState, deviceHandle: String?, serverHasIt: Bool?) -> LiveStartPlan {
        guard let deviceHandle else { return .none }
        guard enabled, let token, !token.isEmpty else {
            return (state.handle != nil || serverHasIt == true) ? .clear : .none
        }
        guard state.handle != nil, state.token == token, state.parent == deviceHandle, serverHasIt != false else {
            return .create(token: token)
        }
        return .none
    }
}
