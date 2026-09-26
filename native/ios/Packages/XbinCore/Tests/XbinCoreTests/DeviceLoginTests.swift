import Foundation
import Testing
@testable import XbinCore

@Suite struct DeviceLoginTests {
    @Test func messageBytes() throws {
        let m = try DeviceLogin.message(origin: "https://xbin.example.com", deviceId: "dev_7Hq2xK", nonce: "n0nce-3f9a1c")
        #expect(String(decoding: m, as: UTF8.self) == "xbin-device-login-v1\nhttps://xbin.example.com\ndev_7Hq2xK\nn0nce-3f9a1c")
        #expect(m.map { String(format: "%02x", $0) }.joined()
            == "7862696e2d6465766963652d6c6f67696e2d76310a68747470733a2f2f7862696e2e6578616d706c652e636f6d0a6465765f374871327"
            + "84b0a6e306e63652d336639613163")
        let viaServer = try DeviceLogin.message(server: ServerOrigin(string: "HTTPS://XBIN.example.com:443/"), deviceId: "dev_7Hq2xK", nonce: "n0nce-3f9a1c")
        #expect(viaServer == m)
        // UTF-8, not Latin-1 or UTF-16.
        #expect(try DeviceLogin.message(origin: "https://ü.example", deviceId: "d", nonce: "n").count == 21 + 18 + 1 + 1 + 1 + 1)
    }

    @Test func messageRefusesAmbiguousFields() {
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceLogin.message(origin: "https://x", deviceId: "a\nb", nonce: "n") }
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceLogin.message(origin: "https://x", deviceId: "d", nonce: "n\r") }
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceLogin.message(origin: "", deviceId: "d", nonce: "n") }
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceLogin.message(origin: "https://x", deviceId: "d", nonce: "") }
    }

    /// The shared vector (Resources/device-login-v1.json =
    /// native/spec/device-login.md §7, xbind's TestDeviceLoginVector).
    @Test func sharedVector() throws {
        let v = try Resources.json("device-login-v1.json")
        func str(_ k: String) throws -> String { try #require(v[k]?.stringValue) }
        let msg = try DeviceLogin.message(origin: str("origin"), deviceId: str("deviceId"), nonce: str("nonce"))
        #expect(String(decoding: msg, as: UTF8.self) == (try str("message")))

        let spki = try #require(Base64URL.decode(try str("publicKey")))
        #expect(DeviceLogin.isP256SPKI(spki))
        let x963 = try hexData(str("publicKeyX963Hex"))
        #expect(try DeviceLogin.spki(x963: x963) == spki)
        #expect(spki.suffix(65) == x963)
        let sig = try #require(Base64URL.decode(try str("signature")))
        #expect(DeviceLogin.isDERSignature(sig))
        // A trailing slash (or any other change) is a different message.
        #expect(try DeviceLogin.message(origin: str("origin") + "/", deviceId: str("deviceId"), nonce: str("nonce")) != msg)

        let req = DeviceLoginRequest(deviceId: try str("deviceId"), nonce: try str("nonce"), signatureDER: sig)
        let body = try JSONValue(parsing: JSONEncoder().encode(req))
        #expect(body == ["deviceId": .string(try str("deviceId")), "nonce": .string(try str("nonce")), "signature": v["signature"]!])

        // Through a workspace record: the enrollment's origin is what's signed.
        var w = WorkspaceRecord(server: try ServerOrigin(string: "http://10.0.0.5:8080"), user: .init(id: "alice"))
        #expect(throws: DeviceLogin.Invalid.self) { try w.deviceLoginMessage(nonce: "n") }
        w.enrolled(try DeviceEnrollment(json: ["deviceId": .string(try str("deviceId")), "user": "alice",
                                                "origin": .string(try str("origin")), "name": "Alice's iPhone"]))
        #expect(try w.deviceLoginMessage(nonce: str("nonce")) == msg)
        #expect(w.deviceName == "Alice's iPhone")
    }

    func hexData(_ s: String) throws -> Data {
        var out = Data()
        var it = s.makeIterator()
        while let a = it.next(), let b = it.next() {
            out.append(try #require(UInt8(String([a, b]), radix: 16)))
        }
        return out
    }

    @Test func keyAndSignatureShapes() throws {
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceLogin.spki(x963: Data(repeating: 4, count: 64)) }
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceLogin.spki(x963: Data([0x02] + [UInt8](repeating: 1, count: 64))) }
        #expect(!DeviceLogin.isP256SPKI(Data(repeating: 0, count: 91)))
        #expect(!DeviceLogin.isDERSignature(Data()))
        #expect(!DeviceLogin.isDERSignature(Data([0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01, 0x00]))) // trailing byte
        #expect(DeviceLogin.isDERSignature(Data([0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01])))
        #expect(!DeviceLogin.isDERSignature(Data([0x30, 0x06, 0x02, 0x01, 0x01, 0x03, 0x01, 0x01])))
    }

    @Test func base64url() {
        // RFC 4648 §10, in the url alphabet without padding.
        let vectors = ["": "", "f": "Zg", "fo": "Zm8", "foo": "Zm9v", "foob": "Zm9vYg", "fooba": "Zm9vYmE", "foobar": "Zm9vYmFy"]
        for (plain, enc) in vectors {
            #expect(Base64URL.encode(plain) == enc)
            #expect(Base64URL.decode(enc) == Data(plain.utf8))
        }
        #expect(Base64URL.encode(Data([0xFB, 0xFF, 0xBF])) == "-_-_")
        #expect(Base64URL.encode(Data([0xFB, 0xFF])) == "-_8")
        #expect(Base64URL.decode("-_8") == Data([0xFB, 0xFF]))
        #expect(Base64URL.decode("Zg==") == Data("f".utf8))
        #expect(Base64URL.decode("Zm8=") == Data("fo".utf8))
        #expect(Data(base64URLEncoded: "Zm9v")?.base64URLEncodedString == "Zm9v")
        for bad in ["+_8", "/w", "Z", "Zg=", "Zm9v=", "Zg===", "Zm 9", "Zm9v\n", "é"] {
            #expect(Base64URL.decode(bad) == nil, "\(bad)")
        }
        var r = SeededRNG(seed: 64)
        for n in 0..<100 {
            let d = Data((0..<n).map { _ in UInt8.random(in: 0...255, using: &r) })
            let s = Base64URL.encode(d)
            #expect(!s.contains("=") && !s.contains("+") && !s.contains("/"))
            #expect(Base64URL.decode(s) == d)
        }
    }

    @Test func serverOrigins() throws {
        #expect(try ServerOrigin(string: "HTTPS://Xbin.Example.COM:443/").origin == "https://xbin.example.com")
        #expect(try ServerOrigin(string: "http://host:80").origin == "http://host")
        #expect(try ServerOrigin(string: "http://host:8080").origin == "http://host:8080")
        #expect(try ServerOrigin(string: "https://host:80").origin == "https://host:80")
        #expect(try ServerOrigin(string: "http://[::1]:8080").origin == "http://[::1]:8080")
        #expect(try ServerOrigin(string: "http://[::1]:8080").authority == "[::1]:8080")
        #expect(try ServerOrigin(string: " https://h ").origin == "https://h")
        #expect(try ServerOrigin(userInput: "xbin.example.com").origin == "https://xbin.example.com")
        #expect(try ServerOrigin(userInput: "xbin.example.com:8443/some/path?q#f").origin == "https://xbin.example.com:8443")
        #expect(try ServerOrigin(userInput: "HTTP://10.0.0.5:8080").origin == "http://10.0.0.5:8080")
        let s = try ServerOrigin(string: "https://h:8443")
        #expect(s.webSocketOrigin == "wss://h:8443" && s.isSecure && s.url.absoluteString == "https://h:8443")
        #expect(try ServerOrigin(string: "http://h").webSocketOrigin == "ws://h")
        #expect(s.url(path: "/api/xbin/whoami")?.absoluteString == "https://h:8443/api/xbin/whoami")
        for bad in ["ftp://h", "https://", "h", "https://u:p@h", "https://good@evil.com", "https://h/path", "https://h?x", "https://h#f",
                    "javascript:alert(1)", "https://h:0", "https://h:70000"] {
            #expect(throws: ServerOrigin.Invalid.self, "\(bad)") { try ServerOrigin(string: bad) }
        }
        #expect(throws: ServerOrigin.Invalid.self) { try ServerOrigin(userInput: "ftp://h") }
        // Codable as the origin string.
        #expect(String(decoding: try JSONEncoder().encode([s]), as: UTF8.self) == #"["https:\/\/h:8443"]"#)
        #expect(try JSONDecoder().decode([ServerOrigin].self, from: Data(#"["HTTPS://H:443"]"#.utf8)) == [try ServerOrigin(string: "https://h")])
    }

    @Test func wireTypes() throws {
        // Requests: the exact bodies of native/spec/device-login.md.
        let enroll = DeviceEnrollRequest(code: "K3DQ", name: "Łukasz's iPhone", platform: "ios", publicKeySPKI: Data([1, 2, 3]))
        #expect(try JSONValue(parsing: JSONEncoder().encode(enroll))
            == ["code": "K3DQ", "name": "Łukasz's iPhone", "platform": "ios", "publicKey": "AQID"])
        #expect(try JSONValue(parsing: JSONEncoder().encode(DeviceChallengeRequest(deviceId: "dev-1"))) == ["deviceId": "dev-1"])
        #expect(try JSONValue(parsing: JSONEncoder().encode(PasswordLoginRequest(username: "alice", password: "pw")))
            == ["username": "alice", "password": "pw"])
        #expect(try JSONValue(parsing: JSONEncoder().encode(TicketRedeemRequest(ticket: "t", verifier: "v"))) == ["ticket": "t", "verifier": "v"])

        // Responses.
        let e = try DeviceEnrollment(json: ["deviceId": "dev-1", "user": "alice", "origin": "https://x", "name": "iPhone"])
        #expect(e.deviceId == "dev-1" && e.user == "alice" && e.origin == "https://x" && e.name == "iPhone")
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceEnrollment(json: ["deviceId": "dev-1"]) }
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceEnrollment(json: ["origin": "https://x"]) }

        let code = try EnrollCode(json: ["code": "ABC", "url": "xbin://enroll?u=https%3A%2F%2Fx&c=ABC", "origin": "https://x", "expires": 1_790_000_300])
        #expect(code.expires == Date(timeIntervalSince1970: 1_790_000_300))
        #expect(try DeepLink(string: code.url) == .enroll(server: try ServerOrigin(string: "https://x"), code: "ABC"))

        let ch = try DeviceChallenge(json: ["nonce": "n1", "expires": 1_790_000_060])
        #expect(ch == DeviceChallenge(nonce: "n1", expires: Date(timeIntervalSince1970: 1_790_000_060)))
        #expect(throws: DeviceLogin.Invalid.self) { try DeviceChallenge(json: ["nonce": ""]) }

        let sess = try AppSession(json: ["token": "tok", "tokenType": "Bearer", "user": ["id": "alice", "name": "Alice", "role": "user"],
                                         "deviceId": "dev-1", "expiresIdle": 1_790_043_200, "expiresMax": 1_792_592_000])
        #expect(sess.token == "tok" && sess.user == .init(id: "alice", name: "Alice", role: "user") && sess.deviceId == "dev-1")
        #expect(sess.expiresIdle == Date(timeIntervalSince1970: 1_790_043_200) && sess.authorization == "Bearer tok")
        let pw = try AppSession(json: ["token": "t2", "user": ["id": "bob"], "expiresIdle": 1, "expiresMax": 2])
        #expect(pw.deviceId == nil && pw.tokenType == "Bearer" && pw.user.id == "bob")
        #expect(throws: DeviceLogin.Invalid.self) { try AppSession(json: [:]) }

        let devices = DeviceInfo.list(json: ["devices": [
            ["id": "dev-1", "name": "iPhone", "platform": "ios", "origin": "https://x", "created": 1_780_000_000,
             "lastUsed": 1_790_000_000, "lastIP": "10.0.0.2", "current": true],
            ["id": "dev-2", "name": "iPad", "platform": "ipados", "origin": "https://x", "created": 1_780_000_001],
        ]])
        #expect(devices.map(\.id) == ["dev-1", "dev-2"] && devices[0].current && !devices[1].current)
        #expect(devices[0].lastUsed == Date(timeIntervalSince1970: 1_790_000_000) && devices[1].lastUsed == nil)
        #expect(DeviceInfo.list(json: [:]).isEmpty)
    }

    @Test func enrollCodesAndPKCE() {
        #expect(DeviceLogin.normalizeEnrollCode(" k3dq-7xyz abcd\n") == "K3DQ7XYZABCD")
        #expect(DeviceLogin.isEnrollCode("ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
        #expect(DeviceLogin.isEnrollCode("A234567ABCDEFGHIJKLMNOPQRS"))
        #expect(!DeviceLogin.isEnrollCode("ABCDEFGHIJKLMNOPQRSTUVWXY1")) // 1 is not base32
        #expect(!DeviceLogin.isEnrollCode("abcdefghijklmnopqrstuvwxyz"))
        #expect(!DeviceLogin.isEnrollCode("ABC"))

        let v = PKCE.makeVerifier()
        #expect(v.count == 43 && PKCE.isValidVerifier(v))
        #expect(PKCE.makeVerifier() != v)
        #expect(!PKCE.isValidVerifier(String(repeating: "a", count: 42)))
        #expect(PKCE.isValidVerifier(String(repeating: "a", count: 128)) && !PKCE.isValidVerifier(String(repeating: "a", count: 129)))
        #expect(!PKCE.isValidVerifier(String(repeating: "a", count: 42) + "+"))
        // RFC 7636 appendix B: the S256 challenge of its example verifier.
        let digest = Data([0x13, 0xd3, 0x1e, 0x96, 0x1a, 0x1a, 0xd8, 0xec, 0x2f, 0x16, 0xb1, 0x0c, 0x4c, 0x98, 0x2e, 0x08,
                           0x76, 0xa8, 0x78, 0xad, 0x6d, 0xf1, 0x44, 0x56, 0x6e, 0xe1, 0x89, 0x4a, 0xcb, 0x70, 0xf9, 0xc3])
        #expect(PKCE.challenge(sha256Digest: digest) == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
        #expect(AppAuthRoute.ssoStart(challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
            == "/login/sso?app=1&challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
    }
}
