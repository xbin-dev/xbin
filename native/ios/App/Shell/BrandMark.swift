import SwiftUI
import XbinRenderer

// The mark and the wordmark (D183, plans/brand/marks): M3, the bˣ tile — a
// white b with a yellow x raised as its exponent on a square cobalt tile —
// and wordmark A, "xbin" in Bricolage Grotesque 800 as exact outlines. The
// paths are the masters' own `d` attributes (XbinBrandPaths, held to the
// masters by BrandMastersTests), drawn here so the mark stays sharp at any
// size and needs no image asset. The tile never changes between themes;
// the wordmark's ink follows the text colour. The app icon is the same
// drawing (App/Resources/AppIcon.icon).

/// SVG path data (the masters' subset: absolute M L H V C Q Z, a move's
/// extra pairs read as lines) scaled from a `viewBox` into a rect.
struct SVGPath: Shape {
    let d: String
    /// The drawing's own coordinate box (the SVG viewBox).
    let box: CGRect

    func path(in rect: CGRect) -> Path {
        let sx = rect.width / box.width, sy = rect.height / box.height
        let s = min(sx, sy)
        let ox = rect.minX + (rect.width - box.width * s) / 2 - box.minX * s
        let oy = rect.minY + (rect.height - box.height * s) / 2 - box.minY * s
        let pt = { (x: CGFloat, y: CGFloat) in CGPoint(x: ox + x * s, y: oy + y * s) }
        var p = Path()
        var cur = CGPoint.zero
        var cmd: Character = "M"
        let tokens = Self.tokens(d)
        var i = 0
        func num() -> CGFloat {
            defer { i += 1 }
            if case .number(let v)? = tokens[safe: i] { return v }
            return 0
        }
        while i < tokens.count {
            if case .command(let c) = tokens[i] { cmd = c; i += 1 }
            switch cmd {
            case "M":
                cur = CGPoint(x: num(), y: num())
                p.move(to: pt(cur.x, cur.y))
                cmd = "L" // further pairs are lines
            case "L":
                cur = CGPoint(x: num(), y: num())
                p.addLine(to: pt(cur.x, cur.y))
            case "H":
                cur.x = num()
                p.addLine(to: pt(cur.x, cur.y))
            case "V":
                cur.y = num()
                p.addLine(to: pt(cur.x, cur.y))
            case "C":
                let c1 = CGPoint(x: num(), y: num()), c2 = CGPoint(x: num(), y: num())
                cur = CGPoint(x: num(), y: num())
                p.addCurve(to: pt(cur.x, cur.y), control1: pt(c1.x, c1.y), control2: pt(c2.x, c2.y))
            case "Q":
                let c = CGPoint(x: num(), y: num())
                cur = CGPoint(x: num(), y: num())
                p.addQuadCurve(to: pt(cur.x, cur.y), control: pt(c.x, c.y))
            case "Z", "z":
                p.closeSubpath()
                // a Z takes no numbers: the next token is a command
                if case .number? = tokens[safe: i] { i += 1 }
            default:
                i += 1 // an unknown command's numbers: skipped
            }
        }
        return p
    }

    enum Token: Equatable { case command(Character), number(CGFloat) }

    /// Commands and numbers ("M128 128H288", "M0 0 174 -264", "1.5-2").
    static func tokens(_ d: String) -> [Token] {
        var out: [Token] = []
        var num = ""
        func flush() {
            if let v = Double(num) { out.append(.number(CGFloat(v))) }
            num = ""
        }
        for ch in d {
            if ch.isLetter, ch != "e" {
                flush()
                out.append(.command(ch))
            } else if ch == "-" {
                if !num.isEmpty, !num.hasSuffix("e") { flush() }
                num.append(ch)
            } else if ch.isNumber || ch == "." || ch == "e" {
                if ch == ".", num.contains("."), !num.contains("e") { flush() }
                num.append(ch)
            } else {
                flush()
            }
        }
        flush()
        return out
    }
}

private extension Array {
    subscript(safe i: Int) -> Element? { indices.contains(i) ? self[i] : nil }
}

/// The masters' drawings (XbinBrandPaths: plans/brand/marks/mark.svg,
/// wordmark-a.svg), with their boxes as CGRects.
enum BrandPaths {
    static let tile = rect(XbinBrandPaths.tile)
    static let b = XbinBrandPaths.b
    static let x = XbinBrandPaths.x
    static let wordmark = rect(XbinBrandPaths.wordmark)
    static let word = XbinBrandPaths.word

    static func rect(_ b: XbinBrandPaths.Box) -> CGRect { CGRect(x: b.x, y: b.y, width: b.width, height: b.height) }
}

/// The mark (M3): the cobalt tile, the white b, the yellow x — square, the
/// same in both themes.
struct XbinMark: View {
    var body: some View {
        ZStack {
            Rectangle().fill(Color(xbinHex: XbinPalette.markTile))
            SVGPath(d: BrandPaths.b, box: BrandPaths.tile).fill(Color(xbinHex: XbinPalette.markB), style: FillStyle(eoFill: true))
            SVGPath(d: BrandPaths.x, box: BrandPaths.tile).fill(Color(xbinHex: XbinPalette.markX))
        }
        .aspectRatio(1, contentMode: .fit)
        .accessibilityHidden(true)
    }
}

/// Wordmark A: "xbin" in the text colour (ink on light, near-white on dark).
struct XbinWordmark: View {
    var body: some View {
        SVGPath(d: BrandPaths.word, box: BrandPaths.wordmark)
            .fill(XbinColor.text)
            .aspectRatio(BrandPaths.wordmark.width / BrandPaths.wordmark.height, contentMode: .fit)
            .accessibilityElement()
            .accessibilityLabel(Text(verbatim: "xbin"))
    }
}

/// The lockup (lockup-a.svg): the tile, then the word on the b's baseline,
/// 2u between them; `height` is the tile's.
struct XbinLockup: View {
    var height: CGFloat = 40

    var body: some View {
        // lockup-a.svg: the tile is 1200 units, the word is scaled 1.136364
        // with its baseline 7/8 down the tile (on the b's) and its x-height
        // the b's; 2u (a quarter of the tile) between them. The word's box
        // (768 units, 14 of them under the baseline) is 0.7273 of the
        // tile's height and starts 0.161 of it below the tile's top.
        let k = height * 1.136364 / 1200
        HStack(alignment: .top, spacing: height / 4) {
            XbinMark().frame(width: height, height: height)
            XbinWordmark()
                .frame(width: 1952 * k, height: 768 * k)
                .padding(.top, height * 7 / 8 - 754 * k)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: "xbin"))
    }
}
