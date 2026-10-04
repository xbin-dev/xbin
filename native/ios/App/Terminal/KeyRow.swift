import UIKit
import XbinRenderer
import XbinTerm

/// How the terminal's key rows look (plans/native.md §12): the terminal's
/// AccessoryBar and a native tile's TileTermAccessory, both a KeyRowBar.
///
/// Since iOS 26 the software keyboard floats on its own glass, and a
/// `.keyboard`-style UIInputView no longer draws the keyboard's backdrop
/// behind an input accessory. The row stood transparent over the black
/// terminal in the system's appearance — in light mode faint grey keys with
/// black labels, "esc"/"ctrl"/"tab" clipped — and read as something behind
/// the keyboard rather than a row on top of it. So the row draws its own
/// bar, in the terminal's dark appearance (the terminal asks for the dark
/// keyboard to match: `keyboardAppearance`), with opaque keys whose labels
/// fit them. The same bar is what a hardware keyboard leaves on screen.
@MainActor
enum KeyRow {
    /// The keys' height, and the bar's margin around them: a 46 pt bar.
    static let keyHeight: CGFloat = 36
    static let margin: CGFloat = 5
    static var height: CGFloat { keyHeight + 2 * margin }
    /// Concrete Night's terminal chrome in both appearances (the terminal
    /// is dark either way, D185): the bar, the keys on it, their labels.
    static let barColor = UIColor(xbinHex: XbinPalette.Terminal.bar)
    static let keyColor = UIColor(xbinHex: XbinPalette.Terminal.key)
    static let labelColor = UIColor(xbinHex: XbinPalette.Terminal.foreground)
    /// A sticky modifier in force: the terminal's accent (its cursor).
    static let stickyColor = UIColor(xbinHex: XbinPalette.Terminal.cursor)
    static let stickyInk = UIColor(xbinHex: XbinPalette.Terminal.cursorInk)
    /// Word caps ("esc", "ctrl", a slot's "sudo") shrink to fit down to this.
    static let minimumFont: CGFloat = 9

    /// A key cap showing `title` at `size` pt.
    static func configuration(_ title: String, size: CGFloat) -> UIButton.Configuration {
        var cfg = UIButton.Configuration.filled()
        cfg.title = title
        cfg.baseBackgroundColor = keyColor
        cfg.baseForegroundColor = labelColor
        // Base Two's keys are plates: square corners.
        cfg.cornerStyle = .fixed
        cfg.background.cornerRadius = XbinShapes.radius
        cfg.contentInsets = NSDirectionalEdgeInsets(top: 4, leading: 2, bottom: 4, trailing: 2)
        cfg.titleLineBreakMode = .byClipping
        cfg.titleTextAttributesTransformer = UIConfigurationTextAttributesTransformer { a in
            var a = a
            a.font = KeyRow.font(size)
            return a
        }
        return cfg
    }

    /// A sticky ctrl/alt shows its state: off, once (tinted), locked (filled).
    static func sticky(_ state: StickyModifiers.State, _ cfg: inout UIButton.Configuration) {
        switch state {
        case .off: cfg.baseBackgroundColor = keyColor; cfg.baseForegroundColor = labelColor
        case .once: cfg.baseBackgroundColor = stickyColor.withAlphaComponent(0.45); cfg.baseForegroundColor = labelColor
        case .locked: cfg.baseBackgroundColor = stickyColor; cfg.baseForegroundColor = stickyInk
        }
    }

    /// A cap's face: the terminal's, JetBrains Mono (the system's
    /// monospace where it isn't registered).
    nonisolated static func font(_ size: CGFloat) -> UIFont {
        XbinFont.uiFont(XbinFaces.mono, size: size, fallbackWeight: .medium, monospaced: true)
    }

    /// The preferred size of a cap: glyphs (arrows, `|`) 14 pt, words 13.
    static func preferredFont(_ title: String) -> CGFloat { title.count > 1 ? 13 : 14 }

    /// The size `title` is drawn at in a key `width` pt wide: its preferred
    /// size, or smaller until it fits (down to ``minimumFont``).
    static func fittingFont(_ title: String, width: CGFloat) -> CGFloat {
        var size = preferredFont(title)
        let room = width - 6 // the content insets and a hair
        guard room > 0 else { return minimumFont }
        while size > minimumFont {
            let w = (title as NSString).size(withAttributes: [.font: font(size)]).width
            if w <= room { break }
            size -= 0.5
        }
        return max(size, minimumFont)
    }

    /// The font size of each cap in keys this wide: word caps share the
    /// smallest that fits them all, so the row reads evenly.
    static func fonts(_ titles: [String], widths: [CGFloat]) -> [CGFloat] {
        var words = preferredFont("ww")
        for (t, w) in zip(titles, widths) where t.count > 1 { words = min(words, fittingFont(t, width: w)) }
        return zip(titles, widths).map { t, w in t.count > 1 ? words : fittingFont(t, width: w) }
    }
}

/// The bar a key row is (the terminal's AccessoryBar, a native tile's
/// TileTermAccessory): dark and opaque whatever the system appearance, 46 pt
/// tall, its keys in `stack` a margin inside it, each cap fitted to its key,
/// a sticky modifier showing its state.
///
/// With a hardware keyboard UIKit docks the bar at the very bottom of the
/// screen, where its keys overlap the home indicator's strip, as the row
/// did before it had a bar of its own. They work there; lifting them above
/// the strip is open: UIKit measures this self-sizing accessory once and
/// ignores a later height (a constraint's constant, an intrinsic size),
/// while its safe area does report the strip (plans/native.md §26).
@MainActor
class KeyRowBar: UIInputView {
    let stack = UIStackView()
    /// The keys in order, with the cap each shows.
    private(set) var keys: [(key: AccessoryKey, title: String, button: UIButton)] = []
    private var fonts: [CGFloat] = []
    private var fittedWidths: [CGFloat] = []

    init(distribution: UIStackView.Distribution) {
        super.init(frame: CGRect(x: 0, y: 0, width: 320, height: KeyRow.height), inputViewStyle: .keyboard)
        allowsSelfSizing = true
        overrideUserInterfaceStyle = .dark
        backgroundColor = KeyRow.barColor
        stack.axis = .horizontal
        stack.distribution = distribution
        stack.spacing = 5
        stack.translatesAutoresizingMaskIntoConstraints = false
        addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: layoutMarginsGuide.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: layoutMarginsGuide.trailingAnchor),
            stack.topAnchor.constraint(equalTo: topAnchor, constant: KeyRow.margin),
            stack.bottomAnchor.constraint(equalTo: bottomAnchor, constant: -KeyRow.margin),
            heightAnchor.constraint(equalToConstant: KeyRow.height),
        ])
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    /// Appends a key showing `title`; its button takes the action.
    func addKey(_ key: AccessoryKey, title: String) -> UIButton {
        let b = UIButton(configuration: KeyRow.configuration(title, size: KeyRow.preferredFont(title)))
        keys.append((key, title, b))
        stack.addArrangedSubview(b)
        fittedWidths = []
        return b
    }

    func removeKeys() {
        for k in keys { k.button.removeFromSuperview() }
        keys = []
        fonts = []
        fittedWidths = []
    }

    /// The state a sticky modifier's cap shows; nil shows it off.
    func stickyState(_ m: StickyModifier) -> StickyModifiers.State? { nil }

    /// The caps at their fitted sizes; sticky ctrl/alt show their state:
    /// off, once (tinted), locked (filled).
    func refresh() {
        for (i, k) in keys.enumerated() {
            var cfg = KeyRow.configuration(k.title, size: fonts.indices.contains(i) ? fonts[i] : KeyRow.preferredFont(k.title))
            if case .modifier(let m) = k.key, let state = stickyState(m) { KeyRow.sticky(state, &cfg) }
            k.button.configuration = cfg
        }
    }

    /// The keys' widths are known: fit the caps to them (again only when
    /// they change — a rotation, the slot).
    override func layoutSubviews() {
        super.layoutSubviews()
        stack.layoutIfNeeded() // the keys' frames now, not after this pass
        let widths = keys.map { $0.button.bounds.width }
        guard widths != fittedWidths, !widths.isEmpty, widths.allSatisfy({ $0 > 0 }) else { return }
        fittedWidths = widths
        fonts = KeyRow.fonts(keys.map(\.title), widths: widths)
        refresh()
    }
}
