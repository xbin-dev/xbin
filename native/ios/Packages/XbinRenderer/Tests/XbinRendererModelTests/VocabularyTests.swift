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
        let (r, g, b) = XbinPalette.components(XbinPalette.amber)
        #expect(abs(r - 245.0 / 255) < 1e-9 && abs(g - 166.0 / 255) < 1e-9 && abs(b - 35.0 / 255) < 1e-9)
    }

    /// Text in a colour role is legible on the light backgrounds (white, the
    /// grouped background): AA's 4.5:1 for body text on white.
    @Test func lightTextContrast() {
        let text = [XbinPalette.amberTextLight, XbinPalette.okTextLight, XbinPalette.warnTextLight,
                    XbinPalette.dangerTextLight]
        for c in text {
            #expect(XbinPalette.contrast(c, 0xFFFFFF) >= 4.5, "\(String(c, radix: 16)) on white")
            #expect(XbinPalette.contrast(c, 0xF2F2F7) >= 4.1, "\(String(c, radix: 16)) on the grouped background")
        }
        // What they replace: systemOrange on white.
        #expect(XbinPalette.contrast(0xFF9500, 0xFFFFFF) < 2.5)
        #expect(abs(XbinPalette.contrast(0x000000, 0xFFFFFF) - 21) < 1e-9)
    }
}
