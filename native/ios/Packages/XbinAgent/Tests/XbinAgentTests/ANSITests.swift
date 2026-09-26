import Foundation
import Testing
@testable import XbinAgent

@Suite struct ANSITests {
    let red: TextStyle = { var s = TextStyle(); s.bold = true; s.foreground = .palette(1); return s }()
    let green: TextStyle = { var s = TextStyle(); s.foreground = .palette(2); return s }()

    @Test func sgrBecomesSpans() {
        // the shell fixture's output, verbatim
        let a = ANSIText.parse("\u{1B}[1;31merror\u{1B}[0m: \u{1B}[32mok\u{1B}[0m\r\nline2\r\n")
        #expect(a.plain == "error: ok\nline2\n")
        #expect(a.spans == [StyledSpan("error", red), StyledSpan(": "), StyledSpan("ok", green), StyledSpan("\nline2\n")])
    }

    @Test func extendedColours() {
        let a = ANSIText.parse("\u{1B}[38;5;208mA\u{1B}[48;2;1;2;3mB\u{1B}[38:2::10:20:30mC\u{1B}[39;49;93mD\u{1B}[22;2mE")
        #expect(a.spans.map(\.text) == ["A", "B", "C", "D", "E"])
        #expect(a.spans[0].style.foreground == .palette(208))
        #expect(a.spans[1].style.background == .rgb(1, 2, 3))
        #expect(a.spans[2].style.foreground == .rgb(10, 20, 30) && a.spans[2].style.background == .rgb(1, 2, 3))
        #expect(a.spans[3].style.foreground == .palette(11) && a.spans[3].style.background == nil)
        #expect(a.spans[4].style.dim)
    }

    @Test func carriageReturnOverwritesTheLine() {
        #expect(ANSIText.parse("10%\r50%\r100%\ndone").plain == "100%\ndone")
        #expect(ANSIText.parse("keep\r\nthis").plain == "keep\nthis")
        #expect(ANSIText.parse("trailing\r").plain == "trailing") // nothing overwrote it
        #expect(ANSIText.parse("ab\u{8}c").plain == "ac")
    }

    @Test func otherEscapesAreDropped() {
        #expect(ANSIText.parse("\u{1B}]0;title\u{7}text").plain == "text")
        #expect(ANSIText.parse("\u{1B}]8;;http://x\u{1B}\\link\u{1B}]8;;\u{1B}\\").plain == "link")
        #expect(ANSIText.parse("\u{1B}[2K\u{1B}[1Gx\u{1B}[?25l\u{1B}(By\u{1B}=\u{7}").plain == "xy")
        #expect(ANSIText.parse("tab\there").plain == "tab\there")
    }

    @Test func tails() {
        let a = ANSIText.parse("\u{1B}[31mone\ntwo\u{1B}[0m\nthree\nfour\n")
        #expect(a.suffix(lines: 2).plain == "three\nfour\n")
        #expect(a.suffix(lines: 3).spans.first?.style.foreground == .palette(1)) // "two" keeps its colour
        #expect(a.suffix(lines: 3).plain == "two\nthree\nfour\n")
        #expect(a.suffix(5).plain == "four\n")
        #expect(a.suffix(lines: 10).plain == a.plain)
    }

    @Test func outputView() {
        let long = (1...100).map { "line \($0)" }.joined(separator: "\n") + "\n"
        let v = OutputView(raw: long, maxCharacters: 20_000, maxLines: 10)
        #expect(v.hiddenLines == 90)
        #expect(v.tail.plain.hasPrefix("line 91\n"))
        #expect(v.isTruncated)
        #expect(v.full.plain == long)
        let c = OutputView(raw: String(repeating: "x", count: 30_000))
        #expect(c.hiddenCharacters == 10_000 && c.tail.plain.count == 20_000)
        #expect(OutputView(raw: " \n\u{1B}[0m").isBlank)
        #expect(!OutputView(raw: "short").isTruncated)
    }
}
