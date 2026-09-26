import UIKit
import XbinTerm

/// The precise selection mode's touch layer (plans/native.md §12), over the
/// terminal while the mode is on. One finger selects from where it lands
/// (a drag that starts on a handle moves that end instead), the system loupe
/// shows what is under the finger, dragging to the top or bottom edge scrolls,
/// and two fingers scroll the scrollback. SwiftTerm's own gestures never see
/// these touches, so a drag can't turn into a scroll or a mouse report.
@MainActor
final class SelectionOverlayView: UIView, UIGestureRecognizerDelegate {
    weak var controller: TerminalController?
    private var press: UILongPressGestureRecognizer?
    private var scroll: UIPanGestureRecognizer?
    private var loupe: UITextLoupeSession?
    private var autoscroll: Timer?
    private var autoscrollStep = 0
    private var lastPoint = CGPoint.zero
    private var before: TermSelection?
    private var scrolling = false
    private var scrollCarry: CGFloat = 0

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .clear
        isHidden = true
        let press = UILongPressGestureRecognizer(target: self, action: #selector(pressed(_:)))
        press.minimumPressDuration = 0
        press.allowableMovement = .greatestFiniteMagnitude
        press.delegate = self
        addGestureRecognizer(press)
        self.press = press
        let scroll = UIPanGestureRecognizer(target: self, action: #selector(scrolled(_:)))
        scroll.minimumNumberOfTouches = 2
        scroll.maximumNumberOfTouches = 2
        scroll.delegate = self
        addGestureRecognizer(scroll)
        self.scroll = scroll
        accessibilityLabel = "Text selection"
        accessibilityHint = "Drag to select. Two fingers scroll."
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        true
    }

    /// The mode ends: no loupe or autoscroll left running.
    func endInteraction() {
        stopAutoscroll()
        loupe?.invalidate()
        loupe = nil
        before = nil
        scrolling = false
    }

    // MARK: One finger: select

    @objc private func pressed(_ g: UILongPressGestureRecognizer) {
        guard let c = controller else { return }
        let p = g.location(in: self)
        switch g.state {
        case .began:
            before = c.selection
            lastPoint = p
            c.selectionBegan(at: cell(at: p), tolerance: handleTolerance())
            startLoupe(at: p)
        case .changed:
            guard !scrolling else { return }
            lastPoint = p
            c.selectionMoved(to: cell(at: p))
            moveLoupe(to: p)
            updateAutoscroll(at: p)
        case .ended:
            endInteraction()
        case .cancelled, .failed:
            if let b = before, !scrolling { c.restoreSelection(b) }
            endInteraction()
        default:
            break
        }
    }

    /// The model cell under a point of this layer.
    private func cell(at p: CGPoint) -> TermCell {
        guard let c = controller else { return TermCell(row: 0, col: 0) }
        return c.cell(atContentPoint: c.terminalView.convert(p, from: self))
    }

    /// A handle is "under the finger" within about 22 pt: a fingertip is
    /// wider than a cell.
    private func handleTolerance() -> (rows: Int, cols: Int) {
        let cs = controller?.cellSize ?? CGSize(width: 8, height: 16)
        let cols = max(1, Int((22 / max(cs.width, 1)).rounded(.up)))
        let rows = max(1, Int((22 / max(cs.height, 1)).rounded(.up)))
        return (rows, cols)
    }

    // MARK: The loupe

    private func startLoupe(at p: CGPoint) {
        guard let host = superview else { return }
        loupe?.invalidate()
        loupe = UITextLoupeSession.begin(at: convert(p, to: host), fromSelectionWidgetView: nil, in: host)
    }

    private func moveLoupe(to p: CGPoint) {
        guard let host = superview else { return }
        loupe?.move(to: convert(p, to: host), withCaretRect: .null, trackingCaret: false)
    }

    // MARK: Dragging to an edge scrolls

    private func updateAutoscroll(at p: CGPoint) {
        let edge = max(controller?.cellSize.height ?? 16, 16)
        let step = p.y < edge ? -1 : (p.y > bounds.height - edge ? 1 : 0)
        guard step != autoscrollStep else { return }
        stopAutoscroll()
        autoscrollStep = step
        guard step != 0 else { return }
        autoscroll = Timer.scheduledTimer(withTimeInterval: 0.06, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.autoscrollTick() }
        }
    }

    private func autoscrollTick() {
        guard let c = controller, autoscrollStep != 0 else { return stopAutoscroll() }
        if autoscrollStep < 0 { c.terminalView.scrollUp(lines: 1) } else { c.terminalView.scrollDown(lines: 1) }
        c.selectionMoved(to: cell(at: lastPoint))
    }

    private func stopAutoscroll() {
        autoscroll?.invalidate()
        autoscroll = nil
        autoscrollStep = 0
    }

    // MARK: Two fingers: scroll

    @objc private func scrolled(_ g: UIPanGestureRecognizer) {
        guard let c = controller else { return }
        switch g.state {
        case .began:
            // The first finger already started a selection: take it back.
            scrolling = true
            if let b = before { c.restoreSelection(b) }
            stopAutoscroll()
            loupe?.invalidate()
            loupe = nil
            scrollCarry = 0
        case .changed:
            scrollCarry += g.translation(in: self).y
            g.setTranslation(.zero, in: self)
            let h = max(c.cellSize.height, 1)
            let n = Int(scrollCarry / h)
            guard n != 0 else { return }
            scrollCarry -= CGFloat(n) * h
            // Content follows the fingers: down reveals older lines.
            if n > 0 { c.terminalView.scrollUp(lines: n) } else { c.terminalView.scrollDown(lines: -n) }
        default:
            scrolling = false
            scrollCarry = 0
        }
    }
}
