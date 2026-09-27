import SwiftUI
import UIKit
import XbinCore

/// Whether the panel a view is in is the one shown (the top of its
/// window's stack): a panel under it or kept for forward is still there,
/// alive, but not in front.
extension EnvironmentValues {
    var panelActive: Bool {
        get { self[PanelActiveKey.self] }
        set { self[PanelActiveKey.self] = newValue }
    }
}

private struct PanelActiveKey: EnvironmentKey {
    static let defaultValue = true
}

/// A window's panels side by side (plans/native.md §4, D117): Home → a
/// screen → a tile, terminal or agent, each a full-screen panel with its
/// own bar. The panel under the top one stays put under it, and a back
/// that leaves a panel keeps it, off to the right, for forward:
///
/// - **Back** is a pan from the left edge. It is interactive — the top
///   panel follows the finger over the one below (a peek) — and commits
///   past about a third of the width, or on a flick; otherwise it snaps
///   back.
/// - **Forward** is a pan from the right edge, only while there is a panel
///   a back left (WorkspaceNav's forward memory), pulling it back in.
/// - **The inner stack first**: when what's under the finger is in a
///   navigation stack that can pop (a native tile's own `nav` screens, a
///   tile's `xbin.window`), that stack's own swipe goes back instead.
///
/// Panels below the top don't take touches and are hidden from
/// accessibility; the top one gets `panelActive`.
struct PanelStack<Content: View>: View {
    @Bindable var nav: WorkspaceNav
    /// No swipes while something covers the panels (the switcher, the lock).
    var enabled: Bool
    @ViewBuilder let content: (Panel) -> Content

    @State private var drag = PanelDrag()

    static var animation: Animation { .smooth(duration: 0.32) }

    var body: some View {
        GeometryReader { geo in
            let w = max(geo.size.width, 1)
            let top = nav.depth - 1
            ZStack {
                ForEach(Array(nav.entries.enumerated()), id: \.element.id) { i, e in
                    let shown = shown(i, top: top)
                    content(e.panel)
                        .background(Color(uiColor: .systemBackground))
                        .overlay { Color.black.opacity(dim(i, top: top, width: w)).allowsHitTesting(false) }
                        .offset(x: offset(i, top: top, width: w))
                        // Only what can be seen is drawn — and reachable: a panel
                        // under the top one or kept for forward is transparent
                        // (its bar too, which accessibilityHidden doesn't reach),
                        // once a slide away from it has ended.
                        .opacity(shown ? 1 : 0)
                        .animation(shown ? nil : .linear(duration: 0).delay(0.4), value: shown)
                        .zIndex(Double(i))
                        .allowsHitTesting(i == top && drag.kind == nil)
                        .accessibilityHidden(i != top)
                        .environment(\.panelActive, i == top)
                        .transition(.move(edge: .trailing))
                }
            }
            .background(EdgePans(canBack: nav.canGoBack, canForward: nav.canGoForward, enabled: enabled && drag.settling == false) {
                pan($0, width: w)
            })
        }
        .onAppear {
            nav.animate = { change in withAnimation(Self.animation) { change() } }
        }
    }

    // MARK: Where each panel sits

    /// The top panel at 0; those under it a little to the left (they slide
    /// back in as it leaves); the forward memory off to the right.
    private func offset(_ i: Int, top: Int, width w: CGFloat) -> CGFloat {
        let under = -w * 0.3
        switch drag.kind {
        case .back?:
            if i == top { return drag.x }
            if i == top - 1 { return under * (1 - drag.x / w) }
        case .forward?:
            if i == top + 1 { return w + drag.x }
            if i == top { return under * (-drag.x / w) }
        case nil:
            break
        }
        if i < top { return under }
        if i == top { return 0 }
        return w + 12
    }

    /// The top panel, and the one a swipe is uncovering.
    private func shown(_ i: Int, top: Int) -> Bool {
        switch drag.kind {
        case .back?: return i == top || i == top - 1
        case .forward?: return i == top || i == top + 1
        case nil: return i == top
        }
    }

    /// The shade over the panel being uncovered.
    private func dim(_ i: Int, top: Int, width w: CGFloat) -> Double {
        switch drag.kind {
        case .back?: return i == top - 1 ? 0.12 * Double(1 - drag.x / w) : 0
        case .forward?: return i == top ? 0.12 * Double(-drag.x / w) : 0
        case nil: return 0
        }
    }

    // MARK: The swipes

    private func pan(_ event: EdgePans.Event, width w: CGFloat) {
        switch event {
        case .began(let kind):
            still { drag = PanelDrag(kind: kind, x: 0) }
        case .moved(let x):
            still {
                drag.x = drag.kind == .back ? min(max(x, 0), w) : min(max(x, -w), 0)
            }
        case .ended(let x, let velocity):
            guard let kind = drag.kind else { return }
            let commit = kind == .back ? (x > w * 0.35 || (velocity > 700 && x > 12))
                                       : (-x > w * 0.35 || (velocity < -700 && x < -12))
            drag.settling = true
            if commit {
                withAnimation(.smooth(duration: 0.22)) {
                    drag.x = kind == .back ? w : -w
                } completion: {
                    if kind == .back { UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil) }
                    still {
                        let a = nav.animate
                        nav.animate = nil
                        if kind == .back { nav.back() } else { nav.forward() }
                        nav.animate = a
                        drag = PanelDrag()
                    }
                }
            } else {
                withAnimation(.smooth(duration: 0.2)) { drag.x = 0 } completion: { still { drag = PanelDrag() } }
            }
        case .cancelled:
            withAnimation(.smooth(duration: 0.2)) { drag.x = 0 } completion: { still { drag = PanelDrag() } }
        }
    }

    /// A change that follows the finger (or lands where it already is):
    /// never animated.
    private func still(_ change: () -> Void) {
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t, change)
    }
}

/// A swipe in progress.
struct PanelDrag: Equatable {
    enum Kind { case back, forward }
    var kind: Kind?
    /// The finger's travel: 0…width for back, -width…0 for forward.
    var x: CGFloat = 0
    /// Released and animating to where it lands: no new swipe meanwhile.
    var settling = false
}

/// The two edge pans, on the window (UIKit's edge recognizers see the
/// screen's edges; a SwiftUI drag would fight every scroll view and web
/// page). They start only on this stack's area, with no sheet up, when
/// there is somewhere to go — and back never where an inner navigation
/// stack under the finger can pop. Scroll views and web pages wait for
/// them to fail near the edge, as they do for UINavigationController's.
struct EdgePans: UIViewRepresentable {
    enum Event {
        case began(PanelDrag.Kind)
        case moved(CGFloat)
        case ended(CGFloat, velocity: CGFloat)
        case cancelled
    }

    var canBack: Bool
    var canForward: Bool
    var enabled: Bool
    let handle: (Event) -> Void

    func makeUIView(context: Context) -> Installer { Installer(self) }
    func updateUIView(_ uiView: Installer, context: Context) { uiView.config = self }

    final class Installer: UIView {
        var config: EdgePans
        private let left = UIScreenEdgePanGestureRecognizer(target: nil, action: nil)
        private let right = UIScreenEdgePanGestureRecognizer(target: nil, action: nil)
        // (UIView has a gestureRecognizerShouldBegin of its own: the
        // recognizers' delegate is a separate object.)
        private lazy var rules = Rules(self)

        init(_ config: EdgePans) {
            self.config = config
            super.init(frame: .zero)
            isUserInteractionEnabled = false
            for (r, edge) in [(left, UIRectEdge.left), (right, UIRectEdge.right)] {
                r.edges = edge
                r.delegate = rules
                r.addTarget(self, action: #selector(panned(_:)))
            }
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            for r in [left, right] { r.view?.removeGestureRecognizer(r) }
            guard let window else { return }
            window.addGestureRecognizer(left)
            window.addGestureRecognizer(right)
        }

        @objc private func panned(_ r: UIScreenEdgePanGestureRecognizer) {
            let x = r.translation(in: window).x
            switch r.state {
            case .began: config.handle(.began(r === left ? .back : .forward))
            case .changed: config.handle(.moved(x))
            case .ended: config.handle(.ended(x, velocity: r.velocity(in: window).x))
            case .cancelled, .failed: config.handle(.cancelled)
            default: break
            }
        }

        func shouldBegin(_ g: UIGestureRecognizer) -> Bool {
            guard config.enabled, let window, window.rootViewController?.presentedViewController == nil,
                  bounds.contains(g.location(in: self)) else { return false }
            if g === right { return config.canForward }
            guard config.canBack else { return false }
            return !Self.innerStackCanPop(in: window, at: g.location(in: window))
        }

        /// Whether a navigation stack holding what's under `point` has
        /// something to pop: its own swipe goes back first.
        static func innerStackCanPop(in window: UIWindow, at point: CGPoint) -> Bool {
            var r: UIResponder? = window.hitTest(point, with: nil)
            while let cur = r {
                if let nc = cur as? UINavigationController, nc.viewControllers.count > 1 { return true }
                r = cur.next
            }
            return false
        }

        final class Rules: NSObject, UIGestureRecognizerDelegate {
            weak var installer: Installer?
            init(_ installer: Installer) { self.installer = installer }

            func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool { installer?.shouldBegin(g) ?? false }

            /// Pans of scroll views and pages wait for an edge pan to fail
            /// (at once, away from the edge).
            func gestureRecognizer(_ g: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer) -> Bool {
                other is UIPanGestureRecognizer && !(other is UIScreenEdgePanGestureRecognizer) && other.view?.window === g.view
            }
        }
    }
}
