import SwiftUI
import XbinRenderer
import XbinCore

// Pairing through a workspace's app tunnel (D204; native/spec/tunnel.md §5,
// §6): after "Pair with <host>?", this page runs TunnelPairer
// (App/Model/TunnelPairing.swift) and shows where it is — connecting, then
// the confirmation code the person compares with the computer that showed
// the QR code ("Confirm on your computer: 012 345 — waiting…"; the
// computer has "Codes match" and "They don't"), then the sign-in — or why
// it failed. Debug builds' `-XbinPairingDemo <state>` (``PairingDemo``)
// draws each state without a server, for the UI tests' gallery.

@MainActor
@Observable
final class PairingModel {
    enum Phase: Equatable {
        case connecting
        /// The computer shows the code now; it waits for its click.
        case waiting(code: String, expires: Date?)
        case signingIn
        case failed(ConnectProblem)
    }

    var phase: Phase = .connecting
    private var started = false

    func run(link: TunnelLink, flow: OnboardingFlow) async {
        if let demo = PairingDemo.current {
            phase = PairingDemo.phase(demo)
            return
        }
        guard !started else { return }
        started = true
        guard let dialer = TunnelRuntime.dialer, let signIn = TunnelRuntime.signInTransport else {
            phase = .failed(TunnelRuntime.unavailable)
            return
        }
        let pairer = TunnelPairer(keys: EnclaveKeyStore(), vault: KeychainTunnelVault(), dialer: dialer,
                                  signInTransport: signIn, clientHeader: AppInfo.clientHeader, platform: AppInfo.platform)
        do {
            let paired = try await pairer.pair(link, deviceName: AppInfo.deviceName) { [weak self] code, expires in
                await self?.show(code: code, expires: expires)
            } onSigningIn: { [weak self] in
                await self?.signingIn()
            }
            await AppModel.shared.add(paired.record, session: paired.session, in: flow.scene, replacing: flow.replacing)
        } catch is CancellationError {
        } catch {
            phase = .failed(ConnectProblem(error, server: link.server))
            started = false
        }
    }

    private func show(code: String, expires: Date?) {
        phase = .waiting(code: code, expires: expires)
        Haptics.needsYou()
    }

    private func signingIn() { phase = .signingIn }
}

struct PairingPage: View {
    let flow: OnboardingFlow
    let link: TunnelLink
    @State private var model = PairingModel()

    var body: some View {
        Form {
            Section {
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: link.server.authority).font(.headline)
                    Label("Through the workspace's encrypted tunnel", systemImage: "lock.shield")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }
            switch model.phase {
            case .connecting:
                Section {
                    HStack(spacing: 12) {
                        ProgressView()
                        Text("Connecting to the workspace…")
                    }
                } footer: {
                    Text("The first connection goes through the workspace's relay and can take a few seconds.")
                }
            case .waiting(let code, let expires):
                codeSection(code, expires: expires)
            case .signingIn:
                Section {
                    HStack(spacing: 12) {
                        ProgressView()
                        Text("Paired. Signing in…")
                    }
                } footer: {
                    Text("This device signs in with its new key: confirm with Face ID if asked.")
                }
            case .failed(let p):
                ProblemSection(problem: p)
                Section {
                    Button("Scan a new code", systemImage: "qrcode.viewfinder") { flow.showLogin() }
                }
            }
        }
        .navigationTitle("Pairing")
        .navigationBarTitleDisplayMode(.inline)
        .task { await model.run(link: link, flow: flow) }
    }

    @ViewBuilder private func codeSection(_ code: String, expires: Date?) -> some View {
        Section {
            VStack(spacing: 14) {
                Text("Confirm on your computer:")
                    .font(.headline)
                Text(verbatim: code)
                    .font(XbinFont.mono(.largeTitle).weight(.bold))
                    .tracking(2)
                    .lineLimit(1)
                    .minimumScaleFactor(0.5)
                    .accessibilityLabel(Text(verbatim: TunnelSAS.spoken(code)))
                    .accessibilityValue(Text(verbatim: code))
                    .accessibilityIdentifier("pairing-sas")
                HStack(spacing: 8) {
                    ProgressView()
                    Text("waiting…").foregroundStyle(.secondary)
                }
                if let expires {
                    Text("The code expires in \(expires, style: .relative)").font(.footnote).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 12)
        } footer: {
            Text("The computer that shows the QR code shows a code too. If it's the same, click Codes match there. "
                + "If it isn't, click They don't: someone else may have your code.")
        }
    }
}

/// Debug builds' `-XbinPairingDemo <state>`: the pairing screens with no
/// server or tunnel, for XbinOnboardingTests' gallery. `ask` (the "Pair
/// with …?" question), `required` (a pairing-only workspace's sign-in
/// page), `connecting`, `sas`, `signingIn`, `declined`, `expired`,
/// `unreachable`, `limit`.
enum PairingDemo {
    static var current: String? {
        #if DEBUG
        let s = UserDefaults.standard.string(forKey: "XbinPairingDemo")
        return s?.isEmpty == false ? s : nil
        #else
        return nil
        #endif
    }

    static let server = try! ServerOrigin(string: "https://xbin.example.com")

    static var link: TunnelLink {
        TunnelLink(server: server, code: "DEMODEMODEMODEMODEMODEMODE", ticket: "endpoint" + String(repeating: "a", count: 56),
                   serverKey: Data(repeating: 9, count: 32), psk: Data(repeating: 7, count: 32))
    }

    static func phase(_ state: String) -> PairingModel.Phase {
        let host = server.authority
        switch state {
        case "connecting": return .connecting
        case "signingIn": return .signingIn
        case "declined": return .failed(.pairingDeclined)
        case "expired": return .failed(.pairingExpired)
        case "unreachable": return .failed(.tunnelUnreachable(host: host, detail: "no answer from its relay"))
        case "limit":
            return .failed(ConnectProblem(PairingFailure.rejected(.limit, message: ""), server: server))
        default: return .waiting(code: "012 345", expires: Date().addingTimeInterval(270))
        }
    }

    /// Puts a fresh Welcome's stack on the state's screen.
    @MainActor static func stage(_ flow: OnboardingFlow) {
        guard let s = current, flow.path.isEmpty else { return }
        switch s {
        case "ask":
            flow.path = [.login]
            flow.pendingPair = link
            flow.askPair = true
        case "required":
            let m = LoginMethods(title: "Acme", password: true, sso: true, tunnel: .init(enabled: true, required: true))
            flow.path = [.login, .methods(server: server, methods: m)]
        default:
            flow.path = [.login, .pairing(link)]
        }
    }
}
