import Foundation
import Testing
import XbinRendererModel

/// The app's palette (XbinPalette) and the reference renderer's table
/// (web/xb/render-theme.js) name the same colour for every role both have:
/// a preview shows what the phone draws (D185). Read from the repository,
/// so it runs wherever the tests see the source tree (Linux, the Mac's
/// `swift test`); it skips when they don't.
@Suite struct ThemeParityTests {
    /// render-theme.js, found up the tree from this file.
    static func source() -> String? {
        var dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        for _ in 0..<10 {
            let f = dir.appendingPathComponent("web/xb/render-theme.js")
            if let s = try? String(contentsOf: f, encoding: .utf8) { return s }
            dir = dir.deletingLastPathComponent()
        }
        return nil
    }

    /// The `#RRGGBB` values of `const <name> = { … };` by key.
    static func table(_ name: String, in js: String) -> [String: UInt32] {
        guard let start = js.range(of: "const \(name) = {"),
              let end = js.range(of: "};", range: start.upperBound..<js.endIndex) else { return [:] }
        let body = String(js[start.upperBound..<end.lowerBound])
        var out: [String: UInt32] = [:]
        let re = try! NSRegularExpression(pattern: #"'?([a-z0-9-]+)'?:\s*'#([0-9A-Fa-f]{6})'"#)
        for m in re.matches(in: body, range: NSRange(body.startIndex..., in: body)) {
            guard let k = Range(m.range(at: 1), in: body), let v = Range(m.range(at: 2), in: body),
                  let hex = UInt32(body[v], radix: 16) else { continue }
            out[String(body[k])] = hex
        }
        return out
    }

    @Test func referenceEqualsThePalette() throws {
        guard let js = Self.source() else { return } // no source tree here
        let term = Self.table("TERM", in: js)
        for dark in [false, true] {
            let t = Self.table(dark ? "DARK" : "LIGHT", in: js).merging(term) { a, _ in a }
            let want: [String: UInt32] = [
                "bg": XbinPalette.bg.value(dark: dark), "surface": XbinPalette.panel.value(dark: dark),
                "surface2": XbinPalette.panel2.value(dark: dark), "border": XbinPalette.border.value(dark: dark),
                "separator": XbinPalette.border.value(dark: dark), "text": XbinPalette.text.value(dark: dark),
                "muted": XbinPalette.muted.value(dark: dark), "accent": XbinPalette.accent.value(dark: dark),
                "accent-text": XbinPalette.accent.value(dark: dark), "on-accent": XbinPalette.accentInk.value(dark: dark),
                "ok": XbinPalette.ok.value(dark: dark), "warn": XbinPalette.warn.value(dark: dark),
                "danger": XbinPalette.danger.value(dark: dark), "fill": XbinPalette.hover.value(dark: dark),
                "bubble": XbinPalette.panel2.value(dark: dark),
                "term-bg": XbinPalette.Terminal.background, "term-fg": XbinPalette.Terminal.foreground,
                "term-cursor": XbinPalette.Terminal.cursor, "term-bar": XbinPalette.Terminal.bar,
                "term-muted": XbinPalette.Terminal.barText,
            ]
            for (k, v) in want {
                #expect(t[k] == v, "\(dark ? "DARK" : "LIGHT").\(k): render-theme.js \(t[k].map { String($0, radix: 16) } ?? "missing"), XbinPalette \(String(v, radix: 16))")
            }
            let chart = dark ? XbinPalette.chartDark : XbinPalette.chartLight
            for (i, c) in chart.enumerated() {
                #expect(t["chart-\(i + 1)"] == c, "\(dark ? "DARK" : "LIGHT").chart-\(i + 1)")
            }
        }
        // The shapes and faces the table names.
        #expect(js.contains("--xb-radius: \(Int(XbinShapes.radius))px"))
        #expect(js.contains("'large-title': [[800, 34, 41], [800, 40, 48]]"))
        #expect(js.contains("\"Bricolage Grotesque\"") && js.contains("\"JetBrains Mono\""))
    }
}
