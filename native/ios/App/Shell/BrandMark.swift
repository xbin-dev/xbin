import SwiftUI
import XbinRenderer

// The mark and the wordmark (D183, plans/brand/marks): M3, the bˣ tile — a
// white b with a yellow x raised as its exponent on a square cobalt tile —
// and wordmark A, "xbin" in Bricolage Grotesque 800 as exact outlines. The
// paths are the masters' own `d` attributes, drawn here so the mark stays
// sharp at any size and needs no image asset. The tile never changes
// between themes; the wordmark's ink follows the text colour. The app icon
// is the same drawing (App/Resources/AppIcon.icon).

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

/// The masters' drawings (plans/brand/marks/mark.svg, wordmark-a.svg).
enum BrandPaths {
    static let tile = CGRect(x: 0, y: 0, width: 1024, height: 1024)
    /// The white b (even-odd: its counter is the tile showing through).
    static let b = "M128 128H288V448C304 404 356 376 440 376C572 376 672 482 672 640C672 798 572 904 440 904C356 904 308 884 288 856V896H128Z"
        + "M288 576C288 536 336 512 416 512C500 512 544 556 544 640C544 724 500 768 416 768C336 768 288 744 288 704Z"
    /// The yellow x, the exponent.
    static let x = "M640 128H728L768 189L808 128H896L812 256L896 384H808L768 323L728 384H640L724 256Z"

    /// Wordmark A's box (viewBox 0 -754 1952 768).
    static let wordmark = CGRect(x: 0, y: -754, width: 1952, height: 768)
    static let word = "M0 0 174 -264 1 -528H186L273 -334H292L379 -528H564L390 -264L565 0H381L292 -196H273L186 0Z "
        + "M913.3 14Q863.3 14 825.8 -5Q788.3 -24 764.8 -62Q741.3 -100 733.3 -156H714.3L711.3 0H579.3V-259V-720H740.3V-545"
        + "Q740.3 -521 736.8 -494.5Q733.3 -468 727.8 -439Q722.3 -410 716.3 -377H739.3Q753.3 -431 776.3 -467Q799.3 -503 833.8 -521.5"
        + "Q868.3 -540 915.3 -540Q981.3 -540 1029.8 -506Q1078.3 -472 1105.3 -409.5Q1132.3 -347 1132.3 -260Q1132.3 -176 1105.8 -114.5"
        + "Q1079.3 -53 1030.3 -19.5Q981.3 14 913.3 14ZM853.3 -116Q886.3 -116 911.3 -134.5Q936.3 -153 949.8 -186.5Q963.3 -220 963.3 -265"
        + "Q963.3 -311 950.3 -343.5Q937.3 -376 913.8 -394Q890.3 -412 857.3 -412Q835.3 -412 816.8 -404Q798.3 -396 784.3 -382"
        + "Q770.3 -368 760.3 -350.5Q750.3 -333 745.3 -313.5Q740.3 -294 740.3 -275V-254Q740.3 -233 746.3 -209Q752.3 -185 765.8 -164"
        + "Q779.3 -143 800.8 -129.5Q822.3 -116 853.3 -116Z "
        + "M1184.8 0V-528H1345.8V0ZM1265.8 -602Q1219.8 -602 1195.3 -621.5Q1170.8 -641 1170.8 -677Q1170.8 -715 1195.3 -734.5"
        + "Q1219.8 -754 1265.8 -754Q1312.8 -754 1337.3 -734.5Q1361.8 -715 1361.8 -678Q1361.8 -641 1337.3 -621.5Q1312.8 -602 1265.8 -602Z "
        + "M1425.2 0V-318V-528H1555.2L1557.2 -373H1577.2Q1590.2 -429 1614.2 -467Q1638.2 -505 1675.2 -523.5Q1712.2 -542 1762.2 -542"
        + "Q1855.2 -542 1903.7 -477Q1952.2 -412 1952.2 -270V0H1790.2V-252Q1790.2 -334 1766.7 -371.5Q1743.2 -409 1697.2 -409"
        + "Q1659.2 -409 1634.7 -386Q1610.2 -363 1598.2 -325Q1586.2 -287 1586.2 -240V0Z"
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
