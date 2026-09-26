import Foundation

// Live Activities, the wire around them (native/spec/push.md §7): the relay
// handles of ActivityKit tokens, and their registration with xbind. The
// ActivityKit side is the app's (App/Push/LiveActivities.swift); the card's
// model is XbinAgent's (LiveActivity.swift).

extension PushRelayAPI {
    /// `POST <relay>/v1/handles` for an ActivityKit push token (an
    /// activity's update token, or the app's push-to-start token) under
    /// `parent`, the device handle of the same workspace: it lives, binds
    /// and goes with that one. The same token under the same parent
    /// answers the same handle.
    public static func newActivityHandle(apnsToken: String, parent: String, topic: String, production: Bool) -> APIRequest {
        let o: [String: JSONValue] = ["apnsToken": .string(apnsToken), "topic": .string(topic),
                                      "env": .string(production ? "production" : "development"),
                                      "pushType": .string("liveactivity"), "parent": .string(parent)]
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
    /// push-started one carries — one of them.
    public static func registerActivity(deviceId: String, session: String? = nil, ref: String? = nil, handle: String) -> APIRequest {
        var o: [String: JSONValue] = ["deviceId": .string(deviceId), "handle": .string(handle)]
        if let session, !session.isEmpty { o["session"] = .string(session) }
        if let ref, !ref.isEmpty { o["ref"] = .string(ref) }
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
    /// Make a relay handle for `token` under the device handle, then
    /// register it with xbind (`startHandle`).
    case create(token: String)
    /// The relay handle is current; xbind does not hold it: register it.
    case register(handle: String)
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
        guard let h = state.handle, state.token == token, state.parent == deviceHandle else { return .create(token: token) }
        return serverHasIt == false ? .register(handle: h) : .none
    }
}
