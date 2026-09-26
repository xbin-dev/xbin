import Foundation
import Testing
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif
import SwiftTerm
import XbinCore
import XbinTerm

/// The repository root, from this file's path.
let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

func specJSON(_ name: String) throws -> JSONValue {
    try JSONValue(parsing: Data(contentsOf: repo.appendingPathComponent("native/spec/\(name)")))
}

func b64(_ v: JSONValue?) throws -> Data { try #require(v?.stringValue.flatMap(Base64URL.decode)) }

func hex(_ s: String) -> Data {
    var d = Data()
    var i = s.startIndex
    while i < s.endIndex {
        let j = s.index(i, offsetBy: 2)
        d.append(UInt8(s[i..<j], radix: 16)!)
        i = j
    }
    return d
}

/// native/spec/push.md §5: what "a device implementation must" do.
@Suite struct PushVectorTests {
    @Test func opensEveryVector() throws {
        let v = try specJSON("push-vectors.json")
        let vectors = try #require(v["vectors"]?.arrayValue)
        #expect(vectors.count >= 3)
        for vec in vectors {
            let envJSON = try #require(vec["envelope"])
            let env = try #require(PushEnvelope(json: envJSON))
            let priv = try b64(vec["recipientPrivate"])
            // (a) opens to exactly the plaintext.
            let plain = try #require(PushCrypto.open(env, privateKey: priv))
            #expect(String(decoding: plain, as: UTF8.self) == vec["plaintext"]?.stringValue)
            // The intermediates: the shared secret's HKDF key.
            let key = try #require(PushCrypto.derive(priv: .init(rawRepresentation: priv), epk: env.epk))
            #expect(key.withUnsafeBytes { Data($0) } == (try b64(vec["key"])))
            #expect(PushCrypto.publicKey(of: priv) == (try b64(vec["recipientPublic"])))
            // (b) reproduces ct from the ephemeral key and nonce.
            let recipient = try b64(vec["recipientPublic"])
            let ephemeral = try b64(vec["ephemeralPrivate"])
            let again = try #require(PushCrypto.seal(plain, to: recipient, ephemeral: ephemeral, nonce: env.nonce))
            #expect(again.sealed == env.sealed && again.epk == env.epk)
            // And the payload is one the app understands.
            #expect(PushPayload(plaintext: plain) != nil)
        }
    }

    @Test func refusesEveryInvalidEntry() throws {
        let v = try specJSON("push-vectors.json")
        let invalid = try #require(v["invalid"]?.arrayValue)
        #expect(!invalid.isEmpty)
        for bad in invalid {
            let priv = try b64(bad["recipientPrivate"])
            let envJSON = try #require(bad["envelope"])
            if let env = PushEnvelope(json: envJSON) {
                #expect(PushCrypto.open(env, privateKey: priv) == nil, "\(bad["name"]?.stringValue ?? "?") opened")
            }
        }
    }

    /// The extension's whole path: several workspaces' keys, one payload.
    @Test func extensionPicksTheWorkspace() throws {
        let a = PushCrypto.newKeyPair(), b = PushCrypto.newKeyPair()
        let payload = Data(#"{"v":1,"ws":"push-b","kind":"agent.turn","title":"Done","body":"x","link":"agent/s1"}"#.utf8)
        let env = try #require(PushCrypto.seal(payload, to: b.publicKey, ephemeral: PushCrypto.newKeyPair().privateKey,
                                               nonce: Data((0..<12).map { UInt8($0) })))
        let keys = ["a": a.privateKey, "b": b.privateKey]
        let opened = PushOpener.open(env, candidates: [("a", "push-a"), ("b", "push-b")]) { ws in
            PushCrypto.open(env, privateKey: keys[ws]!)
        }
        #expect(opened?.appWorkspace == "b" && opened?.payload.deepLink(appWorkspace: "b") == .agent(workspace: "b", session: "s1"))
    }
}

/// native/spec/device-login.md §7 — the checks "a client test should make".
@Suite struct DeviceKeyTests {
    @Test func specVector() throws {
        let v = try JSONValue(parsing: Data(contentsOf: repo.appendingPathComponent(
            "native/ios/Packages/XbinCore/Tests/XbinCoreTests/Resources/device-login-v1.json")))
        func s(_ k: String) throws -> String { try #require(v[k]?.stringValue) }
        let key = try SoftwareDeviceKey(rawRepresentation: hex(try s("privateScalarHex")))
        #expect(key.publicKeySPKI == (try b64(v["publicKey"])))
        #expect(try P256.Signing.PublicKey(derRepresentation: key.publicKeySPKI).x963Representation == hex(try s("publicKeyX963Hex")))
        let msg = try DeviceLogin.message(origin: s("origin"), deviceId: s("deviceId"), nonce: s("nonce"))
        #expect(Data(SHA256.hash(data: msg)) == hex(try s("messageSHA256Hex")))
        let sig = try b64(v["signature"])
        #expect(SoftwareDeviceKey.verify(signature: sig, message: msg, publicKeySPKI: key.publicKeySPKI))
        for (o, d, n) in [(try s("origin") + "/", try s("deviceId"), try s("nonce")),
                          (try s("origin"), try s("deviceId") + "x", try s("nonce")),
                          (try s("origin"), try s("deviceId"), try s("nonce") + "x")] {
            let other = try DeviceLogin.message(origin: o, deviceId: d, nonce: n)
            #expect(!SoftwareDeviceKey.verify(signature: sig, message: other, publicKeySPKI: key.publicKeySPKI))
        }
        // Our own signatures: DER (not raw r‖s), and they verify.
        let mine = try key.sign(msg)
        #expect(DeviceLogin.isDERSignature(mine) && mine.count != 64)
        #expect(SoftwareDeviceKey.verify(signature: mine, message: msg, publicKeySPKI: key.publicKeySPKI))
        #expect(DeviceLogin.isP256SPKI(SoftwareDeviceKey().publicKeySPKI))
    }
}

final class Headless: TerminalDelegate {
    var sent: [UInt8] = []
    var cursorHidden = false
    func send(source: Terminal, data: ArraySlice<UInt8>) { sent += data }
    func showCursor(source: Terminal) { cursorHidden = false }
    func hideCursor(source: Terminal) { cursorHidden = true }
}

/// The terminal glue on a real SwiftTerm emulator: the framebuffer the
/// predictor reads, and a prediction confirmed by the echo.
@Suite struct SwiftTermScreenTests {
    func terminal(_ cols: Int = 20, _ rows: Int = 5) -> (Terminal, Headless) {
        let d = Headless()
        let t = Terminal(delegate: d, options: TerminalOptions(cols: cols, rows: rows))
        return (t, d)
    }

    @Test func framebuffer() {
        let (t, _) = terminal()
        let fb = SwiftTermScreen(terminal: t)
        #expect(fb.rows == 5 && fb.cols == 20)
        t.feed(text: "$ hi")
        #expect(fb.cursor == TermPos(row: 0, col: 4))
        #expect(fb.charAt(0, 0) == "$" && fb.charAt(0, 1) == " " && fb.charAt(0, 3) == "i")
        #expect(fb.charAt(0, 10) == "")                                   // never written
        #expect(fb.lineAt(0) == "$ hi" + String(repeating: " ", count: 16))
        t.feed(text: "\r\n中x")
        #expect(fb.widthAt(1, 0) == 2 && fb.widthAt(1, 1) == 0 && fb.widthAt(1, 2) == 1)
        #expect(fb.charAt(1, 0) == "中" && fb.charAt(1, 2) == "x" && fb.cursor == TermPos(row: 1, col: 3))
        #expect(fb.lineAt(1).count == 20)
        #expect(fb.charAt(9, 0) == "" && fb.widthAt(9, 0) == 1)          // out of range
    }

    @Test func predictionConfirmedByEcho() {
        let (t, _) = terminal()
        let fb = SwiftTermScreen(terminal: t)
        t.feed(text: "$ ")
        let p = Predictor()
        p.setMode(.on)
        p.newUserData("ls", fb, now: 0)
        p.setLocalFrameSent(1)
        let r = p.render(fb)
        #expect(r.cells.map(\.text) == ["ls"] && r.cells.first?.col == 2 && r.cursor == TermPos(row: 0, col: 4))
        t.feed(text: "ls")                                                 // the shell echoes
        p.setLateAck(1)
        p.cull(fb, now: 60)
        #expect(p.render(fb).cells.isEmpty && p.pending() == 0)
    }

    @Test func resetClearsScreenAndScrollback() {
        let (t, _) = terminal(10, 3)
        let fb = SwiftTermScreen(terminal: t)
        t.feed(text: "a\r\nb\r\nc\r\nd\r\n")
        t.resetToInitialState()
        #expect(fb.cursor == TermPos(row: 0, col: 0) && fb.charAt(0, 0) == "")
    }
}
