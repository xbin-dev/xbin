// TermSelectionTests.swift — the precise selection mode's model: drags by
// character, word and line, handles grabbed again, nudges, soft wraps, wide
// glyphs and the copied text.

import Foundation
import Testing
@testable import XbinTerm

private func cell(_ r: Int, _ c: Int) -> TermCell { TermCell(row: r, col: c) }

@Suite("precise selection")
struct TermSelectionTests {
    // 20 columns; row 2 soft-wraps into row 3.
    let buf = TermTextBuffer([
        "$ ls -la /tmp",        // 0
        "total 8",              // 1
        "drwxr-xr-x 2 me me 4",  // 2 (full width: 20)
        "096 foo.txt",          // 3 (wrapped)
        "中文 text",             // 4 wide glyphs
        "",                     // 5
    ], cols: 20, wrapped: [3])

    @Test("character: the cells between the two ends, both included")
    func characters() {
        var s = TermSelection()
        #expect(s.isEmpty && s.range(in: buf) == nil && s.text(in: buf) == "")
        s.begin(at: cell(0, 2), in: buf)
        #expect(s.range(in: buf) == TermSelectionRange(start: cell(0, 2), end: cell(0, 3)))
        #expect(s.text(in: buf) == "l")
        s.extend(to: cell(0, 4), in: buf)
        #expect(s.text(in: buf) == "ls ")
        s.extend(to: cell(0, 0), in: buf)       // backwards past the anchor
        #expect(s.range(in: buf) == TermSelectionRange(start: cell(0, 0), end: cell(0, 3)))
        #expect(s.text(in: buf) == "$ l")
        s.extend(to: cell(1, 4), in: buf)       // across rows: a newline, trailing blanks dropped
        #expect(s.text(in: buf) == "ls -la /tmp\ntotal")
        s.extend(to: cell(1, 5), in: buf)       // a blank with text after it on the row stays
        #expect(s.text(in: buf) == "ls -la /tmp\ntotal ")
    }

    @Test("clamped to the buffer; the last column is reachable without a newline")
    func clamping() {
        var s = TermSelection()
        s.begin(at: cell(-3, -1), in: buf)
        #expect(s.anchor == cell(0, 0))
        s.extend(to: cell(99, 99), in: buf)
        #expect(s.head == cell(5, 19))
        var t = TermSelection()
        t.begin(at: cell(2, 17), in: buf)
        t.extend(to: cell(2, 19), in: buf)
        #expect(t.range(in: buf)?.end == cell(2, 20))
        #expect(t.text(in: buf) == "e 4")
    }

    @Test("word: runs of one class; paths, flags and URLs are one word; a soft wrap doesn't split one")
    func words() {
        var s = TermSelection(unit: .word)
        s.begin(at: cell(0, 10), in: buf)       // inside "/tmp"
        #expect(s.text(in: buf) == "/tmp")
        s.begin(at: cell(0, 6), in: buf)        // "-la"
        #expect(s.text(in: buf) == "-la")
        s.begin(at: cell(0, 1), in: buf)        // the blank between "$" and "ls"
        #expect(s.range(in: buf) == TermSelectionRange(start: cell(0, 1), end: cell(0, 2)))
        s.begin(at: cell(2, 19), in: buf)       // "4" continues on the next row as "096"
        #expect(s.text(in: buf) == "4096")
        #expect(s.range(in: buf) == TermSelectionRange(start: cell(2, 19), end: cell(3, 3)))
        s.extend(to: cell(3, 6), in: buf)       // drag onto "foo.txt": both words, joined
        #expect(s.text(in: buf) == "4096 foo.txt")
        #expect(TermSelection.charClass("(") == .punct && TermSelection.charClass("é") == .word && TermSelection.charClass("\t") == .blank)
    }

    @Test("wide glyphs: either cell selects the glyph, copied once")
    func wide() {
        var s = TermSelection()
        s.begin(at: cell(4, 1), in: buf)        // the second cell of 中
        #expect(s.anchor == cell(4, 0))
        #expect(s.range(in: buf) == TermSelectionRange(start: cell(4, 0), end: cell(4, 2)))
        #expect(s.text(in: buf) == "中")
        s.extend(to: cell(4, 3), in: buf)
        #expect(s.text(in: buf) == "中文")
        var w = TermSelection(unit: .word)
        w.begin(at: cell(4, 3), in: buf)
        #expect(w.text(in: buf) == "中文")
    }

    @Test("line: whole logical lines, soft wraps included, the last column too")
    func lines() {
        var s = TermSelection(unit: .line)
        s.begin(at: cell(3, 5), in: buf)
        #expect(s.range(in: buf) == TermSelectionRange(start: cell(2, 0), end: cell(3, 20)))
        #expect(s.text(in: buf) == "drwxr-xr-x 2 me me 4096 foo.txt")
        s.extend(to: cell(0, 0), in: buf)
        #expect(s.text(in: buf) == "$ ls -la /tmp\ntotal 8\ndrwxr-xr-x 2 me me 4096 foo.txt")
    }

    @Test("a handle can be grabbed again: the other end holds")
    func handles() {
        var s = TermSelection()
        s.begin(at: cell(0, 2), in: buf)
        s.extend(to: cell(0, 7), in: buf)       // "ls -la"
        #expect(s.handles(in: buf).map { [$0.start, $0.end] } == [cell(0, 2), cell(0, 8)])
        // the end handle (the last cell is (0,7)), dragged right
        #expect(s.grab(at: cell(0, 8), tolerance: (1, 1), in: buf) == .end)
        s.extend(to: cell(0, 12), in: buf)
        #expect(s.text(in: buf) == "ls -la /tmp")
        // the start handle, dragged left
        #expect(s.grab(at: cell(0, 1), tolerance: (1, 1), in: buf) == .start)
        s.extend(to: cell(0, 0), in: buf)
        #expect(s.text(in: buf) == "$ ls -la /tmp")
        // far from both: nothing grabbed, nothing changed
        let before = s
        #expect(s.grab(at: cell(1, 5), tolerance: (0, 1), in: buf) == nil)
        #expect(s == before)
        #expect(TermSelection().handles(in: buf) == nil)
    }

    @Test("a one-cell selection: grabbing picks the end past it, the start before it")
    func grabTies() {
        var s = TermSelection()
        s.begin(at: cell(1, 3), in: buf)
        var a = s
        #expect(a.grab(at: cell(1, 2), tolerance: (0, 1), in: buf) == .start)
        a.extend(to: cell(1, 0), in: buf)
        #expect(a.text(in: buf) == "tota")
        var b = s
        #expect(b.grab(at: cell(1, 4), tolerance: (0, 1), in: buf) == .end)
        b.extend(to: cell(1, 6), in: buf)
        #expect(b.text(in: buf) == "al 8")
    }

    @Test("nudges move the head one unit: cells wrap between rows, words skip blanks, lines skip soft wraps")
    func nudges() {
        var s = TermSelection()
        s.begin(at: cell(0, 10), in: buf)
        s.nudge(.right, in: buf); s.nudge(.right, in: buf)
        #expect(s.text(in: buf) == "tmp")
        s.nudge(.left, in: buf)
        #expect(s.text(in: buf) == "tm")
        s.nudge(.down, in: buf)
        #expect(s.head == cell(1, 11))
        s.nudge(.up, in: buf); s.nudge(.up, in: buf)
        #expect(s.head == cell(0, 11), "clamped at the top")
        // cell steps wrap: the end of row 1 → the start of row 2
        var c = TermSelection()
        c.begin(at: cell(1, 19), in: buf)
        c.nudge(.right, in: buf)
        #expect(c.head == cell(2, 0))
        c.nudge(.left, in: buf)
        #expect(c.head == cell(1, 19))
        var z = TermSelection()
        z.begin(at: cell(0, 0), in: buf)
        z.nudge(.left, in: buf); z.nudge(.up, in: buf)
        #expect(z.head == cell(0, 0))
        // a wide glyph is one step
        var g = TermSelection()
        g.begin(at: cell(4, 0), in: buf)
        g.nudge(.right, in: buf)
        #expect(g.head == cell(4, 2))
        g.nudge(.left, in: buf)
        #expect(g.head == cell(4, 0))

        var w = TermSelection(unit: .word)
        w.begin(at: cell(0, 2), in: buf)        // "ls"
        w.nudge(.right, in: buf)
        #expect(w.text(in: buf) == "ls -la")
        w.nudge(.right, in: buf)
        #expect(w.text(in: buf) == "ls -la /tmp")
        w.nudge(.right, in: buf)                // nothing left on row 0: the next row's first word
        #expect(w.head == cell(1, 0))
        w.nudge(.left, in: buf)
        #expect(w.head == cell(0, 9) && w.text(in: buf) == "ls -la /tmp")
        w.nudge(.left, in: buf); w.nudge(.left, in: buf)
        #expect(w.text(in: buf) == "ls")
        w.nudge(.left, in: buf)                 // past the anchor: "$ ls"
        #expect(w.text(in: buf) == "$ ls")

        var l = TermSelection(unit: .line)
        l.begin(at: cell(1, 0), in: buf)
        l.nudge(.down, in: buf)                 // rows 2–3 are one line
        #expect(l.range(in: buf)?.end == cell(3, 20))
        l.nudge(.down, in: buf)
        #expect(l.head?.row == 4)
        l.nudge(.up, in: buf)
        #expect(l.head?.row == 2)
        l.nudge(.left, in: buf)
        #expect(l.head?.row == 1 && l.text(in: buf) == "total 8")
    }

    @Test("swapping ends moves the other one")
    func swap() {
        var s = TermSelection()
        s.begin(at: cell(0, 2), in: buf)
        s.extend(to: cell(0, 7), in: buf)
        s.swapEnds(in: buf)
        #expect(s.head == cell(0, 2) && s.anchor == cell(0, 7))
        s.nudge(.left, in: buf)
        #expect(s.text(in: buf) == " ls -la")
        s.swapEnds(in: buf)
        s.nudge(.right, in: buf)
        #expect(s.text(in: buf) == " ls -la ")
    }

    @Test("select all, and a range ending at a row start takes that newline")
    func selectAllAndNewline() {
        var s = TermSelection(unit: .word)
        s.selectAll(in: buf)
        #expect(s.unit == .character)
        #expect(s.text(in: buf) == "$ ls -la /tmp\ntotal 8\ndrwxr-xr-x 2 me me 4096 foo.txt\n中文 text\n")
        let r = TermSelectionRange(start: cell(0, 9), end: cell(1, 0))
        #expect(TermSelection.text(r, in: buf) == "/tmp\n")
        #expect(TermSelection.text(TermSelectionRange(start: cell(1, 0), end: cell(1, 0)), in: buf) == "")
        var e = TermSelection()
        e.selectAll(in: TermTextBuffer([], cols: 10))
        #expect(e.isEmpty)
    }

    @Test("rows need not start at 0 (SwiftTerm's scroll-invariant rows)")
    func offsetRows() {
        struct Shifted: TermLineSource {
            let inner: TermTextBuffer
            let offset: Int
            var rows: Range<Int> { (inner.rows.lowerBound + offset)..<(inner.rows.upperBound + offset) }
            var cols: Int { inner.cols }
            func line(_ row: Int) -> TermTextLine { inner.line(row - offset) }
        }
        let src = Shifted(inner: buf, offset: 500)
        var s = TermSelection(unit: .line)
        s.begin(at: cell(0, 0), in: src)
        #expect(s.anchor == cell(500, 0))
        #expect(s.text(in: src) == "$ ls -la /tmp")
        s.nudge(.up, in: src)
        #expect(s.head == cell(500, 0))
        s.extend(to: cell(503, 1), in: src)
        #expect(s.text(in: src) == "$ ls -la /tmp\ntotal 8\ndrwxr-xr-x 2 me me 4096 foo.txt")
    }
}
