import SwiftTerm
import UIKit
import XbinRendererModel

/// How every terminal looks (D185): Concrete Night's terminal palette —
/// the background, foreground, cursor in the accent, selection and the 16
/// ANSI colours of product-ui 7, the web terminal's "Concrete Night" — in
/// both appearances (the phone's terminal stays dark, with its dark keyboard
/// and key row), in JetBrains Mono with its bold for bold: weight, never a
/// brighter colour. The shell's terminal (TerminalController) and a native
/// tile's (TileTerminal) both take it.
@MainActor
enum TerminalLook {
    /// The four faces at `size`: JetBrains Mono regular and bold (the app
    /// bundles no italic: italic text is drawn upright), else the system's
    /// monospace when the faces aren't registered.
    static func fonts(size: CGFloat) -> (normal: UIFont, bold: UIFont) {
        let normal = UIFont(name: XbinFaces.mono, size: size) ?? .monospacedSystemFont(ofSize: size, weight: .regular)
        let bold = UIFont(name: XbinFaces.monoBold, size: size) ?? .monospacedSystemFont(ofSize: size, weight: .bold)
        return (normal, bold)
    }

    static func setFont(_ view: TerminalView, size: CGFloat) {
        let f = fonts(size: size)
        view.setFonts(normal: f.normal, bold: f.bold, italic: f.normal, boldItalic: f.bold)
    }

    static func apply(_ view: TerminalView, size: CGFloat) {
        setFont(view, size: size)
        let t = XbinPalette.Terminal.self
        view.nativeBackgroundColor = UIColor(terminalHex: t.background)
        view.nativeForegroundColor = UIColor(terminalHex: t.foreground)
        view.caretColor = UIColor(terminalHex: t.cursor)
        view.caretTextColor = UIColor(terminalHex: t.cursorInk)
        view.selectedTextBackgroundColor = UIColor(terminalHex: t.selection)
        view.useBrightColors = false
        view.installColors(t.ansi.map(color))
    }

    /// A palette entry as SwiftTerm's 16-bit colour.
    static func color(_ hex: UInt32) -> SwiftTerm.Color {
        let c = { (shift: UInt32) in UInt16((hex >> shift) & 0xFF) * 257 }
        return SwiftTerm.Color(red: c(16), green: c(8), blue: c(0))
    }
}

extension UIColor {
    /// An sRGB colour from `0xRRGGBB` (the terminal's palette).
    convenience init(terminalHex hex: UInt32) {
        let (r, g, b) = XbinPalette.components(hex)
        self.init(red: CGFloat(r), green: CGFloat(g), blue: CGFloat(b), alpha: 1)
    }
}
