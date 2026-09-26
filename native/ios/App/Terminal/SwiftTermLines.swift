import Foundation
import SwiftTerm
import XbinTerm

/// SwiftTerm's buffer, scrollback included, as the precise selection reads it
/// (XbinTerm's `TermLineSource`). Rows are SwiftTerm's scroll-invariant rows
/// (`getScrollInvariantLine`): a selection keeps its text while output scrolls
/// and the scrollback's oldest lines are trimmed. SwiftTerm's own coordinates
/// (`SelectionService`, `TerminalView`) are buffer rows: invariant − trimmed.
/// UIKit-free: also compiled by native/tools/app-check against a headless
/// SwiftTerm on Linux, where the copied text is checked against SwiftTerm's.
struct SwiftTermLines: TermLineSource {
    let terminal: Terminal
    /// Lines trimmed off the top of the scrollback so far.
    let trimmed: Int
    let rows: Range<Int>
    var cols: Int { terminal.cols }

    init(terminal: Terminal) {
        self.terminal = terminal
        let trimmed = terminal.buffer.totalLinesTrimmed
        self.trimmed = trimmed
        // The buffer's line count isn't public: the rows that exist run from
        // `trimmed` up to the first one `getScrollInvariantLine` refuses.
        func exists(_ r: Int) -> Bool { terminal.getScrollInvariantLine(row: r) != nil }
        var lo = trimmed + terminal.buffer.yDisp
        guard exists(lo) else {
            rows = trimmed..<trimmed
            return
        }
        var step = max(1, terminal.rows)
        var hi = lo + step
        while exists(hi) { lo = hi; step *= 2; hi = lo + step }
        while hi - lo > 1 {
            let mid = lo + (hi - lo) / 2
            if exists(mid) { lo = mid } else { hi = mid }
        }
        rows = trimmed..<(lo + 1)
    }

    func line(_ row: Int) -> TermTextLine {
        let cols = self.cols
        guard let l = terminal.getScrollInvariantLine(row: row) else {
            return TermTextLine(cells: Array(repeating: " ", count: cols))
        }
        var cells: [Character?] = []
        cells.reserveCapacity(cols)
        for c in 0..<cols {
            guard c < l.count else { cells.append(" "); continue }
            let cd = l[c]
            if cd.width == 0 { cells.append(nil); continue } // a wide glyph's second cell
            let ch = terminal.getCharacter(for: cd)
            cells.append(ch == "\u{0}" ? " " : ch)
        }
        return TermTextLine(cells: cells, wrapped: l.isWrapped)
    }

    /// A model cell in SwiftTerm's buffer coordinates.
    func position(_ c: TermCell) -> Position { Position(col: c.col, row: c.row - trimmed) }

    /// The cell at a buffer row (what `TerminalView`'s content coordinates give).
    func cell(bufferRow: Int, col: Int) -> TermCell { TermCell(row: bufferRow + trimmed, col: col) }
}

extension SelectionService {
    /// Shows `range` as SwiftTerm's selection, which the view draws with its
    /// handles and its Copy menu copies. `setSelection` would clamp an end at
    /// `col == cols` to the last column and drop that column; `startSelection`
    /// + `dragExtend` (character mode) keep it.
    func show(_ range: TermSelectionRange, in lines: SwiftTermLines) {
        let s = lines.position(range.start), e = lines.position(range.end)
        // startSelection(row:col:) is screen-relative (it adds yDisp).
        startSelection(row: s.row - lines.terminal.buffer.yDisp, col: s.col)
        dragExtend(bufferPosition: e)
    }
}
