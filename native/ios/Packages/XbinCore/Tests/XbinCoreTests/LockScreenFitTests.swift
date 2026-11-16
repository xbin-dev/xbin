import Foundation
import Testing
@testable import XbinCore

/// The lock screen's rule in rendered terms (D241, review findings 1 and 6):
/// the app's copy of xbind's delegation.LockScreenFits, held to it by the
/// vectors xbind's test writes (lockscreen-fit.json), and the attacks the
/// review found refused by the app's own check.
@Suite struct LockScreenFitTests {
    private func request(_ body: String, target: String = "/api/apps/note/note") -> AgentNoticeData.Request {
        var r = AgentNoticeData.Request()
        r.id = "0123456789abcdef01234567"
        r.kind = "call"
        r.tile = "apps/note"
        r.offer = "once"
        r.method = "PUT"
        r.target = target
        r.contentType = "application/json"
        r.body = body
        r.bodySize = body.utf8.count
        return r
    }

    @Test func xbindsVectors() throws {
        let cases = try #require(Resources.json("lockscreen-fit.json").arrayValue)
        #expect(cases.count > 400)
        for c in cases {
            let m = c["method"]?.stringValue ?? "", t = c["target"]?.stringValue ?? "", b = c["body"]?.stringValue ?? ""
            #expect(LockScreenFit.fits(method: m, target: t, body: b) == (c["fits"]?.boolValue ?? false), "\(m) \(t) \(b.debugDescription)")
            if c["fits"]?.boolValue == true {
                #expect(LockScreenFit.lines(method: m, target: t, body: b) == Int(c["lines"]?.intValue ?? -1), "\(b.debugDescription)")
            }
        }
    }

    @Test func whatIsNotSeenIsNeverOffered() {
        #expect(AgentNotice.eligible(request(#"{"note":"ok"}"#)))
        let attacks: [String: String] = [
            "line separators": #"{"note":"ok"# + String(repeating: "\u{2028}", count: 5) + #"","all":true}"#,
            "paragraph separators": #"{"note":"ok"# + String(repeating: "\u{2029}", count: 5) + #"","all":true}"#,
            "next line": "{\"note\":\"ok\u{85}\u{85}\u{85}\u{85}\",\"all\":true}",
            "vertical tab": "{\"note\":\"ok\u{0B}\u{0B}\",\"all\":true}",
            "form feed": "{\"note\":\"ok\u{0C}\u{0C}\",\"all\":true}",
            "a carriage return": "{\"note\":\"ok\r\r\r\",\"all\":true}",
            "a tab": "{\"note\":\"ok\t\",\"all\":true}",
            "spaces pushing the tail": #"{"note":"ok""# + String(repeating: " ", count: 150) + #","all":true}"#,
            "two spaces": #"{"note":"ok  then"}"#,
            "a no-break space": "{\"note\":\"ok\u{A0}then\"}",
            "an ideographic space": "{\"note\":\"ok\u{3000}then\"}",
            "right-to-left letters": #"["א",1,2,"ב"]"#,
            "arabic": #"{"note":"مرحبا"}"#,
            "wide characters": #"{"note":"請款單"}"#,
            "an emoji": #"{"note":"ok 👍"}"#,
            "a combining mark": "{\"note\":\"ok\u{301}\"}",
            "at the byte limits": #"{"note":""# + String(repeating: "abcdefgh ", count: 21) + #""}"#,
            "four short lines": "a\nb\nc\nd",
        ]
        for (name, body) in attacks {
            #expect(!AgentNotice.eligible(request(body)), "\(name): offered Allow once")
        }
        #expect(!AgentNotice.eligible(request("{}", target: "/api/apps/note/" + String(repeating: "segment/", count: 13))),
                "a 120-byte path that wraps past the lines shown")
    }

    /// Generated bodies: one that fits is countable and comes to at most
    /// four lines; one that doesn't never fits with anything appended.
    @Test func generatedBodies() {
        var rng = SplitMix(seed: 241)
        let alphabet = Array("abcxyz ABC MW019{}[]\":,./-_ il \n\u{2028}\u{A0}\t\u{5D0}\u{301}\u{3000}👍éß–…".unicodeScalars)
        for _ in 0..<3000 {
            var s = String.UnicodeScalarView()
            for _ in 0..<Int(rng.next() % 100) { s.append(alphabet[Int(rng.next() % UInt64(alphabet.count))]) }
            let body = String(s)
            if LockScreenFit.fits(method: "PUT", target: "/api/apps/note/note", body: body) {
                #expect(!body.contains("  ") && LockScreenFit.lines(method: "PUT", target: "/api/apps/note/note", body: body) <= LockScreenFit.lines)
                continue
            }
            for c in alphabet.prefix(24) {
                #expect(!LockScreenFit.fits(method: "PUT", target: "/api/apps/note/note", body: body + String(c)), "\(body.debugDescription)+\(c)")
            }
        }
    }
}

/// A small deterministic generator (the tests run the same everywhere).
struct SplitMix {
    var state: UInt64
    init(seed: UInt64) { state = seed }
    mutating func next() -> UInt64 {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return z ^ (z >> 31)
    }
}
