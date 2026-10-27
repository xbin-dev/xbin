import Foundation
import Testing
@testable import XbinCore

// App tunnels (D204, native/spec/tunnel.md): the link, the pairing messages,
// the confirmation code's digits, the client config, the record's field,
// status, reconnecting. The cryptography is the app's (Shared/
// NoiseTunnel.swift) and is checked by native/tools/app-check.

/// RFC 4648 base32, lowercase, no padding (what iroh's tickets use).
func base32(_ d: Data) -> String {
    let alphabet = Array("abcdefghijklmnopqrstuvwxyz234567")
    var out = "", buffer: UInt32 = 0, bits = 0
    for b in d {
        buffer = buffer << 8 | UInt32(b)
        bits += 8
        while bits >= 5 {
            bits -= 5
            out.append(alphabet[Int((buffer >> UInt32(bits)) & 31)])
        }
    }
    if bits > 0 { out.append(alphabet[Int((buffer << UInt32(5 - bits)) & 31)]) }
    return out
}

enum TunnelFixture {
    static let server = try! ServerOrigin(string: "https://xbin.example.com")
    static let code = "K3DQAAAABBBBCCCCDDDDEEEEFF"
    /// A relay-only ticket's shape: a variant byte, the EndpointId, a relay URL.
    static let ticket = "endpoint" + base32(Data([0]) + Data(repeating: 0xAB, count: 32) + Data("https://relay.example.com/".utf8))
    static let serverKey = Data((0..<32).map { UInt8($0) })
    static let psk = Data((0..<32).map { UInt8(255 - $0) })
    static var k: String { Base64URL.encode(serverKey) }
    static var p: String { Base64URL.encode(psk) }
    static var base: String { "xbin://enroll?u=https%3A%2F%2Fxbin.example.com&c=\(code)" }
    static var link: String { base + "&tv=1&t=\(ticket)&k=\(k)&p=\(p)" }
}

@Suite struct TunnelLinkTests {
    typealias F = TunnelFixture

    @Test func parsesAWholeLink() throws {
        let l = try DeepLink(string: F.link)
        guard case .tunnelEnroll(let t) = l else { Issue.record("not a tunnel link: \(l)"); return }
        #expect(t.server == F.server)
        #expect(t.code == F.code)
        #expect(t.ticket == F.ticket)
        #expect(t.serverKey == F.serverKey)
        #expect(t.psk == F.psk)
        #expect(t.relayToken == nil)
        #expect(t.required == false)
        #expect(l.workspace == nil)
        // The token is URL-encoded on the wire; rq pins the tunnel.
        let full = try DeepLink(string: F.link + "&r=tok%2Bwith%2Fslash&rq=1")
        guard case .tunnelEnroll(let t2) = full else { Issue.record("not a tunnel link"); return }
        #expect(t2.relayToken == "tok+with/slash")
        #expect(t2.required)
    }

    @Test func roundTrips() throws {
        let t = TunnelLink(server: F.server, code: F.code, ticket: F.ticket, serverKey: F.serverKey, psk: F.psk,
                           relayToken: "a+b/c=d", required: true)
        #expect(try DeepLink(string: t.string) == .tunnelEnroll(t))
        #expect(DeepLink.tunnelEnroll(t).string == t.string)
        #expect(t.string.hasPrefix(F.base + "&tv=1&t=endpoint"))
        #expect(t.string.hasSuffix("&r=a%2Bb%2Fc%3Dd&rq=1"))
        let plain = TunnelLink(server: F.server, code: F.code, ticket: F.ticket, serverKey: F.serverKey, psk: F.psk)
        #expect(try DeepLink(string: plain.string) == .tunnelEnroll(plain))
        #expect(!plain.string.contains("&r=") && !plain.string.contains("rq"))
    }

    /// An old-style link is still a classic enrollment; unknown extras
    /// don't make it a tunnel link.
    @Test func classicLinksStayClassic() throws {
        #expect(try DeepLink(string: F.base) == .enroll(server: F.server, code: F.code))
        #expect(try DeepLink(string: F.base + "&x=1") == .enroll(server: F.server, code: F.code))
    }

    /// Any tunnel parameter makes it all or nothing: never a quiet fallback
    /// to classic enrollment.
    @Test func refusesDamagedLinks() {
        let longKey = F.k + "A", padded = F.k + "="
        let shortKey = Base64URL.encode(Data(count: 31))
        let bad: [String: String] = [
            "no tv": F.base + "&t=\(F.ticket)&k=\(F.k)&p=\(F.p)",
            "tv only": F.base + "&tv=1",
            "no ticket": F.base + "&tv=1&k=\(F.k)&p=\(F.p)",
            "no key": F.base + "&tv=1&t=\(F.ticket)&p=\(F.p)",
            "no psk": F.base + "&tv=1&t=\(F.ticket)&k=\(F.k)",
            "bad ticket prefix": F.base + "&tv=1&t=node\(F.ticket.dropFirst(8))&k=\(F.k)&p=\(F.p)",
            "bad ticket chars": F.base + "&tv=1&t=endpoint0189&k=\(F.k)&p=\(F.p)",
            "short ticket": F.base + "&tv=1&t=endpoint\(base32(Data(count: 8)))&k=\(F.k)&p=\(F.p)",
            "long key": F.base + "&tv=1&t=\(F.ticket)&k=\(longKey)&p=\(F.p)",
            "padded key": F.base + "&tv=1&t=\(F.ticket)&k=\(padded)&p=\(F.p)",
            "short key": F.base + "&tv=1&t=\(F.ticket)&k=\(shortKey)&p=\(F.p)",
            "standard alphabet": F.base + "&tv=1&t=\(F.ticket)&k=\(F.k)&p=\(F.p.replacingOccurrences(of: "_", with: "/"))",
            "empty token": F.link + "&r=",
            "token with space": F.link + "&r=a%20b",
            "rq other": F.link + "&rq=yes",
            "tv junk": F.base + "&tv=01&t=\(F.ticket)&k=\(F.k)&p=\(F.p)",
            "rq alone": F.base + "&rq=1",
        ]
        for (name, s) in bad {
            #expect(throws: DeepLink.Invalid.self, "\(name)") { try DeepLink(string: s) }
            if let e = (try? DeepLink(string: s)) == nil ? Self.error(s) : nil {
                #expect(e.tunnel != nil, "\(name): not a tunnel refusal")
                if case .newerVersion? = e.tunnel { Issue.record("\(name): read as a newer version") }
            }
        }
    }

    /// A version this app doesn't speak says "update", not "damaged".
    @Test func newerVersionsSayUpdate() throws {
        let s = F.base + "&tv=2&t=\(F.ticket)&k=\(F.k)&p=\(F.p)"
        let e = try #require(Self.error(s))
        #expect(e.tunnel == .newerVersion("2"))
        #expect(e.tunnel?.description.contains("Update xbin") == true)
        // The paste field's words, and the problem card's.
        #expect(throws: SignInLink.Invalid(reason: TunnelLink.Invalid.newerVersion("2").description)) { try SignInLink(s) }
        let p = ConnectProblem(SignInLink.Invalid(reason: "x"), server: nil)
        #expect(p.kind == .other)
    }

    @Test func signInLinkKinds() throws {
        let l = try SignInLink(F.link)
        guard case .tunnelEnroll(let t) = l else { Issue.record("not a tunnel link"); return }
        #expect(l.server == F.server)
        #expect(t.code == F.code)
        // A damaged one says it's damaged (not "not one that adds a device").
        do {
            _ = try SignInLink(F.base + "&tv=1&k=\(F.k)&p=\(F.p)")
            Issue.record("damaged link accepted")
        } catch let e as SignInLink.Invalid {
            #expect(e.reason.contains("damaged"))
        }
    }

    @Test func selectorInput() {
        let t = TunnelLink(server: F.server, code: "ABC", ticket: F.ticket, serverKey: F.serverKey, psk: F.psk)
        #expect(t.selectorInput == Data("xbin-pair-select-v1\nABC".utf8))
    }

    @Test func base32Decoding() {
        #expect(TunnelTicket.base32Decode("my") == Data([0x66]))
        #expect(TunnelTicket.base32Decode("MZXW6") == Data("foo".utf8))
        #expect(TunnelTicket.base32Decode("mzxw6ytboi") == Data("foobar".utf8))
        #expect(TunnelTicket.base32Decode("mz") == nil)   // non-zero leftover bits
        #expect(TunnelTicket.base32Decode("m") == nil)
        #expect(TunnelTicket.base32Decode("mzxw6===") == nil)
        #expect(TunnelTicket.base32Decode("") == nil)
        for n in [1, 5, 31, 32, 33, 100] {
            let d = Data((0..<n).map { UInt8(truncatingIfNeeded: $0 &* 37) })
            #expect(TunnelTicket.base32Decode(XbinCoreTests.base32(d)) == d)
        }
    }

    static func error(_ s: String) -> DeepLink.Invalid? {
        do { _ = try DeepLink(string: s); return nil } catch { return error as? DeepLink.Invalid }
    }
}

@Suite struct TunnelSASTests {
    @Test func digits() {
        #expect(TunnelSAS.number(Data(repeating: 0, count: 8)) == 0)
        #expect(TunnelSAS.digits(Data(repeating: 0, count: 8)) == "000 000")
        // 0x00000000000F4240 = 1 000 000 → 0; 0x…3039 = 12345.
        #expect(TunnelSAS.digits(Data([0, 0, 0, 0, 0, 0x0F, 0x42, 0x40])) == "000 000")
        #expect(TunnelSAS.digits(Data([0, 0, 0, 0, 0, 0, 0x30, 0x39])) == "012 345")
        // Big-endian, the whole 64 bits: 2^64-1 mod 10^6 = 551615.
        #expect(TunnelSAS.digits(Data(repeating: 0xFF, count: 8)) == "551 615")
        #expect(TunnelSAS.number(Data(count: 7)) == nil)
        #expect(TunnelSAS.number(Data(count: 9)) == nil)
    }

    @Test func parseAndSpeak() {
        #expect(TunnelSAS.parse("012345") == "012 345")
        #expect(TunnelSAS.parse("012 345") == "012 345")
        #expect(TunnelSAS.parse(.int(12345)) == "012 345")
        #expect(TunnelSAS.parse("12345") == nil)
        #expect(TunnelSAS.parse("01234a") == nil)
        #expect(TunnelSAS.parse(.int(1_000_000)) == nil)
        #expect(TunnelSAS.parse(nil) == nil)
        #expect(TunnelSAS.spoken("012 345") == "0 1 2, 3 4 5")
    }
}

@Suite struct PairingMessageTests {
    @Test func hello() throws {
        let spki = Data([1, 2, 3, 0xFF])
        let h = PairingHello(name: "Alice's iPhone", platform: "ios", publicKeySPKI: spki)
        let j = try JSONValue(parsing: h.data)
        #expect(j == ["v": 1, "name": "Alice's iPhone", "platform": "ios", "publicKey": "AQID_w"])
    }

    @Test func replies() throws {
        #expect(try PairingReply(data: Data(#"{"type":"await","expires":"2026-10-27T12:00:00Z"}"#.utf8))
            == .await(expires: Date(timeIntervalSince1970: 1_793_102_400)))
        #expect(try PairingReply(data: Data(#"{"type":"await","expires":"2026-10-27T12:00:00.5Z"}"#.utf8))
            == .await(expires: Date(timeIntervalSince1970: 1_793_102_400.5)))
        let enrolled = #"""
        {"type":"enrolled","deviceId":"dev_1","user":{"id":"alice","name":"Alice"},"origin":"https://xbin.example.com",
         "name":"Alice's iPhone","tunnel":{"ticket":"\#(TunnelFixture.ticket)","relay":{"url":"https://relay.example.com","token":"t0k"},"required":true}}
        """#
        guard case .enrolled(let e) = try PairingReply(data: Data(enrolled.utf8)) else { Issue.record("not enrolled"); return }
        #expect(e.deviceId == "dev_1")
        #expect(e.user == WorkspaceUser(id: "alice", name: "Alice"))
        #expect(e.origin == "https://xbin.example.com")
        #expect(e.tunnel.ticket == TunnelFixture.ticket)
        #expect(e.tunnel.relay == TunnelRelay(url: "https://relay.example.com", token: "t0k"))
        #expect(e.tunnel.required)
        #expect(e.enrollment.deviceId == "dev_1" && e.enrollment.user == "alice")
        for (reason, want) in [("mismatch", PairingRejection.mismatch), ("expired", .expired), ("limit", .limit),
                               ("refused", .refused), ("odd", .other("odd"))] {
            #expect(try PairingReply(json: ["type": "rejected", "reason": .string(reason), "error": "no"])
                == .rejected(want, message: "no"))
        }
        for bad in [#"{"type":"hello"}"#, #"{}"#, #"{"type":"enrolled","origin":"https://x"}"#,
                    #"{"type":"enrolled","deviceId":"d","origin":"https://x"}"#,
                    #"{"type":"enrolled","deviceId":"d","tunnel":{"ticket":"\#(TunnelFixture.ticket)"}}"#,
                    #"{"type":"enrolled","deviceId":"d","origin":"o","tunnel":{"ticket":"nope"}}"#] {
            #expect(throws: (any Error).self, "\(bad)") { try PairingReply(data: Data(bad.utf8)) }
        }
    }

    @Test func failuresAsProblems() {
        let s = TunnelFixture.server
        #expect(ConnectProblem(PairingFailure.rejected(.mismatch, message: "x"), server: s) == .pairingDeclined)
        #expect(ConnectProblem(PairingFailure.rejected(.expired, message: ""), server: s) == .pairingExpired)
        #expect(ConnectProblem(PairingFailure.timedOut, server: s) == .pairingExpired)
        #expect(ConnectProblem(PairingFailure.rejected(.refused, message: "spent"), server: s) == .codeRefused(detail: "spent"))
        #expect(ConnectProblem(PairingFailure.handshake("bad psk"), server: s).kind == .codeRefused)
        #expect(ConnectProblem(PairingFailure.unreachable("relay"), server: s)
            == .tunnelUnreachable(host: "xbin.example.com", detail: "relay"))
        let limit = ConnectProblem(PairingFailure.rejected(.limit, message: "you have 1 device; this workspace allows 1"), server: s)
        #expect(limit == .account("Can't add this device: you have 1 device; this workspace allows 1"))
        let limitNoWords = ConnectProblem(PairingFailure.rejected(.limit, message: ""), server: s)
        #expect(limitNoWords.message.contains("Remove one under Devices"))
        for p in [ConnectProblem.pairingDeclined, .pairingExpired, .tunnelRequired("x"),
                  .tunnelUnreachable(host: "h", detail: "d")] {
            #expect(!p.title.isEmpty && !p.message.isEmpty)
        }
    }

    @Test func record() throws {
        let link = TunnelLink(server: TunnelFixture.server, code: "C", ticket: TunnelFixture.ticket,
                              serverKey: TunnelFixture.serverKey, psk: TunnelFixture.psk, required: true)
        let e = PairingEnrolled(deviceId: "dev_9", user: WorkspaceUser(id: "alice"), origin: "https://ext.example.com",
                                name: "srv-name", tunnel: TunnelClientConfig(ticket: TunnelFixture.ticket,
                                                                             relay: TunnelRelay(url: "https://r.example")))
        let r = link.record(e, id: "WS1", deviceName: "My iPhone", serverEndpoint: Data([0xAB, 0x01]), port: 49152)
        #expect(r.id == "ws1")
        #expect(r.server == TunnelFixture.server)          // the identity stays the link's u
        #expect(r.signedOrigin == "https://ext.example.com") // what device login signs
        #expect(r.deviceId == "dev_9" && r.deviceName == "My iPhone")
        #expect(r.tunnel == TunnelInfo(endpoint: "ab01", relay: "https://r.example", required: true, port: 49152))
        #expect(r.isEncryptedInTransit)
    }
}

@Suite struct TunnelConfigTests {
    @Test func clientConfig() throws {
        let j: JSONValue = ["endpointId": "AB01", "ticket": .string(TunnelFixture.ticket),
                            "serverKey": .string(TunnelFixture.k),
                            "relay": ["url": "https://r1.example", "token": "t1", "next": ["url": "https://r2.example", "token": "t2"]],
                            "required": true]
        let c = try TunnelClientConfig(json: j)
        #expect(c.endpointId == "ab01")
        #expect(c.serverKey == TunnelFixture.serverKey)
        #expect(c.relay == TunnelRelay(url: "https://r1.example", token: "t1"))
        #expect(c.next == TunnelRelay(url: "https://r2.example", token: "t2"))
        #expect(c.required)
        #expect(c.relay.host == "r1.example")
        // n0's relays: no URL, no token.
        let n0 = try TunnelClientConfig(json: ["ticket": .string(TunnelFixture.ticket), "relay": [:]])
        #expect(n0.relay == TunnelRelay() && n0.next == nil && !n0.required && n0.relay.host == nil)
        #expect(throws: (any Error).self) { try TunnelClientConfig(json: ["ticket": "x"]) }
        #expect(throws: (any Error).self) {
            try TunnelClientConfig(json: ["ticket": .string(TunnelFixture.ticket), "serverKey": "short"])
        }
        #expect(TunnelClientConfig.path == "/api/xbin/app-tunnel/client")
    }

    @Test func secretsRoundTrip() throws {
        var s = TunnelSecrets(irohSecret: Data(repeating: 1, count: 32), noiseStatic: Data(repeating: 2, count: 32),
                              serverKey: TunnelFixture.serverKey, ticket: TunnelFixture.ticket,
                              relay: TunnelRelay(url: "https://r1.example", token: "t1"), endpoint: "ab01")
        #expect(TunnelSecrets(data: s.encoded()) == s)
        s.apply(TunnelClientConfig(ticket: TunnelFixture.ticket, relay: TunnelRelay(), next: TunnelRelay(url: "https://r2.example"),
                                   required: true, endpointId: "cd02", serverKey: Data(repeating: 9, count: 32)))
        #expect(s.endpoint == "cd02" && s.required && s.relay == TunnelRelay() && s.next?.url == "https://r2.example")
        #expect(s.serverKey == Data(repeating: 9, count: 32))
        #expect(TunnelSecrets(data: s.encoded()) == s)
        #expect(s.info(port: 5) == TunnelInfo(endpoint: "cd02", relay: nil, required: true, port: 5))
        // Damaged or newer items aren't misread.
        #expect(TunnelSecrets(data: Data("{}".utf8)) == nil)
        var newer = s.json
        if case .object(var o) = newer { o["v"] = 2; newer = .object(o) }
        #expect(TunnelSecrets(data: newer.jsonData) == nil)
    }

    @Test func loginMethods() throws {
        let base: JSONValue = ["api": 1, "auth": true, "password": ["enabled": true], "sso": ["enabled": true, "label": "Acme"]]
        // Before D204: no tunnel field.
        let old = try LoginMethods(json: base)
        #expect(old.tunnel == nil && !old.tunnelRequired)
        #expect(SignInOptions(old).methods == [.password(header: nil), .sso(label: "Acme")])
        var on = base
        if case .object(var o) = on { o["tunnel"] = ["enabled": true, "required": false]; on = .object(o) }
        let enabled = try LoginMethods(json: on)
        #expect(enabled.tunnel == LoginMethods.TunnelMethods(enabled: true, required: false))
        #expect(SignInOptions(enabled).methods == [.password(header: nil), .sso(label: "Acme")])
        var req = base
        if case .object(var o) = req { o["tunnel"] = ["enabled": true, "required": true]; req = .object(o) }
        let required = try LoginMethods(json: req)
        #expect(required.tunnelRequired)
        // QR pairing only: no password form, no SSO button.
        let opts = SignInOptions(required)
        #expect(opts.methods == [.pairing])
        #expect(!opts.hasPassword && opts.ssoLabel == nil && opts.explanation == nil)
        // "required" without "enabled" is nothing.
        var odd = base
        if case .object(var o) = odd { o["tunnel"] = ["enabled": false, "required": true]; odd = .object(o) }
        #expect(try !LoginMethods(json: odd).tunnelRequired)
    }

    @Test func upgrade() throws {
        let r = TunnelUpgrade.request(deviceId: "dev 1", noiseKey: Data(repeating: 0xFF, count: 32), endpoint: Data([0x0A, 0xBC]))
        #expect(r.method == "POST" && r.path == "/api/xbin/devices/dev%201/tunnel")
        #expect(body(r) == ["noiseKey": .string(Base64URL.encode(Data(repeating: 0xFF, count: 32))), "endpoint": "0abc"])
        var rec = WorkspaceRecord(id: "w", server: TunnelFixture.server, user: WorkspaceUser(id: "a"), deviceId: "d")
        let on = LoginMethods(password: true, sso: false, tunnel: .init(enabled: true, required: false))
        #expect(TunnelUpgrade.isDue(record: rec, methods: on))
        #expect(!TunnelUpgrade.isDue(record: rec, methods: LoginMethods(password: true, sso: false)))
        #expect(!TunnelUpgrade.isDue(record: rec, methods: nil))
        rec.tunnel = TunnelInfo(endpoint: "ab")
        #expect(!TunnelUpgrade.isDue(record: rec, methods: on))
        rec.tunnel = nil
        rec.deviceId = nil
        #expect(!TunnelUpgrade.isDue(record: rec, methods: on))
    }

    @Test func pairingStatus() throws {
        let s = PairingStatus(json: ["state": "confirm", "sas": "012345", "device": ["name": "Bob's iPad", "platform": "ipados"],
                                     "expires": 1_790_000_000])
        #expect(s == PairingStatus(state: .confirm, sas: "012 345", deviceName: "Bob's iPad", devicePlatform: "ipados",
                                   expires: Date(timeIntervalSince1970: 1_790_000_000)))
        #expect(PairingStatus(json: ["state": "handshaken"]).state == .waiting)
        #expect(PairingStatus(json: ["state": "done"]).state.isFinal)
        #expect(!PairingStatus(json: ["state": "confirm"]).state.isFinal)
        #expect(PairingStatus(json: ["state": "abandoned"]).state == .abandoned)
        #expect(PairingStatus(json: ["state": "confirm", "atLimit": true]).atLimit)
        #expect(PairingStatus(json: ["state": "confirm", "limit": ["max": 1, "count": 1]]).atLimit)
        // S1's final shapes (D204): the states, the code as "012345", "012 345"
        // or a number, expires in unix seconds, limit and count as numbers.
        let s1 = PairingStatus(json: ["state": "confirm", "sas": 12345, "device": ["name": "Phone", "platform": "ios"],
                                      "expires": 1_790_000_000, "atLimit": true, "limit": 1, "count": 1])
        #expect(s1.sas == "012 345" && s1.atLimit && s1.expires == Date(timeIntervalSince1970: 1_790_000_000))
        #expect(PairingStatus(json: ["state": "confirm", "sas": "012 345"]).sas == "012 345")
        #expect(PairingStatus(json: ["state": "minted"]).state == .waiting)
        #expect(PairingStatus(json: ["state": "enrolled", "deviceId": "dev_2"]).state == .done)
        #expect(PairingStatus(json: ["state": "rejected", "reason": "mismatch"]).state == .rejected)
        #expect(PairingStatus(json: ["state": "expired"]).state.isFinal)
        #expect(!PairingStatus(json: ["state": "confirm", "limit": 3, "count": 1]).atLimit)
        #expect(PairingStatus.poll("p/1").path == "/api/xbin/devices/pairing/p%2F1")
        let c = PairingStatus.confirm("p1", match: true, replace: "dev_old")
        #expect(c.method == "POST" && c.path == "/api/xbin/devices/pairing/p1/confirm")
        #expect(body(c) == ["match": true, "replace": "dev_old"])
        #expect(body(PairingStatus.confirm("p1", match: false)) == ["match": false])
    }
}

@Suite struct TunnelStatusTests {
    @Test func summaries() throws {
        let https = TunnelFixture.server
        #expect(TunnelStatus.direct.summary(server: https) == "Direct (https)")
        #expect(TunnelStatus.connecting.summary(server: https) == "Tunnel · connecting…")
        #expect(TunnelStatus.connected(path: .direct, rtt: 0.018).summary(server: https) == "Tunnel · direct · 18 ms")
        #expect(TunnelStatus.connected(path: .relay(host: "relay.example.com"), rtt: 0.085).summary(server: https)
            == "Tunnel · relay (relay.example.com) · 85 ms")
        #expect(TunnelStatus.connected(path: .relay(host: nil), rtt: nil).summary(server: https) == "Tunnel · relay")
    }

    @Test func reconnectPolicy() {
        var p = TunnelReconnectPolicy(base: 0.5, maximum: 4)
        #expect(p.nextDelay == 0)
        #expect(p.failed() == 0.5)
        #expect(p.failed() == 1)
        #expect(p.failed() == 2)
        #expect(p.failed() == 4)
        #expect(p.failed() == 4)
        p.succeeded()
        #expect(p.nextDelay == 0)
        _ = p.failed()
        p.setActive(false)
        #expect(p.failed() == nil)     // nothing in the background
        p.setActive(true)              // back in front: at once
        #expect(p.nextDelay == 0)
    }
}

@Suite struct TunnelRefusalTests {
    /// 403 `{tunnel: "required"}` ends a request as a sign-in problem that
    /// offers pairing (never a re-sign loop), from any route.
    @Test func requestsOffTheTunnel() async throws {
        let server = FakeServer()
        let refusal: JSONValue = ["error": "this workspace accepts the app only through QR pairing", "tunnel": "required",
                                  "docs": "/docs/app-tunnel.md"]
        await server.on("GET /api/xbin/whoami") { _ in json(403, refusal) }
        await server.on("POST /login/device/challenge") { _ in json(403, refusal) }
        let keys = FakeKeys()
        await keys.add("w")
        let rec = WorkspaceRecord(id: "w", server: TunnelFixture.server, user: WorkspaceUser(id: "a"), deviceId: "d")
        let sessions = MemorySessionStore()
        await sessions.saveSession(SessionCredential(token: "t", kind: .device), workspace: "w")
        let auth = WorkspaceAuth(record: rec, transport: server, keys: keys, sessions: sessions, clientHeader: "app/1")
        do {
            _ = try await auth.json(APIRequest("GET", "/api/xbin/whoami"))
            Issue.record("no refusal")
        } catch let e as SignInError {
            #expect(e == .tunnelRequired("this workspace accepts the app only through QR pairing"))
            #expect(e.needsEnrollment)
            #expect(ConnectProblem(e, server: TunnelFixture.server).kind == .tunnelRequired)
        }
        // The device sign-in says the same.
        await auth.adopt(nil)
        await #expect(throws: SignInError.tunnelRequired("this workspace accepts the app only through QR pairing")) {
            try await auth.signIn()
        }
        #expect(await keys.prompts == 0)
        // Other 403s are answers, as before.
        await server.on("GET /api/xbin/x") { _ in json(403, ["error": "no"]) }
        await auth.adopt(SessionCredential(token: "t", kind: .device))
        #expect(try await auth.send(APIRequest("GET", "/api/xbin/x")).status == 403)
    }

    @Test func sign_in_routes() {
        let e = APIError(json(403, ["error": "pair", "tunnel": "required"]))
        #expect(e.isTunnelRequired && e.tunnel == "required")
        #expect(ConnectProblem(Enrollment.Failure.server(e), server: nil) == .tunnelRequired("pair"))
        #expect(!APIError(json(403, ["error": "x", "tunnel": "device"])).isTunnelRequired)
        #expect(!APIError(json(401, ["error": "x", "tunnel": "required"])).isTunnelRequired)
    }
}

@Suite struct DeviceListTunnelTests {
    @Test func limitAndTunnelRows() throws {
        let j: JSONValue = ["devices": [["id": "a", "name": "iPhone", "tunnel": true], ["id": "b", "name": "iPad"]],
                            "limit": 3, "count": 2]
        let rows = DeviceInfo.list(json: j)
        #expect(rows.map(\.tunnel) == [true, false])
        let l = try #require(DeviceInfo.limit(json: j))
        #expect(l.count == 2 && l.limit == 3)
        #expect(DeviceInfo.limit(json: ["devices": []]) == nil)   // an xbind before limits
        #expect(DeviceInfo(json: ["id": "c", "tunnel": ["endpoint": "ab"]]).tunnel)
    }
}
