import Foundation
import Testing
@testable import XbinCore

@Suite struct LiveActivityAPITests {
    func body(_ r: APIRequest) throws -> JSONValue { try JSONValue(parsing: try #require(r.body)) }

    @Test func relayHandle() throws {
        let r = PushRelayAPI.newActivityHandle(apnsToken: "c3c3", parent: "dev-handle", topic: "dev.xbin.app", production: false)
        #expect(r.method == "POST" && r.path == "/v1/handles")
        #expect(try body(r) == ["apnsToken": "c3c3", "topic": "dev.xbin.app", "env": "development",
                                "pushType": "liveactivity", "parent": "dev-handle"])
    }

    @Test func xbindRoutes() throws {
        let plain = PushAPI.register(deviceId: "d1", handle: "h", publicKey: "k", kinds: ["agent"], startHandle: nil)
        #expect(try body(plain)["startHandle"] == nil)
        let with = PushAPI.register(deviceId: "d1", handle: "h", publicKey: "k", kinds: ["agent"], startHandle: "sh")
        let w = try body(with)
        #expect(w["startHandle"] == "sh" && w["kinds"] == ["agent"] && with.path == PushAPI.registrations)
        #expect(try body(PushAPI.register(deviceId: "d1", handle: "h", publicKey: "k", kinds: [], startHandle: ""))["startHandle"] == "")

        let bySession = PushAPI.registerActivity(deviceId: "d1", session: "s1", handle: "la")
        #expect(bySession.path == "/api/xbin/devices/push/activities")
        #expect(try body(bySession) == ["deviceId": "d1", "session": "s1", "handle": "la"])
        #expect(try body(PushAPI.registerActivity(deviceId: "d1", ref: "r1", handle: "la")) == ["deviceId": "d1", "ref": "r1", "handle": "la"])
        #expect(try body(PushAPI.registerActivity(deviceId: "d1", session: "s1", handle: "la", since: 1_790_000_000))["since"] == 1_790_000_000)
        let del = PushAPI.unregisterActivity(deviceId: "d/1", session: "s 1")
        #expect(del.method == "DELETE" && del.path == "/api/xbin/devices/push/d%2F1/activities/s%201")

        #expect(PushAPI.activitySession(["activity": ["session": "s1", "created": 5]]) == "s1")
        #expect(PushAPI.activitySession(["activity": [:]]) == nil)
        let listed: JSONValue = ["devices": [["deviceId": "d1", "pushToStart": true], ["deviceId": "d2"]]]
        #expect(PushAPI.hasPushToStart(listed, deviceId: "d1") == true)
        #expect(PushAPI.hasPushToStart(listed, deviceId: "d2") == false)
        #expect(PushAPI.hasPushToStart(listed, deviceId: "d3") == nil)
        let registered: JSONValue = ["device": ["deviceId": "d1", "pushToStart": true], "workspace": "w", "enabled": true]
        #expect(PushAPI.hasPushToStart(registered, deviceId: "d1") == true)
        #expect(PushAPI.hasPushToStart(["device": ["deviceId": "d1"]], deviceId: "d1") == false)
    }

    @Test func startPlan() {
        let cur = LiveStartState(token: "t1", handle: "sh", parent: "dh")
        func plan(_ on: Bool = true, tok: String? = "t1", st: LiveStartState = cur, dh: String? = "dh", has: Bool? = true) -> LiveStartPlan {
            LiveStartPlan.plan(enabled: on, token: tok, state: st, deviceHandle: dh, serverHasIt: has)
        }
        #expect(plan() == .none)
        #expect(plan(dh: nil) == .none)                                      // push isn't set up
        #expect(plan(st: LiveStartState()) == .create(token: "t1"))          // first time
        #expect(plan(tok: "t2") == .create(token: "t2"))                     // a new token
        #expect(plan(dh: "dh2") == .create(token: "t1"))                     // a new device handle orphaned it
        #expect(plan(has: false) == .register(handle: "sh"))                 // xbind lost it (a new registration)
        #expect(plan(has: nil) == .none)
        #expect(plan(false) == .clear)                                       // the user turned it off
        #expect(plan(false, st: LiveStartState(), has: false) == .none)
        #expect(plan(false, st: LiveStartState(), has: true) == .clear)
        #expect(plan(tok: nil) == .clear)
    }
}
