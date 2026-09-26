import SwiftUI
import UIKit
import XbinTerm

/// The terminal's area: one full-screen terminal (plans/native.md §12), or on
/// a partially folded iPhone Duo in tabletop the terminal above the fold and
/// a key panel between the fold and the keyboard. The arithmetic is XbinTerm's
/// `TermTabletopLayout`; the fold is the active `.division` reserved region
/// (iOS 27.1 SDK, compiled only with XBIN_SDK_27_1), or a simulated one with
/// the `-XbinForceTabletop YES` launch argument. The terminal keeps one
/// identity in both layouts (its UIKit view is never re-hosted).
struct TerminalArea: View {
    let controller: TerminalController
    /// The fold and the full height, measured ignoring the keyboard.
    @State private var probe = FoldProbe(fold: nil, height: 0)

    var body: some View {
        GeometryReader { visible in
            let full = probe.height > 0 ? probe.height : visible.size.height
            let keyboard = max(0, full - visible.size.height)
            let layout = TermTabletopLayout.compute(width: Double(visible.size.width), height: Double(full),
                                                    fold: probe.fold, keyboardHeight: Double(keyboard))
            let tabletop = layout.posture == .tabletop
            VStack(spacing: 0) {
                TerminalHost(controller: controller)
                    .frame(height: tabletop ? CGFloat(layout.terminalHeight) : visible.size.height)
                if tabletop {
                    // The fold and its margins: nothing to touch there.
                    Color.clear.frame(height: CGFloat(layout.panel.y - layout.terminalHeight))
                    TabletopKeyPanel(controller: controller, layout: layout, keyboardShown: keyboard > 1)
                }
            }
            .frame(width: visible.size.width, height: visible.size.height, alignment: .top)
        }
        .background {
            GeometryReader { fullProxy in
                Color.clear.onChange(of: FoldProbe(fold: TerminalFold.rect(in: fullProxy), height: fullProxy.size.height),
                                     initial: true) { _, p in probe = p }
            }
            .ignoresSafeArea(.keyboard)
        }
    }
}

struct FoldProbe: Equatable {
    var fold: TermRect?
    var height: CGFloat
}

/// Where the Duo's fold is, in a geometry proxy's coordinates.
enum TerminalFold {
    /// The launch argument that simulates a horizontal fold mid-screen, to
    /// check the tabletop layout on any simulator or device.
    static let forceKey = "XbinForceTabletop"

    @MainActor
    static func rect(in proxy: GeometryProxy) -> TermRect? {
        if UserDefaults.standard.bool(forKey: forceKey) {
            return TermTabletopLayout.simulatedFold(width: Double(proxy.size.width), height: Double(proxy.size.height))
        }
        #if XBIN_SDK_27_1
        if #available(iOS 27.1, *) {
            // Only active regions are returned: the fold exists while the
            // device is partially folded (none when flat or closed). Names from
            // the iOS 27.1 SDK as reported (GeometryProxy.reservedRegions(kind:),
            // ReservedRegion.frame); verify against the SDK before enabling.
            if let f = proxy.reservedRegions(kind: .division).first?.frame {
                return TermRect(x: Double(f.minX), y: Double(f.minY), width: Double(f.width), height: Double(f.height))
            }
        }
        #endif
        return nil
    }
}

/// The keys below the fold: the rows a terminal wants that the accessory row
/// leaves out (XbinTerm's `TermTabletopKeys`), and the accessory row itself
/// while the keyboard is hidden, with a button to bring the keyboard back.
struct TabletopKeyPanel: View {
    let controller: TerminalController
    let layout: TermTabletopLayout
    let keyboardShown: Bool

    var body: some View {
        let rows = TermTabletopKeys.rows(count: layout.keyRows, keyboardShown: keyboardShown, slot: TerminalPrefs.accessorySlot)
        let _ = controller.stickyRevision // redraw when ctrl/alt change
        VStack(spacing: 6) {
            ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                HStack(spacing: 6) {
                    ForEach(Array(row.enumerated()), id: \.offset) { _, key in
                        keyButton(key)
                    }
                }
            }
            if !keyboardShown {
                Button("Keyboard", systemImage: "keyboard") { controller.showKeyboard() }
                    .buttonStyle(.bordered)
            }
        }
        .padding(.horizontal, 10)
        .padding(.top, 6)
        .frame(maxWidth: .infinity, maxHeight: CGFloat(layout.panel.height), alignment: .top)
        .clipped()
    }

    private func keyButton(_ key: AccessoryKey) -> some View {
        Button { controller.accessory(key) } label: {
            Text(verbatim: AccessorySlot.capLabel(key))
                .font(.system(size: 15, weight: .medium, design: .monospaced))
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .frame(maxWidth: .infinity, minHeight: 34)
        }
        .buttonStyle(.bordered)
        .tint(keyTint(key))
        .accessibilityLabel(AccessorySlot.describe(key))
    }

    /// Sticky ctrl/alt show their state, as on the accessory row.
    private func keyTint(_ key: AccessoryKey) -> Color? {
        guard case .modifier(let m) = key else { return nil }
        switch controller.keyboard.sticky.state(m) {
        case .off: return nil
        case .once, .locked: return .orange
        }
    }
}
