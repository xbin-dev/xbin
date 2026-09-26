// Predictor.swift — predictive local echo: a port of web/term-predict.js
// (mosh's PredictionEngine, src/frontend/terminaloverlay.cc, in its
// "experimental" flavour) over an abstract framebuffer. D70, D71. The web
// file's header is the design; this comment only notes what differs.
//
// The model. Typed bytes produce PREDICTIONS — overlay cells {row, col,
// replacement} and one cursor position — each stamped with the input frame
// whose effect it predicts (`expiration`) and the time it was made. The
// server acks an input frame once it is ECHO_TIMEOUT old (docs/protocol.md
// §/ws/term), so when lateAck ≥ expiration the framebuffer already shows the
// application's answer: a matching cell is confirmed and dropped, a wrong
// cell is dropped alone, a blank replacement never counts as wrong.
// Predictions are always computed; the mode decides whether they are shown.
//
// A hidden terminal cursor (D71): full-screen programs (Claude Code, most
// TUIs) hide it and echo typed text in their own field, so the engine learns
// the ANCHOR — the cell after where the last typed character appeared — and
// predicts there in overwrite mode.
//
// Port notes (the conformance suite is Tests/XbinTermTests/PredictorTests.swift,
// every case of hack/term-predict.test.mjs):
// - Input is iterated by Unicode scalar, as JS's [...str] iterates code
//   points (a Swift Character would fuse "\r\n").
// - A framebuffer line is indexed by Character (one per cell), where JS
//   indexes UTF-16 units; the two agree for every single-scalar BMP cell.
// - A render run carries its cells, so runs coalesce by cell count rather
//   than by UTF-16 length.
// - Row maps keep JS Map insertion order (render/cull iterate in it).

import Foundation

public enum PredictMode: String, Sendable, CaseIterable {
    case auto, on, off
}

/// Tunables, identical to web/term-predict.js.
public enum PredictConst {
    /// ms: auto shows predictions above SHOW, hides again at ≤ HIDE (mosh: 60/40)
    public static let srttShow: Double = 100
    public static let srttHide: Double = 60
    /// ms: underline predictions above FLAG, stop at ≤ UNFLAG (mosh's own thresholds)
    public static let srttFlag: Double = 160
    public static let srttUnflag: Double = 100
    /// a prediction pending this long is a glitch: show them regardless of RTT
    public static let glitchThreshold: Double = 250
    /// quick confirmations that cure a glitch
    public static let glitchRepairCount = 10
    /// ms between confirmations that count toward the cure
    public static let glitchRepairMinInterval: Double = 150
    /// pending this long: show AND underline
    public static let glitchFlagThreshold: Double = 5000
    /// the server acks input this long after the PTY took it
    public static let echoTimeout: Double = 50
    /// longer input chunks (pastes) predict nothing
    public static let maxChunk = 64
    /// keystrokes remembered for anchor learning
    public static let maxLearn = 32
    /// ms a keystroke waits for its echo before it is forgotten
    public static let learnTTL: Double = 5000
}

/// RFC 6298 smoothing, as mosh's network layer does it.
public func srttUpdate(_ prev: Double?, _ r: Double) -> Double {
    guard let prev else { return r }
    return prev * 7 / 8 + r / 8
}

/// wcwidth, compact: 0 for combining marks, 2 for East-Asian wide and emoji,
/// 1 for everything else. Only width-1 glyphs are predicted.
public func wcwidth(_ cp: UInt32) -> Int {
    if (0x0300...0x036f).contains(cp) || (0x1ab0...0x1aff).contains(cp) || (0x1dc0...0x1dff).contains(cp)
        || (0x20d0...0x20ff).contains(cp) || (0xfe20...0xfe2f).contains(cp) || cp == 0x200b || cp == 0x200d {
        return 0
    }
    if (0x1100...0x115f).contains(cp) || (0x2e80...0x303e).contains(cp) || (0x3041...0x33ff).contains(cp)
        || (0x3400...0x4dbf).contains(cp) || (0x4e00...0x9fff).contains(cp) || (0xa000...0xa4cf).contains(cp)
        || (0xac00...0xd7a3).contains(cp) || (0xf900...0xfaff).contains(cp) || (0xfe30...0xfe4f).contains(cp)
        || (0xff00...0xff60).contains(cp) || (0xffe0...0xffe6).contains(cp) || (0x1f300...0x1faff).contains(cp)
        || (0x20000...0x3fffd).contains(cp) {
        return 2
    }
    return 1
}

/// A cell position on the visible screen, 0-based.
public struct TermPos: Hashable, Sendable, CustomStringConvertible {
    public var row: Int
    public var col: Int
    public init(row: Int, col: Int) { self.row = row; self.col = col }
    public var description: String { "(\(row),\(col))" }
}

/// The screen the predictor validates against — whatever the app wraps its
/// emulator in (SwiftTerm's active buffer, a test double). Rows and columns
/// are of the VISIBLE screen (row 0 = the top of the viewport at the bottom
/// of the scrollback), as xterm's buffer.baseY + row is in bx-terminal.
public protocol TermFramebuffer {
    var rows: Int { get }
    var cols: Int { get }
    /// The terminal cursor.
    var cursor: TermPos { get }
    /// "" for a never-written cell, " " for a blank, else the cell's glyph.
    func charAt(_ row: Int, _ col: Int) -> String
    /// 0 (a wide glyph's continuation cell), 1, or 2 (a wide glyph).
    func widthAt(_ row: Int, _ col: Int) -> Int
    /// The row's text, one Character per cell.
    func lineAt(_ row: Int) -> String
}

/// One run of consecutive predicted cells to draw over the screen.
public struct PredictedRun: Equatable, Sendable {
    public var row: Int
    public var col: Int
    /// One string per cell ("" never appears: a blank prediction is " ").
    public var cells: [String]
    public var underline: Bool
    public var text: String { cells.joined() }
    public init(row: Int, col: Int, cells: [String], underline: Bool) {
        self.row = row; self.col = col; self.cells = cells; self.underline = underline
    }
}

/// What to draw: runs of predicted cells that differ from the screen, and the
/// predicted cursor (nil = the real cursor is right).
public struct PredictionRender: Equatable, Sendable {
    public var cells: [PredictedRun]
    public var cursor: TermPos?
    public var flagging: Bool
    public static let empty = PredictionRender(cells: [], cursor: nil, flagging: false)
    public init(cells: [PredictedRun], cursor: TermPos?, flagging: Bool) {
        self.cells = cells; self.cursor = cursor; self.flagging = flagging
    }
}

/// A keystroke waiting for its echo, to place the anchor (hidden cursor).
public struct LearnEntry: Sendable {
    public var ch: String
    public var expiration: UInt64
    public var time: Double
    public var before: [[Character]]
    public var expect: TermPos?
}

/// An overlay cell. `orig` is every content this cell had before we predicted
/// over it: a prediction that merely restores one of those earns no credit
/// (mosh's original_contents rule). A class: the JS engine aliases cells.
final class PredCell {
    var active = false
    var unknown = false
    var replacement = ""
    var expiration: UInt64 = 0
    var time: Double = 0
    var orig: [String]
    init(orig: [String] = []) { self.orig = orig }
}

private struct PredCursor {
    var row: Int
    var col: Int
    var expiration: UInt64
    var time: Double
}

private enum Validity { case pending, correct, noCredit, incorrect }

/// JS string equality: code-unit exact, no canonical equivalence.
@inline(__always) private func same(_ a: String, _ b: String) -> Bool {
    a.utf16.elementsEqual(b.utf16)
}
@inline(__always) private func blank(_ s: String) -> Bool { s.isEmpty || s == " " }

/// mosh's reset_with_orig: an active, known cell carries its replacement into the history.
private func resetWithOrig(_ c: PredCell?) -> PredCell {
    if let c, c.active, !c.unknown { return PredCell(orig: c.orig + [c.replacement]) }
    return PredCell()
}

private func snapshot(_ fb: any TermFramebuffer) -> [[Character]] {
    (0..<max(fb.rows, 0)).map { Array(fb.lineAt($0)) }
}

/// findEcho: where did the typed character newly appear? The candidate nearest
/// the expected anchor, else the bottom-most (input fields live at the bottom).
private func findEcho(_ l: LearnEntry, _ cur: [[Character]], _ fb: any TermFramebuffer) -> TermPos? {
    let ch = Character(l.ch)
    var best: (pos: TermPos, d: Int)?
    for r in 0..<max(fb.rows, 0) {
        let b = r < l.before.count ? l.before[r] : []
        let c = r < cur.count ? cur[r] : []
        if b == c { continue }
        for x in 0..<c.count {
            if c[x] != ch || (x < b.count ? b[x] : " ") == ch { continue }
            let d: Int
            if let e = l.expect { d = abs(r - e.row) * 1000 + abs(x - e.col) } else { d = (fb.rows - r) * 1000 + x }
            if best == nil || d < best!.d { best = (TermPos(row: r, col: x), d) }
        }
    }
    return best?.pos
}

/// The predictive echo engine. Not thread-safe: one owner (the terminal
/// session) drives it from one isolation domain. Times are milliseconds on
/// any monotonic clock; frame numbers are the caller's count of binary input
/// frames on the current socket (docs/protocol.md §/ws/term `ack`).
public final class Predictor {
    public private(set) var mode: PredictMode = .auto
    /// smoothed RTT in ms; nil until the first pong
    public private(set) var srtt: Double?
    /// input frames sent so far (the caller counts)
    public private(set) var localFrameSent: UInt64 = 0
    /// the server's echo ack
    public private(set) var lateAck: UInt64 = 0
    public private(set) var srttTrigger = false
    public private(set) var flagging = false
    public private(set) var glitchTrigger = 0
    private var lastQuick: Double = 0
    private var lastRows = 0, lastCols = 0
    /// row → cells (nil = no prediction); `rowOrder` is the JS Map's insertion order
    private var rows: [Int: [PredCell?]] = [:]
    private var rowOrder: [Int] = []
    private var cursor: PredCursor?
    /// the application hid the terminal cursor (DECTCEM off)
    public private(set) var cursorHidden = false
    /// hidden cursor: where the next typed character will appear
    public private(set) var anchor: TermPos?
    /// hidden cursor: keystrokes awaiting their echo
    public private(set) var learn: [LearnEntry] = []

    public init() {}

    public func setMode(_ m: PredictMode) {
        if m == mode { return }
        mode = m
        if m == .off { reset() }
    }
    public func setSrtt(_ ms: Double?) { srtt = ms }
    public func setLocalFrameSent(_ n: UInt64) { localFrameSent = n }
    public func setLateAck(_ n: UInt64) { lateAck = n }
    public func setCursorHidden(_ h: Bool) {
        if h == cursorHidden { return }
        cursorHidden = h; anchor = nil; learn = []; cursor = nil
    }
    public func reset() {
        rows.removeAll(); rowOrder.removeAll(); cursor = nil; anchor = nil; learn = []
    }

    /// Are predictions being displayed right now?
    public func shown() -> Bool {
        mode == .on || (mode == .auto && (srttTrigger || glitchTrigger > 0))
    }
    /// Is anything predicted (shown or not)?
    public func active() -> Bool { cursor != nil || pending() > 0 }
    public func pending() -> Int {
        var n = 0
        for k in rowOrder { for c in rows[k]! where c?.active == true { n += 1 } }
        return n
    }

    // ---- input → predictions (mosh: new_user_byte) ----

    /// Typed input, as it is about to be sent as the next binary frame.
    public func newUserData(_ str: String, _ fb: any TermFramebuffer, now: Double) {
        if mode == .off { return }
        cull(fb, now: now)
        let exp = localFrameSent + 1
        if cursorHidden { anchorInput(str, fb, exp, now); return }
        // arrows move the predicted cursor (ESC O x is the application-mode spelling)
        if str == "\u{1b}[C" || str == "\u{1b}OC" {
            initCursor(fb, exp, now)
            if cursor!.col < fb.cols - 1 { moveCursor(1, exp, now) }
            return
        }
        if str == "\u{1b}[D" || str == "\u{1b}OD" {
            initCursor(fb, exp, now)
            if cursor!.col > 0 { moveCursor(-1, exp, now) }
            return
        }
        // any other escape (modified keys, bracketed paste, the terminal's own
        // DA/DSR/mouse/focus replies) and pastes predict nothing
        let cps = Array(str.unicodeScalars)
        if cps.contains("\u{1b}") { return }
        if cps.count > PredictConst.maxChunk { return }
        for s in cps {
            let cp = s.value
            if cp == 0x7f { backspace(fb, exp, now) }
            else if cp == 0x0d { newlineCR(fb, exp, now) }
            else if cp >= 0x20 && wcwidth(cp) == 1 { printChar(String(s), fb, exp, now) }
            // other controls and wide/combining glyphs predict nothing
        }
    }

    /// Typed input as raw bytes (what the emulator hands the app). Input that
    /// is not valid UTF-8 predicts nothing.
    public func newUserData(bytes: some Sequence<UInt8>, _ fb: any TermFramebuffer, now: Double) {
        guard let s = String(validating: Array(bytes), as: UTF8.self) else { return }
        newUserData(s, fb, now: now)
    }

    // ---- hidden cursor: predict at the learned anchor (D71) ----

    private func anchorInput(_ str: String, _ fb: any TermFramebuffer, _ exp: UInt64, _ now: Double) {
        if str == "\u{1b}[C" || str == "\u{1b}OC" {
            if let a = anchor, a.col < fb.cols - 1 { anchor!.col += 1 }
            return
        }
        if str == "\u{1b}[D" || str == "\u{1b}OD" {
            if let a = anchor, a.col > 0 { anchor!.col -= 1 }
            return
        }
        let cps = Array(str.unicodeScalars)
        if cps.contains("\u{1b}") { return }
        if cps.count > PredictConst.maxChunk { return }
        for s in cps {
            let cp = s.value
            if cp == 0x0d {
                anchor = nil; learn = [] // Enter submits: the field is about to change
            } else if cp == 0x7f {
                if let a = anchor, a.col > 0 {
                    anchor!.col -= 1
                    overwrite(fb, anchor!, "", exp, now)
                }
            } else if cp >= 0x20 && wcwidth(cp) == 1 {
                let ch = String(s)
                if learn.count < PredictConst.maxLearn {
                    learn.append(LearnEntry(ch: ch, expiration: exp, time: now, before: snapshot(fb), expect: anchor))
                }
                if let a = anchor {
                    overwrite(fb, a, ch, exp, now)
                    anchor = a.col + 1 < fb.cols ? TermPos(row: a.row, col: a.col + 1) : nil
                }
            }
        }
    }

    private func overwrite(_ fb: any TermFramebuffer, _ at: TermPos, _ ch: String, _ exp: UInt64, _ now: Double) {
        if fb.widthAt(at.row, at.col) != 1 { return }
        let cell = stamp(resetWithOrig(cellAt(at.row, at.col, fb.cols)), exp, now)
        setCell(at.row, at.col, cell)
        cell.replacement = ch
        cell.orig.append(fb.charAt(at.row, at.col))
    }

    /// learnAnchor moves the anchor to where acked keystrokes actually appeared.
    private func learnAnchor(_ fb: any TermFramebuffer, _ now: Double) {
        if learn.isEmpty { return }
        let cur = snapshot(fb)
        var keep: [LearnEntry] = []
        for l in learn {
            if lateAck < l.expiration {
                if now - l.time < PredictConst.learnTTL { keep.append(l) }
                continue
            }
            if let at = findEcho(l, cur, fb) {
                anchor = at.col + 1 < fb.cols ? TermPos(row: at.row, col: at.col + 1) : nil
            }
        }
        learn = keep
    }

    // ---- the row map (a JS Map: insertion-ordered, set on an existing key keeps its place) ----

    /// The row's cells, created (or recreated at a new width) on demand.
    @discardableResult private func row(_ row: Int, _ cols: Int) -> Int {
        if let r = rows[row], r.count == cols { return row }
        if rows[row] == nil { rowOrder.append(row) }
        rows[row] = Array(repeating: nil, count: max(cols, 0))
        return row
    }
    private func cellAt(_ r: Int, _ c: Int, _ cols: Int) -> PredCell? {
        row(r, cols)
        return c >= 0 && c < rows[r]!.count ? rows[r]![c] : nil
    }
    private func peek(_ r: Int, _ c: Int) -> PredCell? {
        guard let cells = rows[r], c >= 0, c < cells.count else { return nil }
        return cells[c]
    }
    private func setCell(_ r: Int, _ c: Int, _ cell: PredCell?) {
        guard c >= 0, c < rows[r]!.count else { return }
        rows[r]![c] = cell
    }
    private func deleteRow(_ r: Int) {
        rows[r] = nil
        rowOrder.removeAll { $0 == r }
    }

    private func initCursor(_ fb: any TermFramebuffer, _ exp: UInt64, _ now: Double) {
        if cursor == nil {
            cursor = PredCursor(row: min(fb.cursor.row, fb.rows - 1), col: min(fb.cursor.col, fb.cols - 1), expiration: exp, time: now)
        }
    }
    private func moveCursor(_ d: Int, _ exp: UInt64, _ now: Double) {
        cursor!.col += d; cursor!.expiration = exp; cursor!.time = now
    }
    @discardableResult private func stamp(_ cell: PredCell, _ exp: UInt64, _ now: Double) -> PredCell {
        cell.active = true; cell.expiration = exp; cell.time = now
        return cell
    }

    private func printChar(_ ch: String, _ fb: any TermFramebuffer, _ exp: UInt64, _ now: Double) {
        initCursor(fb, exp, now)
        let row = cursor!.row, col = cursor!.col
        // a wide glyph (or its continuation cell) in the span we would shift would
        // misplace every cell after it: move the cursor, predict no cells
        var plain = true
        var i = col
        while i < fb.cols { if fb.widthAt(row, i) != 1 { plain = false; break }; i += 1 }
        if plain {
            self.row(row, fb.cols)
            // the insert: everything right of the cursor moves one cell right; the
            // last column's content is unknown (a wrap? a lost character?)
            var i = fb.cols - 1
            while i > col {
                let prev = peek(row, i - 1)
                let cell = stamp(resetWithOrig(peek(row, i)), exp, now)
                setCell(row, i, cell)
                cell.orig.append(fb.charAt(row, i))
                if i == fb.cols - 1 { cell.unknown = true }
                else if let prev, prev.active {
                    if prev.unknown { cell.unknown = true } else { cell.replacement = prev.replacement }
                } else { cell.replacement = fb.charAt(row, i - 1) }
                i -= 1
            }
            let cell = stamp(resetWithOrig(peek(row, col)), exp, now)
            setCell(row, col, cell)
            cell.replacement = ch
            cell.orig.append(fb.charAt(row, col))
        }
        cursor!.expiration = exp; cursor!.time = now
        if col < fb.cols - 1 { cursor!.col += 1 }
        else { newlineCR(fb, exp, now) } // typed in the last column: assume a wrap
    }

    private func backspace(_ fb: any TermFramebuffer, _ exp: UInt64, _ now: Double) {
        initCursor(fb, exp, now)
        if cursor!.col <= 0 { return }
        moveCursor(-1, exp, now)
        let row = cursor!.row, col = cursor!.col
        self.row(row, fb.cols)
        // everything right of the cursor moves one cell left; the last two
        // columns become unknown
        var i = col
        while i < fb.cols {
            let next = peek(row, i + 1)
            let cell = stamp(resetWithOrig(peek(row, i)), exp, now)
            setCell(row, i, cell)
            cell.orig.append(fb.charAt(row, i))
            if i + 2 < fb.cols {
                if let next, next.active {
                    if next.unknown { cell.unknown = true } else { cell.replacement = next.replacement }
                } else { cell.replacement = fb.charAt(row, i + 1) }
            } else { cell.unknown = true }
            i += 1
        }
    }

    private func newlineCR(_ fb: any TermFramebuffer, _ exp: UInt64, _ now: Double) {
        initCursor(fb, exp, now)
        cursor!.col = 0; cursor!.expiration = exp; cursor!.time = now
        if cursor!.row == fb.rows - 1 {
            // no scroll prediction (mosh: "until we have versioned cell
            // predictions"); predict the bottom row blank instead
            let r = cursor!.row
            self.row(r, fb.cols)
            for i in 0..<max(fb.cols, 0) {
                let cell = stamp(peek(r, i) ?? PredCell(), exp, now)
                setCell(r, i, cell)
                cell.replacement = ""
            }
        } else { cursor!.row += 1 }
    }

    // ---- validation against the framebuffer (mosh: cull) ----

    private func cellValidity(_ cell: PredCell, _ row: Int, _ col: Int, _ fb: any TermFramebuffer) -> Validity {
        if row >= fb.rows || col >= fb.cols { return .incorrect }
        if lateAck < cell.expiration { return .pending }
        if cell.unknown || blank(cell.replacement) { return .noCredit } // a blank is too easy to be right about
        if !same(fb.charAt(row, col), cell.replacement) { return .incorrect }
        return cell.orig.contains(where: { same($0, cell.replacement) }) ? .noCredit : .correct
    }
    private func cursorValidity(_ fb: any TermFramebuffer) -> Validity {
        let c = cursor!
        if c.row >= fb.rows || c.col >= fb.cols { return .incorrect }
        if lateAck < c.expiration { return .pending }
        return fb.cursor.row == c.row && fb.cursor.col == c.col ? .correct : .incorrect
    }

    /// Judges every prediction against the screen: call it whenever the
    /// screen changed (after the emulator parsed output) and before render.
    public func cull(_ fb: any TermFramebuffer, now: Double) {
        if mode == .off { return }
        if fb.rows != lastRows || fb.cols != lastCols { lastRows = fb.rows; lastCols = fb.cols; reset() }
        let srtt = self.srtt ?? 0
        // the RTT triggers, with hysteresis; predictions on screen keep the show trigger
        if srtt > PredictConst.srttShow { srttTrigger = true }
        else if srttTrigger && srtt <= PredictConst.srttHide && !active() { srttTrigger = false }
        if srtt > PredictConst.srttFlag { flagging = true }
        else if srtt <= PredictConst.srttUnflag { flagging = false }
        if glitchTrigger > PredictConst.glitchRepairCount { flagging = true } // a big glitch underlines too
        for row in rowOrder {
            if row < 0 || row >= fb.rows { deleteRow(row); continue }
            guard let r = rows[row] else { continue }
            var live = 0
            for col in 0..<r.count {
                guard let cell = r[col], cell.active else { continue }
                switch cellValidity(cell, row, col, fb) {
                case .incorrect: // experimental: this cell only
                    rows[row]![col] = nil
                case .correct:
                    // quick confirmations slowly cure a glitch
                    if now - cell.time < PredictConst.glitchThreshold && glitchTrigger > 0
                        && now - PredictConst.glitchRepairMinInterval >= lastQuick {
                        glitchTrigger -= 1; lastQuick = now
                    }
                    rows[row]![col] = nil
                case .noCredit:
                    rows[row]![col] = nil
                case .pending: // a long wait is a glitch — show predictions even on a fast link
                    if now - cell.time >= PredictConst.glitchFlagThreshold {
                        glitchTrigger = PredictConst.glitchRepairCount * 2
                    } else if now - cell.time >= PredictConst.glitchThreshold && glitchTrigger < PredictConst.glitchRepairCount {
                        glitchTrigger = PredictConst.glitchRepairCount
                    }
                    live += 1
                }
            }
            if live == 0 { deleteRow(row) }
        }
        // confirmed or wrong: the real cursor takes over
        if cursor != nil && cursorValidity(fb) != .pending { cursor = nil }
        if cursorHidden { learnAnchor(fb, now) }
    }

    // ---- what to draw ----

    /// Runs of consecutive predicted cells that differ from the framebuffer,
    /// plus the predicted cursor. Blank predictions draw as spaces (that is how
    /// the bottom row clears on Enter); unknown cells draw nothing.
    public func render(_ fb: any TermFramebuffer) -> PredictionRender {
        if !shown() { return .empty }
        var out: [PredictedRun] = []
        for row in rowOrder {
            if row >= fb.rows { continue }
            let r = rows[row]!
            var inRun = false
            for col in 0..<min(r.count, fb.cols) {
                var text: String?
                if let cell = r[col], cell.active, !cell.unknown {
                    let cur = fb.charAt(row, col)
                    let differs = blank(cell.replacement) ? !blank(cur) : !same(cell.replacement, cur)
                    if differs { text = blank(cell.replacement) ? " " : cell.replacement }
                }
                guard let text else { inRun = false; continue }
                if inRun, let last = out.last, last.col + last.cells.count == col {
                    out[out.count - 1].cells.append(text)
                } else {
                    out.append(PredictedRun(row: row, col: col, cells: [text], underline: flagging))
                    inRun = true
                }
            }
        }
        let c = cursor.map { TermPos(row: $0.row, col: $0.col) }
        return PredictionRender(cells: out, cursor: c, flagging: flagging)
    }
}
