import UIKit
import XbinTerm

/// The keyboard accessory row (plans/native.md §12): esc, sticky ctrl, tab,
/// arrows, `| ~ / -` and a customizable slot. Arrows repeat while held;
/// a horizontal or vertical swipe along the row sends arrow repeats.
@MainActor
final class AccessoryBar: UIInputView {
    private weak var controller: TerminalController?
    private let stack = UIStackView()
    private var buttons: [(AccessoryKey, UIButton)] = []
    private var repeatTimer: Timer?
    private var swipeOrigin: CGPoint = .zero

    /// The user's extra key (Settings), after the designed row.
    static var customKey: AccessoryKey? {
        guard let d = UserDefaults.standard.data(forKey: "termCustomKey") else { return .key(.function(1)) }
        return try? JSONDecoder().decode(AccessoryKey.self, from: d)
    }

    init(controller: TerminalController) {
        self.controller = controller
        super.init(frame: CGRect(x: 0, y: 0, width: 320, height: 46), inputViewStyle: .keyboard)
        allowsSelfSizing = true
        stack.axis = .horizontal
        stack.distribution = .fillEqually
        stack.spacing = 5
        stack.translatesAutoresizingMaskIntoConstraints = false
        addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: layoutMarginsGuide.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: layoutMarginsGuide.trailingAnchor),
            stack.topAnchor.constraint(equalTo: topAnchor, constant: 5),
            stack.bottomAnchor.constraint(equalTo: bottomAnchor, constant: -5),
            heightAnchor.constraint(equalToConstant: 46),
        ])
        var row = AccessoryKey.defaultRow
        if let c = Self.customKey { row.append(c) }
        for key in row { add(key) }
        let pan = UIPanGestureRecognizer(target: self, action: #selector(swiped(_:)))
        pan.cancelsTouchesInView = false
        addGestureRecognizer(pan)
        refresh()
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    private func add(_ key: AccessoryKey) {
        var cfg = UIButton.Configuration.gray()
        cfg.title = key.label
        cfg.contentInsets = NSDirectionalEdgeInsets(top: 4, leading: 2, bottom: 4, trailing: 2)
        cfg.titleTextAttributesTransformer = UIConfigurationTextAttributesTransformer { a in
            var a = a
            a.font = UIFont.monospacedSystemFont(ofSize: 14, weight: .medium)
            return a
        }
        let b = UIButton(configuration: cfg)
        b.accessibilityLabel = key.label
        b.addAction(UIAction { [weak self] _ in self?.tap(key) }, for: .touchUpInside)
        if key.repeats {
            let hold = UILongPressGestureRecognizer(target: self, action: #selector(held(_:)))
            hold.minimumPressDuration = 0.35
            b.addGestureRecognizer(hold)
            b.tag = buttons.count
        }
        buttons.append((key, b))
        stack.addArrangedSubview(b)
    }

    private func tap(_ key: AccessoryKey) {
        UIDevice.current.playInputClick()
        controller?.accessory(key)
        refresh()
    }

    /// Sticky ctrl/alt show their state: off, once (tinted), locked (filled).
    func refresh() {
        guard let kb = controller?.keyboard else { return }
        for (key, b) in buttons {
            guard case .modifier(let m) = key else { continue }
            var cfg = b.configuration ?? .gray()
            switch kb.sticky.state(m) {
            case .off: cfg.baseBackgroundColor = nil; cfg.baseForegroundColor = nil
            case .once: cfg.baseBackgroundColor = .systemOrange.withAlphaComponent(0.35); cfg.baseForegroundColor = .label
            case .locked: cfg.baseBackgroundColor = .systemOrange; cfg.baseForegroundColor = .black
            }
            b.configuration = cfg
        }
    }

    @objc private func held(_ g: UILongPressGestureRecognizer) {
        guard let b = g.view as? UIButton, buttons.indices.contains(b.tag) else { return }
        let key = buttons[b.tag].0
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
