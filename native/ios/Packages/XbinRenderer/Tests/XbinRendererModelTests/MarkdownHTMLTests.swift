import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// Markdown copied as rich text (#34): the HTML keeps what a person sees —
/// headings, list numbers, tables, links — escapes everything, and the
/// plain text keeps the numbers and the table's columns.
@Suite struct MarkdownHTMLTests {
    @Test func listsKeepTheirNumbersAndTablesTheirCells() {
        let md = """
        ## Steps

        3. Rotate the **password**
        4. Restart `xbind`

        | Name | Owner |
        |:-----|------:|
        | db | Chloé |

        See [the docs](https://xbin.dev/docs) — not [this](javascript:alert(1)).
        """
        let r = Markdown.pasteboard(markdown: md)
        #expect(r.html.hasPrefix("<meta charset=\"utf-8\">"))
        #expect(r.html.contains("<h2>Steps</h2>"))
        #expect(r.html.contains("<ol start=\"3\"><li>Rotate the <strong>password</strong></li><li>Restart <code>xbind</code></li></ol>"))
        #expect(r.html.contains("<table><thead><tr><th style=\"text-align:left\">Name</th><th style=\"text-align:right\">Owner</th></tr></thead><tbody><tr><td style=\"text-align:left\">db</td><td style=\"text-align:right\">Chloé</td></tr></tbody></table>"))
        #expect(r.html.contains("<a href=\"https://xbin.dev/docs\">the docs</a>"))
        #expect(!r.html.contains("javascript"), "a link the screen wouldn't open is only its text")
        #expect(r.plain.contains("3. Rotate the password\n4. Restart xbind"))
        #expect(r.plain.contains("Name\tOwner\ndb\tChloé"))
    }

    @Test func everythingIsEscaped() {
        let r = Markdown.pasteboard(markdown: "a <script>alert(\"x\")</script> & 'b'\n\n```html\n<b>bold</b>\n```")
        #expect(!r.html.contains("<script>"))
        #expect(!r.html.contains("<b>"))
        #expect(r.html.contains("<pre><code>&lt;b&gt;bold&lt;/b&gt;</code></pre>"))
        #expect(r.html.contains("&amp; &#39;b&#39;"))
    }

    @Test func tasksQuotesAndRules() {
        let r = Markdown.pasteboard(markdown: "- [x] done\n- [ ] next\n\n> quoted\n\n---")
        #expect(r.html.contains("<ul><li>☑ done</li><li>☐ next</li></ul>"))
        #expect(r.html.contains("<blockquote><p>quoted</p></blockquote>"))
        #expect(r.html.hasSuffix("<hr>"))
    }
}
