// TermSelection.swift — the precise selection mode (plans/native.md §12
// "long-press to select, a precise selection mode"). SwiftTerm's own
// selection is a long-press menu and a double-tap drag; on a phone the pan
// that scrolls the terminal fights it. In precise mode every drag selects, by
// character, word or line, the handles can be grabbed again, and the moving
// end steps by one unit at a time — the model here; the app draws it with
// SwiftTerm's selection (handles included) and owns the gestures.
//
// Rows are the source's own numbering (the app uses SwiftTerm's
// scroll-invariant rows, so a selection stays on its text while output
// scrolls). Ranges are half-open, [start, end): `end` is the cell after the
// last one selected, which is SwiftTerm's convention too; an end at
// `col == cols` means "through the last column" without the newline that
// (row + 1, 0) would add.

import Foundation

/// A cell: `row` in the source's numbering, `col` 0-based.
public struct TermCell: Hashable, Comparable, Sendable, CustomStringConvertible {
    public var row: Int
    public var col: Int
    public init(row: Int, col: Int) { self.row = row; self.col = col }
    public static func < (a: TermCell, b: TermCell) -> Bool { a.row != b.row ? a.row < b.row : a.col < b.col }
    public var description: String { "(\(row),\(col))" }
}

/// One line of the emulator's buffer as the selection reads it.
public struct TermTextLine: Equatable, Sendable {
    /// Exactly `cols` entries: the glyph, " " for a blank or never-written
    /// cell, nil for the second cell of a wide glyph.
    public var cells: [Character?]
    /// This line continues the previous one (a soft wrap, not a newline).
    public var wrapped: Bool
    public init(cells: [Character?], wrapped: Bool = false) { self.cells = cells; self.wrapped = wrapped }

    /// The glyph at `col`: " " out of range, nil on a wide glyph's second cell.
    public func glyph(_ col: Int) -> Character? { cells.indices.contains(col) ? cells[col] : " " }

    /// `col` is the second cell of a wide glyph.
    public func isWideTail(_ col: Int) -> Bool { cells.indices.contains(col) && cells[col] == nil }
}

/// The buffer, scrollback included. Rows `rows` exist; everything else reads
/// as a blank line.
public protocol TermLineSource {
    var rows: Range<Int> { get }
    var cols: Int { get }
    func line(_ row: Int) -> TermTextLine
}

/// An in-memory buffer (tests, previews): each string is one row, laid out
/// by `wcwidth` (a wide glyph takes two cells); `wrapped` lists the rows that
/// continue the previous one.
public struct TermTextBuffer: TermLineSource, Sendable {
    public var cols: Int
    public var lines: [TermTextLine]
    public var rows: Range<Int> { 0..<lines.count }

    public init(_ text: [String], cols: Int, wrapped: Set<Int> = []) {
        self.cols = cols
        lines = text.enumerated().map { i, s in
            var cells: [Character?] = []
            for ch in s {
                let w = ch.unicodeScalars.first.map { wcwidth($0.value) } ?? 1
                if w == 0, !cells.isEmpty { continue } // combining marks ride on the previous cell
                guard cells.count + max(w, 1) <= cols else { break }
                cells.append(ch)
                if w == 2 { cells.append(nil) }
            }
            cells += Array(repeating: " ", count: max(0, cols - cells.count))
            return TermTextLine(cells: cells, wrapped: wrapped.contains(i))
        }
    }

    public func line(_ row: Int) -> TermTextLine {
        rows.contains(row) ? lines[row] : TermTextLine(cells: Array(repeating: " ", count: cols))
    }
}

public enum TermSelectionUnit: String, CaseIterable, Sendable, Codable {
    case character, word, line

    public var label: String {
        switch self {
        case .character: return "Character"
        case .word: return "Word"
        case .line: return "Line"
        }
    }
}

public enum TermSelectionHandle: Equatable, Sendable { case start, end }

public enum TermNudge: Equatable, Sendable { case left, right, up, down }

/// A selected range, half-open.
public struct TermSelectionRange: Equatable, Sendable {
    public var start: TermCell
    public var end: TermCell
    public init(start: TermCell, end: TermCell) { self.start = start; self.end = end }
}

/// The selection being made: a fixed `anchor` and a moving `head` (the end
/// the finger or the nudge buttons move), expanded to `unit`.
public struct TermSelection: Equatable, Sendable {
    public private(set) var anchor: TermCell?
    public private(set) var head: TermCell?
    public var unit: TermSelectionUnit

    public init(unit: TermSelectionUnit = .character) { self.unit = unit }

    public var isEmpty: Bool { anchor == nil }

    public mutating func clear() { anchor = nil; head = nil }

    /// A touch down (or a tap): a new selection of the unit under `cell`.
    public mutating func begin(at cell: TermCell, in src: some TermLineSource) {
        let c = Self.clamp(cell, src)
        anchor = c
        head = c
    }

    /// The finger moved: the head follows it.
    public mutating func extend(to cell: TermCell, in src: some TermLineSource) {
        guard anchor != nil else { return begin(at: cell, in: src) }
        head = Self.clamp(cell, src)
    }

    /// A touch down near a handle takes that handle: the other end becomes
    /// the anchor and the head moves to `cell`. `tolerance` is in cells.
    /// nil (and nothing changes) when neither handle is near.
    public mutating func grab(at cell: TermCell, tolerance: (rows: Int, cols: Int), in src: some TermLineSource) -> TermSelectionHandle? {
        guard let r = range(in: src) else { return nil }
        let last = Self.lastCell(of: r, in: src)
        func distance(_ a: TermCell) -> (Int, Int) { (abs(a.row - cell.row), abs(a.col - cell.col)) }
        let ds = distance(r.start), de = distance(last)
        let nearStart = ds.0 <= tolerance.rows && ds.1 <= tolerance.cols
        let nearEnd = de.0 <= tolerance.rows && de.1 <= tolerance.cols
        guard nearStart || nearEnd else { return nil }
        let sd = ds.0 * 10_000 + ds.1, ed = de.0 * 10_000 + de.1
        let takeStart = nearStart && (!nearEnd || sd < ed || (sd == ed && cell <= r.start))
        let c = Self.clamp(cell, src)
        if takeStart {
            anchor = last
            head = c
            return .start
        }
        anchor = r.start
        head = c
        return .end
    }

    /// Swaps which end moves (the nudge buttons' "other end").
    public mutating func swapEnds(in src: some TermLineSource) {
        guard let r = range(in: src), let a = anchor, let h = head else { return }
        let first = r.start, last = Self.lastCell(of: r, in: src)
        if h >= a { anchor = last; head = first } else { anchor = first; head = last }
    }

    /// Moves the head one unit: a cell (wrapping between rows), a word, or a
    /// line (a soft-wrapped line counts as one).
    public mutating func nudge(_ n: TermNudge, in src: some TermLineSource) {
        guard let h = head else { return }
        let next: TermCell
        switch (unit, n) {
        case (.character, .left): next = Self.stepCell(h, by: -1, in: src)
        case (.character, .right): next = Self.stepCell(h, by: 1, in: src)
        case (.word, .left): next = Self.previousWord(from: h, in: src)
        case (.word, .right): next = Self.nextWord(from: h, in: src)
        case (.line, .left), (.line, .up): next = Self.stepLine(h, by: -1, in: src)
        case (.line, .right), (.line, .down): next = Self.stepLine(h, by: 1, in: src)
        case (_, .up): next = TermCell(row: h.row - 1, col: h.col)
        case (_, .down): next = TermCell(row: h.row + 1, col: h.col)
        }
        head = Self.clamp(next, src)
    }

    /// Everything in the buffer.
    public mutating func selectAll(in src: some TermLineSource) {
        guard !src.rows.isEmpty else { return clear() }
        unit = .character
        anchor = TermCell(row: src.rows.lowerBound, col: 0)
        head = TermCell(row: src.rows.upperBound - 1, col: max(0, src.cols - 1))
    }

    /// Where the handles sit: the start (before its cell) and the end
    /// (after the last selected cell), in cells.
    public func handles(in src: some TermLineSource) -> (start: TermCell, end: TermCell)? {
        range(in: src).map { ($0.start, $0.end) }
    }

    /// The selection, expanded to the unit.
    public func range(in src: some TermLineSource) -> TermSelectionRange? {
        guard let a = anchor, let h = head else { return nil }
        let ea = Self.expand(a, unit, src), eh = Self.expand(h, unit, src)
        return TermSelectionRange(start: min(ea.start, eh.start), end: max(ea.end, eh.end))
    }

    /// The selected text: logical lines joined with "\n" (a soft wrap joins
    /// without one), blanks at the end of a line dropped, wide glyphs once.
    public func text(in src: some TermLineSource) -> String {
        guard let r = range(in: src) else { return "" }
        return Self.text(r, in: src)
    }

    public static func text(_ r: TermSelectionRange, in src: some TermLineSource) -> String {
        guard r.start < r.end else { return "" }
        // [.., (row, 0)) takes the previous row through its newline.
        let lastRow = r.end.col == 0 ? r.end.row - 1 : r.end.row
        var lines: [String] = []
        var cur = ""
        // Blanks at the end of a line's text go (the terminal can't tell a
        // written blank from an empty cell), unless text follows them on the row.
        var restBlank = true
        for row in r.start.row...lastRow {
            let l = src.line(row)
            if row > r.start.row, !l.wrapped { lines.append(restBlank ? trimRight(cur) : cur); cur = "" }
            let from = row == r.start.row ? r.start.col : 0
            let to = row == r.end.row ? min(r.end.col, src.cols) : src.cols
            restBlank = (to..<max(to, src.cols)).allSatisfy { l.glyph($0) == " " }
            guard from < to else { continue }
            for c in from..<to { if let ch = l.glyph(c) { cur.append(ch) } }
        }
        lines.append(restBlank ? trimRight(cur) : cur)
        var s = lines.joined(separator: "\n")
        if r.end.col == 0, !src.line(r.end.row).wrapped { s += "\n" }
        return s
    }

    // MARK: - Cells, words and lines

    /// The class of a character for word selection: blanks, word characters
    /// (letters, digits and GNOME Terminal's `-#%&+,./:=?@_~`, so a path or
    /// URL is one word) and other punctuation, each run of one class a word.
    public enum CharClass: Equatable, Sendable { case blank, word, punct }

    public static func charClass(_ ch: Character) -> CharClass {
        if ch == " " || ch.isWhitespace { return .blank }
        if ch.isLetter || ch.isNumber || "-#%&+,./:=?@_~".contains(ch) { return .word }
        return .punct
    }

    static func clamp(_ c: TermCell, _ src: some TermLineSource) -> TermCell {
        let rows = src.rows
        guard !rows.isEmpty else { return TermCell(row: 0, col: 0) }
        let row = min(max(c.row, rows.lowerBound), rows.upperBound - 1)
        let col = min(max(c.col, 0), max(0, src.cols - 1))
        return snapHead(TermCell(row: row, col: col), src)
    }

    /// A wide glyph's second cell selects the glyph.
    static func snapHead(_ c: TermCell, _ src: some TermLineSource) -> TermCell {
        var c = c
        let l = src.line(c.row)
        while c.col > 0, l.isWideTail(c.col) { c.col -= 1 }
        return c
    }

    /// The cell after `c`: past a wide glyph's second cell; `col == cols` at
    /// the end of a row.
    static func after(_ c: TermCell, _ src: some TermLineSource) -> TermCell {
        var col = c.col + 1
        let l = src.line(c.row)
        while col < src.cols, l.isWideTail(col) { col += 1 }
        return TermCell(row: c.row, col: min(col, src.cols))
    }

    /// The last selected cell of a range.
    static func lastCell(of r: TermSelectionRange, in src: some TermLineSource) -> TermCell {
        if r.end.col > 0 { return snapHead(TermCell(row: r.end.row, col: r.end.col - 1), src) }
        return snapHead(TermCell(row: r.end.row - 1, col: max(0, src.cols - 1)), src)
    }

    static func expand(_ c: TermCell, _ unit: TermSelectionUnit, _ src: some TermLineSource) -> TermSelectionRange {
        switch unit {
        case .character:
            return TermSelectionRange(start: c, end: after(c, src))
        case .word:
            return word(at: c, in: src)
        case .line:
            let (first, last) = logicalLine(c.row, src)
            return TermSelectionRange(start: TermCell(row: first, col: 0), end: TermCell(row: last, col: src.cols))
        }
    }

    /// The first and last rows of the logical line holding `row`.
    static func logicalLine(_ row: Int, _ src: some TermLineSource) -> (Int, Int) {
        var first = row, last = row
        while first > src.rows.lowerBound, src.line(first).wrapped { first -= 1 }
        while last + 1 < src.rows.upperBound, src.line(last + 1).wrapped { last += 1 }
        return (first, last)
    }

    /// The class of the cell (a wide glyph's second cell reads as the glyph).
    static func classAt(_ c: TermCell, _ src: some TermLineSource) -> CharClass {
        let h = snapHead(c, src)
        return charClass(src.line(h.row).glyph(h.col) ?? " ")
    }

    /// The previous cell within a logical line (crossing a soft wrap), or nil.
    static func prevInLine(_ c: TermCell, _ src: some TermLineSource) -> TermCell? {
        if c.col > 0 { return snapHead(TermCell(row: c.row, col: c.col - 1), src) }
        guard c.row > src.rows.lowerBound, src.line(c.row).wrapped else { return nil }
        return snapHead(TermCell(row: c.row - 1, col: src.cols - 1), src)
    }

    /// The next cell within a logical line (crossing a soft wrap), or nil.
    static func nextInLine(_ c: TermCell, _ src: some TermLineSource) -> TermCell? {
        let a = after(c, src)
        if a.col < src.cols { return a }
        guard a.row + 1 < src.rows.upperBound, src.line(a.row + 1).wrapped else { return nil }
        return TermCell(row: a.row + 1, col: 0)
    }

    /// The run of one class around `c` (never crossing a hard newline).
    static func word(at c: TermCell, in src: some TermLineSource) -> TermSelectionRange {
        let c = snapHead(c, src)
        let k = classAt(c, src)
        var start = c
        while let p = prevInLine(start, src), classAt(p, src) == k { start = p }
        var last = c
        while let n = nextInLine(last, src), classAt(n, src) == k { last = n }
        return TermSelectionRange(start: start, end: after(last, src))
    }

    /// One cell left or right, wrapping between rows.
    static func stepCell(_ c: TermCell, by d: Int, in src: some TermLineSource) -> TermCell {
        if d > 0 {
            let a = after(c, src)
            return a.col < src.cols ? a : TermCell(row: a.row + 1, col: 0)
        }
        if c.col > 0 { return snapHead(TermCell(row: c.row, col: c.col - 1), src) }
        return c.row > src.rows.lowerBound ? snapHead(TermCell(row: c.row - 1, col: src.cols - 1), src) : c
    }

    /// The first cell of the next word that isn't blank (the next row's first
    /// when this one has none left).
    static func nextWord(from c: TermCell, in src: some TermLineSource) -> TermCell {
        var cur = word(at: c, in: src).end
        var guardRows = 0
        while cur.row < src.rows.upperBound, guardRows < 1000 {
            if cur.col >= src.cols {
                cur = TermCell(row: cur.row + 1, col: 0)
                guardRows += 1
                continue
            }
            if classAt(cur, src) != .blank { return cur }
            cur = after(cur, src)
        }
        return c
    }

    /// The first cell of the previous word that isn't blank.
    static func previousWord(from c: TermCell, in src: some TermLineSource) -> TermCell {
        var cur = word(at: c, in: src).start
        var guardSteps = 0
        while guardSteps < 100_000 {
            guardSteps += 1
            let p = stepCell(cur, by: -1, in: src)
            if p == cur { return cur }
            cur = p
            if classAt(cur, src) != .blank { return word(at: cur, in: src).start }
        }
        return c
    }

    /// The first row of the previous or next logical line, keeping the column.
    static func stepLine(_ c: TermCell, by d: Int, in src: some TermLineSource) -> TermCell {
        let (first, last) = logicalLine(c.row, src)
        if d < 0 {
            guard first > src.rows.lowerBound else { return c }
            return TermCell(row: logicalLine(first - 1, src).0, col: c.col)
        }
        guard last + 1 < src.rows.upperBound else { return c }
        return TermCell(row: last + 1, col: c.col)
    }

    static func trimRight(_ s: String) -> String {
        var s = s
        while let l = s.last, l == " " { s.removeLast() }
        return s
    }
}

