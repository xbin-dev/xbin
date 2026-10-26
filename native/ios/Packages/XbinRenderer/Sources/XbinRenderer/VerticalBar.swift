#if canImport(UIKit)
import SwiftUI
import UIKit

// The iPhone Duo's vertical bar (D200). Open, the Duo's inner screen is
// wide and short; iOS 27.1 can draw the navigation bar's items in a bar
// down the screen's side (the system picks the edge, `verticalBarEdge`)
// instead of across its top, which frees the height for content.
//
// SDK, as the iOS 27.1 SDK (Xcode 27.1, 27A9269) declares it:
//   SwiftUI  ToolbarContent.axisBehavior(_: ToolbarItemAxisBehavior)
//            (.automatic · .horizontalOnly · .verticalPreferred)
//   UIKit    UITraitCollection.verticalBarEdge (.unspecified where the
//            system never places a vertical bar: other hardware, or a size
//            class or orientation without one) and
//            systemTraitsAffectingVerticalBarEdge
// Compiled only with XBIN_SDK_27_1 (ios.yml's `duo` job, `XBIN_XCODE` the
// 27.1 Xcode); every other build keeps today's bars exactly.

extension ToolbarContent {
    /// Items the person reaches all the time (the workspace switcher, Back,
    /// the tile's ⋯ and its toolbar): on an open Duo they prefer the
    /// vertical bar at the screen's side; wherever there is no vertical bar
    /// they stay at the top as before.
    @ToolbarContentBuilder
    public func preferVerticalBar() -> some ToolbarContent {
        #if XBIN_SDK_27_1
        if #available(iOS 27.1, *) {
            axisBehavior(.verticalPreferred)
        } else {
            self
        }
        #else
        self
        #endif
    }
}

#if XBIN_SDK_27_1
/// Whether the system places a vertical bar where this view is (an open
/// Duo): reported on appearance and whenever the traits that decide it
/// change. The two-pane `split` arranges itself for the hinge only there.
@available(iOS 27.1, *)
struct VerticalBarProbe: UIViewRepresentable {
    @Binding var present: Bool

    func makeUIView(context: Context) -> ProbeView {
        let v = ProbeView()
        v.isUserInteractionEnabled = false
        v.report = { p in if present != p { present = p } }
        return v
    }

    func updateUIView(_ v: ProbeView, context: Context) {
        v.report = { p in if present != p { present = p } }
    }

    final class ProbeView: UIView {
        var report: ((Bool) -> Void)?
        private var registered = false

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if !registered, window != nil {
                registered = true
                registerForTraitChanges(UITraitCollection.systemTraitsAffectingVerticalBarEdge) { (v: ProbeView, _: UITraitCollection) in
                    v.send()
                }
            }
            send()
        }

        private func send() {
            let present = traitCollection.verticalBarEdge != .unspecified
            // Never during SwiftUI's update of the view that owns the state.
            DispatchQueue.main.async { [weak self] in self?.report?(present) }
        }
    }
}
#endif
#endif
