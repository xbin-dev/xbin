import Foundation
import Testing
import SwiftTerm
import XbinTerm

/// The precise selection on a real SwiftTerm buffer (App/Terminal/
/// SwiftTermLines.swift): the model reads SwiftTerm's lines — soft wraps,
/// wide glyphs, trimmed scrollback — and the text it copies is exactly what
/// SwiftTerm's own Copy gives for the same range once `show` hands it over.
@Suite struct SwiftTermSelectionTests {
    func terminal(_ cols: Int, _ rows: Int, scrollback: Int = 100) -> Terminal {
        Terminal(delegate: Headless(), options: TerminalOptions(cols: cols, rows: rows, scrollback: scrollback))
    }

    func cell(_ r: Int, _ c: Int) -> TermCell { TermCell(row: r, col: c) }

    /// SwiftTerm's copy of `range` (what its Copy menu puts on the pasteboard).
    func swiftTermText(_ range: TermSelectionRange, _ t: Terminal, _ lines: SwiftTermLines) -> String {
        let sel = SelectionService(terminal: t)
        sel.show(range, in: lines)
        #expect(sel.active)
        return sel.getSelectedText()
    }

    @Test func readsTheBuffer() {
        let t = terminal(10, 3)
        t.feed(text: "hello worl\r\n0123456789abc\r\n中x")
        let lines = SwiftTermLines(terminal: t)
        #expect(lines.rows == 0..<4 && lines.cols == 10 && lines.trimmed == 0)
        #expect(lines.line(0).cells.compactMap { $0 }.map(String.init).joined() == "hello worl")
        #expect(!lines.line(1).wrapped && lines.line(2).wrapped, "the 13 characters wrapped")
        #expect(String(lines.line(2).cells.prefix(3).compactMap { $0 }) == "abc")
        let wide = lines.line(3)
        #expect(wide.cells[0] == "中" && wide.cells[1] == nil && wide.cells[2] == "x" && wide.cells[3] == " ")
        #expect(lines.line(99).cells.count == 10 && !lines.line(99).wrapped)
        var s = TermSelection(unit: .word)
        s.begin(at: cell(2, 1), in: lines)
        #expect(s.text(in: lines) == "0123456789abc", "a word across the soft wrap")
    }

    @Test func trimmedScrollbackKeepsRows() {
        let t = terminal(12, 3, scrollback: 5)
        for i in 0..<30 { t.feed(text: "line \(i)\r\n") }
        let lines = SwiftTermLines(terminal: t)
        #expect(lines.trimmed > 0 && lines.rows.lowerBound == lines.trimmed)
        #expect(lines.rows.count == 8, "5 of scrollback + 3 on screen")
        let first = String(lines.line(lines.rows.lowerBound).cells.compactMap { $0 }).trimmingCharacters(in: .whitespaces)
        #expect(first == "line 23")
        #expect(lines.position(cell(lines.trimmed + 2, 4)) == Position(col: 4, row: 2))
        #expect(lines.cell(bufferRow: 2, col: 4) == cell(lines.trimmed + 2, 4))
        var s = TermSelection(unit: .line)
        s.begin(at: cell(lines.rows.lowerBound, 0), in: lines)
        #expect(s.text(in: lines) == "line 23")
        #expect(swiftTermText(s.range(in: lines)!, t, lines) == "line 23")
    }

    @Test func copiesWhatSwiftTermCopies() {
        let t = terminal(20, 6)
        t.feed(text: "$ ls -la /tmp\r\ntotal 8\r\ndrwxr-xr-x 2 me me 4096 foo.txt\r\n中文 text\r\nlast line here")
        let lines = SwiftTermLines(terminal: t)
        let cases: [(TermSelectionUnit, TermCell, TermCell)] = [
            (.character, cell(0, 2), cell(0, 7)),       // "ls -la"
            (.character, cell(0, 9), cell(0, 12)),      // "/tmp", then blanks to the row's end
            (.character, cell(0, 2), cell(1, 4)),       // across a newline
            (.character, cell(2, 15), cell(3, 3)),      // across the soft wrap
            (.character, cell(4, 1), cell(4, 3)),       // wide glyphs
            (.word, cell(0, 10), cell(0, 10)),
            (.word, cell(2, 19), cell(3, 6)),
            (.line, cell(3, 0), cell(3, 0)),            // a wrapped line through its last column
            (.line, cell(1, 0), cell(5, 0)),
        ]
        for (unit, a, h) in cases {
            var s = TermSelection(unit: unit)
            s.begin(at: a, in: lines)
            s.extend(to: h, in: lines)
            let r = s.range(in: lines)!
            #expect(s.text(in: lines) == swiftTermText(r, t, lines), "\(unit) \(a)–\(h)")
        }
        // the last column, which setSelection would drop
        var last = TermSelection()
        last.begin(at: cell(2, 17), in: lines)
        last.extend(to: cell(2, 19), in: lines)
        #expect(swiftTermText(last.range(in: lines)!, t, lines) == "e 4")
    }
}
