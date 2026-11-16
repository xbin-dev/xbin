#if canImport(UIKit)
import SwiftUI
import UIKit

/// Two panes side by side for the app's own chrome (D233): its panels and,
/// beside them, the workspace agent — a trailing column on an iPad, the
/// second pane of an open iPhone Duo.
///
/// The primary keeps its place and its identity whether the secondary shows
/// or not (its screens, web views and scroll positions are not rebuilt when
/// the agent opens). The secondary takes `fraction` of the width, at least
/// `minSecondary` and never so much that the primary drops under
/// `minPrimary`. Where the system draws its vertical bar — an open Duo
/// (D200; iOS 27.1, compiled with `XBIN_SDK_27_1`) — the panes are halves,
/// one each side of the hinge. (The Duo simulator boots closed — a compact
/// width, the agent a sheet — and no test can open it: built, not run.)
public struct XbinTwoPanes<Primary: View, Secondary: View>: View {
    private let showsSecondary: Bool
    private let fraction: CGFloat
    private let minSecondary: CGFloat
    private let minPrimary: CGFloat
    private let primary: Primary
    private let secondary: Secondary
    /// An open Duo: the system's vertical bar is here (``VerticalBarProbe``).
    @State private var verticalBar = false

    public init(showsSecondary: Bool, fraction: CGFloat = 0.4, minSecondary: CGFloat = 340, minPrimary: CGFloat = 320,
                @ViewBuilder primary: () -> Primary, @ViewBuilder secondary: () -> Secondary) {
        self.showsSecondary = showsSecondary
        self.fraction = fraction
        self.minSecondary = minSecondary
        self.minPrimary = minPrimary
        self.primary = primary()
        self.secondary = secondary()
    }

    public var body: some View {
        GeometryReader { geo in
            HStack(spacing: 0) {
                primary.frame(maxWidth: .infinity, maxHeight: .infinity)
                if showsSecondary {
                    Rectangle().fill(XbinColor.borderStrong).frame(width: 1).ignoresSafeArea()
                    secondary.frame(width: Self.secondaryWidth(total: geo.size.width, fraction: verticalBar ? 0.5 : fraction,
                                                               minSecondary: verticalBar ? 0 : minSecondary, minPrimary: minPrimary))
                        .frame(maxHeight: .infinity)
                }
            }
        }
        .background { probe }
    }

    /// The secondary's width: its share, kept between its own minimum and
    /// what leaves the primary its minimum.
    public static func secondaryWidth(total: CGFloat, fraction: CGFloat, minSecondary: CGFloat, minPrimary: CGFloat) -> CGFloat {
        let most = max(0, total - minPrimary - 1)
        return max(0, min(max(total * fraction, minSecondary), most))
    }

    /// Reports an open Duo into ``verticalBar`` (nothing without the 27.1 SDK).
    @ViewBuilder private var probe: some View {
        #if XBIN_SDK_27_1
        if #available(iOS 27.1, *) {
            VerticalBarProbe(present: $verticalBar).frame(width: 0, height: 0).accessibilityHidden(true)
        }
        #endif
    }
}
#endif
