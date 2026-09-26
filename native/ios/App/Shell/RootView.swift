import SwiftUI
import UIKit
import XbinCore

/// One window's root: its workspace full screen, the switcher over it (a
/// two-finger swipe down, a tap on the title, ⌘1…⌘9 — never a docked rail,
/// §4), the add-workspace sheet, the lock. Every window (iPad, Stage
/// Manager, the Mac) has its own selection and navigation — windows are
/// tabs — kept across launches in `@SceneStorage`; a window opened with
/// `openWindow(value:)` (a dragged tile, "Open in New Window") starts on
/// that value.
struct RootView: View {
    /// The window's `WindowGroup(for:)` value (nil for the first window).
    @Binding var target: WindowTarget?

    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var phase
    @State private var scene = SceneModel()
    @State private var restored = false
    @SceneStorage("xbin.workspace") private var storedWorkspace = ""
    @SceneStorage("xbin.surface") private var storedTarget = ""

    var body: some View {
        @Bindable var scene = scene
        ZStack {
            if let w = scene.selected {
                WorkspaceView(workspace: w, nav: scene.nav(for: w))
                    .id(w.id)
            } else {
                Welcome()
            }
            if scene.showSwitcher {
                SwitcherOverlay()
                    .transition(.move(edge: .top).combined(with: .opacity))
                    .zIndex(1)
            }
            if app.locked {
                LockView().zIndex(2)
            }
        }
        .environment(scene)
        .animation(.snappy, value: scene.showSwitcher)
        .background(TwoFingerSwipeDown { withAnimation { scene.showSwitcher = true } })
        .background { WorkspaceShortcuts() }
        .background(KeyWindowReporter { app.focus(scene) })
        .sheet(item: $scene.addRequest) { request in
            AddWorkspaceView(request: request).environment(scene)
        }
        .sheet(isPresented: $scene.showInbox) { InboxView().environment(scene) }
        .sheet(isPresented: $scene.showSettings) { SettingsView().environment(scene) }
        // Links and Handoff go to a window already open (the one in front)
        // rather than making a new one; a dragged tile still makes its own.
        .handlesExternalEvents(preferring: ["*"], allowing: ["*"])
        .onOpenURL { scene.open(url: $0) }
        .onContinueUserActivity(HandoffActivity.type) { activity in
            restored = true
            if let link = HandoffActivity.link(activity) { scene.open(link: link) }
        }
        .userActivity(HandoffActivity.type, isActive: AppSettings.handoff && scene.selected != nil) { activity in
            if let w = scene.selected { HandoffActivity.fill(activity, w, scene.existingNav(w.id)?.surface) }
        }
        .onAppear(perform: appeared)
        .onChange(of: phase) { _, p in phaseChanged(p) }
        .onChange(of: scene.current) { _, t in remember(t) }
    }

    private func appeared() {
        if app.register(scene) { restored = true }
        phaseChanged(phase)
        guard !restored else { return }
        restored = true
        // This window's own last place, else the value it was opened with,
        // else where the user was last.
        if let t = WindowTarget(encoded: storedTarget), app.workspace(t.workspace) != nil {
            scene.show(t)
        } else if !storedWorkspace.isEmpty, app.workspace(storedWorkspace) != nil {
            scene.select(storedWorkspace)
        } else if let t = target, app.workspace(t.workspace) != nil {
            scene.show(t)
        } else if let id = app.lastSelectedID ?? app.workspaces.first?.id {
            scene.select(id)
        }
    }

    private func phaseChanged(_ p: ScenePhase) {
        scene.isForeground = p != .background
        if p == .active { app.focus(scene) }
        app.updateSockets()
    }

    private func remember(_ t: WindowTarget?) {
        storedWorkspace = t?.workspace ?? ""
        storedTarget = t?.encoded ?? ""
        // The window's value follows what it shows, so "open in a new
        // window" for a place already open brings that window forward.
        if target != t { target = t }
    }
}

/// ⌘1…⌘9 jump straight to a workspace (hardware keyboards).
private struct WorkspaceShortcuts: View {
    @Environment(SceneModel.self) private var scene

    var body: some View {
        ZStack {
            ForEach(0..<9, id: \.self) { i in
                Button("") { scene.select(index: i) }
                    .keyboardShortcut(KeyEquivalent(Character("\(i + 1)")), modifiers: .command)
            }
            Button("") { withAnimation { scene.showSwitcher.toggle() } }
                .keyboardShortcut("k", modifiers: [.command, .shift])
        }
        .opacity(0)
        .accessibilityHidden(true)
    }
}

/// No workspace yet.
private struct Welcome: View {
    @Environment(SceneModel.self) private var scene

    var body: some View {
        ContentUnavailableView {
            Label("An office for your agents", systemImage: "square.grid.2x2")
        } description: {
            Text("Add a workspace: scan the QR code from your workspace's account menu (Devices → Add a device), or sign in with its address.")
        } actions: {
            Button("Add a workspace") { scene.addRequest = .blank }.buttonStyle(.borderedProminent)
        }
    }
}

/// One workspace in one window: its current surface full screen, or the
/// navigator as home. The window's navigation is in the environment for
/// the screens under it (`@Environment(WorkspaceNav.self)`).
struct WorkspaceView: View {
    let workspace: WorkspaceModel
    @Bindable var nav: WorkspaceNav
    @Environment(AppModel.self) private var app
    @Environment(SceneModel.self) private var scene

    var body: some View {
        NavigationStack(path: $nav.windows) {
            Group {
                if let s = nav.surface {
                    surface(s)
                } else {
                    NavigatorView(workspace: workspace)
                }
            }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { withAnimation { scene.showSwitcher = true } } label: {
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
                if nav.surface != nil {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { nav.showNavigator = true } label: { Image(systemName: "square.grid.2x2") }
                            .accessibilityLabel("Tiles")
                    }
                }
            }
            .navigationDestination(for: PushedWindow.self) { w in WindowScreen(workspace: workspace, window: w) }
        }
        .environment(nav)
        .sheet(isPresented: $nav.showNavigator) {
            NavigationStack { NavigatorView(workspace: workspace, overlay: true) }
                .presentationDetents([.medium, .large])
                .environment(nav)
                .environment(scene)
        }
        .overlay(alignment: .bottom) {
            if let p = workspace.signInProblem {
                SignInProblemBar(workspace: workspace, problem: p)
            }
        }
        .task { if workspace.whoami == nil { await workspace.refresh() } }
        .onChange(of: nav.surface) { _, s in
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
    @Environment(SceneModel.self) private var scene

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(verbatim: problem.description).font(.footnote)
            HStack {
                if problem.needsEnrollment || problem == .ssoRequired {
                    Button("Sign in again") { scene.addRequest = .blank }.buttonStyle(.borderedProminent)
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

/// Tells its window's model when the window becomes key (the user is in
/// it): code outside a window then acts on this one (AppModel.focus).
struct KeyWindowReporter: UIViewRepresentable {
    let becameKey: () -> Void

    func makeUIView(context: Context) -> Reporter { Reporter(becameKey: becameKey) }
    func updateUIView(_ uiView: Reporter, context: Context) { uiView.becameKey = becameKey }

    final class Reporter: UIView {
        var becameKey: () -> Void

        init(becameKey: @escaping () -> Void) {
            self.becameKey = becameKey
            super.init(frame: .zero)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        // A selector observer: NotificationCenter drops it with the view.
        override func didMoveToWindow() {
            super.didMoveToWindow()
            NotificationCenter.default.removeObserver(self, name: UIWindow.didBecomeKeyNotification, object: nil)
            guard let window else { return }
            NotificationCenter.default.addObserver(self, selector: #selector(keyChanged(_:)),
                                                   name: UIWindow.didBecomeKeyNotification, object: window)
            if window.isKeyWindow { becameKey() }
        }

        @objc private func keyChanged(_ note: Notification) { becameKey() }
    }
}
