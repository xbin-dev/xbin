// PredictorTests.swift — the conformance suite for Predictor.swift: every case
// of hack/term-predict.test.mjs, in the same order, with the same names and
// the same assertions. A fake framebuffer plays the screen. Keep the two in
// step: a change to web/term-predict.js changes its test and this file.

import Testing
@testable import XbinTerm

/// A rows × cols screen of glyphs ("" = never written) with a cursor. `echo`
/// is what a shell does with typed text: print it at the cursor and advance.
final class FakeScreen: TermFramebuffer {
    var rows: Int
    var cols: Int
    var cursor = TermPos(row: 0, col: 0)
    var g: [[String]]
    var wide = Set<TermPos>()

    init(_ rows: Int = 4, _ cols: Int = 10) {
        self.rows = rows; self.cols = cols
        g = Array(repeating: Array(repeating: "", count: cols), count: rows)
    }
    func charAt(_ r: Int, _ c: Int) -> String {
        guard r >= 0, r < g.count, c >= 0, c < g[r].count else { return "" }
        return g[r][c]
    }
    func widthAt(_ r: Int, _ c: Int) -> Int { wide.contains(TermPos(row: r, col: c)) ? 2 : 1 }
    func lineAt(_ r: Int) -> String { g[r].map { $0.isEmpty ? " " : $0 }.joined() }
    func put(_ r: Int, _ c: Int, _ s: String) {
        for (i, ch) in s.unicodeScalars.enumerated() { g[r][c + i] = String(ch) }
    }
    func echo(_ s: String) {
        for ch in s.unicodeScalars { g[cursor.row][cursor.col] = String(ch); cursor.col += 1 }
    }
    func markWide(_ r: Int, _ c: Int) { wide.insert(TermPos(row: r, col: c)) }
    static func blankRow(_ n: Int) -> [String] { Array(repeating: "", count: n) }
}

/// A predictor in 'on' mode with the caller counting frames like bx-terminal does.
final class Rig {
    let p = Predictor()
    let fb: FakeScreen
    var seq: UInt64 = 0
    init(_ fb: FakeScreen, _ mode: PredictMode = .on) { self.fb = fb; p.setMode(mode) }
    @discardableResult func type(_ s: String, _ now: Double = 0) -> UInt64 {
        p.newUserData(s, fb, now: now); seq += 1; p.setLocalFrameSent(seq); return seq
    }
    func ack(_ n: UInt64, _ now: Double = 0) { p.setLateAck(n); p.cull(fb, now: now) }
}

func texts(_ p: Predictor, _ fb: FakeScreen) -> [String] {
    p.render(fb).cells.map { "\($0.row):\($0.col):\($0.text)" }
}
func run(_ row: Int, _ col: Int, _ text: String, _ underline: Bool = false) -> PredictedRun {
    PredictedRun(row: row, col: col, cells: text.unicodeScalars.map { String($0) }, underline: underline)
}
func pos(_ r: Int, _ c: Int) -> TermPos { TermPos(row: r, col: c) }
func cp(_ s: String) -> UInt32 { s.unicodeScalars.first!.value }

@Suite("term-predict conformance (hack/term-predict.test.mjs)")
struct PredictorTests {
    @Test("constants and helpers")
    func constants() {
        #expect(PredictConst.srttShow == 100); #expect(PredictConst.srttHide == 60); #expect(PredictConst.srttFlag == 160)
        #expect(srttUpdate(nil, 120) == 120)
        #expect(srttUpdate(120, 200) == 130)
        #expect(wcwidth(cp("a")) == 1); #expect(wcwidth(cp("ł")) == 1)
        #expect(wcwidth(cp("漢")) == 2); #expect(wcwidth(cp("🙂")) == 2)
        #expect(wcwidth(0x0301) == 0)
    }

    @Test("typing predicts the glyph at the cursor and the cursor one cell right")
    func typing() {
        let fb = FakeScreen(); let t = Rig(fb)
        t.type("a")
        let r = t.p.render(fb)
        #expect(r.cells == [run(0, 0, "a")])
        #expect(r.cursor == pos(0, 1))
        t.type("b")
        #expect(texts(t.p, fb) == ["0:0:ab"], "consecutive predictions coalesce into one run")
        #expect(t.p.render(fb).cursor == pos(0, 2))
    }

    @Test("an insert shifts the rest of the row right; the last column is unknown and not drawn")
    func insertShift() {
        let fb = FakeScreen(1, 6); fb.put(0, 0, "abcdef"); fb.cursor.col = 2
        let t = Rig(fb)
        t.type("X")
        #expect(texts(t.p, fb) == ["0:2:Xcd"], "X then the shifted c d; the last column is unknown (a wrap? a lost e?) and left alone")
    }

    @Test("backspace moves the cursor left and shifts the row left")
    func backspace() {
        let fb = FakeScreen(1, 8); fb.put(0, 0, "abcd"); fb.cursor.col = 4
        let t = Rig(fb)
        t.type("\u{7f}")
        let r = t.p.render(fb)
        #expect(r.cursor == pos(0, 3))
        #expect(r.cells == [run(0, 3, " ")], "the d is predicted gone")
        t.type("\u{7f}")
        #expect(t.p.render(fb).cursor == pos(0, 2))
        #expect(texts(t.p, fb) == ["0:2:  "])
        let fb2 = FakeScreen(); let two = Rig(fb2)
        two.type("\u{7f}")
        #expect(two.p.render(fb2).cursor == pos(0, 0), "backspace at column 0 moves nothing (the cursor prediction just pins the place)")
        #expect(two.p.pending() == 0)
    }

    @Test("Enter moves the cursor to the next row; on the bottom row it predicts that row blank")
    func enter() {
        let fb = FakeScreen(3, 6); fb.put(2, 0, "prompt"); fb.cursor = pos(1, 3)
        let t = Rig(fb)
        t.type("\r")
        #expect(t.p.render(fb).cursor == pos(2, 0))
        t.type("\r")
        #expect(t.p.render(fb).cursor == pos(2, 0), "no scroll prediction")
        #expect(texts(t.p, fb) == ["2:0:      "], "the bottom row is drawn blank")
        #expect(t.p.render(fb).cells[0].text.count == 6)
    }

    @Test("arrows move the predicted cursor, in normal and application mode; other escapes and pastes predict nothing")
    func arrows() {
        let fb = FakeScreen(); fb.cursor.col = 3
        let t = Rig(fb)
        t.type("\u{1b}[C"); #expect(t.p.render(fb).cursor == pos(0, 4))
        t.type("\u{1b}OD"); t.type("\u{1b}[D"); #expect(t.p.render(fb).cursor == pos(0, 2))
        t.p.reset()
        t.type("\u{1b}[3~"); t.type("\u{1b}[1;5C"); t.type("\u{1b}[200~abc\u{1b}[201~")
        #expect(t.p.active() == false, "delete, ctrl-arrow, bracketed paste")
        t.type(String(repeating: "x", count: PredictConst.maxChunk + 1))
        #expect(t.p.active() == false, "a long chunk is a paste")
        t.type("\u{03}"); t.type("\n"); t.type("\t")
        #expect(t.p.active() == false, "other controls")
    }

    @Test("wide characters predict no cell; a row with a wide glyph in the way predicts the cursor only")
    func wide() {
        let fb = FakeScreen(); let t = Rig(fb)
        t.type("漢")
        #expect(t.p.active() == false)
        fb.markWide(0, 4)
        t.type("a")
        #expect(t.p.render(fb).cells == [], "no cell prediction: the shift would misplace the wide glyph")
        #expect(t.p.render(fb).cursor == pos(0, 1))
    }

    @Test("a confirmed prediction is dropped once the ack covers it; a pending one stays")
    func confirmed() {
        let fb = FakeScreen(); let t = Rig(fb)
        let n = t.type("a")
        fb.echo("a")
        #expect(texts(t.p, fb) == [], "the echo landed: nothing differs, nothing drawn")
        #expect(t.p.pending() > 0, "but the prediction is still pending")
        t.ack(n - 1)
        #expect(t.p.pending() > 0, "an older ack does not cover it")
        t.ack(n)
        #expect(t.p.pending() == 0)
        #expect(t.p.render(fb).cursor == nil, "the cursor prediction was confirmed and released")
    }

    @Test("a wrong prediction drops that cell only (experimental); its neighbours survive")
    func wrongCell() {
        let fb = FakeScreen(1, 12); let t = Rig(fb)
        t.type("a"); t.type("b"); let n = t.type("c")
        fb.echo("aXc") // the application printed X for b
        t.ack(n)
        #expect(t.p.pending() == 0, "a and c confirmed, b incorrect — all three gone")
        fb.echo(""); fb.cursor.col = 3
        t.type("d"); let m = t.type("e")
        fb.echo("d") // the ack says e was answered — so e is wrong
        t.ack(m)
        #expect(texts(t.p, fb) == [])
        #expect(t.p.pending() == 0)
        // a mismatch on one of two cells with a later frame pending keeps the pending one
        let fb2 = FakeScreen(1, 12); let r2 = Rig(fb2)
        let k1 = r2.type("p"); r2.type("q")
        fb2.echo("Z")
        r2.ack(k1)
        #expect(r2.p.pending() > 0, "q (frame 2) is still pending")
        #expect(texts(r2.p, fb2) == ["0:1:q"], "p was wrong and is gone; q still drawn")
    }

    @Test("the cursor prediction goes on a mismatch")
    func cursorMismatch() {
        let fb = FakeScreen(); let t = Rig(fb)
        let n = t.type("a")
        fb.echo("a"); fb.cursor.col = 5 // the app moved the cursor elsewhere
        t.ack(n)
        #expect(t.p.render(fb).cursor == nil)
    }

    @Test("a resize resets everything")
    func resize() {
        let fb = FakeScreen(); let t = Rig(fb)
        t.type("abc")
        #expect(t.p.active())
        fb.cols = 20
        t.p.cull(fb, now: 0)
        #expect(t.p.active() == false)
    }

    @Test("auto mode follows the RTT with hysteresis; off mode predicts nothing")
    func autoMode() {
        let fb = FakeScreen(); let t = Rig(fb, .auto); let p = t.p
        p.setSrtt(80); p.cull(fb, now: 0)
        #expect(p.shown() == false, "below the threshold")
        p.setSrtt(120); p.cull(fb, now: 0)
        #expect(p.shown() == true, "above 100 ms")
        p.setSrtt(80); p.cull(fb, now: 0)
        #expect(p.shown() == true, "hysteresis: still on at 80")
        t.type("a")
        p.setSrtt(50); p.cull(fb, now: 0)
        #expect(p.shown() == true, "a prediction on screen keeps it on even at 50")
        p.reset(); p.setSrtt(50); p.cull(fb, now: 0)
        #expect(p.shown() == false, "nothing pending and ≤ 60: off")
        p.setMode(.off); t.type("a")
        #expect(p.active() == false); #expect(p.shown() == false)
        p.setMode(.on); #expect(p.shown() == true)
    }

    @Test("a prediction pending 250 ms is a glitch: shown on a fast link until ten quick confirmations")
    func glitch() {
        let fb = FakeScreen(1, 40); let t = Rig(fb, .auto); let p = t.p
        p.setSrtt(10)
        var now: Double = 1000
        let n = t.type("a", now)
        p.cull(fb, now: now + 100)
        #expect(p.shown() == false)
        p.cull(fb, now: now + PredictConst.glitchThreshold)
        #expect(p.shown() == true, "glitch trigger set")
        #expect(p.glitchTrigger == PredictConst.glitchRepairCount)
        #expect(p.flagging == false, "a plain glitch shows but does not underline")
        fb.echo("a"); t.ack(n, now + 300)
        // ten quick confirmations, at least 150 ms apart, cure it
        for _ in 0..<PredictConst.glitchRepairCount {
            now += 200
            let k = t.type("b", now); fb.echo("b"); t.ack(k, now + 20)
        }
        #expect(p.glitchTrigger == 0)
        #expect(p.shown() == false)
        // a very long wait underlines too
        let m = t.type("c", now); p.cull(fb, now: now + PredictConst.glitchFlagThreshold)
        #expect(p.glitchTrigger == PredictConst.glitchRepairCount * 2)
        p.cull(fb, now: now + PredictConst.glitchFlagThreshold + 1) // the underline follows on the next cull, as in mosh
        #expect(p.flagging == true)
        #expect(p.render(fb).cells.first?.underline == true)
        fb.echo("c"); t.ack(m, now + PredictConst.glitchFlagThreshold + 1)
    }

    @Test("flagging (underline) follows the RTT: on above 160 ms, off at 100")
    func flagging() {
        let fb = FakeScreen(); let t = Rig(fb, .on); let p = t.p
        p.setSrtt(200); t.type("a")
        #expect(p.render(fb).cells[0].underline == true)
        p.setSrtt(120); p.cull(fb, now: 0)
        #expect(p.flagging == true, "hysteresis")
        p.setSrtt(100); p.cull(fb, now: 0)
        #expect(p.flagging == false)
    }

    @Test("render skips what already matches the screen and never draws unknown cells")
    func renderSkips() {
        let fb = FakeScreen(2, 4); fb.put(0, 0, "ab"); fb.cursor.col = 2
        let t = Rig(fb)
        t.type("c"); t.type("d") // d lands in the last column → wraps the predicted cursor
        let r = t.p.render(fb)
        #expect(r.cells == [run(0, 2, "cd")])
        #expect(r.cursor == pos(1, 0), "a character in the last column predicts a wrap")
        fb.put(0, 2, "c")
        #expect(texts(t.p, fb) == ["0:3:d"], "the confirmed-looking c is no longer drawn")
    }

    @Test("the caller can drive the ack out of order with the ECHO_TIMEOUT model: a restored original earns no credit")
    func restoredOriginal() {
        let fb = FakeScreen(1, 6); fb.put(0, 0, "a"); fb.cursor.col = 0
        let t = Rig(fb)
        let n = t.type("a") // predicts a over a: the screen already shows it
        t.ack(n)
        #expect(t.p.pending() == 0, "dropped without credit")
    }

    // ---- a hidden cursor: anchor learning (D71) ----

    @Test("hidden cursor: nothing is predicted until the echo places the anchor; then typing predicts there, overwriting")
    func hiddenAnchor() {
        let fb = FakeScreen(6, 20); fb.put(4, 2, "> "); fb.cursor = pos(5, 0) // an Ink-style field; the terminal cursor is parked below it
        let t = Rig(fb); let p = t.p
        p.setCursorHidden(true)
        let n1 = t.type("a")
        #expect(p.pending() == 0, "the terminal cursor says nothing: no prediction yet")
        #expect(p.render(fb).cursor == nil)
        fb.put(4, 4, "a") // the app echoes into its field
        t.ack(n1)
        #expect(p.anchor == pos(4, 5), "the anchor is learned from the echo")
        let n2 = t.type("b")
        #expect(texts(p, fb) == ["4:5:b"], "predicted at the anchor, no shift")
        #expect(p.anchor == pos(4, 6))
        fb.put(4, 5, "b"); t.ack(n2)
        #expect(p.pending() == 0)
        t.type("\u{7f}")
        #expect(p.anchor == pos(4, 5), "backspace steps the anchor back")
        #expect(texts(p, fb) == ["4:5: "], "and predicts the cell blank")
        t.type("\r")
        #expect(p.anchor == nil, "Enter submits: the anchor is forgotten")
    }

    @Test("hidden cursor: the anchor follows the echo when the field moves; arrows move it; a visible cursor ends anchor mode")
    func hiddenFollows() {
        let fb = FakeScreen(6, 20); fb.put(4, 2, "> ab"); fb.cursor = pos(5, 0)
        let t = Rig(fb); let p = t.p
        p.setCursorHidden(true)
        let n1 = t.type("c")
        fb.put(4, 6, "c"); t.ack(n1)
        #expect(p.anchor == pos(4, 7))
        let n2 = t.type("d")
        #expect(texts(p, fb) == ["4:7:d"])
        // the field re-renders one row up (the frame grew): d lands there
        fb.g[3] = fb.g[4]; fb.g[4] = FakeScreen.blankRow(20); fb.put(3, 7, "d")
        t.ack(n2)
        #expect(p.pending() == 0, "the wrong cell is withdrawn")
        #expect(p.anchor == pos(3, 8), "re-learned from where d appeared")
        t.type("\u{1b}[D"); #expect(p.anchor == pos(3, 7))
        t.type("\u{1b}OC"); #expect(p.anchor == pos(3, 8))
        p.setCursorHidden(false)
        #expect(p.anchor == nil)
        t.type("x")
        #expect(p.render(fb).cursor == pos(5, 1), "back to cursor mode")
    }

    @Test("hidden cursor: a keystroke that never echoes is forgotten; escapes predict nothing")
    func hiddenForgotten() {
        let fb = FakeScreen(4, 10); fb.cursor = pos(3, 0)
        let t = Rig(fb); let p = t.p
        p.setCursorHidden(true)
        let n = t.type("z", 0)
        t.ack(n, 10)
        #expect(p.anchor == nil, "no echo anywhere: no anchor")
        #expect(p.learn.count == 0, "acked and unfound: dropped")
        t.type("q", 100)
        p.cull(fb, now: 100 + PredictConst.learnTTL + 1)
        #expect(p.learn.count == 0, "unacked past the TTL: dropped")
        t.type("\u{1b}[A")
        #expect(p.pending() == 0)
    }
}
