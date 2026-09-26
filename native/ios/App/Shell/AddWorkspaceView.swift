import AuthenticationServices
import CryptoKit
import SwiftUI
import VisionKit
import XbinCore

/// Adding a workspace (plans/native.md §5; native/spec/device-login.md):
/// scan the QR code a signed-in browser shows (Devices → Add a device), or
/// paste its `xbin://enroll` link — the app makes a Secure Enclave key and
/// redeems the code; or sign in here (password or SSO) and the app mints
/// its own code and enrolls; or, for development, a server URL and a token.
struct AddWorkspaceView: View {
    let request: AddRequest

    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @Environment(\.webAuthenticationSession) private var webAuth

    @State private var link = ""
    @State private var server = ""
    @State private var username = ""
    @State private var password = ""
    @State private var token = ""
    @State private var busy: String?
    @State private var error: String?
    @State private var scanning = false
    @State private var confirmEnroll: (ServerOrigin, String)?
    @State private var showAdvanced = false

    private var enrollment: Enrollment {
        Enrollment(transport: AppTransport.shared, keys: EnclaveKeyStore(), clientHeader: AppInfo.clientHeader,
                   platform: AppInfo.platform)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    if DataScannerViewController.isSupported {
                        Button("Scan the QR code", systemImage: "qrcode.viewfinder") { scanning = true }
                            .disabled(!DataScannerViewController.isAvailable || busy != nil)
                    }
                    TextField("or paste the xbin://enroll link", text: $link)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .onSubmit { useLink(link) }
                    if !link.isEmpty { Button("Add with this link") { useLink(link) }.disabled(busy != nil) }
                } header: { Text("From a signed-in browser") } footer: {
                    Text("In the workspace: your account menu → Devices → Add a device.")
                }

                Section {
                    TextField("Workspace address (https://…)", text: $server)
                        .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    TextField("Username", text: $username).textInputAutocapitalization(.never).autocorrectionDisabled()
                        .textContentType(.username)
                    SecureField("Password", text: $password).textContentType(.password)
                    Button("Sign in") { Task { await passwordSignIn() } }
                        .disabled(server.isEmpty || username.isEmpty || password.isEmpty || busy != nil)
                    Button("Sign in with SSO") { Task { await ssoSignIn() } }
                        .disabled(server.isEmpty || busy != nil)
                } header: { Text("Sign in here") } footer: {
                    Text("Then this device is enrolled with a key in its Secure Enclave; later sign-ins are a Face ID prompt.")
                }

                Section {
                    DisclosureGroup("Advanced: server URL and token", isExpanded: $showAdvanced) {
                        SecureField("Token", text: $token)
                        Button("Connect") { Task { await tokenSignIn() } }
                            .disabled(server.isEmpty || token.isEmpty || busy != nil)
                    }
                } footer: {
                    Text("For development: a bearer token (for example the workspace's owner token). It isn't renewed.")
                }

                if let error { Section { Text(verbatim: error).foregroundStyle(.red) } }
            }
            .navigationTitle("Add a workspace")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { app.addRequest = nil } } }
            .overlay { if let busy { ProgressView(busy).padding().background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12)) } }
            .sheet(isPresented: $scanning) {
                QRScanner { text in
                    scanning = false
                    useLink(text)
                }
                .ignoresSafeArea()
            }
            .confirmationDialog("Add this workspace?", isPresented: Binding(get: { confirmEnroll != nil }, set: { if !$0 { confirmEnroll = nil } }),
                                titleVisibility: .visible) {
                Button("Add \(confirmEnroll?.0.authority ?? "")") {
                    if let pair = confirmEnroll { Task { await enroll(server: pair.0, code: pair.1) } }
                    confirmEnroll = nil
                }
            } message: {
                Text("This device gets a key that signs you in to \(confirmEnroll?.0.origin ?? "") with Face ID.")
            }
            .onAppear {
                if case .enroll(let s, let c) = request { confirmEnroll = (s, c) }
            }
        }
    }

    private func useLink(_ text: String) {
        error = nil
        guard let l = try? DeepLink(string: text.trimmingCharacters(in: .whitespacesAndNewlines)),
              case .enroll(let s, let c) = l else {
            error = "That isn't an xbin://enroll link."
            return
        }
        confirmEnroll = (s, c)
    }

    private func enroll(server: ServerOrigin, code: String) async {
        busy = "Adding…"
        defer { busy = nil }
        do {
            let (rec, session) = try await enrollment.enroll(server: server, code: code, deviceName: AppInfo.deviceName)
            await app.add(rec, session: session)
        } catch { self.error = describe(error) }
    }

    private func origin() -> ServerOrigin? {
        do { return try ServerOrigin(userInput: server) } catch {
            self.error = "\(error)"
            return nil
        }
    }

    private func passwordSignIn() async {
        guard let o = origin() else { return }
        busy = "Signing in…"
        defer { busy = nil }
        do {
            let s = try await enrollment.passwordLogin(server: o, username: username, password: password)
            let (rec, dev) = try await enrollment.enrollSignedIn(server: o, session: s, deviceName: AppInfo.deviceName,
                                                                  password: password)
            password = ""
            await app.add(rec, session: dev)
        } catch { self.error = describe(error) }
    }

    /// SSO (device-login.md §5): PKCE, the web authentication session, the
    /// one-shot ticket, then enrollment with the fresh session.
    private func ssoSignIn() async {
        guard let o = origin() else { return }
        let verifier = PKCE.makeVerifier()
        let challenge = PKCE.challenge(sha256Digest: Data(SHA256.hash(data: Data(verifier.utf8))))
        guard let url = o.url(path: AppAuthRoute.ssoStart(challenge: challenge)) else { return }
        busy = "Signing in…"
        defer { busy = nil }
        do {
            let back = try await webAuth.authenticate(using: url, callbackURLScheme: DeepLink.scheme,
                                                      preferredBrowserSession: .ephemeral)
            switch try DeepLink(url: back) {
            case .sso(let ticket):
                let s = try await enrollment.redeemTicket(server: o, ticket: ticket, verifier: verifier)
                let (rec, dev) = try await enrollment.enrollSignedIn(server: o, session: s, deviceName: AppInfo.deviceName)
                await app.add(rec, session: dev)
            case .ssoError(let code):
                error = "SSO sign-in failed (\(code))."
            default:
                error = "Unexpected answer from the sign-in page."
            }
        } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
            // The user closed the sheet.
        } catch { self.error = describe(error) }
    }

    private func tokenSignIn() async {
        guard let o = origin() else { return }
        busy = "Connecting…"
        defer { busy = nil }
        do {
            let (rec, s) = try await enrollment.tokenLogin(server: o, token: token.trimmingCharacters(in: .whitespacesAndNewlines))
            token = ""
            await app.add(rec, session: s)
        } catch { self.error = describe(error) }
    }

    private func describe(_ e: any Error) -> String {
        if let f = e as? Enrollment.Failure { return f.description }
        if let s = e as? SignInError { return s.description }
        if let a = e as? APIError { return a.description }
        return e.localizedDescription
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
