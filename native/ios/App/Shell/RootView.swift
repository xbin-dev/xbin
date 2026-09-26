import SwiftUI
import UIKit
import XbinCore

/// The app's root: the selected workspace full screen, the switcher over it
/// (a two-finger swipe down, a tap on the title, ⌘1…⌘9 — never a docked
/// rail, §4), the add-workspace sheet, the lock.
struct RootView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        @Bindable var app = app
        ZStack {
            if let w = app.selected {
                WorkspaceView(workspace: w)
                    .id(w.id)
            } else {
                Welcome()
            }
            if app.showSwitcher {
                SwitcherOverlay()
                    .transition(.move(edge: .top).combined(with: .opacity))
                    .zIndex(1)
            }
            if app.locked {
                LockView().zIndex(2)
            }
        }
        .animation(.snappy, value: app.showSwitcher)
        .background(TwoFingerSwipeDown { withAnimation { app.showSwitcher = true } })
        .background { WorkspaceShortcuts() }
        .sheet(item: Binding(get: { app.addRequest.map(AddRequestBox.init) }, set: { app.addRequest = $0?.request })) { box in
            AddWorkspaceView(request: box.request)
        }
        .sheet(isPresented: $app.showInbox) { InboxView() }
        .sheet(isPresented: $app.showSettings) { SettingsView() }
    }
}

/// `.sheet(item:)` needs Identifiable.
struct AddRequestBox: Identifiable {
    let request: AddRequest
    var id: String {
        switch request {
        case .blank: return "blank"
        case .enroll(let s, let c): return "\(s.origin)|\(c)"
        }
    }
}

/// ⌘1…⌘9 jump straight to a workspace (hardware keyboards).
private struct WorkspaceShortcuts: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        ZStack {
            ForEach(0..<9, id: \.self) { i in
                Button("") { app.select(index: i) }
                    .keyboardShortcut(KeyEquivalent(Character("\(i + 1)")), modifiers: .command)
            }
            Button("") { withAnimation { app.showSwitcher.toggle() } }
                .keyboardShortcut("k", modifiers: [.command, .shift])
        }
        .opacity(0)
        .accessibilityHidden(true)
    }
}

/// No workspace yet.
private struct Welcome: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        ContentUnavailableView {
            Label("An office for your agents", systemImage: "square.grid.2x2")
        } description: {
            Text("Add a workspace: scan the QR code from your workspace's account menu (Devices → Add a device), or sign in with its address.")
        } actions: {
            Button("Add a workspace") { app.addRequest = .blank }.buttonStyle(.borderedProminent)
        }
    }
}

/// One workspace: its current surface full screen, or the navigator as home.
struct WorkspaceView: View {
    @Bindable var workspace: WorkspaceModel
    @Environment(AppModel.self) private var app

    var body: some View {
        NavigationStack(path: $workspace.windows) {
            Group {
                if let s = workspace.surface {
                    surface(s)
                } else {
                    NavigatorView(workspace: workspace)
                }
            }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { withAnimation { app.showSwitcher = true } } label: {
                        HStack(spacing: 6) {
                            BrandIcon(workspace: workspace, size: 22)
                            Text(verbatim: workspace.title).font(.headline).lineLimit(1)
                            if app.needsYouCount > 0 {
                                Text(verbatim: "\(app.needsYouCount)").font(.caption2.bold())
                                    .padding(.horizontal, 5).padding(.vertical, 1)
                                    .background(Color.xbinAmber, in: Capsule()).foregroundStyle(.black)
                            }
                        }
                    }
                    .accessibilityLabel("Workspaces")
                }
                if workspace.surface != nil {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { workspace.showNavigator = true } label: { Image(systemName: "square.grid.2x2") }
                            .accessibilityLabel("Tiles")
                    }
                }
            }
            .navigationDestination(for: PushedWindow.self) { w in WindowScreen(workspace: workspace, window: w) }
        }
        .sheet(isPresented: $workspace.showNavigator) {
            NavigationStack { NavigatorView(workspace: workspace, overlay: true) }
                .presentationDetents([.medium, .large])
        }
        .overlay(alignment: .bottom) {
            if let p = workspace.signInProblem {
                SignInProblemBar(workspace: workspace, problem: p)
            }
        }
        .task { if workspace.whoami == nil { await workspace.refresh() } }
        .onChange(of: workspace.surface) { _, s in
            if let s { app.visited(workspace, s, title: s.title) }
        }
    }

    @ViewBuilder private func surface(_ s: Surface) -> some View {
        switch s {
        case .tile(let path, let sub, let fragment):
            TileScreen(workspace: workspace, path: path, subpath: sub, fragment: fragment)
                .id(path + "|" + sub)
        case .terminal(let cwd, let session):
            TerminalScreen(workspace: workspace, cwd: cwd, sessionID: session)
                .id("term|" + cwd + "|" + (session ?? ""))
        case .agent(let cwd, let session):
            AgentScreen(workspace: workspace, cwd: cwd, sessionID: session)
                .id("agent|" + (cwd ?? "") + "|" + (session ?? ""))
        }
    }
}

/// Signing in needs the user: re-enroll, SSO, or just try again.
private struct SignInProblemBar: View {
    let workspace: WorkspaceModel
    let problem: SignInError
    @Environment(AppModel.self) private var app

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(verbatim: problem.description).font(.footnote)
            HStack {
                if problem.needsEnrollment || problem == .ssoRequired {
                    Button("Sign in again") { app.addRequest = .blank }.buttonStyle(.borderedProminent)
                } else {
                    Button("Try again") { Task { await workspace.signIn() } }.buttonStyle(.borderedProminent)
                }
                Button("Dismiss") { workspace.signInProblem = nil }
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.regularMaterial)
    }
}

/// The workspace's branding icon (D76), or its initial.
struct BrandIcon: View {
    let workspace: WorkspaceModel
    var size: CGFloat = 28

    var body: some View {
        let title = workspace.title
        ZStack {
            RoundedRectangle(cornerRadius: size * 0.25).fill(Color.xbinAmber.opacity(0.9))
            if let icon = workspace.record.branding?.icon, icon.count <= 4, !icon.isEmpty {
                Text(verbatim: icon).font(.system(size: size * 0.6))
            } else {
                Text(verbatim: String(title.prefix(1)).uppercased())
                    .font(.system(size: size * 0.55, weight: .bold)).foregroundStyle(.black)
            }
        }
        .frame(width: size, height: size)
    }
}

/// "Require Face ID when opening the app".
private struct LockView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        ZStack {
            Rectangle().fill(.background).ignoresSafeArea()
            VStack(spacing: 16) {
                Image(systemName: "lock.fill").font(.largeTitle)
                Button("Unlock") { Task { await app.unlock() } }.buttonStyle(.borderedProminent)
            }
        }
    }
}

/// A two-finger swipe down anywhere opens the switcher: a recognizer on
/// the window, alongside everything else (web views and the terminal keep
/// their own gestures).
struct TwoFingerSwipeDown: UIViewRepresentable {
    let action: () -> Void

    func makeUIView(context: Context) -> Installer { Installer(action: action) }
    func updateUIView(_ uiView: Installer, context: Context) { uiView.action = action }

    final class Installer: UIView, UIGestureRecognizerDelegate {
        var action: () -> Void
        private var recognizer: UISwipeGestureRecognizer?

        init(action: @escaping () -> Void) {
            self.action = action
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if let r = recognizer { r.view?.removeGestureRecognizer(r) }
            guard let window else { return }
            let r = UISwipeGestureRecognizer(target: self, action: #selector(fire))
            r.direction = .down
            r.numberOfTouchesRequired = 2
            r.cancelsTouchesInView = false
            r.delegate = self
            window.addGestureRecognizer(r)
            recognizer = r
        }

        @objc private func fire() { action() }

        func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
            true
        }
    }
}
