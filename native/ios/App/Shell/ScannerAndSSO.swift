import AuthenticationServices
import CryptoKit
import SwiftUI
import VisionKit
import XbinCore

// The halves of adding a workspace that need Apple-only frameworks
// (Onboarding.swift has the rest): VisionKit's QR scanner and SSO in
// ASWebAuthenticationSession. native/tools/app-stubcheck skips this file and
// stands in for these two views.

/// "Scan QR code", always shown. Where the camera scanner is unsupported
/// (the simulator, a device without the hardware) it's disabled and says
/// to paste the link instead; `found` gets the first QR code's text.
struct QRScanButton: View {
    let found: (String) -> Void
    @State private var scanning = false

    var body: some View {
        let supported = DataScannerViewController.isSupported
        let available = supported && DataScannerViewController.isAvailable
        VStack(alignment: .leading, spacing: 4) {
            Button("Scan QR code", systemImage: "qrcode.viewfinder") { scanning = true }
                .disabled(!available)
            if !supported {
                Text("This device can't scan; paste the link instead.")
                    .font(.footnote).foregroundStyle(.secondary)
            } else if !available {
                Text("The camera isn't available to xbin. Allow it in Settings, or paste the link instead.")
                    .font(.footnote).foregroundStyle(.secondary)
            }
        }
        .sheet(isPresented: $scanning) {
            QRScanner { text in
                scanning = false
                found(text)
            }
            .ignoresSafeArea()
        }
    }
}

/// VisionKit's QR scanner; hands back the first code's text.
struct QRScanner: UIViewControllerRepresentable {
    let found: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .balanced,
                                           recognizesMultipleItems: false, isHighFrameRateTrackingEnabled: false,
                                           isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        try? vc.startScanning()
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(found: found) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let found: (String) -> Void
        private var done = false
        init(found: @escaping (String) -> Void) { self.found = found }

        func dataScanner(_ scanner: DataScannerViewController, didAdd addedItems: [RecognizedItem], allItems: [RecognizedItem]) {
            guard !done else { return }
            for item in addedItems {
                if case .barcode(let b) = item, let s = b.payloadStringValue {
                    done = true
                    scanner.stopScanning()
                    found(s)
                    return
                }
            }
        }
    }
}

/// The workspace's SSO button (device-login.md §5): PKCE, the web
/// authentication session, the one-shot ticket, then this device enrolled
/// with the fresh session.
struct SSOButton: View {
    let server: ServerOrigin
    let label: String
    let flow: OnboardingFlow
    @Environment(\.webAuthenticationSession) private var webAuth

    var body: some View {
        Button(label, systemImage: "person.badge.key") { Task { await signIn() } }
    }

    private func signIn() async {
        let verifier = PKCE.makeVerifier()
        let challenge = PKCE.challenge(sha256Digest: Data(SHA256.hash(data: Data(verifier.utf8))))
        guard let url = server.url(path: AppAuthRoute.ssoStart(challenge: challenge)) else { return }
        await flow.run("Signing in…", server: server) {
            let back: URL
            do {
                back = try await webAuth.authenticate(using: url, callbackURLScheme: DeepLink.scheme,
                                                      preferredBrowserSession: .ephemeral)
            } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
                return // the person closed the sheet
            }
            switch try DeepLink(url: back) {
            case .sso(let ticket):
                let s = try await flow.enrollment.redeemTicket(server: server, ticket: ticket, verifier: verifier)
                try await flow.enrollThisDevice(server: server, session: s)
            case .ssoError(let code):
                throw ConnectProblem.sso(error: code)
            default:
                throw ConnectProblem.other("The sign-in page answered with a link this app doesn't know.")
            }
        }
    }
}
