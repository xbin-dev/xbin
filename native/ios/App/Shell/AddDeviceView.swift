import CoreImage
import CoreImage.CIFilterBuiltins
import SwiftUI
import UIKit
import XbinCore

/// "Add another device" (native/spec/device-login.md §3): this device,
/// signed in, mints a one-time enrollment code and shows its `xbin://enroll`
/// link as a QR code — the new device scans it (Add a workspace → Scan) and
/// gets its own key. The code is a credential while it lives (a few
/// minutes): it is shown only here, hidden from screenshots of the app
/// switcher, and never logged.
///
/// A session older than the step-up window gets `{stepUp}` back: `signin`
/// → sign in again with this device's key (one Face ID prompt) and ask
/// again; `password` → the account password.
struct AddDeviceView: View {
    let workspace: WorkspaceModel

    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var phase
    @State private var state: Step = .minting
    @State private var password = ""
    @State private var now = Date()

    enum Step: Equatable {
        case minting
        case password(String?)
        case code(link: String, code: String, expires: Date?)
        case failed(String)
    }

    var body: some View {
        NavigationStack {
            Form {
                switch state {
                case .minting:
                    Section { HStack { Spacer(); ProgressView("Making a code…"); Spacer() } }
                case .password(let error):
                    Section {
                        SecureField("Password", text: $password).textContentType(.password)
                            .onSubmit { Task { await mint(password: password) } }
                        Button("Continue") { Task { await mint(password: password) } }.disabled(password.isEmpty)
                        if let error { Text(verbatim: error).foregroundStyle(.red).font(.footnote) }
                    } header: { Text("Confirm it's you") } footer: {
                        Text("Adding a device needs your password when you signed in a while ago.")
                    }
                case .code(let link, let code, let expires):
                    codeSection(link: link, code: code, expires: expires)
                case .failed(let why):
                    Section {
                        Label { Text(verbatim: why) } icon: { Image(systemName: "exclamationmark.triangle") }
                            .foregroundStyle(.red)
                        Button("Try again") { Task { await mint(password: nil) } }
                    }
                }
            }
            .navigationTitle("Add a device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task { await mint(password: nil) }
            .task {
                while !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: 1_000_000_000)
                    now = Date()
                }
            }
        }
    }

    @ViewBuilder private func codeSection(link: String, code: String, expires: Date?) -> some View {
        let expired = expires.map { $0 <= now } ?? false
        Section {
            VStack(spacing: 14) {
                if expired {
                    ContentUnavailableView("The code expired", systemImage: "clock.badge.xmark")
                    Button("Make a new code") { Task { await mint(password: nil) } }.buttonStyle(.borderedProminent)
                } else {
                    QRCodeImage(text: link)
                        .frame(width: 240, height: 240)
                        .blur(radius: phase == .active ? 0 : 20)
                        .accessibilityLabel(Text(verbatim: "QR code to add a device to \(workspace.title)"))
                    Text(verbatim: AddDevice.grouped(code))
                        .font(.body.monospaced()).textSelection(.enabled).multilineTextAlignment(.center)
                    if let expires {
                        Text("Expires \(expires, style: .relative)").font(.footnote).foregroundStyle(.secondary)
                    }
                    ShareLink(item: link) { Label("Share the link", systemImage: "square.and.arrow.up") }
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 8)
            .privacySensitive()
        } footer: {
            Text("On the new device: xbin → Add a workspace → Scan the QR code. Anyone with this code can add a device as you until it expires.")
        }
    }

    private func mint(password: String?) async {
        state = .minting
        do {
            var out = AddDevice.outcome(try await workspace.auth.send(AddDevice.request(password: password)), origin: workspace.origin)
            if out == .signInAgain, workspace.canResign {
                // A fresh device login is a step-up (one Face ID prompt).
                _ = try await workspace.auth.signIn()
                out = AddDevice.outcome(try await workspace.auth.send(AddDevice.request(password: password)), origin: workspace.origin)
            }
            switch out {
            case .code(let link, let code, let expires):
                self.password = ""
                state = .code(link: link, code: code, expires: expires)
            case .needsPassword:
                state = .password(password == nil ? nil : "That password didn't work.")
            case .signInAgain:
                state = .failed("Sign in to this workspace again, then add the device.")
            case .refused(let m):
                state = .failed("This sign-in can't add devices (\(m)).")
            case .failed(let e):
                state = .failed(e.description)
            }
        } catch {
            state = .failed(workspace.describe(error))
        }
    }
}

/// A QR code for `text` (CoreImage's generator, medium error correction),
/// drawn with hard pixel edges; rendered once per text.
struct QRCodeImage: View {
    let text: String
    @State private var image: UIImage?

    var body: some View {
        Group {
            if let image {
                Image(uiImage: image)
                    .interpolation(.none)
                    .resizable()
                    .scaledToFit()
                    .padding(12)
                    .background(Color.white, in: RoundedRectangle(cornerRadius: 12))
            } else {
                Image(systemName: "qrcode").font(.largeTitle).foregroundStyle(.secondary)
            }
        }
        .task(id: text) { image = Self.render(text) }
    }

    static func render(_ text: String) -> UIImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(text.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage else { return nil }
        let scaled = output.transformed(by: CGAffineTransform(scaleX: 10, y: 10))
        guard let cg = CIContext().createCGImage(scaled, from: scaled.extent) else { return nil }
        return UIImage(cgImage: cg)
    }
}
