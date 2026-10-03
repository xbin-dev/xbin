import SwiftUI
import UIKit
import XbinCore
import XbinRenderer

// Adding a workspace (plans/native.md §5; native/spec/device-login.md): the
// Welcome with its levels — Log in (a QR code from a signed-in browser, or
// the workspace's address, then the sign-in methods it offers), Join with an
// invite, Run your own xbin, What is xbin? — and the same stack in the
// add-workspace sheet, which starts at Log in. Every path ends with this
// device enrolled (a key in its Secure Enclave): there is no token login.
// The pages are in SignInPages.swift; the camera and the SSO sheet, which
// need Apple-only frameworks, in ScannerAndSSO.swift. What they decide —
// discovery, invites, links, telling failures apart — is XbinCore's
// (Client/Onboarding.swift, Client/ConnectProblem.swift).

/// A page of the onboarding stack.
enum OnboardingStep: Hashable {
    case login
    case address
    case methods(server: ServerOrigin, methods: LoginMethods)
    case invite
    case inviteAccept(server: ServerOrigin, token: String, info: InviteInfo)
    case inviteInBrowser(server: ServerOrigin, token: String)
    case runYourOwn
    case about(page: Int)
    case help(page: Int)
}

/// One onboarding stack's state and actions: the Welcome's, or one
/// add-workspace sheet's.
@MainActor
@Observable
final class OnboardingFlow {
    /// A scanned or opened enrollment link, waiting for "Add".
    struct PendingEnroll: Identifiable, Equatable {
        let server: ServerOrigin
        let code: String
        var id: String { server.origin + "|" + code }
    }

    var path: [OnboardingStep] = []
    /// What's in progress (the overlay's words); nil = idle.
    var busy: String?
    /// The last action's failure, shown on the page on top.
    var problem: ConnectProblem?
    var pendingEnroll: PendingEnroll?
    /// The "Add this workspace?" question is up (about ``pendingEnroll``).
    var askEnroll = false
    /// The address page's field.
    var address = ""
    /// The Log in page's link field.
    var pasted = ""
    /// The invite page's link field.
    var inviteLink = ""
    /// "Sign in again": the workspace the new sign-in replaces.
    let replacing: String?
    @ObservationIgnored weak var scene: SceneModel?

    init(scene: SceneModel?, request: AddRequest? = nil) {
        self.scene = scene
        switch request {
        case .enroll(let s, let c)?:
            replacing = nil
            pendingEnroll = PendingEnroll(server: s, code: c)
            askEnroll = true
        case .signInAgain(let w, let s)?:
            replacing = w
            address = s.origin
            path = [.address]
        case .blank?, nil:
            replacing = nil
        }
    }

    var enrollment: Enrollment {
        Enrollment(transport: AppTransport.shared, keys: EnclaveKeyStore(), clientHeader: AppInfo.clientHeader,
                   platform: AppInfo.platform)
    }

    /// Runs one action: busy while it runs, its failure classified.
    func run(_ label: String, server: ServerOrigin?, _ work: () async throws -> Void) async {
        guard busy == nil else { return }
        problem = nil
        busy = label
        defer { busy = nil }
        do { try await work() } catch is CancellationError {} catch {
            problem = ConnectProblem(error, server: server)
        }
    }

    /// Shows `step` on top (a button's destination).
    func push(_ step: OnboardingStep) {
        problem = nil
        path.append(step)
    }

    /// Back to Log in from anywhere (the Welcome keeps its first level
    /// under it).
    func showLogin() {
        problem = nil
        if let i = path.firstIndex(of: .login) { path.removeSubrange((i + 1)...) } else { path = [.login] }
    }

    // MARK: Links and addresses

    /// A scanned QR code or a pasted link: an enrollment asks first, an
    /// invite is checked, an address is looked up.
    func use(link text: String, allowAddress: Bool = true) async {
        problem = nil
        let link: SignInLink
        do { link = try SignInLink(text, allowAddress: allowAddress) } catch {
            problem = ConnectProblem(error, server: nil)
            return
        }
        switch link {
        case .enroll(let s, let c):
            pendingEnroll = PendingEnroll(server: s, code: c)
            askEnroll = true
        case .invite(let s, let t): await checkInvite(server: s, token: t)
        case .address(let s):
            address = s.origin
            await lookUp(s)
        }
    }

    /// Enter workspace address → Continue.
    func lookUp(address text: String) async {
        problem = nil
        do { await lookUp(try ServerOrigin(userInput: text)) } catch {
            problem = ConnectProblem(error, server: nil)
        }
    }

    /// `GET /api/xbin/login/methods`, then the sign-in page for what it
    /// offers.
    func lookUp(_ s: ServerOrigin) async {
        await run("Looking up \(s.authority)…", server: s) {
            let m = try await enrollment.loginMethods(server: s)
            path.append(.methods(server: s, methods: m))
        }
    }

    // MARK: Signing in

    func passwordSignIn(server s: ServerOrigin, username: String, password: String) async {
        await run("Signing in…", server: s) {
            let session = try await enrollment.passwordLogin(server: s, username: username, password: password)
            try await enrollThisDevice(server: s, session: session, password: password)
        }
    }

    /// After a password, invite or SSO sign-in: mint a code with that
    /// session, enroll, and add the workspace.
    func enrollThisDevice(server s: ServerOrigin, session: SessionCredential, password: String? = nil) async throws {
        busy = "Adding this device…"
        let (rec, dev) = try await enrollment.enrollSignedIn(server: s, session: session, deviceName: AppInfo.deviceName,
                                                             password: password)
        await AppModel.shared.add(rec, session: dev, in: scene, replacing: replacing)
    }

    /// The QR code's path, after "Add": probe, redeem, sign in.
    func enroll(_ p: PendingEnroll) async {
        await run("Adding \(p.server.authority)…", server: p.server) {
            let (rec, dev) = try await enrollment.enrollChecked(server: p.server, code: p.code, deviceName: AppInfo.deviceName)
            await AppModel.shared.add(rec, session: dev, in: scene, replacing: replacing)
        }
    }

    // MARK: Invites

    func checkInvite(server s: ServerOrigin, token t: String) async {
        await run("Checking the invite…", server: s) {
            do {
                let info = try await enrollment.checkInvite(server: s, invite: t)
                path.append(.inviteAccept(server: s, token: t, info: info))
            } catch Enrollment.Failure.inviteNeedsBrowser {
                path.append(.inviteInBrowser(server: s, token: t))
            }
        }
    }

    func joinInvite(server s: ServerOrigin, token t: String, info: InviteInfo, password: String) async {
        await run("Joining \(info.title.isEmpty ? s.authority : info.title)…", server: s) {
            let session = try await enrollment.redeemInvite(server: s, invite: t, password: password)
            do {
                try await enrollThisDevice(server: s, session: session, password: password)
            } catch {
                // The invite is spent and the password set: say so, and how
                // to go on.
                let p = ConnectProblem(error, server: s)
                throw ConnectProblem.other("Your password is set, but adding this device failed: \(p.title.lowercased()). "
                    + "\(p.message) Then log in as \(info.userID) with the new password.")
            }
        }
    }

    /// The invite link as the browser opens it.
    static func inviteURL(server s: ServerOrigin, token t: String) -> URL? {
        s.url(path: "/login?invite=" + URLComponent.encode(t))
    }
}

// MARK: - The stacks

/// No workspace yet: the Welcome, full screen.
struct WelcomeView: View {
    @Environment(SceneModel.self) private var scene
    @Environment(AppModel.self) private var app
    @State private var flow = OnboardingFlow(scene: nil)

    var body: some View {
        OnboardingStack(flow: flow) { WelcomeLevel(flow: flow) }
            .onAppear { flow.scene = scene }
            .overlay(alignment: .topLeading) {
                // (Debug fresh starts: the UI tests wait for this to go.)
                if app.cleaningUp {
                    Text(verbatim: " ").font(.caption2).accessibilityIdentifier("xbin-fresh-cleanup")
                }
            }
    }
}

/// The add-workspace sheet: Log in first (the switcher's "Add a
/// workspace", "Sign in again", an `xbin://enroll` link from outside).
struct AddWorkspaceSheet: View {
    @Environment(SceneModel.self) private var scene
    @State private var flow: OnboardingFlow

    init(request: AddRequest) {
        _flow = State(initialValue: OnboardingFlow(scene: nil, request: request))
    }

    var body: some View {
        OnboardingStack(flow: flow) {
            LoginLevel(flow: flow)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel") { scene.addRequest = nil } }
                }
        }
        .onAppear { flow.scene = scene }
    }
}

/// A NavigationStack over the flow's path, with the busy overlay, the
/// enrollment question and every page as a destination.
struct OnboardingStack<Root: View>: View {
    @Bindable var flow: OnboardingFlow
    @ViewBuilder let root: () -> Root

    var body: some View {
        NavigationStack(path: $flow.path) {
            root()
                .concreteBackground()
                .navigationDestination(for: OnboardingStep.self) { step in OnboardingPage(flow: flow, step: step).concreteBackground() }
        }
        .disabled(flow.busy != nil)
        .overlay {
            if let busy = flow.busy {
                ProgressView(busy)
                    .padding(20)
                    .background(XbinColor.surface, in: .xbinPlate)
                    .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.borderStrong, lineWidth: 1))
            }
        }
        .onChange(of: flow.path) { _, _ in flow.problem = nil }
        .confirmationDialog("Add this workspace?", isPresented: $flow.askEnroll, titleVisibility: .visible) {
            if let p = flow.pendingEnroll {
                Button("Add \(p.server.authority)") {
                    flow.pendingEnroll = nil
                    Task { await flow.enroll(p) }
                }
            }
            Button("Cancel", role: .cancel) { flow.pendingEnroll = nil }
        } message: {
            Text(verbatim: "This device gets a key that signs you in to \(flow.pendingEnroll?.server.origin ?? "the workspace") with Face ID.")
        }
    }
}

/// One page of the stack.
struct OnboardingPage: View {
    let flow: OnboardingFlow
    let step: OnboardingStep

    var body: some View {
        switch step {
        case .login: LoginLevel(flow: flow)
        case .address: AddressPage(flow: flow)
        case .methods(let s, let m): MethodsPage(flow: flow, server: s, methods: m)
        case .invite: InvitePage(flow: flow)
        case .inviteAccept(let s, let t, let info): InviteAcceptPage(flow: flow, server: s, token: t, info: info)
        case .inviteInBrowser(let s, let t): InviteInBrowserPage(flow: flow, server: s, token: t)
        case .runYourOwn: RunYourOwnPage(flow: flow)
        case .about(let n): AboutPage(flow: flow, page: n)
        case .help(let n): HelpPage(flow: flow, page: n)
        }
    }
}

// MARK: - The first level

/// The Welcome's first level (D185): the lockup, the first run's stair —
/// the one place it appears in the product (product-ui 10) — over the
/// definition, then four ways in.
struct WelcomeLevel: View {
    let flow: OnboardingFlow

    /// The definition, verbatim (plans/brand.md 1).
    static let definition = "xbin is a workspace where people and AI agents build the systems a company runs on, and where those systems run."

    var body: some View {
        // Centred on the screen; it scrolls when large text makes it taller.
        GeometryReader { geo in
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    XbinLockup(height: 36)
                    VStack(alignment: .leading, spacing: 16) {
                        WelcomeStair()
                        Text(verbatim: Self.definition)
                            .font(.title3)
                            .foregroundStyle(XbinColor.muted)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    VStack(spacing: 10) {
                        WelcomeButton(title: "Log in", symbol: "person.crop.square", prominent: true) { flow.push(.login) }
                        WelcomeButton(title: "Join with an invite", symbol: "envelope.open") { flow.push(.invite) }
                        WelcomeButton(title: "Run your own xbin", symbol: "server.rack") { flow.push(.runYourOwn) }
                        WelcomeButton(title: "What is xbin?", symbol: "info.square") { flow.push(.about(page: 1)) }
                    }
                }
                .frame(maxWidth: 420, alignment: .leading)
                .padding(.horizontal, 24)
                .padding(.vertical, 32)
                .frame(maxWidth: .infinity, minHeight: geo.size.height)
            }
        }
        .background(XbinColor.background)
        .toolbar(.hidden, for: .navigationBar)
    }
}

/// The stair (plans/brand.md 5.4): "Your apps on your phone." set as type
/// that grows line by line, each line on its field in the doubling order —
/// yellow, green, magenta — bleeding to the screen's leading edge. On a
/// phone the width sets the steps (×1, ×1.5, then a doubling: ×3). One
/// heading for VoiceOver; it shrinks, never wraps, when large text would
/// overflow a line.
struct WelcomeStair: View {
    @ScaledMetric(relativeTo: .largeTitle) private var unit: CGFloat = 26

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            step("Your apps", size: unit, field: XbinPalette.Field.yellow, ink: XbinPalette.Field.yellowInk)
            step("on your", size: unit * 1.5, field: XbinPalette.Field.green, ink: XbinPalette.Field.greenInk)
            step("phone.", size: unit * 3, field: XbinPalette.Field.magenta, ink: XbinPalette.Field.magentaInk)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(verbatim: "Your apps on your phone."))
        .accessibilityAddTraits(.isHeader)
    }

    private func step(_ text: String, size: CGFloat, field: UInt32, ink: UInt32) -> some View {
        Text(verbatim: text)
            .font(XbinFont.display(.largeTitle, size: size))
            .tracking(-0.03 * size)
            .lineLimit(1)
            .minimumScaleFactor(0.4)
            .foregroundStyle(Color(xbinHex: ink))
            .padding(.vertical, size * 0.06)
            .padding(.trailing, size * 0.3)
            // The field bleeds to the leading edge past the page margin.
            .padding(.leading, 24)
            .background(Color(xbinHex: field))
            .padding(.leading, -24)
    }
}

/// A full-width button of the first level: Base Two's controls — square
/// corners, the accent fill for the one primary action, the panel with the
/// strong edge for the rest.
struct WelcomeButton: View {
    let title: String
    let symbol: String
    var prominent = false
    let action: () -> Void

    var body: some View {
        if prominent {
            Button(action: action) { label }
                .xbinPrimary()
        } else {
            Button(action: action) { label }
                .buttonStyle(.plain)
                .foregroundStyle(XbinColor.text)
                .background(XbinColor.surface, in: .xbinPlate)
                .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.borderStrong, lineWidth: 1))
        }
    }

    private var label: some View {
        Label(title, systemImage: symbol)
            .font(.headline)
            .frame(maxWidth: .infinity, minHeight: 44)
            .contentShape(Rectangle())
    }
}

// MARK: - Run your own xbin

struct RunYourOwnPage: View {
    let flow: OnboardingFlow
    @Environment(\.openURL) private var openURL
    @State private var copied = false

    static let install = "curl -fsSL https://xbin.dev/install.sh | sh"

    var body: some View {
        Form {
            Section {
                Text("xbin is one program. It runs on a Linux machine: a server, a virtual machine or a spare computer. "
                    + "It serves the workspace to browsers and to this app.")
            }
            Section {
                Text(verbatim: Self.install)
                    .font(XbinFont.mono(.callout))
                    .lineLimit(1)
                    .minimumScaleFactor(0.6)
                    .textSelection(.enabled)
                Button(copied ? "Copied" : "Copy the command", systemImage: copied ? "checkmark" : "doc.on.doc") {
                    UIPasteboard.general.string = Self.install
                    copied = true
                }
            } header: { Text("Install") } footer: {
                Text("Run it in the machine's terminal. The installer prints its plan and asks before it changes anything.")
            }
            Section("Requirements") {
                Label("Linux on x86-64 or arm64. On a Mac, the installer sets up a Lima VM.", systemImage: "desktopcomputer")
                Label("An address this phone can reach: an https address, or the same network or VPN.", systemImage: "network")
            }
            Section {
                Button("Open xbin.dev", systemImage: "safari") {
                    if let u = URL(string: "https://xbin.dev") { openURL(u) }
                }
                Button("Log in", systemImage: "person.crop.circle") { flow.showLogin() }
            } footer: {
                Text("Once it runs, sign in to it in a browser, then come back and log in.")
            }
        }
        .navigationTitle("Run your own xbin")
        .navigationBarTitleDisplayMode(.inline)
    }
}

// MARK: - What is xbin?

struct AboutPage: View {
    let flow: OnboardingFlow
    let page: Int
    @Environment(\.openURL) private var openURL

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                Image(systemName: symbol).font(.system(size: 40)).foregroundStyle(XbinColor.text)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, 24)
                Text(title).font(XbinFont.display(.title)).tracking(-0.4)
                ForEach(Array(paragraphs.enumerated()), id: \.offset) { _, p in
                    Text(p).font(.body)
                }
                if page == 3 {
                    Button("Privacy policy", systemImage: "hand.raised") {
                        if let u = URL(string: "https://xbin.dev/privacy.html") { openURL(u) }
                    }
                }
                Text(verbatim: "\(page) of 3").font(.footnote).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.top, 8)
                if page < 3 {
                    Button { flow.push(.about(page: page + 1)) } label: {
                        Text("Next").font(.headline).frame(maxWidth: .infinity, minHeight: 34)
                    }
                    .xbinPrimary()
                } else {
                    Button { flow.showLogin() } label: {
                        Text("Log in").font(.headline).frame(maxWidth: .infinity, minHeight: 34)
                    }
                    .xbinPrimary()
                }
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 32)
            .frame(maxWidth: 560)
            .frame(maxWidth: .infinity)
        }
        .navigationTitle("What is xbin?")
        .navigationBarTitleDisplayMode(.inline)
    }

    private var symbol: String {
        switch page {
        case 1: return "square.grid.2x2"
        case 2: return XbinGlyphs.symbol("agent")
        default: return "lock"
        }
    }

    // The voice (plans/brand.md 3): the statement, then a plain sentence;
    // agents are software that builds and changes apps; nothing says where
    // xbin runs.
    private var title: String {
        switch page {
        case 1: return "The systems a company runs on"
        case 2: return "Agents build, people approve"
        default: return "Yours, and private"
        }
    }

    private var paragraphs: [String] {
        switch page {
        case 1:
            return [WelcomeLevel.definition,
                    "Each app is a folder with its own sandbox, identity, grants and network rules.",
                    "You use it in a browser on a computer, and on your phone with this app."]
        case 2:
            return ["Point an agent at an app and it changes the code, tests it and saves it. The app is live once it saves.",
                    "Agents run in terminals and sessions inside the workspace, and ask for a grant before they reach anything new.",
                    "This app shows what needs you, and you answer from where you are."]
        default:
            return ["Your workspace keeps its apps and files. This app connects only to the workspaces you add.",
                    "It collects nothing: no analytics, no tracking. Every few hours it reads a static file from xbin.dev "
                    + "that can switch off native views; the request carries no identifier.",
                    "One exception, and only if you turn it on: push notifications pass through a relay the xbin "
                    + "project runs. It keeps this device's push token and random handles, never names, addresses "
                    + "or what the notifications say."]
        }
    }
}

// MARK: - Where do I find the QR code?

struct HelpPage: View {
    let flow: OnboardingFlow
    let page: Int

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text(title).font(XbinFont.display(.title2)).tracking(-0.3).padding(.top, 16)
                Text(text)
                if let image {
                    HelpImage(name: image, caption: caption)
                }
                Text(verbatim: "\(page) of 3").font(.footnote).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
                if page < 3 {
                    Button { flow.push(.help(page: page + 1)) } label: {
                        Text("Next").font(.headline).frame(maxWidth: .infinity, minHeight: 34)
                    }
                    .xbinPrimary()
                } else {
                    Button { flow.showLogin() } label: {
                        Text("Back to Log in").font(.headline).frame(maxWidth: .infinity, minHeight: 34)
                    }
                    .xbinPrimary()
                }
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 32)
            .frame(maxWidth: 560)
            .frame(maxWidth: .infinity)
        }
        .navigationTitle("Where's the QR code?")
        .navigationBarTitleDisplayMode(.inline)
    }

    private var title: String {
        switch page {
        case 1: return "1. Open settings"
        case 2: return "2. Add a device"
        default: return "3. Scan it"
        }
    }

    private var text: String {
        switch page {
        case 1:
            return "On a computer, open your workspace in a browser where you're signed in. Click settings at the top right."
        case 2:
            return "Choose add a device, at the top of the menu. A QR code appears. It works once, for 5 minutes."
        default:
            return "In this app, tap Log in, then Scan QR code, and point the camera at the code. No camera? Copy "
                + "the link under the code and paste it on Log in.\n\nIf your browser reaches xbin through a tunnel "
                + "or a proxy, first set \"address your phone uses\" in the same panel to an address this phone can "
                + "reach, such as the workspace's https address."
        }
    }

    /// The web shell's screenshots (App/Resources/Help, made by the UI
    /// harness from the real shell).
    private var image: String? {
        switch page {
        case 1: return "help-1-settings"
        case 2: return "help-2-add-device"
        default: return nil
        }
    }

    private var caption: String {
        page == 1 ? "The settings menu, with add a device at the top" : "The add-device panel with its QR code"
    }
}

/// A help screenshot from the app's resources; nothing when it's missing.
struct HelpImage: View {
    let name: String
    let caption: String

    var body: some View {
        if let ui = UIImage(named: name) {
            Image(uiImage: ui)
                .resizable()
                .scaledToFit()
                .clipShape(.xbinPlate)
                .overlay(RoundedRectangle.xbinPlate.strokeBorder(XbinColor.border, lineWidth: 1))
                .frame(maxWidth: .infinity)
                .accessibilityLabel(caption)
        }
    }
}
