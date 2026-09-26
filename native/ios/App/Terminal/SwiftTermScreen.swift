import Foundation
import SwiftTerm
import XbinTerm

/// SwiftTerm's visible screen as XbinTerm's predictor reads it
/// (`TermFramebuffer`): row 0 is the top of the viewport. The predictor
/// only runs while the view isn't scrolled back (TerminalScreen hides the
/// overlay then), so the viewport is the live screen. UIKit-free: also
/// compiled by native/tools/app-check against a headless SwiftTerm on Linux.
struct SwiftTermScreen: TermFramebuffer {
    let terminal: Terminal

    var rows: Int { terminal.rows }
    var cols: Int { terminal.cols }

    var cursor: TermPos {
        let c = terminal.getCursorLocation()
        return TermPos(row: c.y, col: c.x)
    }

    /// "" for a never-written cell (code 0), else the glyph (" " for a blank
    /// that was written).
    func charAt(_ row: Int, _ col: Int) -> String {
        guard let cd = terminal.getCharData(col: col, row: row) else { return "" }
        let ch = terminal.getCharacter(for: cd)
        return ch == "\u{0}" ? "" : String(ch)
    }

    /// 0 for a wide glyph's second cell, 2 for the glyph, 1 otherwise.
    func widthAt(_ row: Int, _ col: Int) -> Int {
        guard let cd = terminal.getCharData(col: col, row: row) else { return 1 }
        return Int(cd.width)
    }

    /// One Character per cell; unwritten cells read as spaces.
    func lineAt(_ row: Int) -> String {
        var s = ""
        s.reserveCapacity(cols)
        for c in 0..<cols {
            let ch = charAt(row, c)
            s += ch.isEmpty ? " " : ch
        }
        return s
    }
}
