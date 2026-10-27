import CoreImage
import CoreImage.CIFilterBuiltins
import SwiftUI
import XbinRenderer
import UIKit
import XbinCore

/// "Add another device" (native/spec/device-login.md §3): this device,
/// signed in, mints a one-time enrollment code and shows its `xbin://enroll`
/// link as a QR code — the new device scans it (Add a workspace → Scan) and
/// gets its own key. The code is a credential while it lives (a few
/// minutes): it is shown only here, hidden from screenshots of the app
/// switcher, and never logged.
///
/// A workspace with app tunnels (D204) mints a pairing code instead: a
/// bigger QR code (the tunnel's ticket, keys and relay token ride in it),
/// and this device is "the computer" — it polls the pending pairing, shows
/// the code the new device derived, and asks: Codes match / They don't. At
/// the person's device limit, matching names a device to replace.
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
    /// Mint classic codes (the "older app" link), not pairing codes.
    @State private var classic = false
    @State private var pairing: PairingStatus?
    @State private var devices: [DeviceInfo] = []
    @State private var confirming = false

    enum Step: Equatable {
        case minting
        case password(String?)
        case code(link: String, code: String, expires: Date?)
        case pairing(link: String, id: String, expires: Date?, required: Bool)
        /// The pairing ended: added, declined, abandoned, expired.
        case pairingEnded(PairingStatus.State, name: String?)
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
                        if let error { Text(verbatim: error).foregroundStyle(XbinColor.danger).font(.footnote) }
                    } header: { Text("Confirm it's you") } footer: {
                        Text("Adding a device needs your password when you signed in a while ago.")
                    }
                case .code(let link, let code, let expires):
                    codeSection(link: link, code: code, expires: expires)
                case .pairing(let link, let id, let expires, let required):
                    pairingSections(link: link, id: id, expires: expires, required: required)
                case .pairingEnded(let s, let name):
                    endedSection(s, name: name)
                case .failed(let why):
                    Section {
                        Label { Text(verbatim: why) } icon: { Image(systemName: "exclamationmark.triangle") }
                            .foregroundStyle(XbinColor.danger)
                        Button("Try again") { Task { await mint(password: nil) } }
                    }
                }
            }
            .keyboardHideButton()
            .concreteBackground()
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
            .task(id: pairingID) { await poll() }
        }
    }

    private var pairingID: String? {
        if case .pairing(_, let id, _, _) = state { return id }
        return nil
    }

    // MARK: Classic codes

    @ViewBuilder private func codeSection(link: String, code: String, expires: Date?) -> some View {
        let expired = expires.map { $0 <= now } ?? false
        Section {
            VStack(spacing: 14) {
                if expired {
                    ContentUnavailableView("The code expired", systemImage: "clock.badge.xmark")
                    Button("Make a new code") { Task { await mint(password: nil) } }.xbinPrimary()
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

    // MARK: Pairing codes (D204)

    @ViewBuilder private func pairingSections(link: String, id: String, expires: Date?, required: Bool) -> some View {
        let expired = expires.map { $0 <= now } ?? false
        if let p = pairing, p.state == .confirm, let code = p.sas {
            confirmSections(p, code: code, id: id)
        } else if expired {
            Section {
                VStack(spacing: 14) {
                    ContentUnavailableView("The code expired", systemImage: "clock.badge.xmark")
                    Button("Make a new code") { Task { await mint(password: nil) } }.xbinPrimary()
                }
            }
        } else {
            Section {
                VStack(spacing: 14) {
                    QRCodeImage(text: link)
                        .frame(width: 300, height: 300)
                        .blur(radius: phase == .active ? 0 : 20)
                        .accessibilityLabel(Text(verbatim: "QR code to pair a device with \(workspace.title)"))
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Waiting for the new device…").foregroundStyle(.secondary)
                    }
                    if let expires {
                        Text("Expires \(expires, style: .relative)").font(.footnote).foregroundStyle(.secondary)
                    }
                    ShareLink(item: link) { Label("Share the link", systemImage: "square.and.arrow.up") }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 8)
                .privacySensitive()
            } footer: {
                Text("On the new device: xbin → Log in → Scan the QR code. It connects through the workspace's encrypted "
                    + "tunnel and shows a 6-digit code; this screen shows it too, to compare. Until the code expires, "
                    + "the QR code and its link are a credential.")
            }
            if !required {
                Section {
                    Button("Older app? Show a classic code") {
                        classic = true
                        Task { await mint(password: nil) }
                    }
                } footer: {
                    Text("An app from before tunnels can't pair. A classic code signs it in over the workspace's address.")
                }
            }
        }
    }

    @ViewBuilder private func confirmSections(_ p: PairingStatus, code: String, id: String) -> some View {
        Section {
            VStack(spacing: 14) {
                Text(verbatim: "\(p.deviceName ?? "A new device") wants to join. Does it show this code?")
                    .font(.headline).multilineTextAlignment(.center)
                Text(verbatim: code)
                    .font(XbinFont.mono(.largeTitle).weight(.bold))
                    .tracking(2)
                    .accessibilityLabel(Text(verbatim: TunnelSAS.spoken(code)))
                    .accessibilityValue(Text(verbatim: code))
                    .accessibilityIdentifier("pairing-confirm-sas")
                Text("Only confirm if your device shows this code.").font(.footnote).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 8)
            Button("Codes match") { Task { await confirm(id: id, match: true) } }
                .xbinPrimary()
                .disabled(confirming)
            Button("They don't", role: .destructive) { Task { await confirm(id: id, match: false) } }
                .disabled(confirming)
        }
        if p.atLimit {
            Section {
                ForEach(devices.filter { !$0.current }) { d in
                    Button {
                        Task { await confirm(id: id, match: true, replace: d.id) }
                    } label: {
                        Label(d.name.isEmpty ? d.id : d.name, systemImage: "arrow.triangle.2.circlepath")
                    }
                    .disabled(confirming)
                }
            } header: {
                Text("Replace a device")
            } footer: {
                Text("This account has as many devices as the workspace allows. Pick one to replace: it is removed "
                    + "when the new one is added.")
            }
        }
    }

    @ViewBuilder private func endedSection(_ s: PairingStatus.State, name: String?) -> some View {
        Section {
            switch s {
            case .done:
                Label { Text(verbatim: "\(name ?? "The device") is added.") } icon: { Image(systemName: "checkmark.circle") }
            case .rejected:
                Label("Declined. That code can't be used again.", systemImage: "hand.raised")
            case .abandoned:
                Label("The new device went away before you confirmed.", systemImage: "questionmark.circle")
            case .expired:
                Label("The code expired.", systemImage: "clock.badge.xmark")
            default:
                Label("The pairing ended.", systemImage: "exclamationmark.triangle")
            }
            if s != .done {
                Button("Make a new code") { Task { await mint(password: nil) } }.xbinPrimary()
            }
        }
    }

    // MARK: Requests

    private func mint(password: String?) async {
        state = .minting
        pairing = nil
        devices = []
        let tunnel = !classic
        do {
            var out = AddDevice.outcome(try await workspace.auth.send(AddDevice.request(password: password, tunnel: tunnel)),
                                        origin: workspace.origin)
            if out == .signInAgain, workspace.canResign {
                // A fresh device login is a step-up (one Face ID prompt).
                _ = try await workspace.auth.signIn()
                out = AddDevice.outcome(try await workspace.auth.send(AddDevice.request(password: password, tunnel: tunnel)),
                                        origin: workspace.origin)
            }
            switch out {
            case .code(let link, let code, let expires):
                self.password = ""
                state = .code(link: link, code: code, expires: expires)
            case .pairing(let link, let id, let expires, let required):
                self.password = ""
                state = .pairing(link: link, id: id, expires: expires, required: required)
            case .atLimit(let m):
                state = .failed("Can't add a device: \(m.isEmpty ? ConnectProblem.deviceLimitText : m)")
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

    /// Follows the pending pairing until the new device's code appears and
    /// the pairing ends.
    private func poll() async {
        guard let id = pairingID else { return }
        while !Task.isCancelled {
            if let r = try? await workspace.auth.send(PairingStatus.poll(id)), r.status == 200, let j = try? r.json() {
                var s = PairingStatus(json: j)
                if pairing?.atLimit == true { s.atLimit = true }
                pairing = s
                if s.atLimit, devices.isEmpty { await loadDevices() }
                if s.state.isFinal {
                    state = .pairingEnded(s.state, name: s.deviceName)
                    return
                }
            }
            try? await Task.sleep(nanoseconds: 1_000_000_000)
        }
    }

    private func loadDevices() async {
        guard let j = try? await workspace.auth.json(APIRequest("GET", AppAuthRoute.devices)) else { return }
        devices = DeviceInfo.list(json: j)
    }

    private func confirm(id: String, match: Bool, replace: String? = nil) async {
        confirming = true
        defer { confirming = false }
        do {
            let r = try await workspace.auth.send(PairingStatus.confirm(id, match: match, replace: replace))
            if r.status == 409, match, replace == nil {
                // At the limit: name a device to replace.
                pairing?.atLimit = true
                await loadDevices()
                return
            }
            guard r.isSuccess else {
                state = .failed(APIError(r).message)
                return
            }
            if !match { state = .pairingEnded(.rejected, name: pairing?.deviceName) }
        } catch {
            state = .failed(workspace.describe(error))
        }
    }
}

/// A QR code for `text` (CoreImage's generator, medium error correction —
/// what native/spec/tunnel.md §2 asks of pairing links), drawn with hard
/// pixel edges; rendered once per text.
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
                    .background(Color.white, in: .xbinPlate) // a QR code reads dark on white, in either theme
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
