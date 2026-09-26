import SafariServices
import SwiftUI
import UIKit

/// An in-app Safari view (SFSafariViewController): the workspace's own web
/// pages — chrome tiles, "Open in Safari" — opened by a one-shot ticket
/// when the workspace can issue one (WorkspaceModel.openInSafari): the
/// workspace shows "Continue as <name>" (or goes straight to the page when
/// this Safari view is already signed in as the user), one tap signs it in.
/// It keeps its own cookies, apart from the tiles' web views and from Safari.
struct SafariView: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> SFSafariViewController {
        let vc = SFSafariViewController(url: url)
        vc.dismissButtonStyle = .close
        return vc
    }

    func updateUIViewController(_ vc: SFSafariViewController, context: Context) {}
}

/// Presents a Safari view over the focused window from model code (no view
/// to hang a sheet on): its key window's top-most controller.
@MainActor
enum SafariPresenter {
    static func present(_ url: URL) {
        guard ["http", "https"].contains(url.scheme?.lowercased() ?? "") else { return }
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let scene = scenes.first { $0.activationState == .foregroundActive } ?? scenes.first
        guard var top = scene?.keyWindow?.rootViewController ?? scene?.windows.first?.rootViewController else {
            UIApplication.shared.open(url)
            return
        }
        while let p = top.presentedViewController, !p.isBeingDismissed { top = p }
        let vc = SFSafariViewController(url: url)
        vc.dismissButtonStyle = .close
        top.present(vc, animated: true)
    }
}
