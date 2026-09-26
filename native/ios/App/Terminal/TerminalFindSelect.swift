import SwiftTerm
import UIKit
import XbinTerm

// The terminal's scrollback search (⌘F, the find bar) and precise selection
// mode (plans/native.md §12). The decisions are XbinTerm's (TermFind,
// TermSelection); SwiftTerm searches, draws the selection with its handles,
// and scrolls. The gestures are SelectionOverlayView's, the bars
// TerminalBars.swift's.

extension TerminalController {
    // MARK: Scrollback search (⌘F)

    /// Opens the find bar (or focuses it again); leaves the selection mode.
    func openFind() {
        if selecting { exitSelectionMode(showKeyboard: false) }
        find.open()
        findFocusRequests += 1
        syncSelectionOwner()
    }

    func closeFind() {
        find.close()
        terminalView.clearSearch()
        syncSelectionOwner()
        showKeyboard()
    }

    /// The query or an option changed: search again as the user types,
    /// from the newest output up (SwiftTerm keeps the current match while it
    /// still matches).
    func findChanged() {
        guard find.canSearch else {
            terminalView.clearSearch()
            return
        }
        findStep(.older)
    }

    /// A match up (older) or down (newer) the scrollback — selected and
    /// scrolled into view by SwiftTerm — then the "3 of 14" counter.
    func findStep(_ a: TermFindAction) {
        switch a {
        case .close:
            closeFind()
        case .older, .newer:
            guard find.canSearch else { return }
            let q = find.query
            let o = SwiftTerm.SearchOptions(caseSensitive: find.options.caseSensitive, regex: find.options.regex,
                                            wholeWord: find.options.wholeWord)
            let found = a == .older ? terminalView.findPrevious(q, options: o) : terminalView.findNext(q, options: o)
            let summary = terminalView.searchMatchSummary(q, options: o, limit: TermFind.countLimit)
            find.record(found: found, index: summary.index, total: summary.total)
        }
    }

    /// While the find bar or the selection mode shows a selection, output
    /// must not clear it: SwiftTerm drops the selection on every feed while
    /// it may report taps to mouse-aware programs (tmux, vim). So taps aren't
    /// reported meanwhile (in the selection mode they never reach SwiftTerm).
    func syncSelectionOwner() {
        terminalView.allowMouseReporting = !(find.visible || selecting)
    }

    // MARK: Precise selection

    /// SwiftTerm's buffer as the selection model reads it.
    var lines: SwiftTermLines { SwiftTermLines(terminal: terminalView.getTerminal()) }

    /// One cell in points (SwiftTerm lays the grid out at its optimal frame).
    var cellSize: CGSize {
        let t = terminalView.getTerminal()
        let f = terminalView.getOptimalFrameSize()
        guard t.cols > 0, t.rows > 0, f.width > 0, f.height > 0 else { return CGSize(width: 8, height: 16) }
        return CGSize(width: f.width / CGFloat(t.cols), height: f.height / CGFloat(t.rows))
    }

    /// The cell at a point in the terminal view's content coordinates (row 0
    /// is the top of the scrollback, as in SwiftTerm's own hit test).
    func cell(atContentPoint p: CGPoint) -> TermCell {
        let c = cellSize
        return lines.cell(bufferRow: Int((p.y / c.height).rounded(.down)), col: Int((p.x / c.width).rounded(.down)))
    }

    func enterSelectionMode() {
        if find.visible {
            find.close()
            terminalView.clearSearch()
        }
        selecting = true
        selection = TermSelection(unit: selection.unit)
        terminalView.clearSelection()
        selectionOverlay.isHidden = false
        _ = terminalView.resignFirstResponder()
        syncSelectionOwner()
    }

    func exitSelectionMode(showKeyboard again: Bool = true) {
        selectionOverlay.endInteraction()
        selectionOverlay.isHidden = true
        selecting = false
        selection.clear()
        terminalView.clearSelection()
        syncSelectionOwner()
        if again { showKeyboard() }
    }

    /// Draws the model's selection with SwiftTerm's (highlight and handles).
    func showSelection() {
        let l = lines
        if let r = selection.range(in: l) { terminalView.selection.show(r, in: l) } else { terminalView.clearSelection() }
    }

    /// A touch down on the selection layer: a handle near it is taken,
    /// otherwise a new selection starts. `tolerance` is in cells.
    func selectionBegan(at cell: TermCell, tolerance: (rows: Int, cols: Int)) {
        let l = lines
        if selection.grab(at: cell, tolerance: tolerance, in: l) == nil { selection.begin(at: cell, in: l) }
        showSelection()
    }

    func selectionMoved(to cell: TermCell) {
        selection.extend(to: cell, in: lines)
        showSelection()
    }

    /// A gesture turned out to be something else (two fingers: a scroll).
    func restoreSelection(_ s: TermSelection) {
        selection = s
        showSelection()
    }

    func nudgeSelection(_ n: TermNudge) {
        let l = lines
        selection.nudge(n, in: l)
        showSelection()
        if let h = selection.head { reveal(bufferRow: l.position(h).row) }
    }

    func swapSelectionEnds() {
        selection.swapEnds(in: lines)
        showSelection()
    }

    func selectAllText() {
        selection.selectAll(in: lines)
        showSelection()
    }

    /// Copies the selection (the model's text: logical lines, soft wraps
    /// joined) and leaves the mode.
    func copySelection() {
        let text = selection.text(in: lines)
        guard !text.isEmpty else { return }
        UIPasteboard.general.string = text
        exitSelectionMode()
    }

    /// Scrolls so a buffer row is on screen (a nudge moved past the edge).
    private func reveal(bufferRow row: Int) {
        let t = terminalView.getTerminal()
        let top = t.buffer.yDisp
        if row < top { terminalView.scrollTo(row: row) }
        else if row >= top + t.rows { terminalView.scrollTo(row: row - t.rows + 1) }
    }
}
