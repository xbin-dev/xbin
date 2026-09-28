import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// The agent screen's markdown memo (D130/E3): a streaming reply re-lexes
/// only its tail and still equals the whole parse; the least recently used
/// entries go past the capacity.
@Suite struct MarkdownMemoTests {
    @Test func streamingEqualsWhole() {
        let reply = "### Unit 1\n\nChanging `file1.go`:\n\n- rename the helper\n- keep **behaviour**\n\n```go\nfunc helper1() int { return 1 }\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\nDone."
        let memo = MarkdownMemo()
        var text = ""
        for ch in reply {
            text.append(ch)
            #expect(memo.blocks(id: "m1", text: text) == Markdown.blocks(MarkdownLexer.tokens(text)), "at \(text.debugDescription)")
        }
        #expect(memo.blocks(id: "m1", text: "other") == Markdown.blocks(MarkdownLexer.tokens("other")))
    }

    @Test func evictsTheLeastRecentlyUsed() {
        let memo = MarkdownMemo(capacity: 8)
        for i in 0..<8 { _ = memo.blocks(id: "m\(i)", text: "text \(i)") }
        _ = memo.blocks(id: "m0", text: "text 0") // used again: kept
        _ = memo.blocks(id: "m8", text: "text 8")
        #expect(memo.count <= 8)
        #expect(memo.blocks(id: "m0", text: "text 0") == Markdown.blocks(MarkdownLexer.tokens("text 0")))
    }
}
