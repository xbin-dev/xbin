import Foundation

/// The mark and wordmark A as drawings (D183, D185): the masters' own SVG
/// path data (plans/brand/marks/mark.svg, wordmark-a.svg), which the app
/// draws (App/Shell/BrandMark.swift: XbinMark, XbinWordmark, XbinLockup) so
/// the mark stays sharp at any size and needs no image asset. The app
/// icon's layers (App/Resources/AppIcon.icon/Assets) carry the same b and
/// x. BrandMastersTests holds all of them to the masters, as the web's
/// TestBrandMarkIsTheMasters does its copies: a change to a master fails
/// there until the app follows.
public enum XbinBrandPaths {
    /// An SVG viewBox.
    public struct Box: Sendable, Equatable {
        public let x, y, width, height: Double

        public init(x: Double, y: Double, width: Double, height: Double) {
            self.x = x
            self.y = y
            self.width = width
            self.height = height
        }
    }

    /// The tile's box (mark.svg's viewBox): 1024 square.
    public static let tile = Box(x: 0, y: 0, width: 1024, height: 1024)
    /// The white b (even-odd: its counter is the tile showing through).
    public static let b = "M128 128H288V448C304 404 356 376 440 376C572 376 672 482 672 640C672 798 572 904 440 904C356 904 308 884 288 856V896H128Z"
        + "M288 576C288 536 336 512 416 512C500 512 544 556 544 640C544 724 500 768 416 768C336 768 288 744 288 704Z"
    /// The yellow x, the exponent.
    public static let x = "M640 128H728L768 189L808 128H896L812 256L896 384H808L768 323L728 384H640L724 256Z"

    /// Wordmark A's box (wordmark-a.svg's viewBox 0 -754 1952 768).
    public static let wordmark = Box(x: 0, y: -754, width: 1952, height: 768)
    /// Wordmark A, "xbin" in Bricolage Grotesque 800 as outlines.
    public static let word = "M0 0 174 -264 1 -528H186L273 -334H292L379 -528H564L390 -264L565 0H381L292 -196H273L186 0Z "
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
