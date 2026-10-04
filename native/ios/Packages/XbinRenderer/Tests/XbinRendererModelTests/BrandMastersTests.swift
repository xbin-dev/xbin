import Foundation
import Testing
import XbinRendererModel

/// The app's copies of the brand's masters stay the masters (D183, D185):
/// the mark and wordmark A it draws (XbinBrandPaths), the mark's colours
/// (XbinPalette), the app icon's fill and layers (AppIcon.icon), and the
/// code face as the brand sets it — JetBrains Mono without its ligatures.
/// The web's copies have TestBrandMarkIsTheMasters; a swap of a master (the
/// wordmark, brand.md §13) fails here until the app follows. Read from the
/// repository, so it runs wherever the tests see the source tree (Linux,
/// the Mac's `swift test`); it skips when they don't.
@Suite struct BrandMastersTests {
    /// The repository root, found up the tree from this file.
    static func root() -> URL? {
        var dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        for _ in 0..<10 {
            if FileManager.default.fileExists(atPath: dir.appendingPathComponent("plans/brand/marks/mark.svg").path) { return dir }
            dir = dir.deletingLastPathComponent()
        }
        return nil
    }

    static func read(_ path: String, in root: URL) throws -> String {
        try String(contentsOf: root.appendingPathComponent(path), encoding: .utf8)
    }

    /// Every `<path …>` element's `fill` and `d`, in order.
    static func paths(_ svg: String) -> [(fill: String, d: String)] {
        let re = try! NSRegularExpression(pattern: #"<path\b([^>]*)/?>"#)
        return re.matches(in: svg, range: NSRange(svg.startIndex..., in: svg)).compactMap { m in
            guard let r = Range(m.range(at: 1), in: svg) else { return nil }
            let attrs = String(svg[r])
            return (attribute("fill", in: attrs) ?? "", attribute("d", in: attrs) ?? "")
        }
    }

    static func attribute(_ name: String, in attrs: String) -> String? {
        let re = try! NSRegularExpression(pattern: #"(?:^|\s)"# + name + #"="([^"]*)""#)
        guard let m = re.firstMatch(in: attrs, range: NSRange(attrs.startIndex..., in: attrs)),
              let r = Range(m.range(at: 1), in: attrs) else { return nil }
        return String(attrs[r])
    }

    static func hex(_ v: UInt32) -> String { String(format: "#%06X", v) }

    @Test func theMarkIsTheMaster() throws {
        guard let root = Self.root() else { return } // no source tree here
        let mark = try Self.read("plans/brand/marks/mark.svg", in: root)
        #expect(mark.contains(#"viewBox="0 0 1024 1024""#))
        #expect(XbinBrandPaths.tile == .init(x: 0, y: 0, width: 1024, height: 1024))
        #expect(mark.contains(#"<rect width="1024" height="1024" fill="\#(Self.hex(XbinPalette.markTile))"/>"#),
                "the tile is \(Self.hex(XbinPalette.markTile))")
        let p = Self.paths(mark)
        try #require(p.count == 2, "mark.svg: the b and the x")
        #expect(p[0].d == XbinBrandPaths.b, "XbinBrandPaths.b is mark.svg's b")
        #expect(p[0].fill == Self.hex(XbinPalette.markB))
        #expect(p[1].d == XbinBrandPaths.x, "XbinBrandPaths.x is mark.svg's x")
        #expect(p[1].fill == Self.hex(XbinPalette.markX))
    }

    @Test func theWordmarkIsTheMaster() throws {
        guard let root = Self.root() else { return }
        let word = try Self.read("plans/brand/marks/wordmark-a.svg", in: root)
        #expect(word.contains(#"viewBox="0 -754 1952 768""#))
        #expect(XbinBrandPaths.wordmark == .init(x: 0, y: -754, width: 1952, height: 768))
        let p = Self.paths(word)
        try #require(p.count == 1, "wordmark-a.svg: one path")
        #expect(p[0].d == XbinBrandPaths.word, "XbinBrandPaths.word is wordmark A")
    }

    @Test func theIconIsTheMark() throws {
        guard let root = Self.root() else { return }
        let dir = "native/ios/App/Resources/AppIcon.icon"
        for (file, d, fill) in [("b.svg", XbinBrandPaths.b, XbinPalette.markB), ("x.svg", XbinBrandPaths.x, XbinPalette.markX)] {
            let svg = try Self.read("\(dir)/Assets/\(file)", in: root)
            #expect(svg.contains(#"viewBox="0 0 1024 1024""#), "\(file) is on the tile's 1024 canvas")
            let p = Self.paths(svg)
            #expect(p.count == 1 && p.first?.d == d, "\(file) is the master's drawing")
            #expect(p.first?.fill == Self.hex(fill), "\(file) is \(Self.hex(fill))")
        }
        // Every fill of the icon, in each appearance, is the tile's cobalt
        // (Icon Composer's sRGB components, 5 places).
        let json = try Self.read("\(dir)/icon.json", in: root)
        let (r, g, b) = XbinPalette.components(XbinPalette.markTile)
        let cobalt = String(format: "srgb:%.5f,%.5f,%.5f,1.00000", r, g, b)
        let re = try NSRegularExpression(pattern: #""solid" : "([^"]*)""#)
        let fills = re.matches(in: json, range: NSRange(json.startIndex..., in: json)).compactMap { Range($0.range(at: 1), in: json).map { String(json[$0]) } }
        #expect(!fills.isEmpty, "icon.json has a solid fill")
        for f in fills { #expect(f == cobalt, "icon.json's fill \(f) is the tile's \(cobalt)") }
    }

    @Test func theCodeFaceDrawsNoLigatures() throws {
        guard let root = Self.root() else { return }
        for face in ["JetBrainsMono-Regular", "JetBrainsMono-Bold"] {
            let data = try Data(contentsOf: root.appendingPathComponent("native/ios/App/Resources/Fonts/\(face).ttf"))
            let tags = Self.gsubFeatures(data)
            #expect(tags.contains("ccmp"), "\(face): read its GSUB features (\(tags.sorted()))")
            #expect(!tags.contains("calt") && !tags.contains("liga"),
                    "\(face) still has its ligatures: run native/ios/scripts/app-fonts.sh")
        }
    }

    /// The feature tags of a TrueType font's GSUB table.
    static func gsubFeatures(_ font: Data) -> Set<String> {
        let b = [UInt8](font)
        func u16(_ o: Int) -> Int { o + 1 < b.count ? Int(b[o]) << 8 | Int(b[o + 1]) : 0 }
        func u32(_ o: Int) -> Int { u16(o) << 16 | u16(o + 2) }
        func tag(_ o: Int) -> String { o + 3 < b.count ? String(decoding: b[o..<o + 4], as: UTF8.self) : "" }
        var gsub = 0
        for i in 0..<u16(4) where tag(12 + 16 * i) == "GSUB" { gsub = u32(12 + 16 * i + 8) }
        guard gsub > 0 else { return [] }
        let list = gsub + u16(gsub + 6)
        return Set((0..<u16(list)).map { tag(list + 2 + 6 * $0) })
    }
}
