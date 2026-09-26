// PredictorTraceTests.swift — the differential half of the port's conformance:
// Resources/term-predict-trace.json holds seeded random sessions run through
// web/term-predict.js (hack/term-predict-trace.mjs) — each step a concrete
// operation and what the JS engine reported after it. Replaying them through
// Predictor.swift must report exactly the same at every step.

import Foundation
import Testing
@testable import XbinTerm

/// The trace's screen (hack/term-predict-trace.mjs `Screen`), op for op.
final class TraceScreen: TermFramebuffer {
    var rows: Int, cols: Int
    var cursor = TermPos(row: 0, col: 0)
    var g: [[String]]
    var wide = Set<TermPos>()
    init(_ rows: Int, _ cols: Int) {
        self.rows = rows; self.cols = cols
        g = Array(repeating: Array(repeating: "", count: cols), count: rows)
    }
    func charAt(_ r: Int, _ c: Int) -> String {
        guard r >= 0, r < g.count, c >= 0, c < g[r].count else { return "" }
        return g[r][c]
    }
    func widthAt(_ r: Int, _ c: Int) -> Int { wide.contains(TermPos(row: r, col: c)) ? 2 : 1 }
    func lineAt(_ r: Int) -> String {
        guard r >= 0, r < g.count else { return "" }
        return g[r].map { $0.isEmpty ? " " : $0 }.joined()
    }
    func put(_ r: Int, _ c: Int, _ s: String) {
        if r >= 0, r < g.count, c >= 0, c < g[r].count { g[r][c] = s }
    }
    func resize(_ nr: Int, _ nc: Int) {
        g = (0..<nr).map { r in (0..<nc).map { c in charAt(r, c) } }
        rows = nr; cols = nc; wide.removeAll()
        cursor = TermPos(row: min(cursor.row, nr - 1), col: min(cursor.col, nc))
    }
    func scroll() { g.removeFirst(); g.append(Array(repeating: "", count: cols)) }
    func move(_ from: Int, _ to: Int) { g[to] = g[from]; g[from] = Array(repeating: "", count: cols) }
}

/// One operation: [letter, ...arguments] (booleans as 0/1).
enum TraceOp: Decodable {
    case type(String, Double), ack(UInt64, Double), cull(Double), put(Int, Int, String), cursor(Int, Int)
    case scroll, move(Int, Int), resize(Int, Int), wide(Int, Int), hidden(Bool), srtt(Double?), mode(String), reset

    init(from decoder: any Decoder) throws {
        var c = try decoder.unkeyedContainer()
        let o = try c.decode(String.self)
        switch o {
        case "t": self = .type(try c.decode(String.self), try c.decode(Double.self))
        case "a": self = .ack(try c.decode(UInt64.self), try c.decode(Double.self))
        case "c": self = .cull(try c.decode(Double.self))
        case "p": self = .put(try c.decode(Int.self), try c.decode(Int.self), try c.decode(String.self))
        case "k": self = .cursor(try c.decode(Int.self), try c.decode(Int.self))
        case "S": self = .scroll
        case "M": self = .move(try c.decode(Int.self), try c.decode(Int.self))
        case "z": self = .resize(try c.decode(Int.self), try c.decode(Int.self))
        case "w": self = .wide(try c.decode(Int.self), try c.decode(Int.self))
        case "h": self = .hidden(try c.decode(Int.self) != 0)
        case "r": self = .srtt(try c.decode(Double?.self))
        case "m": self = .mode(try c.decode(String.self))
        case "x": self = .reset
        default: throw DecodingError.dataCorruptedError(in: c, debugDescription: "unknown op \(o)")
        }
    }
}

struct TraceStep: Decodable {
    let op: TraceOp
    let want: String?
    init(from decoder: any Decoder) throws {
        var c = try decoder.unkeyedContainer()
        op = try c.decode(TraceOp.self)
        want = try c.decode(String?.self)
    }
}
struct TraceSession: Decodable { let rows: Int; let cols: Int; let cells: [[CellArg]]; let steps: [TraceStep] }
enum CellArg: Decodable {
    case int(Int), str(String)
    init(from decoder: any Decoder) throws {
        let c = try decoder.singleValueContainer()
        if let i = try? c.decode(Int.self) { self = .int(i) } else { self = .str(try c.decode(String.self)) }
    }
    var int: Int { if case .int(let i) = self { return i }; return -1 }
    var str: String { if case .str(let s) = self { return s }; return "" }
}
struct Trace: Decodable { let v: Int; let seed: Int; let sessions: [TraceSession] }

/// observe: hack/term-predict-trace.mjs `observe`, field for field.
func observe(_ p: Predictor, _ fb: any TermFramebuffer) -> String {
    let r = p.render(fb)
    let cells = r.cells.map { "\($0.row):\($0.col):\($0.text)\($0.underline ? "_" : "")" }.joined(separator: "|")
    let cur = r.cursor.map { "\($0.row),\($0.col)" } ?? "-"
    let anc = p.anchor.map { "\($0.row),\($0.col)" } ?? "-"
    let b = { (x: Bool) in x ? 1 : 0 }
    return "c=\(cells);k=\(cur);p=\(p.pending());a=\(b(p.active()));s=\(b(p.shown()));f=\(b(p.flagging));t=\(b(p.srttTrigger));g=\(p.glitchTrigger);h=\(b(p.cursorHidden));n=\(anc);l=\(p.learn.count)"
}

@Suite("term-predict differential trace")
struct PredictorTraceTests {
    @Test("every step of the JS engine's trace replays identically")
    func replay() throws {
        let url = try #require(Bundle.module.url(forResource: "term-predict-trace", withExtension: "json"))
        let trace = try JSONDecoder().decode(Trace.self, from: Data(contentsOf: url))
        #expect(trace.v == 1)
        var total = 0, failures = 0
        for (si, s) in trace.sessions.enumerated() {
            let fb = TraceScreen(s.rows, s.cols), p = Predictor()
            for c in s.cells { fb.put(c[0].int, c[1].int, c[2].str) }
            var seq: UInt64 = 0
            var want = ""
            for (k, step) in s.steps.enumerated() {
                switch step.op {
                case .type(let str, let now): p.newUserData(str, fb, now: now); seq += 1; p.setLocalFrameSent(seq)
                case .ack(let n, let now): p.setLateAck(n); p.cull(fb, now: now)
                case .cull(let now): p.cull(fb, now: now)
                case .put(let r, let c, let str): fb.put(r, c, str)
                case .cursor(let r, let c): fb.cursor = TermPos(row: r, col: c)
                case .scroll: fb.scroll()
                case .move(let from, let to): fb.move(from, to)
                case .resize(let r, let c): fb.resize(r, c)
                case .wide(let r, let c): fb.wide.insert(TermPos(row: r, col: c))
                case .hidden(let h): p.setCursorHidden(h)
                case .srtt(let ms): p.setSrtt(ms)
                case .mode(let m): p.setMode(PredictMode(rawValue: m)!)
                case .reset: p.reset()
                }
                if let w = step.want { want = w }
                total += 1
                let got = observe(p, fb)
                if got != want {
                    failures += 1
                    if failures <= 5 { Issue.record("session \(si) step \(k) \(step.op): got \(got), want \(want)") }
                    break // the rest of this session would only repeat the divergence
                }
            }
        }
        #expect(failures == 0)
        #expect(total > 3000)
    }
}
