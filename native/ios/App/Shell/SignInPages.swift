import SwiftUI
import XbinCore

// The sign-in pages of the onboarding stack (Onboarding.swift): Log in, the
// workspace's address, its sign-in methods, and an invite.

/// Log in: the QR code a signed-in browser shows (or its link), or the
/// workspace's address.
struct LoginLevel: View {
    @Bindable var flow: OnboardingFlow

    var body: some View {
        Form {
            Section {
                QRScanButton { text in Task { await flow.use(link: text) } }
                TextField("Paste the link", text: $flow.pasted)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                    .keyboardType(.URL)
                    .submitLabel(.continue)
                    .onSubmit { Task { await flow.use(link: flow.pasted) } }
                if !flow.pasted.isEmpty {
                    Button("Continue") { Task { await flow.use(link: flow.pasted) } }
                }
                Button("Where do I find the QR code?", systemImage: "questionmark.circle") { flow.push(.help(page: 1)) }
            } header: {
                Text("From a browser where you're signed in")
            } footer: {
                Text("In the workspace: settings → add a device shows a QR code for this app.")
            }
            Section {
                Button("Enter workspace address", systemImage: "globe") { flow.push(.address) }
            } header: {
                Text("With your account")
            } footer: {
                Text("After signing in, this device keeps a key in its Secure Enclave: later sign-ins are a Face ID prompt.")
            }
            ProblemSection(problem: flow.problem)
        }
        .navigationTitle("Log in")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Enter workspace address → Continue (then MethodsPage).
struct AddressPage: View {
    @Bindable var flow: OnboardingFlow

    var body: some View {
        Form {
            if flow.replacing != nil {
                Section {
                    Text("This device can't sign in to this workspace any more. Sign in again: the workspace is replaced, "
                        + "not added twice.")
                }
            }
            Section {
                TextField("Workspace address", text: $flow.address)
                    .keyboardType(.URL)
                    .textContentType(.URL)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                    .submitLabel(.continue)
                    .onSubmit { continueWith() }
                Button("Continue") { continueWith() }
                    .disabled(flow.address.trimmingCharacters(in: .whitespaces).isEmpty)
            } footer: {
                Text("The address you open the workspace at in a browser, for example https://xbin.example.com.")
            }
            ProblemSection(problem: flow.problem)
        }
        .navigationTitle("Workspace address")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func continueWith() {
        Task { await flow.lookUp(address: flow.address) }
    }
}

/// The sign-in methods the workspace offers (`login/methods`): the password
/// form and/or its SSO button; SSO first, the form under "Workspace admin",
/// in SSO-only mode.
struct MethodsPage: View {
    @Bindable var flow: OnboardingFlow
    let server: ServerOrigin
    let methods: LoginMethods
    @State private var username = ""
    @State private var password = ""

    var body: some View {
        let options = SignInOptions(methods)
        Form {
            Section {
                VStack(alignment: .leading, spacing: 4) {
                    Text(verbatim: methods.title.isEmpty ? server.authority : methods.title).font(.headline)
                    Text(verbatim: server.origin).font(.footnote).foregroundStyle(.secondary)
                }
            }
            ForEach(Array(options.methods.enumerated()), id: \.offset) { _, m in
                switch m {
                case .password(let header): passwordSection(header: header)
                case .sso(let label):
                    Section {
                        SSOButton(server: server, label: label, flow: flow)
                    } footer: {
                        Text("Signs in with your organisation's account, in a browser sheet.")
                    }
                }
            }
            if let why = options.explanation {
                Section { Text(why) }
            }
            if methods.isLegacy {
                Section {
                    Text("This workspace's xbin is older, and doesn't say how it signs people in: both ways are shown.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }
            ProblemSection(problem: flow.problem)
        }
        .navigationTitle("Sign in")
        .navigationBarTitleDisplayMode(.inline)
    }

    @ViewBuilder private func passwordSection(header: String?) -> some View {
        Section {
            TextField("Username", text: $username)
                .textContentType(.username)
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                .submitLabel(.next)
            SecureField("Password", text: $password)
                .textContentType(.password)
                .submitLabel(.go)
                .onSubmit { signIn() }
            Button("Sign in") { signIn() }
                .disabled(username.isEmpty || password.isEmpty)
        } header: {
            if let header { Text(header) }
        } footer: {
            if header != nil {
                Text("This workspace signs people in with SSO. Its admins may also use a password.")
            }
        }
    }

    private func signIn() {
        guard !username.isEmpty, !password.isEmpty else { return }
        let (u, p) = (username, password)
        Task {
            await flow.passwordSignIn(server: server, username: u, password: p)
            password = ""
        }
    }
}

/// Join with an invite: scan or paste the invite link.
struct InvitePage: View {
    @Bindable var flow: OnboardingFlow

    var body: some View {
        Form {
            Section {
                Text("Someone who runs a workspace sent you an invite link. Scan it, or paste it here.")
            }
            Section {
                QRScanButton { text in Task { await flow.use(link: text, allowAddress: false) } }
                TextField("Invite link", text: $flow.inviteLink)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                    .submitLabel(.continue)
                    .onSubmit { Task { await flow.use(link: flow.inviteLink, allowAddress: false) } }
                Button("Continue") { Task { await flow.use(link: flow.inviteLink, allowAddress: false) } }
                    .disabled(flow.inviteLink.trimmingCharacters(in: .whitespaces).isEmpty)
            } footer: {
                Text("It looks like https://your-workspace/login?invite=…")
            }
            ProblemSection(problem: flow.problem)
        }
        .navigationTitle("Join with an invite")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// "You're invited to <title> as <name>": set a password, join, enroll.
struct InviteAcceptPage: View {
    let flow: OnboardingFlow
    let server: ServerOrigin
    let token: String
    let info: InviteInfo
    @State private var password = ""
    @State private var confirm = ""

    private var mismatch: Bool { !confirm.isEmpty && confirm != password }

    var body: some View {
        Form {
            Section {
                VStack(alignment: .leading, spacing: 6) {
                    Text(verbatim: "You're invited to \(info.title.isEmpty ? server.authority : info.title) as \(info.userName).")
                        .font(.headline)
                    Text(verbatim: "Your username is \(info.userID), at \(server.origin).")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }
            Section {
                SecureField("New password", text: $password)
                    .textContentType(.newPassword)
                SecureField("Confirm password", text: $confirm)
                    .textContentType(.newPassword)
                    .submitLabel(.join)
                    .onSubmit { join() }
                Button("Join") { join() }
                    .disabled(password.isEmpty || confirm != password)
            } header: {
                Text("Choose a password")
            } footer: {
                if mismatch {
                    Text("The passwords don't match.").foregroundStyle(.red)
                } else {
                    Text("At least 8 characters. You'll use it to sign in from a browser; this device gets its own key.")
                }
            }
            ProblemSection(problem: flow.problem)
        }
        .navigationTitle("Join")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func join() {
        guard !password.isEmpty, password == confirm else { return }
        let p = password
        Task { await flow.joinInvite(server: server, token: token, info: info, password: p) }
    }
}

/// An xbind older than app invites: the browser sets the password.
struct InviteInBrowserPage: View {
    let flow: OnboardingFlow
    let server: ServerOrigin
    let token: String
    @Environment(\.openURL) private var openURL

    var body: some View {
        Form {
            Section {
                Text("This workspace's xbin is older than invites in the app. Open the invite link in a browser and "
                    + "set your password there, then come back and log in.")
            }
            Section {
                Button("Open the link in a browser", systemImage: "safari") {
                    if let u = OnboardingFlow.inviteURL(server: server, token: token) { openURL(u) }
                }
                Button("Log in", systemImage: "person.crop.circle") { flow.showLogin() }
            }
        }
        .navigationTitle("Join with an invite")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// The last action's failure: what kind, and what to do.
struct ProblemSection: View {
    let problem: ConnectProblem?

    var body: some View {
        if let p = problem {
            Section {
                VStack(alignment: .leading, spacing: 6) {
                    Label(p.title, systemImage: symbol(p.kind))
                        .font(.headline)
                        .foregroundStyle(.red)
                    Text(p.message).font(.callout)
                }
                .padding(.vertical, 4)
                .accessibilityElement(children: .combine)
            }
        }
    }

    private func symbol(_ k: ConnectProblem.Kind) -> String {
        switch k {
        case .cantConnect: return "wifi.exclamationmark"
        case .certificate: return "lock.trianglebadge.exclamationmark"
        case .notXbin: return "questionmark.app"
        case .codeRefused: return "qrcode"
        case .account: return "person.crop.circle.badge.exclamationmark"
        case .throttled: return "hourglass"
        case .server: return "exclamationmark.icloud"
        case .other: return "exclamationmark.triangle"
        }
    }
}
