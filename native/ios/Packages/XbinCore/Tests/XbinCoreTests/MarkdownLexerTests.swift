import Foundation
import Testing
@testable import XbinCore

/// MarkdownLexer against the runtime's own lexer (web/xb/rt-markdown.js over
/// marked): Resources/markdown/expected.json, regenerated with
/// `node native/tools/markdown-parity.mjs`.
@Suite struct MarkdownLexerTests {
    @Test func matchesTheRuntimesLexer() throws {
        let cases = try #require(try Resources.json("markdown/expected.json").arrayValue)
        #expect(cases.count >= 40)
        var mismatches: [String] = []
        for c in cases {
            let src = c["src"]?.stringValue ?? ""
            let want = c["tokens"] ?? .array([])
            let got = MarkdownLexer.tokens(src)
            if got != want {
                mismatches.append("\(src.debugDescription)\n  want \(want.jsonString)\n  got  \(got.jsonString)")
            }
        }
        #expect(mismatches.isEmpty, "\(mismatches.count) of \(cases.count) differ:\n\(mismatches.joined(separator: "\n"))")
    }

    @Test func linksArePolicied() {
        let t = MarkdownLexer.inline("[x](javascript:alert(1)) [y](mailto:a@b.c)")
        #expect(t.first == ["t": "text", "text": "x "])
        #expect(t.last?["t"] == "link" && t.last?["href"] == "mailto:a@b.c")
    }

    @Test func neverCrashesOnJunk() {
        var r = SeededRNG(seed: 7)
        let pieces = ["*", "**", "_", "`", "``", "[", "]", "(", ")", "<", ">", "\n", "\n\n", "- ", "1. ", "> ", "#", "|", "---",
                      "```", "~", "&", ";", "\\", "a", " ", "https://x.y", "\t"]
        for _ in 0..<2000 {
            let s = (0..<Int.random(in: 0...30, using: &r)).map { _ in pieces.randomElement(using: &r)! }.joined()
            _ = MarkdownLexer.tokens(s)
        }
    }
}
