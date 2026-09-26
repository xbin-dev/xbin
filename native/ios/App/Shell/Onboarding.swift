import SwiftUI
import UIKit
import XbinCore

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
    @State private var flow = OnboardingFlow(scene: nil)

    var body: some View {
        OnboardingStack(flow: flow) { WelcomeLevel(flow: flow) }
            .onAppear { flow.scene = scene }
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
                .navigationDestination(for: OnboardingStep.self) { step in OnboardingPage(flow: flow, step: step) }
        }
        .disabled(flow.busy != nil)
        .overlay {
            if let busy = flow.busy {
                ProgressView(busy)
                    .padding(20)
                    .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
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

/// The Welcome's first level: the mark and four ways in.
struct WelcomeLevel: View {
    let flow: OnboardingFlow

    var body: some View {
        ScrollView {
            VStack(spacing: 28) {
                VStack(spacing: 14) {
                    XbinMark().frame(width: 96, height: 96)
                        .shadow(color: .black.opacity(0.18), radius: 10, y: 4)
                    Text(verbatim: "xbin").font(.largeTitle.bold())
                    Text("An office for your agents.").font(.title3).foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .padding(.top, 48)
                VStack(spacing: 12) {
                    WelcomeButton(title: "Log in", symbol: "person.crop.circle", prominent: true) { flow.push(.login) }
                    WelcomeButton(title: "Join with an invite", symbol: "envelope.open") { flow.push(.invite) }
                    WelcomeButton(title: "Run your own xbin", symbol: "server.rack") { flow.push(.runYourOwn) }
                    WelcomeButton(title: "What is xbin?", symbol: "questionmark.circle") { flow.push(.about(page: 1)) }
                }
                .frame(maxWidth: 380)
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 32)
            .frame(maxWidth: .infinity)
        }
        .toolbar(.hidden, for: .navigationBar)
    }
}

/// A full-width button of the first level.
struct WelcomeButton: View {
    let title: String
    let symbol: String
    var prominent = false
    let action: () -> Void

    var body: some View {
        if prominent {
            Button(action: action) { label }.buttonStyle(.borderedProminent).foregroundStyle(.black)
        } else {
            Button(action: action) { label }.buttonStyle(.bordered)
        }
    }

    private var label: some View {
        Label(title, systemImage: symbol)
            .font(.headline)
            .frame(maxWidth: .infinity, minHeight: 34)
    }
}

/// The xbin mark (web/favicon.svg, the app icon's layers): the amber plate
/// with its chamfered corners and rivets, the charcoal X.
struct XbinMark: View {
    var body: some View {
        ZStack {
            MarkPlate().fill(Color.xbinAmber)
            MarkX().fill(Color(red: 0x23 / 255, green: 0x27 / 255, blue: 0x2E / 255))
            MarkRivets().fill(Color(red: 0x23 / 255, green: 0x27 / 255, blue: 0x2E / 255).opacity(0.4))
        }
        .accessibilityHidden(true)
    }
}

/// The icon's geometry: plate.svg / x.svg, whose plate spans 148…876 of a
/// 1024 canvas — scaled so the plate fills the rect.
private func markPoint(_ x: CGFloat, _ y: CGFloat, _ r: CGRect) -> CGPoint {
    CGPoint(x: r.minX + (x - 148) / 728 * r.width, y: r.minY + (y - 148) / 728 * r.height)
}

struct MarkPlate: Shape {
    func path(in r: CGRect) -> Path {
        var p = Path()
        let k = r.width / 728
        p.move(to: markPoint(330, 148, r))
        p.addLine(to: markPoint(824, 148, r))
        p.addArc(center: markPoint(824, 200, r), radius: 52 * k, startAngle: .degrees(-90), endAngle: .degrees(0), clockwise: false)
        p.addLine(to: markPoint(876, 694, r))
        p.addLine(to: markPoint(694, 876, r))
        p.addLine(to: markPoint(200, 876, r))
        p.addArc(center: markPoint(200, 824, r), radius: 52 * k, startAngle: .degrees(90), endAngle: .degrees(180), clockwise: false)
        p.addLine(to: markPoint(148, 330, r))
        p.closeSubpath()
        return p
    }
}

struct MarkX: Shape {
    func path(in r: CGRect) -> Path {
        let pts: [(CGFloat, CGFloat)] = [(327.63, 410.37), (410.37, 327.63), (512, 429.27), (613.63, 327.63), (696.37, 410.37),
                                         (594.73, 512), (696.37, 613.63), (613.63, 696.37), (512, 594.73), (410.37, 696.37),
                                         (327.63, 613.63), (429.27, 512)]
        var p = Path()
        for (i, (x, y)) in pts.enumerated() {
            if i == 0 { p.move(to: markPoint(x, y, r)) } else { p.addLine(to: markPoint(x, y, r)) }
        }
        p.closeSubpath()
        return p
    }
}

struct MarkRivets: Shape {
    func path(in r: CGRect) -> Path {
        var p = Path()
        let d = 60 / 728 * r.width
        for (x, y) in [(CGFloat(785), CGFloat(239)), (239, 785)] {
            let c = markPoint(x, y, r)
            p.addEllipse(in: CGRect(x: c.x - d / 2, y: c.y - d / 2, width: d, height: d))
        }
        return p
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
                Text("xbin is one program. Run it on a Linux machine you control: a server, a VPS or a spare computer. "
                    + "It serves your workspace to browsers and to this app.")
            }
            Section {
                Text(verbatim: Self.install)
                    .font(.system(.callout, design: .monospaced))
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
                Image(systemName: symbol).font(.system(size: 44)).foregroundStyle(Color.xbinAmber)
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.top, 24)
                Text(title).font(.title.bold())
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
                    .buttonStyle(.borderedProminent).foregroundStyle(.black)
                } else {
                    Button { flow.showLogin() } label: {
                        Text("Log in").font(.headline).frame(maxWidth: .infinity, minHeight: 34)
                    }
                    .buttonStyle(.borderedProminent).foregroundStyle(.black)
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
        case 2: return "terminal"
        default: return "lock.shield"
        }
    }

    private var title: String {
        switch page {
        case 1: return "An office for your agents"
        case 2: return "Agents at work"
        default: return "Yours, and private"
        }
    }

    private var paragraphs: [String] {
        switch page {
        case 1:
            return ["xbin is a workspace of apps that you, your team and your agents build and use together.",
                    "Each app runs in its own sandbox, with an identity, grants and network policy.",
                    "You use it in a browser on a computer, and on your phone with this app."]
        case 2:
            return ["Point an agent at an app, and it edits, tests and commits the change. The app is live right away.",
                    "Agents work in terminals and sessions inside the workspace.",
                    "This app shows what needs you, and you answer from wherever you are."]
        default:
            return ["xbin runs on a machine that you or your organisation control. Your workspace, its apps and "
                    + "files stay there.",
                    "This app connects only to the workspaces you add. It collects nothing: no analytics, no tracking. "
                    + "Every few hours it reads a static file from xbin.dev that can switch off native views; the "
                    + "request carries no identifier.",
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
                Text(title).font(.title2.bold()).padding(.top, 16)
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
                    .buttonStyle(.borderedProminent).foregroundStyle(.black)
                } else {
                    Button { flow.showLogin() } label: {
                        Text("Back to Log in").font(.headline).frame(maxWidth: .infinity, minHeight: 34)
                    }
                    .buttonStyle(.borderedProminent).foregroundStyle(.black)
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
                .clipShape(RoundedRectangle(cornerRadius: 12))
                .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.quaternary, lineWidth: 1))
                .frame(maxWidth: .infinity)
                .accessibilityLabel(caption)
        }
    }
}
