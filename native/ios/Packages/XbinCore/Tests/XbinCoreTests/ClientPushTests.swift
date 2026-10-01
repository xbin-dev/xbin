import Foundation
import Testing
@testable import XbinCore

@Suite struct ClientPushTests {
    /// native/spec/push-vectors.json (copied): every envelope decodes, and
    /// every plaintext is a payload the app understands.
    @Test func vectors() throws {
        let v = try Resources.json("push-vectors.json")
        let vectors = try #require(v["vectors"]?.arrayValue)
        #expect(vectors.count >= 3)
        func str(_ v: JSONValue, _ k: String) throws -> String { try #require(v[k]?.stringValue) }
        for vec in vectors {
            let envJSON = try #require(vec["envelope"])
            let env = try #require(PushEnvelope(json: envJSON))
            #expect(env.epk == Base64URL.decode(try str(vec, "ephemeralPublic")))
            #expect(env.nonce == Base64URL.decode(try str(vec, "nonce")))
            #expect(env.ciphertext.count + 16 == env.sealed.count)
            let r = try #require(Base64URL.decode(try str(vec, "recipientPublic")))
            #expect(env.salt(recipientPublicKey: r) == env.epk + r && env.salt(recipientPublicKey: r).count == 64)
            let p = try #require(PushPayload(plaintext: Data(try str(vec, "plaintext").utf8)))
            #expect(!p.ws.isEmpty && !p.kind.isEmpty)
        }
        let first = try #require(PushPayload(plaintext: Data(try str(vectors[0], "plaintext").utf8)))
        #expect(first.typedKind == .agentPermission && first.typedKind.needsYou)
        #expect(first.deepLink(appWorkspace: "w-1") == .agent(workspace: "w-1", session: "s-1a2b"))
        #expect(first.userInfo == ["ws": "Zm9vYmFyYmF6cXV4", "kind": "agent.permission", "link": "agent/s-1a2b"])
    }

    @Test func envelopeAndPayloadAreStrict() {
        let ok: JSONValue = ["v": 1, "epk": .string(Base64URL.encode(Data(count: 32))), "n": .string(Base64URL.encode(Data(count: 12))),
                             "ct": .string(Base64URL.encode(Data(count: 20)))]
        #expect(PushEnvelope(json: ok) != nil)
        var v2 = ok.objectValue!; v2["v"] = 2
        #expect(PushEnvelope(json: .object(v2)) == nil)
        var shortKey = ok.objectValue!; shortKey["epk"] = .string(Base64URL.encode(Data(count: 31)))
        #expect(PushEnvelope(json: .object(shortKey)) == nil)
        var shortCT = ok.objectValue!; shortCT["ct"] = .string(Base64URL.encode(Data(count: 15)))
        #expect(PushEnvelope(json: .object(shortCT)) == nil)
        #expect(PushPayload(plaintext: Data(#"{"v":2,"ws":"a"}"#.utf8)) == nil)
        #expect(PushPayload(plaintext: Data(#"{"v":1}"#.utf8)) == nil)
        let p = PushPayload(plaintext: Data(#"{"v":1,"ws":"a","kind":"tile.alert","title":"T","link":"c/apps/cal/?d=1#x","extra":true}"#.utf8))!
        #expect(p.typedKind == .tile("alert") && p.typedKind.category == "xbin.tile")
        #expect(p.deepLink(appWorkspace: "w") == .tile(workspace: "w", tile: "apps/cal", fragment: "x"))
        #expect(PushPayload(ws: "a", kind: "test", title: "t").deepLink(appWorkspace: "w") == .workspace("w"))
        #expect(PushPayload(ws: "a", kind: "x", title: "t", link: "https://evil").deepLink(appWorkspace: "w") == .workspace("w"))
        #expect(PushPayload.Kind("agent.newthing") == .other("agent.newthing"))
    }

    /// push.md §2's xbind-raised kinds (D181): decoded, grouped and given
    /// the right actions, and their `xbin/partitions` link opens the page.
    @Test(arguments: ["tile.partition-switch", "tile.partition-deleted", "tile.partition-consent", "tile.partition-reset"])
    func partitionNotices(_ kind: String) throws {
        let plain = #"{"v":1,"ws":"pw","kind":"\#(kind)","title":"apps/x is paused","body":"b","link":"xbin/partitions","collapseId":"xbin:partition-switch:apps/x"}"#
        let p = try #require(PushPayload(plaintext: Data(plain.utf8)))
        #expect(p.typedKind == .tile(String(kind.dropFirst(5))))
        #expect(p.collapseID == "xbin:partition-switch:apps/x")
        #expect(p.page == .partitions)
        #expect(p.deepLink(appWorkspace: "w-1") == .page(workspace: "w-1", page: .partitions))
        // Open, never "Mute this tile"; a group of their own per workspace.
        #expect(p.category == "xbin.page.partitions" && p.category == PushPayload.Kind.pageCategory(.partitions))
        #expect(p.threadID == "pw/xbin/partitions")
        #expect(p.userInfo == ["ws": "pw", "kind": kind, "link": "xbin/partitions"])
    }

    @Test func accountCredential() throws {
        let plain = #"{"v":1,"ws":"pw","kind":"account.credential","title":"A sign-in link for your account","body":"Made by admin at 2026-10-01 10:00 UTC. It works only once you allow it…","link":"xbin/partitions","collapseId":"xbin:credential:h1"}"#
        let p = try #require(PushPayload(plaintext: Data(plain.utf8)))
        #expect(p.typedKind == .account("credential"))
        #expect(p.typedKind.category == "xbin.account" && p.category == "xbin.account")
        #expect(p.deepLink(appWorkspace: "w") == .page(workspace: "w", page: .partitions))
        #expect(p.threadID == "pw/xbin/partitions")
        #expect(PushPayload.Kind("account") == .account(nil))
        // Without a page link (a future one): still the account's actions,
        // and the workspace opens.
        let bare = PushPayload(ws: "pw", kind: "account.other", title: "t")
        #expect(bare.category == "xbin.account" && bare.threadID == "pw" && bare.deepLink(appWorkspace: "w") == .workspace("w"))
    }

    /// A tile can name a `partition-…` kind (`POST /api/xbin/notify`), but
    /// its link is always its own: it shows as the tile's, Mute included.
    /// xbind's own `tile.partition-restored` links the tile, likewise.
    @Test func tileNamedPartitionKindsStayTiles() {
        let spoof = PushPayload(ws: "pw", kind: "tile.partition-switch", title: "t", link: "c/apps/evil/")
        #expect(spoof.page == nil && spoof.category == "xbin.tile" && spoof.threadID == "pw")
        #expect(spoof.deepLink(appWorkspace: "w") == .tile(workspace: "w", tile: "apps/evil", fragment: nil))
        let restored = PushPayload(ws: "pw", kind: "tile.partition-restored", title: "t", link: "c/apps/notes/")
        #expect(restored.category == "xbin.tile")
        // Unknown kinds still show like a tile's.
        #expect(PushPayload(ws: "pw", kind: "brand.new", title: "t").category == "xbin.tile")
        // An xbind page this app doesn't know: the workspace, a tile's actions.
        let unknownPage = PushPayload(ws: "pw", kind: "tile.partition-switch", title: "t", link: "xbin/consents")
        #expect(unknownPage.page == nil && unknownPage.deepLink(appWorkspace: "w") == .workspace("w"))
        #expect(unknownPage.category == "xbin.tile" && unknownPage.threadID == "pw")
    }

    /// The app registers for every kind push.md §2 lists (xbind's
    /// kindAllowed): `account` was missing before D181.
    @Test func appKindsReceiveEveryKind() {
        for k in ["agent.permission", "agent.question", "agent.turn", "tile", "tile.alert", "tile.partition-switch",
                  "tile.partition-deleted", "tile.partition-consent", "tile.partition-reset", "tile.partition-restored",
                  "account.credential", "test"] {
            #expect(PushAPI.receives(PushAPI.appKinds, k), "\(k)")
        }
        #expect(!PushAPI.receives(["agent", "tile", "test"], "account.credential"))
        #expect(!PushAPI.receives(["tile"], "tiles.x") && !PushAPI.receives(["agent"], "agentx"))
        #expect(PushAPI.receives([], "anything") && PushAPI.receives(["agent"], "test"))
    }

    @Test func openerPicksTheRightWorkspace() {
        let env = PushEnvelope(epk: Data(count: 32), nonce: Data(count: 12), sealed: Data(count: 16))
        let good = Data(#"{"v":1,"ws":"push-B","kind":"test","title":"hi"}"#.utf8)
        let opened = PushOpener.open(env, candidates: [("A", "push-A"), ("B", "push-B")]) { $0 == "B" ? good : nil }
        #expect(opened?.appWorkspace == "B" && opened?.payload.title == "hi")
        // A key that opens it but whose workspace isn't the payload's: not ours.
        #expect(PushOpener.open(env, candidates: [("A", "push-A")]) { _ in good } == nil)
        #expect(PushOpener.isRelayProbe(["aps": ["content-available": 1]]))
        #expect(!PushOpener.isRelayProbe(["aps": ["alert": "New activity", "mutable-content": 1], "xbin": [:]]))
        #expect(APNsToken.hex(Data([0x00, 0xab, 0x10])) == "00ab10")
    }

    /// push.md §1.4, as a table.
    @Test func maintenancePlan() throws {
        let server = PushStatus(json: try Resources.json("server/push-list.json"))
        #expect(server.workspace == "coyxxRBwDvBo4-0d" && !server.enabled && server.devices.map(\.deviceId) == ["dev-test"])
        let reg = PushStatus(json: try Resources.json("server/push-register.json"))
        #expect(reg.devices.first?.kinds == ["agent", "tile"])

        let good = PushState(handle: "h1", relay: "https://relay", apnsToken: "aa", pushWorkspace: "coyxxRBwDvBo4-0d",
                             publicKey: "pk", kinds: ["agent", "tile"])
        func plan(_ s: PushState, token: String = "aa", relay: String = "https://relay", key: String = "pk",
                  kinds: [String] = ["agent", "tile"], device: String = "dev-test", server st: PushStatus? = server) -> PushMaintenance {
            PushMaintenance.plan(state: s, apnsToken: token, relay: relay, publicKey: key, kinds: kinds, deviceId: device, server: st)
        }
        #expect(plan(good) == .none)
        #expect(plan(good, server: nil) == .none)                                   // offline: nothing to learn
        #expect(plan(PushState()) == .newHandle)
        #expect(plan(good, relay: "https://other") == .newHandle)
        #expect(plan(good, token: "bb") == .updateToken(handle: "h1"))
        #expect(plan(good, device: "dev-other") == .register)                       // missing on xbind
        #expect(plan(good, key: "pk2") == .register)
        #expect(plan(good, kinds: ["agent"]) == .register)
        let stale = PushStatus(json: ["workspace": "w", "enabled": true, "devices": [
            ["deviceId": "dev-test", "kinds": ["agent", "tile"], "needsNewHandle": true, "relayError": "handle_bound"]]])
        #expect(stale.devices[0].relayError == "handle_bound")
        #expect(plan(good, server: stale) == .replaceHandle(old: "h1"))
    }

    @Test func requests() throws {
        let r = PushAPI.register(deviceId: "dev-1", handle: "h", publicKey: "pk", kinds: [])
        #expect(r.method == "POST" && r.path == "/api/xbin/devices/push")
        #expect(body(r) == ["deviceId": "dev-1", "handle": "h", "publicKey": "pk"])
        #expect(body(PushAPI.register(deviceId: "d", handle: "h", publicKey: "p", kinds: ["agent"]))["kinds"] == ["agent"])
        #expect(PushAPI.unregister(deviceId: "dev 1").path == "/api/xbin/devices/push/dev%201")
        #expect(body(PushAPI.setMuted(["apps/a"])) == ["mutedTiles": ["apps/a"]])
        #expect(PushAPI.mutedTiles(["mutedTiles": ["x", 1]]) == ["x"])
        let h = PushRelayAPI.newHandle(apnsToken: "aa", topic: "dev.xbin.app", production: false)
        #expect(h.path == "/v1/handles" && body(h) == ["apnsToken": "aa", "topic": "dev.xbin.app", "env": "development"])
        #expect(PushRelayAPI.updateHandle("h/1", apnsToken: "b", topic: "t", production: true).path == "/v1/handles/h%2F1")
        #expect(PushRelayAPI.deleteHandle("h").method == "DELETE")
        #expect(PushRelayAPI.handle(from: ["handle": "abc"]) == "abc" && PushRelayAPI.handle(from: [:]) == nil)
        #expect(PushRelayAPI.errorCode(json(404, ["error": "x", "code": "handle_unknown"])) == "handle_unknown")
        let st = PushState(handle: "h", kinds: ["a"])
        #expect(try JSONDecoder().decode(PushState.self, from: JSONEncoder().encode(st)) == st)
    }
}
