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

    /// A streaming reply re-lexes only its tail, and every step equals the
    /// whole text's tokens: the corpus and random junk, grown a few
    /// characters at a time (and whole lines at a time).
    @Test func incrementalEqualsWhole() throws {
        let cases = try #require(try Resources.json("markdown/expected.json").arrayValue)
        var srcs = cases.compactMap { $0["src"]?.stringValue }
        srcs.append(srcs.joined(separator: "\n\n"))
        var r = SeededRNG(seed: 11)
        let pieces = ["*", "**", "_", "`", "``", "[", "]", "(", ")", "<", ">", "\n", "\n\n", "- ", "1. ", "> ", "#", "# ", "|", "---",
                      "```", "~~~", "    ", "a", "b c", " ", "https://x.y", "\t", "| a | b |\n|---|---|\n", "===", "- [ ] "]
        for _ in 0..<300 {
            srcs.append((0..<Int.random(in: 1...60, using: &r)).map { _ in pieces.randomElement(using: &r)! }.joined())
        }
        var mismatches: [String] = []
        var reused = 0
        for src in srcs where !src.contains("\r") {
            var inc = MarkdownLexer.Incremental()
            let chars = Array(src)
            var n = 0
            while n < chars.count {
                n = min(chars.count, n + Int.random(in: 1...7, using: &r))
                let prefix = String(chars[..<n])
                let got = inc.update(prefix)
                reused += inc.kept
                if JSONValue.array(got) != MarkdownLexer.tokens(prefix) {
                    mismatches.append("\(prefix.debugDescription)\n  whole \(MarkdownLexer.tokens(prefix).jsonString)\n  inc   \(JSONValue.array(got).jsonString)")
                    break
                }
            }
        }
        #expect(mismatches.isEmpty, "\(mismatches.count) differ:\n\(mismatches.prefix(5).joined(separator: "\n"))")
        #expect(reused > 1000, "the tail only: \(reused) tokens kept")
        // a text that is not an extension starts over
        var inc = MarkdownLexer.Incremental()
        _ = inc.update("# a\n\nb")
        #expect(JSONValue.array(inc.update("c")) == MarkdownLexer.tokens("c") && inc.kept == 0)
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
