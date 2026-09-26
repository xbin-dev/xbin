import UIKit
import XbinTerm

/// The keyboard accessory row (plans/native.md §12): esc, sticky ctrl, tab,
/// arrows, `| ~ / -` and a customizable slot. Arrows repeat while held;
/// a horizontal or vertical swipe along the row sends arrow repeats. It sits
/// on top of the software keyboard, or alone at the bottom with a hardware
/// one, on a dark bar of its own (KeyRowBar: iOS 26's keyboard draws none
/// behind it).
@MainActor
final class AccessoryBar: KeyRowBar {
    private weak var controller: TerminalController?
    private var repeatTimer: Timer?
    private var swipeOrigin: CGPoint = .zero

    /// The user's extra key (TerminalKeyboardSettingsView), after the designed row.
    static var customKey: AccessoryKey? { TerminalPrefs.accessorySlot }

    init(controller: TerminalController) {
        self.controller = controller
        super.init(distribution: .fill) // widths by constraint (buildRow)
        buildRow()
        let pan = UIPanGestureRecognizer(target: self, action: #selector(swiped(_:)))
        pan.cancelsTouchesInView = false
        addGestureRecognizer(pan)
        NotificationCenter.default.addObserver(self, selector: #selector(keysChanged), name: .xbinTerminalKeysChanged, object: nil)
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    /// The designed row and the user's slot.
    private func buildRow() {
        repeatTimer?.invalidate()
        repeatTimer = nil
        removeKeys()
        let slot = Self.customKey
        var row = AccessoryKey.defaultRow
        if let slot { row.append(slot) }
        for key in row { add(key) }
        // Equal widths, but a slot whose cap is a word ("sudo") gets two.
        if let first = keys.first?.button {
            for (i, k) in keys.enumerated() where i > 0 {
                let wide = slot != nil && i == row.count - 1 && k.title.count > 3
                k.button.widthAnchor.constraint(equalTo: first.widthAnchor, multiplier: wide ? 2 : 1).isActive = true
            }
        }
        refresh()
        setNeedsLayout()
    }

    /// The slot was changed in the settings: redraw the row.
    @objc private func keysChanged() { buildRow() }

    private func add(_ key: AccessoryKey) {
        let tag = keys.count
        let b = addKey(key, title: AccessorySlot.capLabel(key))
        b.accessibilityLabel = AccessorySlot.describe(key)
        b.addAction(UIAction { [weak self] _ in self?.tap(key) }, for: .touchUpInside)
        if key.repeats {
            let hold = UILongPressGestureRecognizer(target: self, action: #selector(held(_:)))
            hold.minimumPressDuration = 0.35
            b.addGestureRecognizer(hold)
            b.tag = tag
        }
    }

    private func tap(_ key: AccessoryKey) {
        UIDevice.current.playInputClick()
        controller?.accessory(key)
        refresh()
    }

    override func stickyState(_ m: StickyModifier) -> StickyModifiers.State? { controller?.keyboard.sticky.state(m) }

    @objc private func held(_ g: UILongPressGestureRecognizer) {
        guard let b = g.view as? UIButton, keys.indices.contains(b.tag) else { return }
        let key = keys[b.tag].key
        switch g.state {
        case .began:
            repeatTimer?.invalidate()
            repeatTimer = Timer.scheduledTimer(withTimeInterval: 0.07, repeats: true) { [weak self] _ in
                MainActor.assumeIsolated { self?.controller?.accessory(key) }
            }
        case .ended, .cancelled, .failed:
            repeatTimer?.invalidate()
            repeatTimer = nil
        default: break
        }
    }

    /// Swiping along the row: one arrow per ~14 pt of travel.
    @objc private func swiped(_ g: UIPanGestureRecognizer) {
        switch g.state {
        case .began:
            swipeOrigin = .zero
        case .changed:
            let t = g.translation(in: self)
            let dx = t.x - swipeOrigin.x, dy = t.y - swipeOrigin.y
            let step: CGFloat = 14
            if abs(dx) >= step, abs(dx) >= abs(dy) {
                controller?.accessory(.key(dx > 0 ? .right : .left))
                swipeOrigin.x += dx > 0 ? step : -step
            } else if abs(dy) >= step {
                controller?.accessory(.key(dy > 0 ? .down : .up))
                swipeOrigin.y += dy > 0 ? step : -step
            }
        default: break
        }
    }
}

extension AccessoryBar: UIInputViewAudioFeedback {
    var enableInputClicksWhenVisible: Bool { true }
}
