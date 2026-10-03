import Foundation
import Testing
import XbinCore
@testable import XbinRendererModel

/// The renderer's tables against native/spec/vocab.json (the export of
/// web/xb/vocab.js): a primitive, event report, token or icon added there
/// must be added here — with the view that draws it.
@Suite struct VocabularyTests {
    @Test func primitivesAndRevisions() throws {
        let v = try vocabJSON()
        #expect(v["v"]?.intValue == Int64(XbinVocabulary.version))
        let prims = try #require(v["prims"]?.objectValue)
        var spec: [String: Int] = [:]
        for (name, p) in prims { spec[name] = p["rev"]?.intValue.map { Int($0) } }
        #expect(spec == XbinVocabulary.primitives)
        #expect(Set((v["features"]?.arrayValue ?? []).compactMap(\.stringValue)) == Set(XbinVocabulary.features))
    }

    /// Widgets (tree.md §13): the caps feature the app sends, the card
    /// sizes, and primitives this renderer draws.
    @Test func widgets() throws {
        let w = try #require(try vocabJSON()["widget"])
        #expect(w["feature"]?.stringValue == NativeCaps.widgetFeature)
        #expect((w["sizes"]?.arrayValue ?? []).compactMap(\.stringValue) == CardSize.allCases.map(\.rawValue))
        for p in (w["prims"]?.arrayValue ?? []).compactMap(\.stringValue) {
            #expect(XbinVocabulary.primitives[p] != nil, "widget primitive \(p)")
        }
        // A wire feature, not a vocabulary one: the app adds it to the caps.
        #expect(!XbinVocabulary.features.contains(NativeCaps.widgetFeature))
        #expect(XbinVocabulary.caps(app: "1").withWidget(size: .wide).supports("widget"))
    }

    @Test func reports() throws {
        let prims = try #require(try vocabJSON()["prims"]?.objectValue)
        var spec: [String: [String: XbinVocabulary.Report]] = [:]
        for (name, p) in prims {
            for (event, e) in p["events"]?.objectValue ?? [:] {
                guard let r = e["reports"]?.objectValue, let prop = r["prop"]?.stringValue else { continue }
                spec[name, default: [:]][event] = XbinVocabulary.Report(prop: prop, from: r["from"]?.stringValue, value: r["value"])
            }
        }
        #expect(spec == XbinVocabulary.reports)
    }

    @Test func tokensAndIcons() throws {
        let v = try vocabJSON()
        let tokens = try #require(v["tokens"]?.objectValue)
        for (name, values) in tokens {
            let mine = try #require(XbinVocabulary.tokens[name], "token set \(name) missing")
            #expect(Set((values.arrayValue ?? []).compactMap(\.stringValue)) == Set(mine), "token set \(name)")
        }
        #expect(Set(tokens.keys) == Set(XbinVocabulary.tokens.keys))
        var icons: [String: String] = [:]
        for (k, s) in v["icons"]?.objectValue ?? [:] { icons[k] = s.stringValue }
        #expect(icons == XbinIcons.symbols)
        #expect(XbinIcons.symbol("nope") == XbinIcons.placeholder)
        #expect(XbinIcons.symbol(nil) == nil && XbinIcons.symbol("") == nil)
        #expect(XbinIcons.symbol("send") == "arrow.up.circle.fill")
    }

    @Test func caps() throws {
        let caps = XbinVocabulary.caps(app: "1.0 (1)")
        #expect(caps.renderer == "swiftui" && caps.v == 1 && caps.supports("composer") && caps.supports("chart.area"))
        // Round trip through the injection's JSON.
        #expect(try NativeCaps(json: caps.json) == caps)
    }

    @Test func tokenValues() {
        #expect(XbinGap.allCases.map(\.points) == [0, 4, 8, 12, 16, 24, 32])
        #expect(XbinHeight.allCases.map(\.points) == [48, 96, 160, 240, 360])
        #expect(XbinTone("warn") == .warn && XbinTone("info") == nil && XbinTone(nil) == nil)
        #expect(XbinNoticeTone("info")?.tone == nil && XbinNoticeTone("danger")?.symbol == "exclamationmark.octagon")
        #expect(XbinTypeRole("caption2") == .caption2 && XbinTypeRole("huge") == nil)
        let (r, g, b) = XbinPalette.components(XbinPalette.accent.day)
        #expect(abs(r - 31.0 / 255) < 1e-9 && abs(g - 61.0 / 255) < 1e-9 && abs(b - 1.0) < 1e-9)
        #expect(XbinPalette.accent.value(dark: true) == 0x8C9BFF && XbinShade(both: 1) == XbinShade(1, 1))
    }

    /// Text in a colour role is legible where it sits, in both appearances
    /// (WCAG 2.2 AA, as theme.css holds them): body text and every role
    /// drawn as text 4.5:1 on the panel and the inset panel, muted and the
    /// accent on the canvas too; the accent ink on the accent; the mark's
    /// white b 4.5:1 and its yellow x 3:1 (a graphic) on the cobalt tile.
    @Test func textContrast() {
        let asText: [XbinShade] = [XbinPalette.text, XbinPalette.muted, XbinPalette.subtle, XbinPalette.accent,
                                    XbinPalette.ok, XbinPalette.warn, XbinPalette.danger, XbinPalette.info]
        for dark in [false, true] {
            for c in asText {
                for ground in [XbinPalette.panel, XbinPalette.panel2] {
                    let (fg, bg) = (c.value(dark: dark), ground.value(dark: dark))
                    #expect(XbinPalette.contrast(fg, bg) >= 4.5, "\(String(fg, radix: 16)) on \(String(bg, radix: 16))")
                }
            }
            for c in [XbinPalette.text, XbinPalette.muted, XbinPalette.accent] {
                let (fg, bg) = (c.value(dark: dark), XbinPalette.bg.value(dark: dark))
                #expect(XbinPalette.contrast(fg, bg) >= 4.5, "\(String(fg, radix: 16)) on the canvas \(String(bg, radix: 16))")
            }
            let (ink, accent) = (XbinPalette.accentInk.value(dark: dark), XbinPalette.accent.value(dark: dark))
            #expect(XbinPalette.contrast(ink, accent) >= 4.5, "the accent ink on the accent")
            // Status on its own tint (a badge, a notice).
            for (c, tint) in [(XbinPalette.ok, XbinPalette.okBg), (XbinPalette.warn, XbinPalette.warnBg),
                              (XbinPalette.danger, XbinPalette.dangerBg), (XbinPalette.info, XbinPalette.infoBg)] {
                #expect(XbinPalette.contrast(c.value(dark: dark), tint.value(dark: dark)) >= 4.5,
                        "\(String(c.value(dark: dark), radix: 16)) on its tint")
            }
        }
        #expect(XbinPalette.contrast(XbinPalette.markB, XbinPalette.markTile) >= 4.5)
        #expect(XbinPalette.contrast(XbinPalette.markX, XbinPalette.markTile) >= 3)
        // The terminal's foreground and its normal colours on its background
        // (white is dim text, as on the web).
        for c in [XbinPalette.Terminal.foreground] + XbinPalette.Terminal.ansi[1...7] {
            #expect(XbinPalette.contrast(c, XbinPalette.Terminal.background) >= 4.5, "\(String(c, radix: 16)) in the terminal")
        }
        #expect(abs(XbinPalette.contrast(0x000000, 0xFFFFFF) - 21) < 1e-9)
    }

    /// Base Two's shapes and part tabs.
    @Test func shapes() {
        #expect(XbinShapes.radius == 2 && XbinWidgetMetrics.cornerRadius == 2)
        #expect(XbinPalette.partTabHeight == 3)
        #expect(XbinPalette.Terminal.ansi.count == 16)
        #expect(XbinPalette.chartLight.count == 6 && XbinPalette.chartDark.count == 6)
    }
}
